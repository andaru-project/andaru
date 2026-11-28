# Native VPN (WireGuard, strongSwan XFRM, OpenVPN DCO) + SELinux Lab Qualification Spike

Date: 2026-11-28  
Scope: Slice 7 lab gate for [2026-11-28-vpn-pki-acme-design.md](../superpowers/specs/2026-11-28-vpn-pki-acme-design.md) and Slice 7 of [2026-11-18-appliance-image-design.md](../superpowers/specs/2026-11-18-appliance-image-design.md).

## Outcome

**Passed.** Native `systemd-networkd` WireGuard (`wg0`), `strongSwan 6.1.0` (`charon-systemd` + `swanctl`) over `systemd-networkd` Linux XFRM route-based interfaces (`ipsec0`, `Kind=xfrm`, `Independent=yes`), and `OpenVPN 2.7.9` with upstream Linux kernel DCO (`ovpn0`, `ovpn` kernel module) all run simultaneously under **SELinux `Enforcing`** on Fedora CoreOS `45.20261030.3.1` (`7.2.11-300.fc45.x86_64`) with **zero** VPN daemon SELinux AVC denials (using 3 CIL `filecon` rules), **zero** competing nftables tables (only `table inet andaru`), clean `systemd-networkd` coexistence (`51-andaru-unclassified.network` ignores `wg*`, `ipsec*`, and `ovpn*`), working `table inet andaru` zone interface-name sets across all three tunnel types, and a clean `bootc container lint --fatal-warnings` build with `strongswan` installed.

Four concrete platform facts were discovered empirically and incorporated into the Slice 7 design:

1. **Fedora 45 kernel `7.2.11-300.fc45` ships all four kernel modules stock:** `wireguard.ko.xz`, `xfrm_interface.ko.xz`, `tun.ko.xz`, and upstream **`ovpn.ko.xz` (OpenVPN Data Channel Offload)**. OpenVPN `2.7.9` activates DCO automatically (`GLOBAL_STATS dco_enabled 1`) on `ovpn0` with AEAD ciphers (`AES-256-GCM`, `CHACHA20-POLY1305`).
2. **Fedora 45 `strongswan 6.1.0` paths and defaults:**
   - Binary/unit: `/usr/bin/charon-systemd` (`ipsec_exec_t` → `ipsec_t`), `/usr/bin/swanctl` (`ipsec_mgmt_exec_t` → `ipsec_mgmt_t`), unit `strongswan.service` (`Type=notify`).
   - VICI Unix socket path is **`/run/strongswan/charon.vici`** (`ipsec_var_run_t`, `0660 root:root`), not `/run/charon.vici`.
   - `kernel-netlink.install_routes_xfrmi = no` is already the default, leaving XFRM interface routing 100% under Andaru's `systemd-networkd` `[Route]` sections.
   - `swanctl --load-all --file /run/andaru-ipsec/swanctl.conf --noprompt` scans sibling credential subdirectories (`x509/`, `x509ca/`, `private/`, etc.) beside the config file; creating those directories (empty when unused) avoids benign warning messages and allows clean credential loading without writing to `/etc/strongswan/swanctl/`.
3. **OpenVPN SELinux socket separation (`openvpn_etc_t` vs `openvpn_var_run_t`):**
   - Placing `mgmt.sock` inside `/run/andaru-openvpn/<id>/` (`openvpn_etc_t`) fails under SELinux enforcing with:
     `avc: denied { create } for comm="openvpn" name="mgmt.sock" scontext=openvpn_t tcontext=openvpn_etc_t tclass=sock_file permissive=0`
   - Separating runtime sockets into `/run/andaru-openvpn/run/<id>/mgmt.sock` labeled `openvpn_var_run_t` resolves this completely with zero AVCs while keeping config and PEM files read-only (`openvpn_etc_t`).
   - Note: OpenVPN 2.7.9 deprecates `--persist-key` (ignored with warning); generated configs should omit it.
4. **Exact three CIL `filecon` lines required in `image/files/usr/share/andaru/selinux/andaru.cil`:**
   ```cil
   (filecon "/run/andaru-ipsec(/.*)?" any (system_u object_r ipsec_conf_file_t ((s0) (s0))))
   (filecon "/run/andaru-openvpn(/.*)?" any (system_u object_r openvpn_etc_t ((s0) (s0))))
   (filecon "/run/andaru-openvpn/run(/.*)?" any (system_u object_r openvpn_var_run_t ((s0) (s0))))
   ```

## Environment

