# Debian Packages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `apt install andaru` from the signed repository at `https://apt.andaru.io/`, bootstrapped by a checksum-verified keyring package.

**Architecture:** One debhelper source package in `debian/` builds `andaru` (any) and `andaru-archive-keyring` (all). A release workflow in `andaru-project/andaru` attaches the `.deb`s and `SHA256SUMS` to a GitHub release; a publishing workflow in `andaru-project/apt`, which alone holds the signing subkey, pulls releases, indexes them with `apt-ftparchive`, signs and commits.

**Tech Stack:** debhelper (compat 13), dpkg triggers, AppArmor 4, systemd 257, apt-ftparchive, GnuPG, GitHub Actions, shellspec.

**Spec:** `docs/superpowers/specs/2026-11-14-debian-packages-design.md`

## Global Constraints

- **Paths:** as the spec's table. AppArmor rules at `/etc/apparmor.d/andaru/{kea-dhcp4,kea-dhcp6,unbound}`, included by the line `include if exists <andaru/<daemon>>` in `/etc/apparmor.d/local/usr.sbin.<daemon>`.
- **Keys:** primary `7CD6177785416B306999DEF2B5E7EDE2133D18DF`, signing subkey `67BF6A43DD97A19C2AEDE7294FA659D5E10049DD`; secret `APT_SIGNING_KEY` on `andaru-project/apt` only.
- **Repository:** suites `trixie` (releases) and `trixie-test` (prereleases), component `main`, architectures `amd64 arm64`, `Valid-Until` = `Date` + 14 days, newest five `andaru` versions kept.
- **Versions:** tag `v<x.y.z>` gives `<x.y.z>-1`; tag `v<x.y.z>-rc<N>` gives `<x.y.z>~rc<N>-1` (a prerelease); tag `keyring-<yyyy.mm.dd>` builds `andaru-archive-keyring` `<yyyy.mm.dd>` once.
- **Never** restart networkd, reload networkd or touch an interface in a maintainer script.

## Review Focus

1. **An administrator's own AppArmor local rules** survive install, upgrade and purge (Task 1).
2. **Installing Kea or Unbound after Andaru** still gets Andaru's rules loaded (Task 1, trigger).
3. **An upgrade** restarts the engine but no oneshot and no network unit (Task 2).
4. **A release that fails its checksum** is never published (Task 4).
5. **A prerelease** never reaches the `trixie` suite (Task 4).

---

### Task 1: The source package

**Files:** create `debian/{control,rules,changelog,copyright,source/format,andaru.install,andaru.postinst,andaru.postrm,andaru.triggers,andaru-archive-keyring.install,andaru.lintian-overrides}`, `packaging/apparmor/andaru-apparmor` (the include step), move `packaging/apparmor/usr.sbin.*` to `packaging/apparmor/andaru/{kea-dhcp4,kea-dhcp6,unbound}`, move `packaging/iproute2/andaru.conf` content unchanged; `debian/keyring/andaru.sources`; `tests/shell/apparmor_include_spec.sh`.

**Interfaces:** `andaru-apparmor ROOT add|remove` (ROOT empty in maintainer scripts; a scratch directory in tests): `add` appends the include line to each existing profile's local file once, creating a missing local file, then `apparmor_parser -r` the profile when ROOT is empty and the parser exists; `remove` deletes exactly the lines `add` wrote.

- [ ] **Step 1: failing shell spec** `apparmor_include_spec.sh`: with a scratch root holding `etc/apparmor.d/usr.sbin.kea-dhcp6` and a local file with the administrator's line `/srv/x r,`: `add` twice leaves one include line and the administrator's line; a missing local file is created with the line; a daemon without a profile gets nothing; `remove` leaves only the administrator's line.
- [ ] **Step 2:** run `shellspec tests/shell/apparmor_include_spec.sh` and watch it fail.
- [ ] **Step 3: implement** the script and the `debian/` files: `postinst configure` masks `nftables.service`, runs `andaru-apparmor '' add` and `systemd-tmpfiles --create andaru.conf`, then `#DEBHELPER#`; `postinst triggered` runs `andaru-apparmor '' add`; `andaru.triggers` has `interest-noawait` on the three profiles' paths; `postrm purge` runs `andaru-apparmor '' remove`; `rules` overrides `dh_installsystemd` to enable and start `andaru-engine.service andaru-leases.path andaru-prefixes.path andaru-prefixes.timer`, enable `andaru-network-boot.service` without starting it, and leave the oneshots unstarted (`--no-start` for those), and overrides `dh_auto_build`/`dh_auto_install` to `CGO_ENABLED=0 go build -trimpath -o debian/tmp/usr/bin/andaru ./cmd/andaru`; `andaru-archive-keyring` builds `andaru-archive-keyring.gpg` from `debian/keyring/andaru-archive-key.asc` with `gpg --dearmor`.
- [ ] **Step 4:** `shellspec` passes; `dpkg-buildpackage -us -uc -b` in a Debian 13 container builds both packages; `lintian` has no errors.
- [ ] **Step 5: commit** `feat: Debian packages for Andaru and its archive keyring`.

