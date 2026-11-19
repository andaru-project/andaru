# Offline install spike

## Conclusion

The offline install route in the [first boot, safe mode and resets design](../superpowers/specs/2026-11-19-first-boot-safe-mode-resets-design.md) works on x86_64, with two fixes the design did not have:

1. The installer must randomize the disk GUID before it grows the root partition.
2. The stock first boot must deploy with `/sysroot` writable.

With both fixes, a box with no network installed `0.5.0-a.1` from the rebuilt ISO. It booted Andaru under UEFI Secure Boot with the signed registry origin, and its booted digest is the published amd64 digest. Signature enforcement on that origin is proven, and cosign verifies the digest. aarch64 was not tested: the lab has no aarch64 hardware.

This is a bounded lab spike (2026-11-19), slice 1 of that design. It is not the installer.

## Inputs

- **Release:** `ghcr.io/andaru-project/andaru:0.5.0-a.1`, amd64 manifest `sha256:72eff027c5d62dc482c74b7a2b76af29bc870d13b1a54263b8e8ff1eb437adb9`.
- **Base:** Fedora CoreOS 44.20261023.3.1. The image records no base version; it was identified by finding all 66 layers of `quay.io/fedora/fedora-coreos:44.20261023.3.1` in Andaru's manifest.
- **Live ISO:** `fedora-coreos-44.20261023.3.1-live-iso.x86_64.iso`, its checksum matching the build's `meta.json`.

## The archive and the ISO

- **Archive:** `skopeo copy --preserve-digests docker://ghcr.io/andaru-project/andaru@sha256:72eff… oci-archive:andaru.oci` gives 1,045,776,384 B (0.97 GiB), and its `index.json` names exactly the published digest.
- **Rebuild:** `xorriso -indev fcos.iso -outdev out.iso -compliance iso_9660_level=1 -map andaru.oci /andaru.oci -boot_image any replay` with xorriso 1.5.8 keeps the volume ID. It warns that it finds no `/EFI/BOOT` directory, which is harmless.
- **Customize:** `coreos-installer iso customize --live-ignition live.ign --live-karg-append console=ttyS0,115200n8` (0.27.0) works on the rebuilt ISO, and so do `iso kargs show`, `iso ignition show` and `iso reset`.
- **Size:** 2,026,438,656 B (1.89 GiB), about 115 MiB under GitHub's 2 GiB release asset limit.
- **Boot:** the rebuilt ISO boots under UEFI with Secure Boot on (OVMF with Microsoft keys). Legacy BIOS boot was not tested.

## Installing

The VM had a 24 GB disk, 4 GiB of RAM, two NICs with their links down, and a serial console. In the live system the NICs were down with no routes, and the archive was at `/run/media/iso/andaru.oci`. The install took about a minute and a half:

```sh
coreos-installer install /dev/sda --console tty0 --console ttyS0,115200n8 --ignition-file dest.ign
sgdisk --disk-guid=R --move-second-header /dev/sda   # required: see below
growpart /dev/sda 4 && partx -u /dev/sda
mount /dev/disk/by-partlabel/root /mnt/root && xfs_growfs /mnt/root
# the stateroot's /var is /mnt/root/ostree/deploy/fedora-coreos/var
install -d -m 0700 "$VAR/andaru-install"   # then copy the archive to a temporary name, sync, rename; install the seed 0600
```

**Growing the root before first boot sends that boot to the emergency shell** unless the installer first does what Fedora CoreOS's first boot would:
- Fedora CoreOS's `coreos-gpt-setup` runs `sgdisk --disk-guid=R --move-second-header` on first boot, while the disk GUID is still the image's placeholder.
- Once partition 4 reaches the last usable sector, that call aborts: "Secondary partition table overlaps the last partition".
- Running the same command in the installer, before `growpart`, avoids it. First boot then finds a random GUID and leaves the disk alone, and `ignition-ostree-growfs` reports nothing to grow.

## The stock first boot

**`/sysroot` is read-only on Fedora CoreOS,** so the deploy as the design wrote it fails with "Cannot write to repository: Read-only file system". The unit that works:

```ini
ExecStart=/usr/bin/unshare -m -- /bin/sh -c 'mount -o remount,rw /sysroot && exec ostree container image deploy --sysroot / --imgref ostree-unverified-image:oci-archive:/var/andaru-install/andaru.oci --target-imgref ostree-image-signed:docker://ghcr.io/andaru-project/andaru:0.5.0-a.1'
ExecStart=/usr/bin/systemctl reboot
```

- The deploy took 25 s offline.
- It stages the deployment, which `ostree-finalize-staged` writes during the reboot.
- `/boot` did not need to be writable.
- The stock boot's journal shows no sshd, no NetworkManager and no DHCP.

## On Andaru

- **Origin:** `bootc status` shows `ghcr.io/andaru-project/andaru:0.5.0-a.1`, transport `registry`, signature `containerPolicy`. The booted digest is `sha256:72eff027…adb9`, and the rollback deployment is stock Fedora CoreOS.
- **Kernel arguments:** `/proc/cmdline` has `console=tty0 console=ttyS0,115200n8`.
- **Boot menu:** GRUB 2.12's menu appears on serial. The menu on the screen was not captured. Both entries read "Fedora CoreOS 44.20261023.3.1", because Andaru's image keeps Fedora CoreOS's os-release.
- **The rebase unit** was skipped on its unmet `ConditionPathExists=!/usr/bin/andaru`.
- **SELinux,** enforcing:
  - `/var/lib` and `/var/lib/andaru` are `var_lib_t`;
  - `/var/andaru-install` is `var_t`;
  - `restorecon -nvR` changes nothing.
- **Units:**
  - NetworkManager and zincati are masked;
  - sshd is disabled and inactive;
  - `andaru-engine` and `systemd-networkd` are active;
  - `andaru-network-boot` succeeded;
  - no unit failed.
- **Disk:** 3.5 GB used of 24 GB, with the archive still present.

## Online

- **Network:** with its links up, the box took a DHCP lease from the factory network.
- **Update check:** `bootc upgrade --check` reports no changes, with no policy error.
- **Enforcement:** pulling the origin with the real policy succeeds. The same pull with a policy that trusts the nightly key instead fails with "cryptographic signature verification failed".
- **cosign:** `cosign verify --key image/cosign.pub` passes for the booted digest.

## Not tested

- aarch64, including GRUB's serial menu on `ttyAMA0`.
- The boot menu on the screen.
- Legacy BIOS boot.
- The peak use of `/var/tmp` during the deploy.
- The fallback route (`rpm-ostree rebase`, then `bootc switch`), which was not needed.

## Lab notes

- The live and installed systems stop at a login prompt with no way in, so the lab added a serial autologin drop-in to both Ignition configs. No SSH was used.
- The throwaway VM, its disk and the uploaded ISO were deleted afterwards.
