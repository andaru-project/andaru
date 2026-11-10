# Durable saved network configuration

## Status and intent

Approved in conversation, including the correction to simple named secrets; not implemented behaviour. The approved slice uses canonical JSON snapshots in SQLite; physically separate configuration and configuration-secret stores published as one saved version; schema validation and stable network-object IDs; per-object optimistic locking; atomic multi-object saves; persistence without activating networking; real database and Debian-lab proof. Contracts remain provisional until Andaru 1.0.

This follows the root-local engine shipped in [PR #24](https://github.com/andaru-project/andaru/pull/24). It gives the CLI and eventual UI a real way to save links, segments and zones. It does not take over interfaces, configure networkd/nft, manufacture a confirmed generation, or expose a network listener.

### Authorities and precedence

- [Issue #6](https://github.com/andaru-project/andaru/issues/6): JSON/JSON Schema, SQLite documents, object model, paired stores, saved-version history, optimistic writes and per-area apply. Its later no-draft ruling supersedes the earlier candidate table. Its API draft corrects stale `If-Match` failures to HTTP `412`, not `409`.
- [Issue #12](https://github.com/andaru-project/andaru/issues/12): configuration and related secrets version together; users, role memberships and user credentials remain outside that history; normal exports omit secret values.
- [Web UI foundation](../../research/web-ui-foundation.md): API-first; saved, live and observed state are different; mockups are not a prerequisite.
- [Traefik decisions](../../research/traefik-manifest-example.md#apply-backups-and-offline-recovery): save/apply separation, secret-free default exports, recovery is not a successful apply.

## Decisions and scope

Keep whole canonical documents as SQLite blobs, with small relational tables for immutable revisions, object versions and the saved head. Do not normalize every network field into SQL columns or create a second JSON-file source of truth. JSON files alone would not supply the selected transaction/history model; normalized domain tables would duplicate the JSON Schema and complicate portable snapshots.

The first declaration contains only `schemaVersion`, `links`, `segments` and `zones`. Unsupported top-level sections are rejected, not accepted and ignored. Firewall rules, routes, DHCP/DNS service configuration, management takeover, roles/users, extension settings, backups, pruning and live-generation/apply APIs are subsequent slices. The four-store architecture is preserved, but unused users/credentials databases and empty API services are not created.

Network save support includes Ethernet, VLAN, bridge, bond and PPPoE link definitions, plus static/dynamic IPv4 and IPv6 addressing intent. PPPoE supplies the first real configuration-secret consumer; supporting its declaration is not a claim that the engine starts PPP sessions. The owner's VLAN/PPPoE and delegated-IPv6 requirements remain part of the product, not silently removed from its model.

## Declaration and identity

Store 1 is a versioned document:

```json
{"schemaVersion":1,"links":[],"segments":[],"zones":[]}
```

Each network object has an immutable server-generated ID, a mutable `name`, and optional `description`. IDs are `<kind>_<UUIDv4>` with prefixes `lnk`, `seg`, `zon`; references use IDs, never mutable names, runtime ifindexes or current interface addresses. These UUID rules do not apply to named secrets. Network names are non-empty UTF-8 strings of at most 128 Unicode code points, reject Unicode control and format characters (including bidi controls), and are unique within the collection using exact case-sensitive comparison. Descriptions are plain text, at most 4096 code points; no HTML interpretation is authorized.

### Links

Every link requires `kind`, `mtu` (68–65535) and `enabled`. Declared IPv6 other than `none` requires MTU at least 1280. A VLAN's MTU cannot exceed its declared parent's; a PPPoE link's MTU cannot exceed its declared parent's minus 8. Host capability checks remain apply-time checks. Exactly the fields of its kind are accepted:

| Kind | Additional fields |
| --- | --- |
| `ethernet` | `permanentMac`: a six-byte unicast, nonzero MAC; canonical lowercase colon notation |
| `vlan` | `parent`: link ID; `vlanId`: integer 1–4094 |
| `bridge` | `members`: required unique array of link IDs, permitted to be empty |
| `bond` | `members`: non-empty unique array of Ethernet link IDs; `mode`: Linux mode name `balance-rr`, `active-backup`, `balance-xor`, `broadcast`, `802.3ad`, `balance-tlb` or `balance-alb` |
| `pppoe` | `parent`: Ethernet, VLAN, bridge or bond link ID; `username`: non-empty text up to 256 code points, no control characters; `password`: configuration-secret reference |

The stable identity is declarative. The current `status/links` MAC does not establish that a permanent MAC was discovered; saving an Ethernet assignment is explicit operator input. Missing hardware does not invalidate a save or invent a replacement device. Host capabilities, effective MTU and availability must be checked again by a future apply operation.

Reject cycles across parent/member relationships; missing/self references; duplicate Ethernet permanent MAC assignments; duplicate VLAN parent/tag pairs; a member enslaved into multiple bridges/bonds; and a segment or PPPoE parent attached directly to an enslaved member. VLAN parents may be Ethernet, VLAN, bridge or bond; bridge members may be Ethernet, VLAN or bond. PPPoE itself is not an L2 member or VLAN parent. Use the bridge/bond master, not its enslaved port, when selecting a logical parent.

### Segments and zones

A zone has only the common identity/name/description fields. It is an explicit policy grouping, not a hard-coded WAN/LAN role.

A segment requires `link`, exactly one `zone`, and `addressing`. A link has at most one segment: additional L2 domains use distinct VLAN/link objects. Management, LAN and WAN are not mutually exclusive properties of physical hardware. A physical parent may carry several VLANs; this schema does not require separate physical NICs for them.

`addressing.ipv4` requires `mode`: `none`, `dhcp`, `static` or `ppp`. Static mode requires a non-empty, unique `addresses` array of host-address/prefix strings; optional `gateway` is an IPv4 unicast host address, not a local address, reachable within a declared static subnet. Other modes reject those static-only fields. `ppp` is required for IPv4 negotiated on a PPPoE link and is rejected on other link kinds; a PPPoE segment cannot use IPv4 DHCP/static mode. Explicit off-link gateways belong to the later routes contract, not an inferred route.

`addressing.ipv6` requires `mode`: `none`, `static`, `slaac`, `dhcp6` or `delegated`. Static mode requires non-empty unique host-address/prefix `addresses`; optional `gateway` is a non-local IPv6 unicast address reachable in a declared static subnet or a link-local gateway scoped by this segment. Reject textual zone IDs; segment identity supplies the scope. `dhcp6` optionally accepts `requestPrefixLength` (integer 1–64). Delegated mode requires `from` (a distinct segment configured for DHCPv6 prefix acquisition) and `subnetId` (1–4 hexadecimal digits). Canonicalize the ID as lowercase hexadecimal without leading zeroes, except `0`, and reject duplicate numeric `(from, subnetId)` allocations. Other modes reject mode-specific fields. Actual allocation/fit is checked when the upstream prefix is known, not guessed from the requested length.

An optional IPv6 `ra` object has explicit booleans `enabled`, `managed` and `otherInfo`. It expresses router-advertisement intent only; the save path does not run an RA or DHCP server. RA cannot be enabled when IPv6 mode is `none`.

Host bits in static addresses are retained; parsing a network prefix must not mask the configured host address. Reject family/prefix mismatches, textual zone IDs, IPv4-mapped IPv6, unspecified, loopback and multicast local addresses, and IPv4 limited broadcast. For IPv4 prefixes of /30 or shorter, reject subnet-network and directed-broadcast host addresses; /31 point-to-point endpoints and /32 hosts are valid. Private unicast and link-local unicast addresses are allowed. Reject duplicate local addresses and overlapping static subnets on different segments in this non-VRF model, except IPv6 link-local addresses/subnets, whose identity and overlap are scoped to each segment. Compare subnet ranges for overlap, not merely host-address strings. Do not infer an upstream gateway from an interface address.

## Schema validation and canonical form

Publish an embedded JSON Schema draft 2020-12 bundle at `/api/v1/schema`, with named `$defs` for `declaration` (persisted secret references), `objectWrite` (write-only secret commands) and `changes` (batch requests); the root `$ref` selects `declaration`. OpenAPI separately describes redacted response views. These are deliberately different shapes, not one schema falsely describing all three. Resolve only embedded trusted resources, never fetch schemas over the network. Use a maintained 2020-12 validator and pure-Go SQLite driver; the implementation plan pins compatible releases of the previously proposed `github.com/santhosh-tekuri/jsonschema/v6` and `modernc.org/sqlite` after checking support and licences. No ORM or generic storage-provider abstraction is needed.

Every write validates its request shape, then the complete resulting declaration against the stored schema and Go semantic checks: identities, references, topology, address families, overlap and secret ownership. Validation failure returns HTTP `422 VALIDATION_FAILED` with bounded field details using JSON Pointers. Construct error messages from engine-owned constraint descriptions, never validator strings that echo submitted values; a secret-field error identifies only the field and violated constraint. Reject invalid UTF-8, lone-surrogate escapes, duplicate JSON keys, trailing JSON, unknown fields and any explicit null, including optional fields. Do not silently coerce strings into numbers, replace malformed Unicode with U+FFFD, or fill in a different network intent.

Canonical serialization uses Go JSON encoding with deterministic sorted object keys, two-space indentation disabled, HTML escaping disabled, compact separators and exactly one trailing newline. Use the same encoder settings on every path; this is not a claim of RFC 8785 interoperability. Domain numeric values are bounded integers, never floating-point data; strings retain their Unicode content without normalization. Unordered object arrays sort by ID, member arrays by referenced ID, and static address arrays by canonical address/prefix. An empty bridge member array remains `[]`. These rules do not authorize sorting future policy/NAT rules. Optional omitted fields remain omitted. Canonicalize textual MAC/IP/hex forms before comparison; unchanged canonical content is not a new edit.

The database layout version and document `schemaVersion` are separate. This slice implements version 1 only, refuses unknown/newer layouts or documents without rewriting them, and creates no speculative migration chain. Subsequent schema changes must define compatibility/migration explicitly. A saved document whose bytes or hashes are corrupt is an error, not a reason to reset the store.

## Secrets and safe reads

Store 2 is `{"schemaVersion":1,"secrets":{"core":{...}}}`, mapping an owning scope and a user-chosen name to `{kind,value,updatedAt,version}` entries. Names match `[A-Za-z_][A-Za-z0-9_]{0,63}`, are case-sensitive, and are unique within their scope; uppercase names such as `CF_DNS_TOKEN` or `WAN_PASSWORD` are recommended. There is no secret UUID or hidden UUID-to-name registry. The initial network API operates only in the `core` scope. A canonical store-1 password field contains `{"secret":"WAN_PASSWORD"}`, with that scope implicit in this core resource.

Scope and authorization, not obscurity of a name, control access. Root Unix peers may bind core network objects to named core secrets. Future extension scopes remain isolated; a name in another scope is not accessible without its owner's explicit grant. No cross-scope selector or grant-management endpoint is added to this slice. A secret's name is independent of the environment variable or argument receiving its value at runtime.

Initially the supported kind/consumer is `password` for PPPoE. The timestamp records when a value was set. `version` is its last changed saved-version ID, not a content hash. API reads expose `{"secret":"WAN_PASSWORD","isSet":true,"updatedAt":"<UTC timestamp>","version":"7"}`, never the value, secret-document hash, raw store-2 document, database file or secret difference. Store 1 remains a secret-free independently readable document; this slice's API requires a validated pair and is not independent-store recovery tooling. Internal secret values/hashes never enter diagnostics or request logging.

To bind an existing secret, supply `{"secret":"WAN_PASSWORD"}`. To create one or replace its value, supply `{"secret":"WAN_PASSWORD","set":"<value>","expectedVersion":"0"}`; `0` explicitly requires absence, while a positive expected version must match the current named secret. Missing set-preconditions return `428`, stale/failed-absence preconditions `412`, and malformed versions `400`. A plain reference to a missing name returns a field validation error. On replacement, omitting `password` retains the existing reference. Supplied read-back `isSet`, `updatedAt` or `version` fields are rejected at that field with `422`; clients retain the name and remove read-only metadata. Empty password values are allowed; missing bindings are not. Limit a password to 4096 UTF-8 bytes and reject NUL.

A named value can intentionally have several core referrers. A value change requires its own expected version, updates the secret version and all affected network-object versions in the same saved transaction, and records every affected identity without overwriting unrelated public fields. Thus stale pages referring to that value fail their next save rather than silently retaining a stale ETag. Reject multiple `set` commands for one scoped name in a batch. Equal replacement values are no-ops; removing/retyping a link does not delete a user-managed named secret, which may be shared or reused. Report unused names as metadata; do not silently collect them. Old saved pairs retain their corresponding names and values.

No standalone secret CRUD/export service, accounts/sessions, extension grants or encryption-at-rest promise is introduced. Named core secret creation/update is delivered through these real network writes; broader secret-management UI and administrator-only encrypted exports remain later features.

## Files, locking and paired publication

The root engine owns `/var/lib/andaru` (`0700`); files, SQLite sidecars and lock files are root-owned and inaccessible to other users (`0600`, umask `0077`). A canonical absolute `--state-dir` engine option permits isolated tests; the CLI still chooses only its Unix socket. Reject symlinked or untrusted state paths using the existing root-controlled ancestor policy. Refuse special files or unsafe existing permissions rather than changing ownership of foreign state.

A single engine holds an OS advisory lock on `<state-dir>/engine.lock` for its lifetime. A second engine targeting that directory fails before opening/mutating databases, including when using another socket. Within that process, serialize publication and snapshot/prune access through the owning store; SQLite transactions remain the authority for checking versions and publishing the head. Do not rely on a stale in-memory copy for conflict checks.

The paired databases live under `<state-dir>/configuration/`: `config.db` and `secrets.db`. Initialization runs only when that entry is absent; any existing entry, including an empty directory, must be validated as a complete store or rejected, never overwritten. Under `engine.lock`, create a private fixed sibling `.configuration-init`, record a root-only initialization marker, build version 0, checkpoint/close and sync both database files and the staging directory. Publish using Linux `renameat2(RENAME_NOREPLACE)`, not plain overwrite-capable rename, then sync the parent. Failure or unavailable no-replace support is an error, not permission to fall back to overwrite.

If the published directory is absent, an interrupted staging directory can be discarded only after checking its root ownership, private modes, initialization marker and expected bootstrap-only contents without following symlinks. It can contain only an unpublished version-0 pair. An unmarked non-empty or unexpected staging tree is refused for operator inspection; a verified empty root-owned staging directory may be removed and initialization retried. Never discard staging when a published directory exists. Once published, a missing/corrupt pair member is an error; never initialize just that member.

Both databases use WAL and `synchronous=FULL`; enable foreign keys where applicable and `secure_delete=ON` for `secrets.db`. Apply and verify connection-scoped pragmas on every physical connection, including replacements, not a single arbitrary pooled `Exec`. Keep one connection per database; all pair operations already serialize through the owning store. These settings do not promise forensic erasure from WALs, snapshots, SSD remapping or backups. No garbage collection or secret deletion from retained history occurs in this slice.

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

Separate WAL databases do **not** supply an atomic attached-database commit. The ordering guarantees every published version names an already durable secret document. A crash before configuration publication may leave an unreferenced secret document, never a public half-save. A failure before publication preserves the previous head; commit I/O errors can have an uncertain outcome. Do not infer “nothing committed” from a missing acknowledgement, retry mutations blindly, or replace damaged storage with an empty database.

The store has explicit states: `ready` after complete-pair validation; `write-fenced` after a publication I/O failure or uncertain outcome; and `unavailable` when startup/reopen cannot validate storage. In `write-fenced`, configuration reads and writes return `503 STORE_UNAVAILABLE`. The engine completes the failing error response where possible, drains its server and exits non-zero, so existing systemd restart policy reopens and validates the durable head. There is no in-process blind retry. At startup, `unavailable` refuses the whole engine start before binding its socket, including inventory routes; operator recovery must not be confused with an existing readiness API.

Preparation has a 3-second budget including the at-most-2-second SQLite busy wait and honours request cancellation. Once final publication starts, steps 3–4 share a separate 2-second bounded context independent of client cancellation; shutdown allows this bounded phase to finish. Do not start publication when preparation already failed/cancelled. A transport failure can still hide a committed save: return no success, and let the caller read current state before retrying. Mutation CLI timeout is 10 seconds, exceeding the total 5-second server budget; ordinary reads retain their existing 5-second timeout. These budgets are bounds, not a promise that disconnects can never occur during commit.

The SQL transaction must have a store-owned bounded lifetime from its creation; do not begin a transaction on the HTTP request context and try to detach only its commit calls. Cancelling a `database/sql` transaction's creation context can automatically roll it back. Cancellation before publication rolls back; the final phase must retain the independent bound described above.

Saved versions and object versions travel as decimal strings in JSON to avoid JavaScript integer precision loss. Actors come from verified peer credentials (`unix:uid:0`), not request headers or user-supplied attribution. Timestamps come from the server but are not ordering or locking tokens.

## Local API and CLI

Extend the existing `/run/andaru/engine.sock` service and its authorization-before-routing policy. No HTTPS listener, application accounts or token/CSRF stub is added. Existing live inventory and OpenAPI routes retain their behaviour; OpenAPI describes only implemented operations. Extend routing in its owning API layer rather than adding a second handler stack or forwarding through a shell.

Resources:

| Resource | Operations |
| --- | --- |
| `/api/v1/schema` | `GET`/`HEAD`: supported declaration schema |
| `/api/v1/config` | `GET`/`HEAD`: latest secret-free snapshot with `state:"saved"`, `savedVersion` and `value`; no active-state claim |
| `/api/v1/config/{links,segments,zones}` | `GET`/`HEAD` collection, `POST` create |
| `/api/v1/config/{collection}/{id}` | `GET`/`HEAD`, `PUT` replace, `DELETE` |
| `/api/v1/config/changes` | `POST`: all-or-nothing object operations |
| `/api/v1/versions` | `GET`/`HEAD`: saved history, descending ID |
| `/api/v1/versions/{id}` | `GET`/`HEAD`: immutable secret-free saved snapshot and change metadata |

This slice uses full-object `PUT`, not a partial/JSON Patch language. Bodies contain declared fields; the route owns replacement identity and callers cannot change it. Creation returns `201`, ID and `Location`. Successful create/replace bodies return `value` and the current `version`; the published saved-version ID is in `Andaru-Saved-Version`. Do not emit ETag validators on transformed write responses. Delete returns a JSON acknowledgement with `state:"saved"` and `savedVersion`. Single-object routes translate into one operation through the same validation/publication path as batches; do not duplicate the save algorithm.

Individual reads return only `{"value":...,"version":"<last-change ID>"}`; global snapshot version travels in the `Andaru-Saved-Version` header, not that body. GET/HEAD ETag is exactly the quoted string `"<object ID>.<version>"`. This is a pure function the CLI can construct from the returned ID/version for subsequent `If-Match`; batch `expectedVersion` is the same decimal version string. A→B→A gets a new version, while unrelated saves leave both the object's bytes and ETag unchanged. Collections return `state:"saved"`, `items` of those envelopes and one `savedVersion`, read from a consistent snapshot. Only individual GET/HEAD responses emit ETags in this slice.

The saved snapshot also returns sorted `unusedSecrets` names in its outer metadata, never their values; an empty list is `[]`. This is not a separate secret CRUD endpoint. Conflict errors extend the common `{error:{code,message,details}}` envelope with optional `error.current`: redacted object state, a deleted-object marker, or `{kind:"secret",name,version}` for a named-secret precondition. Ordinary errors omit it; no conflict response includes plaintext values.

`PUT` and `DELETE` require that exact strong ETag in `If-Match`; missing gives `428 PRECONDITION_REQUIRED`. A changed object or known tombstone gives `412 PRECONDITION_FAILED`, with current object metadata or an explicit deleted marker, without secrets. A never-existing ID gives `404 NOT_FOUND`, not an invented tombstone. Wildcard, weak or multiple tags give `400 INVALID_REQUEST`: blind overwrites are not another path. Names/references/topology conflicts are `422` field errors. Error precedence is authorization, route/method, body size (`413`), strict JSON syntax (`400`), supplied precondition syntax (`400`), missing precondition (`428`), never-existing ID (`404`), stale/deleted version (`412`), then request-shape/resulting-document validation (`422`).

A create has no existing object ETag. `POST` creates a server-ID object only if its name is still absent in the current collection; duplicate names return `422 VALIDATION_FAILED` at `/name` (the corresponding operation/value path for batches), consistently on create and replacement. It never overwrites an existing object. This is the create precondition, not permission for unversioned updates.

`config/changes` accepts `{"operations":[...]}` with 1–100 operations. Each is a `create`, `replace` or `delete`, names a collection, and supplies `value` where needed. Replace/delete name an existing `id` and an `expectedVersion` from the read envelope; missing expectations return `428`, mismatches `412`. Duplicate targets are rejected rather than interpreted sequentially. Check the complete batch against the head and validate one resulting declaration, allowing references and deletions to change together. Success returns `state:"saved"`, one `savedVersion`, and the changed object envelopes/create-ID mapping; there is no per-operation partial success.

Batch creates have a unique request-local `localId` (ASCII letter followed by up to 31 letters/digits/underscores). References may use `@localId` only in network-object reference fields, never names or secret names. Allocate real server IDs for all network creates before resolving/validating references; return the local-to-real mapping on success. Unknown labels, cross-kind references and unresolved labels reject the entire batch. No temporary object is committed or exposed. This permits creating a link, zone and segment atomically without a separate ID-allocation API.

Every request is limited to 1 MiB, depth 32 and 100 reported validation details. Reject oversized bodies with `413 REQUEST_TOO_LARGE` before unbounded buffering. Cap each canonical store document at 4 MiB and the configuration at 10,000 objects. History uses `limit` (default 50, maximum 100) and an exclusive `before` saved-version cursor; no offset scan or unbounded list. Storage/timeout failures use structured `503 STORE_UNAVAILABLE`, subject to the fencing rules above. Store errors disclose no SQL, filesystem paths or secret bodies to clients.

Add CLI commands under `andaru ctl config` for snapshot/list/get/create/replace/delete/batch, and `andaru ctl versions` for history/snapshot reads. Mutations read a JSON body from standard input, not secret-bearing arguments, and require explicit expected versions rather than fetching and silently substituting the newest token. Keep stdout machine-readable, diagnostics on stderr, and the existing no-success-stdout-on-error policy. No generic arbitrary-URL client or direct database-edit path is added.

## Initialization, supervision and lifecycle

The first complete store contains saved version `0` with empty collections and the empty secret document, with no applied generation. Startup validates layout/schema and paired-head integrity before binding the socket; failure exits non-zero without erasing state. The service adds `StateDirectory=andaru` and `StateDirectoryMode=0700`, retaining `ProtectSystem=strict`, Unix/Netlink-only address families and empty capability sets. Writes are confined to owned storage, not `/etc` or network runtime files.

Retain all saved history in this first persistence slice. The agreed future policy remains `system.history.keepSavedVersions` default 100, minimum 10, with confirmed/last-known-good protections and system-area apply. Implementing deletion now, before live-generation pins exist, would invent a competing retention policy. There is no misleading editable retention setting or no-op prune endpoint here. History can grow until that lifecycle slice; disk-full errors must leave the previous head readable and report save failure. `secure_delete` is not a claim that retained passwords have been removed.

Do not emit SSE events, compare against an imagined empty live firewall, or display “applied” after a save. Networkd/nft files, current links, forwarding, management access and installed unrelated software must remain unchanged. A restart loads the saved head for reads only; it never realizes it.

## Acceptance and evidence

The implementation must prove these behaviours, not only schema wiring:

1. Valid Ethernet/VLAN/bridge/bond/PPPoE and IPv6-delegation topologies round-trip with stable IDs and canonical host addresses. Test an empty bridge, MTU relationships, duplicate numeric subnet IDs, /31 and /32 IPv4 hosts, forbidden address classes, IPv6 link-local scoping, malformed Unicode/JSON and oversized input. Invalid requests leave the head unchanged.
2. Concurrent same-object saves produce one winner and `412` for the stale token; independent-object edits survive together. A→B→A cannot reuse an old token. Unrelated saves leave single-object response bytes/ETag unchanged, while the global version header changes. Collection reads during writes contain exactly one snapshot. Batch creates/local references and secret edits are all-or-nothing; never-existing IDs, tombstones and duplicate-name errors follow the defined precedence.
3. Named secret changes advance their own version and every referring object's version; old saved pairs retain old values. Two core links can deliberately share `WAN_PASSWORD`; independent public-field edits survive a shared-value change, and competing value changes/absence preconditions cannot overwrite one another. No API/CLI/history/error output contains plaintext values or secret-document hashes, including invalid secret commands and schema failures. Test lone-surrogate escapes and valid Unicode without replacement. Reject cross-scope names/selectors and duplicate secret-set commands; removing a link leaves its named secret available to other referrers.
4. Root-required `integration` tests use real SQLite child processes. A test-binary coordinator exercises the owning package's real private transaction/publication helpers and terminates children at secret-commit, configuration-commit and initialization boundaries; do not add public fault-injection APIs, runtime test flags or production callback/no-op scaffolding. Supplement deterministic boundary proof with kill sampling of the unmodified shipped engine. Assert old/new complete pairs only, no published missing secret, no-overwrite initialization even with an existing empty directory, and safe stale-staging recovery. Exercise denied permissions, second-engine locking, missing/corrupt members and ENOSPC in a task-owned size-limited tmpfs at both commit boundaries, including fencing/non-zero exit/reopen. Verify pragmas on newly opened/replaced connections. Process-kill proof does not establish hardware power-loss guarantees.
5. Run the final binary under systemd on retained Debian VM 114 after a new verified Proxmox snapshot. Save and read real records through the root-only Unix API/CLI, restart, verify persistence and denied non-root access, and independently compare nft rules, forwarding and management addresses before/after. Retain the VM. Do not claim a reboot, restore or full firewall test unless actually performed.

No permanent source-text, mock-forwarding or copied-schema tests substitute for behaviour. Contract checks cover actual served schemas/OpenAPI and responses. One fresh adversarial implementation review remains required before publishing product code; this specification's review is not that implementation gate.

## Owner review tradeoffs

- **History lifecycle is deferred, not silently implemented as fixed retention.** This slice keeps all versions; roughly 1,000 distinct maximum-size configuration snapshots alone can consume about 4 GiB, before secret history and overhead. The agreed configurable 100-version policy and live-generation pins remain a later lifecycle slice.
- **Failed publication can retain secret material.** A secret document committed before a failed configuration publication remains root-only and unreferenced until future safe cleanup. It is excluded from configuration reads but not forensically erased; `secure_delete` does not change that.
- **First-schema choices are provisional:** server-generated UUIDv4 network-object IDs rather than the issue's illustrative short IDs, but simple scoped names for secrets; Ethernet/VLAN/bridge/bond/PPPoE definitions now; case-sensitive names without Unicode normalization. Network names reject control and Unicode format characters (including bidi controls), but visually confusable spellings can still differ.
- **Recovery boundary:** startup requires both databases; this is not independent-store restore tooling. Saving never realizes networking, and neither crash tests nor this design promise full firewall recovery.

## Review checkpoint

This document resolves storage publication and save semantics, not live apply or the eventual full firewall declaration. The owner approved the written specification and its named-secret correction before implementation planning. The plan must identify concrete files, pin dependencies, provide red/green checks, and preserve these boundaries; plan review and an execution choice still precede product code. No product code or dependencies are installed by recording this design.
