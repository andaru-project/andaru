# Web UI shell and the apply flow

Status: the owner chose this slice on 2026-11-21; the author calls below await review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

The web UI today is an identity tool: one page with buttons for users, roles, tokens and audit, and an apply-and-confirm flow that lives inside the roles screen. The API already serves interfaces, firewall policy, NAT, DHCP, DNS, saved versions, applies, confirmation and rollback, none of which has a screen.

This slice builds the frame every later page plugs into:

- an application shell with a router, a left navigation and a header;
- a persistent notice of saved-but-unapplied changes, and of an apply awaiting confirmation;
- one Review and apply page for every change, with confirmation and rollback;
- a read-only Interfaces page, the first network page.

It follows the direction in [web UI foundation](../../research/web-ui-foundation.md) and the stack chosen in [#12](https://github.com/andaru-project/andaru/issues/12): React, TypeScript, Vite and Mantine, served by `andaru-web` as local assets.

## Owner decisions

From the 2026-11-21 conversation:

- **The first web UI slice is the shell and the apply flow,** with a read-only interfaces page. Firewall rules, interface editing, NAT, DHCP and DNS come in later slices.

## Author calls

1. **Routing uses React Router** (`react-router`, data-less declarative mode), pinned like the other dependencies. The foundation asks for an established router rather than a hand-built one. Routes are paths, so a page can be bookmarked and the browser's back button works. `andaru-web` already falls back to `index.html` for unknown paths outside `/api/`.
2. **No query library yet.** Pages fetch with the existing `ApiClient` in effects, as the identity screens do. One small hook, `useResource`, carries loading, error, data and reload, so pages do not repeat that state. A cache library waits until two pages need the same data kept fresh.
3. **The shell.**
   - The header shows the Andaru mark, the signed-in account, the connection state and an account menu (change password, sign out). No endpoint serves the box's hostname or version yet, so the header does not show them.
   - The left navigation has groups, each shown only when the session holds a capability for one of its pages:
     - **Network:** Interfaces.
     - **Changes:** Review and apply.
     - **System:** Users, Roles, Tokens, Audit.
   - Groups without a page yet (Dashboard, Firewall, Services, Logs) are not shown. A navigation entry that leads nowhere is noise.
   - Below about 900 px the navigation collapses behind a button (Mantine `AppShell` with a burger).
4. **The pending notice** sits above every page's content, in one place, and says one of:
   - "Saved changes are not applied yet" with a Review and apply link, when the saved head is newer than the live generation's saved version;
   - "Applied, awaiting confirmation: rolls back at HH:MM:SS" with Confirm and Roll back, when the latest apply is `awaiting_confirmation`;
   - "The last apply failed" or "The last apply was rolled back" with a link to its details, when that is the newest outcome and nothing newer is saved;
   - nothing otherwise.
   The shell reads `GET /api/v1/applies` and `GET /api/v1/versions` on load, after any change it makes, and every 15 seconds while the tab is visible. The countdown is display only: the deadline comes from the server, and the server rolls back.
5. **Review and apply** is one page, `/changes`, for everything saved:
   - **What is live:** the newest generation that is `confirmed` or `awaiting_confirmation`, with its saved version. None yet: "Nothing has been applied; the box runs its factory network."
   - **What would change:** every saved version after the live one, newest first, with its actor, time and changed objects (`GET /api/v1/versions`, then `GET /api/v1/versions/{id}` for the objects). For each changed object, the old and new value side by side, taken from the declaration view of the live saved version and of the saved head (`GET /api/v1/versions/{id}` returns `value`, the whole declaration). The page computes that per-object difference for display only; the engine still validates and applies the saved version as saved.
   - **Network preview:** `GET /api/v1/preview/network?savedVersion=<head>` gives `realizable` and its findings. Errors block the Apply button and are listed with their paths; warnings are listed.
   - **Apply** sends `POST /api/v1/applies` with the reviewed head as `savedVersion`, so a newer save made meanwhile is never applied unseen. A 409 or 412 says the saved state moved and reloads the review. The confirmation window is a choice of 60, 120 (the default) or 300 seconds.
   - **Awaiting confirmation:** the page shows the server's deadline, Confirm (`POST /applies/{id}/confirmation`) and Roll back (`POST /applies/{id}/rollback`). The engine decides whether a confirmation counts; the page only reports its answer.
   - **Lost connection:** if a request fails with no response while awaiting confirmation, the page says "Connection lost: the box will roll back at HH:MM:SS unless this apply is confirmed. Its state is unknown until the box answers again." It never claims a rollback from a local timer. When the API answers again, the page reloads the apply and shows what the server says.
   - **History:** the last 20 applies with their state, saved version, time and reason.
   - Users without `configuration.apply` see the review read-only; without `configuration.confirm`, no Confirm button.
6. **The apply flow leaves the roles screen.** Its apply and confirmation buttons go; a saved role change shows the pending notice like any other save.
7. **Interfaces** is one read-only page, `/network/interfaces`:
   - one row per link from `GET /api/v1/status/links`: name, MAC address, kind, operational state, MTU and addresses;
   - beside each link, what the saved configuration puts there, from `GET /api/v1/config`: the segment, its zone, and its addressing mode; the management segment is marked;
   - a saved link with no live device is listed as missing, and a live device the configuration does not use as unused;
   - no editing; the page says interface editing comes later only by not offering it.
8. **Connection state.** The header shows "Connected" or "Connection lost". Any request that fails with no response sets it lost; the next success clears it. A 401 still signs the session out, as today.
9. **States each page covers:** loading, empty, read-only for the session's capabilities, an API error with its message, connection lost, and, for Review and apply, nothing to apply, applying, awaiting confirmation, confirmed, rolled back and failed.
10. **No new API.** Everything above reads existing endpoints. A server-side difference between two saved versions would also serve `andaru ctl`, and can replace the page's computation later without changing the page's layout.
11. **Tests.** Vitest and Testing Library, as today:
    - the shell renders only the navigation the capabilities allow, and routes;
    - the pending notice in each of its four states, the countdown reading the server's deadline;
    - Review and apply: the difference shown for created, changed and deleted objects; Apply sending the reviewed version; a 409 reloading; a blocking finding disabling Apply; awaiting confirmation with Confirm and Roll back; lost connection wording;
    - Interfaces: the join of live links and saved segments, missing and unused links.
    A browser check of the built assets under the production CSP stays as today.

## Out of scope

- Editing interfaces, firewall rules, NAT, DHCP, DNS; a dashboard; logs; server-sent events.
- A system information endpoint (hostname, version, image verification, safe mode). The header gains those once the engine serves them.
- Setup in the browser ([#89](https://github.com/andaru-project/andaru/issues/89)), and enabling `andaru-web` on an installed box, which slice 3's setup does.
- Translations: strings stay English, in the components, until an i18n slice.

## Related documents

- [Web UI foundation](../../research/web-ui-foundation.md): stack, shell and apply-review direction.
- [Identity and audit core](2026-11-15-identity-audit-core-design.md): sessions, CSRF, capabilities.
- [Saved network configuration](2026-11-10-saved-network-configuration-design.md): saved versions and applies.
