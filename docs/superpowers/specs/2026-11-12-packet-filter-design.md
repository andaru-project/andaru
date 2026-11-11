# Packet filter and outbound NAT

## Status and intent

Requested in conversation: the MVP needs firewall rule creation and management ([issue #1](https://github.com/andaru-project/andaru/issues/1), journey steps 5, 7 and 8). Owner decisions in conversation, 2026-11-12:

- **First slice:** the stateful filter with automatic outbound NAT. Port forwards, 1:1 NAT and the `hybrid`/`manual` outbound modes come in the next slice.
- **IPv6:** one policy for both families; a rule's zones and objects match IPv4 and IPv6 alike, and ICMPv6 neighbour discovery is always accepted.
- **Anti-lockout:** a `management` setting now, whose SSH access is a fixed rule no edit removes; the confirm-from-a-new-session timer stays the backstop.
- **Realization:** network and filter are one generation. The nft ruleset is one more rendered file, applied, confirmed, rolled back and booted with the networkd files.

Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

### Authorities

- [Issue #1](https://github.com/andaru-project/andaru/issues/1): Andaru writes nft directly, owns named tables only and never flushes rules of Podman, libvirt or Docker; all NAT is Andaru's nft, not networkd's `IPMasquerade=`.
- [Issue #6](https://github.com/andaru-project/andaru/issues/6): the object model (`objects`, `policy.input`/`policy.forward`, fixed defaults, starter rules, `nat.outbound`, `management`, reference rules).
- [Issue #7](https://github.com/andaru-project/andaru/issues/7) (IPv6) and [#8](https://github.com/andaru-project/andaru/issues/8) (NAT boundary).
- [`docs/research/web-ui-foundation.md`](../../research/web-ui-foundation.md), "Firewall Rules": execution order is authoritative; reordering is a saved edit with version checks.
- [Saved network configuration](2026-11-10-saved-network-configuration-design.md): identities, versions, canonical form ("do not sort ordered policy/NAT rules").
- [Network takeover and confirmed apply](2026-11-11-network-takeover-apply-design.md), [interactive takeover](2026-11-11-interactive-takeover-design.md), [ifupdown takeover](2026-11-11-ifupdown-takeover-design.md): generations, timer, confirmation, boot.

## Scope

| In this slice | Later |
| --- | --- |
| `objects.addresses` (host, network, range, group), `objects.services` (tcp, udp, icmp, icmpv6, group) | `fqdn` addresses, schedules |
| `policy.input`, `policy.forward`: ordered rules with accept, drop, reject, log | Counters, a log viewer (#15), per-rule statistics |
| `nat.outbound` mode `automatic` | `hybrid`, `manual`, `portForward`, `oneToOne`, reflection, NPt (#8) |
| `management` with `segment` and `allowedSources`; SSH always accepted there | Web UI listener, port and TLS (#12), first-boot addressing (#14) |
| Starter rules written by a takeover | Rule templates |
| Forwarding enabled together with the filter | Routing protocols, policy routing |

## Declaration

All new objects follow the saved-configuration rules: immutable server-generated `id`, mutable unique `name`, optional `description`, references by ID, dangling references rejected, deletion of a referenced object refused.

### Objects

- **Address** (`objects.addresses`, prefix `adr`): `kind` and its value.
  - `host`: `address`, one IPv4 or IPv6 address.
  - `network`: `prefix`, one IPv4 or IPv6 prefix with host bits zero.
  - `range`: `first`, `last`, same family, `first ≤ last`.
  - `group`: `members`, address IDs that are not groups (one level), at least one.
- **Service** (`objects.services`, prefix `svc`):
  - `protocol` `tcp` or `udp` with `ports`: a list of ports or `first-last` ranges, 1–65535, at least one.
  - `protocol` `icmp` or `icmpv6`, optionally `types`: ICMP type names nft accepts (`echo-request`, …).
  - `group`: `members`, service IDs that are not groups, at least one.

### Policy

`policy.input` (traffic to the firewall) and `policy.forward` (through it) are ordered lists; position is execution order and is preserved in canonical form. A rule (prefix `rul`):

| Field | Meaning |
| --- | --- |
| `enabled` | Disabled rules stay in place and are not rendered. |
| `action` | `accept`, `drop` or `reject`. |
| `from`, `to` | Lists of zone and address IDs; empty means any. On `input`, `to` must be empty (the firewall is the destination). |
| `services` | Service IDs; empty means any protocol. |
| `log` | Log matches to the kernel log before the verdict. |

A rule matches when the packet's interface zone or address matches any `from` entry, likewise for `to`, and any service matches. A rule whose addresses are all of one family matches only that family; a rule with no addresses matches both.

### Outbound NAT

`nat.outbound` is one object: `{"mode": "automatic"}` or `{"mode": "none"}`. `automatic` source-NATs (masquerades) everything forwarded out of an **uplink segment**: a segment with a static IPv4 gateway, DHCPv4, or a PPPoE link. IPv6 is never translated.

### Management

`management` is one object: `segment` (a segment ID or null) and `allowedSources` (address IDs; empty means any). When `segment` is set, SSH (22/tcp) arriving on that segment from an allowed source is always accepted by a fixed rule.

### API

The existing configuration API, socket and authorization:

- `GET/POST /api/v1/config/addresses`, `/services`, and `GET/PUT/DELETE …/{id}`, as for links, segments and zones.
- `GET /api/v1/config/policy/{input|forward}` returns the ordered rules and the list's version. `POST` creates a rule; optional `before` (a rule ID of the same list) places it, otherwise it is appended. `GET/PUT/DELETE …/{chain}/{id}` edit one rule with its own version.
- `PUT /api/v1/config/policy/{chain}/order` with `If-Match` on the list version and the full ID order. It must be a permutation of the current IDs; otherwise `422`. A stale version is `412`.
- `GET/PUT /api/v1/config/nat/outbound` and `/api/v1/config/management`, version-checked like objects.
- The change-batch endpoint accepts all of the above, so the assistant writes a takeover in one batch.

CLI: the generic `andaru ctl config` verbs gain the new collections, plus `andaru ctl config order input|forward --version V <id>...`.

## Rendering

The renderer emits one more file, `nftables: inet andaru`, beside the networkd files; previews, drafts and applies show it like the others. It is one script that replaces the table atomically:

```
table inet andaru {}
delete table inet andaru
table inet andaru {
  comment "<generation-independent sha256 of the body>"
  sets …
  chains …
}
```

`nft -f` runs the script as one transaction: the previous table stays until the new one is complete, and no other table is touched.

### Chains and fixed rules

- `input`: `type filter hook input priority filter; policy drop;`
- `forward`: `type filter hook forward priority filter; policy drop;`
- `postrouting`: `type nat hook postrouting priority srcnat;` masquerade only.
- No `output` chain: the firewall's own traffic is not filtered.

Fixed rules come first in each filter chain, in this order, and are not editable:

1. `ct state established,related accept`, `ct state invalid drop`.
2. `input` only: `iif lo accept`.
3. `input` only: ICMPv6 neighbour solicitation and advertisement, router solicitation and advertisement accepted. They are link-local and never forwarded.
4. `input` only: DHCPv6 replies, `ip6 saddr fe80::/10 udp sport 547 udp dport 546 accept`. DHCPv4 replies reach networkd through its raw socket before netfilter and need no rule.
5. `input` only: the management rule, `iifname "<management segment's interface>" tcp dport 22 accept`, with `ip saddr @mgmt_4` / `ip6 saddr @mgmt_6` added when `allowedSources` is set.

Then the operator's rules in list order, then the chain's `drop` policy.

### Objects in nft

- **Zone:** a named set `zone_<id>` of the interface names of its segments' links (`type ifname`), matched with `iifname`/`oifname`. Names, not indexes, so a PPP or VLAN interface that appears later is matched.
- **Address:** for each family it has values in, a named interval set `adr_<id>_4` (`type ipv4_addr; flags interval`) or `_6`. A group's sets are the union of its members'.
- **Service:** inline matches; a rule with several services renders one line per service.
- **Rule:** each rendered line ends with `comment "<rule id>"`; `log` adds `log prefix "andaru <rule id> "` before the verdict; `reject` renders `reject with tcp reset` for TCP services and `reject` otherwise.

### Forwarding

A generation that carries the table renders `IPv4Forwarding=yes` and `IPv6Forwarding=yes` in each `.network` file of a segment that is in any zone; without the table it renders none. The current apply refusal of forwarding, "waits for the firewall", is removed for everything this slice covers; PPPoE, delegated prefixes and sending router advertisements stay refused as before.

### Findings

- `TOOL_MISSING` (error): `/usr/sbin/nft` is absent. Cloud images ship without the `nftables` package.
- Rendering never fails on a rule; an impossible rule (a `to` on `input`, an empty group after deletions, a family mismatch between `from` and `to` addresses) is a validation error at save, with the rule's JSON pointer.

## Apply, rollback, boot, verification

The ordering rule is **tighten first, loosen last**.

- **Activate:** after the timer is armed, `realize` loads the generation's script with `nft -f` from the generation directory, then places the networkd files and reloads. A load error changes nothing (the transaction aborts); activation fails before any network file moves, reporting nft's message and the rule ID read from the failing line's comment.
- **Verification** adds one check: `nft -j list table inet andaru` reports the table comment of this generation's script. The existing checks (files bound, addresses, default route) are unchanged, and confirmation from a new session proves the management path.
- **Rollback / failed activation to a generation:** that generation's script is loaded first, then its files. **To the baseline** (no Andaru table): networkd files first, which turns forwarding off, then `nft delete table inet andaru`.
- **Boot:** `andaru realize boot` loads the confirmed generation's script before placing its files; the boot unit already runs before `network-pre.target`, so no interface comes up unfiltered. If the script does not load, boot falls back to the last-known-good generation as a whole; with nothing usable, to the baseline (no table, no forwarding), as today.
- **Engine:** never runs `nft`; its sandbox has no capabilities. Only `realize` (root, transient unit) loads rules.

### New refusal: `FOREIGN_FILTER`

An apply that carries the table is refused while a service would undo or override it, path naming the unit:

- `nftables.service` enabled: Debian's `/etc/nftables.conf` begins with `flush ruleset`.
- `firewalld.service` or `ufw.service` enabled.

Other tables (Podman's, libvirt's) are allowed and never touched.

## The takeover assistant

A takeover proposal (netplan or ifupdown) additionally proposes:

- **Starter rules**, for each zone that has no uplink segment (the proposal's `lan`): `forward`: accept from that zone to any. `input`: accept ICMP and ICMPv6 echo requests and DNS (53/udp, 53/tcp) from that zone. Each is an ordinary rule the operator can edit or delete.
- `nat.outbound`: `automatic` when any segment is an uplink.
- **Management:** a new question, "Management segment", pre-filled with the segment whose interface carries the local address of `SSH_CONNECTION` (the CLI passes it to the proposal); on the serial console, the first non-uplink segment, else the uplink. Choosing a segment in a zone with an uplink adds a review warning that SSH is reachable from that network, with the hint to set `allowedSources`.
- The review shows the rule table (position, action, from, to, services) next to the files.

## Acceptance and evidence

Unit tests, in CI on both architectures:

1. **Validation:** every object and rule field, references, deletion refusals, `before`, the order permutation check, `412` on a stale list version.
2. **Rendering:** golden scripts for a two-zone host with starter rules, every object kind, groups, disabled and logged rules, and management with and without `allowedSources`; `nft -c -f` accepts every golden script (CI installs `nftables`).
3. **Realize, against a temporary tree with a scripted `nft`:** activation loads before files; a load failure moves no file; rollback to a generation loads first, to the baseline deletes last; boot loads before placing; a script that fails at boot falls back to last-known-good.
4. **Gates:** `FOREIGN_FILTER` per unit; `TOOL_MISSING`; forwarding rendered only with the table.

Root integration test (GitHub-hosted runner, `sudo`): in a network namespace with veth pairs for `wan`, `lan` and a management segment, load a rendered script and assert with real packets that unsolicited WAN ingress is dropped, LAN traffic is forwarded and masqueraded, management SSH is accepted, a `drop` rule placed above an `accept` wins, and an unrelated table in the namespace is untouched.

Lab: VM 114 gains a second NIC on an isolated VLAN of `vmbr0` (tag 4093); a clone of VM 117 is the LAN client there.

5. Takeover through the assistant: starter rules and outbound NAT; the client reaches the internet through masquerade; a probe from the lab network to the WAN address is dropped; SSH works on the management segment.
6. Add, move and disable a rule from the CLI; apply; confirm; the client's traffic follows the order.
7. An apply not confirmed: the timer restores the previous filter.
8. Reboot: `nft list table inet andaru` is loaded before the interfaces come up (journal order); the client works again.
9. `nftables.service` enabled: `FOREIGN_FILTER`.

## Limits the owner accepts with this slice

- **The firewall's own outbound traffic is unfiltered.**
- **No port forwards or 1:1 NAT;** inbound services on the LAN are unreachable from the WAN until the next slice.
- **IPv6 is routed, not translated;** LAN clients need delegated prefixes (later) to reach the IPv6 internet through Andaru.
- **SSH on port 22 is the only management service;** a moved SSH port is not followed.
- **Logs go to the kernel log** only; there is no viewer yet.
