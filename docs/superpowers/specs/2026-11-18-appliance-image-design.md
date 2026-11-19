# Andaru as an appliance image

Status: direction approved by the owner on 2026-11-18. The author calls below await review. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

Andaru stops installing onto any Debian host and ships as its own bootable, image-based operating system built on Fedora CoreOS. The [uCore spike](../../research/ucore-spike.md) ran Andaru's engine, applies with confirmation, the nft filter, the boot service and Kea on a ucore-minimal image, with image updates and rollbacks that kept Andaru's state. It did not test Kea's hooks, Unbound, PPPoE, signing or factory reset, and it ran on ucore-minimal, not stock Fedora CoreOS. On an image, Andaru owns the network from the first boot, so the takeover subsystem and all Debian packaging work go away. This document sets the direction, decides what the image contains, and orders the work into slices. Each slice gets its own plan.

## Owner decisions

From the 2026-11-18 conversation:

- **One product: the image.** The Debian package is dropped, not kept as a second target. Takeover, takeover baselines and hand-back, maintainer scripts, purge semantics, the APT archive and dpkg locking are deleted.
- **Registry and signing.** Images are published to `ghcr.io/andaru-project/andaru` and signed with a cosign key pair. The image refuses unsigned updates. Keyless signing was chosen first and then dropped: `policy.json` can match a keyless signature only by an email identity, and GitHub workflow certificates carry none.
- **No third-party software in the image.** Tailscale is not shipped, not even disabled. Packages that matter to a firewall and come from Fedora, such as OpenVPN, are included by default.
- **WireGuard is managed by Andaru** through systemd-networkd, which configures WireGuard natively.
- **Guest tools.** The image works as a Proxmox guest without extra installation.
- **Add-ons are containers.** Third-party software such as Tailscale is installable as an add-on: a container the engine runs through Podman Quadlet, inside the normal apply, confirm and rollback cycle. Package layering (`rpm-ostree install`) is an unsupported escape hatch.

## Author calls

1. **Base: stock Fedora CoreOS, not uCore.** The image is built `FROM quay.io/fedora/fedora-coreos:stable`. ucore-minimal replaces Fedora's kernel with a version-locked one for ZFS, adds firewalld, Cockpit, Docker, Tailscale and other software a firewall does not need, changes the update policy, and adds a rebuild hop between Fedora's fixes and Andaru's. The spike used nothing uCore adds. The base is one line of the Containerfile, so this call is cheap to reverse.
2. **Image contents.** Added from Fedora's repositories: `systemd-networkd`, `kea`, `kea-hooks`, `unbound`, `ppp`, `openvpn` and `qemu-guest-agent`. Removed: `moby-engine` with `containerd` and the `docker.socket` enablement, because add-ons run on Podman. `wireguard-tools` stays, as stock and useful for diagnostics (`wg show`). The IPsec implementation is chosen in the VPN slice. Images are built for x86_64 and aarch64, as Andaru's CI already is.
3. **Services and boot.** NetworkManager is masked and systemd-networkd enabled. `zincati` is disabled, because Andaru drives updates itself. Stock Fedora CoreOS has no firewalld, so there is nothing to mask. A `kargs.d` file adds the serial console, as in the spike. The guest agent's start condition is confirmed in the lab.
4. **Fedora's paths and names.** Andaru's Debian assumptions move to Fedora's:
   - Kea units are `kea-dhcp4.service` and `kea-dhcp6.service`: in `internal/generation/services.go`, the drop-in directories, and the `After=` lines of `andaru-leases.service` and `andaru-prefixes.service`.
   - Kea's hook libraries are under `/usr/lib64/kea/hooks`, and its user is `kea`, not `_kea`.
   - Unbound includes `/etc/unbound/conf.d/*.conf`, not `unbound.conf.d/`.
   - The root trust anchor is not refreshed by Debian's `/usr/libexec/unbound-helper root_trust_anchor_update`, which `internal/generation/services.go` calls and whose failure it ignores. Fedora uses `unbound-anchor`. Andaru calls Fedora's mechanism and treats its failure as an error.
   - pppd plugins are under `/usr/lib64/pppd/<version>/`. The lab confirms that the kernel has `ppp_generic` and PPPoE.
   - `TOOL_MISSING` messages name Fedora's packages.
