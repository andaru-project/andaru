# Add-on support

Status: owner decisions recorded on 2026-11-27; ready for implementation planning. Reconciles the appliance image add-on direction ([2026-11-18-appliance-image-design.md](2026-11-18-appliance-image-design.md)), the event bus add-on grant model ([#154](https://github.com/andaru-project/andaru/issues/154), [2026-11-23-event-bus-design.md](2026-11-23-event-bus-design.md)), safe-mode and reset rules ([2026-11-19-first-boot-safe-mode-resets-design.md](2026-11-19-first-boot-safe-mode-resets-design.md), [2026-11-22-settings-safe-mode-design.md](2026-11-22-settings-safe-mode-design.md)), and the provisional declarative manifest contract ([extension-contract.md](../../research/extension-contract.md), [traefik-manifest-example.md](../../research/traefik-manifest-example.md)). Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

Andaru ships as a signed Fedora CoreOS appliance image without third-party software baked into the base OS. Optional third-party services—starting with Tailscale—run as managed add-ons: confined Podman Quadlet containers configured declaratively inside Andaru's saved configuration, realized inside the normal apply, confirmation, and rollback cycle, gated by safe mode, and integrated into Andaru's firewall zones, event bus, and web UI without injecting third-party code into the root engine or the administrator's browser session.

This document establishes one coherent architecture and splits the work into independently testable modules before implementation begins.

## Owner decisions (already recorded)

Consolidated from existing specs and issues; these are fixed constraints unless the owner revises them:

- **Containers via Podman Quadlet (2026-11-18):** add-ons are containers rendered as Podman Quadlet units in the same generation as the network, inside the normal apply, confirm, and rollback cycle. Package layering (`rpm-ostree install`) is unsupported.
- **Image-shipped, digest-pinned catalog (2026-11-18):** add-ons come from a catalog shipped in the signed Andaru image, with each entry pinned by immutable image digest (`@sha256:...`). The signed Andaru image vouches for the catalog and its digests.
- **Host networking first (2026-11-18):** initial add-ons use host networking only so Podman never creates bridge nftables tables that compete with Andaru's `inet andaru` table. Bridged / rootless pasta networking waits for a dedicated coexistence design.
- **Zone membership, no add-on firewall rules (2026-11-18):** an add-on's kernel interface (such as `tailscale0`) joins an Andaru zone and runs with its own firewall management disabled (e.g., `--netfilter-mode=off` for Tailscale). `51-andaru-unclassified.network` matches only `en*`, leaving add-on interfaces outside `systemd-networkd`.
- **State location and resets (2026-11-17, 2026-11-18, 2026-11-19):** add-on persistent state lives under `/var/lib/andaru/addons/`. Full factory reset wipes it; soft reset stops add-ons and wipes their state and unreferenced secrets only after the soft-reset network apply is confirmed.
- **Safe mode (2026-11-19, 2026-11-22):**
  - Boot safe mode (one-boot flag `/var/lib/andaru/safe-mode-next` moved to `/run/andaru/safe-mode`, or kernel arg `andaru.safe=1`) disables all add-ons fail-closed via unit conditions while keeping the network and packet filter up.
  - Settings-upgrade hold (safe mode for pending upgrade decisions) keeps add-ons off until decisions are answered.
- **Declarative manifest and generated UI (2026-11-15, 2026-11-21):**
  - Andaru generates add-on management pages from declarative metadata; no third-party JavaScript runs in Andaru's browser session (`script-src 'self'`). An application's own WebUI is linked separately and never receives Andaru's session cookie or token.
  - Ordered arrays of objects for named collections; direct property types with inline bindings (JSON Schema derived internally); strict unknown-field, duplicate-key, `null`, and duplicate-binding rejection; literal English or `{ "i18n": "key" }` strings; sanitized Markdown descriptions with raw HTML disabled. (Custom multi-page `menu` trees attaching to top-level navigation branches are deferred from Slice 6 per the 2026-11-27 decision below.)
  - Andaru passes validated environment variables, argument vectors (never through a shell), and authorized credential/storage paths; no template engine, preparation-script API, or privileged lifecycle/migration hooks.
- **Event bus and Android-style grants (#154, 2026-11-23):**
  - Add-ons connect over a dedicated bus socket with authentication.
  - Manifests declare requested publish/subscribe topic patterns; the administrator grants or denies each explicitly at enable/install or when an update requests a new topic. Grants are never auto-added; removed requests lose their grants; changing a grant restarts the add-on. Add-ons must function with any requested grant denied.
  - Only the notification subsystem may subscribe to `notification/#`; add-ons may request permission to publish notifications under their own source.
- **Slice 6 decisions (2026-11-27):**
  - **Catalog scope:** Slice 6 ships image-shipped, digest-pinned catalog add-ons only; external signed catalogs remain deferred until this path is proven.
  - **Web UI scope (start simple):** no custom add-on pages or nav-tree attachment (`menu.parent` / `menu.children`) in Slice 6. **System → Add-ons** (`/system/addons`) is the single entry point, offering **Install** (or **Update** when a newer digest is shipped), **Uninstall** (with confirmation), and a **Settings** page (`/system/addons/<slug>`) for each installed add-on that renders everything declared in its manifest (properties, zone bindings, bus permissions, lifecycle/health, logs, and optional external WebUI link).
  - **RBAC granularity in Slice 1:** map manifest permissions onto core `addons.read`, `addons.manage`, and existing `configuration.*` capabilities; per-add-on custom-role capabilities (`addon.<slug>.<perm>`) come in a later slice.
  - **Image staging UX:** **Install** / **Update** runs an integrated "Stage image and continue" flow that pulls the pinned digest before and outside the 45-second apply rollback window (Quadlets still enforce `Pull=never` and apply preview still blocks if unstaged).
  - **Tailscale state in portable backups:** `/var/lib/tailscale` node-key state is excluded from portable backups (`backup: false`) so restored boxes re-authenticate cleanly and never create duplicate Tailscale node identities.

## What this supersedes

- **Debian-era native & external-Compose runtime portions** of [extension-contract.md](../../research/extension-contract.md) and [traefik-manifest-example.md](../../research/traefik-manifest-example.md): native binary runtimes, host `.deb` package trust boundaries, and external `docker-compose` / `podman-compose` providers are replaced by Podman Quadlet containers on the Fedora CoreOS appliance image. The declarative manifest, strict validation, binding semantics, secret handling, and generated-UI rules are retained and refined below.

## Capability map and build order

Add-on support spans six independently testable modules with acyclic dependencies:

| Module id | Responsibility | Depends on |
| --- | --- | --- |
| `safe-mode` | Boot safe-mode one-boot flag, `andaru.safe=1`, settings-hold flag, console entry, `status/system` reporting | — |
| `addon-catalog` | Manifest v1 contract and strict parser/validator, image-shipped catalog loader, derived settings schema, `declaration.addons` model, `addon:<id>` secret namespaces | — |
| `addon-runtime` | Explicit image staging (`podman pull` / `Pull=never`), Quadlet rendering in generations, apply/confirm/rollback, zone interface attachment in nftables, fail-closed safe-mode conditions, lifecycle & health, reset cleanup | `safe-mode`, `addon-catalog` |
| `addon-bus` | `/run/andaru-addons/bus.sock`, per-add-on runtime token auth, dynamic grant table & disconnect-on-revoke, Android-style grant enforcement, `notification/addon.<slug>` publishing | `addon-catalog`, `addon-runtime` |
| `addon-ui` | **System → Add-ons** management page (Install/Update, Uninstall with confirmation, status) and per-add-on Settings page rendered from manifest declarations under strict CSP (zero add-on JS) | `addon-catalog`, `addon-runtime`, `addon-bus` |
| `addon-tailscale` | SELinux + `/dev/net/tun` + `Network=host` + `--netfilter-mode=off` lab qualification, Tailscale catalog manifest, end-to-end appliance verification | `addon-runtime`, `addon-ui` |

**Build order:**
1. **Lab gate (parallel with step 2):** run the Slice 6 Tailscale container + SELinux + `/dev/net/tun` lab check before finalizing `addon-runtime` Quadlet flags and SELinux policy.
2. `safe-mode` and `addon-catalog` (parallel).
3. `addon-runtime`.
4. `addon-bus`.
5. `addon-ui`.
6. `addon-tailscale`.

Each module lands in its own focused implementation plan and PR(s).

---

## Reconciled design (author calls)

### 1. Catalog, identity, and distribution (`addon-catalog`)

1. **Image-shipped catalog location.** Catalog entries ship read-only in the image under `/usr/share/andaru/addons/<slug>/manifest.json` (with optional read-only packaged assets alongside it, never writable). The engine loads and strictly validates every catalog manifest at startup; an invalid manifest fails tests/CI and is refused at load.
2. **Identifiers.**
   - `id`: stable reverse-DNS identifier (`^[a-z][a-z0-9-]{0,31}(\.[a-z][a-z0-9-]{0,31})+$`, e.g. `org.andaru.tailscale`), used in saved configuration, backups, and secret namespaces (`addon:<id>`).
   - `slug`: short unique lowercase identifier (`^[a-z][a-z0-9-]{0,31}$`, e.g. `tailscale`), used for systemd unit names (`andaru-addon-<slug>.service`), Quadlet files (`andaru-addon-<slug>.container`), state directories (`/var/lib/andaru/addons/<slug>/`), bus user names (`addon.<slug>`), and Settings URL routes (`/system/addons/<slug>`).
   - Both `id` and `slug` are unique across the catalog; duplicates fail catalog load.
3. **Source-agnostic contract.** Although Slice 6 ships only the image catalog, nothing in the manifest, configuration schema, secret store, or Quadlet pipeline assumes the catalog came from `/usr/share/andaru/addons`. Future signed external OCI add-ons and independent update checks (`requires: andaru.extension-api`) can reuse the exact same contract without redesign.
4. **Catalog updates and compatibility.** For image-shipped add-ons, an Andaru `bootc upgrade` brings the updated catalog manifests and image digests together. If an add-on's new manifest adds a bus permission request, the existing installation keeps running with the new permission **denied** until an administrator reviews and applies it. If a future external add-on declares a `requires` constraint incompatible with a staged core upgrade, upgrade preflight blocks the core upgrade (honouring the #13 policy: offer update or explicit removal, never silent disable).

### 2. Manifest v1 contract (`addon-catalog`)

Retains the owner-approved structure from [traefik-manifest-example.md](../../research/traefik-manifest-example.md), adapted to Podman Quadlet and finishing its open wire-format items:

1. **Strict parsing and validation order.**
   - Parse with duplicate-key rejection (using `internal/jsonprofile`).
   - Validate `manifestVersion` (must be `1`) and `requires` before full field validation so an unsupported manifest version produces a clear compatibility error rather than unknown-field noise.
   - Reject any unknown field anywhere in the manifest with its JSON Pointer path.
   - Reject duplicate names in every named array (`permissions`, `properties`, `storage`, `endpoints`, `interfaces`, `logs`, `metrics`, `bus.publish`, `bus.subscribe`, enum `options`).
2. **Top-level sections.**
   - Identity & compatibility: `manifestVersion`, `id`, `slug`, `name`, `version`, `application` (`name`, `version`), `requires`.
   - `permissions`: declared named permission descriptors (`view`, `configure`, `apply`, `manage`, `logs`, `metrics`), scoped to the add-on.
   - `properties`: ordered array of setting property definitions rendered on the add-on's Settings page (no `menu` tree or custom navigation attachment in Slice 6; multi-page `menu` trees remain deferred).
   - `runtime`: single `container` Quadlet specification:
     - `image`: immutable digest-pinned reference (`<registry>/<repo>@sha256:<64 hex>`, tag-only references rejected); optional per-architecture digest map or multi-arch manifest-list digest.
     - `network`: `"host"` (the only allowed value in Slice 6; other values rejected until bridged/pasta coexistence is qualified).
     - `command` / `args`: optional static entrypoint/base argument vector (never shell strings).
     - `profile`: `"unprivileged"` (default: `DropCapability=ALL`, `NoNewPrivileges=true`, non-root UID) or `"network-interface"` (drops all capabilities except those explicitly declared from an allowlist—initially `NET_ADMIN`, `NET_RAW`—plus declared device paths from an allowlist—initially `/dev/net/tun`—with `NoNewPrivileges=true`; `privileged: true` is never representable).
     - `settingsActivation`: `"recreate"` (default for Quadlet containers) or `"reload"` (only when all modified properties use file bindings, never environment/argument bindings).
     - `stopTimeoutSeconds`: bounded graceful stop timeout (default 15, max 60).
   - `interfaces` (optional array): kernel network interfaces created by the add-on (e.g. `{ "name": "tailscale0", "title": "Tailscale tunnel", "zoneRequired": false }`). Names must match Linux `IFNAMSIZ` (`^[a-z][a-z0-9_-]{0,14}$`), must not match `en*`, `eth*`, `lo`, `andaru*`, or `podman*`, and must be unique across all catalog add-ons.
   - `storage` (optional array): named subdirectories under `/var/lib/andaru/addons/<slug>/<name>` with `purpose` (`state`, `data`, `cache`, `logs`), `mountPath` (absolute container path), `readOnly` (default `false`), and `backup` (`bool`). Host paths are never author-supplied.
   - `endpoints` (optional array): `{ name, protocol, port, exposure }` (`exposure`: `"internal"` loopback-only or `"managed"`). Declaring an endpoint requests metadata for health/metrics/WebUI; it opens no firewall port automatically.
   - `bus` (optional object): `{ "publish": [...], "subscribe": [...] }`, each entry `{ "topic": "<filter>", "title": ..., "description": ... }`.
     - Validation forbids any `subscribe` filter matching `notification/#`, `notification/+`, `notification/...`, `delivery/#`, or `alias-fetch/#`.
     - Validation forbids any `publish` topic under `notification/` other than exact `notification/addon.<slug>`, and forbids core-owned prefixes (`config/`, `apply/`, `delivery/`, `alias-fetch/`, `alias-result/`, `notifications/`).
   - `logs` (optional array): `runtime` (container journal stream via `journalctl` for `andaru-addon-<slug>.service`) or `file` (relative path under a declared `storage` entry, no `..`, opened with `O_NOFOLLOW` / `openat2`).
   - `metrics` (optional array): Prometheus scrape target on an `"internal"` endpoint.
   - `health` (optional object): HTTP probe on an `"internal"` endpoint (`endpoint`, `path`, `expectedStatus`, `intervalSeconds`, `timeoutSeconds`, `startPeriodSeconds`) or container `healthCmd` argument vector (must avoid shell form; per [podman-compose-spike.md](../../research/podman-compose-spike.md), exec-form args without embedded shell parsing).
   - `webui` (optional object): `{ title, permission, endpoint, hostnameSetting?, path }` for a separate external link.
3. **Property types, grammar, and wire serialization.**
   - Property `name`: `^[a-zA-Z][a-zA-Z0-9_]{0,63}$`.
   - Supported `type` values: `string` (with optional `format`: `hostname`, `uri`, `ipv4`, `ipv6`, `cidr`, `date`, `date-time`; `minLength`, `maxLength`, `pattern`; URI userinfo always rejected), `boolean`, `integer`, `number`, `enum` (array of unique option strings, `minItems`, `maxItems`), `secret-reference`, `zone-reference`. (`certificate-reference` and `alias-reference` are reserved types rejected until core certificate management and add-on alias binding exist.)
   - **Binding grammar:**
     - `environment`: `name` matches `^[A-Z_][A-Z0-9_]{0,63}$` (omitted defaults to property `name` if valid); `ANDARU_*` prefix is reserved for platform-injected variables and rejected in author bindings.
     - `argument`: `name` matches `^--?[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`. Booleans support `style: "flag"` (`true` emits `name`, `false` omits it); without `style: "flag"`, emits `[name, "true"|"false"]`.
     - `file` (for `secret-reference`): `name` matches `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`, optional `format` (`raw`, `htpasswd`). Delivered as read-only file at `/run/andaru/secrets/<name>` and exposed via `ANDARU_SECRET_<UPPER_NAME>_FILE`.
     - **Multi-selection enum encoding:** required whenever `maxItems == nil || maxItems > 1`. Binding must specify either `"encoding": { "mode": "join", "separator": "," }` (valid for `environment` or `argument`, rejecting option values that contain the separator) or `"encoding": { "mode": "repeat" }` (valid for `argument` only, emitting `[name, v1, name, v2]`). Single-select enums (`minItems <= 1 && maxItems == 1`) unwrap their sole value automatically; empty selection (`[]` when `minItems == 0`) emits no binding.
     - **Collision checks:** duplicate environment names, duplicate non-repeated argument names, or duplicate secret file names across properties fail manifest load.
   - **Null and default semantics:** explicit `null` values in settings are rejected; defaults resolve and persist on save; explicit `false`, `0`, and `""` are preserved; missing optional settings without defaults emit no binding.
4. **Platform-injected runtime environment variables.** Reserved `ANDARU_*` variables passed to containers automatically when applicable:
   - `ANDARU_ADDON_ID`, `ANDARU_ADDON_SLUG`
   - `ANDARU_STORAGE_<UPPER_NAME>` for each declared `storage` mount path
   - `ANDARU_SECRET_<UPPER_NAME>_FILE` for each bound `secret-reference` file
   - `ANDARU_BUS_SOCKET=/run/andaru/bus.sock` and `ANDARU_BUS_TOKEN_FILE=/run/andaru/bus-token` (only when the add-on has at least one granted bus topic)

### 3. Saved configuration and secret namespaces (`addon-catalog`)

1. **Declaration model (`internal/config`).**
   - Add an optional `addons` slice (`json:"addons,omitzero"`) to `declaration` in schema v4 (additive, backward-compatible with existing v4 documents without requiring a breaking schema bump).
   - Each entry in `declaration.addons`:
     ```json
     {
       "id": "org.andaru.tailscale",
       "enabled": true,
       "settings": {
         "hostname": "andaru-gw",
         "advertiseRoutes": ["192.168.10.0/24"],
         "authKey": { "secret": "auth_key" }
       },
       "interfaces": [
         { "name": "tailscale0", "zone": "zon_..." }
       ],
       "busGrants": {
         "publish": { "notification/addon.tailscale": "granted" },
         "subscribe": { "config/head": "denied" }
       }
     }
     ```
   - Saving validates `settings` against the catalog manifest's derived schema, verifies `interfaces[].zone` references existing zones (and enforces `zoneRequired`), verifies every `busGrants` key exists in the manifest's requested topics, and checks optimistic locking (`expectedSavedVersion` + secret `expectedVersion`).
   - Deleting a zone referenced by an enabled add-on's interface is rejected by cross-reference validation (same rule as segments and firewall rules).
2. **Secret namespaces (`internal/config/secrets.go`).**
   - Extend `secretDocument.Scopes` (which currently holds `"core"`) to allow `"addon:<id>"` scopes alongside `"core"`, using the existing `secretEntry` format (`kind: "addon"`, `value`, `updatedAt`, `version`), `secretPrecondition`, and write-only `secretWrite` / `secretRef` API contract.
   - An add-on's settings may reference only secrets in its own `"addon:<id>"` namespace (administrator-assigned shared core secrets can be added later with an explicit ACL table; no cross-add-on secret reading is permitted).
   - Unresolved secret references (for example, after restoring a secrets-excluded backup) block applying an enabled add-on, matching link password references (`checkResolvedReferences`).
3. **Apply boundary.**
   - `declaration.addons` is included in `RealizationInput`. Any add-on enable/disable, setting change, interface zone change, or bus-grant change sets `applies.unapplied = true` and takes effect only through Preview → Apply → Rollback timer → Confirm.

### 4. Runtime execution, Podman Quadlet, and offline recovery (`addon-runtime`)

1. **Privilege separation.**
   - `andaru-engine` stays unprivileged (`CapabilityBoundingSet=`, `ProtectSystem=strict`, `RestrictAddressFamilies=AF_UNIX AF_NETLINK`). It renders desired files into the generation directory and invokes `andaru realize` through PID 1 (`systemdRunner`).
   - `andaru realize` writes Quadlet files, runtime secret/token files, reloads systemd, manages add-on units, and executes image staging commands.
2. **Explicit image staging (never pull during apply).**
   - Container images are **never** pulled during `realize activate`, boot, or rollback. Pulling over WAN inside the 45-second apply timeout would break network reconfigurations and offline boots.
   - Staging runs via `POST /api/v1/addons/{slug}/stage`, invoking `andaru realize addon-stage <slug>` (which runs `podman pull <digest>` using the catalog's pinned digest and verifies the pulled image ID/digest in local storage). In the Web UI, enabling an unstaged add-on presents an integrated "Stage image and continue" flow that runs this stage step to completion before saving/enabling, while still remaining strictly outside the apply rollback timer.
   - `GET /api/v1/preview/network` and `POST /api/v1/applies` fail closed with a blocking finding (`ADDON_IMAGE_NOT_STAGED`) if an enabled add-on's pinned digest is not present in local Podman storage.
   - **Offline rollback retention:** when an Andaru image update bumps an add-on's pinned digest, local Podman storage retains the previous digest until an explicit post-confirmation prune, so `bootc rollback` starts the previous generation's container without network access.
3. **Quadlet rendering in generations (`internal/render` + `internal/generation`).**
   - For each enabled add-on in the generation, `internal/render` emits:
     - `/run/containers/systemd/andaru-addon-<slug>.container` (mode `0600`, root-owned)
     - `/run/andaru-addons/<slug>/env` (mode `0600`, non-secret validated environment variables, only if non-empty)
     - `/run/andaru-addons/<slug>/secrets/<name>` (mode `0400`, owned by the container's runtime UID, only for bound `secret-reference` properties)
   - Every generated `.container` file contains:
     - `[Unit]` with `After=andaru-network-boot.service systemd-networkd.service` and the fail-closed safe-mode conditions:
       ```ini
       ConditionPathExists=!/run/andaru/safe-mode
       ConditionPathExists=!/var/lib/andaru/safe-mode-next
       ConditionPathExists=!/run/andaru/addons-held
       ConditionKernelCommandLine=!andaru.safe=1
       ```
     - `[Container]` with `Image=<pinned-digest>`, `Pull=never`, `Network=host`, `NoNewPrivileges=true`, `DropCapability=ALL` (plus only allowlisted `AddCapability=` / `AddDevice=` for `network-interface` profile), `EnvironmentFile=` (if present), `Exec=` (argument vector rendered safely with systemd quoting), `Volume=` binds for declared `storage` under `/var/lib/andaru/addons/<slug>/<name>` (`:Z` or explicit SELinux label validated by the lab spike) and read-only secret/bus mounts.
     - `[Service]` with `Restart=on-failure`, `RestartSec=5`, `TimeoutStopSec=<stopTimeoutSeconds>`.
     - `[Install]` with `WantedBy=multi-user.target` (so `andaru-network-boot` placing the confirmed generation's Quadlets before `systemd-networkd` brings add-ons up on normal boots and skips them automatically on safe-mode boots).
   - **Activation and rollback in `internal/generation`:**
     - `realize activate` writes the new generation's Quadlet/env/secret files into `/run`, removes stale `andaru-addon-*.container` files from the prior generation, runs `systemctl daemon-reload` (which invokes `podman-system-generator`), stops removed/disabled add-on units, restarts changed add-on units, and verifies readiness against each enabled add-on's declared health check (or active unit state if none declared) within a bounded timeout.
     - **Failure and rollback:** if activation fails or the confirmation timer expires, `realize rollback` restores the previous generation's Quadlet/env/secret files, runs `systemctl daemon-reload`, stops newly added units, and restarts the prior generation's add-on units. A failed apply remains `failed` even after fallback succeeds.
4. **Zone membership in nftables (`internal/render/nft.go`).**
   - `internal/render/nft.go` already models zones as dynamic nftables interface-name sets (`iifname @<zone>` / `oifname @<zone>`), matching interfaces even when they appear after boot.
   - For each enabled add-on with an `interfaces` entry bound to a `zone`, `nft.go` adds that interface name (e.g. `"tailscale0"`) to the zone's interface-name set.
   - Traffic Flow simulator and firewall validation recognize add-on interface zone attachments so administrators can write rules to/from the add-on's zone and inspect them in Diagnostics.
5. **Manual lifecycle controls.**
   - `POST /api/v1/addons/{slug}/start|stop|restart` runs `andaru realize addon-ctl <action> <slug>` on the **applied** generation's unit (`andaru-addon-<slug>.service`). It never reads or promotes pending saved edits. It refuses with `409 SAFE_MODE_ACTIVE` when safe mode or settings hold is active.

### 5. Safe mode (`safe-mode`)

Completes the unbuilt safe-mode foundation from [2026-11-19-first-boot-safe-mode-resets-design.md](2026-11-19-first-boot-safe-mode-resets-design.md) and [2026-11-22-settings-safe-mode-design.md](2026-11-22-settings-safe-mode-design.md):

1. **Boot safe mode (one-boot flag):**
   - Console "Reboot into safe mode" writes `/var/lib/andaru/safe-mode-next` atomically and reboots.
   - Early in `andaru realize boot` (`andaru-network-boot.service`), if `/var/lib/andaru/safe-mode-next` exists or `/proc/cmdline` contains the exact token `andaru.safe=1`, it creates `/run/andaru/safe-mode` (0644) and removes `/var/lib/andaru/safe-mode-next` after syncing.
   - Because `andaru-network-boot.service` runs `Before=systemd-networkd.service` and add-on Quadlet units run `After=andaru-network-boot.service` with all four `Condition*` lines, add-ons never start during safe mode—even if boot fails mid-way before moving the file.
2. **Settings-upgrade hold:**
   - `andaru realize boot` evaluates whether the saved head has pending upgrade decisions; if so, it writes `/run/andaru/addons-held`.
   - When the administrator resolves all upgrade decisions via `POST /api/v1/config/upgrade`, the engine calls `andaru realize addons-release-hold`, which removes `/run/andaru/addons-held` and starts enabled add-on units of the live generation (unless boot safe mode is also active).
3. **Visibility:**
   - `GET /api/v1/status/system` exposes `safeMode: { active: bool, reason: "boot" | "kernelArg" | "settingsHold" | "" }`.
   - Console status and the Web UI header/System widget show a clear safe-mode banner when active.

### 6. Event bus add-on socket and Android-style grants (`addon-bus`, #154)

1. **Socket and transport.**
   - `packaging/tmpfiles.d/andaru.conf` creates `/run/andaru-addons` (`0711 root root`).
   - `andaru-engine` binds `/run/andaru-addons/bus.sock` (`0666` inside the restricted directory, or group-scoped with bind-mount isolation—the socket is bind-mounted **only** into containers that have at least one granted bus topic, at `/run/andaru/bus.sock`).
   - Reuses the exact JSON-lines protocol in `internal/bus` (`hello`, `publish`, `subscribe`, `consume`, `ack`, `event`, `gap`, `detached`).
2. **Authentication (per-add-on runtime token).**
   - Unlike host systemd services (`andaru-outbound`), containers may share host UIDs or remap UIDs inside user namespaces, so `SO_PEERCRED` alone does not uniquely identify an add-on.
   - On each apply (and on engine start for the live generation), the engine generates a fresh 256-bit random token per enabled add-on with bus grants, writes it to `/run/andaru-addons/<slug>/bus-token` (mode `0400`, owned by the container's UID, mounted read-only at `/run/andaru/bus-token`), and registers `(user: "addon." + slug, token, grants)` on the bus.
   - Tokens live only in `/run` and engine memory—never in saved configuration or backups.
   - `bus.Bus` gains thread-safe `SetClient(user, token string, grants Grants)` and `RemoveClient(user string)`, which immediately closes any existing connection for `user` so revoked grants or rotated tokens take effect at once.
3. **Android-style grant enforcement.**
   - Only topics explicitly set to `"granted"` in the **applied** generation's `declaration.addons[].busGrants` are registered on the bus. Denied or unreviewed topics are absent from the client's `Grants`.
   - Add-ons requesting `notification/addon.<slug>` publish durable `notification.raised.v1` / `notification.cleared.v1` events with `source: "addon." + slug`. `internal/notify` already verifies `topic == "notification/" + e.Source`, preventing any add-on from spoofing core notification sources (`engine`, `delivery`, `notifications`) or another add-on's source.
   - Add-on notifications support `link` paths restricted to `/system/addons/<slug>` (or `/system/addons`) so an add-on cannot spoof links to unrelated core pages.
   - Every topic the manifest declares is optional at runtime: if an administrator denies a topic, the bus refuses that subscribe/publish request with an error frame, and the add-on continues operating without it.

### 7. Authorization, API, and Web UI (`addon-ui`)

1. **RBAC capabilities (`internal/authz`).**
   - Add two core capabilities to `internal/authz/catalog.go`:
     - `addons.read`: view catalog, installed add-ons, runtime status, health, logs, and add-on configuration pages (included in built-in `read-only` and `administrator` roles).
     - `addons.manage`: stage/remove container images, start/stop/restart applied add-on units (included in built-in `administrator` role).
   - Saving add-on settings, zone attachments, and bus grants continues to require `configuration.write`; applying and confirming continue to require `configuration.apply` and `configuration.confirm`.
   - Manifest-level `permissions` (`view`, `configure`, `apply`, `manage`, `logs`, `metrics`) map onto these capabilities in Slice 1 (`view`/`logs`/`metrics` → `addons.read` + `configuration.read` for settings; `configure` → `configuration.write`; `apply` → `configuration.apply`; `manage` → `addons.manage`). Per-add-on fine-grained capabilities for custom roles (`addon.<slug>.<perm>`) can be layered later without changing the manifest format.
2. **Engine API endpoints (`/api/v1/addons/...`).**
   - `GET /api/v1/addons` (`addons.read`): catalog manifests joined with staged image state, saved/applied config summary, live systemd unit status, health status, and unreviewed bus-permission count.
   - `GET /api/v1/addons/{slug}` (`addons.read`): full manifest, staged image status, runtime/health status, effective applied vs saved state.
   - `POST /api/v1/addons/{slug}/stage` (`addons.manage`): pulls pinned image digest into local Podman storage (audited).
   - `DELETE /api/v1/addons/{slug}/stage` (`addons.manage`): removes unused staged image when add-on is disabled in both saved and live generations (audited).
   - `POST /api/v1/addons/{slug}/{start|stop|restart}` (`addons.manage`): manual lifecycle action on the applied generation's unit (audited).
   - `GET /api/v1/addons/{slug}/logs/{name}` (`addons.read`): bounded tail of journal or declared file log stream (cursor/line-limited, safe path open).
   - Add-on settings, zone bindings, and bus grants are read and written through the existing configuration API (`GET/PUT /api/v1/config` or dedicated sub-resource backed by the same optimistic-locked `config.Store` commit), keeping one version history, one Review-and-apply diff, and one rollback model.
3. **Web UI (`web/src`).**
   - **Navigation:** single entry **System → Add-ons** (`/system/addons`, visible when holding `addons.read`), beside Updates, Backups, and Notifications. Installed add-ons do not add custom pages to the left navigation tree in Slice 6.
   - **Add-on management page (`/system/addons`):** lists catalog add-ons with status, health, installed vs available version/digest, unreviewed bus-permission count, and the safe-mode banner:
     - **Install** (for not-installed add-ons): integrated "Stage image and continue" flow that pulls the pinned digest outside the apply window, then adds the add-on to saved configuration (opening its Settings page to configure required fields / review bus grants before applying).
     - **Update** (for installed add-ons whose catalog digest advanced after an Andaru update): stages the new digest outside the apply window and surfaces any newly requested bus permissions before applying.
     - **Settings** (for installed add-ons): opens `/system/addons/<slug>`.
     - **Uninstall** (for installed add-ons, with explicit confirmation dialog): removes the add-on from saved configuration so stopping and removing its Quadlet goes through the normal Apply → Rollback timer → Confirm cycle (preventing lockout if an administrator is connected through that add-on, e.g. Tailscale); once confirmed, removes `/var/lib/andaru/addons/<slug>` and unreferenced staged image.
   - **Add-on Settings page (`/system/addons/<slug>`):** accessed from `/system/addons` for installed add-ons, rendering everything declared in the manifest with Mantine controls and static CSS (no inline style props, preserving `style-src 'self'` and `script-src 'self'`):
     - Declarative `properties` form with visible Markdown descriptions below controls (`aria-describedby`), raw HTML disabled and sanitized, and selected-option descriptions for single-select enums.
     - Write-only secret inputs using the existing `secretWrite` pattern (never displaying secret values).
     - Interface-to-zone selectors (when `interfaces` are declared) and `zone-reference` selectors from `/api/v1/config` zones.
     - **Event bus permissions** section (when `bus` topics are declared) listing each requested publish/subscribe topic with its title, description, and Grant/Deny toggle (highlighting newly added unreviewed requests).
     - Runtime status, health, Start/Stop/Restart controls (operating on the applied generation), bounded log viewer (when `logs` are declared), and separate external WebUI link (when `webui` is declared, `target="_blank" rel="noopener noreferrer"`, noting separate authentication).
   - **Dashboard widgets:** deferred to a later slice after core add-on pages ship (matching [2026-11-21-web-ui-dashboard-design.md](2026-11-21-web-ui-dashboard-design.md)).

### 8. Backups, resets, and updates

1. **Portable backups (`internal/backup` / `internal/config`).**
   - `declaration.addons` travels automatically inside `configuration.json`.
   - `addon:<id>` secrets travel inside `configuration-secrets.json` under the same `backups.exportSecrets` + password-encrypted archive requirement as `core` secrets; default exports omit secret values and mark restored references `unresolved: true`.
   - Restoring a backup with enabled add-ons succeeds in saving the configuration, marks missing images as needing staging (`ADDON_IMAGE_NOT_STAGED` on preview/apply), and asks the administrator before staging/enabling rather than silently pulling images from the Internet during restore.
   - Backing up durable add-on data volumes (`storage[].backup == true`) is explicitly scoped as a follow-up backup format increment once an add-on declares durable backupable data. Tailscale's `/var/lib/tailscale` node-key state is marked `backup: false` so restored boxes re-authenticate cleanly and avoid duplicate Tailscale node identities.
2. **Full factory reset (`internal/reset`).**
   - Before wiping `/var/lib/andaru`, stops all `andaru-addon-*.service` units, removes `/run/containers/systemd/andaru-addon-*.container` and `/run/andaru-addons/`, runs `systemctl daemon-reload`, and removes Andaru-managed add-on containers/images from Podman storage. `/var/lib/andaru/addons/` is wiped with the rest of the wipeable tree.
3. **Soft reset (`2026-11-19-first-boot-safe-mode-resets-design.md`).**
   - The soft-reset declaration removes `declaration.addons`.
   - After step 2's network apply is confirmed, step 3 stops add-ons, wipes `/var/lib/andaru/addons/`, and drops add-on secret scopes. If step 2 rolls back, add-ons and their state remain untouched.

---

## First add-on: Tailscale (`addon-tailscale`)

### Lab qualification gate (must pass before locking `addon-runtime` SELinux/Quadlet flags)

Per Slice 6 of [2026-11-18-appliance-image-design.md](2026-11-18-appliance-image-design.md), run a bounded lab check on the Fedora CoreOS Andaru VM with SELinux enforcing:

1. Pull a digest-pinned official `ghcr.io/tailscale/tailscale@sha256:...` image for x86_64 (and verify aarch64 manifest-list digest).
2. Run it via a system Podman Quadlet under `/run/containers/systemd/andaru-addon-tailscale.container` with:
   - `Network=host`, `Pull=never`
   - `DropCapability=ALL`, `AddCapability=NET_ADMIN`, `AddCapability=NET_RAW` (test whether `NET_RAW` is actually needed)
   - `AddDevice=/dev/net/tun`
   - `NoNewPrivileges=true`
   - `Volume=/var/lib/andaru/addons/tailscale/state:/var/lib/tailscale:Z`
   - `TS_EXTRA_ARGS=--netfilter-mode=off` (and `TS_USERSPACE=false` to verify real kernel `tailscale0` creation)
3. Verify:
   - Zero SELinux AVC denials (or record the minimal `.cil` rules needed in `image/files/usr/share/andaru/selinux/andaru.cil`).
   - `tailscale0` comes up without `systemd-networkd` touching it (`51-andaru-unclassified.network` ignores it).
   - `nft list ruleset` shows only Andaru's `inet andaru` table (zero Podman or Tailscale tables/chains).
   - Adding `"tailscale0"` to a zone's interface-name set enforces Andaru firewall rules on traffic entering/leaving `tailscale0`.
   - Rebooting with `andaru.safe=1` prevents `andaru-addon-tailscale.service` from starting while the network and filter come up normally.
   - Record findings in `docs/research/tailscale-quadlet-spike.md`.

### Provisional Tailscale manifest sketch

- `id`: `org.andaru.tailscale`, `slug`: `tailscale`
- `runtime`: `network: "host"`, `profile: "network-interface"`, `capabilities: ["NET_ADMIN"]`, `devices: ["/dev/net/tun"]`, `args: ["--netfilter-mode=off"]`
- `interfaces`: `[{ "name": "tailscale0", "title": "Tailscale tunnel", "zoneRequired": true }]`
- `storage`: `[{ "name": "state", "purpose": "state", "mountPath": "/var/lib/tailscale", "backup": false }]`
- `properties`:
  - `authKey` (`secret-reference`, optional, bound to `file` -> `ANDARU_SECRET_AUTH_KEY_FILE` / `TS_AUTHKEY` file or `TS_AUTH_ONCE`)
  - `hostname` (`string`, `format: "hostname"`, optional, bound to `environment` `TS_HOSTNAME`)
  - `acceptRoutes` (`boolean`, default `false`)
  - `advertiseRoutes` (`string` / CIDR list, optional)
  - `advertiseExitNode` (`boolean`, default `false`)
- `logs`: `application` (`source: { type: "runtime" }`)

---

## Commands, testing strategy, and boundaries

### Commands

- Go unit and integration tests: `go test ./...`
- Focused package tests during development: `go test ./internal/addon/... ./internal/config/... ./internal/render/... ./internal/generation/... ./internal/bus/... ./internal/engine/...`
- Web UI tests & typecheck: `cd web && npm test && npm run build`
- Container image lint (CI / lab): `podman build -f image/Containerfile .` (runs `bootc container lint --fatal-warnings`)

### Testing strategy

- **`safe-mode`:** unit tests for `realize boot` flag transition (`/var/lib/andaru/safe-mode-next` -> `/run/andaru/safe-mode`), `andaru.safe=1` cmdline parsing, settings-hold flag creation/release, and `status/system` API response.
- **`addon-catalog`:** table-driven manifest parser tests covering valid manifests, unknown fields (exact JSON Pointer path), duplicate JSON keys, duplicate IDs/slugs/names/bindings, reserved `ANDARU_*` env rejection, forbidden bus topics (`notification/#`, core prefixes), `null` setting rejection, default persistence, `false`/`0`/`""` preservation, multi-select enum encoding, and `addon:<id>` secret isolation + optimistic locking.
- **`addon-runtime`:** golden tests for rendered Quadlet `.container` files (verifying `Pull=never`, all four `Condition*` safe-mode lines, dropped capabilities, volume/secret mounts, safe systemd argument quoting); `nft.go` tests confirming add-on interface zone sets; `Realizer` integration tests covering activate, health-check failure + rollback, confirm, boot, manual lifecycle refusal in safe mode, and factory/soft reset cleanup.
- **`addon-bus`:** unit and Unix-socket tests for token auth, wrong/missing token rejection, dynamic `SetClient` / `RemoveClient` closing live revoked connections, denied grant refusal, and `notification/addon.<slug>` end-to-end delivery (plus forged-source rejection).
- **`addon-ui`:** Vitest + Testing Library tests for catalog management, image staging UI, declarative form rendering (all property types, sanitized Markdown with raw HTML escaped, `aria-describedby`, write-only secrets, zone picker, bus grant review toggles, external WebUI link without credentials, capability-gated read-only views) plus built-asset CSP verification.
- **`addon-tailscale`:** Proxmox Fedora CoreOS VM lab test proving SELinux enforcement, zero extra nft tables, zone rule enforcement on `tailscale0`, apply/rollback, and safe-mode boot.

### Boundaries

- **Always:**
  - Keep `andaru-engine` unprivileged; delegate all Podman, systemd, and `/run/containers/systemd` mutations to `andaru realize`.
  - Enforce `Pull=never` in generated Quadlets; never pull images inside the apply rollback window or on boot.
  - Validate manifests and settings fail-closed at load, save, and apply.
  - Include all four safe-mode `Condition*` directives on every generated add-on unit.
  - Keep third-party code out of `andaru-engine` and Andaru's browser origin.
- **Ask first:**
  - Any schema bump beyond `schemaVersion: 4` or breaking change to existing `declaration` / backup format fields.
  - Adding any capability to the `network-interface` allowlist beyond `NET_ADMIN` / `NET_RAW` or any device beyond `/dev/net/tun`.
  - Adding bridged/pasta container networking or external (non-catalog) add-on repositories.
  - Adding new Go or npm dependencies.
- **Never:**
  - Render `Privileged=true`, arbitrary host bind mounts, or Podman/Docker socket mounts into an add-on Quadlet.
  - Pass user settings through a shell (`sh -c`).
  - Forward Andaru's session cookie, CSRF token, or API token to an add-on WebUI.
  - Allow any add-on to subscribe to `notification/#` or publish under a core notification source.
  - Silently disable an incompatible add-on during an upgrade.

## Deferred (explicitly out of Slice 6)

- Custom add-on navigation pages / multi-page `menu` trees (`menu.parent` + `menu.children` attaching to top-level nav branches; Slice 6 uses **System → Add-ons** and per-add-on Settings pages at `/system/addons/<slug>`).
- External/third-party signed add-on catalogs and independent add-on update channels (manifest/config contracts stay source-agnostic so these plug in later without redesign).
- Per-add-on custom-role RBAC capabilities (`addon.<slug>.<perm>`).
- Bridged / rootless `pasta` container networking and nftables coexistence for non-host-networked add-ons.
- Add-on dashboard widgets.
- Durable add-on data-volume export in portable backups (`storage[].backup == true`).
- `certificate-reference` and `alias-reference` property types (wait for core certificate management and add-on alias binding).

