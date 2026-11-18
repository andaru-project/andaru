# Andaru as a uCore appliance image

## Conclusion

**Andaru's core runs unchanged on a Fedora CoreOS–based uCore image.** The engine runs, the configuration store works, and network applies with confirmation, the nft filter and the boot service all behave as on Debian. Image updates and rollbacks preserve Andaru's state, and an update downloads only the layers that changed.

On an appliance, Andaru owns the network from the first boot. That removes the takeover subsystem and everything tied to dpkg. In exchange, the port has a list of concrete, bounded items:
- install media;
- masking firewalld;
- Fedora's unit names;
- SELinux policy;
- an update channel with signing.

This is a bounded spike on a throwaway VM (2026-11-18), on the question of whether Andaru should ship as its own image instead of installing onto any Debian host. It is not an image build pipeline, an installer or an update service.

## What was built

The image was built `FROM ghcr.io/ublue-os/ucore-minimal:stable`, which reported Fedora CoreOS 44.20261023.3.1, bootc 1.17.1 and rpm-ostree 2026.3. Its 2.89 GB base already contains nftables, Podman, OpenSSH, the QEMU guest agent and NetworkManager. It has no systemd-networkd, Kea or Unbound.

```dockerfile
FROM ghcr.io/ublue-os/ucore-minimal:stable
RUN dnf -y install systemd-networkd kea unbound && dnf clean all
COPY root/ /            # the payload of the branch's andaru .deb, minus AppArmor; plus the files below
RUN systemctl disable NetworkManager.service NetworkManager-wait-online.service && \
    systemctl mask NetworkManager.service firewalld.service && \
    systemctl enable systemd-networkd.service andaru-engine.service andaru-network-boot.service \
        andaru-factory-reset.service andaru-leases.path andaru-prefixes.path andaru-prefixes.timer
RUN rm -rf /run/dnf /run/kea /run/unbound /tmp/* /var/log/dnf5.log && bootc container lint
```

The extra files:
- `/usr/lib/bootc/kargs.d/10-andaru-console.toml`, adding a serial console;
- `/usr/lib/systemd/network/99-andaru-factory.network`, with DHCP on every `en*`: the factory network the image boots with before setup;
- for the spike only, `/etc/containers/registries.conf.d/` pointing at a local plain-HTTP registry.

The Debian package's payload worked as is: the static Go binaries, units, tmpfiles, sysusers and web assets. `bootc container lint` passed with warnings only about leftovers from package installs.

## Installing

`bootc install to-disk` produced a disk that does not boot. grub stops with `search.c: no such device: boot`, because Fedora CoreOS's grub configuration looks for a separate partition labelled `boot`, and bootc's layout for this image has none.

The path that works is Fedora CoreOS's own:
1. `coreos-installer install --stream stable --ignition-file … --console ttyS0,115200n8` writes stock Fedora CoreOS to a disk.
2. On first boot, an Ignition-provisioned unit runs `rpm-ostree rebase ostree-unverified-registry:<registry>/andaru-ucore:spike` and reboots.

This is also how uCore documents its install. `bootc status` then showed the Andaru image booted and stock Fedora CoreOS as the rollback.

For a product this means install media built on `coreos-installer` with Andaru's Ignition embedded, or images pre-rebased to Andaru's image. The `bootc-image-builder` route was not tried.

## Running Andaru

All of this ran with SELinux enforcing.
- **Factory network:** systemd-networkd brought both NICs up by DHCP from the factory network file.
- **Engine:** `andaru-engine` started and initialised its store, and `andaru ctl` answered.
- **Takeover is gone:** the takeover assistant refused with `TAKEOVER_UNSUPPORTED_BASELINE`, since there is neither netplan nor ifupdown. It is not needed: Andaru's `50-andaru-*` files sort before the factory `99-` file. A saved declaration applied with `takeover: false`, with WAN by DHCP on `ens18` and a static LAN with management on `ens19`.
- **firewalld:** the apply gate first refused with `FOREIGN_FILTER` until firewalld was masked. uCore enables it; the image must mask it.
- **Confirmation:** the apply reached `awaiting_confirmation` and was confirmed from a new SSH session on the LAN. The filter allowed SSH on `ens19` only, and port 22 on the WAN was closed.
- **Kea:** Fedora's Kea 3.0.5 ran from a configuration under `/run/andaru-kea` with no SELinux denial. It does so because it runs as `unconfined_service_t`: Fedora has no policy module for Kea. Andaru itself also runs unconfined.

## Updating and rolling back

The image was rebuilt as v2 (a marker file, firewalld masked) and pushed to the registry.
- **Upgrade:** `bootc upgrade` staged it with a 13.2 MB download, three changed layers. After a reboot, v2 was booted.
- **State survived:** `/var/lib/andaru` was intact, and `andaru-network-boot` restored the confirmed generation, with addresses and filter, before networkd started.
- **Rollback:** `bootc rollback` and a reboot returned to v1, again with Andaru's state and network intact.
- **`/etc`:** a change made by hand in `/etc` (the firewalld mask) persisted across both image changes, because `/etc` is merged three ways, not replaced.

## What the port would take

| Item | Finding |
| --- | --- |
| Install media | `coreos-installer` with an embedded Ignition config, then a rebase, or pre-rebased disk and ISO images. `bootc install to-disk` does not boot Fedora CoreOS–based images. |
| Factory network | The image ships a default `.network` so first setup has a network. Setup becomes an appliance wizard with no takeover. |
| firewalld | Mask it in the image. |
| NetworkManager | Mask it and add `systemd-networkd`; worked as is. |
| Kea unit names | Fedora's are `kea-dhcp4.service` and `kea-dhcp6.service`. Andaru's code and drop-ins use Debian's `kea-dhcp4-server.service`. |
| Confinement | Andaru's AppArmor profiles have no SELinux counterpart. Kea and Andaru run as `unconfined_service_t`. Parity means writing SELinux policy modules. |
| Updates | A registry and image signing (sigstore/cosign and a `policy.json`), neither tested. The base is 2.9 GB; updates download only changed layers. |
| `/etc` | Local `/etc` changes persist across images. Andaru should keep generated configuration in `/var` or `/run`, or accept the merge. |
| Lab control | The QEMU guest agent runs confined by SELinux and cannot execute commands; the serial console and SSH work. |

**What becomes unnecessary:**
- the takeover assistant, takeover baselines, masks and hand-back;
- the ifupdown DUID carry-over;
- the apt upgrade and recovery-preparation work (#56);
- maintainer scripts, prerm guards and purge semantics (#67);
- dpkg locking in factory reset.

Factory reset reduces to wiping `/var/lib/andaru` on a known image. The kept-copy and ownership machinery could shrink, though that was not tested.

## Not tested

- Unbound.
- The web service.
- IPv6 prefix delegation.
- Real hardware, Secure Boot and signed images.
- Automatic updates.
- An installer ISO or `bootc-image-builder`.
- Factory reset on the image.
- Running the takeover-free first setup end to end.

## Environment

- **VM:** throwaway VM 9121 on `proxmox-5`: OVMF, q35, two virtio NICs on `vmbr0`, 2 vCPU, 4 GB, 20 GB.
- **Registry:** a temporary plain-HTTP registry on the build host.
- **Cleanup:** the VM, its imported disk image and the registry were all deleted after the spike.
