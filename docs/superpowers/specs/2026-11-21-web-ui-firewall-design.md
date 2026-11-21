# Web UI: firewall rules, addresses and services

Status: a draft for the owner, 2026-11-21. Every call below is the author's and awaits review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

The second web UI slice makes Andaru usable as a firewall from the browser: seeing, creating, ordering, enabling, disabling and removing rules (journey step 7 of [#1](https://github.com/andaru-project/andaru/issues/1)), with the address and service objects rules refer to.

It builds on the shell ([shell design](2026-11-21-web-ui-shell-design.md)): every save shows the pending notice, and Review and apply makes it live. Nothing on these pages applies anything.

The API exists: `policy.input` and `policy.forward` with their order operation, `addresses` and `services` ([packet filter design](2026-11-12-packet-filter-design.md)). This slice adds no engine behaviour except where call 9 says so.

## Author calls

1. **Navigation gains a Firewall group:** Rules, Addresses, Services. It needs `configuration.read`; editing needs `configuration.write`, and without it the pages are read-only.

2. **Rules is one page with two tabs,** "Through the firewall" (`policy.forward`) and "To the firewall" (`policy.input`). The tab names say what the chain means; the chain names appear as a caption for people who know nftables.
   - Above the table, a fixed, read-only block lists what Andaru always does before any rule: established and related traffic accepted, invalid dropped, loopback and neighbour discovery accepted, DHCP requests on served segments, SSH on the management segment. Below the table: "Everything else is dropped."
   - Without `management.segment` the filter is off (packet filter design). The page then says so and links to Interfaces instead of showing an empty table that looks like "allow nothing".

3. **The table is the execution order.** Columns: position, enabled (a switch), action, from, to, services, name with its description beneath, and row actions.
   - **No sorting.** A filter box narrows the rows and keeps each row's real position number.
   - **From and to** show zone and address names as chips; an empty list reads "any". A zone chip and an address chip look different (a zone is where traffic enters or leaves; an address is who).
   - **Action** is a word with a mark, never colour alone: accept, drop, reject. A logging rule carries a "logs" tag.
   - A disabled rule stays in place, dimmed, with its switch off.

4. **Reordering is explicit and keyboard-first.** Each row has Move up and Move down, and a "Move to position…" action for long lists. Drag and drop is not in this slice.
   - A move is a saved edit: it sends the whole order with the list's version (`PUT …/order`, `If-Match`). A 412 means someone else reordered: the page reloads the list and says so, and never guesses.
   - While the filter box hides rows, moving is disabled, with the reason shown: a move among hidden rows would be a guess.

5. **Editing a rule is a side panel,** not a dialog over the table, so the order stays visible. Fields: name, description, enabled, action, from, to, services, log.
   - From and to are pickers over zones and addresses, grouped, searchable by name. "Any" is the empty selection, stated in words.
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

9. **A lockout warning, from the engine.** A rule that would cut the management path must not be found out by losing the session. Review and apply already runs the network preview; this slice asks the engine for one new preview finding, `MANAGEMENT_UNREACHABLE` (warning), when the saved input rules would drop the web UI's or SSH's port from the management segment's allowed sources. The rollback timer remains the real protection; the warning only saves a wasted apply.
   - This is the slice's one engine change. If the owner prefers no engine change here, the page instead shows a static note beside "To the firewall".

10. **Starter rules.** A new box has no rules, so nothing forwards. The Rules page, when empty, offers "Allow the LAN out": one forward rule from the management segment's zone to anywhere, saved like any rule. It is an offer, never automatic, and slice 3's seed apply may make it moot.

11. **States each page covers:** loading, empty, read-only, validation errors, version conflict, saved-but-unapplied (the shell's notice), filter off, and connection lost.

12. **Tests:** the table keeps execution order under filtering; moves send the full order with the version and handle a 412; the editor maps engine field errors to fields; read-only accounts get no controls; deleting a used object lists its users.

## Open for the owner

- **Tabs or one list?** Two tabs keep each chain's order clear. One list with a "direction" column matches pfSense's per-interface tabs less, but shows everything at once.
- **Drag and drop:** leave it out for now (call 4), or add it with the keyboard controls?
- **The lockout warning (call 9):** an engine finding, or a note only?
- **Starter rule (call 10):** offer it, or leave an empty table with an explanation?
- **NAT** (outbound mode, port forwards, 1:1) is the natural third slice. Should port forwards sit under Firewall or under their own NAT group?

## Out of scope

- NAT, DHCP, DNS and interface editing: later slices.
- Counters, a log viewer and per-rule statistics ([#15](https://github.com/andaru-project/andaru/issues/15)).
- Rule templates, schedules and FQDN addresses.
- Widths below 900 px.

## Related documents

- [Web UI shell and the apply flow](2026-11-21-web-ui-shell-design.md)
- [Packet filter and outbound NAT](2026-11-12-packet-filter-design.md)
- [Web UI foundation](../../research/web-ui-foundation.md), "Firewall Rules"