### Task 2: CI builds, checks and installs the packages

**Files:** `.github/workflows/ci.yml` (a `packages` job), `scripts/ci/install-check.sh`.

- [ ] **Step 1:** the job, per architecture: build in `debian:trixie` with Go from `go.mod`, `lintian --fail-on error`, then in a Debian 13 container booted with systemd as PID 1, `apt install ./andaru_*.deb` with recommends and run `install-check.sh`, which asserts: the files in the spec's table, `systemctl is-enabled nftables.service` = `masked`, the five units enabled, `andaru-engine.service` active and `andaru ctl links` answering, the include lines present, no interface changed (`ip -j addr` before and after), then an upgrade (`dpkg -i` of the same version rebuilt with a higher one) restarts the engine and starts no oneshot.
- [ ] **Step 2:** run the job's steps locally in the `v6probe`-style privileged container; it passes.
- [ ] **Step 3: commit** `ci: build, check and install the Debian packages`.

### Task 3: Releasing

**Files:** `.github/workflows/release.yml`.

- [ ] **Step 1:** on tags `v*` and `keyring-*`: compute the version (Global Constraints), write the `debian/changelog` entry, build (both architectures for `v*`; `all` for `keyring-*`), `lintian`, create the GitHub release (a prerelease for `-rc` tags) with the `.deb`s and `SHA256SUMS`.
- [ ] **Step 2:** `actionlint` passes; the version mapping is a function in `scripts/ci/version.sh` with a shellspec (`v0.4.0` gives `0.4.0-1`, `v0.4.0-rc1` gives `0.4.0~rc1-1` and prerelease, `keyring-2026.11.14` gives `2026.11.14`, anything else fails).
- [ ] **Step 3: commit** `ci: release workflow`.

### Task 4: Publishing (in `andaru-project/apt`)

**Files (that repository):** `.github/workflows/publish.yml`, `bin/publish`, `apt-ftparchive/{trixie,trixie-test}.conf`, `andaru-archive-key.asc`, `tests/`.

- [ ] **Step 1: failing tests** (`bin/publish` against a fake release directory and a throwaway key): a release whose `.deb` does not match its `SHA256SUMS` is refused and nothing changes; a prerelease lands only in `trixie-test`; a sixth `andaru` version removes the oldest from the pool and indexes; `InRelease` verifies with the throwaway key and carries `Valid-Until` 14 days after `Date`; a run with no new release still re-signs.
- [ ] **Step 2: implement** `bin/publish` (download with `gh release download`, check, place in `pool/`, `apt-ftparchive packages`/`release` per suite, sign `InRelease` and `Release.gpg` with the subkey imported from `APT_SIGNING_KEY`, prune) and the workflow (`workflow_dispatch` with an optional tag, and `schedule` daily), committing with the Actions bot.
- [ ] **Step 3:** tests pass; push.

### Task 5: Guide, release, lab, review, PR

- [ ] **Step 1: guide:** "Installing" with the bootstrap and the primary fingerprint; the hand-installed cleanup list; "From source" with the package's paths; the release procedure (tags, `gh workflow run publish`).
- [ ] **Step 2: full gate** plus the packages job locally.
- [ ] **Step 3: review, PR, CI, Codex and CodeRabbit, merge.**
- [ ] **Step 4: first releases:** tag `keyring-2026.11.14`, then `v0.4.0-rc1`; publish; record the keyring's checksum in the guide (a docs commit).
- [ ] **Step 5: lab:** the spec's two VMs from `apt.andaru.io` (`trixie-test` for the rc), takeover, apply, reboot, upgrade to `v0.4.0-rc2`; record "Debian packages lab qualification" in the guide.
