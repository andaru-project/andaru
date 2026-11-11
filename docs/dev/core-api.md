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

CI targets native Linux amd64 and arm64 on Ubuntu 24.04; the persistent Debian lab remains a separate amd64 qualification environment. Native CI does not establish ARM appliance boot or Debian arm64 qualification. Go selects the required patched toolchain through its normal verified toolchain mechanism if the installed compiler is older. Build in a task-owned worktree; do not write binaries into tracked source directories.

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
| `GET /api/v1/preview/network` | Files a saved version would hand to systemd-networkd and pppd on this host, redacted, with findings; `state:"preview"`; optional `?savedVersion=N` |
| `GET /api/v1/takeover/proposal` | What a first takeover of this host would save, as a change batch, with the host's adapters, its `state` and findings; writes nothing |
| `POST /api/v1/preview/network/draft` | The files and findings a change batch would produce if saved now, as the preview and an apply would judge them; saves nothing |

GET resources support HEAD without a body. Unsupported methods return `405 METHOD_NOT_ALLOWED` with the resource's `Allow` methods; unknown resources return `404 NOT_FOUND`. Missing/unverified/non-root peers get `403 FORBIDDEN` before routing/body processing. Inventory failures return `503 INVENTORY_UNAVAILABLE`, never an empty/partial success. Errors share `{"error":{"code":"...","message":"...","details":[]}}`; field details use safe request-relative JSON Pointers and conflicts may add redacted `error.current`. JSON responses are `no-store`; diagnostics never quote secret values.

`ctl` writes successful validated JSON to stdout. Transport, API, malformed-envelope and oversized-response failures exit `1` without success stdout; invalid command/arguments exit `2`. Error diagnostics are on stderr. A broken stdout pipe can of course truncate a write; the CLI reports write failure rather than returning a successful exit. Help is available with `--help`.

Each link contains `ifIndex`, `name`, `linkType`, `mtu`, `operState`, `flags`, optional `linkAddress`, and `addresses`. Addresses contain `family` (`ipv4`/`ipv6`), the host `address`, `prefixLength` and `scope`. Empty lists are `[]`, not `null`. Links are ordered by current kernel index; address-list order is not policy order. These are observations, **not** immutable configuration IDs or permanent-MAC identity. Loopback `UNKNOWN` and an all-zero link-layer address are preserved when reported. An absent link-layer address is omitted, not invented.

The engine runs exactly `/usr/bin/ip -d -j address show` through an argument vector, without a shell or command override. It ignores unrelated tool JSON fields, validates required link/address data and fails the entire read on invalid data. Fixed bounds are a 2-second child deadline, 4 MiB stdout, 4 KiB retained stderr, and a 5-second/8 MiB CLI response budget. No stale snapshot cache is used.

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

A change batch may carry `"expectedSavedVersion": "N"`: the batch is then refused
with `412` at `/expectedSavedVersion`, before any operation, unless N is still
the saved head. The takeover assistant uses it so a save from another session
between its proposal and its commit is never overwritten.

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

## Network preview

