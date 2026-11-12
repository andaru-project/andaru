# DHCP and DNS for the LAN

## Status and intent

Requested in conversation: the MVP's first LAN must work for a client ([issue #1](https://github.com/andaru-project/andaru/issues/1), journey step 6 and the acceptance line "a LAN client obtains the expected DHCP lease, resolves DNS through the configured service, and reaches the documented external test endpoint"). This is [issue #10](https://github.com/andaru-project/andaru/issues/10). Owner decisions in conversation, 2026-11-12:

- **Daemons:** Kea for DHCPv4, Unbound for DNS, as already directed on #10. pfBlockerNG will become a first-class component bundled with every installation and sits in front of Unbound later; Andaru is not married to Unbound, so the API and the UI describe DNS without naming its backend.
- **The firewall resolves through it:** the host's own lookups go to the same DNS service the LAN uses.
- **One slice, one PR:** DHCPv4 and DNS land together.
- **Leases in DNS:** DHCP clients' hostnames are published in the DNS service, the way pfSense does it with Kea and Unbound, with Andaru doing the work: Kea only signals that leases changed; the engine reads the leases from Kea and rewrites the published records.

Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

### Authorities

- [Issue #10](https://github.com/andaru-project/andaru/issues/10): Kea and Unbound, host-native; configuration generated from the confirmed declaration; neither writes packet-filter rules; upstreams `do53` and `dot` (`doh` and `doq` reserved).
- [Issue #6](https://github.com/andaru-project/andaru/issues/6), "Routes, DHCP, DNS": `dhcp` per served segment (ranges, lease time, static leases, DNS servers handed out) and a backend-neutral `dns` (mode, upstreams, listen segments, local overrides).
- [Issue #5](https://github.com/andaru-project/andaru/issues/5): stock Debian units for Kea and Unbound; no containers on this path.
- [Issue #4](https://github.com/andaru-project/andaru/issues/4): configuration rendered into `/run` each boot; runtime state a service needs persists, configuration does not.
- [Static host DNS](2026-11-11-static-host-dns-design.md): `addressing.dns` on one segment is today the host's own resolver list, with the stated shortcut "revisit when the DNS area owns the resolver". This slice is that revisit.
- [Packet filter](2026-11-12-packet-filter-design.md): the filter, its fixed rules, starter rules, and how a generation carries non-file entries.
- [Network takeover and confirmed apply](2026-11-11-network-takeover-apply-design.md) and [interactive takeover](2026-11-11-interactive-takeover-design.md): generations, timer, confirmation, boot, the assistant.

### Research and probes behind it

- [`docs/research/dhcp-dns-candidates.md`](https://github.com/andaru-project/andaru/blob/research/dhcp-dns-candidates/docs/research/dhcp-dns-candidates.md) (#9).
- pfSense's mechanism (read from `pfsense/pfsense` master, 2026-11-12): Kea's `libdhcp_run_script.so` runs `kea2unbound` on lease events; it fetches every lease over Kea's control socket (`lease4-get-all`), keeps those in state 0 with a hostname, writes A and PTR `local-data` lines into an Unbound include file, and reloads Unbound when the record set changed. It uses lease events (`lease4_add`, `lease4_update`, `lease4_delete`, `hook_load`) that exist only in Netgate's patched Kea, and forks a PHP process per event, which pfSense users report as CPU load and control-socket timeouts. It publishes duplicate names side by side.
- Probed in a Debian 13 container, 2026-11-12, with `kea-dhcp4-server` 2.6.3-1+deb13u1 and `unbound` 1.26.2-0+deb13u1:
  - `kea-common` ships `libdhcp_run_script.so` and `libdhcp_lease_cmds.so` under `/usr/lib/x86_64-linux-gnu/kea/hooks/`.
  - A real lease (busybox `udhcpc -x hostname:laptop-1` over a veth pair) ran the hook script on `leases4_committed` with `LEASES4_AT0_HOSTNAME=laptop-1` and `LEASES4_AT0_ADDRESS=192.168.50.100`, with DDNS off.
  - Kea refuses a control socket whose directory is not `/run/kea` with mode 0750 (`socket path:/run/kea does not exist or does not have permssions = 750`). It accepted a hook script and a lease file outside its own directories.
  - Both packages enable their unit on install (`kea-dhcp4-server.service`, `unbound.service`). Kea's unit runs as `_kea` with `ExecStart=/usr/sbin/kea-dhcp4 -c /etc/kea/kea-dhcp4.conf`, `RuntimeDirectory=kea`, `StateDirectory=kea`. Unbound's reads `/etc/unbound/unbound.conf`, which includes `/etc/unbound/unbound.conf.d/*.conf`; its control socket is `/run/unbound.ctl`.
  - `unbound-control` offers `reload`, `reload_keep_cache` and `fast_reload` (documented as experimental).
  - An Unbound `include:` glob over a directory that does not exist is an error; over an existing empty directory it is not.

## Scope

| In this slice | Later |
| --- | --- |
| DHCPv4 on segments with a static IPv4 address: ranges, lease time, static leases, options for router, DNS and domain | DHCPv6, router advertisements, prefix delegation to the LAN (#7) |
| A DNS service in `recursive` or `forward` mode, upstreams `do53` and `dot` | `doh`, `doq`, the pfBlockerNG front layer, per-client policy, domain overrides, local host overrides |
| The firewall itself resolving through the DNS service | Split-horizon views |
| Published names for DHCP leases and static leases | Lease names from DHCPv6, registration toggles per segment |
| Assistant: a DNS service and DHCP on the LAN in the takeover proposal | A leases status page (#15) |

## Declaration

Both additions are optional top-level sections; a declaration without them realizes exactly as today. `schemaVersion` stays 1.

### `dns` (single object)

| Field | Meaning |
| --- | --- |
| `mode` | `recursive` (asks the root servers itself) or `forward` (asks `upstreams`). |
| `upstreams` | Required and non-empty in `forward`, absent in `recursive`. Ordered list of `{address, protocol, tlsName}`: `protocol` `do53` or `dot`; `tlsName` is the name the upstream's certificate must carry and is required for `dot`; `address` is an IPv4 or IPv6 address, optionally with a port. `doh` and `doq` are reserved values the schema refuses for now. |
| `segments` | Segment IDs whose clients it serves. It listens on each one's static IPv4 address; at least one. The firewall's loopback is always served. |
| `localDomain` | The domain local names live in: lease names, static lease names, and what DHCP hands out as domain name. Default `home.arpa` (RFC 8375). |
| `searchDomains` | The firewall's own search list, as `addressing.dns.searchDomains` was. Optional. |
| `registerLeases` | Publish DHCP clients' hostnames. Default `true`. |

Validation, as `VALIDATION_FAILED` field errors at the field's own path:

- Each listed segment exists and has a static IPv4 address.
- `localDomain` and `searchDomains` entries are DNS names.
- **While `dns` is declared, no segment may declare `addressing.dns`.** The host's servers then live in one place, `dns.upstreams`; the error names the move. Without `dns`, `addressing.dns` keeps its current meaning.

### `dhcp` (collection, prefix `dhp`)

One entry per served segment.

| Field | Meaning |
| --- | --- |
| `segment` | The served segment; unique across entries. It must have a static IPv4 address. |
| `ranges` | At least one `{first, last}`, IPv4, inside the segment's subnet, `first ≤ last`, not containing the segment's own address, not overlapping each other. |
| `leaseTime` | Seconds, 300–604800. Default 7200. |
| `dnsServers` | Optional. When absent: the segment's own address if `dns` serves the segment, otherwise the firewall hands out no DNS server and the draft warns (`DHCP_WITHOUT_DNS`). |
| `staticLeases` | `{mac, address, hostname}`: unique MAC and unique address within the entry; the address inside the subnet and not the segment's own; `hostname` optional, a DNS label. |

The router option is always the segment's own address. The domain name option is `dns.localDomain` when `dns` is declared.

Deleting a segment referenced by `dhcp` or `dns.segments` is refused, as for every reference.

## Realization

Everything below belongs to the generation, exactly as the network files and the filter do: rendered, previewed, applied under the timer, confirmed, rolled back and booted together. Per-area apply from #6 is not part of this slice.

### Files Andaru renders

| Path | Content |
| --- | --- |
| `/run/andaru-kea/kea-dhcp4.conf` | Kea's whole configuration: interfaces of the served segments, one `subnet4` per `dhcp` entry with its pools, reservations and options, a memfile lease database at Kea's default `/var/lib/kea/kea-leases4.csv`, the control socket `/run/kea/kea4-ctrl-socket (Debian's name, which its AppArmor profile allows)`, `lease_cmds` and `run_script` hooks. |
| `/run/andaru-unbound/andaru.conf` | Unbound `server:` settings (interfaces, access control for the served segments' subnets and loopback, `local-zone` for `localDomain`, static lease records) and either recursion or a `forward-zone` for `.` with the upstreams (`forward-tls-upstream` for `dot`, verified against `/etc/ssl/certs/ca-certificates.crt`). |
| `/run/systemd/resolved.conf.d/50-andaru.conf` | `DNS=127.0.0.1` and `::1`, `Domains=` with `searchDomains`, so the firewall resolves through the DNS service. Links stop taking DNS from DHCP (`UseDNS=no`). |

The generation's allowed paths grow by exactly these three. They live outside `/run/andaru`: that directory is the engine's `RuntimeDirectory`, root-only (0700) to protect its socket and removed whenever the engine stops, and Kea runs as `_kea`. `/run/andaru-kea` and `/run/andaru-unbound` are root-owned 0755, their files 0644.

Rendering refuses with `TOOL_MISSING` when `dhcp` is declared without `kea-dhcp4-server` installed, or `dns` without `unbound`. When systemd-resolved is not in charge of `/etc/resolv.conf`, the existing `DNS_NOT_APPLIED` warning applies to the host's own lookups; the LAN is served all the same.

### Files Andaru installs once (packaging, not generations)

They are static, ship next to `andaru-engine.service`, and never change with the configuration:

- `/etc/systemd/system/kea-dhcp4-server.service.d/andaru.conf`: `ConditionPathExists=/run/andaru-kea/kea-dhcp4.conf`, an `ExecStart=` reading that file, and `ExecStartPost=` running `andaru-lease-event`, so a Kea start (boot included) syncs lease names. Without a `dhcp` section Kea does not start. Debian's `/etc/kea/kea-dhcp4.conf` is left alone.
- `/etc/unbound/unbound.conf.d/andaru.conf`: `include: "/run/andaru-unbound/*.conf"` and `include: "/run/andaru-leases/*.conf"`. Debian's `unbound.conf` is left alone; without a `dns` section Unbound keeps its Debian default (loopback only) and nothing points at it.
- A `tmpfiles.d` entry creating `/run/andaru-kea`, `/run/andaru-unbound` and `/run/andaru-leases` (root, 0755) at boot, because Unbound refuses an include glob over a missing directory. `andaru realize` creates them too when missing.
- `/usr/share/kea/scripts/andaru-lease-event`: the `run_script` target, two lines of shell that update the mtime of `/run/kea/andaru-leases-changed`. It runs as `_kea`, which owns `/run/kea`.
- `andaru-leases.path` (`PathModified=/run/kea/andaru-leases-changed`) and `andaru-leases.service`, a root oneshot running `andaru realize leases`: the sync below. It is privileged work, so it runs outside the engine, as every host change does: the engine has no capabilities, cannot enter `/run/kea`, and never writes outside its state directory.

### Apply, rollback, boot

- **Order.** Filter first (as today), then the networkd files and their reload, then Kea and Unbound, then the resolver drop-in: the daemons bind addresses that exist only after networkd, and the host switches to the DNS service only once it runs.
- **Kea and Unbound pick up a new generation** by `systemctl restart` (Kea keeps its leases in its lease file) and `unbound-control reload_keep_cache`; Unbound is restarted when it is not running. A generation without `dhcp` stops Kea; one without `dns` removes Andaru's Unbound file and reloads, leaving Debian's default. resolved is reloaded when its drop-in appears, changes or goes.
- **Validation before activation:** `kea-dhcp4 -t` and `unbound-checkconf` on the rendered files run during render's checks on the engine; a refusal is an error finding, so an invalid configuration never activates.
- **Verification after activation:** the existing verification gains: Kea answers `status-get` on its socket and Unbound answers `unbound-control status`. A failure fails the activation, which restores the previous generation, as for networkd.
- **Rollback and the baseline** follow the same steps in reverse. Rolling back to the pre-Andaru baseline removes all three files, stops Kea, reloads Unbound and resolved.
- **Boot.** `andaru realize boot` places the files with the rest of the generation before networkd; Kea and Unbound start later through their own units, which read them. Kea retries binding until the interfaces exist (`service-sockets-max-retries`).

### Firewall

When the filter is on, the input chain gains one fixed rule per served segment: DHCP requests (`udp sport 68 dport 67`) from that segment's interface are accepted. DNS stays governed by rules: the starter rule already accepts DNS from the LAN to the firewall. Loopback is accepted already, so the firewall's own lookups need nothing.

### Lease names

- **Trigger.** Kea's `run_script` hook (`sync: false`) calls `andaru-lease-event` on `leases4_committed`, `lease4_release`, `lease4_expire` and `lease4_decline`. The script only marks that leases changed; it starts no Andaru process. This avoids pfSense's process per event.
- **Sync.** `andaru-leases.path` starts `andaru realize leases` when the marker changes. It waits one second for more changes, then:
  1. asks Kea for every lease (`lease4-get-all` over `/run/kea/kea4-ctrl-socket`);
  2. keeps leases in state 0 (assigned) with a hostname, in subnets whose segment `dns` serves;
  3. builds `<first label of hostname>.<localDomain>` with an A record and a PTR record each, TTL a third of the lease's valid lifetime (as RFC 4702 suggests and pfSense does);
  4. writes `/run/andaru-leases/leases.conf` atomically when the record set changed, and runs `unbound-control reload_keep_cache`;
  5. runs again if the marker changed while it worked, since a path unit does not trigger an active service.
- Activation, rollback and boot touch the marker after Kea and Unbound pick up a generation, and Kea's start does through its drop-in. An Unbound restart needs nothing: the leases file is in `/run` and Unbound reads it again. The file is runtime state, not part of a generation: a rollback keeps it until the next sync rewrites it.
- **Untrusted names.** Hostnames come from clients. Only a first label of 1–63 characters of letters, digits and inner hyphens is published; anything else is skipped and logged at debug.
- **Collisions.** Static lease names, which come from the declaration, win over lease names. Between two leases with the same name, the most recently renewed (`cltt`) wins. pfSense publishes both; that answers a lookup with an address that may belong to another device.
- With `registerLeases` false, the file is written empty.

## Takeover assistant

The takeover proposal gains, when the host has a LAN segment with a static IPv4 address:

- `dns`: `forward` with the host's current DNS servers as `do53` upstreams when it has declared ones; otherwise `recursive`, because servers a DHCP uplink hands out change and Unbound cannot follow them. `segments` are the LAN segments; `searchDomains` as detected. The segment's `addressing.dns` moves into this object instead of being proposed on the segment.
- A proposed `dhcp` entry for each LAN segment with a static IPv4 address: one range covering the upper half of the subnet, without the segment's own address and the broadcast address (192.168.40.1/24 gets 192.168.40.128–192.168.40.254), lease time default. It travels beside the change batch, like the starter rules, and is **not** in the batch itself: `--accept-detected` never turns on a DHCP server, because the LAN may already have one.
- The assistant asks one yes/no question per such segment, "Serve DHCP on <segment> (<range>)?", defaulting to yes; a yes adds the entry to the batch. The review shows the DHCP ranges and the DNS mode. Editing ranges, and turning DHCP on after an `--accept-detected` takeover, is done with `andaru ctl`.

Without a LAN segment with a static address the proposal is as today.

## API and CLI

- `GET/POST /api/v1/config/dhcp`, `GET/PUT/DELETE /api/v1/config/dhcp/{id}`, and the singleton `GET/PUT/DELETE /api/v1/config/dns` (`If-Match: "dns.N"`), following the existing object and singleton routes, OpenAPI and schema.
- `andaru ctl config` gains the `dhcp` collection and the `dns` singleton.
- No status route in this slice: leases are inspected with Kea's own socket or the leases file until #15.

## Acceptance

### Automated

- Validation: every rule above, including the `addressing.dns` move and ranges outside the subnet.
- Render goldens for `dhcp` and `dns` (recursive, forward with `do53` and `dot`), and `kea-dhcp4 -t` and `unbound-checkconf` accepting every golden when the tools are present (root, as the nft goldens).
- Generation: order of activation, rollback to a generation without `dhcp`, boot placing the files, verification failure restoring the previous generation.
- Lease sync: a fake Kea socket and marker file; names, PTRs, TTLs, untrusted hostnames, collisions, debounce, resync after an Unbound restart.

### Lab (Debian 13 VM with a LAN client, as for the packet filter)

1. Takeover with the assistant on the two-NIC VM, answering yes to DHCP: the client gets an address in the proposed range, the router and DNS options point at the firewall, and the firewall's `resolvectl` shows `127.0.0.1`.
2. The client resolves an Internet name through the firewall and reaches the test endpoint; the firewall resolves too.
3. The client's hostname resolves from the firewall and from the client as `<name>.home.arpa`, forward and reverse; releasing the lease removes it.
4. A static lease gets its address and its name.
5. `forward` mode with a `dot` upstream resolves; a wrong `tlsName` fails resolution, not the apply.
6. An unconfirmed apply that removes `dhcp` comes back with Kea serving and the client keeping its lease; a reboot brings back DHCP, DNS and published lease names.

## Approaches

- **Leases into DNS.**
  1. *Chosen:* Kea's `run_script` hook marks a change; the engine reads every lease from Kea's control socket and rewrites an Unbound include file. pfSense's design without its patched hooks and per-event processes.
  2. The engine reads Kea's lease file directly. No hook, but the memfile is an append log that Kea compacts, so the engine would reimplement its semantics.
  3. Kea's DDNS (RFC 2136). Unbound accepts no dynamic updates, so it would need a second authoritative server.
- **Delivery.** One PR for DHCP and DNS (chosen) over a DNS PR followed by a DHCP PR, so the client path lands at once.
- **Daemon configuration.** Static packaged drop-ins pointing at `/run` (chosen) over replacing Debian's conffiles or diverting them: nothing of Debian's is edited and Kea does not start without Andaru's file.

## Calls made in writing this (owner may correct)

Owner ruling, 2026-11-12: DHCP is never part of `--accept-detected`; the assistant asks per LAN segment, defaulting to yes.
Owner ruling, 2026-11-12, on approving this design: DHCPv6 and router advertisements are the next slice.

- Lease sync runs as a privileged oneshot started by a systemd path unit, like every other host change, not inside the unprivileged engine.
- Rendered files go to root-owned 0755 directories beside `/run/andaru`, not inside it.
- No name for the firewall itself in this slice: the declaration has no hostname yet.
- systemd-resolved not in charge stays a warning for the host's lookups; the LAN is still served.

- `recursive` for hosts whose DNS came from DHCP at takeover, rather than following DHCP-provided servers.
- Lease files stay at Kea's default `/var/lib/kea` rather than Andaru's storage as #4 prefers: it is Debian's state directory for that unit, and Kea 3.0 restricts lease files to it.
- Collision rule: declaration first, then the newest lease.
- `home.arpa` as the default local domain.
- The upper half of the subnet as the proposed range; range editing via the CLI.
- DHCP accepted by a fixed input rule; DNS left to rules.
