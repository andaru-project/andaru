# Local engine API and CLI

This is Andaru's first working core increment: a real Go engine and CLI reporting the host's current interfaces through a protected Unix API. It is **read-only**, not a complete firewall. It neither takes over host networking nor adds a web listener, configuration writes, users/sessions, packet-filter policy or a plugin runtime. The contract remains provisional until Andaru 1.0.

## Build

Use Go 1.27.3. The module has no third-party Go dependencies. Production executables do not need Go or a JavaScript runtime installed on the appliance.

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

HEAD uses the corresponding authorization/status behaviour without a body. Unsupported methods on known resources return `405 METHOD_NOT_ALLOWED` and `Allow: GET, HEAD`. Unknown resources return `404 NOT_FOUND`. Missing, unverified or non-root peers receive `403 FORBIDDEN` before routing or host reads. Inventory command/parsing/size/deadline failures return `503 INVENTORY_UNAVAILABLE`, never an empty or partial success. Errors have `{"error":{"code":"...","message":"...","details":[]}}`, JSON Content-Type and `Cache-Control: no-store`; raw command diagnostics remain in the engine journal, not API responses.

`ctl` writes successful validated JSON to stdout. Transport, API, malformed-envelope and oversized-response failures exit `1` without success stdout; invalid command/arguments exit `2`. Error diagnostics are on stderr. A broken stdout pipe can of course truncate a write; the CLI reports write failure rather than returning a successful exit. Help is available with `--help`.

Each link contains `ifIndex`, `name`, `linkType`, `mtu`, `operState`, `flags`, optional `linkAddress`, and `addresses`. Addresses contain `family` (`ipv4`/`ipv6`), the host `address`, `prefixLength` and `scope`. Empty lists are `[]`, not `null`. Links are ordered by current kernel index; address-list order is not policy order. These are observations, **not** immutable configuration IDs or permanent-MAC identity. Loopback `UNKNOWN` and an all-zero link-layer address are preserved when reported. An absent link-layer address is omitted, not invented.

The engine runs exactly `/usr/bin/ip -j address show` through an argument vector, without a shell or command override. It ignores unrelated tool JSON fields, validates required link/address data and fails the entire read on invalid data. Fixed bounds are a 2-second child deadline, 4 MiB stdout, 4 KiB retained stderr, and a 5-second/8 MiB CLI response budget. No stale snapshot cache is used.

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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/engine-integration.test" ./internal/engine
```

Run that test executable as root in the lab. The UID/GID 65534 helper uses an owned traversable sandbox and deliberately relaxed **test-only** socket permissions after startup, proving `SO_PEERCRED` independently of filesystem access. Other cases cover existing files/symlinks/live/stale sockets, incorrect owners/ancestors, umask-independent modes, replacement preservation, cancellation, forced connection closure and clean restart. Production gets no permission-bypass or test-helper flag.

### Observed Debian lab result

On 2026-11-10 the amd64 binary and source unit were installed on retained Proxmox VM 114 `andaru-lab` (`proxmox-4`, Debian 13.7), after snapshot `pre-core-api-20261110-000136` completed and its entry was checked. This is the [persistent Proxmox lab](../research/podman-compose-spike.md#retained-test-environment), not the separate macOS serial-only QEMU lab.

Executed proof:

- All three Go unit packages passed; the actual compiled API/Unix integration suite passed as root in the VM. Both amd64 and arm64 executables were observed as statically linked ELF files; arm64 was not run.
- `ctl links` matched the VM's independently read `ip` link names, indexes, states, MTUs, current link addresses and IPv4/IPv6 host addresses/prefixes for `lo` and `eth0`. `ctl openapi` returned version 3.1.1 with exactly the two implemented paths.
- The supervised engine was UID 0 with `CapEff=0`, `NoNewPrivs=1`, a Unix-only listener, parent `0700` and socket `0600`. A normal non-root CLI call was denied; the tagged test also proved denial for real UID/GID 65534 after filesystem access was deliberately permitted in its fixture.
- Stopping the service removed its socket; the CLI then exited `1` with empty stdout. Restart became ready within the bounded check. Unsupported command exit was `2`; a non-root engine invocation was refused without creating its endpoint.
- The original stateless nft ruleset, both forwarding sysctls and management link/address state were unchanged. No link, bridge, VLAN, forwarding or filtering configuration was applied.

The lab engine is left **active, not boot-enabled**, with staged artifacts retained for continued development. No VM reboot, snapshot restore, package upgrade, configuration mutation or firewall boot proof is claimed. This qualification does not complete the [firewall MVP](https://github.com/andaru-project/andaru/issues/1): durable saved configuration, version-checked writes, safe networkd/nft apply, independent rollback/boot, application authentication, DHCP/DNS and the owner's VLAN/PPPoE/IPv6 cases remain subsequent real increments, not stubbed endpoints.
