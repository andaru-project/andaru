# Virtual addresses on the firewall, and IPv6 port forwards to it

## Status and intent

Part of [issue #37](https://github.com/andaru-project/andaru/issues/37), pfSense's *Firewall › Virtual IPs*. The owner's pfSense uses three virtual IPs, all IP aliases (read from its `config.xml`, 2026-11-14):

- `10.255.0.3/16` on the DOCKER interface, used as HAProxy's and remote syslog's source address. Andaru already covers this: a segment takes several static addresses.
- `172.16.53.53/32` and `fd00:53:53:53:53:53:53:53/128` on the loopback interface: pfBlockerNG's DNS listener. DHCP and DHCPv6 hand them out as the DNS server on every LAN, and two IPv6 port forwards send DNS sent to the firewall itself (`(self)` port 53) on two interfaces to the IPv6 one.

Owner decisions, 2026-11-14:
- **This slice:** addresses for services on the firewall itself, held on no LAN (pfSense's IP alias on `lo0`), and IPv6 port forwards to the firewall itself.
- **Proxy ARP, Other and CARP** are after the MVP, tracked in [#43](https://github.com/andaru-project/andaru/issues/43), with IPv6 port forwards to inside hosts (NAT66).

Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

### Authorities

- [Inbound NAT](2026-11-12-inbound-nat-design.md): port forwards, `self`, firewall and host targets, the connection mark binding a forward to its rule, and its promise that virtual IPs become firewall targets "with no change to this field". It refuses `127.0.0.0/8` as a target in favour of "an address on the loopback interface".
- [DHCP and DNS](2026-11-12-dhcp-dns-design.md) and [IPv6 for the LAN](2026-11-12-ipv6-lan-design.md): the DNS service's listeners and access list; `dnsServers` on DHCP, DHCPv6 and router advertisements.
- [Packet filter](2026-11-12-packet-filter-design.md): input policy by zone; the filter needs a management segment.

### Probe behind it

systemd-networkd 257, Unbound and nftables on Debian 13 (kernel 6.12), 2026-11-14, in a container with LAN and WAN client namespaces; raw results in `/tmp/vip-probe/RESULTS.md` on the build host:
- **networkd and a dummy:** a `Kind=dummy` netdev with a `/32` and a `/128` comes up `routable (configured)`; the kernel skips duplicate address detection on a dummy, so the IPv6 address is never tentative. A dummy is `RequiredForOnline=yes` by default. Removing its files and reloading does not delete it, and the `/128` stays on the unmanaged device: only `networkctl delete` removes it.
- **Reachability:** with Debian's `rp_filter` (and with strict mode), both addresses answer from the LAN and from the WAN; without a filter, anyone with a route to them reaches them.
- **Unbound:** with `ip-freebind: yes` it starts before the dummy exists and answers on both addresses once they appear, across deletes and re-creations, with `interface-action … allow` alone admitting clients.
- **nftables:** `dnat ip6 to <virtual address>` and `dnat ip to <virtual address>` from the WAN reach Unbound on it, and replies come from the address queried; an input rule on `ip6 daddr <virtual address>` matches after the translation. `redirect` rewrites to the arriving interface's address, so it cannot reach a virtual address; a redirect that changes neither address nor port leaves no `ct status dnat`, which Andaru's binding by connection mark does not rely on.
- **`fib daddr . iif type local`** (Andaru's `self`) matches only the addresses of the interface a packet arrived on, never a virtual address; `fib daddr type local` matches them too.

## Scope

| In this slice | Later (#43) |
| --- | --- |
| `virtualAddresses`: single IPv4 and IPv6 addresses held by the firewall on no LAN | Proxy ARP and proxy NDP; "Other" routed blocks; CARP |
| The DNS service listening on chosen virtual addresses | Other services binding to them (each service's own setting) |
| Port forwards (IPv4 and IPv6) to a virtual address | IPv6 port forwards to inside hosts (NAT66), NPt |
| IPv6 port forwards to the firewall itself: `self`, its static IPv6 addresses, its IPv6 virtual addresses | IPv6 1:1 NAT |

## Declaration

`schemaVersion` stays 1.

### `virtualAddresses` (collection, prefix `vip`)

| Field | Meaning |
| --- | --- |
| `name` | Unique in the collection. |
| `address` | One IPv4 or IPv6 unicast address, without a length. |
| `description` | Optional. |

**Validation:**
- Not unspecified, multicast, loopback (`127.0.0.0/8`, `::1`), link-local, or an IPv4-mapped address.
- Not equal to another virtual address or to any static address of a segment.
- Not inside the subnet of any segment's static address: such an address belongs on the segment as an extra address, where the LAN finds it (Linux answers ARP for any local address on every interface, so it would work by accident and confuse).
- At most 64 entries.
- **Referenced, it cannot go away:** a virtual address a port forward targets (by its address) cannot be deleted, nor its address changed, while the forward exists: the delete or change is refused on the virtual address. Otherwise the forward would silently become a forward to an inside host nobody holds, and its traffic would follow the default route.

### `dns.virtualAddresses`

A list of virtual address IDs the DNS service also listens on. Each must exist. A virtual address in the list cannot be deleted (reference check, as for `dns.segments`).

The DHCP, DHCPv6 and router-advertisement `dnsServers` fields already accept any unicast address: to hand clients the virtual address, name it there. No default changes.

### `nat.portForwards`: `family`

| Field | Meaning |
| --- | --- |
| `family` | `ipv4` (default, the slice before this one) or `ipv6`. |

For an `ipv6` forward:
- `to` is `{"self": true}` (the addresses of the interface the connection arrives on, as for IPv4) or an address object.
- `target` is `{"self": true}`, or an `address` the firewall holds: one of its segments' static IPv6 addresses or an IPv6 virtual address. Any other address is refused: "IPv6 port forwards to inside hosts are not offered (#43)."
- `from` and `to.address` objects must hold IPv6 addresses, as IPv4 forwards' must hold IPv4 ones.
- `reflection` is refused: the forward already reaches the firewall itself from every ingress zone it names.
- `targetPort` and port ranges work as for IPv4.

For an `ipv4` forward, an IPv4 virtual address is a firewall target, like the firewall's static addresses: its rule goes to the input chain. The renderer counts the declared virtual addresses as the firewall's own, whatever the host currently holds.

**Reflection** (IPv4 forwards to `self` with `reflection`) matches connections from inside to the firewall's own addresses other than the internal ones. Virtual addresses count as internal: a LAN client talking to a virtual address reaches the service on it, never a reflected forward for the same port.

The overlap check (same source, destination, zone, protocol, overlapping ports) also keys on `family`.

## Realization

### The addresses

Virtual addresses live on a dummy interface, `andaru-vip`, which the generation renders when the declaration has any: `50-andaru-andaru-vip.netdev` (`Kind=dummy`) and `50-andaru-andaru-vip.network` with one `Address=` per entry (`/32` or `/128`), `LinkLocalAddressing=no` and `RequiredForOnline=no` (the kernel skips duplicate address detection on a dummy, probed). Without entries both files are absent, and the apply deletes the device with `networkctl delete`, as it deletes any device whose `.netdev` is gone: networkd alone would leave it, with its IPv6 address (probed). A dummy, not `lo`: networkd then owns it entirely, on netplan, ifupdown and networkd hosts alike, and `lo` stays the system's.

The link name `andaru-vip` is reserved: a link may not take it (a link's resolved name never matches it on a real host; validation refuses it as a declared name).

### DNS

For each virtual address in `dns.virtualAddresses`, Unbound gets `interface: <address>` and `interface-action: <address> allow`: every source reaching it may ask. Who reaches it is the filter's decision, so the apply refuses `dns.virtualAddresses` without the packet filter (`APPLY_UNSUPPORTED` at `/dns/virtualAddresses`, "needs the packet filter, which needs a management segment"). `ip-freebind: yes` already lets Unbound start before networkd has the addresses.

### Port forwards

IPv6 forwards render in the same `inet andaru` nat prerouting chain:
- match: `iifname @<zone> meta nfproto ipv6` with `fib daddr . iif type local` for `to.self`, or `ip6 daddr @<set>_6`; sources `ip6 saddr @<set>_6`;
- action: `redirect` for a `self` target, `dnat ip6 to <address>` (with `:port` or a port map) for an address target: `redirect` only reaches the arriving interface's address, so a virtual address always takes `dnat` (probed);
- the associated rule, in the input chain: `meta nfproto ipv6`, the forward's connection mark, the translated port, `accept`.

IPv4 forwards whose target is an IPv4 virtual address render as forwards to the firewall's own address do today.

## API and CLI

`virtualAddresses` is a collection: `/api/v1/config/virtualAddresses`, `andaru ctl config … virtualAddresses`. `family` is a new port forward field.

## Acceptance

### Automated

- Validation: each refusal above, at its path; a virtual address named by `dns.virtualAddresses` cannot be deleted; an IPv6 forward to an inside host, with reflection, or with IPv4-only objects is refused.
- Render goldens: the dummy netdev and network files; Unbound's listener and `interface-action`; an IPv6 forward to `self` and to a virtual address, each with its input rule; an IPv4 forward to a virtual address with an input rule. Declarations without virtual addresses or IPv6 forwards render byte for byte as today.
- The apply gate for DNS on virtual addresses without the filter.
- nftables accepts the rendered IPv6 forwards (`nft -c` in the namespace tests).

### Lab (Proxmox)

On VM 114 with VM 117 on the LAN: virtual addresses `172.16.53.53` and `fd00:53:53:53:53:53:53:53`, the DNS service listening on both, DHCP and DHCPv6 handing them out, and IPv4 and IPv6 forwards sending DNS for the firewall's LAN addresses to them. VM 117 resolves through both addresses and through the firewall's own addresses; after a reboot it still does.

## Approaches

- **A dummy interface (chosen):** networkd creates and owns it; nothing else configures it on any of the three host kinds Andaru takes over.
- **`lo`:** pfSense's choice, but on Linux ifupdown or the kernel already own `lo`, and networkd would share it.
- **An extra address on a segment:** already possible; it is answered on that segment's LAN, which a service address for every LAN should not be.

## Calls made after the builder check (author's, not the owner's)

- A port forward's target stays a literal address, as the inbound NAT design promised; deleting or re-addressing a virtual address a forward targets is refused instead.
- Virtual addresses are excluded from reflection, as the internal segment addresses are.

## Gaps against pfSense, stated

- Only single addresses, not ranges or subnets.
- No Proxy ARP, Other or CARP (#43); no IPv6 forwards to inside hosts (#43).
