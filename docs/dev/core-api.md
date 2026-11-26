# Local engine API and CLI

Andaru's root-local Go engine reports observed host interfaces and persists validated **saved** network configuration with named secrets and version-checked writes. Saving never activates networking: there is no networkd/nft apply, live generation, web listener, user/session service or plugin runtime yet. This is not a complete firewall. The contract remains provisional until Andaru 1.0.

## Build

Use Go 1.27.4 on Linux with its default JSON v2 support. Dependencies are pinned in `go.mod`/`go.sum`: a pure-Go SQLite driver, a JSON Schema 2020-12 validator and Linux syscall helpers. Production binaries built with `CGO_ENABLED=0` need neither Go, a C library nor a JavaScript runtime on the appliance.

```sh
BUILD_DIR=$(mktemp -d)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$BUILD_DIR/andaru" ./cmd/andaru
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$BUILD_DIR/andaru-arm64" ./cmd/andaru
go test ./...
```

CI targets native Linux amd64 and arm64 on Ubuntu 24.04; the appliance image is qualified separately on lab boxes. Native CI does not establish ARM appliance boot. Go selects the required patched toolchain through its normal verified toolchain mechanism if the installed compiler is older. Build in a task-owned worktree; do not write binaries into tracked source directories.

## Installing

Andaru ships as its own operating system image, `ghcr.io/andaru-project/andaru`, built on Fedora CoreOS for x86_64 and aarch64. Design: [Andaru as an appliance image](../superpowers/specs/2026-11-18-appliance-image-design.md). The Debian package and the APT repository are no longer built.

There is no installer yet: install media are slice 2 of that design. Until then, the lab route is the install route: install stock Fedora CoreOS with `coreos-installer` and an Ignition config that writes byte-identical copies of the image's `/etc/containers/policy.json`, `/etc/containers/registries.d/andaru.yaml`, `/etc/pki/containers/andaru.pub` and `/etc/pki/containers/andaru-nightly.pub`, then rebases to `ostree-image-signed:docker://ghcr.io/andaru-project/andaru:stable`.

**Signatures.** Every release image is signed with Andaru's cosign key. The image refuses an update that the key did not sign:

- the public key is [`image/cosign.pub`](../../image/cosign.pub), installed as `/etc/pki/containers/andaru.pub`;
- the key file's SHA-256 is `91be11def530307f6afc91290006b25ea5ddfb4c3ce0e98f4bf9e8bb1899b3b7`;
- `/etc/containers/policy.json` requires that signature for `ghcr.io/andaru-project`, except `ghcr.io/andaru-project/andaru-nightly`, which requires the nightly key instead (see Nightlies below). Other registries keep Fedora CoreOS's behaviour.

To check an image by hand: `cosign verify --key image/cosign.pub ghcr.io/andaru-project/andaru:<version>`.

**From source:** `podman build -f image/Containerfile --build-arg VERSION=0.0.0-dev -t andaru:dev .` builds the image for this machine's architecture, `sh scripts/ci/rechunk.sh andaru:dev` rebuilds its layers as releases are, and `sh scripts/ci/image-smoke.sh andaru:dev 0.0.0-dev` checks it.

**Releasing** (maintainers): push a tag on `main`. `v1.2.3` is a release, `v1.2.3-a.N` (alpha), `v1.2.3-b.N` (beta) and `v1.2.3-rc.N` (release candidate) are prereleases; with the dot, `rc.10` sorts after `rc.9`. Tags must be signed: `git tag -s v1.2.3`. The release workflow builds both architectures, pushes them unsigned as `1.2.3-amd64` and `1.2.3-arm64`, signs the multi-architecture index in the `image-release` environment, and only then tags it `1.2.3`, and `stable` for a release.

**Nightlies.** [andaru-project/andaru-nightly](https://github.com/andaru-project/andaru-nightly) builds `main` every night it moved, and publishes it as `ghcr.io/andaru-project/andaru-nightly`. Each nightly gets a version tag, `v<YYYY-MM-DD>.<N>.<commit>` (the UTC date, the day's build number and the first 7 characters of the commit), and the floating tag `nightly`. They are signed with a separate nightly key, [`image/cosign-nightly.pub`](../../image/cosign-nightly.pub) (SHA-256 of the file `81b5e2f56d50905656c43ff646728388aca76e35ddcc1cfc26ffb097053f1f9d`). The image's policy trusts that key only for the `andaru-nightly` repository, so it can never sign a release. A box follows nightlies with `bootc switch --enforce-container-sigpolicy ghcr.io/andaru-project/andaru-nightly:nightly`.

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

Create the first administrator from the root console with `andaru ctl users bootstrap-admin --login <login> [--display-name <name>]`. It asks for the password twice, then for `BOOTSTRAP` to confirm.

The default endpoint is `/run/andaru/engine.sock`. An explicitly selected endpoint is supported on both commands; flags precede the CLI operation:

```sh
sudo "$BUILD_DIR/andaru" engine --socket /run/andaru-dev/engine.sock
sudo "$BUILD_DIR/andaru" ctl --socket /run/andaru-dev/engine.sock links
```

The engine creates only the final parent directory when it is absent. Existing parents must be root-owned directories with mode `0700`, not symlinks. The socket is explicitly set to `0600`, independently of umask. Paths must be canonical, absolute and shorter than 108 bytes. Ancestors must also be root-controlled: root-owned, non-symlink directories, with no group/other write access unless protected by the sticky bit. Root-owned private directories directly below `/tmp` or `/var/tmp` can satisfy that rule; user-owned ancestors and aliases such as `/var/run` do not. Use `/run` directly.

A pre-existing endpoint of any kind at the root socket's path is refused, including a stale socket. Stop its owning service and investigate it; the program never blindly removes an existing path or recursively deletes a directory. The web socket is the one exception: its directory comes from tmpfiles.d, which systemd does not clear, so the engine holds a lock beside it (the socket's path with `.lock` appended, a root-owned 0600 file) and, under that lock, replaces a root-owned socket nothing listens on. Anything else at that path is refused, and a second engine on the same web socket fails on the lock. On normal shutdown it closes its listener and removes only the socket matching its ownership evidence, leaving a replacement untouched. systemd owns runtime-directory cleanup after a supervised crash.

Only kernel-verified UID 0 peers are allowed in this increment. User/UID-like HTTP headers, cookies and bearer text do not grant authority. This is local-console/administrator access, **not** the future application-user authentication model. Application authentication and permissions must be implemented before `andaru-web` or any network-facing API is enabled. Use custom sockets only when you trust the endpoint; the CLI has no arbitrary HTTP-host option and follows no redirects.

## Wire contract

The actual OpenAPI 3.1 document is embedded from [`internal/engine/openapi.json`](../../internal/engine/openapi.json) and served by the engine itself.

| Resource | Result |
| --- | --- |
| `GET /api/v1/status/links` | Complete current snapshot as `{"items":[...]}` |
| `GET /api/v1/status/lldp` | Live LLDP (802.1AB) neighbors reported by systemd-networkd (`networkctl lldp`) as `{"items":[...]}` |
| `GET /api/v1/status/usage` | How hard the box is working now: processor tick counters, load, memory and swap, the room on each device and ZFS pool (one row each, however often it is mounted; FUSE, network and memory file systems are not listed), the connection-tracking table, temperatures. Read at each request; the processor counters are cumulative |
| `GET /api/v1/status/traffic` | The last hour of every network interface's cumulative counters, read every 5 seconds and kept in the engine's memory only. `?since=` with the time of the newest reading a client holds gives only what came after |
| `GET /api/v1/status/system` | What the box is, read from `/proc` and `/sys` at each request: the Andaru version, the kernel, the CPU and its features, the hardware as its firmware names it. What a machine does not report is an empty string or list, and a null boot time |
| `GET /api/v1/openapi.json` | The implemented OpenAPI contract |
| `GET /api/v1/schema` | Public declaration/write/redacted-view JSON Schema; private secret-store layout is not served |
| `GET /api/v1/config` | Latest saved snapshot, labelled `state:"saved"`, never active state |
| `GET/POST /api/v1/config/{collection}` | List/create links, segments or zones |
| `GET/PUT/DELETE /api/v1/config/{collection}/{id}` | Read, version-checked replacement or deletion |
| `POST /api/v1/config/changes` | All-or-nothing batch, including references between new objects |
| `GET /api/v1/versions` | Bounded saved history, default 50/max 100; exclusive `before` cursor |
| `GET /api/v1/versions/{id}` | Secret-free immutable saved snapshot |
| `GET /api/v1/preview/network` | Files a saved version would hand to systemd-networkd and pppd on this host, redacted, with findings; `state:"preview"`; optional `?savedVersion=N` |
| `GET/POST /api/v1/config/aliases/{addresses\|ports}`, `…/{id}` | Address aliases and port aliases, which rules, NAT entries and the management section name |
| `GET /api/v1/config/aliases/search` | Similarity search over saved address or port aliases (`type`, optional `q`, `kind`, `family`, `excludeGroups`, `exclude`, `limit`); when `q` is omitted or blank, returns eligible aliases ordered by name up to `limit`, otherwise returns ranked matches with highlight segments |
| `GET/POST /api/v1/config/rules` | The firewall rules in execution order with the list's `version`; `POST ?before=<rule id>` places a new rule |
| `GET/PUT/DELETE /api/v1/config/rules/{id}` | One rule, version-checked like any object |
| `PUT /api/v1/config/rules/order` | Reorder the rules: every rule ID once, `If-Match: "order.N"`; replies with the new `order` and its `version` |
| `GET/POST /api/v1/config/nat/{port-forwards\|one-to-one}` | An inbound NAT list in evaluation order with its `version`; `POST ?before=<entry id>` places a new entry |
| `GET/PUT/DELETE /api/v1/config/nat/{list}/{id}` | One NAT entry, version-checked like any object |
| `PUT /api/v1/config/nat/{list}/order` | Reorder a NAT list, as for the rules |
| `GET/PUT/DELETE /api/v1/config/management`, `/api/v1/config/nat/outbound` | Single objects, `If-Match: "management.N"` / `"nat.outbound.N"`, `0` while unset |
| `GET/POST /api/v1/config/dhcp`, `GET/PUT/DELETE /api/v1/config/dhcp/{id}` | DHCP servers, one per served segment |
| `GET/PUT/DELETE /api/v1/config/dns` | The DNS service, `If-Match: "dns.N"`, `0` while unset |
| `POST /api/v1/backups` | Portable ZIP without secrets, or passphrase-encrypted age archive when secrets are explicitly selected |
| `POST /api/v1/restores/preview` | Validate an uploaded archive and report selected-store changes without saving |
| `POST /api/v1/restores` | Replace selected saved stores using the preview's saved-head precondition; never apply networking |

GET resources support HEAD without a body. Unsupported methods return `405 METHOD_NOT_ALLOWED` with the resource's `Allow` methods; unknown resources return `404 NOT_FOUND`. Missing/unverified/non-root peers get `403 FORBIDDEN` before routing/body processing. Inventory failures return `503 INVENTORY_UNAVAILABLE`, never an empty/partial success. Errors share `{"error":{"code":"...","message":"...","details":[]}}`; field details use safe request-relative JSON Pointers and conflicts may add redacted `error.current`. JSON responses are `no-store`; diagnostics never quote secret values.

`ctl` writes successful validated JSON to stdout. Transport, API, malformed-envelope and oversized-response failures exit `1` without success stdout; invalid command/arguments exit `2`. Error diagnostics are on stderr. A broken stdout pipe can of course truncate a write; the CLI reports write failure rather than returning a successful exit. Help is available with `--help`.

