# Extension contracts: reuse, avoid, unresolved

## Scope and conclusion

Research for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), not an approved extension specification. Compare pfSense/OPNsense, OpenWrt, and Cockpit; use nfsense/nfSensei as supplementary references. Distinguish documented design lessons from our recommendations: no maintainer is assumed to endorse Andaru's choices.

**Recommendation:** reuse declarative package/page metadata, named backend operations, configuration schemas and migrations, and explicit permissions. Keep extensions out of the privileged engine. Do not build a universal plugin framework or compatibility shim for another firewall.

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
3. Giving third-party JavaScript the main administrator session without an explicit frontend isolation decision. An out-of-process backend does not isolate same-origin browser code.
4. Binding settings and backups to a distro package name, absolute host path, or current internal database layout. Preserve stable extension identity and typed exports.
5. Promising compatibility without migration fixtures and contract checks, or discovering incompatible extensions only after switching the core deployment.

## Unresolved before specifying the contract

1. **Installation trust:** curated native packages with privileged scripts versus restricted artifacts. An admin installation prompt must describe any trust beyond the sandboxed runtime.
2. **Frontend boundary:** unrestricted pages need a considered origin/frame/broker policy; schema-rendered forms are another option. Curation alone does not enforce secret isolation.
3. **Privileged requests:** define the first concrete operations needed by a real extension, including pfBlockerNG. Centralized nft ownership means extensions request approved resources rather than installing their own global rulesets.
4. **Compatibility mechanisms:** capability/version declarations, migration guarantees, deprecation/support windows and extension contract tests implementing the approved update policy. Stay additive where possible; no invented promise of indefinite support.
5. **State/recovery:** distinguish settings, secrets, durable extension data and disposable caches; validate the backup/restore interface and package/dependency recovery with a representative extension.

## Evidence limits

This is documentation/source research, not an installation, sandbox escape test, or performance benchmark. No current or future extension format is certified safe by this report. Concrete runtime, UI and rollback behaviour still require focused VM/browser qualification when implemented.

Sources include moving upstream branches and live documentation; verify them against the supported release when implementing. OpenWrt release notes and the systemd 257 manual provide version-specific anchors. A representative package example is not an exhaustive audit of that ecosystem.
