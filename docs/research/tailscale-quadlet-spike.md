# Tailscale Podman Quadlet + SELinux Lab Qualification Spike

Date: 2026-11-27  
Scope: Slice 6 lab gate for [2026-11-27-addons-design.md](../superpowers/specs/2026-11-27-addons-design.md) and Slice 6 of [2026-11-18-appliance-image-design.md](../superpowers/specs/2026-11-18-appliance-image-design.md).

## Outcome

**Passed.** Official digest-pinned `ghcr.io/tailscale/tailscale` runs cleanly as a system Podman Quadlet under SELinux `Enforcing` (`container_t` with per-container MCS isolation), host networking, `Pull=never`, `NoNewPrivileges=true`, `DropCapability=ALL`, `AddCapability=NET_ADMIN` alone (`NET_RAW` is **not** required), `/dev/net/tun`, and `--netfilter-mode=off`, with **zero** `container_t` SELinux AVC denials, **zero** Podman or Tailscale nftables tables, clean `systemd-networkd` coexistence (`tailscale0` unmanaged), working `table inet andaru` interface-name zone set matching on `tailscale0`, and fail-closed safe-mode gating via both `/run/andaru/safe-mode` and `andaru.safe=1`.

Three concrete runtime requirements were uncovered empirically and fed directly into the add-on design:

1. **Per-add-on `/run` secret and bus-token mounts require `:ro,Z` (not bare `:ro`).** Files created under `/run/andaru-addons/...` inherit `var_run_t`, which `container_t` cannot read under SELinux enforcing (`avc: denied { read } ... tcontext=var_run_t tclass=file`). Adding `,Z` lets Podman relabel the per-add-on secrets directory to `system_u:object_r:container_file_t:s0:<MCS>`, isolating it to that container with zero custom SELinux policy rules.
2. **Tailscale requires `NET_ADMIN` only (`NET_RAW` is unnecessary).** With `DropCapability=ALL` and `AddCapability=NET_ADMIN`, `tailscaled` runs with `CapPrm=CapEff=CapBnd=0000000000001000` and `NoNewPrivs=1`, creates kernel `tailscale0`, and reaches the Tailscale control plane.
3. **Add-on event bus socket (`/run/andaru-addons/bus.sock`) requires two CIL lines in `image/files/usr/share/andaru/selinux/andaru.cil`.** Because `andaru-engine` runs in `unconfined_service_t`, `container_t` connecting to `/run/andaru-addons/bus.sock` hits `sock_file { write }` (on `var_run_t`) and `unix_stream_socket { connectto }` (against `unconfined_service_t`). Both are cleanly resolved with zero AVCs by:
   ```cil
   (filecon "/run/andaru-addons/bus.sock" socket (system_u object_r container_file_t ((s0) (s0))))
   (allow container_t unconfined_service_t (unix_stream_socket (connectto)))
   ```

## Environment

- **Hypervisor:** `proxmox-5`
- **VM:** throwaway VM `9130` (`andaru-tailscale-spike`), OVMF, q35, 2 vCPU, 4 GiB RAM, 24 GiB ZFS volume, single virtio NIC (`ens18`), destroyed and purged (`qm destroy 9130 --purge`) immediately after the spike. Existing always-on VMs `130` (`andaru`) and `131` (`andaru-client`) were untouched.
- **OS / Image:** Installed from `andaru-lab-x86_64.iso` and booted `ghcr.io/andaru-project/andaru:0.5.0-a.2` (`sha256:fa7540fd02a4cb1641a742ccd59b7fad8ad12139cdf4659f33fb4c82f3cb09cf`), Fedora CoreOS `45.20261030.3.1`, kernel `7.2.11-300.fc45.x86_64`, Podman `6.1.3`, SELinux `Enforcing` (with `andaru` CIL module loaded).
- **Lab access:** For this throwaway VM only, enabled `sshd.service` and inserted a temporary `tcp dport 22 accept` rule into `table inet andaru` `chain input` after `andaru-network-boot.service`.

## Verified facts and evidence

### 1. Multi-arch digest-pinned Tailscale image

Inspected `docker://ghcr.io/tailscale/tailscale:stable` via `skopeo inspect --raw` and pulled by immutable OCI index digest:

- Multi-arch OCI index digest: `ghcr.io/tailscale/tailscale@sha256:72c1e19e0aedca96b8533bbb0b4368564410ff20b17e53b804eb6c1f04a31be4`
- Included platforms:
  - `linux/amd64`: `sha256:91b7c58cbce2f1273f1d76046892fcd45b8cdd5efe70a45c8ede53d041e89688`
  - `linux/arm64`: `sha256:8934bed5f68e80c5f9c881f62c65909223ee660432946c10ad212d958fc2c2bb`
- OCI entrypoint/cmd: `Cmd: ["/usr/local/bin/containerboot"]`, `User: null` (UID 0 inside container).

### 2. Quadlet configuration tested

Placed at `/run/containers/systemd/andaru-addon-tailscale.container` (mode `0600`):

```ini
[Unit]
Description=Andaru Add-on: Tailscale
After=andaru-network-boot.service systemd-networkd.service andaru-engine.service
ConditionPathExists=!/run/andaru/safe-mode
ConditionPathExists=!/var/lib/andaru/safe-mode-next
ConditionPathExists=!/run/andaru/addons-held
ConditionKernelCommandLine=!andaru.safe=1

[Container]
Image=ghcr.io/tailscale/tailscale@sha256:72c1e19e0aedca96b8533bbb0b4368564410ff20b17e53b804eb6c1f04a31be4
Pull=never
Network=host
NoNewPrivileges=true
DropCapability=ALL
AddCapability=NET_ADMIN
AddDevice=/dev/net/tun
Volume=/var/lib/andaru/addons/tailscale/state:/var/lib/tailscale:Z
Volume=/run/andaru-addons/tailscale/secrets:/run/andaru/secrets:ro,Z
Environment=TS_USERSPACE=false
Environment=TS_STATE_DIR=/var/lib/tailscale
Environment=TS_EXTRA_ARGS=--netfilter-mode=off

[Service]
Restart=on-failure
RestartSec=5
TimeoutStopSec=5
```