Each link contains `ifIndex`, `name`, `linkType`, `mtu`, `operState`, `flags`, optional `linkAddress`, optional `kind` (the kernel link kind, such as `vlan` or `bridge`; absent for a physical device), optional `permanentAddress` (the hardware address the device cannot change, which a saved Ethernet link names; a virtual link reports the one it inherits or its current address), and `addresses`. Addresses contain `family` (`ipv4`/`ipv6`), the host `address`, `prefixLength` and `scope`. Empty lists are `[]`, not `null`. Links are ordered by current kernel index; address-list order is not policy order. These are observations, **not** immutable configuration IDs; only `permanentAddress` of a link without a `kind` is the identity a saved Ethernet link matches. Loopback `UNKNOWN` and an all-zero link-layer address are preserved when reported. An absent link-layer address is omitted, not invented.

The engine runs exactly `/usr/bin/ip -d -j address show` through an argument vector, without a shell or command override. It ignores unrelated tool JSON fields, validates required link/address data and fails the entire read on invalid data. Fixed bounds are a 2-second child deadline, 4 MiB stdout, 4 KiB retained stderr, and a 5-second/8 MiB CLI response budget. No stale snapshot cache is used.

Any stderr diagnostic from the fixed inventory command also fails the read, even
with exit status zero: iproute2 can report an interrupted, inconsistent netlink
dump that way. The API returns `503`, not a potentially incomplete snapshot.

## Saved configuration and named secrets

The engine owns `/var/lib/andaru` (`0700`), with `configuration/config.db`,
`configuration/secrets.db`, their SQLite sidecars and `engine.lock` protected as
root-only files (`0600`). Active state must use a **local filesystem** supporting
WAL/shared memory and no-replace rename, not an NFS/SMB backup mount. An operator may
mount a local filesystem at `/var/lib/andaru` before first setup, or stop Andaru and
move an existing tree onto that mount. There is no migration command. A missing mount
must not be replaced by an empty directory. `/var/lib/andaru-kept/` on the root disk
holds the write-once first LAN, the last successful LAN, and the explicitly stored
post-reset network. An engine-only
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

A change batch may carry `"expectedSavedVersion": "N"`: the batch is then refused
with `412` at `/expectedSavedVersion`, before any operation, unless N is still
the saved head, so a save from another session between a client's read and its
commit is never overwritten.

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
or promise forensic removal from WALs, snapshots, SSDs or backups.

### Settings across Andaru versions

On start, the engine saves a head of an older schema as a new version of the current one, and a head of a newer schema (after a rollback) opens on a copy of the newest version this Andaru reads ([settings design](../superpowers/specs/2026-11-22-settings-across-andaru-versions-design.md)). A breaking change may come with decisions; while the head needs any, the settings are held ([safe mode](../superpowers/specs/2026-11-22-settings-safe-mode-design.md)):

- Saves and applies answer `409 SETTINGS_UPGRADE_PENDING`. Reads, versions (with their `questions`), backups and restores of a backup that needs no decision still work.
- `POST /api/v1/config/upgrade/preview` with `{"choices": {…}}` returns the head's questions with the choices given so far (`configuration.read`).
- `POST /api/v1/config/upgrade` with `{"choices": {…}, "expectedSavedVersion": "…"}` answers every question and saves the result (`configuration.write`). A missing or unknown choice is `422`, a stale head `412`.

No decision record exists yet; the first comes with the first breaking change that needs one.

## Portable backups and saved-state restore

Portable backups use the running engine's validated logical configuration stores,
not copies of SQLite files:

```sh
sudo andaru ctl backup create --output andaru.zip
sudo andaru ctl backup create --output andaru-secrets.age --include-secrets
sudo andaru ctl backup inspect --input andaru.zip
sudo andaru ctl backup restore --input andaru.zip --stores configuration
sudo andaru ctl backup restore --input andaru-secrets.age --stores configuration,configurationSecrets
```

Creation never overwrites an existing output path. The default ZIP contains the
configuration declaration but no secret values. `--include-secrets` requires a
passphrase entered twice on the controlling terminal and produces a
passphrase-encrypted age file; the passphrase has no flag or environment-variable
form. Inspecting an encrypted file prompts once. The archive input limit is
16 MiB; decrypted ZIP data is limited to 12 MiB, its manifest to 64 KiB and
each document to 4 MiB. Passphrases are at most 1024 bytes. Archives contain
format/source metadata, payload sizes and SHA-256 digests, the saved version and
the observed live/pending generation relationship. The `andaruVersion` source
label is the running binary's embedded package build identity, not a later value
read from dpkg metadata. Archives do not contain database history, WAL files,
package binaries or a general filesystem backup.

Restore accepts only an uploaded archive and an explicit selection of
`configuration` and/or `configurationSecrets`. It validates every included
document before selecting stores, prints a preview, then requires `RESTORE`
typed exactly on the controlling terminal. The commit is compare-and-swap
against the saved head returned by that preview; a concurrent save refuses it
rather than retrying or overwriting the newer state. Restore records a new
saved version but never writes generation files, reloads a service, changes
firewall rules or applies networking.

Configuration-only restore deliberately marks imported secret references
unresolved, even when a destination secret has the same name. Explicitly bind
the reference with an ordinary version-checked configuration replacement, or
restore matching configuration and secrets together, before an apply can use
it. Secrets-only restore keeps the destination declaration. Compatible schema-1
documents remain schema 1 unless unresolved references require schema 2, the
import is already schema 2, or the destination declaration is already schema 2.
Supported older archive documents are validated and converted only when their
configuration store is selected.

These endpoints are an explicit root-console exception to the future
application-user authorization model: only a kernel-verified UID 0 peer on the
root-local Unix socket can use them. No web/IP backup endpoint, scheduled
backup, passphrase recovery, automatic restore, automatic apply or network
change is provided.

An older engine cannot open a schema-2 saved head. Going back to an older
image does not revert state.

### Qualification evidence (2026-11-15)

The candidate `0.4.0~rc3-4`
(`sha256:6196998d10af804c9823a12fac5d490d2f9542d10f3e2f37cb78af349a213ec5`)
passed the final scenario on Debian 13 netplan VM 114 and ifupdown VM 117.
Detailed per-VM logs were captured during qualification. Both fixtures exercised
plain and encrypted creation, independent age decryption,
configuration-only unresolved references, explicit rebinding and secrets-only
declaration preservation. Fixture saves advanced the saved head from 30 to 40;
schema 2 survived an engine restart.

Five default archives captured while five saves ran each matched the fixture
zones in their recorded historical source version. Concurrent secret-bearing requests
returned exactly one `200` and one `409`; the second request did not bypass single-operation admission.
Holding the real generation lock produced `503` after 3.002 seconds, then `200`
after release. Work-factor-18 encryption increased engine `VmHWM` by 261800 KiB
on netplan and 263432 KiB on ifupdown; available RAM afterwards was 3280580 KiB
and 3297288 KiB respectively on these 4 GiB guests. These measurements describe
the qualified fixtures, not a universal memory limit.

A normal explicit network apply and rollback was qualified separately. Backup
and saved-only restore changed no live generation pointer, stateless firewall
rules or network-service invocation ID; restarting the engine also left
systemd-networkd's invocation unchanged. An initial VM 114 terminal-log failure
had no retained per-command log, so it is not attributed retroactively. age
1.2.1 prints its prompt before disabling terminal echo; the independent fixture
now waits for age's echo-off state only. Andaru prompt replies remain immediate
and all secrecy assertions remain enabled; the final scenario passed on both
VMs.

Stopped disk-only snapshots named
`portable-backups-before-20261115` were taken before testing. Afterwards both
VMs were restored to those snapshots, booted with a healthy rc3-2 engine and
verified at saved version 30/schema 1 with `/root/andaru-portable-lab` absent.
Both were then shut down; Proxmox reported them stopped, matching their initial
state. This qualification does not establish physical power-loss behavior,
arm64 hardware, NFS/SMB durability or a full cryptographic audit.

A restore journal whose configuration must still be published but cannot be here, because a rollback set aside the version it expected or because its configuration needs decisions, is abandoned at start instead of blocking it; a replay of it answers `409 RESTORE_ABANDONED`. A configuration it published that a rollback then set aside counts as done. A journal of a newer format is left alone.

## Factory reset

`andaru ctl factory-reset run` deletes every Andaru account, setting, saved version,
generation and audit record under `/var/lib/andaru`, and brings the box back on a
network the operator chose beforehand. It is root-only and interactive, with no
flags. A WebUI control for the same reset comes with the WebUI.

```sh
# Optional, beforehand: keep the current saved head as the post-reset network.
sudo andaru ctl kept store-recovery
sudo andaru ctl factory-reset run
sudo andaru ctl factory-reset status
# Only before andaru-factory-reset.service has begun stopping Andaru:
sudo andaru ctl factory-reset abort
```

`run` asks for the new `admin` password (empty means `admin`; anything else must
meet the password policy), offers a portable backup with secrets, asks whether to
reboot when done (default no), and requires the phrase `FACTORY RESET`. It then
writes the journal in `/var/lib/andaru-reset/` and starts
`andaru-factory-reset.service`, so the reset continues when the SSH session drops.

The unit stops Andaru, removes the live generation's network
files, services and filter (the filter becomes deny-all), and deletes the contents
of `/var/lib/andaru`, keeping the directory and its marker `.andaru-reset`. It
refuses to start when a mount sits inside the tree, and stops when the tree it
finds lacks the marker, as after a boot without the data partition. It writes a
fresh tree: the record that Andaru ran the host's network, empty configuration, the
`admin` account, and a `recovery.completed` audit event with reason
`factory_reset_backup_written` or `factory_reset_backup_skipped`. The engine then
applies and confirms, in order:

1. `/var/lib/andaru-kept/recovery/`, when stored;
2. `/var/lib/andaru-kept/last-lan/`, when the recovery copy is missing or does not
   come up. Its delegated IPv6 becomes none: the uplink it came from is not kept.

With neither, the box stays console-required behind deny-all: log in at the console
and use the root CLI. Old accounts never come back, `first/` is never read, and
`/var/lib/andaru-kept` and `/etc/andaru` are not touched.
A box whose network Andaru never ran keeps its own network; only the state is reset,
and no kept copy is applied. The engine always starts again; the web service and the
lease and prefix units only when they ran before.

A power loss resumes the reset at boot. From the first stop until the fresh tree
exists, boot loads only deny-all (nothing at all on a host whose network Andaru never
ran), and the engine and every realize job wait; a planned reset fences nothing yet.
A copy whose apply a crash cut, or whose apply stayed unconfirmed, counts as not up:
the reset rolls it back and moves on to the next one. A management copy left without
an address of its own is refused.

### Qualification evidence (2026-11-18)

Run on the Proxmox lab VMs `andaru-lab` (114, netplan cloud image, WAN `10.0.1.19/16`
static with a gateway) and `andaru-ifupdown-lab` (117, ifupdown with dhcpcd, WAN by
DHCP, taken over with `andaru ctl takeover --accept-detected`). Each VM had a second NIC
as the management LAN, `192.168.77.1/24` with no gateway, with `last-lan/` recorded by a
root save while that LAN was live. `recovery/` held the same network with the LAN at
`.2` and the WAN. The probe client was another host on the same bridge, given
`192.168.77.10/24`. The VMs were driven through the QEMU guest agent, so a network change
never cut the control channel. Every case started from the same stopped snapshot, ran
the real interactive `andaru ctl factory-reset run` on a pty, and created an extra
administrator `oldadmin` first. Both VMs were rolled back afterwards.