`andaru ctl preview network [--saved-version N]` shows exactly which files Andaru would give systemd-networkd and pppd for a saved version on this host, and what would stop them working. **It changes nothing**: no file is written under `/run`, nothing is reloaded or started, and no state is stored. Applying is a separate, explicit step: see [Applying and taking over](#applying-and-taking-over).

```sh
sudo andaru ctl preview network | jq -r '.files[] | "### \(.path) \(.mode)\n\(.content)"'
sudo andaru ctl preview network | jq '.realizable, .findings'
```

The response has `state: "preview"`, the `savedVersion` rendered, `realizable` (true when no finding is an error), `resolved` (which kernel interface currently carries each declared permanent MAC), `files` and `findings`. Each file has its destination `path`, `mode`, `content` and the declaration objects it came from. A file marked `secret` shows `<redacted>` where the real file holds a secret; no command exports the real value.

What is rendered:

- **Ethernet, VLAN, bridge, bond:** `/run/systemd/network/50-andaru-<name>.netdev` and `.network`. Physical NICs are matched by `PermanentMACAddress=`, and a `.link` file keeps the name each has now across reboots: after a takeover nothing else names them (netplan's own `.link` files did until then), and the boot service has udev apply Andaru's before networkd starts. Virtual links get a derived name: `vl`, `br`, `bo` or `pp` plus the first 12 hex digits of the link ID. Your own `name` and `description` never appear in a rendered file.
- **PPPoE:** pppd runs the session and owns IPv4; networkd keeps IPv6 (router discovery, DHCPv6, prefix delegation) on the PPP interface. Three files: `/run/andaru/ppp/<pp-name>.options` (mode `0600`, secret), `/run/systemd/system/andaru-pppoe-<pp-name>.service`, and a `.network` with `KeepConfiguration=yes` so networkd leaves pppd's address and default route alone. The split follows [systemd#481](https://github.com/systemd/systemd/issues/481).
- Provider-offered DNS, NTP, hostname and search domains are ignored on purpose; those are later areas.

| Finding | Severity | Meaning and remedy |
| --- | --- | --- |
| `HARDWARE_MISSING` | error | No physical interface has the declared permanent MAC. Fit the card or correct `permanentMac`. |
| `HARDWARE_AMBIGUOUS` | error | Several physical interfaces report that MAC (cloned VMs). Give them distinct MACs. |
| `NAME_COLLISION` | error | A derived interface name is already used by something else. Remove or rename the foreign interface. |
| `FOREIGN_CONFIGURATION` | error | A networkd file Andaru did not write exists in `/etc/systemd/network` or `/run/systemd/network` (netplan on cloud images), or a `<file>.d/*.conf` drop-in exists there. It would override or conflict; the takeover step removes it. |
| `TOOL_MISSING` | error | A PPPoE link needs `pppd` and its `pppoe.so` plugin: install `ppp`. |
| `KERNEL_SUPPORT_MISSING` | error | `/dev/ppp` is absent. Try `modprobe ppp_generic`. If the module does not exist, the kernel has no PPP: Debian's cloud kernel (`linux-image-cloud-*`) omits it, so install `linux-image-amd64` or `linux-image-arm64` and boot it. |
| `INTERFACE_NAME_UNSUPPORTED` | error | A PPPoE parent NIC has a kernel name outside `[A-Za-z0-9_.:-]`. |
| `CREDENTIAL_TOO_LONG` | error | pppd truncates user names and passwords over 255 bytes. |
| `UNSUPPORTED_COMBINATION` | error or warning | A PPPoE segment may use IPv4 `ppp`/`none` and IPv6 `none`/`slaac`/`dhcp6`, at least one of them, without RA. A PPPoE link with no segment is left out (warning). |
| `DEPENDENCY_UNAVAILABLE` | warning | Something was left out because a link it needs is missing, ambiguous or disabled. A bridge or bond keeps its remaining members. |
| `RA_PREFIX_UNSUPPORTED` | warning | Router advertisements were requested where nothing can be announced. |
| `DNS_NOT_APPLIED` | warning | A segment declares DNS servers, but this host's `/etc/resolv.conf` is not managed by systemd-resolved, so they are saved and not used. |

### Trying a PPPoE line by hand

The preview is not exercised against a live line. Until apply and rollback exist you can try one by hand, as root, on a host running the standard Debian kernel with `ppp` installed and systemd-networkd active. **This changes live networking with no rollback and no Andaru packet filter. Do it from the console, not over the network you are about to change.**

- pppd installs an IPv4 default route. It can replace the route your management session uses.
- Write **only the WAN-side files**: the WAN NIC's `.network`, the VLAN's `.netdev` and `.network`, the `pp…` `.network`, the `.options` file and the `.service`. Do not write a LAN `.network` that has `IPv6SendRA=yes` or `DHCPPrefixDelegation=yes`: it turns on IPv6 forwarding and announces a prefix, and nothing filters the forwarded traffic yet.
- Do not stop or restart `andaru-engine` during the test. `/run/andaru` is its runtime directory, and systemd deletes it, including `ppp/*.options`, when the engine stops.

Steps:

1. Write each chosen file to its `path` with its `mode`, creating `/run/andaru/ppp` as `0700`. In the `.options` file replace the word `<redacted>` with the password, keeping the quotes; escape `\` and `"` with a backslash.
2. `networkctl reload`, then `systemctl daemon-reload` and `systemctl start andaru-pppoe-<pp-name>.service`.
3. Watch `journalctl -fu andaru-pppoe-<pp-name>.service` and `networkctl status <pp-name>`.

To undo: stop the unit, delete the files you wrote, `systemctl daemon-reload`, `networkctl reload`, and `networkctl delete` the `vl…` device. Everything is under `/run`, so a reboot also clears it. Expect the `[DHCPv6]` keys to be the part that depends on your provider.

## Applying and taking over

An apply makes the saved head's network configuration live. Design and limits: [network takeover and confirmed apply](../superpowers/specs/2026-11-11-network-takeover-apply-design.md).

```sh
sudo andaru ctl preview network                      # note the savedVersion and the findings
sudo andaru ctl apply network --saved-version 12     # add --takeover on a host netplan still configures
# from a NEW terminal or SSH session:
sudo andaru ctl applies confirm 1
```

What happens, in order:

1. **Checks.** `--saved-version` must be the saved head, so you apply what you previewed. The apply is refused (`APPLY_REFUSED`, with findings) if the preview has errors, if an interface that has an address now would be left unconfigured, or if it needs something not yet supported.
2. **A rollback timer is armed first**, owned by systemd, not by the engine. It is set for the timeout plus 60 seconds from the start of the apply, so it fires at most 60 seconds after the confirmation deadline, and it retries every 10 seconds if the rollback fails.
3. **The files are swapped and networkd reloaded.** Andaru then checks that every file is in use, static addresses are present, and the interfaces that had an IPv4 address and default route still have them. If not, it restores the previous files at once and the apply reads `failed`.
4. **You confirm from a new session** within the timeout (default 120 seconds, `--confirm-timeout 30`–`600`). A session that survived the change does not show that a new one can be opened; opening one is the test. Without confirmation the previous configuration returns, including after an engine crash or a reboot.

`andaru ctl applies list` shows every apply and four pointers: `confirmed` (what boot uses), `lastKnownGood` (the one before it), `pending`, and `live` (what runs now; `null` is the pre-Andaru configuration). `andaru ctl applies rollback ID` undoes a pending apply without waiting.

| Finding | Meaning |
| --- | --- |
| `WOULD_UNCONFIGURE` | A physical interface with an address is not configured by the saved configuration. Declare it as an enabled Ethernet link with a segment. |
| `TAKEOVER_MISMATCH` | netplan still configures the host and `--takeover` was not given, or it was given with nothing to take over. |
| `TAKEOVER_UNSUPPORTED_BASELINE` | The host is not one Andaru can take over: both netplan and ifupdown configure it, netplan YAML lives outside `/etc/netplan`, a network file that neither netplan nor Andaru wrote exists, netplan created a bridge, bond or VLAN, netplan set DNS servers and the saved configuration declares none for that interface, or `/etc/network/interfaces` uses something beyond the Debian installer's shapes (see [ifupdown hosts](#taking-over-an-ifupdown-host)). |
| `RESOLVED_NOT_IN_CHARGE` | An ifupdown host's `/etc/resolv.conf` is not managed by systemd-resolved, which becomes the host's resolver. Run `apt install systemd-resolved` first. |
| `DHCP_IDENTITY_UNKNOWN` | An ifupdown host's DHCP client is not dhcpcd with its default identity settings, so networkd could not keep the lease. |
| `DNS_REQUIRED` | No interface of the ifupdown host takes DNS servers from DHCP or router advertisements, and no segment declares `dns`. The message lists the servers in use now. |
| `APPLY_UNSUPPORTED` | PPPoE links, delegated prefixes and router advertisements wait for the firewall. |
| `NETDEV_CHANGE_UNSUPPORTED` | An existing VLAN, bridge or bond would change. Remove it in one apply and add it again in the next. |
| `BOOT_UNIT_DISABLED` | `andaru-network-boot.service` is not enabled, so a confirmed configuration would not return after a reboot. |

### Taking over with the assistant

On a host with no saved network configuration yet, set up by netplan or by the Debian installer's ifupdown, the assistant asks the questions and does the rest. Design and limits: [interactive takeover](../superpowers/specs/2026-11-11-interactive-takeover-design.md).

```sh
sudo andaru ctl takeover                    # questions, pre-filled with what the host uses now
sudo andaru ctl takeover --accept-detected  # no questions: take over exactly what was detected
```

1. **Detect.** The engine reads the files netplan generated, or `/etc/network/interfaces`, and proposes one Ethernet link and segment per adapter, with its zone (`wan` for the adapter carrying the default route, `lan` for the others), addressing and DNS servers. On an all-static ifupdown host it proposes the DNS servers systemd-resolved uses now.
2. **Ask.** Which adapters to keep (an adapter with an address cannot be left out, and on an ifupdown host none can), then per adapter its zone, IPv4 (DHCP, static addresses and gateway, or none) and IPv6 (none, SLAAC, DHCPv6, or a static IPv6 kept as detected), the host's DNS servers, and the confirmation timeout. It runs in any terminal, including the serial console; a console that reports no size is treated as 80×24.
3. **Review.** The assistant shows, per adapter, what runs now and what will run, then the exact files and every finding, from a draft that saves nothing. **Back** is preselected; **Apply** is offered only when nothing would refuse the apply.
4. **Apply.** The answers are saved, guarded by the saved version the proposal was made from, and applied as `andaru ctl apply network --takeover` would, with the rollback timer. Confirm from a new session as usual. From the save on, a dropped session no longer stops the assistant.

Quitting, pressing Ctrl+C or losing the session before **Apply** writes nothing. Without a terminal the assistant refuses and points to `--accept-detected`, which applies only when the detection is complete and nothing would refuse it.

The assistant only takes over: on a host already taken over, or with a configuration already saved, it says what to run instead and changes nothing. It cannot express everything netplan or ifupdown can; when it meets a construct it cannot reproduce, such as a non-default route, a routing policy, a MAC change, DNS set on several interfaces in one file, or an ifupdown hook, it lists it as an error and does not offer **Apply**. If a save from another session lands between the proposal and **Apply**, nothing is applied and the assistant asks to be run again. If the apply fails after the save, it prints the saved version and the command to retry.

### Host DNS servers

One segment may say which DNS servers the firewall host itself asks, as netplan's `nameservers:` does. Design and limits: [static DNS servers for the host](../superpowers/specs/2026-11-11-static-host-dns-design.md).

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
- It works where systemd-resolved manages `/etc/resolv.conf` (Debian cloud images). Elsewhere the preview warns with `DNS_NOT_APPLIED` and the host keeps using its `resolv.conf`.
- To take over a netplan host that sets DNS servers, declare `dns` on the segment of the same interface. A netplan file that sets DNS for several interfaces at once cannot be taken over yet.

### Takeover

`--takeover` replaces netplan's generated files under `/run/systemd/network`. **Until you confirm, nothing outside `/run` changes, so a reboot returns the host to netplan.** Confirming then enables `systemd-networkd` (netplan's generator was doing that), disables `systemd-networkd-wait-online` (no Andaru interface is required for "online", so it would hold every boot for its two-minute timeout), writes `/etc/cloud/cloud.cfg.d/99-andaru-network.cfg` (`network: {config: disabled}`) and moves `/etc/netplan/*.yaml` to `/var/lib/andaru/generations/baseline/netplan/`. The packages stay installed.

Boot needs `andaru-network-boot.service` ([unit](../../packaging/systemd/andaru-network-boot.service)) installed and enabled **before** the takeover: it copies the confirmed generation into `/run` before networkd starts. If that generation is damaged it uses the last-known-good one, and `live` then differs from `confirmed`.

What changes for the host after a takeover:

- On cloud images the interface name changes at the next reboot (`eth0` becomes `ens18`): netplan was pinning it. Andaru matches by permanent MAC, so the configuration follows.
- The host uses the DNS servers you declare, or the DHCP-provided ones when you declare none. NTP servers and hostname from DHCP are ignored.
- `network-online.target` no longer waits for an uplink.
- An apply interrupts IPv6 on the touched interface for about two seconds; IPv4 was uninterrupted on the lab.

To undo a confirmed takeover by hand, from the console:

```sh
sudo systemctl disable --now andaru-engine.service andaru-network-boot.service
sudo mv /var/lib/andaru/generations/baseline/netplan/*.yaml /etc/netplan/
sudo rm /etc/cloud/cloud.cfg.d/99-andaru-network.cfg
sudo systemctl enable systemd-networkd-wait-online.service
sudo rm -r /var/lib/andaru/generations
sudo reboot
```

### Taking over an ifupdown host

A host the Debian installer set up configures its network with ifupdown and dhcpcd, without netplan. `--takeover` (and the assistant) accept it when it has only the installer's shapes. Design and limits: [taking over a default Debian install](../superpowers/specs/2026-11-11-ifupdown-takeover-design.md).

- **Accepted:** `/etc/network/interfaces` and the files it sources with `auto`, `allow-hotplug`, `source`, `source-directory`, `iface lo inet loopback`, and per physical interface `inet dhcp` (option `hostname`) or `inet static` and `inet6 auto` or `inet6 static` (options `address` with a prefix length, and `gateway`). Anything else, a NetworkManager or `dhcpcd.service` beside ifupdown, or a cloud-init file in `interfaces.d`, is `TAKEOVER_UNSUPPORTED_BASELINE`.
- **Before you start:** note the servers in `/etc/resolv.conf`, then `sudo apt install systemd-resolved` and reboot. resolved becomes the host's resolver and must be in charge first (`RESOLVED_NOT_IN_CHARGE` otherwise). With DHCP it then has the offered servers. On an all-static host it has none, so the host stops resolving names until the takeover. To avoid that, put the noted servers in `/etc/systemd/resolved.conf` (`DNS=` and `Domains=` under `[Resolve]`) and run `sudo systemctl restart systemd-resolved`; the assistant then proposes them too. Otherwise enter them in the assistant or declare them as `dns`.
- **Leases are kept:** networkd presents dhcpcd's DHCP identity (its DUID and an IAID from the MAC), so the DHCP server hands out the same IPv4 lease and DHCPv6 address. That needs dhcpcd running for each DHCP interface with its default identity settings (`DHCP_IDENTITY_UNKNOWN` otherwise). The review shows the identity lines.
- **Every interface with an `iface` stanza must be declared** (`WOULD_UNCONFIGURE` otherwise): confirming masks ifupdown for all of them.
- **DNS:** with DHCP or `inet6 auto` the host keeps the offered servers. On an all-static host declare `dns` on a segment (`DNS_REQUIRED` lists the servers in use now).
- **Activation** stops dhcpcd without dropping its addresses, starts networkd and applies the files. **A rollback, or a failed activation, hands the interfaces back to ifupdown** through `ifup@<nic>.service` or `networking.service`, and is recorded only once each interface that had an IPv4 address before has one again (not necessarily the same one; DHCP normally hands back the same lease); until then the timer retries it.
- **Confirming** enables `systemd-networkd`, disables `systemd-networkd-wait-online` and masks `networking.service` and `ifup@.service`. `/etc/network/interfaces` is left as it was and ifupdown stays installed. If no generation is usable at boot, boot removes the masks and starts ifupdown again.
- **Limits:** SLAAC addresses dhcpcd made change at the takeover (dhcpcd derives private ones, networkd EUI-64 ones), so an IPv6 session on one is cut; the kernel's own SLAAC address on an interface without an `inet6` stanza is kept; scripts in `if-up.d` and `if-down.d` stop running after confirmation; a rollback can cut IPv4 for several seconds while ifupdown asks for the lease again. The review shows these as `TAKEOVER_LIMIT` warnings.

To undo a confirmed ifupdown takeover by hand, from the console:

```sh
sudo systemctl disable --now andaru-engine.service andaru-network-boot.service
sudo systemctl unmask networking.service ifup@.service
sudo systemctl disable systemd-networkd.service
sudo systemctl enable systemd-networkd-wait-online.service
sudo rm -r /var/lib/andaru/generations
sudo reboot
```

### How it is built

The engine keeps its sandbox and never writes outside its state directory. It stores each generation under `/var/lib/andaru/generations/<n>/` and asks systemd to run `andaru realize activate|rollback|confirm N` in a transient unit; the timer runs `andaru realize rollback N deadline` the same way. `pointers.json` in that directory is the single commit point, and `realize` re-reads it under a lock before acting, so a timer that fires after a confirmation does nothing. Because the engine can ask systemd to run commands, it is root-equivalent; the sandbox is hardening, not a boundary.

These behaviours are proven on the Debian lab VM, not in CI: see [the lab qualification](#network-takeover-debian-lab-qualification).

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

### Network-preview Debian lab qualification

On 2026-11-10 the CGO-disabled amd64 build of code commit `98d163088e90ad1d7f2bb35334f0d46bf781c0bd` (`vcs.modified=false`, SHA-256 `100144bd45741b98ffd03d673ee2e408f5624267f2860d4b4edfff5ff067f7bc`) replaced the previous lab binary (`e7d3a7d2…`, kept beside the staged artifacts) on VM 114. Snapshot `pre-ppp-20261110-185944` was taken first. Because Debian's cloud kernel has no PPP support, the VM now boots `linux-image-amd64` (6.12.116); `ppp` 2.5.2 is installed.

Executed as root on that VM:

- All four tagged suites passed: store, engine (against the installed-style binary), CLI and render.
- **pppd, no link:** in a private network namespace with a dummy parent interface, `pppd file <rendered> logfd 1 dryrun` accepted the rendered options for PPPoE over a VLAN and directly on a NIC named `enx-1.2`, and reported back the interface name, MTU/MRU and a user name containing quotes, a backslash, `#`, spaces and a newline. The password printed as `??????`. An appended unknown option made it exit 2.
- **systemd:** `systemd-analyze verify` accepted each rendered unit with no output, the `BindsTo=` device name equalled `systemd-escape --path`, and an unknown key produced a warning.
- **networkd:** a private `systemd-networkd` in its own network and mount namespaces logged `Successfully loaded.` for every rendered `.netdev` and `.network` of all seven fixtures with no parse complaint, and reported an injected unknown key. Host links were identical before and after.
- **Installed path:** a synthetic topology was saved through the CLI: the VM's real `eth0` MAC as a 1508-byte WAN NIC, VLAN 6, PPPoE at 1500 with `kpn@internet`, and a LAN NIC with an absent MAC. `andaru ctl preview network` resolved the WAN NIC to `eth0`; rendered its `.network`, the VLAN `.netdev`/`.network`, the pppd options (password shown as `<redacted>`), the service unit and the PPP `.network`; reported `FOREIGN_CONFIGURATION` for both netplan files, `HARDWARE_MISSING` for the LAN NIC and `DEPENDENCY_UNAVAILABLE` for what depends on it; and raised neither `TOOL_MISSING` nor `KERNEL_SUPPORT_MISSING`. A UID 65534 caller got exit 1 and no output.
- The stateless nft ruleset, both forwarding sysctls, interface addresses, `/run/systemd/network` and the absence of `/run/andaru/ppp` and of any Andaru unit were identical before and after. The synthetic objects were deleted, leaving saved version 9 with an empty topology; two unused test secrets remain under the retention policy.

After the adversarial review, code commit `1ec0a3aa91a29fe34cba5ee6569049544abb7acf` (`vcs.modified=false`, SHA-256 `909f9822d6e1b809f14d4f775c02ec517ca5211853647608d9df50312566daa2`) replaced that binary. The tagged engine and render suites passed again on the VM, including the private networkd load of the changed SLAAC output, and the installed CLI previewed the empty saved topology with only the two netplan findings. After the bot review, code commit `5ebcebbe9f828b23f384a357db9d8755f7e4a900` (SHA-256 `6791d4927bec7fcfe984edc0413b2cc522c9b702915cdef57deea033bfda1b24`) replaced it in turn; the tagged CLI and engine suites passed on the VM and `--saved-version ""` exited 2.

The three tool proofs run on this lab, **not in CI**: the hosted runners are Ubuntu 24.04, whose pppd and systemd are not the Debian 13 versions these files target. CI runs the golden-file, findings, redaction, API and CLI tests on amd64 and arm64. Linux arm64 was cross-built, not run on Debian. No PPPoE discovery packet was sent and no session was established; a real line remains the owner's test. The engine is active and still not boot-enabled.

### Network-takeover Debian lab qualification

On 2026-11-11 the CGO-disabled amd64 build of code commit `30d9c88` (SHA-256 `563eb8b4ddca49ac2bdc1ee77d01d6087485cac7c7829b378941e8ee7f7961ae`) was installed on VM 114 with both units enabled, behind snapshot `pre-takeover-20261111-063101`. The VM's NIC was saved as a DHCPv4 + SLAAC Ethernet uplink. A 5-per-second ping ran from another host during each change; confirmations were sent over a new SSH connection.

| Step | Result |
| --- | --- |
| Empty configuration with `--takeover` | Refused: `WOULD_UNCONFIGURE` for `eth0`. |
| Takeover, not confirmed (30 s) | Live in about 1 second. The timer restored both netplan files with their owner, group and mode. 0 of 650 pings lost. |
| Takeover, reboot before the deadline | Back on netplan; the apply reads `rolled_back`, reason `reboot`; `/etc/netplan` untouched. |
| Takeover, confirmed | 0 of 100 pings lost. `/etc/netplan` empty, cloud-init network configuration disabled, networkd enabled, DNS resolving, no timer left. |
| Reboot | Same address on `ens18`, bound to Andaru's file, no netplan file in `/run`. The boot unit finished before networkd started. Boot took 5.8 s. `systemd-analyze verify` printed nothing for the boot unit. |
| `cloud-init clean`, reboot | `/etc/netplan` stayed empty; same result. (cloud-init regenerated the SSH host keys, as it does on a first boot.) |
| Add VLAN 99 with `192.0.2.1/24`, early `applies rollback` | Rolled back while the timer was armed; the VLAN device was deleted. |
| Same apply, engine killed, not confirmed | The timer rolled back with the engine stopped; device deleted. 0 of 600 pings lost across both. |
| Same apply confirmed, reboot | VLAN present with its address; `lastKnownGood` is the takeover generation. |
| Confirmed generation corrupted, reboot | Boot placed last-known-good and logged the fallback; `live` differs from `confirmed`. |
| Changed VLAN id; PPPoE link declared; stale `--saved-version` | `NETDEV_CHANGE_UNSUPPORTED`; `APPLY_UNSUPPORTED`; `412 PRECONDITION_FAILED`. |

**Found on the lab and fixed:** `systemd-networkd-wait-online` is enabled on the cloud image independently of netplan. With no interface required for "online" it ran to its 120-second timeout on every boot and delayed SSH by two minutes. A confirmed takeover now disables it.

Also observed: `systemd-run --wait` returns the command's exit status, and the timer's unit and the on-demand rollback unit coexist.

Not run on the lab: an apply without `--takeover` while netplan is still in place (unit-tested as `TAKEOVER_MISMATCH`), a host with `/var` on its own filesystem, and any baseline other than this cloud image. No PPPoE session and no forwarding were involved.

After the adversarial review, code commit `da58646` (SHA-256 `ba401579ed371c5dccddf23dd8011c18c8a93b2dfb4f024f51eb7c495bea5a3b`) replaced that binary. On the VM an apply went live with its timer's service carrying `Restart=on-failure` and `RestartSec=10s`, and was confirmed from a new session; the reboot and rollback steps above were not repeated with this build.

Afterwards the VM was shut down, rolled back to the pre-takeover snapshot and snapshotted again while stopped as `base-netplan-20261111` (disk only), so the next qualification starts from the netplan baseline. Lab rule: snapshot a stopped VM at its base state, and shut down, roll back and start to reset it when testing is done.

After the bot reviews, code commit `7e4b94f` (SHA-256 `06a3ad873a3eef6bcabc2a08dc921daeef414b7acb9eef56f9a45195cd18d4d2`) was installed on the VM reset to `base-netplan-20261111`. A takeover with a 30-second timeout stayed live when its rollback was run by hand before the deadline (the command exited non-zero), and the timer rolled it back afterwards. A second takeover was confirmed from a new session, `pointers.json` recorded `"takeover":true`, and the VM rebooted in 6.3 s onto Andaru's file with DNS resolving. The VM was then reset to the base snapshot again.

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

### Takeover assistant Debian lab qualification

On 2026-11-11 CGO-disabled amd64 builds of this branch were installed on VM 114 at snapshot `base-netplan-static-20261111` (static 10.0.1.19/16, gateway, two DNS servers, a search domain), reset to that snapshot between runs. A 5-per-second ping ran during each takeover.

| Step | Result |
| --- | --- |
| `andaru ctl takeover` over `ssh -t` (80×24), every detected value accepted, **Apply** | 0 pings lost. Review showed the exact `.network` file; confirmed from a new SSH session. After a reboot: same address on `ens18`, the declared DNS servers, a name resolved, `/etc/netplan` empty. |
| Same, **Quit** at review | Exit 1, "Nothing was changed"; no save, no apply. |
| On the serial console (`ttyS0`, which reports a 0×0 size, `TERM=vt100`), **Apply**, not confirmed | 0 pings lost; the timer rolled back after 120 seconds; the next run reported `saved-not-applied` and changed nothing. |
| A second session saved a zone while the assistant waited at review, then **Apply** | "saved meanwhile from another session"; exit 1, nothing applied, the other session's save kept. |
| Proxmox cloud-init switched to DHCP (netplan `dhcp4`/`dhcp6`), `andaru ctl takeover --accept-detected` | 0 pings lost; IPv4 DHCP and DHCPv6 rendered with the offered DNS; confirmed and rebooted: same addresses on `ens18`, a name resolved. |
| The final build of the branch (SHA-256 `be0c457e475fc6dc52b62f27b6e5cc77e4e1130d70cd08170cdcbb7653c91218`), `--accept-detected --confirm-timeout 30` on the static base, not confirmed | Applied; the timer rolled it back and `live` returned to `null`. |

Two defects found on the lab were fixed with tests before this record: the review counted the preview's `FOREIGN_CONFIGURATION` findings for the netplan files a takeover replaces, so **Apply** was never offered, and on the serial console's 0×0 size the forms had no room to draw.

### ifupdown takeover Debian lab qualification

On 2026-11-11 CGO-disabled amd64 builds of this branch were installed on VM 117: Debian 13 converted to the installer's layout (ifupdown 0.8.44, dhcpcd 10.1, `allow-hotplug ens18`, `inet dhcp`, `inet6 auto`). A 5-per-second ping ran from another host during each takeover. The DHCPv6 address is `2a02:a466:79a7:beef::11e3/128`.

| Step | Result |
| --- | --- |
| Snapshot `base-ifupdown-dhcp-20261111`, `andaru ctl takeover --accept-detected` | Refused with `RESOLVED_NOT_IN_CHARGE` only. |
| `apt install systemd-resolved` on a host where it was never installed, then a reboot; snapshotted while stopped as `base-ifupdown-resolved-20261112` | resolv.conf became resolved's stub and resolved got the DHCP-provided server and domain only after the reboot. |
| `andaru ctl takeover` over `ssh -t` (80×24), every detected value accepted, not confirmed | The review showed the identity lines and both `TAKEOVER_LIMIT` warnings. 0 of 600 pings lost; same IPv4 lease, the DHCPv6 address back within seconds, names resolved. The timer handed back to ifupdown: networkd stopped, dhcpcd running, same IPv4 and DHCPv6 addresses. |
| The same, applied with a 30-second timeout and pinged across the rollback | 1 of 578 pings lost at the hand-back (0.2 s). The probe without `KeepConfiguration=dynamic-on-stop` had lost IPv4 for 7.4 s. |
| Applied, then rebooted before confirming | Came up on ifupdown with the same addresses; the generation reads `rolled_back` (`reboot`). |
| From the resolved base, `andaru ctl takeover` on the serial console (`ttyS0`), confirmed from an SSH session, rebooted | Same IPv4 and DHCPv6 addresses on networkd, `networking.service` and `ifup@ens18.service` masked, names resolved, boot in 6.1 s. A later apply adding a VLAN with a static address was confirmed and kept `ens18`'s address. |
| `systemd-networkd` masked, `--accept-detected` | Activation failed; the host was handed back to ifupdown (dhcpcd running, same addresses, no Andaru file) after a 2.8 s IPv4 gap, and the generation reads `failed`. |
| The installer's static stanza (`10.0.1.13/16`, gateway `10.0.0.1`, no `inet6`), resolved installed afterwards; snapshotted as `base-ifupdown-static-20261112` | resolved has no servers on such a host. `--accept-detected` is refused with `DNS_REQUIRED` ("uses these now: none"). |
| `andaru ctl takeover` on that host, "these servers" entered in the assistant, confirmed | Same address and default route, the entered servers and search domain in use, names resolved, 0 of 200 pings lost. The first run dropped the SLAAC address the kernel had taken without an `inet6` stanza; the reader now detects it as SLAAC and the rerun kept it. |

The VM was left at `base-ifupdown-static-20261112`, its newest snapshot. On this ZFS-backed VM only the newest snapshot can be rolled back to, so the DHCP and resolved bases are now reachable only by cloning.
