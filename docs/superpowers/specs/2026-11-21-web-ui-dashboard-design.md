# Web UI: navigation tree, narrow screens and the dashboard

Status: a draft for the owner, 2026-11-21. It records the owner's decisions of that day and the author's calls that follow from them; the calls await review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Owner decisions

- The navigation is a list on the left, because it uses a wide screen best. It may have submenus that open like a tree. Tabs are not wanted.
- The design is responsive and scales to a phone.
- The home page is a dashboard. Add-ons should be able to define their own widgets, as in pfSense and Unraid. How is not decided.
- The dashboard shows pretty much everything pfSense's main page shows today. Named by the owner: a network activity graph; resource use (CPU, memory, disk); the version and whether an update is available, linking to the upgrade page; IP addresses; and system details: the CPU, the amount of memory, the CPU instructions that matter to a firewall as one line, the x86-64 level, the loaded microcode version, and the BIOS.

This document amends the [shell design](2026-11-21-web-ui-shell-design.md): its call 3 (navigation) and call 19 (widths).

## Author calls: navigation

1. **The navigation is a tree of at most three levels:** a group, a page, and a page's sub-pages. Mantine's `NavLink` nests natively, with a chevron, `aria-expanded` and keyboard support.
   - **Dashboard** is the first entry and stands alone.
   - A branch with sub-pages opens and closes on its label; the label itself is not a page. `NavLink` renders an anchor by default, and an anchor without an address takes no keyboard focus, so a branch label is rendered as a button (`component="button"`).
   - The branch that holds the current page is open. Other branches keep the state the user left them in, per browser.
   - The rule from the shell design stays: an entry shows only when the session holds a capability for it, and only once its page exists.

2. **A page never splits into tabs.** What would be a tab is a sub-page in the tree, with its own address. The planned tree, as pages arrive:
   - Dashboard
   - Network: Interfaces
   - Firewall: Rules, NAT (Port forwards, Outbound, One-to-one), Aliases
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
   - The grid is `repeat(auto-fill, minmax(min(20rem, 100%), 1fr))`: three or four columns on a wide screen, one on a phone. The `min()` lets the single column shrink below 20rem, so a 320 px screen does not scroll sideways.
   - A widget shows only when the session holds its capability.
   - A widget's title links to the page that holds the full view.

10. **The widgets,** in page order. "Served" means the engine serves the data today; the rest is new engine work (call 11).
    - **System:** hostname, Andaru version and image, whether the image's signature verified, uptime, the box's time, safe mode, and the last applied change.
      - **Update:** "Up to date" or "Version X is available", linking to the upgrade page. The check is the engine's, on a schedule, and the widget shows when it last ran. Until an upgrade page exists the line states the version only.
      - **Hardware:** the CPU's model, cores and threads; installed memory; the board, its serial number, and the BIOS vendor, version and date; whether the box is a virtual machine and of which kind.
      - **CPU features:** only those a firewall gains from, shown as present, in three lines (call 17). Not the whole flag list.
      - **x86-64 level:** v1 to v4, worked out from the flags, so the owner can later match a box to an optimised image.
      - **Microcode:** the revision the kernel loaded, not the one on disk.
      - **CPU vulnerabilities:** one line, "all mitigated" or the names of those that are not, from the kernel's own report. pfSense shows two of these; the kernel lists all.
    - **Resources:** CPU use and load average, memory, swap, each file system's use, the connection-tracking table (used of its limit), and temperatures where the hardware reports them. Each is a bar with its number; a bar is never the only carrier of the value.
    - **Network activity:** a line graph of receive and transmit rate for one interface, chosen in the widget, WAN first.
    - **Interfaces:** each interface's name, zone, state, speed and addresses, IPv4 and IPv6 (`status/links`, served).
    - **Changes:** nothing pending, saved but not applied, or awaiting confirmation with its countdown (`config/changes`, `applies`, served).
    - **IPv6 prefixes:** each delegated prefix and where it went (`status/prefixes`, served).
    - **Services:** DHCP, DNS and the packet filter: running or not, and since when.
    - **Recent activity:** the last few audit events (`audit/events?order=desc&limit=5`, served; repeated reads of the log coalesce, so polling it does not flood it), for accounts with `audit.read`.