5. **Confinement.** The AppArmor profiles are deleted. Until a later slice writes SELinux policy modules, Andaru and Kea run as `unconfined_service_t`, as in the spike, with SELinux enforcing for everything else.
6. **Before setup.** The image ships `/usr/lib/systemd/network/99-andaru-factory.network`, with DHCP on every `en*` link, so the first boot has a network for the rebase and for setup. Before networkd starts, an Andaru unit loads a static default-deny filter that admits only DHCPv4 and DHCPv6 client replies, ICMPv6 neighbour discovery and router advertisements, and established and related traffic, so that IPv4, IPv6-only and SLAAC uplinks all work before setup. It is Andaru's own `inet andaru` table, not a second table, so the first applied generation replaces it atomically, as every apply replaces the previous filter, and no leftover drop chain can block what the generation accepts. At boot, `andaru-network-boot` loads the confirmed generation's filter when there is one and the default-deny filter otherwise; a rollback restores the previous generation's filter, or the default-deny filter when it rolls back the first apply. `sshd` is disabled in the image. Setup decides how the operator reaches the console and when SSH is enabled.
7. **Unclassified links after setup.**
   - Every generation renders `51-andaru-unclassified.network`, matching `Name=en*` like the factory file. It gives the link no address, no DHCP and `ActivationPolicy=down`.
   - It sorts after every `50-andaru-<link>.network`, so a declared link, whatever its name, always matches its own file first, and it sorts before `99-andaru-factory.network`, so the factory file never matches once a generation exists.
   - It matches only `en*` names, so add-on interfaces such as `tailscale0`, container `veth` links, WireGuard and `lo` stay outside it.
   - The realizer treats the `51-andaru-` prefix in its own network directory as Andaru's, as it does `50-andaru-` today.
   - After a factory reset, the binding rules still hold: WAN and unclassified links never listen for recovery, and boot invents no address. When the kept copies show that the box was set up before, `andaru-network-boot` writes the catch-all even when no generation can come up, so the factory file never brings up DHCP on WAN after a reset. A box reset before its first LAN ever came up has no kept copies and returns to the factory network, which is its factory state.
8. **Updates.** Updates go through the engine, which runs `bootc upgrade` to stage the next image and reboots when the administrator chooses. `bootc rollback` is the rollback. Before staging, the engine checks that it is idle, as factory reset does today. An image rollback can boot an older Andaru on state written by a newer one. The update slice designs how that is handled, under one constraint: a rollback never brings back accounts, credentials or secrets that a factory reset or a revocation removed.
9. **Add-ons.**
   - Add-ons come from a catalog shipped in the image, and each entry pins its image by digest. The signed Andaru image vouches for the digest.
   - The engine renders a Quadlet file per enabled add-on, in the same generation as the network.
   - Add-ons use host networking only, because Podman's bridge networks write their own nft tables. Bridged add-ons need a later coexistence design.
   - An add-on's interface, such as `tailscale0`, joins a zone. The add-on runs with its own firewall management off: `--netfilter-mode=off` for Tailscale.
   - Add-on state lives under `/var/lib/andaru/addons/`, the name the wipeable-state design reserved, and factory reset wipes it with the rest of that tree.
10. **First install route.**
    - Install media are the Fedora CoreOS live ISO, customised with `coreos-installer iso customize --dest-ignition` to embed Andaru's Ignition config for the installed system.
    - The live environment does not run Andaru. On the console it lists the disks, the operator picks one and confirms that it will be erased, and `coreos-installer install` writes Fedora CoreOS to it with the embedded Ignition config, then reboots into the installed disk. `--dest-device` gives an unattended install for a known disk. Everything below runs on the installed system's first boot.
    - The Ignition config masks `zincati.service`, so stock Fedora CoreOS cannot update and reboot during the rebase. It also writes `policy.json`, the `registries.d` entry and the public key, because stock Fedora CoreOS would otherwise accept an unsigned image.
    - A unit Ignition provisions then rebases to `ostree-image-signed:docker://ghcr.io/andaru-project/andaru:stable`. The unit runs after `network-online.target`, retries, and runs once.
    - The `/etc` merge treats a file as locally modified when its content, mode, ownership, extended attributes or SELinux label differ from the old default, and then keeps it forever. So once the rebase has booted Andaru's image, a run-once unit resets those three files from the image's default `/etc` copies with all their metadata and restores their SELinux labels. Slice 2 qualifies that a later image which rotates the public key reaches an installed box.
    - This route needs the internet on first boot. Pre-built disk images are a later slice.

## Signing

The release workflow signs each image with `cosign sign --key`, using a private key held as a GitHub Actions secret. Moving the key to a KMS is open. The image ships `/etc/containers/policy.json` with a `sigstoreSigned` requirement and `keyPath` for `ghcr.io/andaru-project`, the public key, and a `registries.d` entry with `use-sigstore-attachments: true`. Everything else keeps Fedora CoreOS's defaults. A tag outside `main` cannot produce an image, as it cannot produce a release today. Slice 1 qualifies that bootc refuses an unsigned and a wrongly signed image, and pins the cosign version and flags that produce signatures containers/image reads.

