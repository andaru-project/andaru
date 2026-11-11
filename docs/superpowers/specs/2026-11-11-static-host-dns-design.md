# Static DNS servers for the host

## Status and intent

Requested in conversation: a host whose network is configured statically, DNS servers included, must be something Andaru can describe, apply and take over. Today the saved configuration has no place for DNS servers, so the takeover refuses every netplan host that sets them. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

This builds on [network takeover and confirmed apply](2026-11-11-network-takeover-apply-design.md) ([PR #28](https://github.com/andaru-project/andaru/pull/28)) and must land after it.

### Authorities

- [Issue #6](https://github.com/andaru-project/andaru/issues/6): the declaration schema and its validation rules.
- [Issue #10](https://github.com/andaru-project/andaru/issues/10): which DNS service Andaru runs **for its clients**. Not decided, and not touched here.
- Takeover spec, "Host DNS": the host keeps DHCP-provided DNS until the DNS area exists. This slice adds the static counterpart.

## What it is, and what it is not

**It is** the list of DNS servers the firewall host itself asks, as netplan's `nameservers:` states it today.

**It is not** a DNS service for LAN clients, a forwarder, or DHCP option 6. Those belong to #10. When that area exists it may take over the host's resolver; this field then becomes its upstream list or is migrated. `shortcut: host resolver only, on one segment; revisit when the DNS area owns the resolver.`

## Design

### Declaration

A segment's `addressing` gains an optional `dns` object:

```json
"addressing": {
  "ipv4": {"mode": "static", "addresses": ["192.0.2.10/24"], "gateway": "192.0.2.1"},
  "ipv6": {"mode": "none"},
  "dns": {"servers": ["192.0.2.53", "2001:db8::53"], "searchDomains": ["example.org"]}
}
```

- `servers`: 1 to 8 addresses, in the order they are tried. Required when `dns` is present.
- `searchDomains`: 0 to 8 names, in order. Optional.
- **At most one segment may declare `dns`.** systemd-resolved sends ordinary lookups to every link that has servers, so two declaring segments would split the host's lookups between them. One segment keeps the meaning plain: these are the host's servers.
- The field is optional and additive: every saved version stays valid and `schemaVersion` stays 1. A binary older than this slice cannot read a version that uses `dns`; that is accepted before 1.0.

Validation, as `VALIDATION_FAILED` field errors at the field's own path:

- A server must parse as an IP address without a zone. Refused: unspecified, loopback, multicast, link-local and IPv4-mapped IPv6 addresses.
- Servers are stored in canonical text form, and duplicates are detected after parsing (`2001:db8::53` equals `2001:0DB8::53`). Order is kept, never sorted.
- A search domain is one or more labels of lower-case ASCII letters, digits and hyphens, each 1 to 63 characters, not starting or ending with a hyphen, joined by dots, at most 253 characters in total, with no trailing dot. Internationalised names are written in their `xn--` form. Duplicates are refused; order is kept.
- `dns` is refused on a segment where both families are `none`, and on a second segment.

### Rendering

In the declaring segment's `.network` file, after the address and gateway lines of `[Network]`:

```
DNS=192.0.2.53
DNS=2001:db8::53
Domains=example.org example.net
```

One `DNS=` line per server in declared order; one `Domains=` line, space-separated, omitted when there are none.

- **On the declaring segment, declared servers replace offered ones:** its `[DHCPv4]`, `[DHCPv6]` and `[IPv6AcceptRA]` sections render `UseDNS=no`. Other segments are unchanged (`UseDNS=yes`), so a second DHCP uplink still contributes its servers.
- `UseDomains=no` stays everywhere: search domains come only from the declaration.
- A segment whose link cannot be realised renders no addressing today, and so no `DNS=` either.

### Who actually reads it

systemd-networkd does not resolve names; it hands `DNS=` to systemd-resolved. So the declaration takes effect only where systemd-resolved manages `/etc/resolv.conf`, which is the case on Debian cloud images.

- **"Resolved is in charge"** means: `/etc/resolv.conf`, with every symbolic link followed, is a file under `/run/systemd/resolve/`.
- New preview finding when a segment declares `dns` and resolved is not in charge: **`DNS_NOT_APPLIED`**, a warning, on `/segments/N/addressing/dns`: "This host's `/etc/resolv.conf` is not managed by systemd-resolved, so these servers are saved but not used." A warning, not an error: the host keeps resolving with whatever its `resolv.conf` says, which is how a default Debian install works.

Andaru does not install or enable systemd-resolved and does not rewrite `/etc/resolv.conf` in this slice.

### Takeover

A netplan-generated file that sets `DNS=` or `Domains=` is refused today. The refusal becomes conditional, per baseline file:

1. Find the interfaces networkd has bound to that file (the same lookup `WOULD_UNCONFIGURE` uses). The netplan file name is netplan's own id, not the interface name, so the name is never parsed.
2. For **every** such interface, the generation must contain `50-andaru-<interface>.network` with at least one `DNS=` line.
3. If the file is bound to no interface, or any bound interface fails rule 2, the existing `TAKEOVER_UNSUPPORTED_BASELINE` finding stays, with a message that says to declare `dns` on that interface's segment.

The baseline's values are not compared with the declared ones, as with addresses: you may be changing them on purpose.

### Verification at apply

For a generation with a `DNS=` line, when resolved is in charge (checked again at activation, by the privileged command):

- One `resolvectl dns` call lists the servers per link. Within the existing 30 seconds, the interface bound to the file must report **exactly** the file's `DNS=` servers, in order, compared as parsed addresses. A subset or a superset is a failure: leftover DHCP servers mean the change did not take effect.
- If `resolvectl` cannot be run or resolved does not answer, that is a failure too, and the previous files return.

When resolved is not in charge the check is skipped, matching the warning above. Nothing else in apply, confirm, rollback or boot changes: DNS travels in the same `.network` file.

## Acceptance and evidence

Unit tests, in CI on both architectures:

1. **Schema and validation:** one row per rule above, each asserting the error's field path, including `dns` on a second segment, an IPv4-mapped server, a zoned server, the same IPv6 server written two ways, and a domain with upper case, a trailing dot, a leading `~`, a space or a newline. A saved version without `dns` reads back unchanged; servers and domains read back in declared order.
2. **Rendering:** golden files for a static segment with `dns` and for a DHCP + SLAAC segment with `dns` (showing `UseDNS=no` in each section); `dns` on an unrealisable link renders nothing; the existing goldens are unchanged.
3. **Finding:** `DNS_NOT_APPLIED` for `/etc/resolv.conf` as a plain file and as a dangling link; absent for a relative link into `/run/systemd/resolve/` and when no segment declares `dns`.
4. **Gate:** with a netplan file whose id differs from the interface name: declared on that interface → accepted; declared on another interface only → refused; file bound to two interfaces with one declared → refused; file bound to none → refused; not declared → refused.
5. **Verification:** a parser test on captured `resolvectl dns` output (a `Global:` line, a link with no servers, IPv6 forms). Activation fails and restores when the link reports a subset, a superset, another order, or `resolvectl` fails; passes on an exact match; the check is skipped when resolved is not in charge or the generation has no `DNS=`.

On the lab VM, starting from snapshot `base-netplan-20261111` and reset to it afterwards. "Servers are X" means `resolvectl dns <interface>` prints exactly X:

6. **DHCP uplink with `dns` declared** (the base is DHCP): takeover with a continuous ping, 0 lost; servers are the declared ones, not the DHCP-offered ones; a name resolves; not confirmed → servers are the DHCP-offered ones again.
7. Reconfigure the VM's netplan to its address, gateway and two DNS servers **statically**, reboot, shut down and snapshot that as the static base.
8. Without `dns` declared, `--takeover` is refused with the baseline finding.
9. With address, gateway and `dns` declared (the two servers in the opposite order): takeover with a continuous ping, 0 lost; servers are the declared ones in the declared order; a name resolves.
10. Not confirmed → servers are netplan's, in netplan's order; a name resolves.
11. Confirmed, then rebooted → same address; servers are the declared ones; a name resolves.

Documents updated in the same change: the three sentences of the takeover spec that say static-DNS hosts are refused, the findings table and takeover section of `docs/dev/core-api.md`, and the served schema.

## Limits the owner accepts with this slice

- **Only the host's own lookups.** LAN clients get nothing from this.
- **One segment only.** There is no per-uplink or per-domain DNS.
- **Effective only with systemd-resolved.** Elsewhere the servers are saved and a warning says they are not used.
- **Wrong servers are not detected.** Verification checks that resolved has the declared servers, not that they answer. Declaring unreachable servers leaves the host without name resolution until the next apply.
- **No DNS-over-TLS, no DNSSEC setting.**
- **Baseline values are not compared** with the declared ones at takeover.
