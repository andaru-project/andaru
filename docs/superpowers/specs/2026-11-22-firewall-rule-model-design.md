# Firewall rules as one list between zones, with aliases

Status: implemented, 2026-11-22. The owner's decisions are in [#113](https://github.com/andaru-project/andaru/issues/113), [#114](https://github.com/andaru-project/andaru/issues/114) and [#117](https://github.com/andaru-project/andaru/issues/117); the calls marked "author" below are the author's and await review. Contracts remain provisional until Andaru 1.0.

It replaces the rule and object model of the [packet filter design](2026-11-12-packet-filter-design.md). What that design says about the fixed rules, outbound NAT, applying and the nftables table still holds. It comes before the web UI's firewall pages ([web UI: firewall rules and aliases](2026-11-21-web-ui-firewall-design.md)).

## Intent

A rule runs from a zone to a zone, and one of the zones is the firewall itself. That gives one list in one order, where there were two lists because nftables has two chains. A side of a rule says where traffic arrives or leaves and, within that, who: "From lan, except the printers, to wan, HTTPS: accept". The named objects rules refer to are called aliases, in the saved configuration, the API, the CLI and the web UI alike.

## The saved configuration, schema 4

```json
{
  "schemaVersion": 4,
  "aliases": {
    "addresses": [{"id": "adr_…", "name": "printers", "kind": "group", "members": ["adr_…"]}],
    "ports":     [{"id": "svc_…", "name": "https", "protocol": "tcp", "ports": ["443"]}]
  },
  "rules": [
    {"id": "rul_…", "name": "LAN out, except the printers", "enabled": true, "action": "accept",
     "from": {"zones": ["zon_…"], "addresses": ["adr_…"], "not": true},
     "to":   {"zones": ["zon_…"], "addresses": []},
     "ports": ["svc_…"], "log": false}
  ]
}
```

- **`aliases.addresses`** and **`aliases.ports`** are what `objects.addresses` and `objects.services` were, with the same fields. That an address alias cannot hold ports is structure, not a validation rule.
- **`rules`** is the one ordered list. Its position is its execution order, and it has one order operation and one list version.
- **A side** (`from`, `to`) is `zones`, `addresses` and an optional `not`. Both parts must match.
  - `zones`: any of them. Empty means any network, and never the firewall.
  - `addresses`: address aliases, any of them. Empty means every address.
  - `not: true` turns the address part into "none of these". It needs at least one address and takes at most 48 aliases, and so do both sides of a rule together: they share one nftables rule, and the kernel refused the heaviest such line at 50 (measured, 2026-11-22). An alias is one match however many addresses it holds, so an alias that grows never crosses the limit. A group counts as one, whatever it holds. Zones are not negated. One `not` covers the side's whole address list, which is what nftables' `!= @set` gives and what pfSense does.
  - The zones and the addresses of a side are sets: the store keeps them sorted.
- **`ports`** names port aliases, as `services` did. Empty is any protocol and port.
- **The firewall is the zone `zon_firewall`.** Nothing stores it: it has no interfaces and no name to change. A rule names it in `to.zones` to reach the box.
  - It stands alone there. Reaching the firewall and other zones takes two rules, since that would be one rule in two chains.
  - It is a destination only. The firewall's own outgoing traffic is not filtered, so `from.zones` refuses it.
  - A rule to the firewall may name addresses in `to`: which of the firewall's addresses, such as a virtual address.
- **Rule names need not be unique** (author). Two lists became one, so two rules may already share a name; a rule's place in the list and its ID tell it apart.
- **IDs keep their prefixes** (author): a port alias's ID still starts with `svc_`. An ID is opaque, and changing it would touch every reference for no gain.
- **Port forwards and 1:1 NAT are unchanged** (author). A port forward already has `ingressZones`, `from` and `fromNegated`, which is a negated source. A negated destination needs a forward whose destination is "anywhere", the DNS redirect of #114, which does not exist yet and gets its own design with the NAT pages.

## Rendering

The renderer sorts each rule into its chain by its destination: a rule to `zon_firewall` goes to `input`, every other rule to `forward`. A packet only ever meets one of the two chains, so each chain keeps the list's order and nothing else of the order matters.

One nft line holds one zone and one address alias of each side, so a rule renders one line per combination, per family and per port alias, as before. The parts of a side now share a line:

```
iifname @zon_lan ip saddr @adr_nas_4 oifname @zon_wan accept
```

A negated side is one line per family, naming every alias of that family; a family none of them holds matches whole:

```
iifname @zon_lan ip saddr != @adr_a_4 ip saddr != @adr_b_4 accept
iifname @zon_lan meta nfproto ipv6 accept
```

