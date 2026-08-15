# Research: Cockpit branding, plugin, and privileged-API integration mechanisms on Debian 13

Resolves issue #11. Scope: what Cockpit on Debian 13 "trixie" supports **without a fork**.
Facts and trade-offs only; no decision is made here. All claims cite primary sources
(cockpit-project.org guide/docs, cockpit GitHub source, Debian tracker). Retrieved 2026-08-15.

Note: `https://cockpit-project.org/guide/latest/...` now HTTP-redirects to
`https://docs.cockpit-project.org/cockpit-guide/latest/...`; the latter is cited below.

## 1. Version shipped in Debian 13 and release cadence

| Suite | Version | Source |
| --- | --- | --- |
| Debian 12 bookworm (oldstable) | 287.1-0+deb12u3 | https://tracker.debian.org/pkg/cockpit |
| Debian 13 trixie (stable) | **337-1+deb13u1** | https://tracker.debian.org/pkg/cockpit |
| trixie-backports (stable-bpo) | **365-1~bpo13+1** (accepted 2026-08-03) | https://tracker.debian.org/pkg/cockpit |
| testing/unstable | 365-1 | https://tracker.debian.org/pkg/cockpit |
| upstream latest | 366 (2026-08-13) | https://github.com/cockpit-project/cockpit/releases |