`podman-system-generator` translated this into `/run/systemd/generator/andaru-addon-tailscale.service` with `--pull never --network host --security-opt=no-new-privileges --device /dev/net/tun --cap-drop all --cap-add net_admin`.

### 3. Secret mount SELinux check (`:ro` vs `:ro,Z`)

- With `:ro` (no `,Z`), `TS_AUTHKEY=file:/run/andaru/secrets/andaru-addon-tailscale-auth_key.secret` failed immediately with:
  - Journal: `open /run/andaru/secrets/andaru-addon-tailscale-auth_key.secret: permission denied`
  - Audit: `avc: denied { read } for comm="tailscale" name="andaru-addon-tailscale-auth_key.secret" scontext=system_u:system_r:container_t:s0:c423,c999 tcontext=unconfined_u:object_r:var_run_t:s0 tclass=file permissive=0`
- With `:ro,Z`:
  - Host directory and file were relabeled to `system_u:object_r:container_file_t:s0:c482,c930`.
  - `containerboot` read the file cleanly and attempted control-plane registration (returning `invalid key: unable to validate API key` for the dummy lab key) with **zero** `container_t` AVC denials.
  - Note on `containerboot` behavior: if an invalid `TS_AUTHKEY` is passed, `containerboot` runs `tailscale up`, sends `SIGTERM` to `tailscaled`, and exits with status 1. Without `TS_AUTHKEY`, `tailscaled` stays active, creates `tailscale0`, and waits in `NeedsLogin` with an interactive auth URL.

### 4. Capabilities, SELinux confinement, `tailscale0`, and `systemd-networkd`

With steady-state `andaru-addon-tailscale.service` active:

- Process labels (`ps -eZ`):
  - `conmon`: `system_u:system_r:container_runtime_t:s0`
  - `containerboot` and `tailscaled`: `system_u:system_r:container_t:s0:c482,c930`
- Capabilities (`/proc/<pid>/status` for `tailscaled`):
  - `CapPrm: 0000000000001000` (`CAP_NET_ADMIN` only)
  - `CapEff: 0000000000001000`
  - `CapBnd: 0000000000001000`
  - `NoNewPrivs: 1`
- Interface creation (`ip -d link show tailscale0`):
  - `16: tailscale0: <POINTOPOINT,MULTICAST,NOARP,UP,LOWER_UP> mtu 1280 ... tun type tun`
- `systemd-networkd` coexistence (`networkctl list`):
  - `16 tailscale0 none degraded unmanaged` (`51-andaru-unclassified.network` matches `en*` only and leaves `tailscale0` untouched).
- SELinux audit (`ausearch -m avc`): **zero** `container_t` AVCs.

### 5. nftables coexistence and zone interface-name set enforcement

- `nft list ruleset` while Tailscale was active showed **only** `table inet andaru` — zero Podman or Tailscale tables or chains (`Warning: netfilter=off; configure iptables yourself.` logged by `tailscaled`).
- Added dynamic interface-name set `zone_vpn` (`type ifname; elements = { "tailscale0" }`) and an `oifname @zone_vpn counter accept` rule in `table inet andaru`. Routing a test packet out `tailscale0` incremented the kernel counter (`oifname @zone_vpn counter packets 3 bytes 172 accept`), confirming Andaru's zone set model enforces rules on dynamically created add-on interfaces.

### 6. Event bus Unix socket SELinux check (`/run/andaru-addons/bus.sock`)

Simulated `andaru-engine` listening on `/run/andaru-addons/bus.sock` inside a systemd service (`system_u:system_r:unconfined_service_t:s0`) and connected from a confined `container_t` container:

1. Without relabeling or policy: denied (`avc: denied { write } ... tcontext=var_run_t tclass=sock_file`).
2. With `:z` mount option alone: denied (`avc: denied { connectto } ... scontext=container_t tcontext=unconfined_service_t tclass=unix_stream_socket`).
3. With the 2-line CIL module above installed and `restorecon /run/andaru-addons/bus.sock`: connection succeeded (`ACCEPTED_CONTAINER_BUS_CONNECTION`) with **zero** `container_t` AVCs.

### 7. Safe-mode gating (`/run/andaru/safe-mode` and `andaru.safe=1`)

- **Runtime marker:** Creating `/run/andaru/safe-mode` caused `systemctl start andaru-addon-tailscale.service` to skip the unit (`ActiveState=inactive`, `ConditionResult=no`, journal: `skipped, unmet condition check ConditionPathExists=!/run/andaru/safe-mode`).
- **Kernel argument across real reboot:** Appended `andaru.safe=1` via `rpm-ostree kargs --append=andaru.safe=1`, placed the Quadlet under `/etc/containers/systemd/` with `WantedBy=multi-user.target` to force boot activation attempt, and rebooted. After boot:
  - `/proc/cmdline` contained `andaru.safe=1` and SELinux remained `Enforcing`.
  - `andaru-addon-tailscale.service` was skipped (`ActiveState=inactive`, `ConditionResult=no`, journal: `skipped, unmet condition check ConditionKernelCommandLine=!andaru.safe=1`).
  - `podman ps -a` showed zero containers, while `ens18` (`10.0.1.12/16`) and `table inet andaru` came up normally.
