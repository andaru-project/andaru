# Web UI shell and the apply flow

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the web UI gains an application shell with a router, a left navigation and a header; a persistent notice of saved-but-unapplied changes and of an apply awaiting confirmation; one Review and apply page with confirmation and rollback; and a read-only Interfaces page. No new API.

**Spec:** [web UI shell and the apply flow](../specs/2026-11-21-web-ui-shell-design.md), author calls 1 to 11. "Call N" below is the spec's.

## Global constraints

- Everything stays a local asset under the production CSP (`internal/web/headers.go`): no inline script, no `<style>` element the page depends on.
- One new runtime dependency, `react-router`, pinned to an exact version. No query library, no icon pack.
- Strings stay English, in the components.
- `npm run lint`, `npm run typecheck`, `npm test` and `npm run build` in `web/`, and `go test ./internal/web/...`, pass after every task.

## Tasks

### Task 1: the router and the API client
- [ ] `npm install --save-exact react-router@7.18.5` in `web/` (call 1).
- [ ] `ApiClient` takes an optional connection listener, told after every request whether it got a response (call 8).
- [ ] `ApiClient` reads `GET /api/v1/versions` (with `limit` and `before`), `GET /api/v1/versions/{id}`, `GET /api/v1/preview/network?savedVersion=`, `GET /api/v1/status/links` and `GET /api/v1/config`; `apply` takes the confirmation window.
- [ ] `types.ts`: `VersionInfo`, `History`, `VersionDetail`, `Finding`, `Preview`, `LinkStatus`, `SavedLink`, `Segment`, `Snapshot`; `Apply.state` as its enum, `files[].mode` as a string, as `openapi.json` has them.

### Task 2: `useResource`
- [ ] `src/useResource.ts`: loading, error, data and reload; loads on mount and when its loader changes; a 401 signs the session out; a failed reload keeps the last data; a stale response never overwrites a newer one (call 2).

### Task 3: the shell
- [ ] `src/Shell.tsx`: Mantine `AppShell` with a header (the Andaru mark from `assets/andaru-icon.svg`, the signed-in account, Connected or Connection lost, an account menu with Session details, Change password and Sign out) and a left navigation (call 3).
- [ ] Navigation groups Network (Interfaces), Changes (Review and apply) and System (Users, Roles, Tokens, Audit), each item and group shown only when the session's capabilities allow it; routes `/network/interfaces`, `/changes`, `/system/users`, `/system/roles`, `/system/tokens`, `/system/audit` and `/system/session`; any other path goes to the first visible page.
- [ ] Below 900 px the navigation collapses behind a burger. `styles.css` carries AppShell's sizes, because the CSP blocks the `<style>` element AppShell writes them into.
- [ ] The current session overview moves to `src/screens/SessionScreen.tsx`, reached from the account menu.
- [ ] A connection-lost notice above every page while the last request got no response; the next success clears it (call 8).
- [ ] `App.tsx` keeps login, setup-required, session-lost and disconnected handling, and renders the shell inside `BrowserRouter`.

### Task 4: the pending notice
- [ ] `src/changes.ts`: the shell's change state (`GET /api/v1/applies`, `GET /api/v1/versions?limit=1`), version comparison, the live generation (`pointers.live`), the notice to show, and the countdown hook.
- [ ] `src/PendingNotice.tsx`: unapplied changes with a Review and apply link; an apply activating; awaiting confirmation with the server's deadline, a countdown, Confirm and Roll back (disabled while one runs); the last apply failed or rolled back while nothing newer is saved; nothing otherwise (call 4). Not shown on `/changes`, which owns those controls.
- [ ] The shell reloads the change state on load, every 15 seconds while the tab is visible, when the tab becomes visible, after its own actions (apply, confirm, roll back, a role save or delete), and when the connection comes back.

### Task 5: Review and apply
- [ ] `src/screens/ChangesScreen.tsx` at `/changes` (call 5): what is live, or the factory network; every saved version after the live one, newest first, with actor, time and changed objects.
- [ ] Per changed object, the live and saved values side by side, computed from the declaration views of the live saved version and of the reviewed head (`objectAt` and `differences` in `src/changes.ts`); singletons and list orders by their collection-named IDs.
- [ ] The network preview of the reviewed head: errors listed with their paths block Apply; warnings listed.
- [ ] The review loads its own live and head versions and keeps them until reloaded: when the shell sees a newer save or another live generation, it says so with a Reload button and Apply waits; a failed reload keeps Apply disabled, and a failed load never says there is nothing to apply.
- [ ] Apply sends the reviewed head as `savedVersion` and a 60, 120 (default) or 300 second window. Only 412 `PRECONDITION_FAILED` says the saved state moved and reloads the review; any other refusal shows the server's message, and a 422 `APPLY_REFUSED` lists its findings (`ApiError.findings`).
- [ ] Awaiting confirmation: the deadline, Confirm and Roll back, and the server's answer reported as given. While the connection is lost, the spec's wording, never a local rollback claim, and Confirm and Roll back stay offered: the server decides.
- [ ] History of the last 20 applies; read-only without `configuration.apply`, no Confirm without `configuration.confirm`; loading, empty, error, applying, confirmed, rolled back and failed states (call 9).

### Task 6: the roles screen
- [ ] Remove the apply and confirmation controls from `RolesScreen.tsx`; a save or delete refreshes the shell's notice (call 6).

### Task 7: Interfaces
- [ ] `src/screens/InterfacesScreen.tsx` at `/network/interfaces` (call 7): one row per live link (name, MAC address, kind, state, MTU, addresses), beside the saved link, segment, zone and addressing mode on it, with the management segment marked.
- [ ] A saved link meets its live device as `internal/render` does: an Ethernet link by the preview's `resolved` interface, a virtual link by its derived name. A link with a `HARDWARE_*` or `NAME_COLLISION` finding at its `/links/<index>` path is not joined, and shows as missing, unresolved or not realized with the finding's message. Two saved links on one device both show, marked as a conflict. A live device no saved link uses is unused.
- [ ] When the saved configuration or the preview cannot be read, the live links still show, with the error.
- [ ] Without `configuration.read`, live links only.

### Task 8: tests (call 11)
- [ ] The shell: navigation per capabilities, routing, the redirect from `/`, connection lost.
- [ ] The pending notice in each state, the countdown reading the server's deadline.
- [ ] Review and apply: created, changed and deleted objects; Apply sending the reviewed version and window; a 409 reloading; a blocking finding disabling Apply; Confirm and Roll back; the lost-connection wording; read-only.
- [ ] Interfaces: the join of live links and saved segments, missing and unused links.
- [ ] The roles screen no longer offers an apply.

### Task 9: qualification
- [ ] A browser check of the built assets under the production CSP, as before: the shell lays out at desktop and phone widths and the console shows no blocked asset beyond Mantine's inline styles.

## Out of scope

- Editing interfaces, firewall rules, NAT, DHCP, DNS; a dashboard; logs; server-sent events.
- A system information endpoint for the header; setup in the browser; translations.
