# First boot, slice 2: installer and ISO

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** each release publishes an x86_64 and an aarch64 install ISO that installs exactly that release with no network. The ISO asks for the first settings and writes them to the installed box as a seed. The installed box rebases offline onto the release, keeps the seed for slice 3 to apply, and cleans up the install files once its origin is the registry. The release publishes `SHA256SUMS` for both ISOs, signed with the release key, in a GitHub release.

**Spec:** slice 2 of [first boot, safe mode and resets](../specs/2026-11-19-first-boot-safe-mode-resets-design.md): calls 1 to 6 and 9, the Installer section, and the os-release risk. The route is the one the [offline install spike](../../research/offline-install-spike.md) proved on x86_64.

## Author calls

These await the owner's review.

1. **The ISO finds the image's Fedora CoreOS build in its os-release, not in a label.** The base's `/usr/lib/os-release` carries `OSTREE_VERSION` (for example `45.20261030.3.1`), and the image keeps it. The ISO job reads it with `podman create` and `podman cp` on the architecture's digest, which runs nothing, so it works for aarch64 on an amd64 runner. A label would need every workflow, the nightly repository's included, to pass the build as a build argument, since a Containerfile cannot compute a label. This amends the spec's call 1.
2. **The live ISO comes from Fedora CoreOS's build archive and is verified with Fedora's key.** For build `B` and architecture `A`, the ISO job reads `https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/B/A/meta.json`, takes `images["live-iso"].path`, and downloads it with `coreos-installer download --image-url`, which checks the detached signature against the Fedora keys built into coreos-installer. A build no longer in the archive fails the job.
3. **Pinned tools.** `coreos-installer` and `butane` run from their `quay.io/coreos/*` containers pinned by digest; `xorriso` and `skopeo` come from the Ubuntu runner. Nothing executes code of the target architecture, so both ISOs build on an amd64 runner.
4. **The installer is POSIX `sh` with plain prompts** (no dialog or whiptail: the live system has neither). It asks for IPv4 only, as the spec's screens do, and for at most one SSH key. It checks only the form of each answer (call 6).
5. **It runs on tty1 and the serial console, one session at a time.** An installer unit per console replaces Fedora CoreOS's live autologin there (`console=ttyS0,115200n8` on x86_64, `ttyAMA0` on aarch64, set with `--live-karg-append`). The sessions share a `flock`: the second console shows which console is installing.
6. **The live system has no network and no SSH.** The live Ignition config masks NetworkManager, `sshd.service`, `sshd.socket` and zincati, as call 4 does for the installed system's stock boot. The installer brings links up without addresses, only to read their carrier.
7. **The seed's format is version 1, defined in this slice; its Go reader comes in slice 3.** The format is documented in `docs/dev/install-seed.md`, with valid and invalid fixtures in `image/iso/testdata/`. The installer's tests compare its output with the fixtures byte for byte, and slice 3's strict Go reader is tested against the same files. Slice 3 adds the record of the first apply (call 8). Shape:

   ```json
   {
     "version": 1,
     "release": "0.6.0",
     "management": {"mac": "52:54:00:aa:bb:01", "ipv4": {"address": "192.168.1.1/24"},
                    "serve": {"dhcp": {"start": "192.168.1.100", "end": "192.168.1.199"}, "dns": true}},
     "wan": {"mac": "52:54:00:aa:bb:02", "ipv4": {"dhcp": true}},
     "sshKeys": ["ssh-ed25519 AAAA… admin@laptop"]
   }
   ```

   Management's `ipv4` is `{"address"}` or `{"dhcp": true}`, and `serve` exists only with an address. `wan` is absent for "none yet"; its `ipv4` is `{"dhcp": true}` or `{"address", "gateway", "dns": [...]}`. MACs are permanent MAC addresses, lowercase.
