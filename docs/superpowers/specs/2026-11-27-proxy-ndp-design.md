# Proxy NDP: inside hosts on the uplink's IPv6 prefix

## Status and intent

Part of [#43](https://github.com/andaru-project/andaru/issues/43), pfSense's proxy NDP. Owner decision, 2026-11-27: build proxy NDP next. The proxy ARP design ([2026-11-27](2026-11-27-proxy-arp-design.md)) deferred it because an IPv6 address the firewall answers for has nowhere to go without NAT66; this slice gives it somewhere: a host inside the firewall that holds the address itself.

The case: a provider puts one /64 on the uplink's link and delegates no prefix. Without a delegated prefix, inside hosts get no global IPv6 address. With proxy NDP, an inside host takes an address of the uplink's /64; the firewall answers neighbour discovery for it on the uplink and routes it to the host's segment. No translation: the host's address is its own, and the filter's forward rules govern who reaches it.

Not implemented behaviour until the slice lands. Contracts remain provisional until Andaru 1.0.

## Probe behind it

Kernel 6.12, systemd 257 (Debian 13), 2026-11-27, in network namespaces. The provider's router is `2001:db8:1::1/64` and the firewall's uplink `2001:db8:1::2/64`. An inside host holds `2001:db8:1::50/128` on the LAN, with its default route via the firewall's LAN link-local address.

- **Nothing:** the router's ping to `::50` fails.
- **A `/128` route to the LAN alone:** it still fails, because nothing answers the router's neighbour solicitation.
- **The route plus `proxy_ndp=1` and a proxy entry for `::50` on the uplink:** the ping is answered. The host also reaches the router's address. An unrelated `::99` is not answered.
- **An administrative down and up of the uplink** deletes the proxy entry and keeps the route on the LAN. networkd sets `IPv6ProxyNDPAddress=` entries itself when it configures the link; that it puts them back after such an event is for the lab to confirm.

## Declaration

### `virtualAddresses` gains the kind `proxyNdp`

| Field | Meaning |
| --- | --- |
| `kind` | `proxyNdp`. |
| `address` | One global unicast IPv6 address, without a length: the inside host's. |
| `segment` | The segment on whose link the firewall answers neighbour discovery: the uplink whose /64 the address is on. |
| `hostSegment` | `proxyNdp` only, required: the segment the host is on. The firewall routes the address there. |

**Validation of a `proxyNdp` entry:**
- `address` is one IPv6 address, global unicast: not unique local, link-local, multicast, loopback, unspecified or IPv4-mapped. A length is refused, because the kernel's proxy table holds single addresses: "Expected one global unicast IPv6 address, without a length."
- `segment` and `hostSegment` exist and differ.
- `hostSegment` has IPv6: with mode `none` it has no link-local address and sends no router advertisements, so the host has no route back. Refused at `/hostSegment`.
- When the segment has static IPv6 addresses, the address lies on one of their subnets. For a segment addressed by SLAAC, DHCPv6 or PPPoE, the renderer warns instead (below).
- The address is none of a segment's addresses and overlaps no other virtual address of any kind. It is not inside the static subnets of any segment but `segment`, the host segment's included: a host there already has a route.
- It is no segment's gateway: the route would take the router's traffic to the host's segment. A DNS upstream or management source at the address is the inside host, which the route leads to, so it stays allowed.
- It lies in no `dhcp6` delegation pool.
- `dns.virtualAddresses` may not name it, and no port forward targets it: the firewall does not hold it.
- `hostSegment` is refused on any other kind.
- An apply with `proxyNdp` entries and no packet filter is refused, `APPLY_UNSUPPORTED` at `/virtualAddresses/<i>/kind`. Forwarding, which the routes need, comes with the filter.

## Realization

- **The segment's link `.network`** gains `IPv6ProxyNDPAddress=<address>` for each entry that names it. That implies `IPv6ProxyNDP=yes`; the kernel answers only for the listed addresses.
- **The host segment's link `.network`** gains a `[Route]` with `Destination=<address>/128` for each entry that names it, an on-link route.
- **Traffic to the host** is routed, not translated: the filter's forward chain governs it like any traffic between the two zones.
- **The warning `PROXY_NDP_OFF_SUBNET`** (at `/virtualAddresses/<i>/address`) fires for an address on no subnet the segment's link has at run time. Nothing on the uplink would ask for it there.

## The host

The host configures the address as a /128, with its default route via the firewall. Router advertisements on its segment give that route. A /64 would make the host look for every uplink neighbour on its own link, where nothing answers. The Virtual IPs page and `docs/dev/core-api.md` say so.

## Scope

| In this slice | Later |
| --- | --- |
| Single addresses, each routed to one inside segment | A whole prefix proxied (it needs a proxy daemon such as ndppd, since the kernel table holds single addresses) |
| Static host addressing | Handing the uplink's addresses out by DHCPv6 on the inside segment |

## API and UI

`kind: "proxyNdp"` and the new field `hostSegment` on `/api/v1/config/virtualAddresses`. The Virtual IPs page offers the kind, with the uplink segment, the host segment and the host's address, and says how to address the host.

## Acceptance

- **Validation:** each refusal above, at its path. Existing declarations validate and render byte for byte as before.
- **Render goldens:** a WAN `.network` with `IPv6ProxyNDPAddress=`, a LAN `.network` with the `/128` route, and the warning raised and not raised.
- **Namespace test:** the probe above, as a root integration test that CI runs. It applies the rendered settings by hand, and checks that the router reaches the host and that an unrelated address goes unanswered.
- **Lab (Proxmox):** an inside VM on an address of the uplink's /64, reached from the provider side. It is still reached after the uplink link goes down and up, and after a reboot.

## Calls made (author's, not the owner's)

- A kind of virtual address, beside `proxyArp`, rather than a field on segments: the address is the firewall's answer on one segment for a host on another, as proxy ARP's is.
- Single addresses only, as the kernel's proxy table allows; no proxy daemon.
