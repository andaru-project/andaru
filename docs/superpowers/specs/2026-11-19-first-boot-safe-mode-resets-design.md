# First boot, safe mode and resets

Status: owner decisions from 2026-11-19 below. The author calls await review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

Andaru ships as its own image ([appliance image design](2026-11-18-appliance-image-design.md)). This document designs how a box gets from an install medium to a working first configuration, and how an operator gets a misbehaving box back. It covers four things:

- an installer that asks for the first network settings and carries them into the installed box;
- console access before and after setup;
- a safe-mode boot that runs no add-ons;
- two resets: a soft reset that keeps the interfaces' own settings and the administrators, and the full factory reset that already exists.

It replaces slice 2 (install media) of the appliance image design, and takes over parts of slice 4 (setup) that the installer needs.

## Owner decisions

From the 2026-11-19 conversation:

- **One ISO per release.** The release workflow builds an x86_64 and an aarch64 install ISO and attaches them to that release. Each installs exactly its own release.
- **First access is both.** The local console logs in automatically to Andaru's console. An SSH public key can be given at install time.
- **The installer asks for the first settings, and they carry over.** The management NIC, which may be the LAN, and its settings, and the WAN, are asked once at install and become the installed box's first configuration.
- **PPPoE is a WAN choice** once Andaru can apply PPPoE links. [#77](https://github.com/andaru-project/andaru/issues/77) tracks that.
- **Disk choice:** the installer prompts for the disk, unless a kernel argument presets it.
- **Release rebuilds on a newer Fedora CoreOS wait for slice 5** (updates).
- **Safe mode:** a boot that enables no add-ons at all, to recover from an add-on that broke the box.
- **Soft reset:** it wipes everything except each interface's own network settings, and VLANs go away. It keeps the administrator, with its password and its SSH keys. DHCP and DNS on the management LAN are kept, and so is the audit log.
- **Full factory reset:** the existing one ([2026-11-17 design](2026-11-17-factory-reset-data-design.md)). It wipes everything, the audit log included, brings the management NIC back on a best-effort basis, and asks what the settings should be afterwards.
- **Andaru owns SSH keys.** The keys an administrator logs in with belong to that Andaru account, not to the operating system.

## Author calls

1. **The installer is a small shell program in the live environment.** It asks the questions, checks only their form, and writes the answers as a seed document. Andaru on the installed box turns the seed into a declaration with the same code `andaru setup` uses, so there is one source of truth for what an answer means. The installer never builds a declaration itself.
2. **The administrator's password is asked on the installed box, not by the installer.** A password typed into the live environment would have to cross into the installed system in an Ignition config, as plain text on disk until first boot. Instead, the installed box's first console session asks for it, in the console's setup step. No account exists until the password is set. Until then, the installer's SSH key alone admits a root login, so a headless box is reachable. The setup step then attaches the key to the new administrator account.
3. **The installed box's first boot reaches the internet with the WAN answer.** Stock Fedora CoreOS boots first, rebases to Andaru's image, and reboots. It uses NetworkManager, so the installer also writes a NetworkManager keyfile for the WAN answer: DHCP, static or PPPoE. Every other port gets no connection. Andaru's image masks NetworkManager, so the keyfile is used only by that stock boot.
4. **The rebase target is the release's own version tag,** `ostree-image-signed:docker://ghcr.io/andaru-project/andaru:<version>`. Version tags are never rewritten. Slice 5 decides how a box moves on from there.
5. **The ISO is signed.** The release workflow publishes `SHA256SUMS` and a `cosign sign-blob` bundle for each ISO, made with the release key, beside the ISOs on the GitHub release.
6. **The seed is applied as the first confirmed generation, with no rollback timer.** There is no earlier network to roll back to. A seed that fails Andaru's checks applies nothing: the box stays on the factory network and the factory filter, and the console's setup step shows the refusal and asks again.
7. **Console:** tty1 and the serial console log in automatically to `andaru console`, a menu. Until setup is complete the menu opens on the setup step. Its entries are:
   - setup;
   - reboot into safe mode;
   - soft reset;
   - factory reset;
   - a root shell.
8. **SSH keys are attributes of Andaru accounts.** sshd's `AuthorizedKeysCommand` asks Andaru for the keys of accounts holding the administrator role, and logs in as root. Andaru records which account's key was used through the key's options. The image's sshd configuration sets this, and sshd stays disabled until a key exists.
9. **Safe mode is a one-boot flag.** The console entry writes `/var/lib/andaru/safe-mode-next`, which boot moves to `/run/andaru/safe-mode`, so only the next boot is affected. The kernel argument `andaru.safe=1` sets the same flag, for a box that never reaches the console. Every add-on unit Andaru renders carries `ConditionPathExists=!/run/andaru/safe-mode`. The network and filter come up as usual. The console and the web UI say that the box is in safe mode.
10. **Soft reset is an ordinary apply, then cleanup.** The engine computes the soft-reset declaration from the saved one and applies it with the usual rollback timer and confirmation, so a management path that breaks is restored. Only after confirmation does it remove the other accounts, the tokens and sessions, and the add-ons' state.
11. **What the soft-reset declaration keeps:**
    - every physical link and PPPoE link, with its own addressing;
    - the management segment and its allowed sources, with the address objects they name;
    - the management segment's DHCP and DNS service settings.

    VLANs, bridges and bonds go. So do zones other than management's, rules, NAT, and other objects. Management on a VLAN, bridge or bond moves to its parent NIC with the kept LAN's address, the rule the full reset already uses.
12. **Accounts kept by a soft reset:** every account holding the built-in administrator role, with its password and SSH keys. Other accounts, every API token and every session go. The audit log is kept, and records the reset.
13. **The full factory reset also wipes SSH keys and add-ons,** because both now live in the wipeable tree. After it, the console opens on the setup step again, and the box is on `recovery/` or `last-lan/` as before.

## Installer

1. Boot the ISO. The live environment opens the installer on tty1 and the serial console.
2. **Disk:** the installer lists the disks with size, model and serial, and asks which to erase. A typed confirmation follows. `andaru.disk=/dev/…` on the kernel command line skips both.
3. **Interfaces:** it lists the NICs with name, MAC address, driver and link state, so the operator can tell which port is which.
4. **Management:** which NIC, then static (default `192.168.1.1/24`) or DHCP. With a static address, it asks whether to serve DHCP and DNS on that LAN, and offers a default pool.
5. **WAN** (optional): which NIC, then DHCP, static, or PPPoE (the PPPoE username and password) once #77 lands. Or none yet.
6. **SSH key** (optional): one public key, pasted or read from the boot medium.
7. **Review:** the installer shows the answers and asks for confirmation. Then it writes Fedora CoreOS to the disk with an Ignition config holding:
   - the seed, as `/var/lib/andaru-install/seed.json`, mode 0600, owned by root;
   - the WAN's NetworkManager keyfile;
   - the signature policy and keys, byte-identical to the image's;
   - the rebase unit;
   - `zincati` masked.
8. **Reboot:** the box rebases and reboots into Andaru. Andaru applies the seed, and the console opens on setup for the administrator's password.

A PPPoE password in the seed is the one secret that crosses into the installed box. It travels the same way, read once at first boot and removed from the seed file, and is held as a named secret from then on. The PPPoE choice waits for #77.

## Slices

1. **Installer and ISO:** the installer, the seed, the ISO build and its signing in the release workflow, the NetworkManager keyfile, and the rebase.
2. **First boot:** applying the seed, `andaru console` with its setup step for the password, autologin, and SSH keys owned by Andaru with `AuthorizedKeysCommand`.
3. **PPPoE** ([#77](https://github.com/andaru-project/andaru/issues/77)): apply, lab qualification, and the installer's PPPoE choice.
4. **Safe mode:** the flag, the kernel argument, the console entry, and the add-on unit condition, which slice 6 of the image design (add-ons) then honours.
5. **Soft reset:** the declaration function, the apply and confirmation, and the identity and add-on cleanup, from the console and later from the web UI.

## What this changes

- **Appliance image design:** slice 2 becomes slices 1 and 2 here. Slice 4 (setup) loses the console setup step, the first administrator and SSH, which move to slice 2 here; it keeps the web service and certificates.
- **[First setup design](2026-11-18-andaru-setup-design.md):** its workflow runs on the console and starts from the seed, not a takeover. Its management election and first administrator are the installer's and the console's.
- **Factory reset designs:** a soft reset now exists beside the full reset. The full reset's behaviour is unchanged, except that it also wipes SSH keys and add-ons.

## Non-goals

- Offline installation: the installed box's first boot still needs the internet to rebase.
- Disk encryption, RAID or partition layouts other than Fedora CoreOS's default.
- A graphical installer.
- Choosing an update channel at install.

## Risks

- **First boot needs the internet,** now through the WAN answer rather than DHCP on every port. A wrong WAN answer means no rebase. The stock first boot retries, and the console shows the failure.
- **A PPPoE password crosses an Ignition config** until #77's design confirms that the config is removed from `/boot` after first boot, or moves the password elsewhere.
- **`AuthorizedKeysCommand` runs Andaru on every SSH login.** If the engine is down, key logins fail, so the console stays the way in.
- **Soft reset of a box whose management is on a VLAN** moves management to the parent NIC. A switch port configured only for that VLAN then cuts the operator off until the rollback timer restores the previous generation.
