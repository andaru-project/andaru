# Durable saved network configuration

## Status and intent

Design for owner review, not implemented behaviour. The owner approved this slice in conversation: canonical JSON snapshots in SQLite; physically separate configuration and configuration-secret stores published as one saved version; schema validation and stable object IDs; per-object optimistic locking; atomic multi-object saves; persistence without activating networking; real database and Debian-lab proof. Contracts remain provisional until Andaru 1.0.

This follows the root-local engine shipped in [PR #24](https://github.com/andaru-project/andaru/pull/24). It gives the CLI and eventual UI a real way to save links, segments and zones. It does not take over interfaces, configure networkd/nft, manufacture a confirmed generation, or expose a network listener.

### Authorities and precedence

- [Issue #6](https://github.com/andaru-project/andaru/issues/6): JSON/JSON Schema, SQLite documents, object model, paired stores, saved-version history, optimistic writes and per-area apply. Its later no-draft ruling supersedes the earlier candidate table. Its API draft corrects stale `If-Match` failures to HTTP `412`, not `409`.
- [Issue #12](https://github.com/andaru-project/andaru/issues/12): configuration and related secrets version together; users, role memberships and user credentials remain outside that history; normal exports omit secret values.
- [Web UI foundation](../../research/web-ui-foundation.md): API-first; saved, live and observed state are different; mockups are not a prerequisite.
- [Traefik decisions](../../research/traefik-manifest-example.md#apply-backups-and-offline-recovery): save/apply separation, secret-free default exports, recovery is not a successful apply.

The full Graphify re-extraction at `91fc0cc` read all 13 local documents. Detailed storage decisions remain in issue #6; graph inference is not a substitute for those rulings.

## Decisions and scope

Keep whole canonical documents as SQLite blobs, with small relational tables for immutable revisions, object versions and the saved head. Do not normalize every network field into SQL columns or create a second JSON-file source of truth. JSON files alone would not supply the selected transaction/history model; normalized domain tables would duplicate the JSON Schema and complicate portable snapshots.

The first declaration contains only `schemaVersion`, `links`, `segments` and `zones`. Unsupported top-level sections are rejected, not accepted and ignored. Firewall rules, routes, DHCP/DNS service configuration, management takeover, roles/users, extension settings, backups, pruning and live-generation/apply APIs are subsequent slices. The four-store architecture is preserved, but unused users/credentials databases and empty API services are not created.

Network save support includes Ethernet, VLAN, bridge, bond and PPPoE link definitions, plus static/dynamic IPv4 and IPv6 addressing intent. PPPoE supplies the first real configuration-secret consumer; supporting its declaration is not a claim that the engine starts PPP sessions. The owner's VLAN/PPPoE and delegated-IPv6 requirements remain part of the product, not silently removed from its model.

## Declaration and identity

Store 1 is a versioned document:

```json
{"schemaVersion":1,"links":[],"segments":[],"zones":[]}
```

Each object has an immutable server-generated ID, a mutable `name`, and optional `description`. IDs are `<kind>_<UUIDv4>` with prefixes `lnk`, `seg`, `zon`; references use IDs, never mutable names, runtime ifindexes or current interface addresses. Names are non-empty UTF-8 strings of at most 128 Unicode code points with no control characters, unique within the collection using exact case-sensitive comparison. Descriptions are plain text, at most 4096 code points; no HTML interpretation is authorized.

### Links

Every link requires `kind`, `mtu` (68–65535) and `enabled`. Exactly the fields of its kind are accepted:

| Kind | Additional fields |
| --- | --- |
| `ethernet` | `permanentMac`: a six-byte unicast, nonzero MAC; canonical lowercase colon notation |
| `vlan` | `parent`: link ID; `vlanId`: integer 1–4094 |
| `bridge` | `members`: non-empty unique array of link IDs |
| `bond` | `members`: non-empty unique array of Ethernet link IDs; `mode`: Linux mode name `balance-rr`, `active-backup`, `balance-xor`, `broadcast`, `802.3ad`, `balance-tlb` or `balance-alb` |
| `pppoe` | `parent`: Ethernet, VLAN, bridge or bond link ID; `username`: non-empty text up to 256 code points, no control characters; `password`: configuration-secret reference |

The stable identity is declarative. The current `status/links` MAC does not establish that a permanent MAC was discovered; saving an Ethernet assignment is explicit operator input. Missing hardware does not invalidate a save or invent a replacement device. Host capabilities, effective MTU and availability must be checked again by a future apply operation.

Reject cycles across parent/member relationships; missing/self references; duplicate Ethernet permanent MAC assignments; duplicate VLAN parent/tag pairs; a member enslaved into multiple bridges/bonds; and a segment attached directly to an enslaved member. Reject a VLAN/PPPoE parent of a nonsensical kind rather than promising an unrealizable composition. VLAN parents may be Ethernet, VLAN, bridge or bond; bridge members may be Ethernet, VLAN or bond. PPPoE itself is not an L2 member or VLAN parent.

### Segments and zones

A zone has only the common identity/name/description fields. It is an explicit policy grouping, not a hard-coded WAN/LAN role.

A segment requires `link`, exactly one `zone`, and `addressing`. A link has at most one segment: additional L2 domains use distinct VLAN/link objects. Management, LAN and WAN are not mutually exclusive properties of physical hardware. A physical parent may carry several VLANs; this schema does not require separate physical NICs for them.

`addressing.ipv4` requires `mode`: `none`, `dhcp`, `static` or `ppp`. Static mode requires a non-empty, unique `addresses` array of host-address/prefix strings; optional `gateway` is an IPv4 host address. Other modes reject those static-only fields. `ppp` is required for IPv4 negotiated on a PPPoE link and is rejected on other link kinds; a PPPoE segment cannot use IPv4 DHCP/static mode.

`addressing.ipv6` requires `mode`: `none`, `static`, `slaac`, `dhcp6` or `delegated`. Static mode requires non-empty unique host-address/prefix `addresses`; `dhcp6` optionally accepts `requestPrefixLength` (integer 1–64). Delegated mode requires `from` (another segment configured for DHCPv6 prefix acquisition) and `subnetId` (1–4 hexadecimal digits, canonical lowercase). Other modes reject those mode-specific fields. A delegated segment must not reference itself, and delegation cycles are rejected. Actual prefix allocation/fit is checked when the upstream prefix is known, not guessed from the requested length.

An optional IPv6 `ra` object has explicit booleans `enabled`, `managed` and `otherInfo`. It expresses router-advertisement intent only; the save path does not run an RA or DHCP server. RA cannot be enabled when IPv6 mode is `none`.

Host bits in static addresses are retained; parsing a network prefix must not mask the configured host address. Reject family/prefix mismatches, duplicate local addresses, and overlapping static subnets on different segments in this non-VRF model. Compare subnet ranges for overlap, not merely host-address strings. Do not infer an upstream gateway from an interface address.

## Schema validation and canonical form

Publish embedded JSON Schema draft 2020-12 for the supported declaration. Use a maintained 2020-12 validator and pure-Go SQLite driver; the implementation plan pins compatible releases of the previously proposed `github.com/santhosh-tekuri/jsonschema/v6` and `modernc.org/sqlite` after checking their current support and licences. No ORM or generic storage-provider abstraction is needed.

Every write runs schema validation, then Go semantic checks against the complete resulting declaration: identities, references, topology, address families, overlap and secret ownership. Validation failure returns HTTP `422 VALIDATION_FAILED` with bounded field details using JSON Pointers. Validation must complete before publication, not only at future render time. Reject invalid UTF-8, duplicate JSON keys, trailing JSON, unknown fields and explicit nulls where a value is required. Do not silently coerce strings into numbers or fill in a different network intent.

Canonical serialization uses deterministic object-key ordering and compact JSON with a trailing newline. Domain numeric values are integers; strings retain their Unicode content without normalization. Unordered object arrays sort by ID, member arrays by referenced ID, and static address arrays by canonical address/prefix. These ordering rules do not authorize sorting future policy/NAT rule arrays. Optional omitted fields remain omitted. Canonicalize textual MAC/IP/hex forms before comparison; an unchanged canonical value is not a new edit.

The database layout version and document `schemaVersion` are separate. This slice implements version 1 only, refuses unknown/newer layouts or documents without rewriting them, and creates no speculative migration chain. Subsequent schema changes must define compatibility/migration explicitly. A saved document whose bytes or hashes are corrupt is an error, not a reason to reset the store.

## Secrets and safe reads

Store 2 is `{"schemaVersion":1,"secrets":{...}}`, mapping engine-owned secret IDs to `{kind,value,updatedAt}` entries. The timestamp records when that value was set, not each snapshot that retains it. Initially the only supported kind/consumer is `password` for a PPPoE link. A canonical store-1 password field contains only `{"secret":"sec_<UUIDv4>"}`. Ownership is the containing link and password field; arbitrary reads/rebinding of another object's key are not permitted.

API reads substitute `{"isSet":true,"updatedAt":"<UTC timestamp>"}` for password references. They never return values, secret-document hashes, raw store-2 documents, database files or secret differences. The persisted store-1 snapshot remains secret-free and independently readable. Internal hashes/secret values must not enter diagnostics or request logging.

On creation a PPPoE password is supplied as `{"set":"<value>"}`. On replacement, omit `password` to retain that object's existing binding or supply `{"set":"<value>"}` to replace it. Read-back `isSet` metadata is not a write instruction. Empty passwords are permitted; missing bindings are not. Limit a password to 4096 UTF-8 bytes and reject NUL. The engine allocates the secret ID; the caller cannot choose another object's ID. A password edit advances the owning link's object version even if its public fields are unchanged. Removing/retyping the link removes its secret from the new secret snapshot but preserves older immutable versions. Equal replacement values are a no-op and do not refresh timestamps merely to create history.

No general secret-management service, secret export, role account, session, plugin secret access or encryption-at-rest promise is introduced. Filesystem isolation protects these stores; administrator-only encrypted exports remain a later feature.

## Files, locking and paired publication

The root engine owns `/var/lib/andaru` (`0700`); files, SQLite sidecars and lock files are root-owned and inaccessible to other users (`0600`, umask `0077`). A canonical absolute `--state-dir` engine option permits isolated tests; the CLI still chooses only its Unix socket. Reject symlinked or untrusted state paths using the existing root-controlled ancestor policy. Refuse special files or unsafe existing permissions rather than changing ownership of foreign state.

A single engine holds an OS advisory lock on `<state-dir>/engine.lock` for its lifetime. A second engine targeting that directory fails before opening/mutating databases, including when using another socket. Within that process, serialize publication and snapshot/prune access through the owning store; SQLite transactions remain the authority for checking versions and publishing the head. Do not rely on a stale in-memory copy for conflict checks.

The paired databases live under `<state-dir>/configuration/`: `config.db` and `secrets.db`. This child directory makes first initialization atomic despite systemd creating the state root. Build a new pair in a private sibling staging directory, checkpoint/close and sync the database files/directory, rename the complete directory into place, then sync its parent. Never overwrite an existing published directory. An interrupted staging directory is not a valid store or a reason to replace an existing one. Once published, a missing/corrupt member is a store error; never initialize just that member and pretend existing history is intact.

Both databases use WAL and `synchronous=FULL`; enable foreign keys where applicable and `secure_delete=ON` for `secrets.db`. These settings do not promise forensic erasure from WALs, snapshots, SSD remapping or backups. No garbage collection or secret deletion from retained history occurs in this slice.

`config.db` contains:

- Content-addressed immutable configuration documents.
- Immutable saved-version rows: monotonically increasing ID, parent, schema version, configuration hash, secret-document hash, server timestamp, actor and changed object identities.
- Immutable per-object change records, indexed by identity and saved version. The object's version is its last change at/before the selected saved head, not a separate mutable counter. Deletion records a tombstone; IDs are never reused.
- A single saved-head pointer. There is no candidate table, live/confirmed pointer or fabricated apply row.

`secrets.db` contains content-addressed immutable canonical secret documents. Hashes are internal addressing keys, not public versions or authorization capabilities. Verify content on load instead of trusting a hash column blindly.

For each save, under the store's publication lock:

1. Begin a `config.db` write transaction, load the current head and check every supplied object/create precondition against it.
2. Construct and validate the complete next configuration/secret pair. If canonical content is unchanged, return existing versions without publishing history.
3. Commit the new secret document to `secrets.db` durably first, if it is not already present.
4. In the still-open `config.db` transaction, insert the configuration document, saved version and object-version metadata, then advance the saved head and commit. Only this commit publishes the new pair.

Separate WAL databases do **not** supply an atomic attached-database commit. The ordering above guarantees every published version names an already durable secret document. A crash before step 4 may leave an unreferenced secret document, never a public half-save. Do not delete those orphans in a concurrent cleanup job; this slice does not prune. A failure before publication preserves the previous head. A commit I/O error may have an uncertain outcome: return no success, stop further writes if store integrity is uncertain, and require a fresh read/reopen to establish the published head. Do not claim that a missing acknowledgement proves nothing committed, retry mutations blindly, or replace a damaged database with an empty one.

Saved versions and object versions travel as decimal strings in JSON to avoid JavaScript integer precision loss. Actors come from verified peer credentials (`unix:uid:0`), not request headers or user-supplied attribution. Timestamps come from the server but are not ordering or locking tokens.

## Local API and CLI

Extend the existing `/run/andaru/engine.sock` service and its authorization-before-routing policy. No HTTPS listener, application accounts or token/CSRF stub is added. Existing live inventory and OpenAPI routes retain their behaviour; OpenAPI describes only implemented operations. Extend routing in its owning API layer rather than adding a second handler stack or forwarding through a shell.

Resources:

| Resource | Operations |
| --- | --- |
| `/api/v1/schema` | `GET`/`HEAD`: supported declaration schema |
| `/api/v1/config` | `GET`/`HEAD`: latest saved secret-free configuration, labelled saved rather than active |
| `/api/v1/config/{links,segments,zones}` | `GET`/`HEAD` collection, `POST` create |
| `/api/v1/config/{collection}/{id}` | `GET`/`HEAD`, `PUT` replace, `DELETE` |
| `/api/v1/config/changes` | `POST`: all-or-nothing object operations |
| `/api/v1/versions` | `GET`/`HEAD`: saved history, descending ID |
| `/api/v1/versions/{id}` | `GET`/`HEAD`: immutable secret-free saved snapshot and change metadata |

This slice uses full-object `PUT`, not a partial/JSON Patch language. Create and replacement bodies contain the object's declared fields; the route owns identity for replacement and clients cannot change it. Creation responses are `201` with ID and `Location`; writes return the current object version, saved version and strong ETag. Delete returns a JSON acknowledgement with the saved version, not an invented active-state result.

Individual reads return an object envelope with `value`, `version` and `savedVersion`. Collections return `items` of those envelopes plus the snapshot's saved version; all items come from one read snapshot. The strong object ETag is derived from object ID and its last changed saved-version ID, not its content hash. This prevents an A→B→A edit from reviving an old token. Unrelated-object saves leave that token unchanged.

`PUT` and `DELETE` require the exact object ETag in `If-Match`; missing gives `428 PRECONDITION_REQUIRED`, stale or already-deleted gives `412 PRECONDITION_FAILED`. The latter returns current object metadata when present, or an explicit deleted marker, without secrets. `If-Match: *`, weak tags and lists are rejected (`400 INVALID_REQUEST`): blind overwrites are not an alternative path. Unknown IDs on ordinary reads give `404 NOT_FOUND`. Names/references/topology conflicts are field validation errors, not stale-version errors.

A create has no existing object ETag. `POST` creates a server-ID object only if its name is still absent in the current collection; a duplicate name returns `409 ALREADY_EXISTS`. It never overwrites an existing object. This is the create precondition, not permission for unversioned updates.

`config/changes` accepts `{"operations":[...]}` with 1–100 operations. Each is a `create`, `replace` or `delete`, names a collection, and supplies `value` where needed. Replace/delete name an existing `id` and an `expectedVersion` from the read envelope; missing expectations return `428`, mismatches `412`. Multiple operations targeting one identity are rejected rather than interpreted sequentially. The complete batch is checked against the head and validated as one resulting declaration, allowing references and deletions to change together.

Batch creates have a unique request-local `localId` (ASCII letter followed by up to 31 letters/digits/underscores). References may use `@localId` only in declared reference fields, never names or secret IDs. Allocate real server IDs for all creates before resolving/validating references; return the local-to-real mapping on success. Unknown labels, cross-kind references and unresolved labels reject the entire batch. No temporary object is committed or exposed. This permits creating a link, zone and segment atomically without a separate ID-allocation API.

Every request is limited to 1 MiB, depth 32 and 100 reported validation details. Reject oversized bodies with `413 REQUEST_TOO_LARGE` before unbounded buffering. Cap each resulting canonical store document at 4 MiB and the configuration at 10,000 objects. History uses `limit` (default 50, maximum 100) and an exclusive `before` saved-version cursor; no offset scan or unbounded list. SQLite busy waits are bounded at 2 seconds; request work has a 5-second context deadline and returns a structured `503 STORE_UNAVAILABLE` on unavailable storage. Store errors disclose no SQL, filesystem paths or secret bodies to clients.

Add CLI commands under `andaru ctl config` for snapshot/list/get/create/replace/delete/batch, and `andaru ctl versions` for history/snapshot reads. Mutations read a JSON body from standard input, not secret-bearing arguments, and require explicit expected versions rather than fetching and silently substituting the newest token. Keep stdout machine-readable, diagnostics on stderr, and the existing no-success-stdout-on-error policy. No generic arbitrary-URL client or direct database-edit path is added.

## Initialization, supervision and lifecycle

The first complete store contains saved version `0` with empty collections and the empty secret document. It has no applied generation. Startup checks layout/schema and head integrity before serving configuration operations; store failure must not erase state or advertise readiness. Updating the service adds `StateDirectory=andaru` and mode `0700`, while retaining `ProtectSystem=strict`, Unix/Netlink-only address families and empty capability sets. State writes are confined to owned storage, not `/etc` or network runtime files.

Retain all saved history in this first persistence slice. The agreed future policy remains `system.history.keepSavedVersions` default 100, minimum 10, with confirmed/last-known-good protections and system-area apply. Implementing deletion now, before live-generation pins exist, would invent a competing retention policy. There is no misleading editable retention setting or no-op prune endpoint here. History can grow until that lifecycle slice; disk-full errors must leave the previous head readable and report save failure. `secure_delete` is not a claim that retained passwords have been removed.

Do not emit SSE events, compare against an imagined empty live firewall, or display “applied” after a save. Networkd/nft files, current links, forwarding, management access and installed unrelated software must remain unchanged. A restart loads the saved head for reads only; it never realizes it.

## Acceptance and evidence

The implementation must prove these behaviours, not only schema wiring:

1. A valid link/zone/segment topology round-trips with stable IDs, canonical host addresses and correct references. Ethernet/VLAN/bridge/bond/PPPoE and IPv6 delegation cases exercise their actual semantic boundaries; invalid relationships, unknown fields, duplicate keys and oversized input leave the head unchanged.
2. Two independent readers editing the same object produce exactly one successful stale-token write; the loser gets `412` and current state. Edits to different objects survive together. A→B→A cannot reuse the original ETag. Batch changes are all-or-nothing, including request-local create references and secret edits.
3. A PPPoE password change advances the owning object's version; previous saved pairs still resolve the old secret. API/CLI/history/error output contains neither plaintext passwords nor secret-document hashes. Invalid requests never claim another object's secret.
4. Real SQLite child-process tests exercise restart and termination before/after the secret commit and configuration publication, including first initialization. The only visible outcomes are the previous complete pair or the new complete pair; a committed version never names a missing secret document. Exercise denied permissions, second-engine locking, a missing pair member, corruption and disk/write errors without destructive auto-reset. Process-kill proof is not proof of arbitrary storage hardware surviving power loss.
5. Run the final binary under systemd on retained Debian VM 114 after a new verified Proxmox snapshot. Save and read real records through the root-only Unix API/CLI, restart, verify persistence and denied non-root access, and independently compare nft rules, forwarding and management addresses before/after. Retain the VM. Do not claim a reboot, restore or full firewall test unless actually performed.

No permanent source-text, mock-forwarding or copied-schema tests substitute for behaviour. Contract checks cover actual served schemas/OpenAPI and responses. One fresh adversarial implementation review remains required before publishing product code; this specification's review is not that implementation gate.

## Review checkpoint

This document resolves storage publication and save semantics, not live apply or the eventual full firewall declaration. The owner reviews it before an implementation plan is written. That plan must identify concrete files, pin dependencies, provide red/green checks, and keep secrets and saved/live boundaries intact. No product code or dependencies are installed by recording this design.
