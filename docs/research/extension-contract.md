# Extension contracts: reuse, avoid, unresolved

## Scope and conclusion

Research for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), not an approved extension specification. Compare pfSense/OPNsense, OpenWrt, Cockpit, and Unraid; use nfsense/nfSensei as supplementary references. Distinguish documented design lessons from our recommendations: no maintainer is assumed to endorse Andaru's choices.

**Recommendation:** use a small declarative manifest to generate Andaru's management page, with named backend operations, configuration schemas/migrations and explicit permissions. Keep extension code out of the privileged engine and Andaru frontend. Link an application's own WebUI separately. Do not build a universal plugin framework or compatibility shim for another firewall.

Andaru already requires an API-first GUI, restricted extension services, namespaced secrets, administrator-assigned shared secrets, explicit extension installation on restore, compatibility-first updates, and complete offline recovery. These constraints remain the baseline rather than conclusions of this research.

The [approved update policy](https://github.com/andaru-project/andaru/issues/13#issuecomment-6713617480) is fixed: extensions update independently with administrator approval. An incompatible installed extension **blocks a core upgrade**; offer an extension update or explicit removal, never silent disabling. The compatibility mechanisms below remain open, not these rules.

## Directly documented lessons

### pfSense: useful integration, excessive coupling for our boundary

Netgate's [Developing Packages](https://docs.netgate.com/pfsense/en/latest/development/develop-packages.html) describes package GUI fields, menu/service integration and configuration callbacks. The [Cron package](https://github.com/pfsense/FreeBSD-ports/blob/devel/sysutils/pfSense-pkg-Cron/files/usr/local/pkg/cron.xml) is a concrete example; the core [package installer](https://github.com/pfsense/pfsense/blob/master/src/etc/inc/pkg-utils.inc#L792-L928) evaluates package-defined PHP lifecycle callbacks. This is direct integration with privileged core processing, not a confined extension RPC contract.

The same developer guide documents that a masked `password` field is still stored in clear text in the XML configuration. Masking a form field does not establish a secret store or safe configuration export.

**Andaru lesson:** keep installable packages, declarative navigation and generated service configuration; do not copy in-process callbacks or treat ordinary settings as secret storage. Our separate secret namespaces and write-only UI are stronger requirements.

### OPNsense: separate UI, validated configuration, and privileged operations

OPNsense's [Basics and Future](https://docs.opnsense.org/development/guidelines/basics.html) is a first-party design retrospective, not an inferred opinion. It describes legacy maintenance debt and its transition to model-driven modules, REST APIs, and configd. It explicitly requires new modules to use APIs, prohibits directly including legacy components in new MVC code, and rejects arbitrary raw service configuration because undefined input can become privileged commands.

**Andaru lesson:** schema-checked values and named, authorized operations; no extension-supplied shell string or raw nftables fragment passed to the engine. Templates and privileged operations remain code-review boundaries, not safe merely because they are declarative files.

OPNsense's [HelloWorld example](https://docs.opnsense.org/development/examples/helloworld.html), [configd actions](https://docs.opnsense.org/development/backend/configd.html), and [model migrations](https://docs.opnsense.org/development/frontend/models_migrations.html) make the boundaries concrete: menu/model/API/backend actions are separate, configuration has an owned mount, and schema versions drive migration steps. The migration documentation warns about migration races; having hooks does not by itself make a multi-extension upgrade transactional.

### OpenWrt: composable integration, but ACLs are not page isolation

The official [LuCI example](https://github.com/openwrt/luci/blob/master/applications/luci-app-example/README.md) pairs JavaScript pages with JSON menu entries and rpcd ACL declarations. Its ACL section explicitly warns that grants are global to the web UI: granting access to the example's UCI configuration does not restrict that access to the example page. [UCI](https://openwrt.org/docs/guide-user/base-system/uci) organizes service settings into named configuration packages, not write-only secret fields.

The [procd service guide](https://openwrt.org/docs/guide-developer/procd-init-scripts) provides declarative lifecycle, reload and respawn controls, with optional jails. Service supervision alone does not establish a sandbox. The [ubus documentation](https://openwrt.org/docs/techref/ubus) describes named methods and session ACLs; those should not be assumed to constrain every local process.

The [sysupgrade source](https://github.com/openwrt/openwrt/blob/master/package/base-files/files/sbin/sysupgrade) can include an installed-package list (`-k`), but package names are not the executable artifacts needed for offline recovery. The [25.12 release notes](https://openwrt.org/releases/25.12/notes-25.12.0) and [opkg-to-apk guide](https://openwrt.org/docs/guide-user/additional-software/opkg-to-apk-cheatsheet) document a package-manager transition and warn against treating ordinary package upgrades as a coherent firmware upgrade.

**Andaru lesson:** reuse declarative integration and narrow RPC operations, but distinguish administrator permissions from extension identity and browser isolation. Keep public extension identities independent of a particular package-manager command, and retain exact artifacts/dependencies for offline recovery.

### Cockpit: explicit public contracts and small page manifests

Cockpit's [package documentation](https://docs.cockpit-project.org/cockpit-guide/latest/guide/packages.html) defines a JSON manifest with menu labels, ordering, relative page paths, and a minimum required Cockpit version. It explicitly says undocumented APIs/behaviour are internal and may change. Its manifest also permits CSP overrides and package name/priority overrides; those are flexibility mechanisms, not an extension-isolation guarantee.

**Andaru lesson:** stable extension identifiers, namespaced routes/menu entries, declared API requirements, and a clearly documented public surface. Do not treat a hidden menu item as authorization or permit a manifest to silently replace core pages.

### Unraid: approachable declarative controls, not a model to copy

Unraid's [official container guide](https://docs.unraid.net/unraid-os/using-unraid-to/run-docker-containers/managing-and-customizing-containers/) presents a template-driven form for network mode, port and volume mappings, and environment variables. It also documents a separate WebUI link, project/support links, logs and lifecycle controls, persistent application data, and container health status.

The [template implementation, pinned at `b135287`](https://github.com/unraid/webgui/blob/b135287c706a30da44f71cc7820615209e83e9a0/emhttp/plugins/dynamix.docker.manager/include/CreateDocker.php#L594-L637), binds `Name`, `Target`, `Default`, `Description`, `Type`, `Display`, `Required`, `Mask`, and `Value` to generated controls. Types include paths, ports, variables, labels and devices; basic/advanced display modes reduce visible complexity. `Mask` switches an input to password display: it is not evidence of separate secret storage. The [same source](https://github.com/unraid/webgui/blob/b135287c706a30da44f71cc7820615209e83e9a0/emhttp/plugins/dynamix.docker.manager/include/CreateDocker.php#L1151-L1169) exposes extra runtime parameters and post arguments.

These are inspirations for authoring and user experience, **not** an endorsement of Unraid's XML, security boundary, or full runtime configuration model. The guide recommends arranging startup order with wait times; Andaru should instead use bounded readiness checks where a dependency genuinely requires them. Do not infer that Unraid supplies Andaru's proposed JSON Schema validation or conditional-field rules.

**Andaru lesson:** generate approachable controls with labels, descriptions, defaults and advanced grouping; explicitly bind settings to runtime inputs. Separate semantic validation from presentation, masking from secret handling, and requested ports/mounts from permission to expose host resources. No arbitrary extra-arguments field as an escape from policy.

### systemd: packaging and runtime confinement are separate

[systemd-sysext(8), version 257](https://www.freedesktop.org/software/systemd/man/257/systemd-sysext.html) says system extensions merge files into host `/usr` and `/opt`, imply **no security isolation**, and are not a generic package/dependency framework. Its host/extension compatibility metadata is not an Andaru API compatibility contract.

**Andaru lesson:** systemd sandboxing must be applied to running extension services; an immutable image or signed artifact does not establish least privilege. Installation and migration hooks need their own trust analysis. Do not assume an extension's service restrictions also restrict a package's installation scripts.

### Supplementary peers: useful direction, limited inspectable contracts

nfsense's [roadmap](https://git.soontm.de/speatzle/nfsense/issues/4) places “Plugin System via Sysext” under unchecked **Future** work. It is not evidence of a shipped extension contract; the systemd restrictions above apply regardless.

nfSensei's [homepage](https://nfsensei.org/) describes NFPacks with declarative YAML and sandboxed Lua. Its maintainer's [engineering retrospective](https://blog.nfsensei.org/two-weeks-of-shipping.html), section 8, describes replacing ad hoc hook arguments with typed context, canonical events, versioned IPC, validated declarations and maintained examples. It also reports fixing a hook registry that used synthetic events rather than real commits. These are first-party reports, not independently verified implementation behaviour: its detailed [documentation](https://docs.nfsensei.org/) requires beta credentials, and no public implementation was established in this research.

**Andaru lesson:** use typed hook/event payloads tied to actual committed state, explicit protocol versions and a small maintained example. Do not adopt Lua, YAML, or another runtime merely because a peer uses it; nor claim its sandbox has been audited. No maintainer interviews were conducted; public retrospectives and migration notices are the evidence for lessons learned.

## Reuse

1. **A small declarative manifest.** Identity/version, required public API, menu/pages, config schema, runtime service requirements, and backup-state ownership. Validate required fields and reject unsupported required capabilities; a manifest requests privileges but cannot grant them.
2. **Backend-enforced permissions.** Authenticate the extension process as well as the administrator. A frontend must not obtain arbitrary administrative power simply because an administrator opened its page.
3. **Versioned configuration and explicit migrations.** Keep each extension's settings/secrets namespace distinct, and validate references through the same save/apply boundaries as core settings. Compatibility follows supported contracts, not identical core release numbers.
4. **Stock service supervision.** Andaru owns generated systemd units, lifecycle, health reporting and credential delivery. Extensions contribute scoped requests, not unrestricted unit overrides or a second packet-filter writer.
5. **A complete lifecycle contract.** Install, migrate, start/stop, backup/restore and removal must identify owned state and retained artifacts. Offline recovery must restore a compatible extension executable/data pair, not merely reinstall its latest package from the Internet.

## Avoid

1. Importing extension code into the root engine, exposing arbitrary execution endpoints, or allowing raw privileged service configuration as an escape hatch.
2. Equating signing, a systemd unit, a filesystem image, or hiding navigation with a security boundary. Each protects a different part of the system.
3. Injecting third-party JavaScript into Andaru's administrator session. Generate management controls from validated metadata and link the application's own WebUI separately; an out-of-process backend does not isolate same-origin browser code.
4. Binding settings and backups to a distro package name, absolute host path, or current internal database layout. Preserve stable extension identity and typed exports.
5. Promising compatibility without migration fixtures and contract checks, or discovering incompatible extensions only after switching the core deployment.

## Unresolved before specifying the contract

1. **Installation trust:** curated native packages with privileged scripts versus restricted artifacts. An admin installation prompt must describe any trust beyond the sandboxed runtime.
2. **Frontend details:** following the owner's clarification, Andaru generates the management page from declarative metadata; the application's own WebUI is linked separately, not injected as executable UI code into Andaru. Specify allowed field/visibility rules and safe WebUI URLs. Do not forward the Andaru session or credentials to that application.
3. **Privileged requests:** define the first concrete operations needed by a real extension, including pfBlockerNG. Centralized nft ownership means extensions request approved resources rather than installing their own global rulesets.
4. **Compatibility mechanisms:** capability/version declarations, migration guarantees, deprecation/support windows and extension contract tests implementing the approved update policy. Stay additive where possible; no invented promise of indefinite support.
5. **State/recovery:** distinguish settings, secrets, durable extension data and disposable caches; validate the backup/restore interface and package/dependency recovery with a representative extension.

## Where this leads Andaru

The owner's container-add-on direction narrows the design: an installable add-on needs Andaru integration metadata; importing arbitrary Compose files is not supported. An application may have its own WebUI, linked from its generated Andaru page. The following is a research-derived contract outline, not a finalized manifest schema.

| Part | Responsibility |
| --- | --- |
| Identity and compatibility | Stable add-on/instance identity, package version, supported Andaru API and documented capabilities |
| Settings and presentation | Ordered property objects declare types, defaults and constraints directly; derive standard JSON Schema internally. Each property owns its optional process/file binding. Enum selections are arrays without duplicates; min/max item counts govern selection cardinality. Grouping, help and conditional visibility guide presentation, never authorization; the API validates every save |
| Runtime bindings | Native or container variants share settings and permissions; Compose is a container provider. Typed bindings cover environment values, arguments, configuration/credential files, approved network exposure and storage |
| Management page | One root `menu` attaches to an Andaru top-level page; ordered children contain page definitions. Literal text or explicit i18n objects label generated settings, lifecycle and health controls; central logs/metrics and an optional separate application WebUI remain permission-protected |
| State and recovery | Identify settings, secrets, durable data and caches; migrations and backup ownership; retain compatible executable/image artifacts and dependencies for offline recovery |

For Compose add-ons, generated `.env` files and runtime configuration are disposable realizations, not another source of truth. Prefer mounted secret files when supported; environment-only applications still require secret-store inputs and redacted previews/logs/exports. Validate the fully resolved workload, not merely visible form values: hidden or conditional controls cannot authorize privileged containers, host mounts, runtime-socket access or additional networking.

As a design direction, native add-ons are an equal execution path, not a fallback for failed containers. They may integrate with supported OS packages or verified versioned binaries; interpreted applications additionally need a supported interpreter and dependency environment. Keep them as separate restricted services, not code loaded into Andaru's root engine. The same schema-driven UI, API authorization, secret/certificate references, logs, metrics and state ownership should apply to native and container variants. The spike below qualifies only versioned Traefik binaries, not OS-package or interpreter integration.

Compose is a workload format, not a promise of interchangeable engines. [Podman's Compose command](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) delegates to an external provider. [Docker's firewall documentation](https://docs.docker.com/engine/network/packet-filtering-firewalls/) describes host firewall/forwarding changes and warns that disabling its firewall integration can break networking. Keep Podman preferred and Docker a potential supported backend, but prove a supported networking model for each without bypassing Andaru policy. Container images must be retained by resolved identity/digest for recovery; an image tag alone is insufficient.

**Next design step:** define the package-binding contract behind the [agreed full Traefik manifest example](traefik-manifest-example.md): native descriptors, Compose consumption, generated files/resource paths and activation verification. Do not create a generic form language, arbitrary root script runner or replacement for Compose. Application validation/readiness, installation trust, supported networking and compatibility/migration guarantees remain consequential open decisions.

The [original Traefik probe](traefik-manifest-spike.md) exercises dynamic environment templates, verified TLS, file credentials, dashboard routing, structured logs, metrics and directory-based reload using a native binary. The separately recorded full example incorporates the owner's approved menu, type, localization and inline-binding decisions; it is not an implemented manifest schema. The original probe is runtime evidence for application bindings, not qualification of an Andaru runner or generated GUI.

The subsequent [Podman/Compose VM spike](podman-compose-spike.md) runs a rootless subset with Docker Compose against Podman, verifies application behaviour and offline image restoration, and retains a snapshot-protected Debian lab. It also demonstrates health-check argument translation failures, lost original client IPs and an archive naming pitfall. This is not proof of unrestricted Compose compatibility or the final appliance networking policy.

The [client-IP networking follow-up](container-client-ip-spike.md) proves an IPv4/TCP source-preserving path with direct rootless pasta or slirp, without new host firewall tables. Rootful bridge preserves addresses too but changes host forwarding/rules and needs FORWARD policy. The [dual-stack follow-up](pasta-dualstack-spike.md) adds bounded IPv6 HTTPS and IPv4/IPv6 UDP peer evidence. Prefer further qualification of a rootless ingress profile. Multi-service connectivity, application protocol semantics and the full policy boundary remain open.

The [native systemd spike](native-systemd-spike.md) then exercises dedicated-user Traefik, plain/encrypted systemd credentials, journal/access logs, metrics and real versioned offline replacement/fallback. It also finds that a rejected dynamic reload can leave a healthy old configuration active and that an unknown top-level static key may be accepted. Native and container packaging share integration requirements; this spike demonstrates that systemd activity alone is insufficient proof that an intended apply succeeded.

## Evidence limits

The comparative survey above is documentation/source research, not a sandbox escape test or performance benchmark. Separate runtime probes cover only the versions, topology and behaviours explicitly documented in their reports. No current or future extension format is certified safe by this report; Andaru's own runtime, UI and full rollback behaviour still require focused qualification when implemented.

Sources include moving upstream branches and live documentation; verify them against the supported release when implementing. OpenWrt release notes and the systemd 257 manual provide version-specific anchors. A representative package example is not an exhaustive audit of that ecosystem.