- **Hypervisor:** `proxmox-5`
- **VM:** throwaway VM `9132` (`andaru-vpn-spike`), OVMF, q35, 2 vCPU, 4 GiB RAM, 24 GiB ZFS volume, single virtio NIC (`ens18`), destroyed and purged (`qm destroy 9132 --purge`) immediately after verification. Always-on VMs `130` (`andaru`) and `131` (`andaru-client`) were untouched.
- **Image:** Freshly built from `image/Containerfile` with `strongswan` (`6.1.0`) added to `dnf install` and the 3 CIL lines compiled via `semodule -i`, passing `bootc container lint --fatal-warnings` (`14 passed, 1 skipped, 0 warnings`), booted directly as `Andaru 0.7.0-spike (Fedora CoreOS 45.20261030.3.1)` with kernel `7.2.11-300.fc45.x86_64` and `SELinux Enforcing`.

## Verified facts and evidence

### 1. WireGuard via `systemd-networkd`

- Key files placed at `/run/systemd/network/50-andaru-wg0.key` and `50-andaru-wg0-peer1.psk` (`0640 root:systemd-network`, `net_conf_t`), `.netdev` (`Kind=wireguard`, `MTUBytes=1420`, `ListenPort=51820`, `PrivateKeyFile=`, `PresharedKeyFile=`), `.network` (`Address=10.200.0.1/24`, `IPv4Forwarding=yes`, `IPv6Forwarding=yes`, `[Route] Destination=10.200.10.0/24`).
- `networkctl reload` created `wg0`:
  - `4: wg0: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420 ... wireguard`
  - `networkctl list`: `4 wg0 wireguard routable configured`
  - `wg show wg0 dump` returned local pubkey, listen port `51820`, peer pubkey, PSK, `10.200.10.0/24,10.200.0.2/32`, and keepalive `25`.
  - `ip route show dev wg0` showed `10.200.10.0/24 proto static scope link`.
  - Zero SELinux AVCs.

### 2. IPsec (`strongSwan 6.1.0` + `systemd-networkd` `Kind=xfrm`)

- `50-andaru-ipsec0.netdev` (`Kind=xfrm`, `InterfaceId=42`, `Independent=yes`) + `50-andaru-ipsec0.network` (`Address=169.254.42.1/30`, `[Route] Destination=10.201.0.0/24`).
- `networkctl reload` created `ipsec0@lo` (`xfrm if_id 0x2a`, `UP,LOWER_UP`, `configured`) with static route `10.201.0.0/24 dev ipsec0`.
- `strongswan.service` started `charon-systemd` confined as `system_u:system_r:ipsec_t:s0`, creating `/run/strongswan/charon.vici` (`system_u:object_r:ipsec_var_run_t:s0`, `0660 root:root`).
- `swanctl --load-all --file /run/andaru-ipsec/swanctl.conf --noprompt` (`ipsec_conf_file_t`) loaded connection `site_a` (`IKEv2`, `if_id_in = 42`, `if_id_out = 42`, `start_action = trap`) and installed kernel XFRM `out`/`fwd`/`in` policies with `if_id 0x2a`.
- Zero SELinux AVCs.

### 3. OpenVPN `2.7.9` (kernel DCO + Unix management socket)

- Config and generated ECDSA CA/server certs + `tls-crypt` static key under `/run/andaru-openvpn/ovpn0/` (`openvpn_etc_t`); management socket at `/run/andaru-openvpn/run/ovpn0/mgmt.sock` (`openvpn_var_run_t`, `management-client-user root`).
- `andaru-openvpn-ovpn0.service` (`Type=notify`) ran `/usr/bin/openvpn` confined as `system_u:system_r:openvpn_t:s0`.
- Created `6: ovpn0: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1500 ... ovpn` (`networkctl list`: `6 ovpn0 ovpn routable unmanaged`).
- Querying `status 3` on `/run/andaru-openvpn/run/ovpn0/mgmt.sock` returned structured TSV headers (`CLIENT_LIST`, `ROUTING_TABLE`, `GLOBAL_STATS dco_enabled 1`).
- Zero SELinux AVCs after separating the socket directory to `openvpn_var_run_t`.

### 4. nftables coexistence

- `nft list tables` showed **only** `table inet andaru` while WireGuard, strongSwan, and OpenVPN were all active simultaneously.
- Added `set zone_vpn_spike { type ifname; elements = { "wg0", "ovpn0", "ipsec0" }; }` to `table inet andaru` cleanly.
