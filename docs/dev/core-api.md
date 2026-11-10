# Local engine API and CLI

Andaru's root-local Go engine reports observed host interfaces and persists validated **saved** network configuration with named secrets and version-checked writes. Saving never activates networking: there is no networkd/nft apply, live generation, web listener, user/session service or plugin runtime yet. This is not a complete firewall. The contract remains provisional until Andaru 1.0.

## Build

Use Go 1.27.3 on Linux with its default JSON v2 support. Dependencies are pinned in `go.mod`/`go.sum`: a pure-Go SQLite driver, a JSON Schema 2020-12 validator and Linux syscall helpers. Production binaries built with `CGO_ENABLED=0` need neither Go, a C library nor a JavaScript runtime on the appliance.

```sh
BUILD_DIR=$(mktemp -d)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$BUILD_DIR/andaru" ./cmd/andaru
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$BUILD_DIR/andaru-arm64" ./cmd/andaru
go test ./...
```

Linux amd64 is the runtime qualification target; arm64 is currently cross-build-only. Go selects the required patched toolchain through its normal verified toolchain mechanism if the installed compiler is older. Build in a task-owned worktree; do not write binaries into tracked source directories.

## Run and inspect

For a trusted developer build, start the engine as root:

```sh
sudo "$BUILD_DIR/andaru" engine
```

From another terminal:

```sh
sudo "$BUILD_DIR/andaru" ctl links
sudo "$BUILD_DIR/andaru" ctl openapi
```

The default endpoint is `/run/andaru/engine.sock`. An explicitly selected endpoint is supported on both commands; flags precede the CLI operation:

```sh
sudo "$BUILD_DIR/andaru" engine --socket /run/andaru-dev/engine.sock
sudo "$BUILD_DIR/andaru" ctl --socket /run/andaru-dev/engine.sock links
```

The engine creates only the final parent directory when it is absent. Existing parents must be root-owned directories with mode `0700`, not symlinks. The socket is explicitly set to `0600`, independently of umask. Paths must be canonical, absolute and shorter than 108 bytes. Ancestors must also be root-controlled: root-owned, non-symlink directories, with no group/other write access unless protected by the sticky bit. Root-owned private directories directly below `/tmp` or `/var/tmp` can satisfy that rule; user-owned ancestors and aliases such as `/var/run` do not. Use `/run` directly.

A pre-existing endpoint of any kind is refused, including a stale socket. Stop its owning service and investigate it; the program never blindly removes an existing path or recursively deletes a directory. On normal shutdown it closes its listener and removes only the socket matching its ownership evidence, leaving a replacement untouched. systemd owns runtime-directory cleanup after a supervised crash.

Only kernel-verified UID 0 peers are allowed in this increment. User/UID-like HTTP headers, cookies and bearer text do not grant authority. This is local-console/administrator access, **not** the future application-user authentication model. Application authentication and permissions must be implemented before `andaru-web` or any network-facing API is enabled. Use custom sockets only when you trust the endpoint; the CLI has no arbitrary HTTP-host option and follows no redirects.

## Wire contract

The actual OpenAPI 3.1 document is embedded from [`internal/engine/openapi.json`](../../internal/engine/openapi.json) and served by the engine itself.

| Resource | Result |
| --- | --- |
| `GET /api/v1/status/links` | Complete current snapshot as `{"items":[...]}` |
| `GET /api/v1/openapi.json` | The implemented OpenAPI contract |
| `GET /api/v1/schema` | Public declaration/write/redacted-view JSON Schema; private secret-store layout is not served |
| `GET /api/v1/config` | Latest saved snapshot, labelled `state:"saved"`, never active state |
| `GET/POST /api/v1/config/{collection}` | List/create links, segments or zones |
| `GET/PUT/DELETE /api/v1/config/{collection}/{id}` | Read, version-checked replacement or deletion |
| `POST /api/v1/config/changes` | All-or-nothing batch, including references between new objects |
| `GET /api/v1/versions` | Bounded saved history, default 50/max 100; exclusive `before` cursor |
| `GET /api/v1/versions/{id}` | Secret-free immutable saved snapshot |

GET resources support HEAD without a body. Unsupported methods return `405 METHOD_NOT_ALLOWED` with the resource's `Allow` methods; unknown resources return `404 NOT_FOUND`. Missing/unverified/non-root peers get `403 FORBIDDEN` before routing/body processing. Inventory failures return `503 INVENTORY_UNAVAILABLE`, never an empty/partial success. Errors share `{"error":{"code":"...","message":"...","details":[]}}`; field details use safe request-relative JSON Pointers and conflicts may add redacted `error.current`. JSON responses are `no-store`; diagnostics never quote secret values.