| Case | Result |
| --- | --- |
| `recovery/` stored (both VMs) | Up on `recovery/`: LAN `.2` SSH open, `.1` closed, WAN SSH closed behind the filter; only `admin` left, `oldadmin` gone; takeover kept |
| `recovery/` naming a missing card | The engine refused it (`HARDWARE_MISSING`, `MANAGEMENT_REQUIRED`); up on `last-lan/`, LAN `.1` open, WAN without an address |
| Neither copy | `CONSOLE_REQUIRED`; `andaru deny-all` loaded, every port closed, the engine running for the root CLI, `first/` not used; a reboot loaded deny-all again |
| Power cut at `stopped`, at `torn-down`, and during the wipe (only the marker left, `wiped` not yet journaled) | Resumed at boot and up on `recovery/` |
| Power cut at `applying-recovery` | Resumed at boot and up on `last-lan/`: an interrupted copy is not retried |
| Reboot answered yes | One reboot, then up on `recovery/` |
| ifupdown WAN by DHCP | Same lease `10.0.1.8` after the reset, the same `DUIDRawData` and `IAID` as dhcpcd's; `networking`, `ifup@` and `nftables` still masked |
| `last-lan/` only, after a DHCP WAN | The WAN lost its IPv4 address and dhcpcd is gone; LAN `.1` open |
| SSH session from the LAN the reset cuts | The session timed out; the unit finished the reset and came up on `recovery/` |

The lab found two bugs, fixed before these results: the engine's sandbox made
`/var/lib/andaru-kept` read-only, so `last-lan/` was never recorded and
`kept store-recovery` failed; and a stored `recovery/` carried the built-in roles of
the public view, which a restore refused. Not exercised: the teardown of a Kea or Unbound
service that Andaru runs (no lab configuration declared one; covered by unit tests), and
a recovery copy that passes the engine's gate but fails networkd's verification.

### Qualification across the Debian deletion (2026-11-20)

Slice 3 of the appliance image deleted the takeover, the netplan and ifupdown readers and the upgrade package, but kept reading the state they left. It was qualified on a throwaway Proxmox VM (SeaBIOS, WAN `ens18` by DHCP, LAN `ens19` at `192.168.77.1/24`) with two signed nightlies: `v2026-11-19.1.2ff0c06`, built before the slice, and `v2026-11-20.1.89c8002`, built after it. The box was driven over its serial console.

| Case | Result |
| --- | --- |
| Before the slice: an administrator, two saved versions applied and confirmed | Pointers `{"confirmed":2,"lastKnownGood":1,…,"takeover":false}`, with `last-lan/` recorded |
| `bootc switch --enforce-container-sigpolicy` to the newer nightly, then reboot | The old pointers and manifests load; boot restores generation 2 byte for byte (the networkd files match the manifest, and the filter is unchanged); the pointers are rewritten without `takeover` |
| A new apply and confirm | Generation 3 confirmed, and its manifest has no `takeover` key |
| Factory reset | Up on `last-lan/`: the baseline manifest is `{"reset":true}`, the WAN is down under the catch-all, and the LAN keeps `.1` with SSH |
| `bootc rollback` to the older image | It reads the reset tree the newer image wrote, restores its generation, and recognises a reset tree with no usable generation |
| Reset tree with `last-lan/` usable, on reboot | Restored |
| Reset tree whose only generation fails its manifest check, and a reset with no `last-lan/` to keep | `CONSOLE_REQUIRED`: `andaru deny-all` loaded, only the catch-all network file, both NICs down, `first/` never used |

Not exercised: a reset with `recovery/` stored, and SSH from the LAN side (the lab had no isolated bridge). On Fedora CoreOS 45 under BIOS boot, `efi.automount` fails on every boot; tracked in #83.

## Network preview

