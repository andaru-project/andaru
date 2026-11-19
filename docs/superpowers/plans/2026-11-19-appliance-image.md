# Appliance image, slice 1: the image and its CI

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A signed, bootable Andaru image, `ghcr.io/andaru-project/andaru`, built from stock Fedora CoreOS for x86_64 and aarch64. Every pull request builds and lints it. A release tag pushes and signs it. Andaru's code uses Fedora's names and paths. A Proxmox VM rebased onto it applies and confirms a declaration, serves DHCP and DNS, survives a factory reset without DHCP on WAN, and refuses an unsigned image.

**Architecture:** A multi-stage `image/Containerfile` builds the Go binaries and the web assets, then installs them `FROM quay.io/fedora/fedora-coreos:stable` with Fedora's Kea, Unbound, ppp, OpenVPN, systemd-networkd and qemu-guest-agent. The image carries the signature policy and public key, the factory network file, an SELinux file-context module and the unit enablement. `andaru-network-boot` keeps its place before networkd. On every path where Andaru would otherwise leave no filter, it now loads either the factory filter or deny-all. Every generation and every reset state carries the unclassified-link catch-all. CI builds the image natively on both architectures. The root integration suites move into a privileged Fedora container, so they test against Fedora's Kea and Unbound.

**Tech Stack:** Go, Podman/Buildah on GitHub's Ubuntu runners, `bootc container lint`, cosign with a key pair, containers-policy.json, an SELinux CIL module. No new Go dependency.