`ctl` writes successful validated JSON to stdout. Transport, API, malformed-envelope and oversized-response failures exit `1` without success stdout; invalid command/arguments exit `2`. Error diagnostics are on stderr. A broken stdout pipe can of course truncate a write; the CLI reports write failure rather than returning a successful exit. Help is available with `--help`.

Each link contains `ifIndex`, `name`, `linkType`, `mtu`, `operState`, `flags`, optional `linkAddress`, and `addresses`. Addresses contain `family` (`ipv4`/`ipv6`), the host `address`, `prefixLength` and `scope`. Empty lists are `[]`, not `null`. Links are ordered by current kernel index; address-list order is not policy order. These are observations, **not** immutable configuration IDs or permanent-MAC identity. Loopback `UNKNOWN` and an all-zero link-layer address are preserved when reported. An absent link-layer address is omitted, not invented.

The engine runs exactly `/usr/bin/ip -j address show` through an argument vector, without a shell or command override. It ignores unrelated tool JSON fields, validates required link/address data and fails the entire read on invalid data. Fixed bounds are a 2-second child deadline, 4 MiB stdout, 4 KiB retained stderr, and a 5-second/8 MiB CLI response budget. No stale snapshot cache is used.

Any stderr diagnostic from the fixed inventory command also fails the read, even
with exit status zero: iproute2 can report an interrupted, inconsistent netlink
dump that way. The API returns `503`, not a potentially incomplete snapshot.

## Saved configuration and named secrets

The engine owns `/var/lib/andaru` (`0700`), with `configuration/config.db`,
`configuration/secrets.db`, their SQLite sidecars and `engine.lock` protected as
root-only files (`0600`). Active state must use a **local filesystem** supporting
WAL/shared memory and no-replace rename, not an NFS/SMB backup mount. An engine-only
`--state-dir /absolute/private/path` selects isolated development state. A second
engine cannot own the same state directory, even with another socket.

```sh
sudo andaru ctl config show
sudo andaru ctl config list links
printf '%s\n' '{"name":"lan"}' | sudo andaru ctl config create zones
# Use the exact ID and version returned by your preceding read:
sudo andaru ctl config get zones "$ID"
printf '%s\n' '{"name":"trusted-lan"}' |
  sudo andaru ctl config replace --version "$VERSION" zones "$ID"
sudo andaru ctl versions list --limit 10
sudo andaru ctl versions get "$SAVED_VERSION"
sudo andaru ctl config changes < protected-changes.json
```

Network-object IDs are stable server-generated UUIDv4 identifiers; mutable names
do not break references. Secrets instead use simple scoped names such as
`WAN_PASSWORD` or `CF_DNS_TOKEN`, independently of any runtime environment binding.
The initial network API uses only the core scope. A reference is
`{"secret":"WAN_PASSWORD"}`. A create/set command adds `set` and
`expectedVersion`: `"0"` requires absence, otherwise the secret's current version.
Send secret-bearing JSON through stdin from protected storage, **never argv**.
Read-back `isSet`, `updatedAt` and `version` are metadata, not write commands.
Omitting a PPPoE password on replacement retains its reference.
Cancelled stdin reads exit without sending a request. A blocked reader may remain
until the short-lived CLI process exits; the command never retries a save.

Every replace/delete requires an explicit object version. The CLI constructs the
strong `If-Match`; the server returns `428` if missing, `412` if stale and `422`
for invalid fields/references or duplicate names. No wildcard overwrite or
automatic mutation retry exists. A shared named-secret change versions all its
referrers together. Unrelated object edits do not invalidate one another.
Single-object GET/HEAD ETags cover only that object; the global snapshot version
is `Andaru-Saved-Version`, not part of the ETag-covered body. Transformed write
responses return versions but no ETag.

Requests are bounded to 1 MiB, depth 32 and 100 operations; each stored document
to 4 MiB and the declaration to 10,000 objects. Nulls, duplicate keys, malformed
Unicode, unknown fields and non-integer numeric wire forms are rejected.
Validation resolves topology, VLAN/PPPoE MTUs and IPv4/IPv6 references without
requiring the declared hardware to be present. It never changes host interfaces.