`andaru ctl preview network [--saved-version N]` shows exactly which files Andaru would give systemd-networkd and pppd for a saved version on this host, and what would stop them working. **It changes nothing**: no file is written under `/run`, nothing is reloaded or started, and no state is stored. Applying is a separate, explicit step: see [Applying](#applying).

```sh
sudo andaru ctl preview network | jq -r '.files[] | "### \(.path) \(.mode)\n\(.content)"'
sudo andaru ctl preview network | jq '.realizable, .findings'
```

The response has `state: "preview"`, the `savedVersion` rendered, `realizable` (true when no finding is an error), `resolved` (which kernel interface currently carries each declared permanent MAC), `files` and `findings`. Each file has its destination `path`, `mode`, `content` and the declaration objects it came from. A file marked `secret` shows `<redacted>` where the real file holds a secret; no command exports the real value.

What is rendered:

- **Ethernet, VLAN, bridge, bond:** `/run/systemd/network/50-andaru-<name>.netdev` and `.network`. Physical NICs are matched by `PermanentMACAddress=`, and a `.link` file keeps the name each has now across reboots: the boot service has udev apply Andaru's before networkd starts. Virtual links get a derived name: `vl`, `br`, `bo` or `pp` plus the first 12 hex digits of the link ID. Your own `name` and `description` never appear in a rendered file.
- **PPPoE:** pppd runs the session and owns IPv4; networkd keeps IPv6 (router discovery, DHCPv6, prefix delegation) on the PPP interface. Three files: `/run/andaru-ppp/<pp-name>.options` (mode `0600`, secret), `/run/systemd/system/andaru-pppoe-<pp-name>.service`, and a `.network` with `KeepConfiguration=yes` so networkd leaves pppd's address and default route alone. The split follows [systemd#481](https://github.com/systemd/systemd/issues/481).
- Provider-offered DNS, NTP, hostname and search domains are ignored on purpose; those are later areas.

| Finding | Severity | Meaning and remedy |
| --- | --- | --- |
| `HARDWARE_MISSING` | error | No physical interface has the declared permanent MAC. Fit the card or correct `permanentMac`. |
| `HARDWARE_AMBIGUOUS` | error | Several physical interfaces report that MAC (cloned VMs). Give them distinct MACs. |
| `NAME_COLLISION` | error | A derived interface name is already used by something else. Remove or rename the foreign interface. |
| `FOREIGN_CONFIGURATION` | error | A networkd file Andaru did not write exists in `/etc/systemd/network` or `/run/systemd/network`, or a `<file>.d/*.conf` drop-in exists there. It would override or conflict: remove it. |
| `TOOL_MISSING` | error | A PPPoE link needs `pppd` and its `pppoe.so` plugin: install `ppp`. |
| `KERNEL_SUPPORT_MISSING` | error | `/dev/ppp` is absent. Try `modprobe ppp_generic`. If the module does not exist, the kernel has no PPP: Debian's cloud kernel (`linux-image-cloud-*`) omits it, so install `linux-image-amd64` or `linux-image-arm64` and boot it. |
| `INTERFACE_NAME_UNSUPPORTED` | error | A PPPoE parent NIC has a kernel name outside `[A-Za-z0-9_.:-]`. |
| `CREDENTIAL_TOO_LONG` | error | pppd truncates user names and passwords over 255 bytes. |
| `UNSUPPORTED_COMBINATION` | error or warning | A PPPoE segment may use IPv4 `ppp`/`none` and IPv6 `none`/`slaac`/`dhcp6`, at least one of them, without RA. A PPPoE link with no segment is left out (warning). |
| `DEPENDENCY_UNAVAILABLE` | warning | Something was left out because a link it needs is missing, ambiguous or disabled. A bridge or bond keeps its remaining members. |
| `RA_PREFIX_UNSUPPORTED` | warning | Router advertisements were requested where nothing can be announced. |
| `PROXY_ARP_REQUIRED` | warning | A 1:1 external address is on an uplink's subnet but not assigned to the firewall, so nothing answers ARP for it. Add it to the segment's addresses; proxy ARP is tracked in #43. |
| `ONE_TO_ONE_OWN_ADDRESS` | error | A 1:1 external range holds an address the firewall learned by DHCP or PPPoE; 1:1 NAT would take over its traffic. Checked at preview and apply; boot loads the confirmed table as it was. |
| `RA_DNS_UNAVAILABLE` | warning | The DNS service serves a delegated segment that has no static IPv6 address, so its advertisements carry no DNS server. |
| `DNS_NOT_APPLIED` | warning | A segment declares DNS servers, but this host's `/etc/resolv.conf` is not managed by systemd-resolved, so they are saved and not used. |

### PPPoE sessions

An apply starts each PPPoE session's unit after networkd has reloaded, and boot starts them after it copies the confirmed generation into `/run`. A running session is restarted only when its unit or options changed since Andaru last started it, because a redial drops every connection through it and may change the public address. `/run/andaru-ppp` carries pppd's SELinux type (`pppd_etc_t`) so pppd may read the options there.

Verification treats a session like an interface with an address: a session that was up before the apply, holding an IPv4 address or the default route, must be up again before the verification time runs out, or the apply is rolled back. A new session is not waited for, so a line that is still being provisioned does not block an apply. Watch it with `journalctl -fu andaru-pppoe-<pp-name>.service` and `networkctl status <pp-name>`.

When the packet filter is on, TCP handshakes through a session have their MSS lowered to fit its MTU, both ways (MTU minus 40 for IPv4, minus 60 for IPv6).

`TestPPPoESessionInNamespaces` (in `internal/render`, run by CI's root suites) dials the rendered options against rp-pppoe's `pppoe-server` in a second network namespace: the credentials reach the server intact, the interface gets its name, MTU, address and default route, and a wrong password stays down. DHCPv6 prefix delegation over a session, reboot and reset are qualified on the lab image.

## Applying

An apply makes the saved head's network configuration live. Design and limits: [confirmed apply](../superpowers/specs/2026-11-11-network-takeover-apply-design.md).

```sh
sudo andaru ctl preview network                      # note the savedVersion and the findings
sudo andaru ctl apply network --saved-version 12
# from a NEW terminal or SSH session:
sudo andaru ctl applies confirm 1
```

What happens, in order:

1. **Checks.** `--saved-version` must be the saved head, so you apply what you previewed. The apply is refused (`APPLY_REFUSED`, with findings) if the preview has errors, if an interface that has an address now would be left unconfigured, or if it needs something not yet supported.
2. **A rollback timer is armed first**, owned by systemd, not by the engine. It is set for the timeout plus 60 seconds from the start of the apply, so it fires at most 60 seconds after the confirmation deadline, and it retries every 10 seconds if the rollback fails.
3. **The files are swapped and networkd reloaded.** Andaru then checks that every file is in use, static addresses are present, and the interfaces that had an IPv4 address and default route still have them. If not, it restores the previous files at once and the apply reads `failed`.
4. **You confirm from a new session** within the timeout (default 120 seconds, `--confirm-timeout 30`–`600`). A session that survived the change does not show that a new one can be opened; opening one is the test. Without confirmation the previous configuration returns, including after an engine crash or a reboot.

`andaru ctl applies list` shows every apply and four pointers: `confirmed` (what boot uses), `lastKnownGood` (the one before it), `pending`, and `live` (what runs now; `null` is no generation). `andaru ctl applies rollback ID` undoes a pending apply without waiting.

Boot needs `andaru-network-boot.service` ([unit](../../packaging/systemd/andaru-network-boot.service)) enabled, as the image leaves it: it copies the confirmed generation into `/run` before networkd starts. If that generation is damaged it uses the last-known-good one, and `live` then differs from `confirmed`.

| Finding | Meaning |
| --- | --- |
| `WOULD_UNCONFIGURE` | A physical interface with an address is not configured by the saved configuration. Declare it as an enabled Ethernet link with a segment. |
| `MANAGEMENT_REQUIRED` | Rules or outbound NAT are declared without `management.segment`, or the management segment's interface is unavailable. |
| `DHCP_WITHOUT_DNS` | (warning) A DHCP server hands out no DNS server: the DNS service does not serve its segment and `dnsServers` is empty. |
| `FOREIGN_FILTER` | `nftables.service`, `firewalld` or `ufw` is enabled and would replace or override Andaru's filter. |
| `APPLY_UNSUPPORTED` | Router advertisement modes `managed`, `assisted` and `stateless` need a `dhcp6` entry for the segment; advertisements and delegated prefixes need the packet filter, so a management segment, since IPv6 forwarding stays off without it. |
| `NETDEV_CHANGE_UNSUPPORTED` | An existing VLAN, bridge or bond would change. Remove it in one apply and add it again in the next. |
| `BOOT_UNIT_DISABLED` | `andaru-network-boot.service` is not enabled, so a confirmed configuration would not return after a reboot. |

### Packet filter

Andaru filters with its own nftables table, `inet andaru`, applied, confirmed and rolled back together with the network files of the same generation. Design and limits: [packet filter and outbound NAT](../superpowers/specs/2026-11-12-packet-filter-design.md), and for the rules and aliases [firewall rules as one list between zones](../superpowers/specs/2026-11-22-firewall-rule-model-design.md).

```sh
sudo andaru ctl config list rules                        # rules in execution order, and the list's version
printf '%s\n' '{"name":"web","protocol":"tcp","ports":["80","443"]}' | sudo andaru ctl config create aliases/ports
sudo andaru ctl config order rules --version 12 "$RULE_B" "$RULE_A"
sudo andaru ctl preview network                          # shows "nft:inet/andaru" beside the networkd files
```

- **The filter switches on with `management.segment`.** While it is unset there is no table and nothing forwards, as before; rules or outbound NAT without it are refused with `MANAGEMENT_REQUIRED`. SSH (22/tcp) is always accepted on the management segment, from `allowedSources` when set; no rule can remove that.
- **Aliases:** address aliases are hosts, networks, ranges or one-level groups of those; port aliases are tcp/udp ports and ranges, icmp/icmpv6 types, or groups.
- **Rules:** one list, `rules`, tried in order: `accept`, `drop` or `reject`, `from`, `to`, `ports` (port aliases; empty means any protocol and port), `log` (to the kernel log). A rule matches IPv4 and IPv6 alike unless its addresses are of one family. Reordering is its own version-checked operation, so two sessions cannot reorder past each other.
- **A side of a rule** (`from`, `to`) is `{"zones": […], "addresses": […], "not": true}`, and both parts must match: the zone traffic arrives in or leaves by (any of them; empty means any network), and the address aliases within it (any of them; empty means every address). `not` turns the addresses into "none of these", so "from lan, except the printers" is one rule; it takes at most 40 aliases, and so do both sides of a rule together; a group counts as one, whatever it holds.
- **The firewall is a zone,** `zon_firewall`: a rule reaches the box only by naming it in `to.zones`, alone. "Any" never means the firewall, so traffic to everywhere and to the box is two rules. It is a destination only: the firewall's own outgoing traffic is not filtered.
- **Limits:** a rule leaves out 40 aliases; the rule list holds up to 80,000 rules, and one order request about 24,000 IDs within the 1 MiB request limit.
- **Configurations saved before schema 4** (two rule lists, `objects`) load as the configuration they are now, and the first save writes the current form. An image rolled back after that save does not open the store.
- **Fixed rules, before every rule:** established and related accepted, invalid dropped, loopback and ICMPv6 neighbour discovery accepted, DHCPv6 replies accepted, and on each segment with a `dhcp6` entry DHCPv6 requests from link-local clients (UDP 546 to 547); everything unmatched is dropped. The firewall's own outgoing traffic is not filtered.
- **Outbound NAT** `automatic` masquerades IPv4 forwarded out of every segment with a default route (a static gateway, DHCP or PPPoE). IPv6 is routed, not translated.
- **Applying:** the filter is loaded before the networkd files, and when going back to a generation without one the table is deleted only after the files, with IPv4 and IPv6 forwarding switched off just before it; going back to no generation loads the bare filter first. Boot loads the confirmed filter before networkd starts, with each adapter's name looked up again by its MAC, so a card the kernel names differently after a reboot keeps its rules; a filter that does not load makes boot fall back to the last-known-good generation, files and filter together.
- **Refused:** `nft` missing (`TOOL_MISSING`), or `nftables.service`, `firewalld` or `ufw` enabled (`FOREIGN_FILTER`), which would replace or flush Andaru's table. A masked unit never counts. Andaru itself never touches other tables, such as Podman's or Docker's.

### DHCP and DNS

Andaru runs Kea as the LAN's DHCPv4 server and Unbound as its DNS service; the firewall itself resolves through the same service. Both are configured only from the confirmed generation, applied, confirmed, rolled back and booted with the network and the filter. Design: [DHCP and DNS for the LAN](../superpowers/specs/2026-11-12-dhcp-dns-design.md).

The image ships Kea, Unbound, their settings and the lease hook.

```sh
printf '%s\n' '{"name":"lan","segment":"'"$LAN"'","ranges":[{"first":"192.168.40.128","last":"192.168.40.254"}]}' | sudo andaru ctl config create dhcp
printf '%s\n' '{"mode":"recursive","segments":["'"$LAN"'"]}' | sudo andaru ctl config replace --version 0 dns
```

- **`dhcp`**, one entry per served segment, which needs a static IPv4 address: `ranges`, `leaseTime` (seconds, default 7200), `staticLeases` (`mac`, `address`, optional `hostname`), optional `gateway` (default: the segment's address), `dnsServers`, `domainName` (default: `dns.localDomain`), `domainSearch`, `ntpServers`, `nextServer`, `bootFileName`, and custom `options` (`code`, `type`, `data`, optional `alwaysSend`). Clients get the segment's address as router unless `gateway` overrides it and, when the DNS service serves the segment, as DNS server; otherwise `DHCP_WITHOUT_DNS` warns.
- **`dns`**: `mode` `recursive` or `forward` with `upstreams` (`do53`, or `dot` with the certificate's `tlsName`; one protocol for all), `segments` it serves (may be empty: the firewall only), `localDomain` (default `home.arpa`), `searchDomains`, `registerLeases` (default true). While it is declared no segment may carry `addressing.dns`: the host's servers are the upstreams, and links stop taking DNS servers from DHCP.
- **Upstream loop refusal:** while `dns` is declared, a port 53 upstream at an address the service itself listens on is refused, since it would ask itself: `127.0.0.1`, `::1`, the unspecified address (also written as IPv4-mapped IPv6), a served segment's address (IPv4 or IPv6) and a virtual address named in `dns.virtualAddresses`. `127.0.0.53` and `127.0.0.54`, systemd-resolved's stub and proxy listeners, are refused too: Andaru points resolved at Unbound, so an upstream there loops back. Other loopback addresses such as `127.0.2.1` are accepted, because nothing listens on them. A `dot` upstream defaults to port 853, which Unbound does not serve, so it is never refused for this reason; an explicit port 53 is checked like a `do53` one.
- **Lease names:** a client's hostname is published as `<name>.<localDomain>`, A and PTR, for a third of its lease time. Kea's `run_script` hook only touches `/run/kea/andaru-leases-changed`; `andaru-leases.path` then runs `andaru realize leases`, which reads every lease over Kea's socket and rewrites `/run/andaru-leases/leases.conf`. Only a valid name label is published; static lease names win over lease names, and the most recently renewed lease wins a name.
- **Applying:** after networkd reloads, Kea's and Unbound's own checkers judge the new files; a refusal, or a daemon that does not come up, restores the previous generation. A generation without `dhcp` stops Kea; without `dns`, Unbound falls back to its packaged default and the host's resolver is what it was. Leases stay in Kea's `/var/lib/kea`.
- **Filter:** with the filter on, DHCP requests are accepted on served segments by a fixed rule; DNS is governed by rules.
- **Leases view:** `andaru realize leases` writes `/run/andaru-leases/leases.json` alongside `leases.conf`; `andaru ctl leases` (`GET /api/v1/status/leases`) serves the DHCPv4 and DHCPv6 lease snapshot and marks past-expiry active leases as expired.

### DHCPv6 and prefix delegation

Kea's DHCPv6 server on LAN segments with a static IPv6 `/64`, including prefixes delegated to routers behind them. Design: [DHCPv6 for the LAN](../superpowers/specs/2026-11-13-dhcpv6-design.md).

```sh
printf '%s\n' '{"name":"lan6","segment":"'"$LAN"'","ranges":[{"first":"fd00:beef:1::1000","last":"fd00:beef:1::1fff"}],"prefixDelegation":{"prefix":"fd00:beef:100::/48","delegatedLength":56}}' \
  | sudo andaru ctl config create dhcp6
```

- **`dhcp6`**, one entry per segment with a static IPv6 `/64`: `ranges`, `leaseTime` (default 7200; the preferred lifetime is five-eighths of it), `dnsServers` (default: the segment's stable address when the DNS service serves it), optional `domainSearch` (default: the local domain and the DNS service's search domains), `ntpServers` (SNTP servers), custom `options` (`code`, `type`, `data`, optional `alwaysSend`), `staticLeases` by `duid` with an `address`, a `prefix` and a `hostname`, and `prefixDelegation` (`prefix`, `delegatedLength` 48–64, optional `excludedPrefix` inside the first delegated prefix). On a `delegated` segment the same entry takes suffixes, a static lease's `prefixId` instead of `prefix`, and a pool as `subnetId` and `length` instead of `prefix` (see [DHCPv6 on delegated prefixes](#dhcpv6-on-delegated-prefixes)).
- **Router advertisements:** `managed`, `assisted` and `stateless` apply on a segment with an entry. With other modes `DHCP6_UNANNOUNCED` warns that clients following the flags will not ask.
- **Delegated prefixes are routed via the router that asked for them.** Kea's hook appends the router's link-local address and interface to `/run/kea/andaru-pd-events`; `andaru realize leases` moves that file out of Kea's directory, believes a line only for a served interface, a link-local router and a prefix of that segment's pool, and installs `ip -6 route … proto 77` for each active lease. It removes every protocol 77 route nobody holds. `andaru realize prefixes` does the same once a minute, so a route whose install failed returns without waiting for the router. The next hops are kept in `/var/lib/andaru/pd-next-hops.json`, so the routes return at boot. `ManageForeignRoutes=no` stops networkd deleting them on a restart or an apply.
- **The pool is unreachable** where nobody leases it (`[Route] Type=unreachable` on the segment), so it never leaks out of the WAN. That route covers the whole pool, so the pool must be a `/16` or longer inside `fc00::/7` (unique local) or a `/32` or longer inside `2000::/3` (global unicast); a wider one would blackhole real destinations. A delegation pool needs the packet filter, so a management segment.
- **Names:** DHCPv6 address leases publish AAAA and PTR records. A name static in either family is taken by no dynamic lease of either family, so no client can claim another device's static name, for A or for AAAA; a device with the same static name in both families gets both records.
- **Not offered:** DHCPv6 relays.

### DHCPv6 on delegated prefixes

Kea's DHCPv6 server, and prefix delegation onwards, on segments numbered from the prefix the WAN's DHCPv6 client receives (`delegated` segments). Andaru follows the prefix when the provider changes it, and tells clients to stop using the old one. Design: [DHCPv6 on delegated prefixes](../superpowers/specs/2026-11-13-dhcpv6-delegated-design.md).

```sh
# A /48 requested on the WAN; the LAN is subnet 1 of it, and routers behind it get /64s from the 16 at subnet 0x80.
printf '%s\n' '{"name":"lan6","segment":"'"$LAN"'","ranges":[{"first":"::1000","last":"::1fff"}],"staticLeases":[{"duid":"00:01:00:01:aa:bb:cc:dd:ee:ff","prefixId":"1","hostname":"router"}],"prefixDelegation":{"subnetId":"8","length":60,"delegatedLength":64}}' \
  | sudo andaru ctl config create dhcp6
sudo andaru ctl prefixes
```

- **Suffixes and positions:** on a delegated segment, `ranges` and static `address`es are suffixes (the first 64 bits zero), put behind the segment's current `/64`. A `prefixDelegation` is a position in the WAN's prefix: `subnetId` at `length`, handing out prefixes of `delegatedLength`. A static lease's `prefixId` is a position in that pool, so a router keeps its place whatever prefix the provider delegates. Literal prefixes and an excluded prefix are refused there.
- **What fits:** subnet IDs and pools are checked against the WAN's `requestPrefixLength` when saved (`0` to `ff` for a `/56` request), and against the prefix actually received at apply. A delegated segment saved earlier with a subnet ID wider than its WAN's request no longer validates. At apply, `kea-dhcp6 -t` judges the delegated subnets filled with `2001:db8::` at the requested length. When the provider delegates less than requested, what does not fit is reported (`DELEGATED_PREFIX_TOO_SHORT`) and not served; the rest is. To fit whichever answer the provider gives, keep subnet IDs and pools inside the first `/56`, for example a `/60` pool of `/64`s at `subnetId` `8`.
- **Following the prefix:** `andaru realize prefixes` runs when networkd's link state changes (`andaru-prefixes.path`), every minute (`andaru-prefixes.timer`), at apply and at boot; on a host without a delegated segment it returns at once. It reads the prefix from networkd's `unreachable … proto dhcp` route and the LAN's address, fills the delegated subnets into Kea's file, and hands it to Kea with `config-set` without restarting it. Clients move to the new prefix when they renew; Kea returns their old lease with zero lifetimes. Leases carry `leaseTime` whatever the delegation has left, so keep `leaseTime` at or below the provider's valid lifetime: a client leased just before the provider withdraws the prefix keeps its address until it renews. When no prefix is seen (at boot, during an outage), the last one is served until its valid lifetime ends, and the routers' next hops are kept.
- **Deprecating the old prefix:** networkd stops listing a replaced prefix but never advertises it at lifetime zero, so clients keep it preferred. For ten minutes after a change, Andaru advertises each segment's old `/64` with preferred lifetime 0 (and the segment's own router lifetime and flags), so clients stop choosing addresses from it; the first goes 5 seconds after the change is seen, after networkd's own advertisement. Kea is handed the new prefix first, without waiting, and the deprecation goes out whether or not Kea takes it. RFC 4862 keeps it valid on clients for up to two hours.
- **The prefix view:** `andaru ctl prefixes` (`GET /api/v1/status/prefixes`) shows each WAN's requested length, its current prefix and lifetimes, the subnet IDs it leaves room for, each delegated segment's `/64`, each pool's range, and what does not fit.
- **Routes and names:** a delegated pool is unreachable where nobody leases it (`proto 77`, kept by the lease sync), leased prefixes are routed via the router that asked, and static names publish AAAA and PTR at the current address with a lease's TTL (a third of `leaseTime`, at most an hour). After a change, the old pool's routes stay while Kea still holds their leases and the old prefix is valid, until each router renews.

### Virtual addresses

Addresses the firewall holds on no LAN, for its own services: pfSense's IP aliases on `lo0`, such as a DNS listener every LAN is given. Design: [Virtual addresses](../superpowers/specs/2026-11-14-virtual-addresses-design.md). Proxy ARP, Other and CARP come after the MVP ([#43](https://github.com/andaru-project/andaru/issues/43)).

```sh
printf '%s\n' '{"name":"dns4","address":"172.16.53.53"}' | sudo andaru ctl config create virtualAddresses
printf '%s\n' '{"name":"dns6","address":"fd00:53:53:53:53:53:53:53"}' | sudo andaru ctl config create virtualAddresses
# The DNS service listens on them; DHCP, DHCPv6 and router advertisements hand them out through dnsServers.
printf '%s\n' '{"mode":"recursive","segments":["'"$LAN"'"],"virtualAddresses":["'"$VIP4"'","'"$VIP6"'"]}' | sudo andaru ctl config replace --version "$V" dns
# DNS sent to the firewall itself on the WAN goes to the IPv6 one.
printf '%s\n' '{"name":"dns6","family":"ipv6","enabled":true,"ingressZones":["'"$WAN"'"],"protocol":"tcp-udp","from":[],"to":{"self":true},"filterRule":"auto","ports":"53","target":{"address":"fd00:53:53:53:53:53:53:53"}}' \
  | sudo andaru ctl config create nat/port-forwards
```

- **`virtualAddresses`:** single IPv4 or IPv6 unicast addresses, not loopback, link-local or on any segment's subnet (an address on a LAN's subnet belongs on that segment). They live on the dummy device `andaru-vip`, which the generation creates and removes with them; no link may take that name.
- **DNS:** `dns.virtualAddresses` names those the DNS service listens on, answering every source that reaches them. Who reaches them is the packet filter's decision, so this needs a management segment. To give clients the address, name it in `dnsServers` of `dhcp`, `dhcp6` or the router advertisements.
- **Port forwards to them:** an IPv4 forward whose target is a virtual address is a forward to the firewall itself, with its rule in the input chain. Reflection never takes a virtual address, so a LAN client reaches the service on it. `self` keeps meaning the arriving interface's addresses, never a virtual address.
- **IPv6 port forwards** (`"family":"ipv6"`) reach only the firewall itself: `self`, a segment's static IPv6 address, or an IPv6 virtual address. Forwards to inside hosts (NAT66) and reflection are refused.
- **In use:** a virtual address a port forward targets cannot be deleted or re-addressed until the forward is changed or deleted.

### Inbound NAT

Port forwards and 1:1 NAT, with pfSense's semantics, IPv4 only; IPv6 port forwards to the firewall itself are in "Virtual addresses" above. Both need the packet filter, so a management segment. Design: [inbound NAT](../superpowers/specs/2026-11-12-inbound-nat-design.md).

```sh
# WAN port 8080 to the web server's port 80, from anywhere; its filter rule comes with it.
printf '%s\n' '{"name":"web","enabled":true,"ingressZones":["'"$WAN_ZONE"'"],"protocol":"tcp","from":[],"to":{"self":true},"ports":"8080","target":{"address":"192.168.40.10"},"targetPort":80}' \
  | sudo andaru ctl config create nat/port-forwards
# A public address on the WAN segment mapped both ways to an inside host.
printf '%s\n' '{"name":"server","enabled":true,"zone":"'"$WAN_ZONE"'","external":"203.0.113.20","internal":"192.168.40.30","destination":[]}' \
  | sudo andaru ctl config create nat/one-to-one
sudo andaru ctl config order nat/port-forwards --version "$V" "$ID2" "$ID1"
```

- **`self`** names the firewall on the interface the connection arrives on: as a forward's `to` it follows a DHCP or PPPoE address without a re-render; as its `target` it redirects to the firewall itself.
- **Port forwards** are evaluated in list order, first match. `ports` is one port or a `first-last` range; `targetPort` shifts a range by the same offset. `noRedirect` exempts matching connections from the forwards after it. `from` takes address objects, with `fromNegated` (at most 40; none left out means any source). `toNegated` turns `to` into its complement among unicast destinations: not the firewall's address on the arriving interface (in the LAN zone, the firewall's WAN address is not "this firewall"), or not in the address object. Broadcasts and multicasts never match. A forward with a negated destination has no reflection; when its target is an inside host, internal clients reach it through the hairpin masquerade. A negated address object also matches the firewall's own addresses. Its target's own traffic matches too, unless `from` leaves the target out. A `target` may be an inside host or one of the firewall's own addresses; `127.0.0.0/8` is refused (Linux drops such translations from other interfaces) — use a [virtual address](#virtual-addresses) instead ([#43](https://github.com/andaru-project/andaru/issues/43) tracks proxy ARP, Other and CARP).
- **Associated rule:** with `filterRule: auto` (the default) each forward gets a rule placed before the policy, in `forward`, or in `input` for the firewall itself. It admits only connections the forward translated, so the inside host is not reachable directly: the forward sets the low 16 bits of the connection mark to its position, and its rule matches that mark. The high 16 bits are left to other software. `none` leaves it to the policy.
- **1:1** maps an address or a prefix in both directions within one zone, optionally only for `destination` peers, or with `destinationNegated` for every peer but those (at most 40 aliases; a group counts as one). `noBinat` exempts hosts from the entries after it; they leave through the outbound masquerade. Write the policy rule that admits the traffic yourself, as in pfSense.
- **1:1 addresses** must already reach the firewall: an extra static address on the WAN segment (never its first, which the firewall's own traffic uses) or a block the provider routes to it. `PROXY_ARP_REQUIRED` warns about an address on the WAN subnet the firewall does not hold; proxy ARP is tracked in [#43](https://github.com/andaru-project/andaru/issues/43).
- **1:1 and virtual addresses:** a range holding a virtual address is refused, as `external` or as `internal`: the address belongs to a service on the firewall, and 1:1 NAT would take its traffic over.
- **Reflection** (`reflection: true`, Pure NAT) lets internal clients use the outside address; the server then sees the firewall as the client. Split DNS is the alternative that keeps the client's address.

### IPv6 on the LAN

A LAN segment numbers itself from a prefix the WAN's DHCPv6 client receives, or from static addresses, and advertises itself as a router. Design: [IPv6 for the LAN](../superpowers/specs/2026-11-12-ipv6-lan-design.md).

```sh
# WAN: ask for a /56 and no address of its own.
... "ipv6":{"mode":"dhcp6","requestPrefixLength":56,"prefixOnly":true}
# LAN: subnet 0 of the delegated prefix, a stable unique local /64 for DNS, SLAAC for clients.
... "ipv6":{"mode":"delegated","from":"<wan segment>","subnetId":"0","addresses":["fd12:3456:789a:1::1/64"],"ra":{"mode":"unmanaged"}}
```

- **`ra`**: `mode` `disabled`, `router` (no prefix announced), `unmanaged` (SLAAC); `managed`, `assisted`, `stateless` send clients to the DHCPv6 server, so they are refused at apply without a `dhcp6` entry for the segment. Options: `priority` (`low`, `medium`, `high`), `routerLifetime` (0 or 4–9000 s, default 1800), `validLifetime`/`preferredLifetime` (static prefixes only, default 86400/14400; a delegated prefix keeps the lease's), `dns` (`servers`, `searchDomains`), `pref64`. Versions saved with the older `{enabled, managed, otherInfo}` still load; writing the segment stores the mode.
- **Advertised DNS:** without `ra.dns`, a segment the DNS service serves advertises its first static IPv6 address and the local and search domains. Give a delegated segment a static unique local address (RFC 4193): the delegated prefix can change, the unique local address does not. Without one, `RA_DNS_UNAVAILABLE` warns.
- **WAN DHCPv6 client:** `requestPrefixLength`, `prefixOnly` (`UseAddress=no`), `withoutRA` (solicit without waiting for an advertisement), `sendRelease` (default false: a release makes many providers hand out a new prefix), `duid` (hex, types 1 to 4 with the length each needs; replaces the captured DHCPv6 identity). A `dhcp6` segment always solicits (`DHCPv6Client=always`), whatever flags the upstream advertisement carries.
- **DNS service:** listens on each served segment's stable IPv6 address. On a static segment it answers that subnet, as for IPv4; on a delegated one, whose prefix is unknown in advance, it answers whatever source reaches it there, and the filter (required for delegated segments) decides who may. A segment may be served by IPv6 alone.
- **Filter:** link-local multicast listener discovery is accepted on every interface. Neighbour discovery was already.
- **Prefix changes:** networkd advertises the new delegated prefix at once but does not deprecate the old one; Andaru does, for ten minutes, on each delegated segment that announces prefixes (see [DHCPv6 on delegated prefixes](#dhcpv6-on-delegated-prefixes)). The unique local address keeps DNS working throughout.
- **Not offered:** advertisement intervals (networkd has none), extra advertised subnets.

### Host DNS servers

One segment may say which DNS servers the firewall host itself asks. Design and limits: [static DNS servers for the host](../superpowers/specs/2026-11-11-static-host-dns-design.md).

```json
"addressing": {
  "ipv4": {"mode": "static", "addresses": ["192.0.2.10/24"], "gateway": "192.0.2.1"},
  "ipv6": {"mode": "none"},
  "dns": {"servers": ["192.0.2.53", "2001:db8::53"], "searchDomains": ["example.org"]}
}
```

- `servers` are tried in the order given; `searchDomains` is optional. **Only one segment may declare `dns`.**
- On that segment the declared servers replace any offered by DHCP or router advertisements. Without `dns`, the host uses the offered ones.
- An apply checks that systemd-resolved reports exactly these servers, in order, for the interface; otherwise it restores the previous files. It does not check that the servers answer, and it does not look at servers set globally in `resolved.conf` or on other interfaces, which resolved also asks.
- This is the host's own resolver only. It gives LAN clients nothing.
- It works where systemd-resolved manages `/etc/resolv.conf`. Elsewhere the preview warns with `DNS_NOT_APPLIED` and the host keeps using its `resolv.conf`.

### How it is built

The engine keeps its sandbox and never writes outside its state directory and its runtime directories (`/run/andaru`, `/run/andaru-web`). It stores each generation under `/var/lib/andaru/generations/<n>/` and asks systemd to run `andaru realize activate|rollback|confirm N` in a transient unit; the timer runs `andaru realize rollback N deadline` the same way. `pointers.json` in that directory is the single commit point, and `realize` re-reads it under a lock before acting, so a timer that fires after a confirmation does nothing. Because the engine can ask systemd to run commands, it is root-equivalent; the sandbox is hardening, not a boundary.

## Supervision

The source unit is [`packaging/systemd/andaru-engine.service`](../../packaging/systemd/andaru-engine.service), shipped in the image with the binary at `/usr/bin/andaru` ([Installing](#installing)).

```sh
sudo systemctl start andaru-engine.service
sudo andaru ctl links
sudo systemctl restart andaru-engine.service
sudo journalctl -u andaru-engine.service
```

`Type=exec` is not API readiness: an automated caller should wait under one bounded overall deadline for a successful CLI read. The shipped unit runs as root with no effective/bounding/ambient capabilities, in the `andaru-web` supplementary group so it can hand its web socket to that group without `CAP_CHOWN` (with `/run/andaru-web` writable), `NoNewPrivileges`, strict filesystem/home protection, private temporary storage and kernel/control-group protections. It allows `AF_UNIX AF_NETLINK` so Debian's read-only `ip` operation can inspect the host namespace. It intentionally does not use `PrivateNetwork` or depend on `network-online.target`. It opens no TCP/UDP listener, and its stop action never flushes firewall rules.

SIGINT/SIGTERM cancels request contexts, gives HTTP shutdown up to 3 seconds, then closes held connections if necessary. systemd supplies a 5-second stop budget. This unit's current lack of network-administration capabilities is deliberate: a later realizer must request only the privileges its actual mutation path requires.

## Verification

GitHub Actions runs the same native test job concurrently on `ubuntu-24.04`
(amd64) and `ubuntu-24.04-arm` (arm64) for pull requests targeting `main` and
pushes to `main`.
The workflow verifies the native architecture, runs `go vet`, ordinary and race
suites without elevated privileges, then builds the CGO-disabled program and
executes all three integration binaries as root. The engine fault test receives
that built program through `ANDARU_TEST_BINARY`; no integration cases are skipped.
Race tests retain native CGO support. Privileged tests run only on disposable
GitHub-hosted VMs. ENOSPC phases use private mount namespaces; other cases use
disposable temporary directories.

The runner labels and passwordless `sudo` are documented in
[GitHub's hosted-runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).

The ordinary suite runs without elevated privileges. Explicit kernel-credential/lifecycle tests use an integration build tag and require root; they fail clearly rather than silently skipping the proof. Build the test executable on the developer host and transfer it to the lab box, without installing a Go compiler there:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/config.test" ./internal/config
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/engine.test" ./internal/engine
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/ctl.test" ./cmd/andaru
```

Run that test executable as root in the lab. The UID/GID 65534 helper uses an owned traversable sandbox and deliberately relaxed **test-only** socket permissions after startup, proving `SO_PEERCRED` independently of filesystem access. Other cases cover existing files/symlinks/live/stale sockets, the web socket's lock and its replacement of a dead leftover, incorrect owners/ancestors, umask-independent modes, replacement preservation, cancellation, forced connection closure and clean restart. Production gets no permission-bypass or test-helper flag.
The store's non-root helper copies its executable into a root-owned traversable
sandbox and explicitly sets mode `0755`, so direct `go test` runs also work when
Go's build directory is private or the caller uses a restrictive umask.

The store and engine fault suites require root plus `unshare`/`mount` in the lab.
Mounts and ENOSPC are isolated in private test mount namespaces, never granted
to the production service. Set `ANDARU_TEST_BINARY` to the absolute tested
`andaru` executable when running `engine.test`: its fencing proof invokes the
actual program, reads `503`, requires exit `1`, then verifies complete reopen.
No product test-helper flag or fault-injection callback is installed.

The Debian lab qualifications below are records of what was proven on Debian. Andaru no longer runs on Debian, and their takeover steps no longer exist.

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

### Network-preview Debian lab qualification

On 2026-11-10 the CGO-disabled amd64 build of code commit `98d163088e90ad1d7f2bb35334f0d46bf781c0bd` (`vcs.modified=false`, SHA-256 `100144bd45741b98ffd03d673ee2e408f5624267f2860d4b4edfff5ff067f7bc`) replaced the previous lab binary (`e7d3a7d2…`, kept beside the staged artifacts) on VM 114. Snapshot `pre-ppp-20261110-185944` was taken first. Because Debian's cloud kernel has no PPP support, the VM now boots `linux-image-amd64` (6.12.116); `ppp` 2.5.2 is installed.

Executed as root on that VM:

- All four tagged suites passed: store, engine (against the installed-style binary), CLI and render.
- **pppd, no link:** in a private network namespace with a dummy parent interface, `pppd file <rendered> logfd 1 dryrun` accepted the rendered options for PPPoE over a VLAN and directly on a NIC named `enx-1.2`, and reported back the interface name, MTU/MRU and a user name containing quotes, a backslash, `#`, spaces and a newline. The password printed as `??????`. An appended unknown option made it exit 2.
- **systemd:** `systemd-analyze verify` accepted each rendered unit with no output, the `BindsTo=` device name equalled `systemd-escape --path`, and an unknown key produced a warning.
- **networkd:** a private `systemd-networkd` in its own network and mount namespaces logged `Successfully loaded.` for every rendered `.netdev` and `.network` of all seven fixtures with no parse complaint, and reported an injected unknown key. Host links were identical before and after.
- **Installed path:** a synthetic topology was saved through the CLI: the VM's real `eth0` MAC as a 1508-byte WAN NIC, VLAN 6, PPPoE at 1500 with `kpn@internet`, and a LAN NIC with an absent MAC. `andaru ctl preview network` resolved the WAN NIC to `eth0`; rendered its `.network`, the VLAN `.netdev`/`.network`, the pppd options (password shown as `<redacted>`), the service unit and the PPP `.network`; reported `FOREIGN_CONFIGURATION` for both netplan files, `HARDWARE_MISSING` for the LAN NIC and `DEPENDENCY_UNAVAILABLE` for what depends on it; and raised neither `TOOL_MISSING` nor `KERNEL_SUPPORT_MISSING`. A UID 65534 caller got exit 1 and no output.
- The stateless nft ruleset, both forwarding sysctls, interface addresses, `/run/systemd/network` and the absence of `/run/andaru-ppp` and of any Andaru unit were identical before and after. The synthetic objects were deleted, leaving saved version 9 with an empty topology; two unused test secrets remain under the retention policy.

After the adversarial review, code commit `1ec0a3aa91a29fe34cba5ee6569049544abb7acf` (`vcs.modified=false`, SHA-256 `909f9822d6e1b809f14d4f775c02ec517ca5211853647608d9df50312566daa2`) replaced that binary. The tagged engine and render suites passed again on the VM, including the private networkd load of the changed SLAAC output, and the installed CLI previewed the empty saved topology with only the two netplan findings. After the bot review, code commit `5ebcebbe9f828b23f384a357db9d8755f7e4a900` (SHA-256 `6791d4927bec7fcfe984edc0413b2cc522c9b702915cdef57deea033bfda1b24`) replaced it in turn; the tagged CLI and engine suites passed on the VM and `--saved-version ""` exited 2.

The three tool proofs run on this lab, **not in CI**: the hosted runners are Ubuntu 24.04, whose pppd and systemd are not the Debian 13 versions these files target. CI runs the golden-file, findings, redaction, API and CLI tests on amd64 and arm64. Linux arm64 was cross-built, not run on Debian. No PPPoE discovery packet was sent and no session was established; a real line remains the owner's test. The engine is active and still not boot-enabled.

### Static host DNS Debian lab qualification

On 2026-11-11 the CGO-disabled amd64 build of code commit `40affb6` (SHA-256 `aaff74d3adcff79f81759501fa40ec72a3ae2686eb2f3decb9a94d4d918cb042`) was installed on VM 114. "Servers" below is the output of `resolvectl dns` for the interface; a 5-per-second ping ran during each takeover.

| Step | Result |
| --- | --- |
| DHCP uplink (snapshot `base-netplan-20261111`), `dns` declared as the two lab servers in the opposite order to the DHCP-offered one, takeover with a 30-second timeout | 0 of 100 pings lost. Servers were the declared ones in declared order; the search domain was set; a name resolved. After the timer rollback the servers were the DHCP-offered ones and the search domain was gone. |
| VM switched to a Proxmox cloud-init static address, gateway, two name servers and a search domain; snapshotted while stopped as `base-netplan-static-20261111` | netplan's generated file carries `Address=`, `DNS=`, `Domains=` and a default route. |
| Static address and gateway declared, no `dns`, `--takeover` | Refused: `TAKEOVER_UNSUPPORTED_BASELINE` on netplan's file, telling the operator to declare `dns` on the segment of `eth0`. |
| `dns` declared with the servers in the opposite order to netplan's, takeover with a 30-second timeout | 0 of 100 pings lost. Address and static default route kept; servers were the declared ones in declared order; a name resolved. After the timer rollback netplan's files and netplan's server order were back and a name resolved. |
| Same takeover confirmed from a new session, then rebooted | Same address on `ens18`, servers as declared, a name resolved, `/etc/netplan` empty, boot in 6.0 s. |

Not run on the lab: a host where systemd-resolved does not manage `/etc/resolv.conf` (unit-tested: the check is skipped and the preview warns). The VM was reset to `base-netplan-static-20261111` afterwards. On this ZFS-backed VM only the most recent snapshot can be rolled back to, so the older DHCP base `base-netplan-20261111` is now reachable only by cloning from it.

### Packet filter Debian lab qualification

On 2026-11-12, VM 114 (Debian 13, netplan static, `eth0` WAN at `10.0.1.19/16`) gained a LAN adapter `eth1` at `192.168.40.1/24` on an isolated VLAN, with a Debian client at `192.168.40.2` behind it, and ran this branch together with [#33](https://github.com/andaru-project/andaru/pull/33) (adapter names kept after a netplan takeover):

- **Takeover with starter rules:** `sudo andaru ctl takeover --accept-detected` over SSH (plain `sudo`, which drops `SSH_CONNECTION`) put management on `eth0`, loaded `inet andaru` with SSH, ping and DNS from the LAN, LAN to any and IPv4 masquerade, and set IPv6 forwarding; the WAN's SLAAC and DHCPv6 addresses stayed. The client reached `1.1.1.1` (ping and HTTP) through NAT, a WAN-side probe of port 8080 timed out, SSH stayed up and the confirm went through.
- **Rules from the CLI:** an address object, a `drop` rule for ping to it, `config order forward` to put it first, apply and confirm: the client's ping to `1.1.1.1` was dropped while HTTP still worked. Disabling the rule (replace with `enabled: false`), apply and confirm restored the ping.
- **Unconfirmed apply:** deleting the LAN rule and applying with a 30-second timer cut the client off; nobody confirmed, the timer rolled back, the previous table (same comment hash) was loaded again and the client was back.
- **Reboot:** the boot service ran once, after the root was writable; udev renamed `ens18`/`ens19` to `eth0`/`eth1` from Andaru's `.link` files before the filter loaded (`iifname "eth0"`), and networkd started after it. The client had internet, the WAN probe was dropped and SSH worked.
- **Foreign filter:** with `nftables.service` enabled, the preview reported `FOREIGN_FILTER nftables.service` and the apply was refused with `APPLY_REFUSED`.

The lab also found that the saved declaration did not carry the filter (fixed with a test), and that before #33 a confirmed takeover lost the adapter names at the next reboot.

### DHCP and DNS Debian lab qualification

On 2026-11-12, VM 114 (Debian 13, netplan static, `eth0` WAN at `10.0.1.19/16`, `eth1` LAN at `192.168.40.1/24` on an isolated VLAN, `kea-dhcp4-server` 2.6.3, `unbound` 1.26.2, the packaged files above) with a Debian client on the LAN:

- **Takeover:** the proposal carried the DNS service forwarding to the host's two declared servers and a DHCP offer of `192.168.40.128–192.168.40.254`; `--accept-detected` took over without DHCP, and the firewall then resolved through Unbound (`DNS Servers: 127.0.0.1 ::1`). The DHCP entry was added with `andaru ctl config create dhcp`, with a static lease, and applied.
- **Client:** it got `192.168.40.128` with the firewall as router and DNS server and `home.arpa` as domain, resolved Internet names through the firewall and fetched `http://deb.debian.org/`.
- **Lease names:** the client's hostname resolved forward and reverse as `<name>.home.arpa`, from the client and from the firewall, and the static lease's name `nas.home.arpa` too. Removing the lease in Kea removed the name at the next sync.
- **DNS over TLS:** forwarding to a `dot` upstream resolved; a wrong `tlsName` failed resolution (`SERVFAIL`) while the apply itself succeeded; the right name resolved again. The lab reaches port 853 only on its own resolver, which was used.
- **Unconfirmed apply:** deleting the DHCP entry stopped Kea; nobody confirmed, the timer rolled back, and Kea served again with its file and the filter's DHCP rule.
- **Reboot:** Kea and Unbound came back with the firewall resolving through Unbound, the client got its lease again, and its name was published.

The lab found and this branch fixes: the saved declaration dropped the new sections, a netplan host with DNS servers was refused although the service resolves for it, Unbound did not bind the LAN address after a reload, Debian's AppArmor profiles blocked both daemons from Andaru's files, and resolved started before the boot unit placed its drop-in. The assistant's DHCP question is covered by tests, not driven in the lab.

### IPv6 LAN Debian lab qualification

On 2026-11-12 the CGO-disabled amd64 build of code commit `3ad4ac3` (`vcs.modified=false`, SHA-256 `96dd007dc8e1f18f3652e5e160357608d9509374af544510e2ebee3987a846d4`) was installed on VM 114 at snapshot `base-netplan-static-20261111`, set up as in the DHCP and DNS qualification (`eth0` WAN at `10.0.1.19/16`, `eth1` LAN at `192.168.40.1/24` on VLAN 4093, systemd 257.13, `unbound` 1.26.2), with a third adapter, `ens20`, on VLAN 4092. There an LXC router stands in for the provider: Kea DHCPv6 2.6.3 delegates `/56`s from `fd00:a5d::/49` (renew 300 s, preferred 900 s, valid 1800 s) and routes each to the requesting router, and radvd advertises `fd00:a5d:ffff::/64` with the M and O flags; the router is `fd00:a5d:ffff::1`. There is no IPv6 internet. The LAN client, VM 117, ran systemd-networkd with `192.168.40.2/24`, `IPv6AcceptRA=yes` and `UseDNS=yes`. The prefix change was recorded earlier the same day on the build of `c3783db` (SHA-256 `ec3800cb…`), with the same setup; the guests were reset to their snapshots in between.

- **Takeover:** `--accept-detected` took over `eth0` and `eth1`, confirmed from a new SSH session. netplan did not configure `ens20`, so it was not proposed.
- **Configuration** with `andaru ctl config`: a link for `ens20`'s MAC (named `eth2`; the adapter kept its kernel name, `Name=ens20`) and a `wan` segment with IPv4 `none` and `{"mode":"dhcp6","requestPrefixLength":56,"prefixOnly":true}`; the LAN `{"mode":"delegated","from":"<ens20 segment>","subnetId":"0","addresses":["fd00:beef:1::1/64"],"ra":{"mode":"unmanaged"}}`. The takeover's `dns.segments` already held the LAN. The preview had no findings and rendered `UseAddress=no`, `SendRelease=no`, `PrefixDelegationHint=::/56`; `UplinkInterface=ens20`, `SubnetId=0x0`, `Announce=yes`; `EmitDNS=yes`, `DNS=fd00:beef:1::1`, `Domains=home.arpa lab.example`; `Prefix=fd00:beef:1::/64`; and Unbound's `interface: fd00:beef:1::1` with `interface-action: fd00:beef:1::1 allow`.
- **Apply:** verified on its own within 3 seconds and was confirmed from a new SSH session; `resolvectl dns` still listed no server for `eth1`. On `c3783db` the same apply had failed after 30 seconds with `not verified in time: eth1 resolves through [], not the declared [fd00:beef:1::1]`.
- **Delegation:** within 3 seconds of the apply, `ens20: DHCP: received delegated prefix fd00:a5d::/56` and `eth1: DHCP-PD address fd00:a5d::be24:11ff:fe71:745/64 (valid for 29min 59s, preferred for 14min 59s)`. On `c3783db` also `unreachable fd00:a5d::/56 dev lo proto dhcp` and, on the router, `fd00:a5d::/56 via fe80::be24:11ff:fe53:9dd5 dev eth1`.
- **Client:** `fd00:a5d::be24:11ff:fe38:7137/64` with the lease's lifetimes and `fd00:beef:1:0:be24:11ff:fe38:7137/64` with valid 3600 s and preferred 1800 s (networkd's defaults: a delegated segment's stable prefix takes no lifetimes); `DNS: fd00:beef:1::1`, search domains `home.arpa` and `lab.example`. `dig AAAA debian.org @fd00:beef:1::1` returned four records, also from the delegated source address; `ping -6 fd00:a5d:ffff::1` answered 3 of 3 (`ttl=63`) through the starter rule.
- **Prefix change** (on `c3783db`): the router's pool became `fd00:a5d:100::/56` and Kea restarted. `networkctl renew ens20` sent Kea nothing, twice; at the renew timer Kea logged `DHCP6_PD_LEASE_RENEW … fd00:a5d:100::/56` and zeroed the old lease. `eth1` swapped to `fd00:a5d:100:0:be24:11ff:fe17:6841/64` at once; the client gained `fd00:a5d:100:0:be24:11ff:fe38:7137/64`, chose it as source, and `dig` against the unique local address answered. **The old prefix was not deprecated:** the next advertisement, solicited and captured, carried only `fd00:a5d:100::/64` and `fd00:beef:1::/64`, with no zero-lifetime entry for `fd00:a5d::/64`. The client kept `fd00:a5d::be24:11ff:fe38:7137/64` preferred until the lease's preferred lifetime ran out, 10 minutes after the change (`deprecated … preferred_lft 0sec`, `valid_lft 883sec`). The stand-in router kept routing the old `/56`, as its hook saw no deleted lease.
- **Reboot:** the boot unit placed generation 2 (`confirmed` and `live` 2); 4 seconds after networkd started, `ens20: DHCP: received delegated prefix fd00:a5d::/56` and `eth1: DHCP-PD address fd00:a5d::be24:11ff:fe71:745/64`, beside `fd00:beef:1::1/64`, and Unbound listened on `[fd00:beef:1::1]:53`. The client's `dig` answered and its ping went through, as after the reboot on `c3783db`, which followed the prefix change.

The lab found and this branch fixes: the apply's check read every `DNS=` line of a `.network` file, so the `[IPv6SendRA]` section's advertised server counted as the host's own, and where systemd-resolved is the resolver every apply that advertised a DNS server failed.

The lab found, not yet fixed:

- networkd 257 does not deprecate a replaced delegated prefix, as the design expects it to: clients keep the old address preferred for the rest of the lease's preferred lifetime, hours at many providers, while the provider no longer routes it.
- After the failed apply's rollback on `c3783db`, `eth1`, IPv6 `none` in generation 1, still had `fd00:a5d::be24:11ff:fe17:6841/64` (1721 s valid) when the next apply began.
- `networkctl renew` sends no DHCPv6 message in systemd 257, so the prefix change waited for the renew timer.

### Delegated DHCPv6 Debian lab qualification

On 2026-11-13 the CGO-disabled amd64 builds of `a8d9c7b` and, after the review and lab fixes, `41b53ca` (SHA-256 `5203eecae5ff62d0015dc0112c5ca9c07dbc0e25bf3edd1483fffa0134cb7cbc`) were installed on VM 114, set up as in the IPv6 LAN qualification: `ens20` on VLAN 4092 behind the LXC router that delegates `/56`s from `fd00:a5d::/49` (renew 300 s, valid 1800 s), and the LAN on VLAN 4093. The WAN segment requested a `/56`; the LAN was `delegated` at subnet `1` with `ra.mode` `managed`, and its `dhcp6` entry had the ranges `::1000`–`::1fff`, a static lease `::10` for VM 117, a static lease at `prefixId` `1` for a downstream router (an LXC running networkd as a prefix-delegation client), and the pool `{"subnetId":"8","length":60,"delegatedLength":64}`.

- **Apply:** `kea-dhcp6 -t` judged the template filled with `2001:db8:0:1::/64` and the pool `2001:db8:0:80::/60`; `andaru ctl prefixes` showed `fd00:a5d::/56`, subnet IDs `0`–`ff`, the LAN at `fd00:a5d:0:1::/64` and the pool at `fd00:a5d:0:80::/60`.
- **Clients:** VM 117 took `fd00:a5d:0:1::10`, and `client117.home.arpa` answered AAAA and PTR. The router got `fd00:a5d:0:81::/64`, its fixed position, routed `via fe80::… dev eth1 proto andaru` beside `unreachable fd00:a5d:0:80::/60 proto andaru`; pings from behind it crossed the firewall.
- **Prefix change:** with the router's pool moved to `fd00:a5d:100::/56`, at the WAN's renewal Kea took the new subnet by `config-set` (same process), Andaru advertised the old `/64` with preferred lifetime 0 eleven times over ten minutes (the first 108 ms after Kea took the new prefix), VM 117's old SLAAC address became `deprecated`, and its DHCPv6 address and both delegated prefixes moved at their renewals. The old pool's routes stayed until the old prefix's valid lifetime ended. The 108 ms is not the shipped behaviour: `41b53ca` read the clock separately when recording the change and when sending, so the 5-second wait (`DeprecateAfter`) for networkd's own advertisement never ran. The shipped code reads it once and waits.
- **Reboot** with the delegating router's Kea stopped: `andaru-prefixes.path` stayed active, Kea served `fd00:a5d:100:1::/64` from the saved prefix 0.1 s after it started, VM 117 got a lease before the WAN had a prefix, and the routes came back.

The lab found and this branch fixes: the old pool's routes went a second after the change, while Kea still held its leases, so the old prefixes followed the default route; and networkd's burst of link-file rewrites at boot hit the service's start limit, which stopped the path unit.

Not run on the lab: a prefix shorter than requested, graceful renumbering, the saved prefix expiring, and a `config-set` Kea refuses; unit tests cover them.

### Virtual addresses Debian lab qualification

On 2026-11-14 the CGO-disabled amd64 build of `a972464` (SHA-256 `afb98aeb2dfe7c3fc26d02c0f0aa16e1c7e2fd68fccc98da85f5091b92b05477`) was installed on VM 114, set up as in the DHCP and DNS and IPv6 LAN qualifications, with VM 117 on the LAN and a WAN client in a namespace on the lab network. The declaration had virtual addresses `172.16.53.53` and `fd00:53:53:53:53:53:53:53`, the DNS service listening on both and on the LAN, `dhcp` and `dhcp6` handing them out as DNS servers, and port forwards from the WAN zone sending port 53 of the firewall itself to them: IPv4 (UDP and TCP) to `172.16.53.53`, IPv6 (`family: ipv6`) to `fd00:53:53:53:53:53:53:53`.

- `andaru-vip` held both addresses, `routable (configured)`, and Unbound listened on both.
- VM 117 received `172.16.53.53` by DHCP and `fd00:53:53:53:53:53:53:53` by DHCPv6; its own lease name answered through both and through the firewall's LAN addresses.
- From the WAN, `dig @10.0.1.19` and `dig @fd00:feed::19` were answered over UDP and TCP through the forwards; queries sent to either virtual address directly were not.
- Deleting or re-addressing the IPv4 virtual address while the forward targeted it was refused; removing one virtual address took it off the device, and removing all deleted `andaru-vip`.
- After a reboot all of the above held again.

The lab found, fixed in this branch on `98771b1`: the refusal named the forward only after a DNS reference to the same address was gone, and for an IPv6 virtual address it gave a generic message.

Not run on the lab: public names (the lab's upstream refuses queries from VM 114), forwards from the LAN zone, and the refusals the unit tests cover.

### Inbound NAT Debian lab qualification

On 2026-11-12 the CGO-disabled amd64 build of code commit `6443d4e` (`vcs.modified=false`, SHA-256 `4cdc5f88a44c7bc95a566d6e40b24d9e61d010d006adb8a051d9f5a68514024d`) was installed on VM 114 at snapshot `base-netplan-static-20261111` (Debian 13, systemd 257.13, nftables 1.1.3), with `eth0` WAN at `10.0.1.19/16` and `eth1` LAN at `192.168.40.1/24` on VLAN 4093; DNS servers were removed from `eth1`'s netplan before the takeover. VM 117 (`base-ifupdown-static-20261112`) sat on the LAN at `192.168.40.2/24` via `192.168.40.1`, with `python3 -m http.server` on ports 80, 8000 and 8001. The WAN client was a host on the lab network, `10.0.0.30`, instead of the PD router, whose SSH key was not at hand. `10.0.1.29` was free: no answer to ping, the neighbour entry `INCOMPLETE` from the client and from VM 114, and no ARP entry or DHCP lease on the lab gateway.

- **Takeover:** `--accept-detected`, confirmed from a new SSH session; `confirmed` and `live` 1.
- **Forward:** `10.0.1.29/16` added to the WAN segment and a forward `web` (WAN, `to` `self`, tcp 8080 to `192.168.40.2:80`) created with `andaru ctl config`; the preview had no findings. Applied and confirmed: `prerouting` held `iifname @zon_… fib daddr . iif type local tcp dport 8080 dnat ip to 192.168.40.2:80` and `forward` `iifname @zon_… ct status dnat ct original proto-dst 8080 ip daddr 192.168.40.2 tcp dport 80 accept`. `curl http://10.0.1.19:8080/` returned VM 117's page, and its log showed the client, `10.0.0.30`.
- **Direct connection:** with a route to `192.168.40.0/24` via `10.0.1.19` on the client, `curl http://192.168.40.2/` timed out; `tcpdump` on VM 114 saw five SYNs arrive on `eth0` and none leave. The route was removed.
- **Shifted range:** WAN 9000–9001 to 8000: `dnat ip to tcp dport map { 9000 : 192.168.40.2 . 8000, 9001 : 192.168.40.2 . 8001 }`; port 9000 reached the 8000 listener and 9001 the 8001 one.
- **To the firewall:** WAN `self:2222` to `self` port 22 rendered `redirect to :22` and, in `input`, `ct status dnat ct original proto-dst 2222 tcp dport 22 accept`; `ssh -p 2222` reached VM 114's sshd (`SSH_CONNECTION=10.0.0.30 … 10.0.1.19 22`). The management SSH rule comes first and also accepts port 22 on `eth0`, so `management.allowedSources` was then set to another host: port 22 timed out while `ssh -p 2222` still connected and confirmed that apply. The associated rule alone admits the forward. `allowedSources` was removed again.
- **Reflection** on `web` added `iifname @internal fib daddr type local ip daddr != @internal_4 tcp dport 8080 dnat ip to 192.168.40.2:80` and `oifname "eth1" ct status dnat ip saddr 192.168.40.0/24 masquerade`. From VM 117, `curl http://10.0.1.19:8080/` returned its own page with `192.168.40.1` as the peer; `http://192.168.40.1:8080/` was not redirected (it timed out at the input policy).
- **1:1** from `10.0.1.29` to `192.168.40.2` and a forward rule accepting the WAN zone to an address object for VM 117: `dnat ip to ip daddr map { 10.0.1.29 : 192.168.40.2 }` and `snat ip to ip saddr map { 192.168.40.2 : 10.0.1.29 }`. `curl http://10.0.1.29/` reached VM 117 (peer `10.0.0.30`); VM 117's ping and TCP connection to the client arrived as `10.0.1.29 > 10.0.0.30: ICMP echo request` and `10.0.1.29.51822 > 10.0.0.30.2223: Flags [S]`. The firewall's own traffic still left from `10.0.1.19`.
- **Reboot:** the boot unit succeeded (`confirmed` and `live` 7, boot in 6.5 s); 8080, 9000, 9001, `ssh -p 2222`, reflection (peer `192.168.40.1`) and 1:1 in both directions worked again.
- **Sources:** `web` with `from` set to another host and `fromNegated` rendered `ip saddr != @adr_…_4` in its `prerouting` and `forward` lines, and the client still reached VM 117. With `fromNegated` off it rendered `ip saddr @adr_…_4`: the client's `curl` timed out, but VM 117's reflected `curl` still reached itself.

The lab found, and this branch fixes: **reflection ignored a forward's `from`**, so every internal client was reflected; the reflection line now carries the forward's source match, as pfSense's reflection `rdr` does (`from {$srcaddr}`, `filter.inc`). Review after the lab also fixed: deleting `nat.outbound` dropped the NAT lists, a `self` forward also matched IPv6, `GET /api/v1/config` broke the served schema once NAT lists existed, and a `self` target on an unshifted range sent every port to the first. The table rendered by the final build differs from the one above by `meta nfproto ipv4` in front of `fib daddr . iif type local`.

Not run on the lab: UDP (unit-tested), logging, exclusions, 1:1 prefixes with `destination` and `noBinat` (rendered-table goldens, loaded by `nft -c`), and `PROXY_ARP_REQUIRED` (unit-tested).

### DHCPv6 Debian lab qualification

On 2026-11-13 the CGO-disabled amd64 build of code commit `6254e98` (`vcs.modified=false`, SHA-256 `d3182fc4c08d417898bd067c009c4d7d6552c51f9b0d9f330ae0ca2f6a710e1b`) was installed on VM 114 at snapshot `base-netplan-static-20261111` (Debian 13, systemd 257.13, `kea-dhcp6-server` 2.6.3, `unbound` 1.26.2, nftables 1.1.3), with `eth0` WAN at `10.0.1.19/16` and `eth1` LAN at `192.168.40.1/24` on VLAN 4093, the packaged files of both sections above, and the AppArmor fix below applied by hand. DNS servers were removed from `eth1`'s netplan before the takeover. On the LAN: VM 117 (`base-ifupdown-static-20261112`, moved to systemd-networkd: `192.168.40.2/24`, `IPv6AcceptRA=yes`) and a throwaway Debian 13 LXC router running networkd with `DHCP=ipv6`, `[DHCPv6] PrefixDelegationHint=::/56` and `SendRelease=yes` on `eth0`, and a dummy `lan0` with `DHCPPrefixDelegation=yes`.

- **Takeover:** `--accept-detected`, confirmed from a new SSH session. Its DNS service already served the LAN; its starter input rule `lan to firewall: ping and DNS` was the only input rule.
- **Configuration** with `andaru ctl config`: the LAN's IPv6 `{"mode":"static","addresses":["fd00:beef:1::1/64"],"ra":{"mode":"managed"}}` and the `dhcp6` entry of the example above. The preview had no findings and rendered `Managed=yes`, `[Route] Destination=fd00:beef:100::/48` `Type=unreachable`; a Kea subnet on `eth1` with `pd-pools` `fd00:beef:100::/48` `delegated-len` 56, `preferred-lifetime` 4500, `dns-servers` `fd00:beef:1::1` and `domain-search` `home.arpa, lab.example`; and Unbound's `access-control: fd00:beef:100::/48 allow`. Applied and confirmed: `unreachable fd00:beef:100::/48 dev lo proto static`.
- **Client:** VM 117 first got `fd00:beef:1::1000/128` (valid 2 h, preferred 1 h 15 min) with `DHCPv6 Client DUID: DUID-EN/Vendor:0000ab11220eb653c48b0cf9`. networkd's client asks for a prefix by default, so it also got `fd00:beef:100::/56` and a route to it. A static lease for `00:02:00:00:ab:11:22:0e:b6:53:c4:8b:0c:f9` (`fd00:beef:1::10`, `client117`) was added and applied; after a networkd restart the client had `fd00:beef:1::10/128`. From the client, `dig AAAA client117.home.arpa @fd00:beef:1::1` answered `fd00:beef:1::10` and `dig -x` answered `client117.home.arpa.`; the router's name was published as `pd-client.home.arpa. 2400 IN AAAA fd00:beef:1::1000`.
- **Delegation:** 4 seconds after the router's networkd started, `eth0: DHCP: received delegated prefix fd00:beef:100:100::/56` and `lan0: DHCP-PD address fd00:beef:100:100:f0dc:72ff:fe8d:42fa/64`. On the firewall `ip -6 route show proto 188` listed `fd00:beef:100:100::/56 via fe80::be24:11ff:feaa:4f25 dev eth1` beside VM 117's `/56`, and `pd-next-hops.json` held both. `ping -6 -I` from the `lan0` address to `fd00:beef:1::1` answered 3 of 3 through the starter rule; no rule was added.
- **networkd restart:** both routes stayed and the ping went through.
- **Reboot:** the boot unit finished at 11:12:42.90, networkd started at 43.57, Kea at 43.70, and `andaru-leases.service` finished at 44.99 (`confirmed` and `live` 3). Both routes were back and the ping went through; the router's last DHCPv6 exchange was at 11:11:13, before the reboot, and Kea logged no lease after it.
- **Release:** stopping the router's networkd at 11:13:29.30 sent a release: `DHCP6_RELEASE_PD … prefix fd00:beef:100:100::/56 … was released properly` at 29.35. `andaru-leases.service` finished at 30.49, the route was gone and the table held only VM 117's prefix.
- **Protocol 77:** with code commit `82654e0` (SHA-256 `54a4f3754fc8d2162af707bd7df0a2a9d14bd457bcaf9218ce0df04825128513`) and `/etc/iproute2/rt_protos.d/andaru.conf` installed, the router's next delegation showed both routes as `via … proto andaru`, with no protocol 188 route left. The ping answered 2 of 2.

The lab found and this branch fixes:

- The AppArmor rule let Kea's hook append to `/run/kea/andaru-pd-events` but not create it (`apparmor="DENIED" operation="mknod" … name="/run/kea/andaru-pd-events"`), so no route was installed. It now grants `w`.
- Protocol 188 is `RTPROT_OSPF`, and `ip route` printed it as `proto ospf`. Removing every protocol 188 route nobody holds would have deleted a routing daemon's OSPF routes. The routes now use protocol 77, which no routing protocol is assigned, and it is named `andaru`.

The lab ran on 188; only the protocol 77 check above ran on `82654e0`. When Kea started on the apply, `eth1`'s link-local address was not yet usable (`Failed to bind socket … fe80::be24:11ff:fe71:745/port=547: Cannot assign requested address`); the socket retry bound it, and the client's lease came 3 seconds later.