**Spec:** `docs/superpowers/specs/2026-11-18-appliance-image-design.md`, merged 2026-11-19 (#71). This plan is its slice 1.

## Facts checked for this plan

On `quay.io/fedora/fedora-coreos:stable` (Fedora 44, kernel 7.2.11), 2026-11-19, with `dnf install kea kea-hooks unbound ppp systemd-networkd setools-console`:

- **Kea 3.0.5.**
  - Units: `kea-dhcp4.service` and `kea-dhcp6.service`, with `User=kea`, `RuntimeDirectory=kea`, `RuntimeDirectoryPreserve=yes` and `StateDirectory=kea`.
  - `ExecStart=/usr/sbin/kea-dhcp4 -c /etc/kea/kea-dhcp4.conf`.
  - Hooks are in `/usr/lib64/kea/hooks/`, including `libdhcp_run_script.so`. `/usr/share/kea/scripts` exists.
  - `/usr/sbin/kea-dhcp4` is `bin_t`, so Kea runs unconfined.
- **Unbound 1.26.2.**
  - `unbound.conf` includes `/etc/unbound/conf.d/*.conf`. `fedora-defaults.conf` sets `auto-trust-anchor-file: "/var/lib/unbound/root.key"`.
  - `unbound.service` has `Wants=` and `After=` on `unbound-anchor.service` and `unbound-keygen.service`. There is no `/usr/libexec/unbound-helper`.
  - Without `root.key`, `unbound-checkconf` fails: `auto-trust-anchor-file: "/var/lib/unbound/root.key" does not exist`.
- **SELinux.**
  - Unbound runs as `named_t`, labelled from `named_exec_t`.
  - `/run/andaru-unbound/*`, `/run/andaru-leases/*` and `/run/andaru-kea/*` default to `var_run_t`.
  - No rule lets `named_t` read `var_run_t` files, so Unbound cannot read Andaru's includes without a file-context rule.
- **ppp 2.5.1.** The plugin is `/usr/lib64/pppd/2.5.1/pppoe.so`. The kernel has `ppp_generic.ko.xz` and `pppoe.ko.xz`.
- **Users.** `kea` and `unbound`, not `_kea`.
- **Stock image.** `moby-engine` with `docker.socket` enabled, `wireguard-tools`, and `zincati` enabled. No firewalld, no qemu-guest-agent.

Left to the lab:
- Whether `unbound-control status` answers with Fedora's remote-control defaults.
- Whether Kea 3 accepts Andaru's control socket and hook script paths under Fedora's unit.
- The guest agent's start condition.
- The exit status of `unbound-anchor.service` when it updates the key.

## Calls for the owner

1. **The Debian package stops building in this slice.** Fedora's unit names and paths break it, and the spec drops it. The `packages` CI job and the release workflow's Debian, APT and keyring jobs are removed here. Slice 3 deletes `debian/`, the APT scripts, the takeover code and the rest.
2. **Factory reset runs on the image already in this slice.** `upgrade.CheckIdle` and `upgrade.DpkgConsistent` treat a missing `/usr/bin/dpkg` as nothing to check. Reset's dpkg locks are skipped when `/var/lib/dpkg` does not exist. Slice 3 still moves these helpers out of `upgrade` and adds the bootc fence.
3. **The root integration suites run in a privileged `registry.fedoraproject.org/fedora:44` container** on the same runners, with Fedora's `nftables`, `kea`, `kea-hooks`, `unbound`, `iproute`, `iputils` and `python3`. The AppArmor step goes. `upgrade.test` stays on the Ubuntu runner, unchanged, until slice 3 deletes it. In CI, `ANDARU_TEST_REQUIRE_TOOLS=1` turns every missing-tool skip into a failure.
4. **No VM boots in CI.** CI builds, lints and smoke-tests the image as a container, on both architectures. Booting, applying, DHCP, DNS, PPPoE, factory reset and the signature refusals are tested on a Proxmox VM in the lab, as for factory reset.
5. **Unbound's trust anchor.** Before `unbound-checkconf`, the realizer runs `systemctl start unbound-anchor.service`. It then fails the apply when `/var/lib/unbound/root.key` is still missing, whatever the unit's exit status.
6. **Unbound under SELinux.** The image installs a CIL module that labels `/run/andaru-unbound(/.*)?` and `/run/andaru-leases(/.*)?` as `named_conf_t`. tmpfiles creates the directories with that label, and files written into them inherit it. Andaru and Kea stay unconfined, as the spec says.
7. **Filters on every boot and rollback path.** DHCPv4 uses packet sockets that bypass the nft input hook, so a filter alone cannot keep WAN from taking a lease. Two mechanisms work together:
   - **The catch-all** `51-andaru-unclassified.network`:
     - it matches `Name=en*`, with `ActivationPolicy=down`, `LinkLocalAddressing=no`, `RequiredForOnline=no` and no DHCP;
     - it is written into every generation and is present in every set-up state;
     - it keeps unclassified links down.
   - **The filter**, which Andaru now always loads into its `inet andaru` table, so the first apply replaces it atomically:
     - **never set up** (`/var/lib/andaru-kept` has none of `recovery/`, `last-lan/` or `first/`): the factory filter, which is deny-all plus DHCPv4 and DHCPv6 client replies, ICMPv6 neighbour discovery and router advertisements, and established and related traffic. There is no catch-all, so the factory file gives DHCP;
     - **set up but nothing comes up**: deny-all and the catch-all;
     - **a generation comes up**: its own filter and catch-all.
   - Reading the kept copies fails closed: any error other than "absent" counts as set up.
   - This covers every path that today leaves no filter or removes the table: `disasterBoot`, `Boot` with `Confirmed==nil` and with every candidate failing (on reset trees and normal trees), `Rollback` through `fallback` with no previous generation, `toward(nil)`, and `Teardown`.
8. **Ownership of the catch-all is exact.** The realizer's foreign-file check and the store's `allowed()` accept the exact path `/run/systemd/network/51-andaru-unclassified.network`, not a `51-andaru-` prefix. Verification (`unmet`) skips it.
9. **The first declaration cannot silently down a factory link.** The `WOULD_UNCONFIGURE` gate treats a link currently configured by `99-andaru-factory.network` like one configured by Andaru, so a declaration that omits it is refused unless the gate is acknowledged.
10. **Signing key.**
    - The owner creates the cosign key pair. The private key and its password become secrets of a GitHub environment, `image-release`, restricted to `v*` tags. The plan never handles the private key.
    - The public key is committed as `image/cosign.pub`.
    - `policy.json` uses `"signedIdentity": {"type": "matchRepository"}`, so a pull by tag accepts a signature made by digest.
11. **The lab image matches the release image except for the key and the namespace.**
    - Build arguments set the public key and the signed namespace. The lab image's own `policy.json` and `registries.d` therefore name the lab registry.
    - The lab Ignition config writes byte-identical copies of them, so the lab exercises the files the image ships.
    - The lab signs with the same pinned cosign version and flags as `release.yml`, but signs a single-architecture image. The first release candidate tag qualifies signing the two-architecture index.
12. **Versions are OCI-safe.** `scripts/ci/version.sh` maps `v1.2.3` to `1.2.3` and `v1.2.3-rcN` to `1.2.3-rcN`, and refuses everything else. The `keyring-*` trigger goes. The image's `org.opencontainers.image.version` label and the engine's `buildVersion` come from it. Pull-request builds use `0.0.0-pr<N>`.
13. **The scheduled rebuild waits for slice 2.** Nothing consumes `stable` until install media exist. The rebuild, its pre-publish lint and smoke gates, and a version scheme that sorts correctly against release candidates all move to slice 2, where the spec's rebuild risk is resolved. This slice already gives the base a pin: `ARG BASE_IMAGE=quay.io/fedora/fedora-coreos:stable`, which the release workflow can override with a digest from the repository variable `FCOS_BASE`.

## Global constraints

- The image installs only Fedora packages. No third-party repository, no COPR.
- `sshd` is disabled, `NetworkManager` is masked, `zincati` is disabled, and `moby-engine`, `containerd` and the `docker.socket` link are removed. `bootc container lint` passes with no errors, and the smoke script fails on its `/var` warnings for Kea and Unbound.
- `policy.json` keeps Fedora CoreOS's defaults except one `sigstoreSigned` `keyPath` requirement for the signed namespace. The `registries.d` entry sets `use-sigstore-attachments: true` for that namespace only.
- The factory network file lives in `/usr/lib/systemd/network`, which the realizer does not scan. Nothing Andaru writes lives in `/usr`.
- AppArmor files are not installed in the image. Slice 3 deletes them.
- Behaviour on a box with a confirmed generation does not change, except for the catch-all.

## Review focus

- Call 7: no path, whether boot, rollback, teardown or reset, may leave a set-up box with neither the catch-all nor a filter. A fresh box must get DHCP.
- The catch-all must never shadow a declared link, VLAN, bridge or PPPoE parent. Each of these has its own `50-andaru-` file (`internal/render/networkd.go:282-325`, `internal/render/pppoe.go:46-53`).
- `release.yml`: only `image-release` can sign. The signing job `needs:` the main-ancestry job, signs the digest, and only then moves tags.

## Structure

- `image/Containerfile`, `image/cosign.pub`, `image/files/`: kargs, factory network, policy, registries.d, the SELinux module.
- `packaging/`: Kea drop-in directories renamed, the Unbound file moved to `conf.d`, and `sysusers.d/andaru.conf` moved from `debian/andaru.sysusers`.
- `internal/host/realizer_linux.go`, `internal/generation/{services,store,realize,filter,ownership,takeover,disaster_boot}.go`, `internal/render/{render,services,networkd}.go`, `internal/engine/apply_gates.go`, `internal/upgrade/quiesce_linux.go`, `internal/reset/`, `cmd/andaru/reset.go`.
- `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `scripts/ci/`.

## Task 1: Fedora names and paths

**Files:** `internal/generation/services.go`, `internal/host/realizer_linux.go`, `internal/render/render.go`, `internal/render/services.go`, `internal/render/services_integration_linux_test.go`, `internal/generation/pdroutes.go`, `packaging/systemd/`, `packaging/kea/andaru-lease-event`, their tests.

- [ ] Kea units become `kea-dhcp4.service` and `kea-dhcp6.service` in `services.go:89-90` and in the `After=` lines of `andaru-leases.service` and `andaru-prefixes.service`. Rename the drop-in directories to `kea-dhcp4.service.d/` and `kea-dhcp6.service.d/`.
- [ ] The hook path becomes `/usr/lib64/kea/hooks/libdhcp_run_script.so` in the realizer and in the integration tests' globs. The pppd plugin glob becomes `/usr/lib64/pppd/*/pppoe.so`. Fedora uses `lib64` on both architectures.
- [ ] Replace the `unbound-helper` call (call 5). A unit test covers an anchor unit that fails while `root.key` exists, which succeeds, and one where `root.key` is still missing, which fails the apply.
- [ ] `TOOL_MISSING` messages name `kea`, `kea-hooks`, `unbound`, `ppp` and `nftables`.
- [ ] Comments naming `_kea` say `kea`.
- [ ] The integration tests' tool skips fail when `ANDARU_TEST_REQUIRE_TOOLS=1`. The Unbound tool test checks the fragment through `/etc/unbound/unbound.conf` when that file exists.
- [ ] `go test ./...` passes.

## Task 2: Reset without dpkg

**Files:** `internal/upgrade/quiesce_linux.go`, `internal/reset/reset_linux.go`, `cmd/andaru/reset.go`, tests.

- [ ] `CheckIdle` and `DpkgConsistent` return nil when `/usr/bin/dpkg` does not exist. A test with an injected stat proves this, and another proves that an existing dpkg is still run.
- [ ] Reset takes the dpkg locks only when `/var/lib/dpkg` exists. Existing tests still pass.

## Task 3: Catch-all and filters

**Files:** `internal/render/networkd.go`, `internal/host/realizer_linux.go`, `internal/generation/{store,realize,filter,ownership,takeover,disaster_boot}.go`, `internal/engine/apply_gates.go`, `cmd/andaru/realize.go` (`resetBlocks`), tests.

- [ ] The renderer adds the catch-all to every generation (call 7). Golden tests show it once per generation.
- [ ] Exact ownership (call 8):
  - the realizer test proves that `51-andaru-other.network` is still foreign;
  - the store test proves that a set containing the catch-all is stored;
  - a realize test with no unclassified link reaches `awaiting_confirmation`.
- [ ] Add the factory filter beside `DenyAll`, with an nft validity test like the existing filter tests. A namespace test proves it passes a DHCP reply and an RA and drops a new inbound TCP connection.
- [ ] Add one helper, `setUp()`, implementing the kept-copy rule. Tests cover:
  - kept copies absent, which means never set up;
  - the kept directory present but empty, also never set up;
  - each of the three copies present, which means set up;
  - an unreadable directory, which counts as set up.
- [ ] Apply call 7 on every path, each with a test:
  - `disasterBoot` with no kept copy, and with a kept copy;
  - `Boot` with `Confirmed==nil`, on a reset tree and on a normal tree;
  - `Boot` with every candidate failing, on a reset tree and on a normal tree;
  - `Rollback` of the first apply;
  - `toward(nil)`;
  - `Teardown`, which keeps the catch-all and loads deny-all.
- [ ] Check that `resetBlocks` does not keep boot from loading a filter during a journaled reset.
- [ ] Call 9 in `apply_gates.go:222`, with a test where the declaration omits a link on the factory file.

## Task 4: Image

**Files:** `image/Containerfile`, `image/files/**`, `packaging/sysusers.d/andaru.conf`, `packaging/unbound/andaru.conf` (comment only), `scripts/ci/image-smoke.sh`.

- [ ] Builder stages:
  - Go from `go.mod`'s toolchain and Node from `web/package.json`'s engines, both images pinned by digest.
  - They build `andaru` (with `buildVersion` from `ARG VERSION`), `andaru-web` and `web/dist`, as `debian/rules` does.
- [ ] Final stage, `FROM ${BASE_IMAGE}`:
  - `dnf install systemd-networkd kea kea-hooks unbound ppp openvpn qemu-guest-agent`;
  - `dnf remove moby-engine containerd`;
  - `dnf clean all`.
- [ ] Copy the payload that `debian/andaru.install` lists, minus AppArmor, to the same paths, with two exceptions:
  - the Unbound file goes to `/etc/unbound/conf.d/andaru.conf`;
  - the sysusers file goes to `/usr/lib/sysusers.d/andaru.conf`.
- [ ] `image/files/`:
  - `usr/lib/bootc/kargs.d/10-andaru-console-x86_64.toml` (`console=tty0 console=ttyS0,115200n8`) and `10-andaru-console-aarch64.toml` (`console=tty0 console=ttyAMA0,115200n8`), each with `match-architectures`;
  - `usr/lib/systemd/network/99-andaru-factory.network`: `Name=en*`, `DHCP=yes`, `IPv6AcceptRA=yes`, `RequiredForOnline=no`;
  - `etc/containers/policy.json` and `etc/containers/registries.d/andaru.yaml`, rendered from `ARG SIGNED_NAMESPACE=ghcr.io/andaru-project`;
  - `etc/pki/containers/andaru.pub`, from `ARG COSIGN_PUB`, by default `image/cosign.pub`;
  - `usr/share/andaru/selinux/andaru.cil`, installed with `semodule -i` (call 6).
- [ ] Explicit unit states, without `preset-all`:
  - enable `systemd-networkd`, `andaru-engine.service`, `andaru-network-boot.service`, `andaru-factory-reset.service`, `andaru-leases.path`, `andaru-prefixes.path` and `andaru-prefixes.timer`;
  - disable `sshd.service`, `zincati.service` and `andaru-web.service`;
  - mask `NetworkManager.service` and `NetworkManager-wait-online.service`.
- [ ] Clean `/run`, `/tmp` and the dnf logs, then run `bootc container lint`.
- [ ] `scripts/ci/image-smoke.sh` runs inside the built image as a container and checks:
  - `andaru --version` prints `VERSION`;
  - the packages are present and `moby-engine` is absent;
  - every unit state above;
  - `matchpathcon /run/andaru-unbound/x.conf` reports `named_conf_t`;
  - the lint output has no `/var` warning for Kea or Unbound.

## Task 5: CI and release

**Files:** `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `scripts/ci/version.sh` and its shellspec.

- [ ] `ci.yml`:
  - drop `packages`;
  - add `image ${{ matrix.arch }}` on `ubuntu-24.04` and `ubuntu-24.04-arm`, which runs `podman build --build-arg VERSION=0.0.0-pr<N>` and then the smoke script. Nothing is pushed.
- [ ] `ci.yml` `native-linux`:
  - the unprivileged tests and `upgrade.test` stay on the runner;
  - the other root suites run in `podman run --privileged --network host -e ANDARU_TEST_BINARY -e ANDARU_TEST_REQUIRE_TOOLS=1 -v <art>:<art> registry.fedoraproject.org/fedora:44`, after the `dnf install` of call 3.
- [ ] `version.sh` per call 12, with its shellspec updated.
- [ ] `release.yml`:
  - remove the Debian, APT and keyring jobs, and keep the main-ancestry check;
  - an `image` job per architecture pushes by digest;
  - a job in `image-release` `needs:` the ancestry check and both image jobs. It creates the manifest list, runs `cosign sign --key env://COSIGN_PRIVATE_KEY` on the manifest digest with the pinned cosign version and flags, and only then tags the version, and `stable` for a non-rc tag.
- [ ] The signing job cannot run before a tag exists. The first release candidate tag after merge is its qualification, and the PR says so.

## Task 6: Docs

**Files:** `README.md`, and the pages in `docs/` that describe installing Andaru.

- [ ] Replace the `apt install` instructions with a short "Image" note: the image exists, and it will install through slice 2's ISO. Until then, the lab route is the install route.
- [ ] Record the signing key's fingerprint and where the public key lives.

## Task 7: Lab qualification

On a throwaway Proxmox VM (OVMF, q35, three virtio NICs, serial console), from a stopped snapshot, as in the spike:

- [ ] Build two lab images (calls 11 and 13) and sign them with the lab key:
  - an image `lab1`;
  - an image `lab2` with a marker file;
  - an unsigned `lab-unsigned`;
  - an image `lab-wrongkey` signed with another key.
- [ ] Install stock Fedora CoreOS with `coreos-installer` and a lab Ignition config. The config:
  - masks `zincati`;
  - enables `sshd` for the lab only;
  - writes byte-identical copies of the lab image's policy, registries.d entry and key;
  - runs the rebase unit to `ostree-image-signed:docker://<lab registry>/andaru:lab1`.
- [ ] The rebase to `lab-unsigned` and to `lab-wrongkey` fails, and the rebase to `lab1` succeeds. After it, `bootc switch` and `bootc upgrade` to `lab-unsigned` and `lab-wrongkey` fail, including when one of them is pushed under the `lab1` tag.
- [ ] Before setup:
  - all three NICs take DHCP from the factory file;
  - `nft list ruleset` shows only the factory filter;
  - a TCP connection to port 22 from another host fails while sshd runs;
  - the guest agent runs.
- [ ] Apply and confirm a declaration with `takeover: false`, after the `WOULD_UNCONFIGURE` refusal for the third NIC has been seen and acknowledged. The declaration has WAN by DHCP on NIC 1, a static LAN on NIC 2, Kea DHCPv4 and DHCPv6, and Unbound. Then check:
  - a LAN client gets a lease, and resolves a name and the lease name;
  - lease events reach Andaru;
  - `unbound-control status` answers;
  - `ausearch -m avc` shows no denial;
  - NIC 3 is down with no address.
- [ ] PPPoE against a lab PPPoE server comes up, or the result is recorded as not qualified.
- [ ] After a reboot, the confirmed generation comes back before networkd.
- [ ] Factory reset:
  - the box comes up on `recovery/` or `last-lan/`;
  - WAN takes no DHCP lease at any point, which `journalctl -u systemd-networkd` and the WAN's DHCP server log show.
- [ ] `bootc upgrade` to `lab2` keeps the state, and `bootc rollback` returns.
- [ ] Record the results, and what was not qualified, in the PR.

## Out of scope

- Install media, the scheduled rebuild, and the version scheme for rebuilds: slice 2.
- Deleting Debian, takeover and `upgrade`, and the bootc fence for reset: slice 3. The code stays, unused on the image.
- Setup: slice 4. The lab applies with `andaru ctl`.
- Engine-driven updates: slice 5.
- SELinux confinement of Andaru and Kea: slice 8.