Tags: `stable` and the release version (`1.2.3`, `1.2.3-rcN`). A version tag is never rewritten. A scheduled workflow rebuilds the latest release's commit on the current Fedora CoreOS stable, so that Fedora's security fixes reach Andaru without an Andaru release. It publishes the result as `1.2.3-YYYYMMDD` and moves `stable` to it. The scheduled workflow builds only a commit that a release tag on `main` points to. The signing key is available only to a GitHub environment restricted to release tags and that scheduled workflow on `main`.

## What this supersedes

- [Debian packages and the signed APT repository](2026-11-14-debian-packages-design.md), in full.
- [Conventional Debian upgrades and explicit recovery](2026-11-14-apt-upgrades-design.md), in full. Image updates replace it, including the `/var/lib/andaru-upgrades` repair copy.
- The takeover designs: [ifupdown takeover](2026-11-11-ifupdown-takeover-design.md) and [interactive takeover](2026-11-11-interactive-takeover-design.md) in full, and [network takeover apply](2026-11-11-network-takeover-apply-design.md) in part. That document's rollback timer, confirmation from a new session and confirmed-generation boot rules stay binding for every apply. The pre-Andaru baseline becomes the factory network.
- In [first setup](2026-11-18-andaru-setup-design.md): the takeover step, the separate interactive takeover assistant, the package-removal decision, the rule that Andaru never installs or enables OpenSSH, the postinst install hint (its author call 10) and the `apt install` qualification. On the image, setup is offered on the console at first boot. Setup is re-planned as an appliance wizard on an image that already owns the network.
- In the [2026-11-15](2026-11-15-factory-reset-design.md) and [2026-11-17](2026-11-17-factory-reset-data-design.md) factory reset designs: dpkg locking, the network-ownership record, the takeover-aware teardown, the OpenSSH rule, plugins bundled in the package, and the maintainer-script constraints. The wipe, the kept copies and the boot after a wipe stay.
- Qualification on "both Debian baselines", in every design that requires it, becomes qualification on the image.

## Slices

1. **Image and CI.** The Containerfile for both architectures, built on every pull request and pushed and signed on tags, with the signature policy, the service changes, Fedora's paths and names, the pre-setup filter and the factory network. Qualified on a Proxmox VM: boot, apply and confirm a declaration, Kea DHCP with lease events, Unbound, PPPoE, and bootc refusing an unsigned image.
2. **Install media.** The customised live ISO with the signed rebase.
3. **Deletion.** First, the idle check and writer stop that factory reset takes from the `upgrade` package move into reset, and reset's dpkg locks go. Nothing on the image updates by itself (zincati and the bootc and rpm-ostree update timers are off, which `image-smoke.sh` checks), so the fence against a concurrent image update waits for slice 5. Then the Debian packaging, the APT release jobs, the takeover subsystem, the netplan and ifupdown readers, the rest of the `upgrade` package and the AppArmor profiles are deleted. The `andaru-project/apt` repository is archived.
4. **Setup.** The appliance wizard: console access, SSH, management election, first administrator and the web service, against the merged setup design minus what this document supersedes.
5. **Updates.** Engine-driven `bootc upgrade` and rollback, state across an image rollback, and the factory reset's fence against a concurrent image update.
6. **Add-ons.** The catalog, the Quadlet rendering and Tailscale as the first add-on. The first task is a lab check of Tailscale in a container under SELinux, with host networking and `/dev/net/tun`.
7. **VPN.** Andaru-managed WireGuard, OpenVPN and IPsec, with the IPsec implementation chosen there.
8. **SELinux.** Policy modules for Andaru and Kea.

## Non-goals

- Supporting Debian or any host Andaru does not own.
- Shipping third-party software in the image.
- Managing or detecting layered packages. The documentation warns that a layered package can block an image update.
- An offline installer in the first slices.

## Risks

- **First boot needs the internet.** The live ISO route pulls the full Andaru image on first boot; the spike's ucore-minimal base alone was 2.9 GB. A site without upstream DHCP cannot install. Pre-built disk images remove this, but `bootc install to-disk` produced an unbootable disk in the spike and `bootc-image-builder` is untested.
- **Signing compatibility.** Recent cosign releases default to signature formats that containers/image may not read. Slice 1 pins what works.
- **Unconfined services.** Until slice 8, Andaru and Kea run without confinement, a regression from the AppArmor profiles on Debian.
- **Console access.** Stock Fedora CoreOS has no console password and takes SSH keys from Ignition. How an operator reaches the console for setup is the setup slice's first question.
- **Release dependency on Fedora.** A bad Fedora CoreOS stable release reaches Andaru through the scheduled rebuild. Slice 1 qualifies the rebuilt image before it is published and provides a way to pin the base.
- **`/etc` merge.** Local changes in `/etc` win over the image's on update. Andaru keeps generated configuration where it is today and treats hand edits in its own paths as foreign, as before.
