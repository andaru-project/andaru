# Proxy ARP: answering for addresses the firewall does not hold

## Status and intent

Part of [#43](https://github.com/andaru-project/andaru/issues/43), pfSense's *Virtual IPs › Proxy ARP*. Owner decision, 2026-11-27: build proxy ARP and proxy NDP next, so that 1:1 NAT and port forwards can use extra addresses on a shared WAN subnet; "Other" and CARP stay later.

Today a 1:1 entry or a forward whose external address sits on an uplink's subnet, without being one of the segment's addresses, gets the warning `PROXY_ARP_REQUIRED` ([inbound NAT](2026-11-12-inbound-nat-design.md)): nothing answers ARP for it. With this slice, a virtual address of kind `proxyArp` answers for it, and for a whole subnet, without the firewall holding the address.

Not implemented behaviour until the slice lands. Contracts remain provisional until Andaru 1.0.

## Probe behind it

Kernel 6.12, systemd 257 (Debian 13), 2026-11-27, in network namespaces: an ISP neighbour and the firewall on a shared `203.0.113.0/24`, an inside host on `192.168.1.0/24`, and a 1:1 entry `203.0.113.50` → `192.168.1.10`.

- **A proxy neighbour entry alone does nothing** (`ip neigh add proxy … dev wan`, with or without `proxy_arp`). The kernel proxies an ARP request only when its route to the target leaves by another interface (`arp_process`: `rt->dst.dev != dev`). The external address is on the WAN's own subnet, so a route to it is needed first.
- **A route via the dummy plus per-address entries works,** and so does a route via the dummy plus `proxy_arp=1` on the WAN.
- **Interface-wide `proxy_arp=1` leaks:** it also answered, on the WAN, an ARP request for a LAN host (`192.168.1.10`) from a neighbour that treats the LAN subnet as on-link. Any route through another interface (LAN, VPN) would be proxied.
- **`medium_id` closes the leak.** `arp_fwd_proxy` proxies only between devices whose `medium_id` differ, and never towards one at `-1`. With every interface at `1` (`net.ipv4.conf.default.medium_id = 1` and the glob `net.ipv4.conf.*.medium_id = 1`) and `andaru-vip` at `2`, `proxy_arp=1` on the WAN answers only for routes on `andaru-vip`. A LAN host and a route on an interface created later (inheriting `default`) were not answered. A systemd sysctl.d file applies the explicit `andaru-vip` entry over the glob, and udev's `99-systemd.rules` applies it to each interface as it appears.
- **Link events:** an administrative down and up of the WAN deletes per-address proxy entries (IPv4 and IPv6) and the routes on it; carrier loss keeps them. `proxy_arp` and `medium_id` are interface settings and survive both. networkd puts back its own routes and IPv6 proxy entries; nothing would put back IPv4 entries an apply added by hand.
- **A subnet:** a `203.0.113.56/29` route via the dummy, with a prefix 1:1 map to `192.168.1.8/29`, was answered for `.58`.
- **IPv6:** `proxy_ndp=1` and a per-address entry answer without a route (networkd: `IPv6ProxyNDPAddress=`).

## Scope

| In this slice | Later (#43) |
| --- | --- |
| IPv4 proxy ARP for an address or a subnet (/24 to /32) on a segment | Proxy NDP (below), "Other" routed blocks, CARP |
| 1:1 NAT and port forwards using those addresses | Ranges that are not a prefix |

**Proxy NDP waits.** An IPv6 address answered by the firewall but not held by it has nowhere to go: Andaru offers no IPv6 forwards to inside hosts (NAT66, #43), and a routed inside host on the WAN's prefix needs a design of its own (its route, its LAN addressing). The kind refuses IPv6 addresses until then.

## Declaration

`schemaVersion` stays at its current value: the new fields are optional and absent means the old behaviour.

### `virtualAddresses` gains `kind` and `segment`

| Field | Meaning |
| --- | --- |
| `kind` | `address` (absent means `address`): held by the firewall on `andaru-vip`, as before. `proxyArp`: answered for on a segment, held by nobody on the firewall. |
| `address` | For `address`, one address without a length, as before. For `proxyArp`, an IPv4 address, bare or as a /32, or a prefix of length 24 to 32 written with its length (`203.0.113.56/29`); its host bits must be zero. It is stored in canonical form. |
| `segment` | `proxyArp` only, required: the segment on whose link the firewall answers ARP. |

**Validation of a `proxyArp` entry:**
- IPv4 only: "Proxy NDP is not offered yet (#43)." at `/address`.
- Inside a static IPv4 subnet of its segment: "Expected an address on one of the segment's static IPv4 subnets." A segment addressed by DHCP or PPPoE has no known subnet and is refused at `/segment`.
- It contains none of any segment's static addresses or gateways, no `address` virtual address, and overlaps no other `proxyArp` entry. A gateway inside it would be routed to `andaru-vip`, cutting the uplink off: "This range holds a segment's gateway."
- It holds no other host the declaration names, whose traffic the route to `andaru-vip` would take over, nor its subnet's network or broadcast address. One message for each, at `/address`:
  - an address in a `dhcp` range: "This range overlaps a DHCP range."
  - a `dhcp` static lease: "This range holds a DHCP static lease."
  - a `dhcp` entry's DNS servers, NTP servers or next server: "This range holds a DHCP entry's DNS server." (NTP server, next server).
  - a `dhcp` `gateway` option: "This range holds a DHCP entry's gateway."
  - a segment's `addressing.dns.servers`: "This range holds a segment's DNS server."
  - a `dns` upstream: "This range holds a DNS upstream."
  - an address in the aliases `management.allowedSources` uses, through groups: a host, a network or a range: "This range holds a management source address."
  - the network or broadcast address of a static IPv4 subnet shorter than /31: "This range holds its subnet's network address." or "This range holds its subnet's broadcast address."
- `dns.virtualAddresses` may not name it: the firewall holds no such address.
- `address` entries keep their rules; the one against a segment's subnet already keeps them out of every `proxyArp` entry.
- The 64-entry limit covers both kinds.
- A `/32` written with its length is stored bare, so each entry has one form.

At apply, `proxyArp` entries without the packet filter are refused, as `dns.virtualAddresses` is: the kernel proxies ARP only for a route it forwards to, and forwarding is rendered only with the filter. `APPLY_UNSUPPORTED` at `/virtualAddresses/<i>/kind`: "Proxy ARP entries need the packet filter, which needs a management segment. Set management.segment."

A port forward target or a 1:1 `internal` address inside a `proxyArp` entry is refused: no host holds it, and its traffic would follow the route to `andaru-vip`. "This address is answered by proxy ARP and held by no host."

## Realization

- **`andaru-vip`** is rendered when there is any virtual address of either kind. Its `.network` gains one `[Route]` with `Destination=<address or prefix>` per `proxyArp` entry: the route that leaves by another interface.
- **The segment's link** gets `IPv4ProxyARP=yes` in its `.network` when one or more `proxyArp` entries name it. Without entries the key is absent; networkd does not reset `proxy_arp` when it is removed, so it stays at 1 until a reboot. That is harmless: `medium_id` scopes it to routes on `andaru-vip`, and none are left.
- **`medium_id`** comes from a static file the image ships, `/usr/lib/sysctl.d/50-andaru-proxy-arp.conf`:
  ```
  net.ipv4.conf.default.medium_id = 1
  net.ipv4.conf.*.medium_id = 1
  net.ipv4.conf.andaru-vip.medium_id = 2
  ```
  `medium_id` changes nothing but proxy ARP, which is off everywhere Andaru does not turn it on, so the file is not rendered per generation. The glob also matches `default`, so the first line only states the intent.
- **`medium_id` does not scope answers by segment.** A link with proxy ARP on answers for every `proxyArp` entry, whichever segment it names, when a neighbour on it treats that entry's subnet as on-link. The traffic then dead-ends on the dummy, unless a 1:1 entry translates it.
- **Traffic to a proxied address** that no 1:1 entry or forward translates follows the route to `andaru-vip`, a dummy, and goes nowhere.
- **`PROXY_ARP_REQUIRED`** is not raised for an external address a `proxyArp` entry on that segment covers.

## API and UI

`kind` and `segment` are new optional fields on `/api/v1/config/virtualAddresses` objects. The Virtual IPs page offers the kind, and for `proxyArp` the segment and an address or prefix.

## Acceptance

### Automated

- Validation: every refusal above, at its path; existing declarations validate and render byte for byte as before.
- Render goldens: `andaru-vip.network` with routes; a WAN `.network` with `IPv4ProxyARP=yes`; the warning gone for a covered 1:1 external address and kept for an uncovered one.
- The namespace probe above as a root integration test: the WAN neighbour reaches the inside host through a proxied address and a proxied subnet, and gets no answer for a LAN address or for a route on an interface created later. It applies the image's file with `systemd-sysctl`, and to `andaru-vip` with `--prefix` as udev does; CI runs it.

### Lab (Proxmox)

On VM 114, a second address on the WAN's subnet as a `proxyArp` entry with a 1:1 entry to VM 117: the WAN side reaches VM 117 by it, and after a reboot it still does.

## Calls made (author's, not the owner's)

- Proxy ARP through `proxy_arp` scoped by `medium_id`, not per-address proxy entries: everything is networkd's or static, so nothing has to be put back after a link event.
- Subnets as prefixes only; pfSense's ranges are not offered.
