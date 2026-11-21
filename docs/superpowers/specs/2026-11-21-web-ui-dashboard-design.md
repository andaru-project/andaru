# Web UI: navigation tree, narrow screens and the dashboard

Status: a draft for the owner, 2026-11-21. It records the owner's decisions of that day and the author's calls that follow from them; the calls await review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Owner decisions

- The navigation is a list on the left, because it uses a wide screen best. It may have submenus that open like a tree. Tabs are not wanted.
- The design is responsive and scales to a phone.
- The home page is a dashboard. Add-ons should be able to define their own widgets, as in pfSense and Unraid. How is not decided.

This document amends the [shell design](2026-11-21-web-ui-shell-design.md): its call 3 (navigation) and call 19 (widths).

## Author calls: navigation

1. **The navigation is a tree of at most three levels:** a group, a page, and a page's sub-pages. Mantine's `NavLink` nests natively, with a chevron, `aria-expanded` and keyboard support.
   - **Dashboard** is the first entry and stands alone.
   - A branch with sub-pages opens and closes on its label; the label itself is not a page.
   - The branch that holds the current page is open. Other branches keep the state the user left them in, per browser.
   - The rule from the shell design stays: an entry shows only when the session holds a capability for it, and only once its page exists.

2. **A page never splits into tabs.** What would be a tab is a sub-page in the tree, with its own address. The planned tree, as pages arrive:
   - Dashboard
   - Network: Interfaces
   - Firewall: Rules (Through the firewall, To the firewall), Addresses, Services
   - Changes: Review and apply
   - System: Users, Roles, Tokens, Audit

3. **The navigation keeps the light surface.** The header alone is dark.

## Author calls: narrow screens

4. **One layout, three widths.** No separate mobile site and no user-agent checks.
   - **900 px and wider:** the navigation is always visible.
   - **Below 900 px:** the navigation is a drawer behind the header's button, and closes when a page is chosen.
   - **Below 600 px:** a table row becomes a labelled block, one field per line, and dialogs and side panels take the whole screen.

5. **Widths are static CSS.** The content security policy refuses the inline styles that Mantine's responsive props generate ([#100](https://github.com/andaru-project/andaru/issues/100)). Breakpoints therefore live in `styles.css` as media queries, and grids use `auto-fill` with `minmax`, which needs no breakpoint at all. This closes #100: its remaining cases are replaced, not worked around.

6. **Touch.** Every control is at least 44 px tall below 900 px. Nothing depends on hover: what a tooltip says is also reachable by focus or is on the page.

7. **A row as a block** is the same HTML table, restyled: each cell carries its column name in a `data-label` attribute that CSS shows before the value. One markup, no second component per table.

## Author calls: dashboard

8. **The dashboard is the page at `/`.** It answers, in this order: is the box healthy, is anything waiting for me, what is connected.

9. **It is a grid of widgets.** Each widget is a titled panel that loads its own data and fails alone: one widget's error never blanks the page.
   - The grid is `repeat(auto-fill, minmax(20rem, 1fr))`: three or four columns on a wide screen, one on a phone.
   - A widget shows only when the session holds its capability.
   - A widget's title links to the page that holds the full view.

10. **First widgets, from what the engine already serves:**
    - **Changes:** nothing pending, saved but not applied, or awaiting confirmation with its countdown. The same state as the shell's notice (`config/changes`, `applies`).
    - **Interfaces:** each interface's name, state and addresses (`status/links`).
    - **IPv6 prefixes:** each delegated prefix and where it went (`status/prefixes`).
    - **Recent activity:** the last few audit events (`audit/events`), for accounts with `audit.read`.

11. **A System widget needs an engine change:** hostname, Andaru version, image and its verification, uptime, and safe mode. No endpoint serves these yet (the shell design left them out for that reason). This is the dashboard slice's one engine change, and the header gains the hostname and version from it.

12. **Widgets refresh by polling,** every 10 seconds, and only while the page is visible. Server-sent events remain a later slice.

13. **The first slice has a fixed layout.** Hiding and reordering widgets come second. They need somewhere to keep each account's choice, and no per-account preference store exists yet.

14. **Not in the first slice:** traffic graphs, gateway latency, DHCP leases, firewall log tail, service states. Each needs data the engine does not serve yet ([#15](https://github.com/andaru-project/andaru/issues/15)).

## Add-on widgets: a direction, not a design

Add-ons run as containers. The web UI's content security policy allows scripts from the box's own origin only, so an add-on cannot hand the browser code to run in the page, and should not: that code would hold the administrator's session.

Two ways fit. The author recommends the first, with the second kept possible.

- **Declared widgets.** The add-on's manifest declares a widget: a title, a data address the engine proxies to the add-on, and one of a small set of kinds (a list of names and values, a list of states, a table, a number with a trend). The web UI draws it. Every widget looks like Andaru, works on a phone, and no add-on code runs in the browser. An add-on cannot draw something the kinds do not cover.
- **Framed widgets.** The add-on serves a page, shown in a sandboxed frame from a separate origin with no access to the session. The add-on can draw anything. It will not look or scale like the rest, and it needs the separate origin and a frame policy.

The built-in widgets of calls 10 and 11 are ordinary components in a list of `{id, title, capability, component}`. Nothing more is built for add-ons until the add-on slice, which decides the manifest.

## Open for the owner

- **The first widgets (calls 10, 11):** is anything missing that must be there on day one?
- **Hiding and reordering (call 13):** second slice, or required from the start?
- **Add-on widgets:** declared, framed, or both?
- **Tree depth:** is Rules with a sub-page per chain right, or should the two chains sit directly under Firewall?

## Out of scope

- Any add-on mechanism.
- Per-account preferences.
- Server-sent events.

## Related documents

- [Web UI shell and the apply flow](2026-11-21-web-ui-shell-design.md)
- [Web UI: firewall rules, addresses and services](2026-11-21-web-ui-firewall-design.md)
- [Web UI foundation](../../research/web-ui-foundation.md)
