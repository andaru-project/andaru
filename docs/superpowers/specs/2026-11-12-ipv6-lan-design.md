# IPv6 for the LAN: router advertisements and delegated prefixes

## Status and intent

Requested in conversation: IPv6 parity with pfSense, in three slices ([issue #7](https://github.com/andaru-project/andaru/issues/7)). Owner decisions in conversation, 2026-11-12:

- **Order:** (1) LAN basics: router advertisements, prefixes delegated from the WAN, the WAN's DHCPv6 client options, DNS advertised to clients, and the filter rules they need; (2) Kea DHCPv6 on static IPv6 prefixes; (3) Kea DHCPv6 on delegated prefixes. This document is slice 1.
- **Parity target:** pfSense's IPv6 features, read from `pfsense/pfsense` master, 2026-11-12 (WAN `dhcp6c` options, "Track Interface", the six router-advertisement modes and their options, automatic DHCPv6 and ICMPv6 rules).

Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

### Authorities

- [Issue #7](https://github.com/andaru-project/andaru/issues/7): IPv6 parity.
- [Saved network configuration](2026-11-10-saved-network-configuration-design.md): `addressing.ipv6` with modes `none`, `static`, `slaac`, `dhcp6`, `delegated` (`from`, `subnetId`), `requestPrefixLength`, and `ra {enabled, managed, otherInfo}`.
- [Packet filter](2026-11-12-packet-filter-design.md): neighbour discovery always accepted; IPv6 routed, never translated; "PPPoE, delegated prefixes and sending router advertisements stay refused".
- [DHCP and DNS](2026-11-12-dhcp-dns-design.md): the DNS service listens on each served segment's static IPv4 address.

### Probes behind it

- systemd-networkd 257 (Debian 13), from its manual: `[IPv6SendRA]` offers `Managed`, `OtherInformation`, `RouterLifetimeSec`, `RouterPreference`, `HopLimit`, `ReachableTimeSec`, `RetransmitSec`, `UplinkInterface`, `EmitDNS`/`DNS`, `EmitDomains`/`Domains`, `DNSLifetimeSec`; `[IPv6Prefix]` offers `Prefix`, `AddressAutoconfiguration`, `ValidLifetimeSec`, `PreferredLifetimeSec`, `Assign`; `[IPv6PREF64Prefix]` offers `Prefix`, `LifetimeSec`; `[DHCPv6]` offers `PrefixDelegationHint`, `UseAddress`, `WithoutRA`, `SendRelease`, `DUIDType`, `DUIDRawData`; `[DHCPPrefixDelegation]` offers `UplinkInterface`, `SubnetId`, `Announce`, `Assign`, `Token`. There is no setting for the advertisement interval; networkd uses RFC 4861's defaults.
- networkd re-announces a delegated prefix when the upstream changes it and deprecates the old one by itself; nothing in Andaru has to react to a prefix change in this slice.
- Probed in privileged `debian:trixie` containers (unbound 1.26.2-0+deb13u1, systemd 257.13-1~deb13u1), 2026-11-12:
  - Unbound with `ip-freebind: yes` and `interface: fd00:a5d:1::1` starts before the address exists and answers once it appears.
  - `interface-action: fd00:a5d:1::1 allow` answers clients from any prefix on the link; `access-control: fd00:a5d:1::/64 allow` alone refuses a client numbered from the delegated prefix. `interface-action` must name the listener as `interface:` does: naming the interface (`lan0`) stops Unbound from starting, and `interface: lan0` resolves the addresses once at startup.
  - networkd's RA with `[IPv6Prefix]`, `EmitDNS=yes DNS=<ULA>` and `EmitDomains=yes` carries the prefix (`onlink, auto`), RDNSS and DNSSL options, with no M/O flags.
  - With `DHCPPrefixDelegation=yes` and a Kea DHCPv6 upstream delegating a `/56`, `Announce=no` sends the RA with the ULA prefix only (before and after the lease) while the segment still takes an address from the delegated `/64`; `Announce=yes` adds the delegated `/64` next to the ULA.
- The lab uplink answers no prefix-delegation request (30 s probe, no reply), so qualification needs its own delegating router.

## Scope

| In this slice | Later |
| --- | --- |
| `delegated` segments applied (prefix from a DHCPv6 WAN, subnet ID, the firewall's address in it) | PPPoE (still refused) |
| Router advertisements on `static` and `delegated` segments, modes `disabled`, `router`, `unmanaged` | Modes `managed`, `assisted`, `stateless` (need the DHCPv6 server, slices 2 and 3) |
| Advertisement options: router priority, router lifetime, prefix lifetimes, DNS servers and search domains, PREF64 | Advertisement intervals (networkd has no setting), extra advertised subnets beyond the segment's own |
| WAN DHCPv6 client: prefix only, without RA, send release, custom DUID | `dhcp6c` debug, authentication, raw options; prefix delegation from a non-DHCPv6 source |
| The DNS service answering over IPv6 on served LANs | DNS names for IPv6 clients (slice 2) |
| Filter rules router advertisements and delegated prefixes need | Takeover proposals with IPv6 on the LAN |

## Declaration

`schemaVersion` stays 1.

### `addressing.ipv6.ra`

Replaces `{enabled, managed, otherInfo}`:

| Field | Meaning |
| --- | --- |
| `mode` | `disabled`; `router` (advertises the router, no prefix for autoconfiguration); `unmanaged` (SLAAC); `managed`, `assisted`, `stateless` are accepted by the schema and refused at apply with `APPLY_UNSUPPORTED` until the DHCPv6 server exists. pfSense's names. |
| `priority` | `low`, `medium` (default), `high`. |
| `routerLifetime` | Seconds, 0 or 4–9000 (RFC 4861). Default 1800. 0 announces that this is not a default router. |
| `validLifetime`, `preferredLifetime` | Seconds for an advertised static prefix. Defaults 86400 and 14400 (pfSense's). Preferred must not exceed valid. Refused on `delegated` segments: networkd advertises a delegated prefix with the lifetimes of the upstream lease and has no setting to change them. |
| `dns` | `{servers, searchDomains}`. Omitted: the firewall's own DNS service, at the segment's stable address (below), when it serves this segment, with `dns.localDomain` and `dns.searchDomains`; otherwise nothing. `servers: []` advertises none. |
| `pref64` | Optional NAT64 prefix (`/32`, `/40`, `/48`, `/56`, `/64` or `/96`). |

Old saved versions still decode: `enabled: false` becomes `disabled`; `enabled: true` becomes `managed` when `managed` is set, `stateless` when only `otherInfo` is set, otherwise `unmanaged`. A write uses the new form only. Since advertisements could never be applied, this mapping only keeps old saved versions readable.

`router` and `unmanaged` on `static` without a `/64` that is not link-local, and on `slaac` or `dhcp6`, stay render-time warnings (`RA_PREFIX_UNSUPPORTED`), as today, so every old saved version still validates.

### `addressing.ipv6` on a `delegated` segment

`addresses` becomes allowed next to `from` and `subnetId`: static addresses the segment keeps whatever the upstream prefix is, normally one in a unique local `/64` (RFC 4193, `fd00::/8`). Its `/64` is advertised alongside the delegated prefix with the same mode. The firewall's DNS service and the advertised DNS server use this address, so clients keep a working resolver when the provider renumbers. Without one, the segment gets no IPv6 DNS from the firewall, and a warning says so.

### `addressing.ipv6` on a `dhcp6` segment

| Field | Meaning |
| --- | --- |
| `requestPrefixLength` | Exists: the size requested as a hint. |
| `prefixOnly` | Ask for a delegated prefix and no address (`UseAddress=no`). Default `false`. |
| `withoutRA` | Start DHCPv6 without waiting for the upstream's router advertisement (`WithoutRA=solicit`). Default `false`; PPP keeps its forced `solicit`. |
| `sendRelease` | Release the lease on shutdown. Default `false`: a release makes many providers hand out a new prefix, and the LAN's addresses change. |
| `duid` | Hex DUID for DHCPv6, overriding the identity the takeover captured. The DHCPv4 client identifier is not affected, as with pfSense's `dhcp6c` DUID. |

## Behaviour

### Rendering

- **`delegated`**: as today (`DHCPPrefixDelegation=yes`, `UplinkInterface`, `SubnetId`, `Assign=yes`), plus `Announce=` following the advertisement mode.
- **Advertisements** render into `[IPv6SendRA]`, one `[IPv6Prefix]` per static `/64`, `[IPv6PREF64Prefix]` when set. `router` sends no prefix (`Announce=no` on delegated segments) and sets neither flag; `unmanaged` announces the prefix with autoconfiguration on.
- **DNS:** `EmitDNS=yes` with the servers, or the segment's stable address when the firewall's DNS service answers; `EmitDomains=yes` with the domains.
- **WAN client:** the new fields add their `[DHCPv6]` lines; `duid` replaces the captured `DUIDType`/`DUIDRawData`.

### DNS service

A segment listed in `dns.segments` is served when it has a static IPv4 address, IPv6 `static` or `delegated`, or both; an IPv6-only LAN is served. On IPv4 the service listens on the static address, as today. DHCPv4 still needs the static IPv4 address; its rule does not change.

On a served segment with IPv6, Unbound also listens on the segment's first static IPv6 address that is not link-local (with `ip-freebind`, as for IPv4) and accepts every query that reaches that listener (`interface-action: <address> allow`), because a delegated prefix is not known when the configuration is written. Only the segment's link can reach the address: the filter accepts DNS on its interface alone.

### Filter

Added to the fixed input rules:

- Multicast listener queries and reports from link-local sources (`fe80::/10`), on every interface: they never leave the link, and RFC 4890 says not to drop them.
- DNS (UDP and TCP 53) from served segments over IPv6, matched by interface, like the existing DHCPv4 rule.

ICMPv6 errors about the firewall's own connections (packet too big, time exceeded, parameter problem) are already accepted as `ct state related`. Echo requests to the firewall stay the user's input policy, so the WAN does not answer pings by default, as in pfSense.

Router advertisements leave from the firewall (output is not filtered); clients' solicitations are accepted already. Forwarding stays the user's policy: zones match interfaces, so delegated addresses need no rule change when the prefix changes.

### Gates

The `APPLY_UNSUPPORTED` refusals for `delegated` and for advertisements are removed; `managed`, `assisted` and `stateless` are refused with "needs the DHCPv6 server". PPPoE stays refused.

## Testing

- Unit: decoding old `ra` objects; validation of every new field and its bounds; rendered `.network` sections for each mode, `delegated` with and without announcing, DNS defaulting, WAN client options; filter rules; Unbound listener lines; the gates.
- Debian lab qualification, provisioned by this slice on the Proxmox lab host: an isolated VLAN between VM 114's WAN and a small delegating router (Kea DHCPv6 handing out a `/56`, with a route sending it to VM 114). VM 114 takes the prefix, numbers the LAN from subnet 0, advertises it; the LAN client (VM 117) autoconfigures an address, resolves through the firewall over IPv6, and reaches the delegating router's address through the policy. Then the delegating router changes the prefix, and the client moves to the new one with the old one deprecated.

## Gaps against pfSense, stated

- Advertisement intervals (pfSense's min/max) are not configurable: networkd has no setting.
- `dhcp6c`'s debug, authentication and raw option settings are not offered.
- Extra advertised subnets ("RA Subnets") wait for a use case.
- High availability is out of scope for the MVP.
