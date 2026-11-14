# DHCPv6 on delegated prefixes, the prefix watcher, and deprecating a replaced prefix

## Status and intent

Slice 3 of IPv6 parity with pfSense ([issue #7](https://github.com/andaru-project/andaru/issues/7)), the last one. [Slice 1](2026-11-12-ipv6-lan-design.md) numbers LAN segments from a prefix the WAN's DHCPv6 client receives (`delegated` segments). [Slice 2](2026-11-13-dhcpv6-design.md) serves DHCPv6, and prefix delegation to downstream routers, only on segments with a static `/64`. This slice extends both to the WAN's prefix. When the provider changes that prefix, Andaru follows it, and tells clients to stop using the old one.

Owner decisions, 2026-11-13:
- **Delegating parts of the WAN's prefix to downstream routers** is in this slice (option (a)).
- **Approach A:** a systemd path unit starts a root one-shot on each prefix change. There is no long-running watcher daemon, and no new generation per prefix change.
- **Kea is reconfigured in place** (`config-set` on its control socket) when the prefix changes. It is never restarted for that.
- **The owner's setup:** the provider delegates a `/48` or a `/56`, and only on an explicit request (`requestPrefixLength`). A layout must therefore keep working when a `/56` arrives for a `/48` request (see "At run time" below).
- **Work from the prefix actually delegated** (2026-11-13): providers rarely change it, so Andaru shows the current prefix and judges what the user enters against it. A mismatch with the configuration is reported, and tolerated where the layout still fits: subnet IDs `1` and `2` fit a `/48` and a `/56` alike.
- **Three ways to use the prefix:** a full `/64` per segment or VLAN by subnet ID (16 bits on a `/48`, slice 1's `subnetId`), DHCPv6 on those segments, and delegating parts of it onwards (for example a `/56` of a `/48` to another router).

Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

### Authorities

- [IPv6 for the LAN](2026-11-12-ipv6-lan-design.md): `delegated` segments (`from`, `subnetId`), the segment's stable unique local address that the DNS service listens on, and advertisement modes.
- [DHCPv6 for the LAN](2026-11-13-dhcpv6-design.md): the `dhcp6` collection, Kea's DHCPv6 file and drop-in, the lease sync, routes to delegated prefixes (protocol 77, `ManageForeignRoutes=no`), and the unreachable pool route.
- pfSense master, read 2026-11-13:
  - **Subnet IDs:** "IPv6 Prefix ID" (`track6-prefix-id`) is hex, from 0 to 2^n − 1, where n is the *requested* delegation size (`dhcp6-ia-pd-len`, bits short of 64: 16 for a `/48`), and unique per tracked WAN (`interfaces.php`, `calculate_ipv6_delegation_length`). The actual delegation is not consulted.
  - **DHCPv6 on a tracked interface:** ranges, DNS and NTP servers and static addresses are written with the upper 64 bits zero and merged with the interface's current prefix (`services_dhcpv6.php`, `merge_ipv6_delegated_prefix` in `services.inc`).
  - **Prefix delegation pools:** a literal prefix (`pdprefix`, `pdprefixlen` 48–64, `pddellen`), never merged with the tracked prefix. pfSense cannot delegate onwards from a dynamic prefix; this slice can.
  - radvd deprecates a replaced prefix.

### Probe behind it

systemd-networkd 257 and Kea 2.6.3 (Debian 13), 2026-11-13, in containers: an upstream Kea delegating a `/56`, a firewall running networkd with two delegated LANs, and networkd and kernel SLAAC clients. Raw results: `/tmp/slice3-probe/RESULTS.md` on the build host.

- **Reading the prefix:** networkd installs `unreachable <delegated prefix> proto dhcp` while it holds a delegation, and swaps it on a change; each delegated LAN's address carries the prefix's remaining lifetimes. `networkctl status <wan> --json=short` gives `.DHCPv6Client.Prefixes[]` with `Prefix` (16 bytes as an integer array), `PrefixLength`, `PreferredLifetimeUSec` and `ValidLifetimeUSec`; the times are absolute `CLOCK_BOOTTIME` microseconds. networkctl's JSON comes over D-Bus, so it needs a system bus. In 257, networkd's varlink socket has no link description, and `/run/systemd/netif/links/<ifindex>` holds no prefix.
- **A prefix change:** at the next renewal the upstream returns the new prefix and the old one at lifetimes 0. networkd then replaces the LAN's addresses within a millisecond, swaps the unreachable route for the delegated prefix, and sends one advertisement listing the old `/64` at its remaining lifetimes beside the new one. Later advertisements omit the old `/64`; it is never advertised at lifetime 0. Clients keep their old address preferred until its own lifetime ends.
- **Events:** a path unit with `PathChanged=` on `/run/systemd/netif/links/<n>` fires on a prefix change, a reconfiguration and the end of duplicate address detection. It did not fire on renewals that only refresh lifetimes. `ip -6 monitor address` fires on every renewal.
- **Deprecating:** an advertisement sent by root from the LAN's link-local address, listing the old `/64` with preferred lifetime 0 and router lifetime equal to networkd's, marks the address `deprecated` at once on networkd and kernel clients. RFC 4862's two-hour rule keeps the remaining valid lifetime. It must follow networkd's last advertisement that still lists the old prefix, or that one un-deprecates it. A router lifetime of 0 would remove the clients' default route. Making networkd itself advertise the old prefix (`[IPv6Prefix]` and `networkctl reload`) also deprecates it, but the reload advertised router lifetime 0 (the client lost its default route for 66 ms) and removed the delegated prefix's unreachable route until the next renewal.
- **Kea and a changed prefix:** `config-set`, `config-reload` and a restart all accept a subnet whose prefix changed under the same subnet ID. Existing leases are kept and nothing is sent to clients. At a client's next renewal, Kea allocates from the new prefix and returns the old address and prefix at lifetimes 0, deleting the old leases. No hook reports the deleted leases (`DELETED_LEASES6_SIZE=0`, no release or expiry), so slice 2's route reconciliation against `lease6-get-all`, not delete events, is what removes their routes.

## Scope

| In this slice | Later or never |
| --- | --- |
| `dhcp6` entries on `delegated` segments: ranges and static addresses written as interface-identifier suffixes | A literal static prefix on delegated segments (refused: a position in the pool is used instead) |
| Prefix delegation from the WAN's prefix: a pool at a subnet ID of the WAN prefix | An excluded prefix on such a pool (refused) |
| Following a prefix change: Kea reconfigured, routes and unreachable pool route updated, names follow the leases | Reconfigure messages to clients (Kea 2.6 sends none) |
| Deprecating a replaced `/64` on every delegated segment that advertises prefixes | Shortening the old prefix's valid lifetime below RFC 4862's two hours |
| Static lease names on delegated segments, published by the lease sync | DHCPv6 on `slaac` or `dhcp6` segments |

## Declaration

`schemaVersion` stays 1. No new collection: a `dhcp6` entry may now name a `delegated` segment.

### On a `delegated` segment

| Field | Meaning |
| --- | --- |
| `ranges` | `first`, `last`: written as suffixes, IPv6 addresses whose first 64 bits are zero (`::1000` to `::1fff`). The current `/64` is put in front of them. |
| `staticLeases` | `duid`, `address` (a suffix as above), `prefixId` (hex: a position in the entry's pool, `0` being its first delegated prefix), `hostname`. At least one of `address` and `prefixId`. A literal `prefix` is refused ("A delegated segment's prefix changes with the provider's; name a position in the pool with prefixId."). Owner decision, 2026-11-13: a router keeps the same place in the pool whatever prefix the provider delegates. |
| `leaseTime`, `dnsServers` | As on static segments. `dnsServers` default to the segment's stable unique local address when the DNS service serves the segment, as slice 1's advertisements do. |
| `prefixDelegation` | `subnetId` (hex), `length` (the pool's prefix length), `delegatedLength` (`length` to 64). The pool is the WAN's prefix with `subnetId` placed at `length`: on a `/56`, `subnetId: "8", length: 60` is the sixteen `/64`s from subnet `0x80`. `prefix` and `excludedPrefix` are refused on a delegated segment. |

**Validation (the saved declaration alone):**
- A delegated segment's `subnetId` fits in `64 − requestPrefixLength` bits (pfSense's rule; slice 1 only checked 16 bits).
- Suffixes lie in `::/64`, and ranges do not overlap each other.
- `length` is longer than the WAN segment's `requestPrefixLength` (default 64 in slice 1: a pool needs a hint of 63 or shorter), and at most 64. `subnetId` fits in `length − requestPrefixLength` bits.
- `prefixId` fits in `delegatedLength − length` bits and appears once per entry.
- The pool overlaps no delegated segment's `/64` on the same WAN (each takes `subnetId` at 64), and no other pool.

**Against the prefix actually delegated:** the declaration is judged against the requested length when it is saved, and against the current prefix (below) at apply and at run time:
- **Fits:** nothing is reported, even when the delegated length differs from the requested one.
- **Does not fit** (a `/56` for a `/48` and a subnet ID above `ff`): the apply warns `DELEGATED_PREFIX_TOO_SHORT`, naming each segment and pool that will not be served, with the current prefix; the rest is applied. At run time the same parts are left out, `andaru realize prefixes` logs the warning, and the prefix view (below) shows it.
- **No prefix known yet:** only the saved-declaration rules apply.

### Router advertisements

Unchanged from slices 1 and 2: `managed`, `assisted` and `stateless` need a `dhcp6` entry on the segment, now also on delegated segments.

## Realization

### What the generation holds

The generation renders Kea's DHCPv6 file as today, plus, for each delegated entry, a `subnet6` template: the suffixes, the pool's `subnetId`, `length` and `delegatedLength`, and in its `user-context.andaru` the uplink interface and the segment's `subnetId`. The template is `/run/andaru-kea/kea-dhcp6.template.json` in the generation; the file Kea reads stays `/run/andaru-kea/kea-dhcp6.conf`, written by root (below). Static-prefix subnets pass through unchanged.

At apply, the template is filled with a documentation prefix (`2001:db8::/48` cut to the requested length) and judged by `kea-dhcp6 -t`, as slice 2's file is.

### `andaru realize prefixes` (root)

One step, under the generation lock, used by the apply, by boot and by the path unit. It reads only the kernel (netlink, through `ip -j`), so it needs no system bus and runs on every host with a delegated segment, with or without a `dhcp6` entry.

1. **Read** each WAN's delegated prefix: the `unreachable … dev lo proto dhcp` route networkd installs for it (prefix and length; the route names no WAN, so it is matched to a WAN through that WAN's delegated segments' addresses), and, per delegated segment, its global address inside that prefix with the address's preferred and valid lifetimes. A prefix whose addresses are no longer preferred (preferred lifetime 0, as in graceful renumbering) is *replaced*, not current: of several, the current one is the preferred one.
2. **Keep the last prefix while it is valid.** No prefix seen (at boot before the WAN's lease, during an outage): the last current prefix from `/var/lib/andaru/delegated-prefixes.json` is used until its stored valid lifetime ends. Only then are the delegated subnets left out of Kea's file.
3. **Fill** the template: each delegated segment's `/64`, the pools, and each `prefixId` as a Kea reservation of the prefix at that position, with the suffixes and subnet IDs. Kea's subnet IDs stay those of the generation, so a changed prefix keeps its subnet.
4. **Write and reconfigure:** if the filled file differs from `/run/andaru-kea/kea-dhcp6.conf`, write it and send it to Kea with `config-set` on `/run/kea/kea6-ctrl-socket`. When Kea is not running yet, the unit's start reads the file. A refused `config-set` leaves Kea on its old configuration and fails the step.
5. **Routes:** the unreachable pool routes for delegated pools are installed by this step with protocol 77, and slice 2's reconciliation counts them as held. Then the lease sync runs, which removes routes of leases Kea no longer lists. A pool left out of the file (step 2 expired) keeps its saved next hops in `pd-next-hops.json` until their leases go, so routes return without waiting for each router to renew.
6. **Deprecate:** a `/64` a delegated segment had as current and has no longer is recorded in `/var/lib/andaru/replaced-prefixes.json` with its remaining valid lifetime. For ten minutes, each run advertises it on that segment with preferred lifetime 0, valid lifetime the smaller of 7200 and its remaining lifetime, and the segment's router lifetime (default 1800), M and O flags and router preference, from the segment's link-local address to `ff02::1`, only on segments whose advertisements announce prefixes (`unmanaged`, `managed`, `assisted`, `stateless`). The first is sent 5 seconds after the change is seen, after networkd's own advertisement listing the old prefix. It deprecates the addresses clients built themselves (SLAAC); a DHCPv6 address moves at the client's renewal.

The current and last prefixes, with their lifetimes, are kept in `/var/lib/andaru/delegated-prefixes.json`, so a change across a reboot is still seen. At boot the step runs after networkd, not in `andaru-network-boot` (which runs before it): Kea starts on the saved prefix, so its first lease sync keeps every next hop.

### Units (packaging)

- `andaru-prefixes.path`: `PathChanged=` on `/run/systemd/netif/links/` (the probe saw networkd replace each link's file by rename on a prefix change), starting `andaru-prefixes.service` (`Type=oneshot`, `ExecStart=/usr/bin/andaru realize prefixes`). The service runs only on a host with a delegated segment: the generation renders `/run/andaru-delegated/<interface>` for each one, and the service has `ConditionPathExistsGlob=/run/andaru-delegated/*`.
- `andaru-prefixes.timer`: every 60 seconds, the same service. It resends deprecations during their ten minutes and catches a missed path event.
- Kea's DHCPv6 drop-in is unchanged; its `ExecStartPre` condition still names the rendered file.

### Names, filter, DNS

- Dynamic lease names come from Kea's leases and the filled file, so they follow the prefix.
- Static names on a delegated segment cannot be rendered with the generation, which knows only the suffix. The lease sync publishes them from the filled file's reservations (AAAA and PTR at the current address), so they move with the prefix and a dynamic lease of the same name stays held back.
- The DNS service already answers any source on a delegated segment's listener (slice 1), so hosts behind a downstream router can use it.
- The input rule for DHCPv6 requests is slice 2's. Forwarding stays the user's policy.

## API and CLI

- **The prefix view:** `GET /api/v1/status/prefixes` and `andaru ctl prefixes` (beside `/api/v1/status/links` and `andaru ctl links`) list, per DHCPv6 WAN: the requested length, the current prefix with its preferred and valid lifetimes (or "none yet"), the subnet IDs and pool positions it leaves room for (`0` to `ff` on a `/56`), each delegated segment's `/64` and each pool's current range, and the parts that do not fit. The engine reads `/var/lib/andaru/delegated-prefixes.json`, which `andaru realize prefixes` writes; it needs no network access.
- `andaru realize prefixes` is internal, like `andaru realize leases`.

## Acceptance

### Automated

- Validation: subnet IDs against `requestPrefixLength`; suffix ranges and static addresses, refused `prefix` and `excludedPrefix`, pool length against the hint, pool overlapping a segment's `/64` or another pool.
- Render: the template golden, and the documentation-prefix fill passing `kea-dhcp6 -t` in CI.
- `realize prefixes` against a fake `ip -j` and a fake Kea socket: first fill, an unchanged prefix sends nothing, a changed prefix sends one `config-set` with the same subnet IDs, a too-long provider prefix leaves the pool out with a warning, a refused `config-set` fails the step, and the pool route is installed and kept by the lease sync.
- Deprecation: the advertisement's bytes (prefix option with preferred 0 and the bounded valid lifetime, router lifetime, flags, source), sent only in the ten-minute window and only on segments announcing prefixes; graceful renumbering (old prefix still valid, not preferred) serves the new prefix and deprecates the old.
- Fit against the current prefix: a `/56` for a `/48` request with subnet IDs `1` and `2` reports nothing; with `100` it warns `DELEGATED_PREFIX_TOO_SHORT` at apply and leaves that segment out at run time; the prefix view shows both.
- No prefix seen: the saved one is served until its valid lifetime ends, and the next hops survive.
- Static names on a delegated segment follow the prefix.
- A `prefixId` reservation is filled at its position under the current prefix, and moves with it.

### Lab (Proxmox)

On VM 114 behind `pd-router` (LXC 118), with VM 117 as the client and a downstream router container:
1. VM 117 takes a DHCPv6 address from the delegated LAN's range and resolves its own name.
2. The downstream router gets a `/64` from the pool, and a ping from behind it crosses the firewall.
3. Change `pd-router`'s pool: within a renewal, Kea serves the new prefix, VM 117's old SLAAC address shows `deprecated` within seconds, its DHCPv6 address moves at its renewal, the downstream router's new prefix is routed and the old route is gone.
4. Reboot VM 114: the prefixes and routes come back.

## Approaches

- **A, chosen: a path unit starting a root one-shot.** Same shape as the lease sync; nothing runs between changes; the timer covers deprecation repeats and a missed event.
- **B: a long-running root daemon** on netlink. Faster to react, but a permanently privileged process, and it must filter the renewal noise `ip monitor` shows.
- **C, rejected: a new generation per prefix change.** It would bypass confirm and rollback, and need the owner present to confirm.

## Gaps against pfSense, stated

- pfSense delegates only from literal pools; Andaru also delegates from the WAN's prefix, with fixed positions.
- Old leases stay until each client renews; Kea sends no Reconfigure.
- The replaced prefix stays valid on clients for up to two hours (RFC 4862), deprecated.

## Calls made after the builder check (author's, not the owner's)

- The prefix is read from the kernel (networkd's unreachable route and the LAN addresses), not from `networkctl`, so there is no D-Bus prerequisite and deprecation also works on delegated segments without DHCPv6.
- A prefix no longer preferred counts as replaced, so graceful renumbering is followed.
- The last prefix is served until its valid lifetime ends, through boot and WAN outages.
- Static names on delegated segments are published by the lease sync.
- The deprecation's valid lifetime is at most the old prefix's remaining one.