11. **The engine gains three read-only resources,** beside `status/links` and `status/prefixes` and under the same `status.read` capability, which both built-in roles hold. A new capability would change what the built-in roles are for no gain: who may see the interfaces may see the machine.
    - **`status/system`,** what changes rarely: the System widget's version, clock, uptime, machine and CPU, and the System details page's identity, hardware, features, level, microcode, vulnerabilities and update state. Sources are the kernel's own files (`/proc/cpuinfo`, `/sys/class/dmi/id`, `/sys/devices/system/cpu/vulnerabilities`) and `bootc status`. The engine reads them; no helper runs.
    - **`status/usage`,** what changes every second: CPU times, memory, swap, file systems, connection tracking and temperatures, with the time of the reading.
    - **`status/traffic`,** the traffic history of call 12.
    - The board's serial number is served and shown with the hardware (owner, 2026-11-21).
    - The header takes the hostname and version from `status/system`.
    - `status/system` first serves what the kernel's files say and the Andaru version. The image, its verification and the update state come with the upgrade slice, which owns `bootc`; safe mode comes when the engine has such a state to report. Two points of call 17 wait for a source: PAN is not among the features the arm64 kernel lists, and nothing read here says whether it started at EL2. Memory is what the kernel has to use, a little less than what is installed. The dashboard has no Changes widget: what is pending is the notice above every page, and a second telling beside it said nothing more. The dashboard is for what changes or says something is wrong, as on the dashboards of OPNsense, TrueNAS and UniFi, and not for what the box is made of, which is looked up rarely: the System widget keeps the version, the clock, the uptime and a line each for the machine and its processor, and links to System ▸ Details, a page of three cards (Software, Hardware, Processor) that holds the rest. A page and not a drawer: a drawer opened from a widget is lost when the widget is hidden, and on a phone it is a page without an address. Widgets run down columns, each as tall as what is in it, and not in rows of one height. A traffic graph is two fifths as high as it is wide, and its scale is the highest reading rounded up to a round number, so it is never below the Max beside it. The header names the box and not its version, which the System widget and the details page give. The widgets' first order is System, Interfaces, Resources, Network activity, IPv6 prefixes: what is connected before what is used. On the details page a guest is named on the Machine line, not on the virtualisation line, which lists what the CPU offers; and the memory installed is on the details page, and in the Resources widget beside what is used of it.

12. **The engine keeps one hour of traffic history** (owner, 2026-11-21), so the graph is drawn in full when the dashboard loads.
    - Every 5 seconds the engine records each network adapter's counters: bytes, packets, errors and drops, each way (owner, 2026-11-21: since the history is in memory only, read often enough to show a dip of a few seconds). It keeps the last 720 readings per adapter, which is the hour: about 64 KiB an adapter.
    - The history lives in the engine's memory. It starts again when the engine or the box restarts, and nothing is written to disk. Longer history belongs with the statistics work ([#15](https://github.com/andaru-project/andaru/issues/15)).
    - `status/traffic` serves the readings with their times. The browser works out the rates from neighbouring readings, so a counter that restarts shows as a gap, never as a spike.
    - An adapter that disappears keeps its readings until they age out; a new one starts empty.
    - The graph is an SVG the page draws itself: one widget does not justify a chart library, and the content security policy allows SVG elements.
    - The graph has a text equivalent: the current, average and peak rates, in words, beneath it.

13. **Refresh is by polling,** only while the page is visible: `status/usage` every 2 seconds, `status/traffic` every 5, asking only for the readings it lacks, everything else every 10. Server-sent events remain a later slice.

14. **Widgets can be hidden and reordered from the first slice** (owner, 2026-11-21).
    - The dashboard uses the grip of the firewall rules behind an Edit layout button with a pencil, where the rules have a padlock ([firewall design](2026-11-21-web-ui-firewall-design.md), call 4). Locked, it is plain. Unlocked, each widget shows its corner grip and a Hide button, and a list of hidden widgets offers Show.
    - The layout is two things: the order, a list of widget names, and the set of hidden names. A widget in neither (a new version, an add-on) joins at the end, shown. A name the page no longer knows is ignored and dropped at the next save.
    - **The layout is kept with the account** (owner, 2026-11-21): arranging a dashboard takes time, and losing it by changing browser is frustrating. The engine gains a small per-account preference document, read and written through the session (`session/preferences`), outside the saved configuration: it needs no apply and is in no generation. It is size-limited and holds nothing the engine acts on.
    - One layout per account, for every screen width: the grid already reflows the same order into fewer columns.
    - "Reset" returns to the default order.

