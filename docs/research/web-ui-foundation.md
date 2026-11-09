# Web UI foundation: technology and interaction direction

## Status and purpose

Research and a design direction for [Andaru #12](https://github.com/andaru-project/andaru/issues/12). The owner subsequently [selected Go + React/TypeScript/Vite/Mantine](https://github.com/andaru-project/andaru/issues/12#issuecomment-6752595186), then explicitly deferred mockups and prioritized a solid core/API. The layout ideas below remain references, not prerequisites or a frozen design. All current contracts and visual choices remain provisional until Andaru 1.0. No framework is installed by this report.

The question is not which JavaScript framework can talk to Go: ordinary browser clients can all consume a Go HTTP API. The useful distinction is which combination minimizes the work and ongoing ownership needed for accessible forms, ordered policy tables, live status, Andaru branding and offline appliance delivery.

## Boundaries already established

- The API owns authentication, authorization, validation and state changes; the UI is replaceable. Andaru ships its own UI, not Cockpit. The unprivileged Go web process serves HTTPS/UI assets; the root engine owns configuration and operations. [API-first ruling](https://github.com/andaru-project/andaru/issues/12#issuecomment-6680220311), [own-UI ruling](https://github.com/andaru-project/andaru/issues/12#issuecomment-6680226666), [process-model summary](https://github.com/andaru-project/andaru/issues/1).
- The current API proposal is REST/JSON with OpenAPI and server-sent events. GraphQL has not been selected, and neither React nor Vue requires it. JSON Schema describes configuration values; it is not a choice of UI framework or a GraphQL schema. [API draft](https://github.com/andaru-project/andaru/issues/6#issuecomment-6690626377).
- Saving creates persisted settings; applying activates selected areas. Pending saves, live configuration and observed state are distinct. Saves use object versions; apply shows an authoritative diff and can require connectivity confirmation with timed rollback. There is no shared candidate/draft. [Save/apply ruling](https://github.com/andaru-project/andaru/issues/6#issuecomment-6690380403).
- Missing unrelated interfaces must not prevent management on its elected working path. The UI needs an actionable degraded state, not an unusable full-page failure or an automatic interface substitution. [Setup/recovery direction](https://github.com/andaru-project/andaru/issues/14#issuecomment-6700283805).
- The [manifest example](traefik-manifest-example.md) supplies metadata-driven add-on menus/fields, localized plain titles, sanitized Markdown descriptions and secret references. It does not authorize add-ons to inject arbitrary JavaScript into Andaru's admin session. The [extension contract](extension-contract.md) keeps that boundary; the existing form prototype is exploratory source on `prototype/traefik-form`, not production UI.

## Go delivery does not require a JavaScript server

A client-only frontend build produces files that the Go web process can serve from packaged storage or an embedded filesystem. Go's `embed.FS` works with `net/http`. Vite documents traditional-backend integration and the production build manifest/assets. The build tool is development/packaging machinery, not a requirement to run Node, Vite's dev server, an SSR server or a CDN on the firewall. [Go embed](https://pkg.go.dev/embed#hdr-File_Systems), [Vite backend integration](https://vite.dev/guide/backend-integration).

```text
Browser: branded UI, form interaction, presentation state
    │ HTTPS / API requests and progress events
Unprivileged andaru-web: local assets and management HTTP transport
    │ engine's local management API
Privileged andaru-engine: authentication, RBAC, persistence, apply/recovery
```

The diagram is an ownership sketch, not a new wire protocol. Session cookies, CSRF checks, scoped automation tokens and all permission decisions remain backend responsibilities. UI routing, query caching and validation helpers must not become a second configuration authority. Bundle production fonts/icons/scripts/styles locally; do not introduce a runtime network dependency merely because a documentation example uses a CDN.

## Frontend/component comparison

Primary-source research checked on 2026-11-09. The package versions below are observations, not pins or support commitments. Recommendations in this section are engineering judgments, not measured performance rankings.

| Candidate | What it supplies | What remains Andaru work | Assessment |
| --- | --- | --- | --- |
| React + Mantine | MIT controls, dialogs, form helpers, theme/Styles API and a semantic themed table | Policy ordering, row actions, conflict handling, API integration, application i18n and safe Markdown | Recommended working default: a coherent kit without taking ownership of every copied widget |
| React + MUI Core | MIT Material UI components, semantic Table, theme and locale facilities | Custom policy-order controls if avoiding paid grid features; Andaru API/state behaviour | Viable alternative, not disqualified by MUI X pricing; Material defaults require deliberate branding |
| React + shadcn/ui | Open component source copied into the application, primitive-library composition and a TanStack table-building guide | Ownership/upgrades of copied components plus table/order/form integration | Best when source-level design control is worth the larger assembly/maintenance burden |
| Vue + Naive UI | MIT TypeScript components, DataTable/custom-cell examples, theme overrides and component locale support | Safe policy ordering, API/state behaviour, app translations and accessibility qualification | Credible unrestricted Vue alternative; worth testing if its authoring style is preferred |
| Go templates/templ + HTMX | Server-rendered HTML and progressive interactions; Go retains more presentation code | HTML adapters over the engine API, cross-region UI updates and rich table interactions | Legitimate, but a different presentation architecture rather than a simpler drop-in component kit |

### React + Mantine: strongest default for this scope

Mantine documents MIT licensing for its packages and recommends Vite for a client-side SPA. Its core/hooks packages provide the widget foundation; `@mantine/form` supplies form values and validation helpers. The form helper's `reorderListItem` moves data in a form list: it is not a complete accessible policy-table reorder UI. The core `Table` is a styled semantic table, not an enterprise grid. [Getting started/licensing](https://mantine.dev/getting-started/), [Vite guide](https://mantine.dev/guides/vite/), [form values](https://mantine.dev/form/values/), [Table](https://mantine.dev/core/table/).

Theming uses colour scales and a Styles API rather than forcing the default appearance. This fits an Andaru-owned visual language without maintaining copies of all widget source. Library accessibility documentation, including its Modal, is a useful baseline, not a substitute for keyboard/screen-reader tests of the composed application. [Colours](https://mantine.dev/theming/colors/), [Styles API](https://mantine.dev/styles/styles-api/), [Modal](https://mantine.dev/core/modal/). Registry metadata observed `@mantine/core` **9.8.0 / MIT**; qualify and pin the actual versions when implementing.

Use core/hooks and form helpers where needed, plus ordinary CSS/modules and Andaru theme tokens. Do not automatically install charts, dates, notifications or other optional packages before a screen needs them. Start the policy table with semantic rows and explicit move controls; evaluate drag support or a headless table engine only when the interaction calls for it. A grid dependency is not a prerequisite to having an ordered firewall rule list.

### MUI and shadcn: real alternatives with different ownership costs

MUI's built-in Data Grid row drag-and-drop is a **Pro** feature. That does not make paid software essential: its free semantic Table can host Andaru-owned move/edit/enable controls, just as Mantine's table can. MUI supports custom/system fonts; Roboto and Google-hosted fonts are not mandatory. The reason to prefer Mantine here is judgment about a less Material-specific starting point and a convenient form toolkit, not an assertion that MUI cannot implement Andaru for free. [MUI row ordering](https://mui.com/x/react-data-grid/row-ordering/), [Table](https://mui.com/material-ui/react-table/), [typography customization](https://mui.com/material-ui/customization/typography/), [licensing](https://mui.com/x/introduction/licensing/).

shadcn explicitly distributes component source for the application to own. Its Data Table documentation is a construction guide, not a single prebuilt policy grid. It is appealing for exact design control, but copied source, primitive packages, styling tooling and table integration become our ongoing responsibility. This is a trade-off, not a claim that copying source is inherently bad. [Ownership model](https://ui.shadcn.com/docs), [table guide](https://ui.shadcn.com/docs/components/base/data-table), [MIT license](https://github.com/shadcn-ui/ui/blob/main/LICENSE.md).

### Vue remains viable; current Prime licensing changes the kit choice

Do not confuse Vue's suitability with one vendor's licence. Naive UI registry metadata observed **2.45.3 / MIT**. Its own repository documents TypeScript components and theme customization, with separate attribution for the `Result` component's Twemoji graphics. Its official DataTable documentation has stable row keys, custom cells, editable examples, filters/pagination and resizable columns. These features do not establish keyboard-accessible **row** ordering; Andaru's explicit move controls and save/apply rules still need application code. [Naive UI repository/license](https://github.com/tusen-ai/naive-ui), [DataTable](https://www.naiveui.com/en-US/components/data-table.md), [theme overrides](https://www.naiveui.com/en-US/docs/customize-theme.md), [component locales](https://www.naiveui.com/en-US/docs/i18n.md).

Vue single-file templates versus React TSX is a readability choice to assess with an actual small screen; liking Java/Kotlin or Go does not establish a preference for either. Both have a TypeScript build toolchain and both can produce an API-driven frontend served by Go. The Naive UI coverage here is narrower than the React-kit comparison, and its composed keyboard/screen-reader behaviour has not been tested.

**Important licensing correction:** current PrimeReact **11.2.2** (both `primereact` and `@primereact/ui`) and PrimeVue **5.0.4** publish `SEE LICENSE IN LICENSE.md`, not MIT. The vendor's transition announcement explicitly says PrimeReact 11 / PrimeVue 5 are no longer open-source releases, with Community eligibility/key conditions and separate commercial terms. Old MIT versions remain MIT; an old `master/LICENSE.md` is not evidence for the new package's terms. [Vendor announcement](https://primeui.dev/nextchapter), [Community terms](https://primeui.dev/licenses/community), [PrimeReact registry metadata](https://registry.npmjs.org/primereact/11.2.2), [PrimeVue registry metadata](https://registry.npmjs.org/primevue/5.0.4).

Therefore current PrimeReact/PrimeVue are not working defaults for the unrestricted Free Software contribution/distribution goal of this [AGPLv3 project](../../LICENSE). This is an engineering/licensing-risk choice, not a legal finding that every combination is prohibited. Existing MIT releases or a fork would require a deliberate maintenance/support decision; we do not need that obligation to start Andaru. It does not rule out Vue + another MIT kit.

### Go + HTMX is possible without abandoning API-first

HTMX normally exchanges HTML rather than JSON. To preserve the established engine authority, an unprivileged HTML presentation tier would call the engine's API, then render fragments; it must not bypass engine authentication, permissions or databases. That can work well for forms and server-computed diffs. Pending-change badges, live cross-area state and the rule table require deliberate fragment/event or small-script integration. HTMX is not itself a widget library, and typed `templ` components add a build-time code-generation tool rather than eliminating all tooling. [HTMX introduction](https://htmx.org/docs/#introduction), [fit guidance](https://htmx.org/essays/when-to-use-hypermedia/), [templ](https://templ.guide/), [Go template trust model](https://pkg.go.dev/html/template#hdr-Security_Model).

The objection is total presentation complexity for this particular rich API client, not that HTML-first apps cannot have accessible tables or that API-first requires React. A native move button is enough to reorder rules with either approach; a drag library is optional. The earlier plain-HTML manifest prototype already demonstrates that useful forms need no frontend framework, but it does not supply production routing, server state, table interactions or security integration.

### Working stack to take into the next UI spike

Recommend **React + TypeScript + Vite + Mantine**, with the Go engine/API unchanged. This is a proposal, not an accepted ADR or frozen contract. Keep **Vue + Naive UI** as the meaningful alternative if its authoring style is preferred.

Use established client routing rather than hand-building a router. Add a server-state/query library only where loaders/native fetch cease to keep data flow clear; choose one approach rather than immediately adding Redux plus another cache. Kit form validation helps presentation but does not replace JSON Schema or engine validation. Application translations, Markdown sanitization, API types and event handling are separate concerns whichever kit is chosen. [React app/tooling guidance](https://react.dev/learn/build-a-react-app-from-scratch), [React Router modes](https://reactrouter.com/start/modes), [TanStack Query role](https://tanstack.com/query/latest/docs/framework/react/overview).

Before treating the candidate as implementation-ready, exercise one real library-backed interface form, an ordered policy table and the review/confirmation pattern with keyboard operation, realistic row counts, long addresses/translations, API loading/conflict states, local assets and the intended Content Security Policy. No bundle-size win, lower-memory claim or accessibility certification follows from this documentation research. Do not relax the script policy or load CDN resources just to make a demo work.

## Proposed visual and interaction contract

### Reference lessons, not a copied appearance

Netgate's [rule-list documentation](https://docs.netgate.com/pfsense/en/latest/firewall/rule-list-intro.html) and its [LAN-rule screenshot](https://docs.netgate.com/pfsense/en/latest/_images/firewall-lan-rules.png) show why a firewall needs a dense ordered table, descriptions, action indicators, counters, edit/enable controls and more than a mouse-only reorder interaction. Its differing save behaviour between reorder methods is not something to reproduce: Andaru's save/apply semantics must remain consistent.

The [interface-status inventory](https://docs.netgate.com/pfsense/en/latest/monitoring/status/interfaces.html) distinguishes friendly assignment, device identity, carrier/disabled state, addresses, gateways, MTU and counters. This is a content reference, not a requirement to implement every pfSense protocol or copy its layout. The existing Andaru form prototype provides a second visual reference for restrained brand chrome, inline help and clear validation; it is not a finished visual specification.

### Shell

Use the existing Andaru icon and amber brand accent, a compact header with appliance identity and connection state, a persistent left navigation on desktop, a page title/breadcrumb, and a stable area for pending-change notices. Suggested navigation groups are Dashboard, Network, Firewall, Services, Logs and System; names/routes remain proposals. The add-on menu's parent IDs must resolve through the application navigation registry rather than translated labels.

```text
Andaru   [appliance identity]                [connection] [account]
──────────────────────────────────────────────────────────────────
Navigation       Network / Interfaces
                 [Network: saved changes not yet applied]
                 [interface list / editor]         [Review & apply]
```

Prefer a neutral light content surface with dark brand chrome as the first direction, based on the prototype; it is not a light/dark commitment. Typography should use a locally available/system font, with monospace where it helps inspect addresses, prefixes and IDs. Keep forms readable and policy tables compact, rather than creating a dashboard card for every datum. Reuse kit controls directly with Andaru theme tokens; do not build a universal wrapper around every widget.

The icon's `#ffb21c` is an accent, not body text on white. Calculated WCAG contrast ratios for the proposed existing prototype pairs: amber/white **1.80:1**, dark ink `#17212c`/amber **9.02:1**, white/dark chrome `#152330` **15.97:1**, muted `#526171`/white **6.35:1**. These are static colour calculations, not a rendered-page accessibility audit. Keep brand and warning semantics distinguishable with text/icons and never convey state by colour alone. [WCAG use of colour](https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html).

### Interfaces

The main list should answer which logical segment is on which link, whether it is available, which addresses are actually active, and whether saved settings differ from live state. Show the elected management path explicitly, including relevant VLAN/parent relationships, without assuming management, LAN and WAN require three physical NICs.

Selecting an interface opens a focused editor with descriptions, address validation and field-level errors. Distinguish no carrier, disabled, missing hardware, waiting for upstream addressing, and configuration failure. A failed WAN must not hide a working management page. Save does not claim that addressing changed; Review & apply previews the network area's changes and lockout risk.

### Firewall Rules

Use a policy-area/zone selector above an ordered rule table. Show execution position, enabled state, action, protocol, source, destination/service and description; counters are observed data, not part of the declaration. Preserve readable addresses and expose full values without relying on hover alone.

Execution order is authoritative. Do not apply a generic table's default sort to policy rows and imply that a display sort changes rule priority. Filtering must retain actual positions. Provide explicit keyboard-accessible move-before/after or move-up/down controls even if drag-and-drop is also available; filtered reordering must not guess where hidden rules belong. Reordering is a saved configuration edit with collection-version conflict checks, not a local-only row animation. Enable/disable/edit/delete must have explicit labels and permissions. No automatic live apply from a toggle.

### Apply Review and connection recovery

Review & apply opens a server-derived change summary for the selected area(s), showing old/new values, who saved them and when, dependencies and the saved version the user reviewed. Secret values never appear. The preview must not include unrelated pending areas silently or let a stale review apply newer changes.

After activation, show server-reported progress and, when applicable, the rollback deadline plus an explicit connectivity confirmation action. The backend decides whether confirmation arrived through the required path. A browser countdown is display, not the rollback mechanism. If connectivity disappears, say the connection is lost and recovery status is unknown until the API can be reached again; do not claim rollback from a local timer alone. A successful fallback remains a failed apply of the requested settings.

### States that belong in the first real UI slice

Cover initial loading, empty configuration, read-only access, field validation, stale-version conflict, saved-but-unapplied changes, applying/awaiting confirmation, confirmed/failed/rolled-back outcomes, disconnected API and degraded networking. Keep unsaved browser edits distinct from persisted pending settings; warn before discarding them. Sanitize Markdown after localization and keep values out of HTML execution paths. A component library's accessibility documentation is not proof that the composed app is accessible.

## Readiness checkpoint

Do not block core implementation on mockups or visual approval. Start with a real engine/API slice and test the presentation layer when it has useful secured endpoints to consume; make a reasonable initial interface and refine it from use. The [first core API implementation plan](../superpowers/plans/2026-11-09-core-api.md) begins with the root-local engine/CLI and live interface inventory, then the existing roadmap adds saved configuration, safe apply/recovery and the network-facing authenticated client. When frontend work begins, qualify the actual selected components, local assets, keyboard behaviour and CSP as part of that implementation. No complete plugin platform, author CI, SSR layer, GraphQL service or final design-system catalogue is a prerequisite.

## Evidence limits

This report records documentation/source research, proposed layouts and the explicit static colour calculations. It does not claim a framework build, comparative bundle-size/performance benchmark, live component-library demo, accessibility certification or working firewall. Dependency versions, release health and advisories must be checked when a stack is selected and pinned. A library being capable of static delivery does not prove that every third-party widget avoids remote resources; verify the actual asset build and network behaviour.