The configuration database's head commit publishes a complete already-durable
configuration/secret pair; attached WAL databases are not falsely treated as one
atomic transaction. Startup refuses incomplete, corrupt or foreign stores
without wiping them. Publication I/O failure fences the store, returns `503`
where possible and exits non-zero for systemd to reopen/revalidate. A lost save
response can hide a committed save: read current state before retrying.
Preparation has 3 seconds and publication a separate 2-second bound; mutation
clients allow 10 seconds, reads 5, responses 8 MiB.

All history is retained in this slice; configurable 100-version pruning awaits
live-generation protections. Failed publication can retain a root-only orphan
secret document. `secure_delete` is enabled but does not erase retained history
or promise forensic removal from WALs, snapshots, SSDs or backups. No secret
export, encrypted backup, independent-store restore or migration service exists.

## Supervision

The source unit is [`packaging/systemd/andaru-engine.service`](../../packaging/systemd/andaru-engine.service). It expects the trusted binary at `/usr/bin/andaru`; Debian package construction is a later increment, not an installer hidden in the API.

For a controlled development installation, first ensure there is no unrelated existing `andaru` command, engine unit or `/run/andaru` directory. Install the trusted binary and unit using normal administrator tooling, run `systemctl daemon-reload`, and start the service. Do not overwrite foreign paths or construct systemd enablement symlinks manually.

```sh
sudo systemctl start andaru-engine.service
sudo andaru ctl links
sudo systemctl restart andaru-engine.service
sudo journalctl -u andaru-engine.service
```

`Type=exec` is not API readiness: an automated caller should wait under one bounded overall deadline for a successful CLI read. The shipped unit runs as root with no effective/bounding/ambient capabilities, `NoNewPrivileges`, strict filesystem/home protection, private temporary storage and kernel/control-group protections. It allows `AF_UNIX AF_NETLINK` so Debian's read-only `ip` operation can inspect the host namespace. It intentionally does not use `PrivateNetwork` or depend on `network-online.target`. It opens no TCP/UDP listener, and its stop action never flushes firewall rules.

SIGINT/SIGTERM cancels request contexts, gives HTTP shutdown up to 3 seconds, then closes held connections if necessary. systemd supplies a 5-second stop budget. This unit's current lack of network-administration capabilities is deliberate: a later realizer must request only the privileges its actual mutation path requires.

## Verification

