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
  - **RBAC granularity in Slice 6:** map manifest permissions onto core `addons.read`, `addons.manage`, and existing `configuration.*` capabilities; per-add-on custom-role capabilities (`addon.<slug>.<perm>`) come in a later slice.
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
     - `profile`: `"unprivileged"` (default: `DropCapability=ALL`, `NoNewPrivileges=true`, non-root UID) or `"network-interface"` (drops all capabilities except those explicitly declared from an allowlist—initially `NET_ADMIN`, `NET_RAW`—plus declared device paths from an allowlist—initially `/dev/net/tun`—with `NoNewPrivileges=true`; `privileged: true` is never representable). **Trust boundary:** because Slice 6 combines `Network=host` with `NET_ADMIN`, `profile: "network-interface"` grants host-network-namespace privilege (including the kernel ability to alter host routes or firewall rules if the container is compromised; `--netfilter-mode=off` prevents accidental firewall conflicts, not adversarial tampering). This profile is therefore restricted to curated, image-shipped, digest-pinned network add-ons under SELinux confinement (`container_t` / minimal policy verified in the lab gate); stronger network-namespace isolation waits for the dedicated bridged/netns coexistence design.
     - `settingsActivation`: `"recreate"` (only allowed value in Slice 6; live `"reload"` is deferred until a reload-signal contract is defined).
     - `stopTimeoutSeconds`: bounded graceful stop timeout (default `5`, max `10`, keeping stop + health verification inside `Realizer`'s 45s `activateLimit` and 20s `restoreLimit`).
   - `interfaces` (optional array): kernel network interfaces created by the add-on (e.g. `{ "name": "tailscale0", "title": "Tailscale tunnel", "zoneRequired": true }`). Names must match Linux `IFNAMSIZ` (`^[a-z][a-z0-9_-]{0,14}$`), must not match `en*`, `eth*`, `lo`, `andaru*`, or `podman*`, and must be unique across all catalog add-ons.
   - `storage` (optional array): named subdirectories under `/var/lib/andaru/addons/<slug>/<name>` with `name` matching `^[a-zA-Z][a-zA-Z0-9_]{0,63}$`, `purpose` (`state`, `data`, `cache`, `logs`), `mountPath` (absolute container path), `readOnly` (default `false`), and `backup` (`bool`). Host paths are never author-supplied.
   - `endpoints` (optional array): `{ name, protocol, port, exposure }` (`exposure`: `"internal"` loopback-only or `"managed"`). Declaring an endpoint requests metadata for health/metrics/WebUI; it opens no firewall port automatically.
   - `bus` (optional object): `{ "publish": [...], "subscribe": [...] }`, each entry `{ "topic": "<filter>", "title": ..., "description": ... }`.
     - Validation rejects any `publish` or `subscribe` filter whose first level is `#` or `+`, and runs topic-overlap checks against all forbidden namespaces (`notification/#`, `delivery/#`, `alias-fetch/#`, `alias-result/#`, `config/#`, `apply/#`, `notifications/#` for `publish`; `notification/#`, `delivery/#`, `alias-fetch/#` for `subscribe`).
     - As the single exception to `notification/#` on `publish`, an add-on may request the exact topic `notification/addon.<slug>`.
   - `logs` (optional array): `runtime` (container journal stream via `journalctl` for `andaru-addon-<slug>.service`) or `file` (relative path under a declared `storage` entry, no `..`, opened with `O_NOFOLLOW` / `openat2`).
   - `metrics` (optional array): Prometheus scrape target on an `"internal"` endpoint.
   - `health` (optional object): HTTP probe on an `"internal"` endpoint (`endpoint`, `path`, `expectedStatus`, `intervalSeconds`, `timeoutSeconds`, `startPeriodSeconds`) or container `healthCmd` argument vector (must avoid shell form; per [podman-compose-spike.md](../../research/podman-compose-spike.md), exec-form args without embedded shell parsing).
   - `webui` (optional object): `{ title, permission, endpoint, hostnameSetting, path }` for a separate external link. `hostnameSetting` is required and must reference a `string` property with `format: "hostname"` so an add-on WebUI always lives on a distinct hostname rather than sharing Andaru's management host on another port (browsers scope `__Host-andaru_session` cookies by hostname regardless of port).
3. **Property types, grammar, and wire serialization.**
   - Property `name`: `^[a-zA-Z][a-zA-Z0-9_]{0,63}$`.
   - Supported `type` values: `string` (with optional `format`: `hostname`, `uri`, `ipv4`, `ipv6`, `cidr`, `date`, `date-time`; `minLength`, `maxLength`, `pattern`; URI userinfo always rejected), `string-list` (array of unique strings with optional `itemFormat`: `hostname`, `ipv4`, `ipv6`, `cidr`; `minItems`, `maxItems`, requiring explicit `encoding`), `boolean`, `integer`, `number`, `enum` (array of unique option strings, `minItems`, `maxItems`), `secret-reference`, `zone-reference`. (`certificate-reference` and `alias-reference` are reserved types rejected until core certificate management and add-on alias binding exist.)
   - **Binding grammar:**
     - `environment`: `name` matches `^[A-Z_][A-Z0-9_]{0,63}$` (omitted defaults to property `name` if valid); `ANDARU_*` prefix is reserved for platform-injected variables and rejected in author bindings.
     - `argument`: `name` matches `^--?[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`. Booleans support `style: "flag"` (`true` emits `name`, `false` omits it); without `style: "flag"`, emits `[name, "true"|"false"]`.
     - `file` (for `secret-reference`): `name` matches `^[a-zA-Z][a-zA-Z0-9_]{0,63}$`, optional `format` (`raw`, `htpasswd`), optional `env` (`^[A-Z_][A-Z0-9_]{0,63}$`, non-`ANDARU_*`) and `prefix` (e.g. `"file:"`, for containers like Tailscale's `containerboot` that read `TS_AUTHKEY=file:/run/andaru/secrets/andaru-addon-<slug>-<name>.secret`). Delivered inside the container's read-only secrets directory at `/run/andaru/secrets/andaru-addon-<slug>-<name>.secret` (matching the generation-flat filename in section 4) and exposed via `ANDARU_SECRET_<UPPER_NAME>_FILE` (plus `env` with optional `prefix` when declared), all pointing to that exact container path.
     - **Multi-value encoding (`enum` with `maxItems != 1` and `string-list`):** binding must specify either `"encoding": { "mode": "join", "separator": "," }` (valid for `environment` or `argument`, rejecting values that contain the separator) or `"encoding": { "mode": "repeat" }` (valid for `argument` only, emitting `[name, v1, name, v2]`). Single-select enums (`minItems <= 1 && maxItems == 1`) unwrap their sole value automatically; empty arrays (`[]` when `minItems == 0`) emit no binding.
     - **Collision checks:** across the whole manifest, duplicate environment names (including `file.env`), duplicate non-repeated argument names, duplicate secret file names, any two `storage[].name` entries whose uppercased `ANDARU_STORAGE_<UPPER_NAME>` collide, and any two `secret-reference` `file.name` entries whose uppercased `ANDARU_SECRET_<UPPER_NAME>_FILE` collide fail manifest load.
   - **Null and default semantics:** explicit `null` values in settings are rejected; defaults resolve and persist on save; explicit `false`, `0`, and `""` are preserved; missing optional settings without defaults emit no binding.
4. **Platform-injected runtime environment variables.** Reserved `ANDARU_*` variables passed to containers automatically when applicable:
   - `ANDARU_ADDON_ID`, `ANDARU_ADDON_SLUG`
   - `ANDARU_STORAGE_<UPPER_NAME>` for each declared `storage` mount path
   - `ANDARU_SECRET_<UPPER_NAME>_FILE` for each bound `secret-reference` file
   - `ANDARU_BUS_SOCKET=/run/andaru/bus.sock` and `ANDARU_BUS_TOKEN_FILE=/run/andaru/bus/token` (only when the add-on has at least one granted bus topic)

### 3. Saved configuration and secret namespaces (`addon-catalog`)

1. **Declaration model (`internal/config`).**
   - Add an optional `addons` slice (`json:"addons,omitempty"`) to `declaration` in schema v4 (additive, backward-compatible with existing v4 documents without requiring a breaking schema bump).
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
   - Saving validates `settings` against the catalog manifest's derived schema, verifies `interfaces[].zone` references existing zones (and enforces `zoneRequired`), verifies every `busGrants` key exists in the manifest's requested topics, rejects any `webui.hostnameSetting` value that matches Andaru's appliance hostname, local management FQDN (`<hostname>.<dns.localDomain>`), or any IP address on the management segment (case-insensitive, ignoring trailing dot), and checks optimistic locking (`expectedSavedVersion` + secret `expectedVersion`).
   - Deleting a zone referenced by any saved add-on (enabled or disabled) via either `interfaces[].zone` or a `zone-reference` setting is rejected by cross-reference validation (same rule as segments and firewall rules).
2. **Secret namespaces (`internal/config/secrets.go`).**
   - Extend `secretDocument.Scopes` (and `secret-schema.json`, `backup-secret-schema.json`, `secrets.go`, `changes.go`) from `"core"`-only to allow `"addon:<id>"` scopes alongside `"core"` with `kind: "addon"`, using the existing `secretEntry` format (`value`, `updatedAt`, `version`), `secretPrecondition`, and write-only `secretWrite` / `secretRef` API contract.
   - Extend `config.RealizationInput` and `render.Input` with scoped `AddonSecrets map[string]map[string]string` (keeping `render.Input.Secrets` for `"core"` so namespaces never collide).
   - An add-on's settings may reference only secrets in its own `"addon:<id>"` namespace (administrator-assigned shared core secrets can be added later with an explicit ACL table; no cross-add-on secret reading is permitted).
   - Unresolved secret references (for example, after restoring a secrets-excluded backup) block applying an enabled add-on, matching link password references (`checkResolvedReferences`).
   - Uninstalling an add-on drops its entry from `declaration.addons` and removes its `"addon:<id>"` secret scope from the new saved head; the prior generation's rendered files remain intact on disk so rollback restores it cleanly.
3. **Apply boundary.**
   - `declaration.addons` is included in `RealizationInput`. Any add-on enable/disable, setting change, interface zone change, or bus-grant change sets `applies.unapplied = true` and takes effect only through Preview → Apply → Rollback timer → Confirm.

### 4. Runtime execution, Podman Quadlet, and offline recovery (`addon-runtime`)

1. **Privilege separation.**
   - `andaru-engine` stays unprivileged (`CapabilityBoundingSet=`, `ProtectSystem=strict`, `RestrictAddressFamilies=AF_UNIX AF_NETLINK`). It renders desired files into the generation directory and invokes `andaru realize` through PID 1 (`systemdRunner`).
   - `andaru realize` writes Quadlet files, runtime secret/token files, reloads systemd, manages add-on units, and executes image staging commands.
2. **Explicit image staging (never pull during apply).**
   - Container images are **never** pulled during `realize activate`, boot, or rollback. Pulling over WAN inside the 45-second apply timeout would break network reconfigurations and offline boots.
   - Staging runs via `POST /api/v1/addons/{slug}/stage`, invoking `andaru realize addon-stage <slug>` directly in `cmd/andaru/realize.go` (outside `generation.Realizer`, like `updateVerb`, so it never holds the generations lock or hits `Realizer.Run`'s 10-second command cap). It runs `podman pull <digest>` using the catalog's pinned digest and verifies the pulled image ID/digest in local storage. In the Web UI, **Install** / **Update** runs this stage step to completion before saving/enabling, strictly outside the apply rollback timer.
   - `GET /api/v1/preview/network` and `POST /api/v1/applies` fail closed with a blocking finding (`ADDON_IMAGE_NOT_STAGED`) if an enabled add-on's pinned digest is not present in local Podman storage.
   - **Offline rollback retention:** local Podman storage retains any digest referenced by either the `Confirmed` or `LastKnownGood` generation; `DELETE /api/v1/addons/{slug}/stage` and post-uninstall/post-upgrade cleanup never remove a digest still referenced by `LastKnownGood`, so boot fallback and `bootc rollback` start the prior generation's container without network access.
3. **Quadlet rendering in generations (`internal/render` + `internal/generation`).**
   - Because `internal/generation/store.go` stores generation files flat by `filepath.Base(f.Path)`, every rendered add-on file uses a globally unique basename, and `internal/generation` extends `FileEntry`/`modeFor`/`placed` to support mode `0400` and container UID/GID ownership:
     - `/run/containers/systemd/andaru-addon-<slug>.container` (mode `0600`, root-owned)
     - `/run/andaru-addons/<slug>/andaru-addon-<slug>.env` (mode `0600`, root-owned, non-secret validated environment variables, only if non-empty)
     - `/run/andaru-addons/<slug>/secrets/andaru-addon-<slug>-<name>.secret` (mode `0400`, owned by the container's runtime UID, only for bound `secret-reference` properties; the container mounts the `/run/andaru-addons/<slug>/secrets` directory read-only at `/run/andaru/secrets` so atomic updates never pin stale inodes)
   - Every generated `.container` file contains:
     - `[Unit]` with `After=andaru-network-boot.service systemd-networkd.service andaru-engine.service` and the fail-closed safe-mode conditions:
       ```ini
       ConditionPathExists=!/run/andaru/safe-mode
       ConditionPathExists=!/var/lib/andaru/safe-mode-next
       ConditionPathExists=!/run/andaru/addons-held
       ConditionKernelCommandLine=!andaru.safe=1
       ```
     - `[Container]` with `Image=<pinned-digest>`, `Pull=never`, `Network=host`, `NoNewPrivileges=true`, `DropCapability=ALL` (plus only allowlisted `AddCapability=` / `AddDevice=` for curated `network-interface` add-ons—which are explicitly trusted in the host network namespace as qualified in section 2—under SELinux confinement), `EnvironmentFile=` (if present), `Exec=` (argument vector rendered safely with systemd quoting), `Volume=` binds for declared `storage` under `/var/lib/andaru/addons/<slug>/<name>` (`:Z`), read-only per-add-on directory mounts for secrets (`/run/andaru-addons/<slug>/secrets:/run/andaru/secrets:ro,Z` — `,Z` is required under SELinux enforcing so Podman relabels the per-add-on `/run` directory from `var_run_t` to per-container `container_file_t:s0:<MCS>`), and—only when bus grants exist—the bus token directory mount (`/run/andaru-addons/<slug>/bus:/run/andaru/bus:ro,Z` plus `/run/andaru-addons/bus.sock:/run/andaru/bus.sock`).
     - `[Service]` with `Restart=on-failure`, `RestartSec=5`, `TimeoutStopSec=<stopTimeoutSeconds>`.
     - No `[Install]` `WantedBy=multi-user.target` reliance for boot: `andaru-network-boot.service` runs in `sysinit.target` after PID 1 has already run `podman-system-generator` and computed the `multi-user.target` transaction. Instead, `andaru realize boot` writes `/run/andaru/addons-held` (fail-closed) and places the Quadlet files; once `andaru-engine` starts, verifies no settings-upgrade hold is active, and registers bus tokens/grants, it invokes `andaru realize addons-release-hold`, which removes `/run/andaru/addons-held`, runs `systemctl daemon-reload`, and starts enabled add-on units with `systemctl start --no-block` (matching `internal/generation/pppoe.go`; systemd still skips every unit if boot safe mode is active).
   - **Activation and rollback in `internal/generation`:**
     - `realize activate` writes the new generation's Quadlet/env/secret files into `/run`, removes stale `andaru-addon-*.container` files from the prior generation, runs `systemctl daemon-reload`, and stops removed/disabled add-on units. During normal operation (neither boot safe mode nor settings hold is active), it restarts changed add-on units (concurrently where safe so total stop + health wait stays strictly inside `activateLimit` 45s and `restoreLimit` 20s, and every `systemctl` call stays under `Realizer.Run`'s 10s per-command cap) and verifies readiness against each enabled add-on's declared health check (or active unit state if none declared) within a bounded budget. When boot safe mode (`/run/andaru/safe-mode` or `andaru.safe=1`) or settings hold (`/run/andaru/addons-held`) is active, `realize activate` skips starting and readiness-checking held add-on units so network recovery applies succeed while safe mode holds add-ons off.
     - **Failure and rollback:** if activation fails or the confirmation timer expires, `realize rollback` restores the previous generation's Quadlet/env/secret files, runs `systemctl daemon-reload`, stops newly added units, and—unless safe mode or settings hold is active—restarts the prior generation's add-on units. A failed apply remains `failed` even after fallback succeeds.
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
2. **Settings-upgrade hold (fail-closed without opening `config.db` in `Boot`):**
   - `internal/generation` stays DB-free: `andaru realize boot` always writes `/run/andaru/addons-held` (0644) before returning, so add-ons stay off if `config.db` is unreadable or `andaru-engine` has not yet verified the saved head.
   - At engine startup, after opening `config.db` and `bus.sock`, the engine evaluates `UpgradePreview(ctx, nil)`. If no upgrade decisions are pending, it calls `andaru realize addons-release-hold`, which removes `/run/andaru/addons-held`, runs `systemctl daemon-reload`, and starts enabled add-on units of the live generation (unless boot safe mode is active).
   - If upgrade decisions are pending, `/run/andaru/addons-held` remains in place until cleared by either `POST /api/v1/config/upgrade` or a backup restore that saves a head requiring no decisions—both trigger `andaru realize addons-release-hold`.
3. **Visibility:**
   - `GET /api/v1/status/system` exposes `safeMode: { active: bool, reasons: ("boot" | "kernelArg" | "settingsHold")[] }` so simultaneous reasons (for example, boot safe mode plus settings hold) are never masked.
   - Console status and the Web UI header/System widget show a clear safe-mode banner when active.

### 6. Event bus add-on socket and Android-style grants (`addon-bus`, #154)

1. **Socket and transport.**
   - `packaging/tmpfiles.d/andaru.conf` creates `/run/andaru-addons` (`0700 root root`). `packaging/systemd/andaru-engine.service` adds `ReadWritePaths=-/run/andaru-addons` so the engine can bind `/run/andaru-addons/bus.sock` without weakening `ProtectSystem=strict` or `CapabilityBoundingSet=`. Because host traversal is unnecessary for container bind mounts, local non-root host processes cannot traverse `/run/andaru-addons` or reach `bus.sock`.
   - Under SELinux enforcing (verified in [tailscale-quadlet-spike.md](../../research/tailscale-quadlet-spike.md)), `image/files/usr/share/andaru/selinux/andaru.cil` adds the two minimal rules allowing confined `container_t` add-ons to connect to `andaru-engine`'s add-on bus socket: `(filecon "/run/andaru-addons/bus.sock" socket (system_u object_r container_file_t ((s0) (s0))))` and `(allow container_t unconfined_service_t (unix_stream_socket (connectto)))`.
   - `/run/andaru-addons/bus.sock` is bind-mounted **only** into containers that have at least one granted bus topic, at `/run/andaru/bus.sock`.
   - Reuses the exact JSON-lines protocol in `internal/bus` (`hello`, `publish`, `subscribe`, `consume`, `ack`, `event`, `gap`, `detached`).
2. **Authentication and listener isolation (per-add-on runtime token).**
   - Unlike host systemd services (`andaru-outbound`), containers may share host UIDs or remap UIDs inside user namespaces, so `SO_PEERCRED` alone does not uniquely identify an add-on.
   - `internal/bus` isolates the add-on listener from the root-only `bus.sock` listener (separate token-authenticated listener restricted to `addon.*` principals; root `bus.sock` never shares authentication or principals with add-ons, preventing any add-on from impersonating `realize`). Dynamic client tokens and grants are guarded by `b.mu` so `conn.read()` never races with updates.
   - On each apply (and on engine start for the live generation), the engine generates a fresh 256-bit random token per enabled add-on with bus grants, registers `(user: "addon." + slug, token, grants)` on the bus, and hands the token to `andaru realize` (via stdin or root-only state file) so `realize` writes and `chown`s `/run/andaru-addons/<slug>/bus/token` (mode `0400`, owned by the container's UID; the engine has no `CAP_CHOWN`). The container mounts `/run/andaru-addons/<slug>/bus` read-only at `/run/andaru/bus` (directory bind mount, so atomic renames propagate), and receives `ANDARU_BUS_TOKEN_FILE=/run/andaru/bus/token`.
   - Tokens live only in `/run` and engine memory—never in saved configuration or backups.
   - `bus.Bus` gains thread-safe `SetClient(user, token string, grants Grants)` and `RemoveClient(user string)`, which immediately closes any existing connection for `user` so revoked grants or rotated tokens take effect at once.
3. **Android-style grant enforcement.**
   - Only topics explicitly set to `"granted"` in the **applied** generation's `declaration.addons[].busGrants` are registered on the bus. Denied or unreviewed topics are absent from the client's `Grants`.
   - Add-ons requesting `notification/addon.<slug>` publish durable `notification.raised.v1` / `notification.cleared.v1` events with `source: "addon." + slug`. `internal/notify` already verifies `topic == "notification/" + e.Source`, preventing any add-on from spoofing core notification sources (`engine`, `delivery`, `notifications`) or another add-on's source.
   - For `addon.*` sources, `internal/notify` enforces that `link` (when non-empty) equals `/system/addons` or `/system/addons/<slug>` (exact match after path check, rejecting `..` or sibling prefixes like `/system/addons/<slug>2`), and enforces `requires: "addons.read"` (rejecting omitted or unrelated core capabilities so users without `addons.read` never see add-on notifications).
   - Every topic the manifest declares is optional at runtime: if an administrator denies a topic, the bus refuses that subscribe/publish request with an error frame, and the add-on continues operating without it.

### 7. Authorization, API, and Web UI (`addon-ui`)

1. **RBAC capabilities (`internal/authz`).**
   - Add two core capabilities to `internal/authz/catalog.go`:
     - `addons.read`: view catalog, installed add-ons, runtime status, health, logs, and add-on configuration pages (included in built-in `read-only` and `administrator` roles).
     - `addons.manage`: stage/remove container images, start/stop/restart applied add-on units (included in built-in `administrator` role).
   - Saving add-on settings, zone attachments, and bus grants continues to require `configuration.write`; applying and confirming continue to require `configuration.apply` and `configuration.confirm`.
   - Manifest-level `permissions` (`view`, `configure`, `apply`, `manage`, `logs`, `metrics`) map onto these capabilities in Slice 6 (`view`/`logs`/`metrics` → `addons.read`, plus `configuration.read` for saved/applied settings; `configure` → `configuration.write`; `apply` → `configuration.apply`; `manage` → `addons.manage`). Per-add-on fine-grained capabilities for custom roles (`addon.<slug>.<perm>`) can be layered later without changing the manifest format.
2. **Engine API endpoints (`/api/v1/addons/...`).**
   - `GET /api/v1/addons` (`addons.read`): catalog manifests joined with staged image state, live systemd unit status, health status, and unreviewed bus-permission count. Saved/applied setting values are omitted unless the caller also holds `configuration.read`.
   - `GET /api/v1/addons/{slug}` (`addons.read`): full manifest, staged image status, runtime/health status; saved/applied config state is included only when the caller also holds `configuration.read`.
   - `POST /api/v1/addons/{slug}/stage` (`addons.manage`): pulls pinned image digest into local Podman storage (audited).
   - `DELETE /api/v1/addons/{slug}/stage` (`addons.manage`): removes unused staged image when its digest is not referenced by the saved head, `Confirmed`, or `LastKnownGood` generations (audited).
   - `POST /api/v1/addons/{slug}/{start|stop|restart}` (`addons.manage`): manual lifecycle action on the applied generation's unit (audited).
   - `GET /api/v1/addons/{slug}/logs/{name}` (`addons.read`): bounded tail of journal or declared file log stream (cursor/line-limited, safe path open).
   - Add-on settings, zone bindings, and bus grants are read via `GET /api/v1/config` (`configuration.read`) and written via `POST /api/v1/config/changes` or dedicated `/api/v1/config/addons` collection routes (`configuration.write`), backed by the same optimistic-locked `config.Store` commit—keeping one version history, one Review-and-apply diff, and one rollback model.
3. **Web UI (`web/src`).**
   - **Navigation:** single entry **System → Add-ons** (`/system/addons`, visible when holding `addons.read`), beside Updates, Backups, and Notifications. Installed add-ons do not add custom pages to the left navigation tree in Slice 6.
   - **Add-on management page (`/system/addons`):** lists catalog add-ons with status, health, installed vs available version/digest, unreviewed bus-permission count, and the safe-mode banner:
     - **Install** (for not-installed add-ons): integrated "Stage image and continue" flow that pulls the pinned digest outside the apply window, then adds the add-on to saved configuration (opening its Settings page to configure required fields / review bus grants before applying).
     - **Update** (for installed add-ons whose catalog digest advanced after an Andaru update): stages the new digest outside the apply window and surfaces any newly requested bus permissions before applying.
     - **Settings** (for installed add-ons): opens `/system/addons/<slug>`.
     - **Uninstall** (for installed add-ons, with explicit confirmation dialog): removes the add-on from saved configuration (and its `"addon:<id>"` secret scope from the new saved head) so stopping and removing its Quadlet goes through the normal Apply → Rollback timer → Confirm cycle (preventing lockout if an administrator is connected through that add-on, e.g. Tailscale). Because confirming an apply moves the prior generation into `LastKnownGood`, `/var/lib/andaru/addons/<slug>` and the add-on's staged image digest are retained while either `Confirmed` or `LastKnownGood` (or the saved head) still references the add-on, ensuring boot fallback restores the prior generation with its state intact. Once no retained generation or saved head references the add-on (after a subsequent confirmed apply advances `LastKnownGood`, or during soft/factory reset), the engine garbage-collects `/var/lib/andaru/addons/<slug>` and prunes unreferenced staged digests.
   - **Add-on Settings page (`/system/addons/<slug>`):** accessed from `/system/addons` for installed add-ons, rendering everything declared in the manifest with Mantine controls and static CSS (no inline style props, preserving `style-src 'self'` and `script-src 'self'`):
     - Settings, zone selectors, and bus-permission forms render only when the session also holds `configuration.read` (read-only without `configuration.write`); users holding only `addons.read` see runtime status, health, logs, and the external WebUI link without hitting 403 on `/api/v1/config`.
     - Declarative `properties` form with visible Markdown descriptions below controls (`aria-describedby`), raw HTML disabled and sanitized, and selected-option descriptions for single-select enums.
     - Write-only secret inputs using the existing `secretWrite` pattern (never displaying secret values).
     - Interface-to-zone selectors (when `interfaces` are declared) and `zone-reference` selectors from `/api/v1/config` zones.
     - **Event bus permissions** section (when `bus` topics are declared) listing each requested publish/subscribe topic with its title, description, and Grant/Deny toggle (highlighting newly added unreviewed requests).
     - Runtime status, health, Start/Stop/Restart controls (operating on the applied generation), bounded log viewer (when `logs` are declared), and separate external WebUI link (when `webui` is declared, built from `hostnameSetting` on a separate origin from Andaru's management host, `target="_blank" rel="noopener noreferrer"`, noting separate authentication; if the configured hostname matches the browser's current `window.location.hostname` regardless of port, the link is suppressed with a warning so `__Host-andaru_session` is never sent to the add-on).
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

### Lab qualification gate (passed on 2026-11-27)

Completed on throwaway Fedora CoreOS Andaru `0.5.0-a.2` VM (`SELinux Enforcing`, Podman `6.1.3`); full evidence is recorded in [tailscale-quadlet-spike.md](../../research/tailscale-quadlet-spike.md):

1. Verified multi-arch OCI index digest `ghcr.io/tailscale/tailscale@sha256:72c1e19e0aedca96b8533bbb0b4368564410ff20b17e53b804eb6c1f04a31be4` covering both `linux/amd64` and `linux/arm64`.
2. Confirmed system Podman Quadlet under `/run/containers/systemd/andaru-addon-tailscale.container` with `Network=host`, `Pull=never`, `NoNewPrivileges=true`, `DropCapability=ALL`, `AddCapability=NET_ADMIN` alone (`NET_RAW` is **not** required), `AddDevice=/dev/net/tun`, `:Z` state mount, `:ro,Z` secrets mount, `TS_USERSPACE=false`, and `TS_EXTRA_ARGS=--netfilter-mode=off`.
3. Confirmed zero `container_t` SELinux AVC denials for Tailscale (and verified the 2-line CIL rule required for `addon-bus` `/run/andaru-addons/bus.sock`), `tailscale0` unmanaged by `systemd-networkd`, `nft list ruleset` containing only `table inet andaru`, dynamic `zone_vpn` interface-name set matching `tailscale0`, and fail-closed skipping under both `/run/andaru/safe-mode` and kernel arg `andaru.safe=1` across reboot.

### Provisional Tailscale manifest sketch

- `id`: `org.andaru.tailscale`, `slug`: `tailscale`
- `runtime`: `network: "host"`, `profile: "network-interface"`, `capabilities: ["NET_ADMIN"]`, `devices: ["/dev/net/tun"]`, static env (`TS_USERSPACE=false`, `TS_EXTRA_ARGS=--netfilter-mode=off`, aligned with official `ghcr.io/tailscale/tailscale` `containerboot` and verified by the lab gate)
- `interfaces`: `[{ "name": "tailscale0", "title": "Tailscale tunnel", "zoneRequired": true }]`
- `storage`: `[{ "name": "state", "purpose": "state", "mountPath": "/var/lib/tailscale", "backup": false }]`
- `properties`:
  - `authKey` (`secret-reference`, optional, `file` binding `name: "auth_key"`, `env: "TS_AUTHKEY"`, `prefix: "file:"` -> `TS_AUTHKEY=file:/run/andaru/secrets/andaru-addon-tailscale-auth_key.secret`)
  - `hostname` (`string`, `format: "hostname"`, optional, bound to `environment` `TS_HOSTNAME`)
  - `advertiseRoutes` (`string-list`, `itemFormat: "cidr"`, optional, bound to `environment` `TS_ROUTES` with `encoding: { mode: "join", separator: "," }`)
  - `acceptRoutes` / `advertiseExitNode` (mapped via validated `containerboot` flags verified in the lab gate)
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

