# The install seed

The ISO's installer asks for the first settings and writes them to the installed disk as a seed:
`/var/andaru-install/install-seed.json`, mode 0600, in the root filesystem's `/var`. On the first Andaru boot,
`andaru realize boot` moves it to `/var/lib/andaru/install-seed.json`; slice 3 applies it as the first configuration.
No answer is written to `/boot` or passes through Ignition.

The installer checks each answer on its own, as Andaru's validator (`internal/config/validate.go`) would, so a seed it
writes does not fail on a value first boot would refuse. The checks that relate answers to each other, such as a DHCP
pool inside the management subnet or a gateway inside the WAN subnet, are the reader's (slice 3), which also checks each
`addressing` object against Andaru's configuration schema and its validator. The fixtures in
[`image/iso/testdata/`](../../image/iso/testdata/) are the installer's expected output, byte for byte, and
`internal/config` tests their `addressing` objects against the schema.

## Version 1

```json
{
  "version": 1,
  "management": {
    "mac": "52:54:00:aa:bb:01",
    "addressing": {
      "ipv4": {"mode": "static", "addresses": ["192.168.1.1/24"]},
      "ipv6": {"mode": "delegated", "from": "@wan", "subnetId": "0", "ra": {"mode": "unmanaged"}}
    },
    "serve": {"dhcp": {"start": "192.168.1.100", "end": "192.168.1.199"}, "dns": true}
  },
  "wan": {
    "mac": "52:54:00:aa:bb:02",
    "addressing": {"ipv4": {"mode": "dhcp"}, "ipv6": {"mode": "dhcp6", "requestPrefixLength": 56}}
  },
  "sshKeys": [{"type": "ssh-ed25519", "blob": "AAAAC3NzaC1lZDI1NTE5AAAA…", "comment": "admin@laptop"}]
}
```

The installer writes the seed with `jq`, two-space indented. Key order is the fixtures': `mode` first, then the keys in
the order the schema lists them, with `ra` last.

| Field | Form |
| --- | --- |
| `version` | `1`. |
| `management` | Required: the port Andaru is managed through. |
| `management.mac`, `wan.mac` | The NIC's permanent address (`permaddr` in `ip -j -d link`, or else `address`), lowercased: `^([0-9a-f]{2}:){5}[0-9a-f]{2}$`. Andaru's renderer matches links by the same rule. The two differ. |
| `*.addressing` | Andaru's `$defs/addressing` (`internal/config/schema.json`), restricted as below. |
| `management.serve` | Present only with a `static` IPv4 on management. `dhcp`, when present, is an IPv4 pool, `start` and `end`, served on the management subnet. `dns` is a boolean: whether Andaru answers DNS on management. |
| `wan` | Absent when the box has no WAN yet. |
| `sshKeys` | Zero or one key, as `type`, `blob` and `comment` (an empty string when the key has none). |

### Addressing

An **IPv4 address** has four decimal octets with no leading zero (Go's parser refuses them), and is not `0.0.0.0`,
in `127.0.0.0/8`, in `224.0.0.0/4` or `255.255.255.255`. An **IPv6 address** is not `::`, `::1`, in `ff00::/8`, or
IPv4-mapped (`::ffff:0:0/96`), and carries no zone. These are `validUnicast`'s rules.

- **IPv4** `mode`: `none`, `dhcp` or `static`. `static` has one address in `addresses`, written `address/len` with
  `len` from 1 to 32; with `len` 30 or less, it is neither the subnet's network address nor its broadcast address
  (`validLocal`). On the WAN, `static` also needs a `gateway`. `ppp` waits for PPPoE (#77).
- **IPv6** `mode`: `none`, `static`, `slaac`, `dhcp6`, or, on management only, `delegated`.
  - `static` has one address in `addresses`, written `address/len` with `len` from 1 to 128, and not link-local
    (`fe80::/10`). On the WAN it also needs a `gateway`, which may be link-local.
  - `dhcp6` may carry `requestPrefixLength` (1 to 64), to ask for a delegated prefix, and the advanced options:
    `prefixOnly`, only with `requestPrefixLength` (the WAN then takes no address of its own, only the prefix);
    `withoutRA` and `sendRelease` (booleans); and `duid`, colon-separated hex bytes whose first two give the DUID type,
    1 to 4, followed by at least 7, 5 or 3 bytes for types 1 to 3 and exactly 16 for type 4 (`validDUID`).
  - `delegated` takes subnet `subnetId` (1 to 4 hex digits) of the prefix the WAN requests, so the management network
    follows that prefix when the ISP changes it. `from` is `"@wan"`, the label slice 3 gives the WAN segment it creates
    in the same change, which Andaru resolves to the segment's ID. It needs a WAN with `dhcp6` and a
    `requestPrefixLength`, and `subnetId` must be below 2^(64 − `requestPrefixLength`).
- **Router advertisements**: on management with a `static` or `delegated` IPv6, `ipv6.ra` is always written, as
  `{"mode": M}`; an absent `ra` would mean `disabled`. `M` is one of `unmanaged` (addresses by SLAAC, the menu's
  preselected entry), `router` (Andaru announces itself as a router, with no prefix) or `disabled`. `unmanaged` needs a
  `static` address of length 64, the only length SLAAC announces. The seed sets up no DHCPv6 server, so it offers no
  mode that needs one.
- **DNS servers**: on a WAN with a `static` family, `addressing.dns.servers` holds one to eight addresses, IPv4 or
  IPv6, none link-local and none twice.
- **Management** has at least one family that is not `none`, so the box can be reached. A `dhcp` IPv4 or a `delegated`
  IPv6 there, with the other family `none`, carries the installer's warning: the box's address is not known in
  advance, and a `delegated` one exists only once the ISP delegates a prefix.
- **The WAN** has at least one family that is not `none`.

## Fixtures

| File | Shows |
| --- | --- |
| `static-management-serving.json` | Static management serving DHCP and DNS; DHCP WAN. |
| `dhcp-management.json` | DHCP management. |
| `ipv6-only-management.json` | IPv6-only management (`static`); `dhcp6` WAN with no prefix. |
| `delegated-management.json` | Dual-stack management with a delegated prefix from a `dhcp6` WAN. |
| `static-wan.json` | Static dual-stack WAN with gateways and DNS servers. |
| `slaac-wan.json` | IPv6-only `slaac` WAN. |
| `dhcp6-advanced-wan.json` | `dhcp6` WAN with the advanced options. |
| `no-wan.json` | No WAN. |
| `no-key.json` | No SSH key. |

The fixtures' key is a throwaway, generated for them.
