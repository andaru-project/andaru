# First boot, slice 2: installer and ISO

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** each release publishes an x86_64 and an aarch64 install ISO that installs exactly that release with no network. The ISO asks for the first settings and writes them to the installed box as a seed. The installed box rebases offline onto the release, keeps the seed for slice 3 to apply, and cleans up the install files once its origin is the registry. The release publishes `SHA256SUMS` for both ISOs, signed with the release key, in a GitHub release.

**Spec:** slice 2 of [first boot, safe mode and resets](../specs/2026-11-19-first-boot-safe-mode-resets-design.md): spec calls 1 to 6 and 9, the Installer section, and the os-release risk. The route is the one the [offline install spike](../../research/offline-install-spike.md) proved on x86_64. "Call N" below is this plan's; the spec's are written "spec call N".

## Author calls

The owner approved these on 2026-11-20. Calls 1, 4, 7, 8, 9, 10 and 11 amend the spec.

1. **The ISO finds the image's Fedora CoreOS build and stream in its os-release, not in a label.** The image keeps the base's `OSTREE_VERSION` (for example `45.20261030.3.1`) and `RELEASE_TYPE` (`stable`) in `/usr/lib/os-release`. The ISO build reads them with `podman create` and `podman cp`, which runs nothing, so it works for aarch64 on an amd64 runner. Andaru's own `org.opencontainers.image.version` label overwrites the base's, and a new label would need every workflow, the nightly repository's included, to pass the build as a build argument. This amends spec call 1. `fedora-release*` joins the image's version lock, so no package upgrade rewrites os-release without those fields.
2. **The live ISO is Fedora CoreOS's own, verified with Fedora's key.** For build `B`, stream `S` and architecture `A`, the URL is `https://builds.coreos.fedoraproject.org/prod/streams/S/builds/B/A/fedora-coreos-B-live-iso.A.iso`, built from the build ID rather than read from the unsigned `meta.json`, so the file name binds the build. `coreos-installer download --image-url` fetches `<url>.sig` and checks it against the Fedora keys it ships, and fails without a signature. It runs from the release's amd64 image, which carries the base's `coreos-installer`, so its keys are as new as the release it installs; so do `iso customize` and `iso … show`, for both ISOs. A build no longer in the archive fails the job.
3. **Tools.** `butane` runs from its `quay.io/coreos/butane` container pinned by digest. `xorriso` is installed with apt on the Ubuntu runner; the qualification proves `-boot_image any replay` with that version. Nothing executes code of the target architecture, so both ISOs build on an amd64 runner.
4. **The installer is POSIX `sh` with `whiptail` menus.** `whiptail` ships in the live system and draws its menus on the screen and on any serial terminal. Each screen is a function that returns the answer, with the `whiptail` call behind one wrapper that shellspec replaces, so the specs drive the logic without a terminal. It offers IPv4 and IPv6 on both ports (call 7), where the spec's screens ask for IPv4 only, and at most one SSH key. It checks only the form of each answer (spec call 6): an IPv4 address with no leading zeros in any octet (Go's parser refuses them), an IPv6 address, a prefix length, a MAC address, a key (call 8). A browser-based assistant is #89, not this slice.
5. **It runs on tty1 and the serial console, one session at a time.**
   - Fedora CoreOS 45 runs `kmsconvt@tty1` on the screen, and its live autologin generator does nothing once a user Ignition config is embedded. The live config masks `kmsconvt@tty1`, `autovt@tty1` (kmscon's alias, which logind starts on a switch back to VT1), `getty@tty1` and the serial getty, and its installer units conflict with them.
   - The serial console is `ttyS0` on x86_64 and `ttyAMA0` on aarch64: `live.bu`'s `@CONSOLE@`, also set with `--live-karg-append console=@CONSOLE@,115200n8`.
   - Each console shows "Press Enter to install". The shared `flock` is taken only when someone presses Enter, so an unattended serial port never locks out the screen. The other console then shows which console is installing.
6. **The live system has no network and no SSH.** The live config masks NetworkManager, `sshd.service`, `sshd.socket` and zincati; the live system is discarded, so masking is safe. NetworkManager would otherwise take DHCP leases on every port, the WAN included. It also sets `net.ipv6.conf.all.disable_ipv6=1` and `net.ipv6.conf.default.disable_ipv6=1`, so links the installer brings up to read their carrier get no address from a router advertisement. This is the live system only: the installed box gets the IPv6 settings the seed holds (call 7). The installer does not try the settings it collects: the live system has no network, its stack (NetworkManager's or none) is not the one the installed box runs, and a setting that fails on the installed box is first boot's to report (slice 3).
7. **The seed is a new format, version 1, documented in `docs/dev/install-seed.md` with fixtures in `image/iso/testdata/`.** `andaru setup` does not exist yet, so there is no answer document to reuse; this amends spec call 6. Slice 3 adds its strict Go reader, tested against the same fixtures, the semantic checks (a pool inside the subnet, a gateway on the WAN subnet), and the record of the first apply (spec call 8). Shape:

   ```json
   {
     "version": 1,
     "management": {"mac": "52:54:00:aa:bb:01",
                    "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.1.1/24"]},
                                   "ipv6": {"mode": "delegated", "from": "@wan", "subnetId": "0",
                                            "ra": {"mode": "unmanaged"}}},
                    "serve": {"dhcp": {"start": "192.168.1.100", "end": "192.168.1.199"}, "dns": true}},
     "wan": {"mac": "52:54:00:aa:bb:02",
             "addressing": {"ipv4": {"mode": "dhcp"}, "ipv6": {"mode": "dhcp6", "requestPrefixLength": 56}}},
     "sshKeys": [{"type": "ssh-ed25519", "blob": "AAAA…", "comment": "admin@laptop"}]
   }
   ```

   Each port's `addressing` is Andaru's own configuration object (`$defs/addressing` in `internal/config/schema.json`), so slice 3 checks it with the schema Andaru already has and copies it into the first configuration, where `delegated`'s `from`, `"@wan"`, is the label of the WAN segment slice 3 creates in the same change:
   - **IPv4:** `none`, `dhcp` or `static` (addresses, and a gateway on the WAN). `ppp` waits for PPPoE (#77).
   - **IPv6:** `none`, `static` (addresses, and a gateway on the WAN), `slaac`, `dhcp6` (with `requestPrefixLength` for a delegated prefix, and the advanced `prefixOnly`, `withoutRA`, `sendRelease` and `duid` behind an "Advanced" entry), or, on management only, `delegated`: a subnet (`subnetId`) of the prefix the WAN's `dhcp6` requests, so the LAN follows that prefix when the ISP changes it. `delegated` needs a WAN with `dhcp6` and `requestPrefixLength`.
   - **Router advertisements** on management, with a `static` or `delegated` IPv6: Andaru's `ra.mode`, offered as a list, defaulting to `unmanaged` (SLAAC only), which needs no DHCPv6 server.
   - **DNS servers** (`addressing.dns`) with a static WAN.

   Management needs at least one family that is not `none`, so the box is reachable, and a `dhcp` IPv4 there keeps the spec's warning (spec call 9). `serve` exists only with a static IPv4. `wan` is absent for "none yet". A MAC is the permanent address, `permaddr` from `ip -j -d link` or else `address`, lowercased: the rule Andaru's renderer matches on (`internal/host/links_linux.go`). The WAN list leaves out the management NIC.
8. **SSH key.** The installer offers to paste a key, or to read `andaru-ssh.pub` from the root of another medium, because an ISO written to a USB stick is read-only: this amends the spec's "read from the boot medium".
   - **Finding the file:** every filesystem on a device other than the boot medium and the target disk, mounted `ro,nosuid,nodev,noexec`. The file must be a regular file, not a link, read with `head -c 16384`. More than one match is refused, naming them.
   - **The key:** exactly one line matching `^(ssh-ed25519|sk-ssh-ed25519@openssh.com|ecdsa-sha2-nistp(256|384|521)|sk-ecdsa-sha2-nistp256@openssh.com|ssh-rsa) [A-Za-z0-9+/]+=*( [[:print:]]{0,256})?$`, so no options, no private key and no control bytes. It must then pass `ssh-keygen -l`, and an RSA key must have at least 2048 bits. The seed holds it as type, blob and comment.
9. **Disks.**
   - **The list:** `TYPE=disk`, writable, at least 8 GB (Fedora CoreOS's minimum), never the boot medium; a disk under 20 GB is listed with a warning that add-ons will need more. Both numbers are revisited once the qualification measures the install's peak (Task 9). This amends spec call 2's 20 GB floor. The boot medium is the parent disk (`lsblk -no PKNAME`) of `findmnt -no SOURCE /run/media/iso`.
   - **`andaru.disk=`:** resolved with `readlink -f`, so `/dev/disk/by-id/…` works, and held to the same rules; a value that fails them stops the installer with the reason.
   - **Refusal:** if a disk other than the target has a filesystem labelled `boot`, the installer refuses: Fedora CoreOS's `coreos-ignition-unique-boot.service` would send first boot to the emergency shell.
   - **The root partition:** found by its `root` label on the target disk only (`lsblk`), after `udevadm settle`, never through `/dev/disk/by-partlabel`.
10. **The installed box's side is in this slice, so the seed survives the first boot.**
    - **The move:** `andaru realize boot` moves the seed to `/var/lib/andaru/install-seed.json` as spec call 2 says, after its privilege check and on the reset fence path too. On a fresh install `/var/lib/andaru` does not exist yet, so the move creates it at 0700, the engine unit's `StateDirectoryMode`, and writes the seed at 0600; with no `pointers.json`, boot still takes the disaster path. The temporary file is created in `/var/lib/andaru`, so it takes that directory's label and needs no `restorecon`. The data-location condition of spec call 2 is dropped: only an ISO install stages a seed, and its config declares no data mount. A failed move is logged and never fails boot.
    - **The cleanup:** `andaru-install-cleanup.service`, wanted by `multi-user.target` and ordered after `andaru-engine.service` (ordering it after the target that wants it would be a cycle), with `ConditionPathExists=/var/andaru-install`. Its `ExecCondition` checks with `bootc status --json` and `jq` that the booted origin's transport is `registry`. It removes the archive, any partial file an interrupted installer left, the rebase unit, its enable link and the failure unit, then `rmdir`s the directory, which fails, keeping it, while an unmoved seed remains.
    - **No reset change:** the full reset does not remove `/var/andaru-install` (spec call 15): a reset reboots, and the cleanup runs on that boot.
11. **os-release.** The image rewrites `NAME="Andaru"`, `PRETTY_NAME="Andaru <version> (Fedora CoreOS <build>)"` and `VARIANT="Andaru"` in `/usr/lib/os-release`. It keeps `ID`, `VERSION_ID`, `VARIANT_ID=coreos`, `RELEASE_TYPE` and `OSTREE_VERSION`. No Fedora CoreOS unit or initramfs script matches on `NAME` or `VARIANT`, and ostree titles boot entries from `PRETTY_NAME`, so the Andaru deployment and the stock rollback read differently.
12. **An ISO over 2 GiB fails the release.** The GHCR fallback of spec call 5 waits until an ISO nears the limit: Fedora CoreOS 45's live ISO plus a nightly's layers is about 1.86 GiB.
13. **No nightly ISO, and no VM in CI.** The lab builds an ISO from a nightly with the same script.

## Global constraints

- Every commit builds and passes `go vet`, `go test ./...`, shellcheck and shellspec. The root suites (`scripts/ci/root-tests.sh`) pass.
- The image builds and passes `image-smoke.sh` on both architectures.
- No answer is written to `/boot` or passes through Ignition (spec call 2). The installer asks for no password (spec call 7).
- New shell scripts join `ci.yml`'s shellcheck list.

## Tasks

### Task 1: the image says Andaru
- [ ] Containerfile: the os-release edit (call 11), reading the build from `OSTREE_VERSION`; `fedora-release*` joins the version lock (call 1).
- [ ] `image-smoke.sh`: `NAME`, `PRETTY_NAME` and `VARIANT`; `ID=fedora` and `VARIANT_ID=coreos`; `OSTREE_VERSION`, read by sourcing the file in a subshell (the base quotes it, and its `VERSION` would replace the script's), matches `^[0-9]+\.[0-9]{8}\.[0-9]+\.[0-9]+$` and is the build `PRETTY_NAME` names; `RELEASE_TYPE` is set.

### Task 2: the seed format
- [ ] `docs/dev/install-seed.md`: version 1 (call 7) and each field's form check.
- [ ] Fixtures in `image/iso/testdata/`: static management with DHCP and DNS served, DHCP management, IPv6-only management (`static`), dual-stack management with a delegated prefix from a `dhcp6` WAN, a static dual-stack WAN, an IPv6-only `slaac` WAN, no WAN, no key. Each fixture's `addressing` objects pass `internal/config/schema.json`'s `$defs/addressing`.

### Task 3: the installer
- [ ] `image/iso/andaru-install`, POSIX `sh`, in functions the specs can call one by one:
  - the refusal to start without `/run/media/iso/andaru.oci`, and the Enter prompt and lock (call 5);
  - disks (call 9), with `andaru.disk=` read from `/proc/cmdline`, and the typed confirmation otherwise;
  - NICs: name, permanent MAC (call 7), driver (`device/driver`), and carrier after `ip link set up`;
  - management, WAN and key screens with their form checks (calls 4, 7 and 8), and the DHCP-management warning (spec call 9);
  - the review screen, then the write:
    - `coreos-installer install` with the consoles and the destination config;
    - `sgdisk --disk-guid=R --move-second-header`, `udevadm settle`, `growpart`, mount, `xfs_growfs`;
    - `andaru-install/` at 0700; the archive copied under a temporary name, synced and renamed; the seed at 0600;
    - unmount, `sync`, and a prompt to power off, then remove the medium and power on: the live system runs from the medium, so a shutdown after it is removed crashes (lab, 2026-11-21).
- [ ] The seed is written with `jq -n --arg`, never by string concatenation.
- [ ] `tests/shell/installer_spec.sh`:
  - each function against captures: `lsblk -J` (an isohybrid boot stick mounted as a partition, a small disk, a read-only disk, a stray `boot` label), `ip -j -d link` with and without `permaddr`, `/proc/cmdline`;
  - each form check, with the refusals: leading zeros, a private key, a key with options, two keys, control bytes, RSA-1024, a FIFO or link named `andaru-ssh.pub`, two media carrying one;
  - the seed output against Task 2's fixtures, byte for byte.

### Task 4: the Ignition configs
- [ ] `image/iso/dest.bu`, with `@IMAGE@` and `@VERSION@` filled in by the build:
  - spec call 4's masks and disables;
  - the rebase unit exactly as the spike ran it: `ConditionPathExists=!/usr/bin/andaru` and `ConditionPathExists=/var/andaru-install/andaru.oci`; `unshare -m`; `/sysroot` remounted writable; `ostree container image deploy --sysroot / --imgref ostree-unverified-image:oci-archive:/var/andaru-install/andaru.oci --target-imgref ostree-image-signed:docker://@IMAGE@:@VERSION@`; then reboot;
  - `OnFailure=` writing `/run/issue.d/andaru-install.issue`, "The Andaru install did not finish: reinstall from the ISO.", then starting `getty@tty1` (kmscon, on the screen, shows no issue file) and reloading agetty, so a failed deploy says so on both consoles. In `/run`, the message lasts only for a boot whose rebase failed.
  - It also masks `NetworkManager-wait-online`, as the image does.
- [ ] `image/iso/live.bu`, with `@CONSOLE@`: the installer, its units and the masks (calls 5 and 6), the IPv6 sysctls (call 6), and the rendered `dest.ign` as a file (`butane --files-dir`, so `dest.bu` is rendered first).
- [ ] CI renders both with `butane --strict` (`render.sh`), then checks the rendered units and files (`check-ign.sh`).

### Task 5: the ISO build
- [ ] `image/iso/build.sh REPO VERSION DIGEST ARCH AMD64_DIGEST OUT`, with `ARCH` `x86_64` or `aarch64`, and `coreos-installer` run from `REPO@AMD64_DIGEST` (call 2):
  - read `OSTREE_VERSION` and `RELEASE_TYPE` from `REPO@DIGEST` (call 1); fetch and verify the live ISO (call 2);
  - `skopeo copy --preserve-digests docker://REPO@DIGEST oci-archive:andaru.oci`, and check that its `index.json` names `DIGEST`;
  - `xorriso -indev <fcos.iso> -outdev <out.iso> -compliance iso_9660_level=1 -map andaru.oci /andaru.oci -boot_image any replay`;
  - render `dest.bu` with `REPO:VERSION`, then `live.bu`; `coreos-installer iso customize --live-ignition` and `--live-karg-append`, with `systemd.show_status=false` and `loglevel=3` beside the console, so status lines and kernel messages do not draw over the installer;
  - check `iso kargs show` and `iso ignition show`.

### Task 6: the installed box's side (call 10)
- [ ] The seed move in `realize boot`, on the reset fence path too, reusing `commitRendered` (`cmd/andaru/realize.go`) and syncing `/var/lib/andaru` before the staged copy is removed. Tests: the move, also with no `/var/lib/andaru` yet; a staged copy next to an existing target, which is discarded; a failed move, which leaves boot's exit status alone.
- [ ] `andaru-install-cleanup.service` in `packaging/systemd/`, enabled in the image and checked by `image-smoke.sh`.

### Task 7: the release
- [ ] An `iso` job per architecture after `sign`, from the `digest-amd64` and `digest-arm64` artifacts (amd64 is `x86_64`, arm64 is `aarch64`), uploading `andaru-<version>-<arch>.iso` as an artifact. It fails if the ISO is over 2 GiB (call 12).
- [ ] A `publish` job in the `image-release` environment with `contents: write`, installing cosign as the `sign` job does:
  - `SHA256SUMS` over both ISOs, `cosign sign-blob --yes --bundle SHA256SUMS.cosign.bundle` with the release key, then `cosign verify-blob` with `image/cosign.pub`;
  - `gh release create --draft`, prerelease from `needs.version.outputs.prerelease`, then `gh release upload --clobber` of the ISOs, `SHA256SUMS` and the bundle, then `gh release edit --draft=false`. A rerun of a failed job finishes the same draft. The image tags exist before the ISOs, so boxes can update even while a failed ISO job waits for its rerun.

### Task 8: docs
- [ ] core-api: installing from the ISO; verifying it (`cosign verify-blob --key cosign.pub --bundle SHA256SUMS.cosign.bundle SHA256SUMS`, then `sha256sum -c --ignore-missing SHA256SUMS`); the SSH key file; `andaru.disk=`. The lab route stays for development.
- [ ] README: the install route.
- [ ] The spec: amend spec calls 1, 2, 5, 6 and 15 and the key file, as calls 1, 7, 8, 10 and 12 say.

### Task 9: qualification
- [ ] CI green, including both image builds and smoke.
- [ ] An ISO built from a nightly (call 13), on Proxmox:
  - x86_64 under UEFI with Secure Boot, and under legacy BIOS;
  - the installer on the screen and on serial, with the serial port unattended, then the second console locked out, and a VT switch away from tty1 and back during the install;
  - no address in the live system on a segment with DHCP and router advertisements (`ip addr`, the journal);
  - the refusals: `andaru.disk=` naming the boot medium, and a second disk with a `boot` label;
  - the boot menu on both consoles reads Andaru for the new deployment and Fedora CoreOS for the rollback;
  - after the first Andaru boot: the seed at `/var/lib/andaru/install-seed.json` labelled `var_lib_t`, `/var/andaru-install` and the rebase unit gone, the origin the signed registry reference, and the `bootc status --json` field the cleanup reads;
  - a deploy made to fail shows the issue message;
  - the peak use of `/var/tmp` during the deploy, and the ISO sizes;
  - `bootc upgrade` refusing an image signed with another key.
- [ ] aarch64, if an aarch64 VM is available (for example QEMU with HVF on an Apple Silicon Mac): the install, and GRUB's menu on `ttyAMA0`.

## Out of scope

- Applying and confirming the seed, its semantic checks and Go reader, the console, autologin and SSH: slice 3.
- Verifying the booted image online (spec call 3): slice 3, with the console and the web UI that report it.
- PPPoE at install: slice 4.
- The GHCR fallback for ISOs over 2 GiB (call 12), and a nightly ISO (call 13).