15. **From pfSense's main page, not in this slice,** each for want of data the engine does not hold yet: gateway latency and loss, the DHCP lease list, a firewall log tail, per-rule counters, S.M.A.R.T. state, VPN tunnels and traffic totals over days. Each arrives with its own feature.

16. **Slices** (order agreed by the owner, 2026-11-21). The work is too large for one change:
    - first, the navigation tree and the narrow-screen layout, with a dashboard of the widgets already served, and the per-account preferences that keep its layout;
    - second, `status/system` and the System widget;
    - third, `status/usage` and `status/traffic`, with Resources and Network activity;
    - the update check arrives with the upgrade slice, since the check and the upgrade page share their mechanism.

17. **CPU features, and why each is shown** (owner, 2026-11-21). The lines show names only, three rows and nothing to open (owner, 2026-11-21: an information button was tried and removed). The reasons below are for this document. A feature the CPU lacks is not listed. The list follows the platform.
    - **Cryptography,** which decides VPN and TLS throughput:

      | x86-64 | Why a firewall cares |
      | --- | --- |
      | AES-NI, PCLMULQDQ | AES-GCM in hardware: IPsec, OpenVPN and TLS. Without them AES runs several times slower. |
      | VAES, VPCLMULQDQ | AES-GCM on several blocks at once: the next step up for IPsec and TLS on recent CPUs. |
      | SHA-NI | SHA-1 and SHA-256 in hardware: VPN packet authentication, TLS, and checking signed images. |
      | AVX2, AVX-512 | ChaCha20-Poly1305 in wide vectors: WireGuard's throughput. |
      | ADX, BMI2 | Curve25519 arithmetic: WireGuard and TLS handshakes. |
      | RDRAND, RDSEED | Hardware entropy for the kernel's random numbers, which matters for keys made early in boot. |

      | arm64 | Why a firewall cares |
      | --- | --- |
      | AES, PMULL | AES-GCM in hardware, as AES-NI and PCLMULQDQ. |
      | SHA1, SHA2, SHA512, SHA3 | Hashes in hardware. |
      | ASIMD (NEON), SVE | ChaCha20-Poly1305 in vectors: WireGuard's throughput. |
      | RNG | Hardware entropy. |

    - **Security,** its own line: protections the kernel uses against attacks on itself. x86-64: NX, SMEP, SMAP, UMIP, CET (shadow stack, IBT). arm64: PAN, PAuth, BTI, MTE.
    - **Virtualisation,** its own line: VT-x or AMD-V, and whether an IOMMU is active; on arm64, whether the kernel started at EL2. When Andaru itself runs as a guest, the line says so and names the hypervisor.
    - The exact reasons are checked against the kernel's implementations when the widget is built: the table above states the author's understanding, not a measurement.

## Later, recorded so the navigation leaves room

- **Diagnostics** ([#111](https://github.com/andaru-project/andaru/issues/111)) becomes its own branch in the tree. Its first page answers "what would happen to this traffic" with a flow diagram.
- **pfBlockerNG** ([#112](https://github.com/andaru-project/andaru/issues/112)) gets no branch of its own. Its pages live where their subject lives, under Firewall and DNS, and carry its name and logo there.

## Add-on widgets: a direction, not a design

Decided by the owner, 2026-11-21: **add-ons declare their widgets, and Andaru draws them.** No frames.

- The add-on's manifest declares a widget: a title, a data address the engine proxies to the add-on, and one of a small set of kinds (a list of names and values, a list of states, a table, a number with a trend).
- Every widget then looks like Andaru and works on a phone, and no add-on code runs in the browser. That matters beyond looks: the content security policy allows scripts from the box's own origin only, and code in the page would hold the administrator's session.
- Add-ons may later get some way to draw for themselves. What that is stays undecided; it will not be a frame.

The built-in widgets of call 10 are ordinary components in a list of `{id, title, capability, component}`. Nothing more is built for add-ons until the add-on slice, which decides the manifest.

## Open for the owner


## Out of scope

- Any add-on mechanism.
- Per-account preferences beyond the dashboard layout.
- Server-sent events.

## Related documents

- [Web UI shell and the apply flow](2026-11-21-web-ui-shell-design.md)
- [Web UI: firewall rules and aliases](2026-11-21-web-ui-firewall-design.md)
- [Web UI foundation](../../research/web-ui-foundation.md)
