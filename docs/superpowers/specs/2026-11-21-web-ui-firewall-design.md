# Web UI: firewall rules, addresses and services

Status: a draft for the owner, 2026-11-21. Every call below is the author's and awaits review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

The second web UI slice makes Andaru usable as a firewall from the browser: seeing, creating, ordering, enabling, disabling and removing rules (journey step 7 of [#1](https://github.com/andaru-project/andaru/issues/1)), with the address and service objects rules refer to.

It builds on the shell ([shell design](2026-11-21-web-ui-shell-design.md)): every save shows the pending notice, and Review and apply makes it live. Nothing on these pages applies anything.

The API serves `policy.input` and `policy.forward` with their order operation, `addresses` and `services` ([packet filter design](2026-11-12-packet-filter-design.md)). The owner decided on 2026-11-21 that the firewall itself is a zone (call 2). That makes the two lists one, which is engine work that comes first: [#113](https://github.com/andaru-project/andaru/issues/113). Beyond that and call 9, this slice adds no engine behaviour.

## Author calls

1. **Navigation gains a Firewall branch** in the tree ([navigation design](2026-11-21-web-ui-dashboard-design.md)): Rules, Addresses and Services. It needs `configuration.read`; editing needs `configuration.write`, and without it the pages are read-only.

2. **The firewall itself is a zone** (owner, 2026-11-21), as in Shorewall, UniFi, VyOS and OpenWrt. A rule always runs from a zone to a zone, and one of the zones, "This firewall", is the box. Nothing on the page says "to" or "through" the firewall, or names an nftables chain.
   - **Rules is one page with one list,** in one order ([#113](https://github.com/andaru-project/andaru/issues/113)).
   - "lan to wan, accept" lets the LAN out. "lan to This firewall, DNS, accept" lets the LAN use the box's DNS.
   - **"Any" means every network, never the box.** Reaching the box always takes a rule that names "This firewall". Traffic to everywhere, the box included, is two rules.
   - "This firewall" stands alone as a destination, never in a list with other zones.
   - It is offered as a destination only. Rules for what the box itself sends do not exist yet.
   - Above the table, a fixed, read-only block lists what Andaru always does before any rule: established and related traffic accepted, invalid dropped, loopback and neighbour discovery accepted, DHCP requests on served segments, SSH on the management segment. Below the table: "Everything else is dropped."
   - Without `management.segment` the filter is off (packet filter design). The page then says so and links to Interfaces instead of showing an empty table that looks like "allow nothing".

3. **The table is the execution order.** Columns: position, enabled (a switch), action, from, to, services, name with its description beneath, and row actions.
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
   - While the filter box hides rows, the padlock is disabled, with the reason shown: a move among hidden rows would be a guess.
   - Whether a small library does the dragging or the page's own pointer handling does is settled when it is built. The requirement is touch, pointer and keyboard alike.

5. **Editing a rule is a side panel,** not a dialog over the table, so the order stays visible. Fields: name, description, enabled, action, from, to, services, log.
   - From and to are pickers over zones and addresses, grouped, searchable by name. "Any" is the empty selection, stated in words as "any network". "This firewall" is first among the destination zones.
   - Services is a picker over services; empty is "any service".
   - An address or service that does not exist yet can be created from the picker ("New address…"), which opens the object's own form and returns with it selected.
   - Save uses the object's version (`If-Match`); a 412 shows what changed underneath and keeps the user's edits in the form.
   - The engine's validation is the authority: its field errors show beside their fields (`details[].path`).

6. **The enabled switch saves at once,** as one version-checked edit, and shows the pending notice like any other save. It never applies.

7. **Deleting a rule asks once,** naming the rule. Deleting an address or service that rules still use is refused by the engine; the page lists those rules, taken from the saved configuration, rather than only repeating the refusal.

8. **Addresses and Services are plain tables** with the same side-panel editor.
   - **Address:** name, kind (host, network, range, group) and its value; a group picks members that are not groups.
   - **Service:** name, protocol (TCP, UDP, ICMP, ICMPv6, group) and its ports or ICMP types; a group picks members.
   - Each row shows where it is used ("3 rules"), with the list on demand.

9. **An anti-lockout rule, as pfSense has** (owner, 2026-11-21). The packet filter already accepts SSH on the management segment by a fixed rule no edit removes. That rule grows to cover the web UI's port, so no saved rule can cut an administrator off from the browser either.
   - The owner's definition: the management interface always lets in the web UI, and SSH when SSH is enabled, so an administrator can always reach the box. When the management interface is also the LAN, the rule applies there.
   - It is the slice's one engine change: the fixed management rule accepts the web UI's listening port beside 22/tcp, from the same `allowedSources`.
   - The SSH half follows SSH itself. Today the rule accepts 22/tcp whether or not `sshd` runs, and no setting turns SSH on or off; once one exists, the rule drops its SSH half while SSH is off.
   - The list shows it as its fixed first rule, from the management segment to "This firewall", named "Anti-lockout", with the segment, the ports and the allowed sources, and a link to where the management segment is set.
   - It has no off switch: "always" is the point. Narrowing it is `allowedSources`' job.
   - The rollback timer stays the backstop for everything the rule does not cover, such as a change to the management segment's own addressing.

10. **Starter rules.** A new box has no rules, so nothing passes between networks. The Rules page, when empty, offers "Allow the LAN out": one rule from the management segment's zone to any network, saved like any rule. It is an offer, never automatic, and slice 3's seed apply may make it moot.

11. **States each page covers:** loading, empty, read-only, validation errors, version conflict, saved-but-unapplied (the shell's notice), filter off, and connection lost.

12. **Narrow screens.** Below 900 px each rule becomes a block: position, switch, action and name on the first line, then from, to and services, each labelled. The editor's side panel takes the whole screen. The padlock and the grips stay as they are.

13. **Tests:** the table keeps execution order under filtering; nothing moves while locked; the grip moves a row by keyboard; moves send the full order with the version and handle a 412; the editor maps engine field errors to fields; read-only accounts get no controls; deleting a used object lists its users.

## Open for the owner

- **Starter rule (call 10):** offer it, or leave an empty table with an explanation?
- **Negation** ("not this zone", "not this address"), which pfSense has: the engine's rules have none today. Wanted, and if so when?
- **NAT** (outbound mode, port forwards, 1:1) is the natural third slice. Should port forwards sit under Firewall or under their own NAT group?

## Out of scope

- NAT, DHCP, DNS and interface editing: later slices.
- Counters, a log viewer and per-rule statistics ([#15](https://github.com/andaru-project/andaru/issues/15)).
- Rule templates, schedules and FQDN addresses.

## Related documents

- [Web UI shell and the apply flow](2026-11-21-web-ui-shell-design.md)
- [Packet filter and outbound NAT](2026-11-12-packet-filter-design.md)
- [Web UI foundation](../../research/web-ui-foundation.md), "Firewall Rules"