8. **The SSH key file comes from a second removable medium, not the boot medium.** An ISO written to a USB stick is read-only, so a key file cannot be added to it. The installer offers to paste a key, or to read `andaru-ssh.pub` from the root of any other removable medium, mounted read-only. A key must pass `ssh-keygen -l`. This amends the spec's "read from the boot medium".
9. **Disks.** The list leaves out the boot medium, read-only devices and disks under 20 GB (call 2's minimum). After `coreos-installer install`, the installer finds the root partition by its `root` label on the target disk only, never through `/dev/disk/by-partlabel`, which another disk with Fedora CoreOS could also claim.
10. **The Andaru side of the handover is in this slice, so the seed survives the first boot:**
    - `andaru realize boot` moves the seed to `/var/lib/andaru/install-seed.json` exactly as call 2 says. "Its real data location" means: no mount is declared for `/var/lib/andaru` (`findmnt --fstab`), or it is mounted.
    - `andaru-install-cleanup.service` runs `andaru install cleanup` after `multi-user.target` when `/var/andaru-install` exists. If `bootc status --json` shows the booted origin's transport is `registry`, it removes the archive, the staging directory and the rebase unit with its enable link. It never removes the seed: the move above does, or it was never staged.
    - A full reset removes `/var/andaru-install` under the same condition (call 15).
11. **os-release.** The image rewrites `NAME="Andaru"`, `PRETTY_NAME="Andaru <version> (Fedora CoreOS <build>)"` and `VARIANT="Andaru"` in `/usr/lib/os-release`. It keeps `ID`, `VERSION_ID` and `VARIANT_ID=coreos`, which Fedora CoreOS's own units and scripts match on. The boot menu's titles come from `PRETTY_NAME`, so the Andaru deployment and the stock rollback read differently.
12. **ISOs over 2 GiB fail the release.** The x86_64 ISO measured 1.89 GiB on Fedora CoreOS 44. The GHCR fallback in call 5 waits until an ISO comes close to the limit; the release workflow fails before publishing if one exceeds it.
13. **No nightly ISO, and no VM in CI.** `image/iso/build.sh` builds an ISO from any image reference, so the lab builds one from a nightly. The rebase target then names `andaru-nightly`, which the image's policy checks with the nightly key.
14. **Release order.** The ISO jobs run after the image is signed and tagged, from the per-architecture digests. If an ISO job fails, the image tags already exist and boxes can update; rerunning the failed jobs completes the release. The GitHub release is created only when both ISOs and the signed `SHA256SUMS` exist.
15. **Verifying the booted image online (call 3) moves to slice 3,** with the console and web UI that report it.

## Global constraints

- Every commit builds and passes `go vet`, `go test ./...`, shellcheck and shellspec. The root suites (`scripts/ci/root-tests.sh`) pass.
- The image builds and passes `image-smoke.sh` on both architectures.
- No answer is written to `/boot` or passes through Ignition (call 2). No password is asked by the installer (call 7).
- New shell scripts join `ci.yml`'s shellcheck list.

## Tasks

### Task 1: the image says Andaru
- [ ] Containerfile: the os-release edit (call 11), reading the build from `OSTREE_VERSION`.
- [ ] `image-smoke.sh` checks `NAME`, `PRETTY_NAME` and `VARIANT`, and that `ID`, `VERSION_ID`, `VARIANT_ID` and `OSTREE_VERSION` are unchanged.

### Task 2: the seed format
- [ ] `docs/dev/install-seed.md`: version 1 (call 7), each field's form check, and that slice 3 applies it.
- [ ] Fixtures in `image/iso/testdata/`: static management with DHCP and DNS served, DHCP management, static WAN, no WAN, no key; and invalid ones (pool outside the subnet, the address inside the pool, gateway outside the WAN subnet, the same MAC twice, a malformed key).

### Task 3: the installer
- [ ] `image/iso/andaru-install`, POSIX `sh`, in functions the specs can call one by one:
  - disks: list (call 9), `andaru.disk=` from `/proc/cmdline`, the typed confirmation;
  - NICs: name, permanent MAC, driver, carrier;
  - management, WAN and key prompts with their form checks, and the DHCP-management warning (call 9);
  - review screen, then the write: `coreos-installer install` with the consoles, `sgdisk --disk-guid=R --move-second-header`, `growpart`, mount, `xfs_growfs`, the archive copied under a temporary name, synced and renamed, the seed at 0600 in `andaru-install/` at 0700;
  - the refusal to start without `/run/media/iso/andaru.oci`.
- [ ] The seed is written with `jq -n --arg`, never by string concatenation.
- [ ] `tests/shell/installer_spec.sh`: each function against fixtures (an `lsblk -J` capture, a fake `/sys/class/net` tree, `/proc/cmdline`), the seed output against Task 2's fixtures, and each invalid fixture's answers refused.

### Task 4: the Ignition configs
- [ ] `image/iso/live.bu`: the installer, its units on tty1 and the serial console with the shared lock (call 5), the masks (call 6), and the destination config as a file.
- [ ] `image/iso/dest.bu`, with `@IMAGE@` and `@VERSION@` filled in by the build: call 4's masks and disables, and the rebase unit from the spike (`unshare -m`, remount `/sysroot` writable, `ostree container image deploy --imgref ostree-unverified-image:oci-archive:/var/andaru-install/andaru.oci --target-imgref ostree-image-signed:docker://@IMAGE@:@VERSION@`, then reboot), with `ConditionPathExists=!/usr/bin/andaru`.
- [ ] CI runs `butane --strict` on both.

### Task 5: the ISO build
- [ ] `image/iso/build.sh IMAGE@DIGEST VERSION ARCH OUT`:
  - read the build from the image's os-release (call 1), fetch and verify the live ISO (call 2);
  - `skopeo copy --preserve-digests` the digest to `andaru.oci`, and check that its `index.json` names that digest;
  - `xorriso … -map andaru.oci /andaru.oci -boot_image any replay`, as in call 1;
  - render both Butane configs, then `coreos-installer iso customize --live-ignition` with the console karg;
  - check `iso kargs show` and `iso ignition show`, and fail above 2 GiB (call 12).

### Task 6: the installed box's side (call 10)
- [ ] `realize boot` moves the seed before `generation.Boot`: temporary file in the target directory, sync, rename, `restorecon`, then remove the staged copy; a staged copy next to an existing target is discarded. Tests: the move, a stale staged copy, a declared but unmounted `/var/lib/andaru`, and a power cut between rename and removal.
- [ ] `andaru install cleanup` and `andaru-install-cleanup.service`, with tests for each origin transport and a missing directory.
- [ ] The full reset removes `/var/andaru-install` when the origin is the registry, with a test.
- [ ] `image-smoke.sh` checks the cleanup unit is enabled.

### Task 7: the release
- [ ] `release.yml`: an `iso` job per architecture after `sign`, from the recorded per-architecture digests, uploading the ISO as an artifact.
- [ ] A `publish` job in the `image-release` environment with `contents: write`: `SHA256SUMS` over both ISOs, `cosign sign-blob --bundle SHA256SUMS.cosign.bundle` with the release key, `cosign verify-blob` with `image/cosign.pub`, then `gh release create` with the ISOs, `SHA256SUMS` and the bundle, marked prerelease for `a`, `b` and `rc` tags. ISO names: `andaru-<version>-x86_64.iso` and `andaru-<version>-aarch64.iso`.

### Task 8: docs
- [ ] core-api: installing from the ISO, verifying it (`cosign verify-blob --key cosign.pub --bundle SHA256SUMS.cosign.bundle SHA256SUMS`, then `sha256sum -c --ignore-missing SHA256SUMS`), the SSH key file, `andaru.disk=`; the lab route stays for development.
- [ ] README: the install route.
- [ ] The spec: amend call 1 (call 1 here), the boot medium key file (call 8), call 5's fallback (call 12), and call 3's slice (call 15).

### Task 9: qualification
- [ ] CI green, including both image builds and smoke.
- [ ] A release candidate tag in a fork, or the ISO built locally from a nightly (call 13), on Proxmox:
  - x86_64 under UEFI with Secure Boot, and under legacy BIOS;
  - the installer on the screen and on serial, the second console locked out;
  - no network in the live system (`ip addr`, the journal);
  - the boot menu on both consoles reads Andaru for the new deployment and Fedora CoreOS for the rollback;
  - after the first Andaru boot: the seed at `/var/lib/andaru/install-seed.json` with its label, `/var/andaru-install` and the rebase unit gone, the origin the signed registry reference;
  - the peak use of `/var/tmp` during the deploy, and the ISO sizes;
  - `bootc upgrade` refusing an image signed with another key.
- [ ] aarch64, if an aarch64 VM is available (for example QEMU with HVF on an Apple Silicon Mac): the install, and GRUB's menu on `ttyAMA0`. Otherwise it stays open in the spec's risks.

## Out of scope

- Applying and confirming the seed, the console, autologin and SSH: slice 3.
- PPPoE at install: slice 4.
- The GHCR fallback for ISOs over 2 GiB (call 12).
- A nightly ISO (call 13).
