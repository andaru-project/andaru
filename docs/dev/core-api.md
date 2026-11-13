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
| `GET/POST /api/v1/config/{addresses\|services}`, `…/{id}` | Address and service objects for the packet filter |
| `GET/POST /api/v1/config/policy/{input\|forward}` | A rule list in execution order with its `version`; `POST ?before=<rule id>` places a new rule |
| `GET/PUT/DELETE /api/v1/config/policy/{chain}/{id}` | One rule, version-checked like any object |
| `PUT /api/v1/config/policy/{chain}/order` | Reorder a list: every rule ID once, `If-Match: "order.N"`; replies with the new `order` and its `version` |
| `GET/POST /api/v1/config/nat/{port-forwards\|one-to-one}` | An inbound NAT list in evaluation order with its `version`; `POST ?before=<entry id>` places a new entry |
| `GET/PUT/DELETE /api/v1/config/nat/{list}/{id}` | One NAT entry, version-checked like any object |
| `PUT /api/v1/config/nat/{list}/order` | Reorder a NAT list, as for the policy lists |
| `GET/PUT/DELETE /api/v1/config/management`, `/api/v1/config/nat/outbound` | Single objects, `If-Match: "management.N"` / `"nat.outbound.N"`, `0` while unset |
| `GET/POST /api/v1/config/dhcp`, `GET/PUT/DELETE /api/v1/config/dhcp/{id}` | DHCP servers, one per served segment |
| `GET/PUT/DELETE /api/v1/config/dns` | The DNS service, `If-Match: "dns.N"`, `0` while unset |

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
| `PROXY_ARP_REQUIRED` | warning | A 1:1 external address is on an uplink's subnet but not assigned to the firewall, so nothing answers ARP for it. Add it to the segment's addresses; proxy ARP comes with virtual IPs (#37). |
| `ONE_TO_ONE_OWN_ADDRESS` | error | A 1:1 external range holds an address the firewall learned by DHCP or PPPoE; 1:1 NAT would take over its traffic. Checked at preview and apply; boot loads the confirmed table as it was. |
| `RA_DNS_UNAVAILABLE` | warning | The DNS service serves a delegated segment that has no static IPv6 address, so its advertisements carry no DNS server. |
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
| `MANAGEMENT_REQUIRED` | Rules or outbound NAT are declared without `management.segment`, or the management segment's interface is unavailable. |
| `DHCP_WITHOUT_DNS` | (warning) A DHCP server hands out no DNS server: the DNS service does not serve its segment and `dnsServers` is empty. |
| `FOREIGN_FILTER` | `nftables.service`, `firewalld` or `ufw` is enabled and would replace or override Andaru's filter. |
| `APPLY_UNSUPPORTED` | PPPoE links wait for the firewall; router advertisement modes `managed`, `assisted` and `stateless` wait for the DHCPv6 server; advertisements and delegated prefixes need the packet filter, so a management segment, since IPv6 forwarding stays off without it. |
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

### Packet filter

Andaru filters with its own nftables table, `inet andaru`, applied, confirmed and rolled back together with the network files of the same generation. Design and limits: [packet filter and outbound NAT](../superpowers/specs/2026-11-12-packet-filter-design.md).

```sh
sudo andaru ctl config list policy/forward              # rules in execution order, and the list's version
printf '%s\n' '{"name":"web","protocol":"tcp","ports":["80","443"]}' | sudo andaru ctl config create services
sudo andaru ctl config order forward --version 12 "$RULE_B" "$RULE_A"
sudo andaru ctl preview network                          # shows "nft:inet/andaru" beside the networkd files
```

- **The filter switches on with `management.segment`.** While it is unset there is no table and nothing forwards, as before; rules or outbound NAT without it are refused with `MANAGEMENT_REQUIRED`. SSH (22/tcp) is always accepted on the management segment, from `allowedSources` when set; no rule can remove that.
- **Objects:** addresses are hosts, networks, ranges or one-level groups of those; services are tcp/udp ports and ranges, icmp/icmpv6 types, or groups.
- **Rules:** `policy.input` (to the firewall) and `policy.forward` (through it), tried in list order: `accept`, `drop` or `reject`, `from`/`to` zones or addresses (empty means any), `services`, `log` (to the kernel log). A rule matches IPv4 and IPv6 alike unless its addresses are of one family. Reordering is its own version-checked operation, so two sessions cannot reorder past each other.
- **Fixed rules, before every rule:** established and related accepted, invalid dropped, loopback and ICMPv6 neighbour discovery accepted, DHCPv6 replies accepted; everything unmatched is dropped. The firewall's own outgoing traffic is not filtered.
- **Outbound NAT** `automatic` masquerades IPv4 forwarded out of every segment with a default route (a static gateway, DHCP or PPPoE). IPv6 is routed, not translated.
- **Applying:** the filter is loaded before the networkd files, and when going back to a generation without one, or to the pre-Andaru baseline, the table is deleted only after the files, with IPv4 and IPv6 forwarding switched off just before it. Boot loads the confirmed filter before networkd starts, with each adapter's name looked up again by its MAC, so a card the kernel names differently after a reboot keeps its rules; a filter that does not load makes boot fall back to the last-known-good generation, files and filter together.
- **Refused:** `nft` missing (`TOOL_MISSING`; install `nftables`), or `nftables.service`, `firewalld` or `ufw` enabled (`FOREIGN_FILTER`): Debian's `/etc/nftables.conf` begins with `flush ruleset`. Other tables, such as Podman's, are never touched.
- **The assistant** proposes starter rules for the LAN (to any; ping and DNS to the firewall), outbound NAT, and the management adapter your SSH session arrives on, and shows the rule table in its review.

### DHCP and DNS

Andaru runs Debian's Kea as the LAN's DHCPv4 server and Unbound as its DNS service; the firewall itself resolves through the same service. Both are configured only from the confirmed generation, applied, confirmed, rolled back and booted with the network and the filter. Design: [DHCP and DNS for the LAN](../superpowers/specs/2026-11-12-dhcp-dns-design.md).

```sh
sudo apt install kea-dhcp4-server unbound
# Once, beside andaru-engine.service (packaging/):
sudo install -m 0644 packaging/systemd/kea-dhcp4-server.service.d/andaru.conf -D /etc/systemd/system/kea-dhcp4-server.service.d/andaru.conf
sudo install -m 0644 packaging/unbound/andaru.conf /etc/unbound/unbound.conf.d/andaru.conf
sudo install -m 0644 packaging/tmpfiles.d/andaru.conf /usr/lib/tmpfiles.d/andaru.conf
sudo install -m 0755 packaging/kea/andaru-lease-event -D /usr/share/kea/scripts/andaru-lease-event
sudo install -m 0644 packaging/systemd/andaru-leases.path packaging/systemd/andaru-leases.service /etc/systemd/system/
# Debian's AppArmor profiles confine both daemons; these local rules let them read Andaru's files.
sudo install -m 0644 packaging/apparmor/usr.sbin.kea-dhcp4 packaging/apparmor/usr.sbin.unbound /etc/apparmor.d/local/
sudo apparmor_parser -r /etc/apparmor.d/usr.sbin.kea-dhcp4 /etc/apparmor.d/usr.sbin.unbound
sudo systemd-tmpfiles --create andaru.conf && sudo systemctl daemon-reload && sudo systemctl enable --now andaru-leases.path
sudo systemctl restart kea-dhcp4-server unbound   # Kea stops until a generation declares DHCP

printf '%s\n' '{"name":"lan","segment":"'"$LAN"'","ranges":[{"first":"192.168.40.128","last":"192.168.40.254"}]}' | sudo andaru ctl config create dhcp
printf '%s\n' '{"mode":"recursive","segments":["'"$LAN"'"]}' | sudo andaru ctl config replace --version 0 dns
```

- **`dhcp`**, one entry per served segment, which needs a static IPv4 address: `ranges`, `leaseTime` (seconds, default 7200), `staticLeases` (`mac`, `address`, optional `hostname`), optional `dnsServers`. Clients get the segment's address as router and, when the DNS service serves the segment, as DNS server; otherwise `DHCP_WITHOUT_DNS` warns.
- **`dns`**: `mode` `recursive` or `forward` with `upstreams` (`do53`, or `dot` with the certificate's `tlsName`; one protocol for all), `segments` it serves (may be empty: the firewall only), `localDomain` (default `home.arpa`), `searchDomains`, `registerLeases` (default true). While it is declared no segment may carry `addressing.dns`: the host's servers are the upstreams, and links stop taking DNS servers from DHCP.
- **Lease names:** a client's hostname is published as `<name>.<localDomain>`, A and PTR, for a third of its lease time. Kea's `run_script` hook only touches `/run/kea/andaru-leases-changed`; `andaru-leases.path` then runs `andaru realize leases`, which reads every lease over Kea's socket and rewrites `/run/andaru-leases/leases.conf`. Only a valid name label is published; static lease names win over lease names, and the most recently renewed lease wins a name.
- **Applying:** after networkd reloads, Kea's and Unbound's own checkers judge the new files; a refusal, or a daemon that does not come up, restores the previous generation. A generation without `dhcp` stops Kea; without `dns`, Unbound falls back to Debian's default and the host's resolver is what it was. Leases stay in Kea's `/var/lib/kea`.
- **Filter:** with the filter on, DHCP requests are accepted on served segments by a fixed rule; DNS is governed by rules (the takeover's starter rule allows it from the LAN).
- **Takeover:** with Unbound installed and a static LAN segment, the proposal includes the DNS service: forwarding to the host's declared servers, or recursive when they came from DHCP. With Kea installed it offers DHCP on each static LAN segment (the upper half of the subnet); the assistant asks for each, yes by default, while `--accept-detected` never turns DHCP on.
- Inspect leases with `sudo cat /run/andaru-leases/leases.conf` or Kea's socket until a status page exists.

### DHCPv6 and prefix delegation

Kea's DHCPv6 server on LAN segments with a static IPv6 `/64`, including prefixes delegated to routers behind them. Design: [DHCPv6 for the LAN](../superpowers/specs/2026-11-13-dhcpv6-design.md).

```sh
sudo apt install kea-dhcp6-server
# Once, beside the DHCP and DNS files (packaging/):
sudo install -m 0644 packaging/systemd/kea-dhcp6-server.service.d/andaru.conf -D /etc/systemd/system/kea-dhcp6-server.service.d/andaru.conf
sudo install -m 0644 packaging/systemd/networkd.conf.d/andaru.conf -D /etc/systemd/networkd.conf.d/andaru.conf
sudo install -m 0644 packaging/iproute2/andaru.conf -D /etc/iproute2/rt_protos.d/andaru.conf
sudo install -m 0755 packaging/kea/andaru-lease-event -D /usr/share/kea/scripts/andaru-lease-event
sudo install -m 0644 packaging/apparmor/usr.sbin.kea-dhcp6 /etc/apparmor.d/local/
sudo apparmor_parser -r /etc/apparmor.d/usr.sbin.kea-dhcp6
sudo systemctl daemon-reload && sudo systemctl restart systemd-networkd

printf '%s\n' '{"name":"lan6","segment":"'"$LAN"'","ranges":[{"first":"fd00:beef:1::1000","last":"fd00:beef:1::1fff"}],"prefixDelegation":{"prefix":"fd00:beef:100::/48","delegatedLength":56}}' \
  | sudo andaru ctl config create dhcp6
```

- **`dhcp6`**, one entry per segment with a static IPv6 `/64`: `ranges`, `leaseTime` (default 7200; the preferred lifetime is five-eighths of it), `dnsServers` (default: the segment's stable address when the DNS service serves it), `staticLeases` by `duid` with an `address`, a `prefix` and a `hostname`, and `prefixDelegation` (`prefix`, `delegatedLength` 48–64, optional `excludedPrefix` inside the first delegated prefix). The search list is the local domain and the DNS service's search domains.
- **Router advertisements:** `managed`, `assisted` and `stateless` apply on a segment with an entry. With other modes `DHCP6_UNANNOUNCED` warns that clients following the flags will not ask.
- **Delegated prefixes are routed via the router that asked for them.** Kea's hook appends the router's link-local address and interface to `/run/kea/andaru-pd-events`; `andaru realize leases` moves that file out of Kea's directory, believes a line only for a served interface, a link-local router and a prefix of that segment's pool, and installs `ip -6 route … proto 77` for each active lease. It removes every protocol 77 route nobody holds. The next hops are kept in `/var/lib/andaru/pd-next-hops.json`, so the routes return at boot. `ManageForeignRoutes=no` stops networkd deleting them on a restart or an apply.
- **The pool is unreachable** where nobody leases it (`[Route] Type=unreachable` on the segment), so it never leaks out of the WAN. A delegation pool needs the packet filter, so a management segment.
- **Names:** DHCPv6 address leases publish AAAA and PTR records. Names are kept per family: a static DHCPv4 name does not block a device's IPv6 name.
- **Not offered:** DHCPv6 relays, NTP and custom options.

### DHCPv6 on delegated prefixes

Kea's DHCPv6 server, and prefix delegation onwards, on segments numbered from the prefix the WAN's DHCPv6 client receives (`delegated` segments). Andaru follows the prefix when the provider changes it, and tells clients to stop using the old one. Design: [DHCPv6 on delegated prefixes](../superpowers/specs/2026-11-13-dhcpv6-delegated-design.md).

```sh
# Once, beside the DHCPv6 files (packaging/):
sudo install -m 0644 packaging/systemd/andaru-prefixes.path packaging/systemd/andaru-prefixes.timer packaging/systemd/andaru-prefixes.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now andaru-prefixes.path andaru-prefixes.timer

# A /48 requested on the WAN; the LAN is subnet 1 of it, and routers behind it get /64s from the 16 at subnet 0x80.
printf '%s\n' '{"name":"lan6","segment":"'"$LAN"'","ranges":[{"first":"::1000","last":"::1fff"}],"staticLeases":[{"duid":"00:01:00:01:aa:bb:cc:dd:ee:ff","prefixId":"1","hostname":"router"}],"prefixDelegation":{"subnetId":"8","length":60,"delegatedLength":64}}' \
  | sudo andaru ctl config create dhcp6
sudo andaru ctl prefixes
```

- **Suffixes and positions:** on a delegated segment, `ranges` and static `address`es are suffixes (the first 64 bits zero), put behind the segment's current `/64`. A `prefixDelegation` is a position in the WAN's prefix: `subnetId` at `length`, handing out prefixes of `delegatedLength`. A static lease's `prefixId` is a position in that pool, so a router keeps its place whatever prefix the provider delegates. Literal prefixes and an excluded prefix are refused there.
- **What fits:** subnet IDs and pools are checked against the WAN's `requestPrefixLength` when saved (`0` to `ff` for a `/56` request), and against the prefix actually received at apply. When the provider delegates less than requested, what does not fit is reported (`DELEGATED_PREFIX_TOO_SHORT`) and not served; the rest is. To fit whichever answer the provider gives, keep subnet IDs and pools inside the first `/56`, for example a `/60` pool of `/64`s at `subnetId` `8`.
- **Following the prefix:** `andaru realize prefixes` runs when networkd's link state changes (`andaru-prefixes.path`), every minute (`andaru-prefixes.timer`), at apply and at boot. It reads the prefix from networkd's `unreachable … proto dhcp` route and the LAN's address, fills the delegated subnets into Kea's file, and hands it to Kea with `config-set` without restarting it. Clients move to the new prefix when they renew; Kea returns their old lease with zero lifetimes. When no prefix is seen (at boot, during an outage), the last one is served until its valid lifetime ends, and the routers' next hops are kept.
- **Deprecating the old prefix:** networkd stops listing a replaced prefix but never advertises it at lifetime zero, so clients keep it preferred. For ten minutes after a change, Andaru advertises each segment's old `/64` with preferred lifetime 0 (and the segment's own router lifetime and flags), so clients stop choosing addresses from it. RFC 4862 keeps it valid on clients for up to two hours.
- **The prefix view:** `andaru ctl prefixes` (`GET /api/v1/status/prefixes`) shows each WAN's requested length, its current prefix and lifetimes, the subnet IDs it leaves room for, each delegated segment's `/64`, each pool's range, and what does not fit.
- **Routes and names:** a delegated pool is unreachable where nobody leases it (`proto 77`, kept by the lease sync), leased prefixes are routed via the router that asked, and static names publish AAAA and PTR at the current address.

### Inbound NAT

Port forwards and 1:1 NAT, with pfSense's semantics, IPv4 only. Both need the packet filter, so a management segment. Design: [inbound NAT](../superpowers/specs/2026-11-12-inbound-nat-design.md).

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
- **Port forwards** are evaluated in list order, first match. `ports` is one port or a `first-last` range; `targetPort` shifts a range by the same offset. `noRedirect` exempts matching connections from the forwards after it. `from` takes address objects, with `fromNegated`. A `target` may be an inside host or one of the firewall's own addresses; `127.0.0.0/8` is refused (Linux drops such translations from other interfaces) — use an address on the loopback interface, as a virtual IP will (#37).
- **Associated rule:** with `filterRule: auto` (the default) each forward gets a rule placed before the policy, in `forward`, or in `input` for the firewall itself. It admits only connections the forward translated, so the inside host is not reachable directly: the forward sets the low 16 bits of the connection mark to its position, and its rule matches that mark. The high 16 bits are left to other software. `none` leaves it to the policy.
- **1:1** maps an address or a prefix in both directions within one zone, optionally only for `destination` peers. `noBinat` exempts hosts from the entries after it; they leave through the outbound masquerade. Write the policy rule that admits the traffic yourself, as in pfSense.
- **1:1 addresses** must already reach the firewall: an extra static address on the WAN segment (never its first, which the firewall's own traffic uses) or a block the provider routes to it. `PROXY_ARP_REQUIRED` warns about an address on the WAN subnet the firewall does not hold.
- **Reflection** (`reflection: true`, Pure NAT) lets internal clients use the outside address; the server then sees the firewall as the client. Split DNS is the alternative that keeps the client's address.

### IPv6 on the LAN

A LAN segment numbers itself from a prefix the WAN's DHCPv6 client receives, or from static addresses, and advertises itself as a router. Design: [IPv6 for the LAN](../superpowers/specs/2026-11-12-ipv6-lan-design.md).

```sh
# WAN: ask for a /56 and no address of its own.
... "ipv6":{"mode":"dhcp6","requestPrefixLength":56,"prefixOnly":true}
# LAN: subnet 0 of the delegated prefix, a stable unique local /64 for DNS, SLAAC for clients.
... "ipv6":{"mode":"delegated","from":"<wan segment>","subnetId":"0","addresses":["fd12:3456:789a:1::1/64"],"ra":{"mode":"unmanaged"}}
```

- **`ra`**: `mode` `disabled`, `router` (no prefix announced), `unmanaged` (SLAAC); `managed`, `assisted`, `stateless` are saved but refused at apply until the DHCPv6 server exists. Options: `priority` (`low`, `medium`, `high`), `routerLifetime` (0 or 4–9000 s, default 1800), `validLifetime`/`preferredLifetime` (static prefixes only, default 86400/14400; a delegated prefix keeps the lease's), `dns` (`servers`, `searchDomains`), `pref64`. Versions saved with the older `{enabled, managed, otherInfo}` still load; writing the segment stores the mode.
- **Advertised DNS:** without `ra.dns`, a segment the DNS service serves advertises its first static IPv6 address and the local and search domains. Give a delegated segment a static unique local address (RFC 4193): the delegated prefix can change, the unique local address does not. Without one, `RA_DNS_UNAVAILABLE` warns.
- **WAN DHCPv6 client:** `requestPrefixLength`, `prefixOnly` (`UseAddress=no`), `withoutRA` (solicit without waiting for an advertisement), `sendRelease` (default false: a release makes many providers hand out a new prefix), `duid` (hex, types 1 to 4 with the length each needs; replaces the captured DHCPv6 identity). A `dhcp6` segment always solicits (`DHCPv6Client=always`), whatever flags the upstream advertisement carries.
- **DNS service:** listens on each served segment's stable IPv6 address. On a static segment it answers that subnet, as for IPv4; on a delegated one, whose prefix is unknown in advance, it answers whatever source reaches it there, and the filter (required for delegated segments) decides who may. A segment may be served by IPv6 alone.
- **Filter:** link-local multicast listener discovery is accepted on every interface. Neighbour discovery was already.
- **Prefix changes:** networkd advertises the new delegated prefix at once but does not deprecate the old one, so clients keep the old address until its preferred lifetime from the lease ends. The unique local address keeps DNS working throughout.
- **Not offered:** advertisement intervals (networkd has none), extra advertised subnets, DHCPv6 server modes (next slice).

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

- Interface names stay as they were (`eth0` on cloud images, where netplan pinned it): Andaru's `.link` files keep them across reboots.
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
