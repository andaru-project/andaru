# DHCPv6 for the LAN: addresses, static leases and prefix delegation

## Status and intent

Slice 2 of IPv6 parity with pfSense ([issue #7](https://github.com/andaru-project/andaru/issues/7)). [Slice 1](2026-11-12-ipv6-lan-design.md) already applies router advertisements and delegated prefixes, but refuses the advertisement modes that need a DHCPv6 server (`managed`, `assisted`, `stateless`). This slice adds Kea's DHCPv6 server on LAN segments with static IPv6 prefixes, and lifts that refusal.

Owner decisions, 2026-11-13:
- **Prefix delegation to downstream routers** is part of this slice: "it's not optional for my setup".
- **Order:** DHCPv6 on tracked (delegated) prefixes stays in slice 3, as decided on 2026-11-12.

Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

### Authorities

- [DHCP and DNS](2026-11-12-dhcp-dns-design.md): the pattern this slice repeats.
  - Kea is run from the confirmed generation, with Debian's unit and Andaru's drop-in and AppArmor rules.
  - Lease names are published by `andaru realize leases`, which `andaru-leases.path` starts when the Kea hook marks a change.
  - Kea's own checker judges the files at apply.
- [IPv6 for the LAN](2026-11-12-ipv6-lan-design.md): `ra.mode`; a served segment's stable IPv6 address, which the DNS service listens on; `servedV6`.
- [Packet filter](2026-11-12-packet-filter-design.md): fixed input rules for DHCP; forwarding stays the user's policy.
- pfSense's Kea DHCPv6 (`services_dhcpv6.php`, `kea2fib6`): address pools, prefix delegation pools, static mappings by DUID, DNS and domain options, lifetimes, and routes for delegated prefixes.

### Probe behind it

Kea 2.6.3 (Debian 13), 2026-11-13, a client running systemd-networkd 257 in a network namespace:
- **Leases:** `lease6-get-all` lists the IA_NA and IA_PD leases (`ip-address`, `prefix-len`, `duid`, `iaid`, `state`, `subnet-id`, `valid-lft`, `cltt`, `hostname`). Nothing in a lease names the router that asked for it or the interface it asked on. `hw-address` stayed empty for a DUID-EN client.
- **Hook data:** `run_script` on `leases6_committed` carries `QUERY6_REMOTE_ADDR` (the requester's link-local address) and `QUERY6_IFACE_NAME`, plus `LEASES6_AT<n>_ADDRESS`, `_PREFIX_LEN` and `_TYPE` for each lease. The IA_PD lease is not always at index 0. A release fires `leases6_committed` again with `DELETED_LEASES6_AT*`. `lease6_expire` carries no query.
- **The next hop must come from the hook:** with a stable-privacy link-local address (systemd's own option, Windows, many home routers), the next hop cannot be derived from a MAC address.
- **Config syntax:** `kea-dhcp6 -t` accepts the following.
  - reservations by `duid`, with `ip-addresses` and `prefixes`;
  - `dns-servers` and `domain-search`;
  - `pd-pools` with `excluded-prefix`;
  - `valid-lifetime` and `preferred-lifetime`.
- **networkd and foreign routes:** systemd-networkd 257 removes a route it did not create (protocol 77 here) when it restarts (probed). A link reconfiguration, which an apply causes, does the same (`ManageForeignRoutes=yes`, its default).
- **Runtime:**
  - a `subnet6` must name its `interface`, or clients on the link get "could not select subnet";
  - leases persist across a restart;
  - Kea never listens on an interface that had no carrier when it started, unless `service-sockets-max-retries` is set.

## Scope

| In this slice | Later |
| --- | --- |
| DHCPv6 on segments with a static IPv6 `/64` that is not link-local: address ranges, lease lifetimes, static leases by DUID (address, prefix, hostname), DNS servers and the domain search list | DHCPv6 on delegated segments (slice 3) |
| Prefix delegation pools: a static prefix the firewall owns, the delegated length, an optional excluded prefix | Delegating parts of a prefix the firewall itself received (slice 3) |
| Routes to each delegated prefix via the router that requested it, kept across reboots | DHCPv6 relays; prefix delegation through a relay |
| Advertisement modes `managed`, `assisted`, `stateless` applied | NTP, SIP and custom options; netboot; deny unknown clients |
| Lease names for DHCPv6 address leases (AAAA and PTR) | Dynamic DNS to other servers |
| The fixed input rule DHCPv6 needs | High availability |

## Declaration

### `dhcp6` (collection, prefix `dh6`)

One entry per served segment, beside `dhcp`. `schemaVersion` stays 1.

| Field | Meaning |
| --- | --- |
| `segment` | A segment with IPv6 `static` and a `/64` that is not link-local; the first such `/64` is the served subnet. |
| `ranges` | `first`, `last`: IPv6 addresses inside the served `/64`, not holding the segment's own addresses. Optional: a `stateless` segment serves options only. |
| `leaseTime` | Valid lifetime in seconds, 300–604800, default 7200. The preferred lifetime is five-eighths of it, Kea's default ratio. |
| `dnsServers` | IPv6 addresses. Default: the segment's stable address when the DNS service serves the segment. |
| `staticLeases` | `duid` (hex, validated as in slice 1), `address` (inside the subnet, outside the ranges), `prefix` (inside a delegation pool, of its delegated length), `hostname` (a DNS label). At least one of `address` and `prefix`. A hostname is unique among the DHCPv6 static leases; it may repeat a DHCPv4 static lease's, since one device has both. |
| `prefixDelegation` | Optional: `prefix` (an IPv6 prefix the firewall owns and routes, for example a static `/48` from the provider or a unique local `/48`), `delegatedLength` (48–64, longer than the prefix), `excludedPrefix` (optional, RFC 6603). |

**Validation:**
- one entry per segment;
- the delegation `prefix` overlaps no segment's addresses and no other entry's pool;
- each static `duid` appears once per entry;
- each static address and prefix appears once.

The domain search list is `dns.localDomain` followed by `dns.searchDomains`, as in the router advertisements.

### Router advertisements

`ra.mode` `managed`, `assisted` and `stateless` are applied when the segment has a `dhcp6` entry. Without one, the apply refuses them as now ("needs a DHCPv6 entry for this segment"). An entry with `ranges` and mode `unmanaged`, `router` or `disabled` is allowed: some clients ask for DHCPv6 without the flags. A warning, `DHCP6_UNANNOUNCED`, says clients that follow the flags will not ask, and, for `disabled`, that clients get no default route. A `prefixDelegation` is refused at apply without the packet filter (a management segment): IPv6 forwarding stays off without it, as for delegated prefixes in slice 1.

## Realization

### Files Andaru renders

- `/run/andaru-kea/kea-dhcp6.conf`: one `subnet6` per entry, with:
  - its `interface`, `pools`, `pd-pools` and `reservations`;
  - `option-data` for `dns-servers` and `domain-search`;
  - `valid-lifetime` and `preferred-lifetime`;
  - a `user-context` for the lease sync, as for DHCPv4.

  Top-level settings:
  - `interfaces-config` with `service-sockets-max-retries`, as for DHCPv4;
  - the control socket `/run/kea/kea6-ctrl-socket`;
  - a persisted memfile;
  - `libdhcp_lease_cmds.so` and `libdhcp_run_script.so`. The script is `/usr/share/kea/scripts/andaru-lease-event`, shared with DHCPv4.

### Files Andaru installs once (packaging)

- `kea-dhcp6-server.service.d/andaru.conf`: the same shape as the DHCPv4 drop-in, with the condition on the rendered file, `ExecStart` on it, the lease event after start, and restart on failure.
- `apparmor/usr.sbin.kea-dhcp6`: read `/run/andaru-kea/`, run the event script, write the files below.
- **`andaru-lease-event` gains one job.** On `leases6_committed` it appends one line per IA_PD lease and per deleted IA_PD lease to `/run/kea/andaru-pd-events`: `<add|del> <prefix>/<len> <QUERY6_REMOTE_ADDR> <QUERY6_IFACE_NAME>`. It still touches the change marker. Kea runs it as `_kea`, with no added privilege.

### Routes to delegated prefixes

`andaru realize leases` (root, started by `andaru-leases.path`) reconciles the routes after the lease names:

1. **Gather.** It reads and truncates `/run/kea/andaru-pd-events`, and folds the records into the next-hop table `/var/lib/andaru/pd-next-hops.json`: prefix to next hop and interface.
2. **Check.** A record is believed only when:
   - its interface is a served segment of the confirmed generation;
   - its next hop is a link-local unicast address;
   - its prefix lies in that segment's delegation pool or one of its static prefixes.
3. **Install.** For every active IA_PD lease Kea lists (`state` 0), whose prefix has a believed next hop, the route `<prefix> via <next hop> dev <interface> proto 77` is installed with `ip -6 route replace`.
4. **Remove.** Every route of protocol 77 that is not wanted is removed, and the table drops prefixes Kea no longer leases. Protocol number 77 (named `andaru` in `rt_protos.d`) marks Andaru's delegated-prefix routes, so nothing else is touched.

**Pool route:** each delegation pool is also rendered into its segment's `.network` file as `[Route] Destination=<pool> Type=unreachable`, so traffic to a part of the pool nobody leases is refused at the firewall instead of following the default route out of the WAN (a routing loop with a provider's /48, a leak with a unique local pool). networkd owns that route.

**Keeping the routes:** Andaru installs `/etc/systemd/networkd.conf.d/andaru.conf` with `ManageForeignRoutes=no`, so networkd leaves the delegated-prefix routes alone across restarts, package upgrades and applies. Andaru removes its own stale routes itself (step 4). This also leaves other software's routes alone, which networkd would otherwise delete when it restarts.

**The event file:** root never opens a file in Kea's directory in place. The sync renames `/run/kea/andaru-pd-events` to `/run/andaru-leases/pd-events` (one tmpfs, so the rename is atomic and Kea's next append creates a new file), opens it with `O_NOFOLLOW`, refuses anything but a regular file owned by `_kea` and under 1 MiB, and parses each line strictly; an unparsable line is ignored.

The table lives in `/var/lib`, so after a reboot the routes return as soon as Kea and the boot unit start the sync, without waiting for the routers to renew. A lease whose next hop was never seen (a lease from before this slice) gets its route at the router's next renewal.

The routes follow leases, not generations. A rollback or an apply without the entry removes the routes the new generation no longer serves on the next sync, which the apply triggers.

### Filter

A fixed input rule accepts DHCPv6 requests on served segments: `iifname <segment> udp sport 546 udp dport 547`, sourced from link-local addresses. Delegated prefixes are routed through the `forward` chain, so the user's policy decides what passes, by zone, as for every LAN.

### Lease names

For IA_NA leases with a hostname that is a valid label, `andaru realize leases` publishes AAAA and PTR records, exactly as for DHCPv4: the most recently renewed lease wins a name. A name static in either family blocks dynamic A and AAAA records for that name: otherwise a LAN client could claim a DHCPv4-only device's name for AAAA, which dual-stack clients prefer, or a DHCPv6-only device's name for A, which IPv4-only clients and fallbacks use. A device with the same static name in both families keeps both records, since its static leases publish them. Static DHCPv6 names are rendered into the DNS service's configuration when it is written, as DHCPv4 static names are. Kea's DHCPv6 hostname comes from the client's FQDN option.

**DNS behind a downstream router:** the DNS service's access list for a served segment also covers that segment's delegation pool, so hosts numbered from a delegated prefix can use it. The input policy decides which interfaces may reach it, as for every client.

## API and CLI

`dhcp6` is a collection like `dhcp`: `/api/v1/config/dhcp6`, `andaru ctl config … dhcp6`.

## Acceptance

### Automated

- Unit tests for every validation rule.
- Render goldens: `kea-dhcp6.conf`, the advertisement flags with an entry, the input rule.
- Lease sync tests with a fake Kea socket and a fake event file:
  - a believed record installs its route;
  - a record on an unserved interface is ignored, and so is one with a non-link-local next hop or a prefix outside the pool;
  - a deleted lease removes its route;
  - a restart with an empty event file reinstalls routes from the table.
- `kea-dhcp6 -t` on the rendered goldens in a Debian 13 container.

### Lab (Proxmox)

VM 114 is the firewall, with a static ULA `/64` on the LAN and a delegation pool `fd00:beef:100::/48` delegating `/56`. Two LAN clients:
- VM 117, with mode `managed`:
  - it gets a DHCPv6 address from the range, and a static lease by DUID;
  - its name resolves AAAA and PTR through the firewall.
- A downstream router, a small Debian guest or container on the LAN running networkd with DHCPv6 prefix delegation:
  - it gets a `/56`, and the firewall shows `fd00:beef:100:…::/56 via fe80::… proto 77`;
  - a host behind it reaches the firewall's LAN address, through the policy;
  - after a firewall reboot the route is back before the router renews;
  - a release removes the route.

## Approaches

- **Kea gets network-admin rights and adds the routes in its hook,** as the lab router and pfSense's `kea2fib6` do. Immediate, but it widens what Kea can do, and the routes vanish on reboot until clients renew.
- **The hook records, Andaru's privileged sync checks and installs** (chosen). Kea stays unprivileged. The sync believes nothing it cannot check against the generation and Kea's own leases. The next-hop table brings the routes back at boot. The cost is the sync's start delay, under a second.
- **Derive the next hop from the lease.** Impossible: leases carry neither the requester's address nor, for most clients, its MAC.

## Gaps against pfSense, stated

- No relay support and no DHCPv6 on delegated segments (slice 3).
- No NTP, SIP, netboot or custom options, and no deny-unknown-clients.
- `domain-search` is sent only to clients that ask for it (option 24); systemd-networkd does not ask by default, and the router advertisements' DNSSL covers it.
