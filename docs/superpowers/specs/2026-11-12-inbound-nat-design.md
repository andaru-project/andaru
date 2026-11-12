# Inbound NAT: port forwards and 1:1 NAT

## Status and intent

[Issue #8](https://github.com/andaru-project/andaru/issues/8). Owner decisions, 2026-11-12:

- **Scope:** the MVP's inbound NAT is pfSense's set: port forwards and 1:1 NAT. It sits on top of the automatic outbound NAT that shipped with the packet filter (#34).
- **1:1 external addresses:** this slice uses addresses that already reach the firewall. That means an extra address on the WAN segment (already supported), or a block the provider routes to the firewall. Proxy ARP, which answers for an address the firewall does not hold, comes with virtual IPs ([#37](https://github.com/andaru-project/andaru/issues/37)).

This is not implemented behaviour yet. Contracts stay provisional until Andaru 1.0.

### Authorities

- [#6](https://github.com/andaru-project/andaru/issues/6) sketches the object model: `portForward[]` with `ingressZones`, `protocol`, `from`, `to` (an address object or `{"segmentAddress": ...}`), `ports`, `target` (a host or the firewall), `targetPort`, `filterRule: auto | none`, `reflection` and `log`; `oneToOne[]` with `zone`, `external`, `internal` and `reflection`.
- The [packet filter](2026-11-12-packet-filter-design.md) supplies table `inet andaru`, zone and address sets, ordered policy lists, `nat.outbound`, the management segment, and the rule-ID comments that counters key on (#15).
- **Research:** pfSense master `9363ac5b` was read for the port-forward and 1:1 fields, their validation and the pf rules they generate. Every nftables mapping below passed `nft -c` (nftables 1.1.3, Debian 13). A network-namespace lab ran the port forward, the bound filter rule, Pure-NAT reflection and 1:1 in both directions:
  - a WAN client reached the server, which saw the client's real address;
  - a direct connection to the inside address was dropped;
  - a hairpin connection worked, with the server seeing the firewall as the source;
  - a LAN client connecting to the firewall's own LAN address on the forwarded port was not redirected;
  - the 1:1 host was reachable from the WAN and left through its external address.

## Scope

| In this slice | Later |
| --- | --- |
| IPv4 port forwards, including port ranges, a firewall target, exclusions ("no redirect") and logging | IPv6 port forwards (NAT66), NPt |
| An associated filter rule, kept in step with the forward | pfSense's "add unassociated rule" and "pass" (write a policy rule instead) |
| IPv4 1:1 NAT for single addresses and subnets, with a destination restriction and exclusions | Proxy ARP and other virtual IPs (#37) |
| Reflection in pfSense's Pure-NAT mode, per rule, off by default | NAT+Proxy reflection, a system-wide reflection default |
| Matching by source address objects, with negation | Source ports, port aliases on the target side, HA sync |

## Declaration

Both lists live under `nat` next to `outbound`. They are ordered: position is evaluation order, and the first matching translation wins, as in pf. Objects follow the saved-configuration rules:
- a server-generated `id`;
- a unique `name`;
- an optional `description`;
- references by ID;
- references that must resolve, with deleting a referenced object refused.

`schemaVersion` stays 1, and a declaration without either list realizes exactly as today.

### `nat.portForwards` (prefix `pfw`)

| Field | Meaning |
| --- | --- |
| `enabled` | A disabled forward stays in place and is not rendered. |
| `ingressZones` | Zone IDs whose interfaces the connection arrives on; at least one. |
| `protocol` | `tcp`, `udp` or `tcp-udp`. |
| `from` | Address IDs. Empty means any source. `fromNegated: true` matches every source except these. |
| `to` | The destination as the client sees it. Either `{"segmentAddress": <segment ID>}`, the segment's current IPv4 address (pfSense's "WAN address"; it follows DHCP and PPPoE changes without a re-render; the segment's zone must be one of `ingressZones`), or `{"address": <address ID>}` (an IPv4 host, network, range or group). Omitted means any destination, which is how DNS interception is written (`target: {"firewall": true}`). |
| `ports` | One port, or one `first-last` range, 1–65535. |
| `noRedirect` | An exclusion: matching connections are left untranslated and later forwards are not consulted. An exclusion has no target, filter rule or reflection. |
| `target` | `{"address": "<IPv4>"}` for a host, or `{"firewall": true}` to redirect to the firewall itself (for example, DNS interception). Required unless `noRedirect`. |
| `targetPort` | First target port. A range is shifted by the same offset, pfSense's rule; the last port must stay ≤ 65535. Default: the original ports. |
| `filterRule` | `auto` (default) or `none`. See below. |
| `reflection` | Pure-NAT reflection for clients on internal segments. Default `false`. |
| `log` | Log each new translated connection to the kernel log. Default `false`. |

### `nat.oneToOne` (prefix `oto`)

| Field | Meaning |
| --- | --- |
| `enabled` | As above. |
| `zone` | The zone whose uplink interfaces carry the external address. |
| `external` | An IPv4 address or prefix (host bits zero). |
| `internal` | An IPv4 address or prefix with the same length as `external`. |
| `destination` | Address IDs. Empty means any. When set, only connections to or from these peers are translated. |
| `noBinat` | An exclusion: `internal` is the hosts left out and `external` is absent. It applies to the entries after it, in both directions: their outbound `snat` skips those hosts, and their inbound `dnat` skips the external addresses those hosts would map to. Excluded hosts fall through to the ordinary outbound NAT, as in pfSense. |
| `reflection` | Pure-NAT reflection for internal clients connecting to `external`. Default `false`. |

### Validation

These copy pfSense's rules.

**Port forwards:**
- Two enabled forwards that are not exclusions may not overlap: same `from`, same `to`, a shared ingress zone and protocol, and overlapping port ranges. This is pfSense's rule (`firewall_nat.inc:513-521`); forwards that differ in source or destination are allowed, and their order decides.
- `reflection` is refused when `to` is omitted: a catch-all destination would capture every internal connection to that port.
- `target` must be IPv4, like everything in this slice.
- A forward that redirects to the firewall may not use `reflection`.

**1:1:**
- `external` may not contain any of the firewall's static addresses, since that would take over the firewall's own traffic. Addresses learned by DHCP or PPPoE are only known when the configuration is rendered: an `external` containing one is a render error, `ONE_TO_ONE_OWN_ADDRESS`, so an apply or the boot re-render refuses it.
- `external` may not overlap another enabled 1:1 entry.
- `internal` may not contain the firewall's own address on that segment.

## Behaviour

**Requires the filter.** Like rules and outbound NAT, inbound NAT takes effect only with a management segment, so `MANAGEMENT_REQUIRED` covers both new lists.

### Rendering (table `inet andaru`)

Every rendered line carries the object's ID as its comment, so counters (#15) and the GUI can tie a packet back to a rule.

- **`prerouting`** is a new `type nat hook prerouting priority dstnat` chain. It holds:
  1. port forwards, in order: an exclusion renders as `accept`, and a forward as `dnat ip to` or `redirect to`. `to: segmentAddress` matches the segment's interface and `fib daddr . iif type local`, so it fires only on that segment's own address. A shifted range renders a per-port `dnat ip addr . port to … map`;
  2. then the 1:1 `dnat ip [prefix] to ip daddr map` lines.

  Port forwards come first, so a forward on a 1:1 external address wins for its ports. That is my call; pf evaluates `rdr` and `binat` in the order the ruleset lists them, and pfSense emits `binat` first.
- **`postrouting`** puts the 1:1 `snat … map` lines before the existing outbound masquerade, which the lab showed is required. When any reflection is on, it also adds one masquerade line per served IPv4 segment: `oifname <segment> ct status dnat ip saddr <its subnet> masquerade`. Replies to a hairpin connection then come back through the firewall.
- **Reflection** adds a second `dnat` from the zones that are not uplinks:
  - with a static `to`, it matches that address;
  - with `to: segmentAddress`, it matches any firewall address except the static addresses of the other segments (a set). A client asking the firewall's own LAN address is therefore not redirected. Shortcut: on a host with a second uplink whose address is learned by DHCP or PPPoE, reflection also fires for that uplink's address; nft cannot name another interface's dynamic address. Multi-WAN is not in the MVP.
- **Associated filter rule (`filterRule: auto`):** rendered right after the fixed rules and before the policy:
  - in `forward` for a host target, in `input` for a firewall target;
  - it matches `ct status dnat`, the original destination port (and the original address, when `to` is an address), the translated address and port, the ingress zones and `from`;
  - it accepts only connections that came through this forward, so the inside host is not exposed directly. The lab confirmed this.

  It is derived, not stored, so it can never drift from the forward. The GUI shows it under the forward. With `none`, the operator writes a policy rule.

  Reflected connections are judged by the policy of the zone they arrive from, as in pfSense.

### 1:1 addresses

Andaru only translates: the external address must already reach the firewall. A warning, `PROXY_ARP_REQUIRED`, flags an `external` address that falls inside an uplink segment's static subnet without being one of that segment's addresses. Nothing on the shared subnet would answer ARP for it until #37 exists. Add the address to the segment instead.

### Status

The counters that #15 defines for rules apply to forwards and 1:1 entries as well, through the same ID comments. No new status endpoint is part of this slice.

## API and CLI

`nat.portForwards` and `nat.oneToOne` are ordered collections that behave exactly like `policy.input` and `policy.forward`:
- `GET` lists the entries with the list's version;
- `POST` creates an entry, with an optional `before` to place it;
- `GET`, `PUT` and `DELETE` work on single entries;
- `PUT …/order` reorders the whole list;
- the change batch accepts every operation.

The paths are `/api/v1/config/nat/port-forwards` and `/api/v1/config/nat/one-to-one`. `andaru ctl config` reaches them as it does the policy lists.

## Testing

- **Unit:** validation of every field and rule above; rendered tables (goldens) covering every forward kind, exclusions, shifted ranges, the firewall target, both reflection forms, 1:1 for an address and a prefix with a destination restriction, `noBinat`, the associated rule in `forward` and `input`, and the `PROXY_ARP_REQUIRED` warning.
- **Integration:** `nft -c` and the namespace filter test load every golden table.
- **Debian lab:** VM 114 is the firewall, VM 117 the LAN server, and the PD router (LXC 118, whose management NIC sits on the lab network) the WAN client. The lab covers:
  1. a forward to VM 117;
  2. a direct connection to its address, which must be dropped;
  3. a shifted range;
  4. a redirect to the firewall's own DNS service;
  5. reflection from the LAN;
  6. 1:1 with a second static WAN address, in both directions;
  7. a reboot.

## Gaps against pfSense, stated

- NAT+Proxy reflection is not offered; Pure NAT and split DNS (DNS overrides, once they exist) cover the use.
- "Add unassociated rule" and "pass" are not offered; `filterRule: none` plus a policy rule covers both.
- Source-port matching, port aliases on the target and HA sync are not offered.
- Proxy ARP waits for #37.
