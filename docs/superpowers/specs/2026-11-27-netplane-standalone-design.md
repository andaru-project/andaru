# Netplane standalone: Andaru's interface, without the firewall

## Status and intent

Part of [#172](https://github.com/andaru-project/andaru/issues/172), the last slice of folding Netplane into Andaru. Owner direction, 2026-11-27: work on Netplane now, partly to see early what it looks like. The standalone interface is Andaru's, with the few Andaru-only parts removed. It is packaged to run as a container, for example under Docker or as a Home Assistant add-on. Underneath, the same drivers talk to the devices' own APIs (Andaru, OpenWrt, OPNsense, pfSense, UniFi, SNMP, RTL), instead of to the hardware, nftables and networkd of the box it runs on.

Not implemented behaviour until the slice lands. Contracts remain provisional until Andaru 1.0.

## The starting point

Before this slice:

- **`cmd/netplane serve`** serves the fleet API (`/api/v1/...`), the poller, the event stream and the drivers from `internal/netplane`. It also serves the single-page app, built by `npm run build:netplane` into `web/dist-netplane` and embedded with `-tags embedui`.
- **The standalone app has a shell of its own** (`web/src/netplane/Shell.tsx`). It differs from Andaru's in layout, header and navigation, its routes sit at the root (`/topology`), and it asks for the API token with `window.prompt`.
- **Andaru folds the same fleet screens into its own shell** under `/network/...`, through `FleetWorkspace` (#180).
- **No image is published from this repository.** The `andaru-project/netplane` repository still holds a copy of the code, a Dockerfile and the Home Assistant add-on.

## The interface

The standalone app takes Andaru's shell, with nothing of it re-implemented:
- the header, with the mark and the title;
- the connection badge;
- the account menu: appearance, time zone and sign out;
- the branch navigation, one branch open at a time, collapsing behind a burger below 900 px;
- the page titles, and the alerts above the pages.

The shared frame is factored out of `Shell.tsx`, and Andaru's and Netplane's shells both use it. Andaru looks and behaves byte for byte as before.

| | Andaru | Netplane standalone |
| --- | --- | --- |
| Title | Andaru, and the box's host name | Netplane |
| Home | Dashboard | Overview: the fleet at a glance (the present Overview screen) |
| Network | Interfaces, DHCP, DNS, Virtual IPs, Topology, VLANs & Segments, VLAN Flow, Devices, Clients | Topology, VLANs & Segments, VLAN Flow, Devices, Clients |
| Firewall, Changes | yes | no |
| Diagnostics | Traffic simulator, Tools & events | Tools & events |
| System | Details, Updates, Backups, Notifications, Users, Roles, Tokens, Audit | no |
| Account menu | session, password, appearance, time zone, sign out | appearance, time zone; sign out when signed in with a token |

- **Routes** follow Andaru's: `/network/topology`, `/network/devices/:id` and the rest, with the Overview at `/`. A link copied from one product works in the other. The fleet screens render through `FleetWorkspace` in both, with the API prefix each product serves.
- **Old paths.** The standalone app's former root paths (`/topology`, `/vlans`, `/vlans/flow`, `/devices`, `/devices/:id`, `/clients`, `/diagnostics`) redirect to their `/network/...` paths, keeping the query. Andaru has no such redirects.
- **Signing in.** A server started with a token asks for it on a sign-in page in the style of Andaru's (`LoginScreen`, sharing its card), not with `window.prompt`. The token is kept in the same `SameSite=Strict` cookie, which the event stream needs, scoped to the app's base path and `Secure` on HTTPS. Sign out clears it. A server without a token (loopback only, or `--allow-unauthenticated`) shows no sign-in and no sign out.
  - The app checks access on start (`GET api/v1/diagnostics?limit=1`, with the read timeout). Only a 401 asks for the token; any other answer counts as access, and an unreachable server opens the shell with its connection badge showing the loss.
  - The token is trimmed before it is stored. A token the server refuses, or that could not be checked because the server was unreachable ("Could not reach the Netplane server."), is not kept.
  - When the start-up check passes with a cookie present, the app checks once more without credentials. If that passes too, the server needs no token: the cookie is cleared and no sign out is shown. This also covers a cookie at a parent path, which signing out could not clear.
  - A 401 later in the session clears the cookie and returns to sign-in, saying the stored token is no longer accepted, or, when none was stored, that the server requires one. A 401 to a request sent before the latest sign-in, or with another token than the one now stored (another tab signed in), is ignored.
- **Targets** (the devices Netplane polls) stay in the YAML configuration, as today. Editing them in the interface is a later slice.

## The container

- **Built from this repository** by `packaging/netplane/Containerfile`, in three stages:
  - Node builds `dist-netplane`.
  - Go builds `cmd/netplane` with `-tags embedui`, statically linked.
  - The final image runs it as a non-root user: `netplane serve --config /config/netplane.yaml --db /data/netplane.db --listen :8099`. `/config` and `/data` are volumes.
- **Published by the release workflow** as `ghcr.io/andaru-project/netplane:<version>` and `:latest`, for amd64 and arm64, beside a standalone `netplane` binary per architecture. CI builds the image on every pull request, and does not push it.
- **The Home Assistant add-on** uses that image. Its manifest stays in `andaru-project/netplane` (below).

## The netplane repository

Once the image is published from here, `andaru-project/netplane` keeps only:
- the Home Assistant add-on manifest, pointing at `ghcr.io/andaru-project/netplane`;
- the Apache-2.0 client artifacts: `schema/`, `clients/python/` and `ha-integration/`.

Its copies of `cmd/`, `internal/`, `web/` and the Dockerfile go, so `andaru-project/andaru` stays the one AGPL-3.0-or-later source of the server. That is a pull request in that repository, after this one.

## Acceptance

- **Web:**
  - Andaru's shell tests pass unchanged.
  - New tests cover the standalone shell's navigation, routes and sign-in, and that no Andaru-only page or menu item appears.
  - `npm run build:netplane` builds.
  - Screenshots of the standalone app go in the pull request.
- **Image:**
  - CI builds it.
  - A container run with an example configuration serves the app and `/api/v1/state`, and survives a restart with its database on the volume.

## Calls made (author's, not the owner's)

- **One frame, two shells,** rather than one shell with a product switch: Andaru's shell holds the apply flow, notifications and upgrade checks, which Netplane has none of.
- **Andaru's routes in the standalone app,** dropping the root routes, which have no users yet.
- **A sign-in page for the token,** in place of the browser prompt.
- **Targets stay in YAML** in this slice.