The rule line cap counts a negated side as one line per zone.

## The API and the CLI

| Before | Now |
| --- | --- |
| `/api/v1/config/addresses` | `/api/v1/config/aliases/addresses` |
| `/api/v1/config/services` | `/api/v1/config/aliases/ports` |
| `/api/v1/config/policy/{input\|forward}`, `…/{id}`, `…/order` | `/api/v1/config/rules`, `…/{id}`, `…/order` |
| collections `addresses`, `services`, `policy.input`, `policy.forward` | `aliases.addresses`, `aliases.ports`, `rules` |
| `andaru ctl config … addresses\|services\|policy/input\|policy/forward` | `… aliases/addresses\|aliases/ports\|rules` |
| `andaru ctl config order input\|forward` | `andaru ctl config order rules` |

The old paths and names are gone, not kept as aliases of the new ones.

## Configurations saved before schema 4

A saved version is immutable: the store keeps its bytes and their hash. So nothing rewrites the versions of schema 1 to 3. They are read through an upgrade each time they are loaded, and the engine works with the result:

- `objects.addresses` and `objects.services` become the aliases, IDs unchanged, so every reference holds.
- `policy.input` and `policy.forward` become `rules`: the input rules first, with `zon_firewall` as their destination, then the forward rules. Their meaning is unchanged, because a packet only ever met one of the two lists.
- **A side that listed zones and addresses together** meant "any of these". A side now means "these zones and, within them, these addresses". Such a rule becomes one rule per kind, next to each other: "from lan or from host A" is "from lan" and then "from host A". The added rule gets an ID derived from the rule it came from, the same on every load.
  - A zone on such a side stood for both address families, so "from lan or an IPv4 host, to an IPv6 network" was a valid rule. Its host half can never match, which schema 4 refuses in a rule. That half is left out: the engine before rendered nothing for it.
- The list may hold 80,000 rules, where each of the two lists held 10,000: every rule of both, each split in four at most. A document that grows past the 4 MiB limit by the upgrade does not load; none that small-office configurations approach. One order request carries about 24,000 rule IDs within the API's 1 MiB request limit, so a longer list cannot be reordered in one request.
- `services` on a rule becomes `ports`.

The frozen schema of the earlier forms stays in the binary to check such a document before it is upgraded, and the upgraded document is checked as any document of schema 4 is. A backup or a kept copy of an earlier form restores the same way.

**Every save writes schema 4.** Until now an ordinary change kept the schema number of the document it changed, so that an older engine could still open a store that used nothing new. That is given up here: the form of the rules changed for every document.

**Object versions.** The store records which version last changed each object, by collection name. The records of the earlier forms stay where they are, in `policy_changes` under the names they had. A version of an earlier form finds its versions there: an alias under its old collection, a rule under whichever list held it, a rule the upgrade added under the rule it came from, and the list's version as the later of the two lists'. The first version saved in schema 4 records every alias, every rule and the rule list as changed in `firewall_changes`, a table added beside the others, so that version and those after it need nothing of the earlier forms. That first save therefore counts all of them among its changed objects. What a version of an earlier form changed is reported under today's names, with the rules the upgrade split from a changed rule beside it. Once the first schema 4 version is saved, an alias or rule deleted while the store was of an earlier form is unknown to the versions after it: changing it answers "not found", where it answered "changed underneath".

**Rollback of the image** ([#121](https://github.com/andaru-project/andaru/issues/121)). Reading upgrades nothing in the store: a box that boots the new image and saves nothing is still one the previous image can open. After the first save the head is of schema 4, which the previous image refuses, as it would any document of a form it does not know. The owner's decision on #121 covers that case; this change adds no snapshot of its own.

## Not in this change

- The anti-lockout rule's web UI port (call 9 of the web UI design): the fixed management rule still accepts SSH only.
- Listing the firewall among the zones the API serves. The web UI names it itself.
- Address aliases fed from a URL or from host names ([#115](https://github.com/andaru-project/andaru/issues/115)).
- Rules for the firewall's own outgoing traffic.

## Tests

- The rule sides and "not" in the validator, the renderer's golden files and `nft -c`, and with real packets between network namespaces: a zone and an address must both match, a left-out host is refused, "any" never reaches the firewall, and a rule to the firewall lets nothing through it.
- Every render fixture was rewritten by the same upgrade, and its nftables output did not change.
- A document of schema 1 with mixed sides reads as the expected rules, the same on each read; a store saved in that form lists its rules and aliases with their versions, saves nothing for an unchanged batch, writes schema 4 on its first save, survives a restart and passes the recovery check; the earlier version still loads. A restore onto such a head does the same, and a document whose split half can never match loads without that half.
