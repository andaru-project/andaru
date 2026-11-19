# First boot, safe mode and resets

Status: owner decisions from 2026-11-19 below; the author calls await review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

Andaru ships as its own image ([appliance image design](2026-11-18-appliance-image-design.md)). This document designs how a box gets from an install medium to a working first configuration, and how an operator gets a misbehaving box back. It covers four things:

- an offline installer that asks for the first settings and carries them into the installed box;
- console and SSH access, with SSH keys owned by Andaru accounts;
- a safe-mode boot that runs no add-ons;
- a soft reset beside the existing full factory reset.

## Owner decisions

From the 2026-11-19 conversation:

- **One ISO per release.** The release workflow builds an x86_64 and an aarch64 install ISO for each release. Each installs exactly its own release.
- **Offline install media.** The ISO carries the release's Andaru image, so installing needs no network. This is what makes PPPoE possible at install.
- **The installer asks for the first settings, and they carry over.** The management NIC, which may be the LAN, its settings, and the WAN are asked once at install and become the installed box's first configuration.
- **PPPoE is a WAN choice** once Andaru can apply PPPoE links ([#77](https://github.com/andaru-project/andaru/issues/77)).
- **Disk choice:** the installer prompts for the disk, unless a kernel argument presets it.
- **First access is both:** the local console, and an SSH public key given at install time.
- **Console:** tty1 and the serial console log in automatically to Andaru's console. Once setup is complete, its root shell and both resets ask for an administrator's password.
- **Andaru owns SSH keys.** The keys an administrator logs in with belong to that Andaru account, not to the operating system.
- **Safe mode:** a boot that enables no add-ons at all, to recover from an add-on that broke the box.
- **Soft reset:**
  - It wipes everything except each interface's own network settings.
  - VLANs stay, with their own settings: on fibre the WAN is often a VLAN. (This revises the first ruling that VLANs go.)
  - Bridges and bonds go, except those a kept link or management sits on, which stay with everything under them.
  - It keeps only the administrator performing the reset, with their password and SSH keys.
  - It keeps DHCP and DNS on the management LAN, and the audit log.
- **Full factory reset:** the existing one ([2026-11-17 design](2026-11-17-factory-reset-data-design.md)). It wipes everything, the audit log included, and brings management back on a best-effort basis.
- **Release rebuilds on a newer Fedora CoreOS wait for slice 5** (updates).

## Author calls

1. **The ISO is Fedora CoreOS's live ISO, plus an OCI archive of the release's image.** The release workflow customises the live ISO of the same Fedora CoreOS build as the image's pinned base, so the NIC names the installer shows are the ones the installed box uses. It adds `andaru.oci` to the ISO by rebuilding it with `xorriso -indev <fcos.iso> -outdev <out.iso> -map andaru.oci /andaru.oci -boot_image any replay`, keeping the volume label, because `coreos-installer iso customize` cannot carry a file that size. It then embeds a small installer with `coreos-installer iso customize --live-ignition`, which still finds its embed areas in the rebuilt ISO. The live system sees the archive at `/run/media/iso/andaru.oci`. The archive is the architecture's image exactly as published, made with `skopeo copy --preserve-digests` from the per-architecture manifest digest, which the release records, so the booted digest is the signed one (call 3). A from-scratch build such as `bootc install to-disk` produced an unbootable disk for a Fedora CoreOS–based image in the [uCore spike](../../research/ucore-spike.md), so the install stays on Fedora CoreOS's own route.
2. **The installed box's first boot rebases from the local archive, offline.**
   - The installer writes Fedora CoreOS to the disk with `coreos-installer install`. It grows the root partition and filesystem to the disk (Fedora CoreOS would do so only on first boot), mounts the installed root, and writes into its stateroot's `/var`: the archive as `/var/lib/andaru-install/andaru.oci`, and the seed as `/var/lib/andaru/install-seed.json` (0600 root), inside the wipeable tree so that a full reset removes an unconfirmed seed with everything else. Neither goes through Ignition, so no answer is written to `/boot`.
   - The stock first boot deploys it with `ostree container image deploy --imgref ostree-unverified-image:oci-archive:/var/lib/andaru-install/andaru.oci --target-imgref ostree-image-signed:docker://ghcr.io/andaru-project/andaru:<version>`, which records the signed registry reference as the deployment's origin from the start, and reboots. Andaru's first successful boot deletes the archive, so a failed first boot can still retry offline. The disk must be at least 20 GB: the archive is about 1 GB and unpacking it needs as much again.
   - The deployment's origin is the registry, so later updates are signature-checked as usual, with no second step. If slice 1's spike finds `--target-imgref` unusable, the fallback is `rpm-ostree rebase` from the archive followed by `bootc switch --mutate-in-place --enforce-container-sigpolicy` to the registry reference. Later updates are signature-checked as usual. Slice 5 decides how the box moves on from that version.
3. **The installed image is verified once the box is online.** The archive carries no registry signature, and anything inside the ISO is only as trustworthy as the ISO: the operator verifies the ISO against the signed `SHA256SUMS` (call 5), as with any operating system. On the box, the first time Andaru can reach the registry, it checks that the release's signature covers the digest it booted, with the image's own policy. Until then the console and the web UI say the image is unverified; a mismatch is shown as a failure and audited.
4. **The stock first boot has no network and no SSH.** Its Ignition config masks NetworkManager and `zincati`, disables `sshd.service` and `sshd.socket`, and holds the rebase unit, nothing else. Disable, not mask, for sshd, so Andaru can enable it later. Fedora's presets would otherwise enable sshd and DHCP on every port during that boot, and the enable link would survive the rebase. The image already ships the signature policy and keys, so Ignition writes none: copies written there would count as local changes and stop later images from updating them.
5. **The ISO is signed.** The release workflow publishes `SHA256SUMS` for both ISOs and one `cosign sign-blob` bundle over it, made with the release key. That requires the release workflow to create the GitHub release, with `contents: write` for that job.
   - A GitHub release asset is limited to 2 GiB. If an ISO exceeds that, it is published as an OCI artifact under `ghcr.io/andaru-project/andaru-iso:<version>`, and the release links to it.
6. **The installer is a small shell program and writes `andaru setup`'s answer document.** It checks only the form of each answer. Andaru on the installed box turns the answers into a declaration with the same code `andaru setup` uses, so a refused answer reopens setup, prefilled. NICs are recorded by permanent MAC address, which the renderer already matches on.
7. **The administrator's password is asked on the installed box, not by the installer.** A password typed into the live environment would cross into the installed system as plain text. The seed instead creates the administrator account with the installer's SSH key and no password. The account cannot log in to the web until the console's setup step sets a password.
8. **The seed is applied automatically once, with the usual rollback timer and the usual confirmation.**
   - **When:** at the first Andaru boot, only when no generation, no kept copy and no reset journal exist. That is never after a reset, which keeps the 2026-11-17 order (`recovery/`, then `last-lan/`).
   - **Confirmation:** as for every apply, from a new session over the management path. The console's setup step shows the management address and the exact `andaru ctl applies confirm <id>` to run over SSH with the seed's key, or, once the web service exists, the URL to confirm from. The console does not confirm by itself, because that would prove nothing about the management path. One exception: a box with no SSH key and no web service yet has no remote way to confirm, so there the console offers to confirm after a typed acknowledgement that the management path is unproven, and the audit record says so.
   - **Timer:** the longest rollback timer Andaru allows, since the operator may be walking from the console to a laptop.
   - **Rollback:** if it is not confirmed, the box returns to the factory network and filter, as the appliance design's call 6 defines, and the seed stays. It is not applied again automatically. The console's setup step offers it again, prefilled, to correct and re-apply.
   - **After confirmation:** the confirm writes `first/` as today, and the seed file is deleted in the same step, with an audit record. A seed that fails Andaru's checks applies nothing, and setup on the console shows the refusal.
9. **DHCP management is allowed, with a warning.** Andaru treats DHCP management as an uplink (`UPLINK_MANAGEMENT`), so no `first/` or `last-lan/` is ever recorded, and a full reset then ends at the console. The installer's review screen says so, as setup does before it puts management on an uplink.
10. **SSH: Andaru renders the authorized keys, and sshd reads only Andaru's file.**
   - **The file:** the engine writes `/var/lib/andaru/ssh/authorized_keys`, root-only, from the keys of accounts holding the administrator role, whenever accounts change. The file lives in the wipeable tree.
   - **The image** removes Fedora CoreOS's `/etc/ssh/sshd_config.d/40-ssh-key-dir.conf` (its `AuthorizedKeysCommand`), and its own drop-in sorts first, so its values win. It sets:
     - `AuthorizedKeysFile /var/lib/andaru/ssh/authorized_keys`;
     - `PasswordAuthentication no`;
     - `AllowUsers root` and `PermitRootLogin prohibit-password`, so an administrator's key cannot log in as `core`.
   - **Who logged in:** the login is root. Andaru records which account's key was used from the fingerprint in sshd's "Accepted publickey" log line. A key is accepted for one account only, so the fingerprint names one account.
   - **No per-login program:** no program runs per login, and the engine's root-only socket is not involved.
   - **SELinux:** a file context labels `/var/lib/andaru/ssh(/.*)?` as `ssh_home_t`. The engine restores the label each time it creates the directory, including after a full reset's wipe, because a file context applies only on relabel. The files the installer writes from the live environment are relabelled the same way on the first Andaru boot.
   - **When sshd runs:** Andaru enables sshd when the first key exists and disables it when none is left.
11. **Console.** `andaru console` is a menu on tty1 and the serial console. It is open until an administrator account with a password exists, and opens on the setup step while setup is incomplete. Once an administrator has a password, the root shell and both resets ask for an administrator's password, while safe mode and status stay open. A full reset recreates `admin` with the password typed at reset, so the gate stays meaningful after it. A forgotten password is recovered with the kernel argument `andaru.recovery=1`, typed at the boot menu by someone with physical or hypervisor console access: that boot's console offers the full factory reset without the password, as the 2026-11-17 design's console reset did. Its entries are:
    - setup;
    - status;
    - reboot into safe mode;
    - soft reset;
    - factory reset;
    - a root shell.
12. **Safe mode is a one-boot flag.**
    - **Setting it:**
      - The console entry writes `/var/lib/andaru/safe-mode-next`, and `andaru-network-boot` moves it to `/run/andaru/safe-mode`, so only the next boot is affected.
      - The kernel argument `andaru.safe=1` sets the same flag for a box that never reaches the console.
    - **Effect:** every add-on unit Andaru renders carries `After=andaru-network-boot.service` and three conditions, so it fails closed: `ConditionPathExists=!/run/andaru/safe-mode`, `ConditionPathExists=!/var/lib/andaru/safe-mode-next` (a flag that boot failed to move still counts), and `ConditionKernelCommandLine=!andaru.safe=1`. The network and filter come up as usual. The console and the web UI say that the box is in safe mode.
13. **Soft reset is its own journaled operation.** It runs in the engine, which stays up, and it keeps its journal at `/var/lib/andaru-reset/soft.json`, which the full reset's code and older images do not read, so a rollback to an older image can never take it for a full wipe. It does not use the full reset's fence: the box keeps its network while it runs. Its steps:
    1. **Identity first:** every other account, every API token and every session are revoked. Identity is not connectivity, so this step has no rollback and closes a compromised credential before anything else runs.
    2. **Network:** the soft-reset declaration is applied with the usual rollback timer and confirmed from a new session. If it rolls back, the network stays as it was.
    3. **Cleanup,** only after step 2 is confirmed: add-ons' state and secrets no kept link references are removed. If step 2 rolls back, the soft reset ends there: identity stays revoked, and the network, its secrets and the add-ons stay as they were, so the live configuration still finds everything it names.
    4. **Done:** the journal records each step, so a power loss resumes where it stopped.
14. **The soft-reset declaration** is built from the saved one. It reuses the management closure's ancestry walk, not `managementLAN` itself, which refuses an uplink management segment that the installer allows:
    - **Links kept:** every physical link, VLAN and PPPoE link, with its own addressing, plus every bridge or bond that management or a kept link sits on, with everything under it.
    - **Segments and zones:** each kept link's segment, and those segments' zones, with no rules.
    - **Management:** management, with its allowed sources and the address objects they name, and its DHCP, DHCPv6 and DNS service settings. The DNS service keeps only the management segment and no virtual addresses, since the objects those name go.
    - **Secrets:** exactly the secrets kept links reference, such as a PPPoE password.
    - **Everything else goes:** other bridges and bonds, other zones, rules, NAT, and other objects.
15. **The full reset is unchanged.** It still creates the `admin` account with the password asked at reset. SSH keys now live in the wipeable tree, so it wipes them and disables sshd, and SSH comes back when an administrator adds a key from the console or the web UI. Until then, the console and the web UI on the management LAN are the way in.

## Installer

1. **Start:** the ISO boots into the installer on tty1 and the serial console.
2. **Disk:** the installer lists the disks with size, model and serial, and asks which to erase. A typed confirmation follows. `andaru.disk=/dev/…` on the kernel command line skips both. Not `coreos.inst.install_dev`, which would start Fedora CoreOS's own automatic install.
3. **Management:**
   - the operator picks the NIC from a list showing name, MAC address, driver and link state;
   - then static (default `192.168.1.1/24`) or DHCP, with the warning from call 9;
   - with a static address, the installer asks whether to serve DHCP and DNS on that LAN, and offers a default pool.
4. **WAN** (optional): which NIC, then one of:
   - DHCP;
   - static, with gateway and DNS servers;
   - PPPoE, once #77 lands;
   - or none yet.
5. **SSH key** (optional): one public key, pasted or read from the boot medium.
6. **Review:** the installer shows the answers and the warnings, and asks for confirmation. Then it:
   - writes Fedora CoreOS with the Ignition config of call 4;
   - grows the root and writes the archive and the seed into it (call 2).
7. **First boots:**
   - The stock boot rebases offline and reboots.
   - Andaru boots:
     - it repoints the deployment at the registry (call 2);
     - it applies the seed (call 8);
     - it opens the console on setup for the administrator's password and the confirmation;
     - once online, it verifies the booted image (call 3).

## Slices

1. **Lab spike, first:** rebuild the ISO with the archive and check that `iso customize` still works on it; write into the installed stateroot before first boot; deploy from the `oci-archive` with `--target-imgref` (or the fallback in call 2); check that the next `bootc upgrade` is signature-checked and that the booted digest can be verified against the release signature. Record the ISO size against the 2 GiB asset limit, and how `coreos-installer iso customize` or a rebuilt ISO can carry a file of that size.
2. **Installer and ISO:** the installer, the archive, the seed, the ISO build, `SHA256SUMS` and its signature, and the GitHub release in the release workflow.
3. **First boot:**
   - applying and confirming the seed;
   - `andaru console` with its setup step and its password gate;
   - autologin;
   - SSH keys owned by Andaru, rendered for sshd.
4. **PPPoE** ([#77](https://github.com/andaru-project/andaru/issues/77)): apply, lab qualification, and the installer's PPPoE choice.
5. **Safe mode:** the flag, the kernel argument, the console entry, and the add-on unit condition, which the image design's add-on slice then honours.
6. **Soft reset:** the declaration, the journaled operation, and the console entry, then the web UI.

## What this changes

- **[Appliance image design](2026-11-18-appliance-image-design.md):**
  - Its call 10 (first install route) is replaced by calls 1 to 4 here. Its slice 2 becomes slices 1 and 2 here, and its slice 4 (setup) loses the console setup step, the first administrator and SSH to slice 3 here.
  - The rebase target is the release's own version, not `:stable`.
  - The scheduled rebuild moves to slice 5.
- **[First setup design](2026-11-18-andaru-setup-design.md):** its workflow runs on the console and starts from the seed, not a takeover. Its management election and first administrator are the installer's and the console's.
- **Factory reset designs:** a soft reset now exists beside the full reset. The full reset also wipes SSH keys and add-ons. With SSH keys wiped, its "SSH from the management LAN" way back in becomes the console and the web UI until a key is added.
- **`docs/dev/core-api.md`:** installing is from the ISO, and the lab route stays for development.

## Non-goals

- A network installer: the ISO is the only install route.
- Disk encryption, RAID or partition layouts other than Fedora CoreOS's default.
- A graphical installer.
- Choosing an update channel at install.

## Risks

- **ISO size.** The live ISO plus the image archive may exceed GitHub's 2 GiB asset limit. Call 5's fallback to GHCR covers it, but downloading from GHCR needs an OCI client, not a browser.
- **ISO size, measured:** for 0.5.0-a.1, x86_64 is about 0.91 GiB of live ISO plus 0.97 GiB of image, about 1.89 GiB, under the 2 GiB asset limit by about 110 MiB; aarch64 is about 1.73 GiB.
- **The offline rebase is unproven.** Slice 1's spike decides whether an `oci-archive` rebase followed by `--mutate-in-place` gives a deployment that later updates verify like a registry one. If it does not, the design falls back to building install media with `bootc-image-builder`.
- **A wrong management NIC** is caught only by the rollback timer. The box then returns to the factory network and factory filter, which admits no new inbound connection, so only the console recovers it.
- **The console before setup is an open root shell,** as on most appliances. That is accepted: there is nothing to protect yet.