The ordinary suite runs without elevated privileges. Explicit kernel-credential/lifecycle tests use an integration build tag and require root; they fail clearly rather than silently skipping the proof. Build the test executable on the developer host and transfer it to the Debian VM, without installing a Go compiler there:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/config.test" ./internal/config
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/engine.test" ./internal/engine
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/ctl.test" ./cmd/andaru
```

Run that test executable as root in the lab. The UID/GID 65534 helper uses an owned traversable sandbox and deliberately relaxed **test-only** socket permissions after startup, proving `SO_PEERCRED` independently of filesystem access. Other cases cover existing files/symlinks/live/stale sockets, incorrect owners/ancestors, umask-independent modes, replacement preservation, cancellation, forced connection closure and clean restart. Production gets no permission-bypass or test-helper flag.
The store's non-root helper copies its executable into a root-owned traversable
sandbox and explicitly sets mode `0755`, so direct `go test` runs also work when
Go's build directory is private or the caller uses a restrictive umask.

The store and engine fault suites require root plus `unshare`/`mount` in the lab.
Mounts and ENOSPC are isolated in private test mount namespaces, never granted
to the production service. Set `ANDARU_TEST_BINARY` to the absolute tested
`andaru` executable when running `engine.test`: its fencing proof invokes the
actual program, reads `503`, requires exit `1`, then verifies complete reopen.
No product test-helper flag or fault-injection callback is installed.

### Earlier read-only Debian lab qualification

On 2026-11-10 the amd64 binary and source unit were installed on retained Proxmox VM 114 `andaru-lab` (`proxmox-4`, Debian 13.7), after snapshot `pre-core-api-20261110-000136` completed and its entry was checked. This is the [persistent Proxmox lab](../research/podman-compose-spike.md#retained-test-environment), not the separate macOS serial-only QEMU lab.

Executed proof:

- All three Go unit packages passed; the actual compiled API/Unix integration suite passed as root in the VM. Both amd64 and arm64 executables were observed as statically linked ELF files; arm64 was not run.
- `ctl links` matched the VM's independently read `ip` link names, indexes, states, MTUs, current link addresses and IPv4/IPv6 host addresses/prefixes for `lo` and `eth0`. `ctl openapi` returned version 3.1.1 with exactly the two implemented paths.
- The supervised engine was UID 0 with `CapEff=0`, `NoNewPrivs=1`, a Unix-only listener, parent `0700` and socket `0600`. A normal non-root CLI call was denied; the tagged test also proved denial for real UID/GID 65534 after filesystem access was deliberately permitted in its fixture.
- Stopping the service removed its socket; the CLI then exited `1` with empty stdout. Restart became ready within the bounded check. Unsupported command exit was `2`; a non-root engine invocation was refused without creating its endpoint.
- The original stateless nft ruleset, both forwarding sysctls and management link/address state were unchanged. No link, bridge, VLAN, forwarding or filtering configuration was applied.

That earlier qualification left the lab engine active and not boot-enabled; it did not exercise saved writes. The subsequent saved-configuration qualification below is separate evidence. Neither qualification proves a reboot, snapshot restore, encrypted backup or complete [firewall MVP](https://github.com/andaru-project/andaru/issues/1).

### Saved-configuration Debian lab qualification

On 2026-11-10 the Go 1.27.3, CGO-disabled amd64 build of code commit
`3946ba35c207f0e9f97b4c6e2f05539cd1af4e45` was installed under systemd in
VM 114 after verified snapshot `pre-saved-config-20261110-074903`.
Binary SHA-256:
`a39578271b28c4ad8900df4f64237bf219353360a355ce5bdeb195180ede1ad0`.
The installed unit SHA-256 was
`5ef41ec9e7d533a4571e6ac2dca7423bb0361b8ecf6d1801402af75d55cde61e`.
Previous owned binary/unit copies remain with the task's staged lab artifacts.

Executed evidence:

- All ordinary and race-enabled Go suites passed. The final config, engine and
  CLI integration binaries all passed as root in Debian; engine fencing invoked
  the installed `/usr/bin/andaru`, not a simulated server.
- Native installed CLI commands created a VLAN/PPPoE/delegated-IPv6 declaration,
  changed a shared named password, versioned both referrers, retained disjoint
  edits, rejected stale/A→B→A edits and an atomic stale batch, and retained saved
  state through service restart. Non-root access was denied.
- Deterministic test helpers covered six crash boundaries and ENOSPC at both
  publication stages. A separate actual-engine test read `503`, observed drain
  and exit `1`, then reopened a valid pair after space was restored.
- Eight SIGKILL samples against the unmodified installed engine, after actual
  secret-bearing HTTP request transmission at 0/1/2/5/10/20/50/100 ms, produced
  five old-complete and three new-complete outcomes. Every restart passed engine
  document/hash/reference validation. Sampling is not arbitrary power-loss proof.
- Independent pre/post observations matched exactly for stateless nft rules,
  IPv4/IPv6 forwarding and management link/address state (`lo`, `eth0`).
  The unit retained zero effective capabilities, `NoNewPrivs=1`, a Unix-only
  listener and root-only runtime/state permissions. Boot enablement stayed disabled.

The final adversarial review added regression coverage for detached publication,
cancelled preparation, consistent collection snapshots, A→B→A concurrency, batch
error precedence, secret-schema redaction and isolated topology constraints.
Storage failures journal trusted stage labels and numeric SQLite/OS error codes,
never raw driver messages, input values or hashes. Duplicate `If-Match` field
lines are rejected. An interrupted first initialization with only an empty,
root-owned private marker can restart; unmarked data is still refused.

Review fixes were requalified on the same retained VM using code commit
`880fb575a20b696f4f6047cbddae8ce11ca9e9f4` (`vcs.modified=false`),
binary SHA-256
`e7d3a7d28f73232cdc9bf0157d56f28d8f643327faae9e109fddff248ed61c4c`.
All three root integration binaries passed again, including the installed engine's
ENOSPC fence/drain/reopen test. Eight fresh SIGKILL samples again yielded five
old-complete and three new-complete pairs. The saved snapshot stayed at version 7,
and the exact host-network baseline stayed unchanged. The installed CLI also
exited `1` on SIGINT while waiting for stdin without submitting a save.

The cleanup batch left **saved version 7 with an empty topology**; the fake
`WAN_PASSWORD` test secret remains unused under the stated retention policy.
The service is active. Linux arm64 was cross-built, not run. No real network
configuration, reboot or recovery restore was performed.

`govulncheck v1.9.0` reported zero symbol-level and imported-package findings.
Two advisories exist in unimported packages of transitive `x/text v0.14.0`:
[GO-2026-6629](https://pkg.go.dev/vuln/GO-2026-6629) and
[GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970). Neither affected package
is imported by this build. Review by 2026-12-10 or before changing text/auth/i18n
dependencies; this is a reachability-based deferral, not a clean-module claim.
