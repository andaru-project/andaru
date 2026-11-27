# Routed blocks: held virtual addresses that are prefixes

## Status and intent

Part of [#43](https://github.com/andaru-project/andaru/issues/43), pfSense's *Virtual IPs › Other*. Owner decision, 2026-11-27: build "Other" next.

A provider may route a block to the firewall (`198.51.100.0/29` via the WAN address) instead of putting it on the WAN's subnet. Port forwards and 1:1 NAT already work for such addresses, since they are translated before routing, and an address alias already names the block. What is missing is the firewall answering on the block itself: its own services, and port forwards to the firewall itself, on any address of it. Today a held virtual address (`kind: address`) is a single address.

Not implemented behaviour until the slice lands. Contracts remain provisional until Andaru 1.0.

## Probe behind it

Kernel 6.12, systemd 257 (Debian 13), 2026-11-27, in network namespaces: a provider neighbour routing `198.51.100.0/29` to the firewall's WAN address.

- `ip route add local 198.51.100.0/29 dev andaru-vip` makes every address of the block local. The neighbour's ping to `.5` was answered, and a TCP service bound to `.3` (no address configured anywhere) accepted a connection from it.
- networkd writes such a route with `[Route] Destination=198.51.100.0/29` and `Type=local`: with `Type=local` and no `Table=`, it uses the `local` table (`systemd.network(5)`, `Table=`).

## Declaration

`kind: address` entries accept a prefix in `address`:

| Form | Meaning |
| --- | --- |
| `172.16.53.53`, `fd00:53::53` | One held address, as before. |
| `198.51.100.0/29`, `2001:db8:53::/64` | A held block: every address of it is the firewall's own. IPv4 /24 to /32, IPv6 /48 to /128; host bits zero. |

A /32 or /128 written with its length is stored bare, so each address has one form.

**Validation:** a block follows the rules of a single held address.
- It is not unspecified, multicast, loopback, link-local or IPv4-mapped.
- It does not lie on any segment's subnet, holding none of a segment's static addresses.
- It overlaps no other virtual address of either kind. A block equal to another gets "Another virtual address already has this address."; an address inside a block, "This address is inside virtual address "<name>"."
- **A block** holds no host the declaration names, in either family: a segment's gateway, a DHCP range or static lease, a host in `management.allowedSources`, or a whole network or range there (one merely inside a wide source, as an organisation's /32, is an address the operator routes to the firewall, where no admin sits); the messages are proxy ARP's. Its local route would take their traffic, replies included, and a management source inside it would lock the operator out. Servers the declaration names (DNS and NTP servers, DNS upstreams, DHCP options) may be services on the firewall itself, so they bind only proxy ARP entries.
- **A block** overlaps no `dhcp6` delegation pool ("This range overlaps a DHCPv6 prefix delegation pool."); the pool is refused too, at `/dhcp6/<i>/prefixDelegation/prefix` ("The pool holds a virtual address."). A static lease's prefix lies inside its pool, so the pool's check covers it.
- **A single held address keeps the rules it had.** The store validates saved versions again when it loads them, so a rule a saved address could break would make that version unloadable; the two rules above are for blocks, a new form no saved version has.
- `dns.virtualAddresses` may name only a single address: "The DNS service listens on single addresses; name one address, not a block."

## Realization

- **A single address** stays an `Address=` line on `andaru-vip`, byte for byte as today.
- **A block** is a `[Route]` on `andaru-vip` with `Destination=<prefix>` and `Type=local`.
- **The firewall's own addresses:** the renderer counts every held block as the firewall's own, wherever a held address counts today:
  - the virtual address sets of the filter, which take prefixes;
  - reflection's internal addresses;
  - port forward targets that the firewall holds, which may be any address inside a held block. Such a forward's rule goes to the input chain.
- **A learned subnet:** validation cannot see what the firewall learns at run time. The renderer warns, `HELD_ADDRESS_ON_LEARNED_SUBNET` at the entry's `/address`, about a held address on a subnet of one of its interfaces, or a block on a subnet on one of the firewall's interfaces (learned by DHCP or PPP) or a prefix delegated to a WAN; a learned gateway shows up only inside its learned subnet.
- **IPv6 binding:** every address of an IPv6 block answers pings and port forwards, but Linux refuses to bind one address of an IPv6 local route (`EADDRNOTAVAIL`, kernel 6.12): a service there binds `::` or sets `IPV6_FREEBIND`. IPv4 binds one address of its block as usual.
- **Deleting or narrowing:** as for a single address, a block that a port forward targets (an address inside it) cannot be deleted, nor changed so that the target falls outside it.

## API and UI

There are no new fields. `address` on `/api/v1/config/virtualAddresses` objects of kind `address` accepts a prefix. The Virtual IPs page accepts one for held addresses, and leaves blocks out of the DNS listener choices.

## Acceptance

- **Validation:** each refusal above, at its path. A /32 or /128 is stored bare. Existing declarations validate and render byte for byte as before.
- **Render goldens:** `andaru-vip.network` with a `Type=local` route beside held addresses; the virtual address sets with a block; a port forward to an address inside a block, with its input rule.
- **Namespace test:** the probe above, as a root integration test that CI runs: the neighbour reaches a service on an address of the block, and pings an address of an IPv6 block. The test stands in for networkd: it applies the rendered routes with `ip route add local`. That networkd puts a `Type=local` route without `Table=` in the `local` table rests on `systemd.network(5)` and needs the lab.

## Calls made (author's, not the owner's)

- A block is a held virtual address of a new form, not a new kind: everything a held address means applies to every address of the block.
- No ranges that are not a prefix, as for proxy ARP.
