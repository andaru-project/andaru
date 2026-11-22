# Web UI: firewall rules and aliases

Status: a draft for the owner, 2026-11-21. Every call below is the author's and awaits review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

The second web UI slice makes Andaru usable as a firewall from the browser: seeing, creating, ordering, enabling, disabling and removing rules (journey step 7 of [#1](https://github.com/andaru-project/andaru/issues/1)), with the aliases rules refer to.

It builds on the shell ([shell design](2026-11-21-web-ui-shell-design.md)): every save shows the pending notice, and Review and apply makes it live. Nothing on these pages applies anything.

The API serves `rules` with its order operation, `aliases.addresses` and `aliases.ports` ([firewall rule model](2026-11-22-firewall-rule-model-design.md)). The owner decided on 2026-11-21 that the firewall itself is a zone (call 2). That made the engine's two lists one, which came first: [#113](https://github.com/andaru-project/andaru/issues/113). Beyond that and call 9, this slice adds no engine behaviour.

## Author calls

1. **Navigation gains a Firewall branch** in the tree ([navigation design](2026-11-21-web-ui-dashboard-design.md)): Rules and Aliases. It needs `configuration.read`; editing needs `configuration.write`, and without it the pages are read-only.

2. **The firewall itself is a zone** (owner, 2026-11-21), as in Shorewall, UniFi, VyOS and OpenWrt. A rule always runs from a zone to a zone, and one of the zones, "This firewall", is the box. Nothing on the page says "to" or "through" the firewall, or names an nftables chain.
   - **Rules is one page with one list,** in one order ([#113](https://github.com/andaru-project/andaru/issues/113)).
   - "lan to wan, accept" lets the LAN out. "lan to This firewall, DNS, accept" lets the LAN use the box's DNS.
   - **"Any" means every network, never the box.** Reaching the box always takes a rule that names "This firewall". Traffic to everywhere, the box included, is two rules.
   - "This firewall" stands alone as a destination, never in a list with other zones.
   - It is offered as a destination only. Rules for what the box itself sends do not exist yet.
   - **A side of a rule is a zone and, within it, addresses** (owner, 2026-11-21; [#113](https://github.com/andaru-project/andaru/issues/113)). A zone is a set of interfaces: where traffic arrives or leaves. An address is who. Both must match, so "lan, host A" means host A arriving from lan. Today's engine reads it as "lan or host A"; the model changes.
   - A rule reads as a sentence, and the table shows it as one: "From lan, except the printers, to wan, HTTPS: accept."
   - Above the table, a fixed, read-only block lists what Andaru always does before any rule: established and related traffic accepted, invalid dropped, loopback and neighbour discovery accepted, DHCP requests on served segments, SSH on the management segment. Below the table: "Everything else is dropped."
   - Without `management.segment` the filter is off (packet filter design). The page reads the saved configuration, as all of it does, so it says what applying would do ("No management segment is set: applying this configuration leaves the packet filter off") and links to Interfaces instead of showing an empty table that looks like "allow nothing".

3. **The table is the execution order.** Columns: position, enabled (a switch), action, from, to, ports, name with its description beneath, and row actions.
   - **No sorting.** A filter box narrows the rows and keeps each row's real position number.
   - **From and to** show zone and address names as chips; an empty list reads "any". "This firewall" is a zone chip with its own mark, so rules that open the box stand out in the list. A zone chip and an address chip look different (a zone is where traffic enters or leaves; an address is who).
   - **Action** is a word with a mark, never colour alone: accept, drop, reject. A logging rule carries a "logs" tag.
   - A disabled rule stays in place, dimmed, with its switch off.

4. **Reordering is by a grip, behind a padlock** (owner, 2026-11-21). It must work on a phone and never get in the way of scrolling.
   - **The padlock.** The list is locked when the page opens. A padlock button above the table, with its state in words ("Order locked", "Order unlocked"), unlocks it. Locked, no grip is shown and nothing can move by accident.
   - **The grip.** Unlocked, each row shows a grip in its corner: dots in two columns. A row is dragged by the grip alone, by touch or pointer. Anywhere else on the row, a finger scrolls.
   - **The keyboard uses the same grip.** It is a button: Space or Enter picks the row up, the arrow keys move it, Space or Enter drops it and Escape puts it back. A live region says where the row is. No separate move buttons exist.
   - Dragging near the top or bottom edge scrolls the list, so a long list needs no other control.
   - A drop is a saved edit: it sends the whole order with the list's version (`PUT …/order`, `If-Match`). A 412 means someone else reordered: the page reloads the list and says so, and never guesses.
   - While the filter box hides rows, the order is locked and the padlock is disabled, with the reason shown: a move among hidden rows would be a guess. Typing in the filter box while the order is unlocked locks it, and puts back a row that was picked up.
   - Whether a small library does the dragging or the page's own pointer handling does is settled when it is built. The requirement is touch, pointer and keyboard alike.

5. **Editing a rule is a side panel,** not a dialog over the table, so the order stays visible. Fields: name, description, enabled, action, from, to, ports, log.
   - From and to each have two pickers: zones ("any network" when empty; "This firewall" first among the destinations) and addresses ("every address" when empty), searchable by name.
   - Each address list has a "not" switch (owner, 2026-11-21; [#114](https://github.com/andaru-project/andaru/issues/114)): the rule then matches every address in the zone except those named. The owner's case is a DNS redirect for a whole zone that leaves out the servers which must reach the upstream resolver. Zones are not negated. The table shows "not" as a word before the addresses.
   - Ports is a picker over port aliases; empty is "any protocol and port".
   - An alias that does not exist yet can be created from the picker ("New alias…"), which opens the alias's own form and returns with it selected.
   - Save uses the object's version (`If-Match`); a 412 shows what changed underneath and keeps the user's edits in the form.
   - The engine's validation is the authority: its field errors show beside their fields (`details[].path`).

6. **The enabled switch saves at once,** as one version-checked edit, and shows the pending notice like any other save. It never applies.

7. **Deleting a rule asks once,** naming the rule. Deleting an alias that rules still use is refused by the engine; the page lists those rules, taken from the saved configuration, rather than only repeating the refusal.

8. **Aliases** (owner, 2026-11-21). What the engine stores as `addresses` and `services` the web UI calls aliases, as pfSense does: a name for addresses, or a name for ports. "Service" suggests an address with a port, a thing that listens; "alias" is the word people already use and can say to each other.
   - **One Aliases page, one table,** with a kind column and a filter by kind. No tabs.
   - **Address alias:** a host, a network, a range, or a group of other address aliases.
   - **Port alias:** a protocol (TCP, UDP, ICMP, ICMPv6) with its ports or ICMP types, or a group of other port aliases.
   - The editor is the same side panel as for rules.
   - Each row shows where it is used ("3 rules"), with the list on demand.
   - The API and the saved configuration take the same word (owner, 2026-11-21; [#117](https://github.com/andaru-project/andaru/issues/117)), so the CLI, the API and the web UI say one thing.
   - **One shared alias field** serves every place in the web UI that takes an address: it accepts a literal address or an alias's name, and completes names as one types. Ports get the same. Pages of later slices and of add-ons use that field, not their own.
   - **An address alias fed from a URL** ([#115](https://github.com/andaru-project/andaru/issues/115)), refreshed on a schedule, such as a provider's published ranges. Its row shows when it last refreshed, how many entries it holds and its last error. This is engine work and not in this slice.

9. **An anti-lockout rule, as pfSense has** (owner, 2026-11-21). The packet filter already accepts SSH on the management segment by a fixed rule no edit removes. That rule grows to cover the web UI's port, so no saved rule can cut an administrator off from the browser either.
   - The owner's definition: the management interface always lets in the web UI, and SSH when SSH is enabled, so an administrator can always reach the box. When the management interface is also the LAN, the rule applies there.
   - It is the slice's one engine change: the fixed management rule accepts the web UI's listening port beside 22/tcp, from the same `allowedSources`.
   - The SSH half follows SSH itself. Today the rule accepts 22/tcp whether or not `sshd` runs, and no setting turns SSH on or off; once one exists, the rule drops its SSH half while SSH is off.
   - The list shows it as its fixed first rule, from the management segment to "This firewall", named "Anti-lockout", with the segment, the ports and the allowed sources, and a link to where the management segment is set.
   - It has no off switch: "always" is the point. Narrowing it is `allowedSources`' job.
   - The rollback timer stays the backstop for everything the rule does not cover, such as a change to the management segment's own addressing.

10. **Starter rules.** The setup assistant already writes them: for each zone without an uplink, accept to any network, and ping and DNS to the box. The Rules page adds nothing to that. Only when the list is empty does it say why nothing passes ("No rules: traffic between networks is dropped, except what port forwards let in") and offer to add the same starter rules, saved like any rule and never automatic.

11. **States each page covers:** loading, empty, read-only, validation errors, version conflict, saved-but-unapplied (the shell's notice), filter off, and connection lost.

12. **Narrow screens.** Below 900 px each rule becomes a block: position, switch, action and name on the first line, then from, to and ports, each labelled. The editor's side panel takes the whole screen. The padlock and the grips stay as they are.

13. **Tests:** the table keeps execution order under filtering; nothing moves while locked; the grip moves a row by keyboard; moves send the full order with the version and handle a 412; the editor maps engine field errors to fields; read-only accounts get no controls; deleting a used object lists its users.

## Open for the owner


## NAT, decided for the next slice

Owner decisions, 2026-11-21. The NAT pages get their own design; these points bind it and touch the Rules page.

- **Place:** under Firewall, as NAT with a sub-page each: Port forwards, Outbound, One-to-one.
- **A port forward lets its own traffic in.** No associated firewall rule is shown or kept in step, as pfSense does: the forward already says who, which port and where to. The engine does this today (`filterRule: auto`, [inbound NAT design](2026-11-12-inbound-nat-design.md)): it accepts exactly the connections that forward translated.
- The port forward form has no rule field. Under Advanced, one switch, "Let this traffic in", on by default (`filterRule: none` when off), for whoever writes the rule by hand.
- **The Rules page says so.** Its fixed block carries one read-only line, "Port forwards let their own traffic in (3)", linking to them, so nobody wonders why traffic passes without a rule.
- That accept comes before the list, so a rule in the list cannot stop a forward's traffic. The form says how to restrict a forward: narrow its source, or switch "Let this traffic in" off and write rules.

## Out of scope

- NAT, DHCP, DNS and interface editing: later slices.
- Counters, a log viewer and per-rule statistics ([#15](https://github.com/andaru-project/andaru/issues/15)).
- Rule templates, schedules and FQDN addresses.

## Related documents

- [Web UI shell and the apply flow](2026-11-21-web-ui-shell-design.md)
- [Packet filter and outbound NAT](2026-11-12-packet-filter-design.md)
- [Web UI foundation](../../research/web-ui-foundation.md), "Firewall Rules"