- Upstream 337 was released 2025-04-23; Debian 337-1 changelog: "Upgraded to Patternfly 6"
  (https://metadata.ftp-master.debian.org/changelogs//main/c/cockpit/cockpit_337-1+deb13u1_changelog,
  https://github.com/cockpit-project/cockpit/releases/tag/337).
- `+deb13u1` (2026-04-09) fixes CVE-2026-4631 only. Debian's tracker lists **1 open high
  security issue in trixie, CVE-2026-4802** (RCE via crafted links in the logs UI), fixed
  upstream in 362 and in unstable 362-1; trixie 337-1+deb13u1 is marked *vulnerable*
  (https://tracker.debian.org/pkg/cockpit, https://security-tracker.debian.org/tracker/CVE-2026-4802).
  Bookworm/bullseye notes: "difficult to backport, as code to re-minify patched Javascript
  not functional in LTS" (same tracker page). Trade-off: stable pin (337) vs. backports
  (365, current CVE fixes) — backports track upstream continuously (Martin Pitt uploads
  every upstream release to unstable and then `~bpo13+1`, per tracker news).
- Cadence: "Cockpit has a time-based release cadence, with new versions appearing every two
  weeks." (https://cockpit-project.org/). Point releases exist only on RHEL-tracking
  branches (e.g. 356.3, 310.9 in 2026; branches `rhel-8`, `rhel-9.8`, `rhel-10.2`),
  not for 337 (https://github.com/cockpit-project/cockpit/releases,
  https://github.com/cockpit-project/cockpit/branches).
- Upstream install advice for Debian: "To get the latest version, we recommend to enable the
  backports repository" and `apt install -t ${VERSION_CODENAME}-backports cockpit`
  (https://cockpit-project.org/running.html).
- Debian binary packages (from upstream `tools/debian/control`): `cockpit` (metapackage:
  Depends bridge/ws/system; **Recommends** cockpit-storaged, cockpit-networkmanager,
  cockpit-packagekit), `cockpit-bridge` (Python 3), `cockpit-ws`, `cockpit-system`
  (Provides cockpit-shell/systemd/tuned/users; Recommends `sudo | pkexec | policykit-1`),
  `cockpit-networkmanager` (Depends `network-manager (>= 1.6)`; description: "Incompatible
  with systemd-networkd/ifupdown defined networks"), `cockpit-packagekit`, `cockpit-storaged`,
  `cockpit-sosreport`, `cockpit-doc`
  (https://github.com/cockpit-project/cockpit/blob/main/tools/debian/control).

## 2. Branding / theming mechanisms and limits

Source: `doc/branding.md` and `src/ws/cockpitbranding.c`.

- Search order for branding files (first hit wins), per
  https://github.com/cockpit-project/cockpit/blob/main/doc/branding.md :
  ```
  $prefix/share/cockpit/branding/$ID-$VARIANT_ID
  $prefix/share/cockpit/branding/$ID
  $prefix/share/cockpit/branding/default
  $prefix/share/cockpit/static
  ```
  "`$ID` and `$VARIANT_ID` variables are those listed in `/etc/os-release`". Administrators
  can add `/etc/cockpit/branding/` (`$XDG_CONFIG_DIRS/cockpit/branding/`), "considered
  before the above directories". The C implementation additionally walks `ID_LIKE`
  entries after `$ID` and puts `CustomLoginPage` (cockpit.conf) just before `static`
  (https://github.com/cockpit-project/cockpit/blob/main/src/ws/cockpitbranding.c,
  `cockpit_branding_calculate_static_roots`).
- "All of the files served from these directories are available over HTTP without
  authentication. This is required since these resources will be used on the login screen."
  (doc/branding.md).
- Interesting files: `apple-touch-icon.png`, `favicon.ico`, `branding.css` plus images they
  reference. Cockpit does not ship trademarked logos; distros symlink them (doc/branding.md).
  Debian's `branding/debian/` = `branding.css` + symlinks `logo.png` and `favicon.ico` →
  `/usr/share/pixmaps/debian-logo.png`; `#brand::before { content: "${NAME}"; }`
  (https://github.com/cockpit-project/cockpit/blob/main/src/branding/debian/branding.css,
  .../src/branding/debian/Makefile.am). Debian's `rules` deletes arch/rhel/fedora/centos/
  opensuse (and ubuntu unless Ubuntu-derived) branding dirs; `cockpit-ws.install` ships
  `usr/share/cockpit/branding/`, `usr/share/cockpit/static/`
  (https://github.com/cockpit-project/cockpit/blob/main/tools/debian/rules,
  .../tools/debian/cockpit-ws.install).
- What `branding.css` can style: login background (`html body.login-pf`), `#badge` (logo),
  `#brand::before` (brand text; **`${NAME}`, `${VARIANT}` etc. os-release variables are
  templated** — "The value for these variables come from the machine that cockpit is logged
  into"), the shell accent `--ct-color-host-accent`, and PatternFly brand tokens such as
  `--pf-t--global--color--brand--default` (doc/branding.md). The shell loads
  `../../static/branding.css` and `../../static/favicon.ico`
  (https://github.com/cockpit-project/cockpit/blob/main/pkg/shell/index.html); the login
  page loads `cockpit/static/branding.css` and has `#badge`, `#brand`, `#banner`
  (https://github.com/cockpit-project/cockpit/blob/main/pkg/static/login.html).
- Titles: login-page browser title = `[WebService] LoginTitle`; if unset, the hostname
  (https://docs.cockpit-project.org/cockpit-guide/latest/man/cockpit.conf.5.html;
  https://github.com/cockpit-project/cockpit/blob/main/pkg/static/login.js `environment.page.title`
  → `environment.hostname`). Shell document title = `<page label> - <user>@<host label>`
  (https://github.com/cockpit-project/cockpit/blob/main/pkg/shell/shell.tsx). No config knob
  for the shell title.
- Login page replacement: `[WebService] CustomLoginPage` = "Load the login page from an
  alternative directory. The directory must contain a login.html and optionally a po.js";
  `Banner` (`[Session]`) shows a file such as `/etc/issue` on the login page
  (cockpit.conf.5).
- Whole-shell replacement: `[WebService] Shell` = "The relative URL to top level component
  to display in Cockpit once logged in. Defaults to /shell/index.html" (cockpit.conf.5).
  A replacement shell would be a normal package (see §3), but the shell's internals
  (router, frames, superuser handling) are not documented API — "any APIs or behavior not
  explicitly documented here is an internal API and can be changed at any time"
  (https://docs.cockpit-project.org/cockpit-guide/latest/guide/packages.html).
- **Limits without a fork**: the About dialog hard-codes `productName={_("Web Console")}`,
  `trademark "Licensed under GNU LGPL version 2.1"` and a link to cockpit-project.org
  (https://github.com/cockpit-project/cockpit/blob/main/pkg/shell/shell-modals.tsx); shell
  layout/menu structure is CSS-overridable only through `branding.css`; page-level strings
  are per-package and untouched by branding.

## 3. Packaging custom pages/modules

Source: https://docs.cockpit-project.org/cockpit-guide/latest/guide/packages.html unless noted.

- A package = directory with `manifest.json`; "The name of a package is the name of the
  directory." Search order (XDG data dirs; "first one wins" on same name):
  `~/.local/share/cockpit/`, `/usr/local/share/cockpit/`, `/usr/share/cockpit/`.
  `$XDG_DATA_HOME` packages "are not cached by Cockpit or the web browser"; system packages
  are cached aggressively by checksum and "should not change while Cockpit is running".
  `cockpit-bridge --packages` lists what is installed.
- Manifest fields: `content-security-policy` (default is strict — no inline styles/scripts;
  overridable per package, with `default-src`/`connect-src`/etc. re-added), `name`
  (override directory name), `priority` (higher wins on name conflict), `conditions`
  (`path-exists`, `path-not-exists` since 286, `any` since 348 — "preferable to using
  priority"), `requires: {"cockpit": "NNN"}`, `version`, `preload`, `parent`, and the
  menu placement objects `dashboard` (Apps section), `menu` (System section; ordering
  buckets: System Information 10, Logs 20, major subsystems 30-40, VMs/Containers 50-60,
  Accounts/Services 70-100), `tools`. Each item: `label`, `order`, `path`, `docs`,
  `keywords` (`matches`, `goto`, `weight`, `translate`).
- Additional bridges: `bridges: [{match, spawn, environ, privileged, label, problem}]` —
  lets a package register a helper process for a channel payload; `${var}` substitution
  from the open command.
- Relative links only; base API via `<script src="../base1/cockpit.js">`; "Do not assume you
  can link to any file in any other package." Component URLs are
  `/cockpit/@localhost/<package>/<component>.html`
  (https://docs.cockpit-project.org/cockpit-guide/latest/guide/urls.html).
- Content negotiation: `file.js`, `.js.gz`, `.js.min`, `.js.min.gz` all served.
- Starter kit (https://github.com/cockpit-project/starter-kit): "Scaffolding for a Cockpit
  module"; Debian deps `gettext nodejs npm make`; `make install` → `/usr/local/share/cockpit/`,
  `make devel-install` symlinks `dist/` into `~/.local/share/cockpit/`; watch mode via
  `./build.js -w` (esbuild) with `RSYNC=` remote upload. Toolchain in `package.json`:
  esbuild 0.28.2 (+ `esbuild-wasm` fallback and "Fall back to distro package (e.g. Debian's
  /usr/lib/*/nodejs/esbuild)" in `build.js`), esbuild-sass-plugin, PatternFly 6.1.0
  (`@patternfly/react-core`, `patternfly`, `react-icons`), React 18.3.1, gettext-parser,
  TypeScript, eslint/stylelint, qunit; `"license": "LGPL-2.1"`. Starter manifest:
  `{"requires": {"cockpit": "137"}, "tools": {"index": {"label": "Starter Kit"}}}`
  (https://github.com/cockpit-project/starter-kit/blob/main/package.json,
  .../build.js, .../src/manifest.json). Trade-off: PatternFly 6 matches Debian 13's Cockpit
  337 ("Upgraded to Patternfly 6"), so a page built with the starter kit visually matches
  the stock shell; a page can also be plain HTML/JS using only `base1/cockpit.js`.

## 4. Calling privileged host APIs from a page

Source: https://docs.cockpit-project.org/cockpit-guide/latest/guide/cockpit-spawn.html,
.../cockpit-dbus.html, .../cockpit-http.html, .../cockpit-channels.html,
.../privileges.html.

- **Session model**: "When a user is logged into Cockpit, they are logged into a normal
  session that has exactly the same privileges as if they logged in via SSH". Cockpit
  "will usually try to escalate the privileges of the user using Policy Kit or sudo" —
  if the user can become root from the CLI (with or without a password), "Cockpit will
  be able to escalate the privileges of the session to 'root' immediately upon login".
  Users toggle this via the "Administrative access" indicator; on the next login Cockpit
  restores the same choice. "Usually a user needs to be in the wheel Unix user group"
  (Debian: sudo group / polkit admin rules) (privileges.html).
- **`cockpit.spawn(args, {superuser: "require" | "try", err, environ, pty, directory,
  binary, ...})`**: `superuser: "require"` runs "the process as root instead of the logged
  in user. If the currently logged in user is not permitted to become root (eg: via pkexec)
  then the client will immediately be closed with a 'access-denied' problem code"; `"try"`
  falls back to unprivileged. Returns a promise, streams output; `err: "message"|"out"|"ignore"`.
- **`cockpit.dbus(name, {bus: "system"|"session"|"user"|"none", address, superuser, track})`**:
  default bus is `system`; `superuser: "require"` makes "the DBus service … see the DBus
  method calls and accesses as coming from root, rather than the logged in user. This is
  useful for talking to services that do not correctly use polkit to authorize
  administrative users." Without superuser, the D-Bus daemon (e.g. NetworkManager,
  systemd, firewalld) does its own polkit checks against the *user's* session; admins can
  add `/etc/polkit-1/rules.d/*.rules` (JavaScript `polkit.addRule` /
  `polkit.addAdminRule`) to grant groups specific actions (privileges.html). Client API:
  `client.proxy(iface, path)`, `client.call()`, `client.subscribe()`, `client.watch()`.
  Example in tree: the firewall page uses
  `cockpit.dbus(owner, { superuser: "try", track: true })` on `org.fedoraproject.FirewallD1`
  (https://github.com/cockpit-project/cockpit/blob/main/pkg/networkmanager/firewall-client.js).
- **`cockpit.http(endpoint, {superuser, address, port, tls, headers})`**: "The endpoint can
  be a file path starting with / to connect to a unix socket, or it can be a port number";
  `superuser: "require"` opens the channel as root — i.e. a page can talk to a local daemon's
  REST API over a root-only Unix socket. `.get()/.post()/.request()` with streaming.
- **`cockpit.channel({payload, superuser, ...})`**: raw channel API; "It is rare to use raw
  channels directly." Other helpers on top: `cockpit.file()`, `cockpit.metrics`.
- **Superuser bridge mechanics**: the shell package's manifest declares privileged bridges:
  `sudo -k -A cockpit-bridge --privileged` (with `SUDO_ASKPASS=${libexecdir}/cockpit-askpass`),
  a polkit `StartTransientUnit` variant, and `pkexec --disable-internal-agent cockpit-bridge
  --privileged` (https://github.com/cockpit-project/cockpit/blob/main/pkg/shell/manifest.json).
  A package may register its own `"privileged": true` bridge with a `label`; "When no
  privileged bridge has a label, then Cockpit will start the bridge that runs sudo … if only
  one bridge has a label, then … that one bridge is always started" (packages.html
  "Bridges for specific tasks"). The bridge validates that `sudo` supports `-A`
  (https://github.com/cockpit-project/cockpit/blob/main/src/cockpit/superuser.py).
- No REST/HTTP API for outside callers: "There is no API available for external callers to
  invoke via HTTP, REST or otherwise." (packages.html). Embedding individual components in
  another same-origin web app is documented (`/cockpit/@localhost/<pkg>/<file>.html`; a
  reverse proxy is the usual way to get same origin; `/ping` health endpoint)
  (https://docs.cockpit-project.org/cockpit-guide/latest/guide/embedding.html).

## 5. Hiding or replacing the stock networking / firewall pages

- Both live in one package: `pkg/networkmanager` (Debian binary `cockpit-networkmanager`,
  installed at `usr/share/cockpit/networkmanager/`), manifest `"name": "network"`, menu
  item `index` labelled "Networking" order 40, `conditions: [{"path-exists":
  "/usr/share/dbus-1/system.d/org.freedesktop.NetworkManager.conf"}]`; the firewall UI is
  `firewall.html` inside it, reached as `/network/firewall`
  (https://github.com/cockpit-project/cockpit/blob/main/pkg/networkmanager/manifest.json,
  .../tools/debian/cockpit-networkmanager.install). The Firewall card/link renders only
  when the `firewalld.service` systemd unit exists (`firewall.installed = !!firewalld_service.exists`,
  https://github.com/cockpit-project/cockpit/blob/main/pkg/networkmanager/firewall-client.js).
- Options, all documented/no fork:
  1. **Don't install `cockpit-networkmanager`** (it is only a *Recommends* of the `cockpit`
     metapackage; depend on `cockpit-bridge`/`cockpit-ws`/`cockpit-system` directly)
     (tools/debian/control). Also, without NetworkManager's D-Bus conf file the package's
     `conditions` hide it automatically; without `firewalld.service` the firewall UI never
     appears.
  2. **Manifest override**: `/etc/cockpit/network.override.json` (or per user
     `~/.config/cockpit/`) in JSON Merge Patch form, e.g. `{"menu": {"index": null}}` to
     hide the menu item — "This can be used to hide or modify menu items of an existing
     package" (packages.html "Manifest overrides").
  3. **Replace by name+priority**: ship your own package with `"name": "network"` and
     `"priority": 10` (higher than stock, which sets none) — "it can replace that package by
     claiming the same name and a higher priority" (packages.html "Replacing an existing
     package"); or simply add a new package with its own `menu` entry at order 30-40.
  4. Shell menu manipulation beyond overrides is not a public API (§2 limits).
- Trade-off notes: option 1 keeps the shell fully stock but drops NM-based UI entirely;
  option 2 hides the entry but the component URL `/cockpit/@localhost/networkmanager/index.html`
  still loads for a logged-in user; option 3 also intercepts deep links to `/network/...`.

## 6. Authentication, session and transport policy

Source: https://docs.cockpit-project.org/cockpit-guide/latest/man/cockpit.conf.5.html,
.../guide/authentication.html, .../guide/https.html, .../guide/listen.html,
.../guide/cert-authentication.html, https://github.com/cockpit-project/cockpit/blob/main/doc/authentication.md.

- Config file `/etc/cockpit/cockpit.conf` (INI; `$XDG_CONFIG_DIRS` honoured). "The port
  that cockpit listens on cannot be changed in this file."
- **PAM**: local login "is controlled by a cockpit specific pam stack, generally located at
  /etc/pam.d/cockpit. By default this is configured to allow you to login with the username
  and password of any local account". Debian's stack: `common-auth`/`common-account`/
  `common-session` includes, `pam_ssh_add.so`, and
  `account required pam_listfile.so item=user sense=deny file=/etc/cockpit/disallowed-users
  onerr=succeed` — "List of users to deny access to Cockpit, by default root is included"
  (https://github.com/cockpit-project/cockpit/blob/main/tools/cockpit.debian.pam). Allow-listing
  is therefore done with standard PAM modules (e.g. `pam_listfile sense=allow`,
  `pam_succeed_if`) in that file, not in cockpit.conf.
- **Auth schemes**: per-scheme sections `[basic]`, `[negotiate]` (Kerberos), `[tls-cert]`,
  `[bearer]` with `command`/`UnixPath`, `timeout`, `response-timeout`; a scheme is disabled
  with `action = none` (e.g. `[basic] action = none` after enabling
  `ClientCertAuthentication = yes`) (doc/authentication.md; cert-authentication.html).
  OAuth implicit grant via `[OAuth] URL/ErrorParam/TokenParam` (cockpit.conf.5).
- **`[WebService]`**: `Origins`, `ProtocolHeader`, `ForwardedForHeader`, `UrlRoot` (reverse
  proxy), `LoginTitle`, `LoginTo` (recommend `false` if exposed publicly), `RequireHost`,
  `AllowMultiHost` ("default is OS specific"; host switcher enabled by default only on
  RHEL 9 / Debian 12 / Ubuntu LTS per authentication.html), `MaxStartups` (default 10, or
  `start:rate:full`), `AllowUnencrypted` ("If true, cockpit will accept unencrypted HTTP
  connections. Otherwise, it redirects all HTTP connections to HTTPS. Exceptions are
  connections from localhost and for certain URLs (like /ping). Defaults to false"),
  `ClientCertAuthentication`, `Shell`, `CustomLoginPage`.
- **`[Session]`**: `Banner` (file shown on login page), `IdleTimeout` (minutes; "only
  applies to interactive password logins"; "When not specified, there is no idle timeout by
  default"; 0 disables), `WarnBeforeConnecting`. `[Log] Fatal`.
- **TLS**: certs from `/etc/cockpit/ws-certs.d/` — "last file with a .cert or .crt extension
  in alphabetical order", key in same-named `.key` (unencrypted); if none, a self-signed
  `0-self-signed.cert` is generated. Debian check command:
  `sudo /usr/lib/cockpit/cockpit-certificate-ensure --check`. `cockpit-ws` normally runs
  behind `cockpit-tls` (cockpit-ws.8; https.html).
- **Port/address**: systemd socket drop-in `/etc/systemd/system/cockpit.socket.d/listen.conf`
  with `ListenStream=` (reset) then `ListenStream=443` / `ListenStream=192.168.1.1:443`,
  `FreeBind=yes`; then `daemon-reload` + `restart cockpit.socket` (listen.html).
  `cockpit-ws` exits after 90 s idle when nobody is logged in (cockpit-ws.8).
- **Alternate deployments**: `cockpit-ws --local-ssh` (auth via sshd on 127.0.0.1:22),
  `--local-session BRIDGE` (no auth, must isolate the port), `--for-tls-proxy` (cockpit-ws.8).
  Multi-host: "JavaScript runs without isolation. All systems will be vulnerable to potential
  attacks from other connected hosts" (cockpit.conf.5 `AllowMultiHost`).

## 7. License facts relevant to aggregation with an AGPL product

- Upstream README "Licenses": LGPL-2.1 or later, GPL-3.0 or later, BSD-3, CC-BY-SA-3.0, MIT
  (https://github.com/cockpit-project/cockpit/blob/main/README.md). Website: "Cockpit is free
  to use and available under the GNU LGPL." (https://cockpit-project.org/).
- Per-file SPDX headers in tree: `src/ws/*` (cockpit-ws) and `pkg/*` (web UI, shell,
  networkmanager, static/login) are `LGPL-2.1-or-later`; the **Python bridge**
  (`src/cockpit/bridge.py`, `router.py`, `peer.py`, `superuser.py`, `polkit.py`,
  `packages.py`, `channels/dbus.py`) is `GPL-3.0-or-later`
  (https://github.com/cockpit-project/cockpit/blob/main/src/cockpit/bridge.py and siblings).
  Debian's `copyright` for 337-1+deb13u1 states `Files: * License: LGPL-2.1-or-later` and
  `dist/<pkg>/*` "LGPL-2.1-or-later and MIT" and does not enumerate GPL-3 for `src/cockpit`
  (https://metadata.ftp-master.debian.org/changelogs//main/c/cockpit/cockpit_337-1+deb13u1_copyright)
  — a discrepancy worth noting; the source headers are authoritative for upstream intent.
- Starter kit `package.json` `"license": "LGPL-2.1"`; a page scaffolded from it can be
  relicensed by its author (LGPL permits GPL/AGPL-compatible combination) — the starter
  kit's own contents are only build/tooling scaffolding plus `pkg/lib` helpers pulled from
  cockpit (LGPL-2.1-or-later).
- Aggregation shape available without a fork: an Andaru Cockpit package is a *separate*
  directory under `/usr/share/cockpit/<name>` that loads `../base1/cockpit.js` at runtime
  and talks the cockpit1 protocol; nothing from Cockpit needs to be compiled into it.
  Whether that constitutes mere aggregation vs. a combined work, and how AGPLv3 §13
  interacts with an LGPL-2.1/GPL-3.0 host, is a legal judgment outside this note.

## 8. Open questions / not verified here

- Whether Debian 13's `cockpit-system` also ships `pkg/shell` overrides consumers may rely
  on (only upstream `main` manifests were read; 337 predates `any` conditions (348)).
- Behaviour of `[WebService] Shell` replacement in practice — documented knob, no upstream
  example of a third-party shell was found.
- Whether trixie will receive a CVE-2026-4802 fix (tracker shows no `+deb13u2` yet).
