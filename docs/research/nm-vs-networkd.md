# NetworkManager vs systemd-networkd as the Andaru interface backend on Debian 13

Research for issue #3. Facts only, from primary sources (upstream man pages/docs, Debian
archive metadata, Cockpit source). No decision is made here; trade-offs are listed at the
end for the human to weigh.

Method note: Debian package facts below were taken from the trixie `Packages` index and
from the extracted `.deb` payloads (unit files, `postinst`, shipped config) fetched from
`deb.debian.org` on 2026-08-15. systemd man-page quotes are from the v257 sources in the
upstream git tree (matching the trixie systemd 257.x line); NetworkManager quotes are from
the 1.52 documentation set (matching trixie's 1.52.1).

## 1. Debian 13 "trixie" packaging facts

| Package | trixie version | Priority | Notes |
| --- | --- | --- | --- |
| `systemd` (contains `systemd-networkd`, `networkctl`, `systemd-networkd-wait-online`) | `257.13-1~deb13u1` | `important` | networkd is inside the main `systemd` binary package, no separate package. Files `usr/lib/systemd/systemd-networkd`, `usr/bin/networkctl`, `usr/lib/systemd/system/systemd-networkd.service` all belong to `admin/systemd` in the trixie `Contents-amd64` index. |
| `network-manager` | `1.52.1-1` | `optional` | Section `net`. Depends on a D-Bus system bus; Recommends `dnsmasq-base`, `modemmanager`, `ppp`, `wpasupplicant`, `polkitd`, `wireless-regdb`, ... |
| `ifupdown` | `0.8.44+deb13u1` | `important` | Ships `networking.service`. Recommends `dhcpcd-base \| dhcp-client`. |
| `dhcpcd-base` | `1:10.1.0-11+deb13u3` | `important` | The DHCP client ifupdown pulls in by default. |
| `nftables` | `1.1.3-1` | `important` | Ships `nftables.service` (installed but **not enabled** on fresh install: postinst only re-enables if `was-enabled`). |
| `netplan.io` | `1.1.2-7` | `optional` | Present in trixie but not a default. |
| `cockpit` / `cockpit-networkmanager` | `337-1+deb13u1` | `optional` | `cockpit` Recommends `cockpit-networkmanager`; that package `Depends: network-manager (>= 1.6)`. |
| `isc-dhcp-client` | `4.4.3-P1-8` | `optional` | Description literally says "(deprecated)". |

Sources: `https://qa.debian.org/madison.php?package=network-manager&text=on`,
`https://qa.debian.org/madison.php?package=systemd&text=on`,
`https://qa.debian.org/madison.php?package=ifupdown&text=on`,
`https://qa.debian.org/madison.php?package=cockpit&text=on`,
`https://qa.debian.org/madison.php?package=netplan.io&text=on`,
`https://deb.debian.org/debian/dists/trixie/main/binary-amd64/Packages.gz` (Priority/Depends fields),
`https://deb.debian.org/debian/dists/trixie/main/Contents-amd64.gz` (file-to-package map),
`https://packages.debian.org/trixie/network-manager`, `https://packages.debian.org/trixie/systemd`.

### What a minimal install gets by default

* Debian Policy 2.5: `important` = "Important programs, including those which one would
  expect to find on any Unix-like system … Other packages without which the system will not
  run well or be usable must also have priority important."
  `https://www.debian.org/doc/debian-policy/ch-archive.html`
* `debootstrap(8)`: the default variant creates "a base Debian installation with all
  packages of priority required and important". Therefore `ifupdown`, `dhcpcd-base`,
  `nftables` and `systemd` (hence networkd binaries) are all present on a minimal trixie
  system; `network-manager` (optional) is **not**.
  `https://manpages.debian.org/trixie/debootstrap/debootstrap.8.en.html`
* Debian wiki NetworkConfiguration: "NetworkManager is installed by default if you select a
  Desktop Environment during installation. It is *not* installed by default on servers (or
  any other system without a Desktop Environment)."
  `https://wiki.debian.org/NetworkConfiguration`
* debian-installer (`netcfg` 1.197 in trixie) `finish-install.d/55netcfg-copy-config`
  chooses the target network stack in this order (from the debconf template
  `netcfg/target_network_config`): "Netplan if available (on Linux only), NetworkManager if
  available (on Linux only), Ethernet or wireless configuration through ifupdown and
  loopback configuration through ifupdown as a fallback." The script has **no
  systemd-networkd branch**; when NM is chosen it copies
  `/etc/NetworkManager/system-connections/*` into the target and rewrites
  `/etc/network/interfaces` to loopback only.
  `https://salsa.debian.org/installer-team/netcfg/-/raw/master/finish-install.d/55netcfg-copy-config`,
  `https://salsa.debian.org/installer-team/netcfg/-/raw/master/debian/netcfg-common.templates`
* `ifupdown` postinst creates `/etc/network/interfaces` (with
  `source /etc/network/interfaces.d/*`) if absent and enables `networking.service`
  ("was-enabled defaults to true, so new installations run enable").
  (`ifupdown_0.8.44+deb13u1_amd64.deb` postinst.)
* `systemd` postinst on Debian enables only `getty@tty1`, `remote-fs.target`,
  `systemd-pstore` on new installs; it does **not** enable `systemd-networkd.service`
  (upstream `90-systemd.preset` lists `enable systemd-networkd.service`, but Debian's
  postinst does not run `preset` for it). Enabling networkd is an explicit
  `systemctl enable systemd-networkd` step; the Debian wiki documents exactly that.
  (`systemd_257.13-1~deb13u1_amd64.deb` postinst; `https://wiki.debian.org/SystemdNetworkd`)
* `network-manager` postinst enables `NetworkManager.service` on first install and starts
  it. Debian's shipped `/etc/NetworkManager/NetworkManager.conf` is:

  ```ini
  [main]
  plugins=ifupdown,keyfile

  [ifupdown]
  managed=false
  ```

  so any interface listed in `/etc/network/interfaces` is ignored by NM
  ("If set to false, then any interface listed in /etc/network/interfaces will be ignored
  by NetworkManager" — `NetworkManager.conf(5)` `[ifupdown] managed`). The postinst also
  prints a warning listing such interfaces.
  (`network-manager_1.52.1-1_amd64.deb` postinst + `/etc/NetworkManager/NetworkManager.conf`;
  `https://networkmanager.dev/docs/api/1.52.0/NetworkManager.conf.html`)
* Debian 13 release notes: "The isc-dhcp suite is deprecated upstream. If you are using
  NetworkManager or systemd-networkd, you can safely remove the isc-dhcp-client package as
  they both ship their own implementation." Also: interface names may change trixie vs
  bookworm (systemd v257 now honours ACPI `_SUN`); the notes recommend `.link` files to pin
  names. `https://www.debian.org/releases/trixie/release-notes/issues.en.html`

## 2. D-Bus API surface and stability

### NetworkManager

* Bus name `org.freedesktop.NetworkManager`; object tree with `Manager`, `Settings`,
  `Settings.Connection`, `Device`, `Device.*` (Wired, Bridge, Vlan, Wireless, WireGuard,
  ...), `Connection.Active`, `IP4Config`, `DHCP4Config`, `Checkpoint`, ... Full reference:
  `https://networkmanager.dev/docs/api/1.52.0/spec.html`. Read/write methods on
  `Manager`, `Settings`, `Settings.Connection`, `Device` are gated by polkit
  ("secured with PolicyKit" in the shipped
  `/usr/share/dbus-1/system.d/org.freedesktop.NetworkManager.conf`); root is always
  allowed. `LoadConnections`/`ReloadConnections` are root-only in that policy file.
* Stability: upstream developer page labels the D-Bus API and libnm references as
  "STABLE" and states that development snapshots (odd minor) "don't guarantee a stable
  API". `https://networkmanager.dev/docs/developers/`. `nm-dbus-types` says NM
  capabilities "are part of stable API and a certain capability number is guaranteed not
  to change". `https://networkmanager.dev/docs/api/1.52.0/nm-dbus-types.html`
* Every method/property carries a `Since` annotation in the docs (e.g.
  `CheckpointAdjustRollbackTimeout` "Since 1.12", `Settings.Connection.Filename` "Since
  1.12", `Update2` `version-id` arg "Since 1.38").
* Key write paths: `Settings.AddConnection2(settings, flags, args)` with flags `0x1
  to-disk`, `0x2 in-memory`, `0x20 block-autoconnect`;
  `Settings.Connection.Update2(settings, flags, args)` with `to-disk`, `in-memory`,
  `in-memory-detached`, `in-memory-only`, `volatile`, `block-autoconnect`, `no-reapply`,
  and `args["version-id"]` ("the update request is rejected if the profile's version-id
  does not match. This can be used to catch concurrent modifications");
  `Device.Reapply(connection, version_id, flags)` "Attempts to update the configuration of
  a device without deactivating it", flag `0x1 preserve-external-ip` (since 1.42);
  `Manager.Reload(flags)` (0x0 all, 0x1 NetworkManager.conf, 0x2 DNS rc, 0x4 DNS
  plugin) — "Note that this does not include connections, which can be reloaded via
  Setting's ReloadConnections."
  `https://networkmanager.dev/docs/api/1.52.0/gdbus-org.freedesktop.NetworkManager.html`,
  `https://networkmanager.dev/docs/api/1.52.0/gdbus-org.freedesktop.NetworkManager.Settings.html`,
  `https://networkmanager.dev/docs/api/1.52.0/gdbus-org.freedesktop.NetworkManager.Settings.Connection.html`,
  `https://networkmanager.dev/docs/api/latest/gdbus-org.freedesktop.NetworkManager.Device.html`
* Signals/state: `Manager.StateChanged`, `DeviceAdded/Removed`, `PropertiesChanged`
  everywhere; the tree implements `org.freedesktop.DBus.ObjectManager` (allowed to any
  caller in the policy file), so a client can mirror the whole state.

### systemd-networkd

* Bus name `org.freedesktop.network1` (`org.freedesktop.network1.Manager` at
  `/org/freedesktop/network1`, per-link `org.freedesktop.network1.Link`, per-`.network`
  `org.freedesktop.network1.Network`, plus `DHCPServer`, `DHCPv4Client`, `DHCPv6Client`).
  Manager methods (v257): `ListLinks`, `GetLinkByName`, `GetLinkByIndex`,
  `SetLinkNTP/DNS/DNSEx/Domains/DefaultRoute/LLMNR/MulticastDNS/DNSOverTLS/DNSSEC/
  DNSSECNegativeTrustAnchors`, `RevertLinkNTP`, `RevertLinkDNS`, `RenewLink`,
  `ForceRenewLink`, `ReconfigureLink`, `Reload`, `DescribeLink(out json)`,
  `Describe(out json)`. Manager properties: `OperationalState`, `CarrierState`,
  `AddressState`, `IPv4AddressState`, `IPv6AddressState`, `OnlineState`, `NamespaceId`,
  `NamespaceNSID`. Link methods mirror the per-link subset (`Reconfigure`, `Renew`,
  `Describe`, `Set*`/`Revert*` for resolved-style DNS/NTP). `Network` objects expose only
  read-only `Description`, `SourcePath`, `Match*` properties.
  `https://www.freedesktop.org/software/systemd/man/257/org.freedesktop.network1.html`
  (also `https://raw.githubusercontent.com/systemd/systemd/v257/man/org.freedesktop.network1.xml`)
* **There is no D-Bus method to create, edit or delete a `.network`/`.netdev`; there is
  no checkpoint/rollback method.** Configuration is file-based; the API is "reload and
  reconfigure" plus JSON introspection (`Describe`). Address/route state per link is only
  available through `Describe`/`DescribeLink` JSON (or `networkctl status --json`), not
  as typed D-Bus properties.
* Stability: systemd's Interface Portability and Stability Promise: "The D-Bus interfaces
  of the main service daemon and other daemons. We try to always preserve backwards
  compatibility, and intentional breakage is never introduced. … the API (parameter
  counts and types) is never changed, and existing attributes and methods will not be
  removed." The "Network & Netdev file format" is listed as stable ("yes") in the table.
  `https://raw.githubusercontent.com/systemd/systemd/v257/docs/PORTABILITY_AND_STABILITY.md`
  (rendered: `https://systemd.io/PORTABILITY_AND_STABILITY/`)
* Cockpit maintainer's assessment (2022, cockpit issue #17040): "networkd is a much harder
  API to work with (no notifications, no D-Bus interface, no machine readable
  configuration)"; a later commenter notes the D-Bus API now exists.
  `https://github.com/cockpit-project/cockpit/issues/17040`

## 3. Timed checkpoint / rollback

### NetworkManager: native

* `Manager.CheckpointCreate(ao devices, u rollback_timeout, u flags) -> o checkpoint`:
  "Create a checkpoint of the current networking configuration for given interfaces. If
  rollback_timeout is not zero, a rollback is automatically performed after the given
  timeout." Empty `devices` = all devices; timeout 0 = infinite.
* `CheckpointRollback(o) -> a{su}` (per-device `NMRollbackResult`: `OK`, `ERR_NO_DEVICE`,
  `ERR_DEVICE_UNMANAGED`, `ERR_FAILED`); `CheckpointDestroy(o)`;
  `CheckpointAdjustRollbackTimeout(o, u add_timeout)` (Since 1.12; "Set to 0 to disable
  the timeout"). `Manager.Checkpoints` property lists active ones; each
  `org.freedesktop.NetworkManager.Checkpoint` object exposes `Devices`, `Created`
  (CLOCK_BOOTTIME ms), `RollbackTimeout`.
* Flags (`NMCheckpointCreateFlags`): `DESTROY_ALL 0x01`, `DELETE_NEW_CONNECTIONS 0x02`
  ("upon rollback, delete any new connection added after the checkpoint"),
  `DISCONNECT_NEW_DEVICES 0x04`, `ALLOW_OVERLAPPING 0x08`,
  `NO_PRESERVE_EXTERNAL_PORTS 0x10`, `TRACK_INTERNAL_GLOBAL_DNS 0x20`.
* Introduced in NM 1.4 (NEWS: "Added ability to create a configuration checkpoints and
  rolling back changes after a timeout."); 1.48: "Properly restore in-memory connection
  profiles during the rollback of a checkpoint."
  `https://networkmanager.dev/docs/api/1.52.0/gdbus-org.freedesktop.NetworkManager.html`,
  `https://networkmanager.dev/docs/api/1.52.0/gdbus-org.freedesktop.NetworkManager.Checkpoint.html`,
  `https://networkmanager.dev/docs/api/latest/nm-dbus-types.html`,
  `https://gitlab.freedesktop.org/NetworkManager/NetworkManager/-/raw/1.52.1/NEWS`
* Cockpit uses exactly this: `with_checkpoint()` creates a checkpoint with
  `rollback_time = 7.0` s, applies the change, waits `settle_time = 1.0` s, then
  `CheckpointDestroy`; if the destroy fails (rollback already happened) it shows a
  "breaking change" dialog. Checkpoints are skipped for operations that add/remove
  devices (`hack_does_add_or_remove`).
  `https://raw.githubusercontent.com/cockpit-project/cockpit/main/pkg/networkmanager/interfaces.js`

### systemd-networkd: none built in

* No checkpoint concept in `org.freedesktop.network1`, `networkctl(1)` or
  `systemd.network(5)`. An equivalent must be built by the control plane: keep the previous
  set of `/etc/systemd/network/*` (or write the trial config to `/run/systemd/network/`,
  which takes precedence over `/usr/lib` but is overridden by `/etc`), `networkctl reload`,
  arm a timer (e.g. a transient `systemd-run --on-active=` unit) that restores the files
  and reloads again unless cancelled. Caveats from `networkctl(1)` `reload`: "if the
  corresponding interface already exists, then some of new settings may not be applied.
  E.g., VLAN ID cannot be changed after the interface was created … even if a .netdev file
  is removed, systemd-networkd does not remove the existing netdev corresponding to the
  file. If a new, modified, or removed .network file is found, then all interfaces that
  matched the file are reconfigured." And `systemd-networkd.service(8)`: "when
  configuration is updated and systemd-networkd is restarted, netdev interfaces for which
  configuration was removed will not be dropped, and may need to be cleaned up manually."
  So a file-level rollback does not by itself undo netdev creation; the control plane has
  to `ip link del` (or equivalent) what it created.
  `https://www.freedesktop.org/software/systemd/man/257/networkctl.html`,
  `https://www.freedesktop.org/software/systemd/man/257/systemd-networkd.service.html`

## 4. Coverage: Ethernet, bridge, VLAN, Wi-Fi/AP

### NetworkManager (1.52)

* Setting types include `802-3-ethernet`, `bridge` (+ `bridge-port`, incl. `stp`,
  many bridge options), `vlan` (`id` 0-4094, `flags`, priority maps), `bond`, `wireguard`,
  `802-11-wireless` with `mode` "one of infrastructure, mesh, adhoc or ap"; `ipv4.method`
  `auto|manual|disabled|shared|link-local` ("shared" = NM-spawned dnsmasq DHCP + NAT).
  `NMDeviceType` covers Ethernet, WiFi, Bond, Bridge, VLAN, WireGuard, Loopback, etc.
  Ports attach via `connection.controller` (formerly `master`) = "Interface name of the
  controller device or UUID of the controller connection".
  `https://networkmanager.dev/docs/api/1.52.0/nm-settings-nmcli.html`,
  `https://networkmanager.dev/docs/api/1.52.0/nm-settings-dbus.html`,
  `https://networkmanager.dev/docs/api/latest/nm-dbus-types.html`
* Wi-Fi station and AP mode are driven by NM through `wpa_supplicant` (Debian
  `network-manager` Recommends `wpasupplicant`, `wireless-regdb`); AP mode is a first-class
  profile (`wifi.mode=ap`), no hostapd needed.
* Software devices (bridge/vlan/bond) are created and deleted by NM from profiles
  (`Device.Delete`: "Deletes a software device from NetworkManager and removes the
  interface from the system").

### systemd-networkd (257)

* `.netdev` `Kind=` includes `bridge`, `vlan`, `bond`, `vxlan`, `wireguard`, `macvlan`,
  `veth`, `dummy`, `vrf`, `tun`/`tap`, tunnels (`gre`, `ipip`, `sit`, `vti`, ...), and
  `wlan` ("A virtual wireless network (WLAN) interface", `[WLAN] PhysicalDevice=`,
  `Type=` one of `ad-hoc, station, ap, ap-vlan, wds, monitor, mesh-point, p2p-client,
  p2p-go, p2p-device, ocb, nan`). Ports join via `[Network] Bridge=` / `Bond=` /
  `VLAN=` in the port's `.network`.
  `https://www.freedesktop.org/software/systemd/man/257/systemd.netdev.html`
* Wi-Fi: networkd can create the wlan netdev and match on `[Match] WLANInterfaceType=`,
  `SSID=`, `BSSID=`, and ships examples `80-wifi-station.network.example`
  (`[Match] Type=wlan WLANInterfaceType=station SSID=*` / `DHCP=yes`) and
  `80-wifi-ap.network.example` (`WLANInterfaceType=ap` / `Address=0.0.0.0/24
  DHCPServer=yes IPMasquerade=both IPv6SendRA=yes`). **It does not speak 802.11 itself**:
  association/authentication (station) and beaconing/auth (AP) need `wpa_supplicant`,
  `iwd` or `hostapd` run separately; networkd only handles the L3 side once the interface
  exists/associates. (Examples shipped in `systemd_257.13-1~deb13u1`,
  `/usr/lib/systemd/network/80-wifi-*.network.example`;
  `https://www.freedesktop.org/software/systemd/man/257/systemd.network.html`)
* networkd has a built-in DHCP server (`DHCPServer=`), RA sender (`IPv6SendRA=`) and
  `IPMasquerade=` (systemd NEWS 248: "systemd-networkd's IPMasquerade= option now supports
  nftables as back-end", i.e. it programs NAT rules itself) — relevant to a firewall
  product because it may collide with Andaru's own nftables ruleset if enabled. Also
  `NFTSet=` can push addresses/prefixes/ifindexes into pre-existing nft sets.
  `https://raw.githubusercontent.com/systemd/systemd/v257/NEWS`,
  `https://www.freedesktop.org/software/systemd/man/257/systemd.network.html`

## 5. Interaction with Cockpit's networking page

* Cockpit's `network` module is enabled only when
  `/usr/share/dbus-1/system.d/org.freedesktop.NetworkManager.conf` exists
  (`manifest.json` `conditions: [{"path-exists": ...}]`) — i.e. only when NetworkManager
  is installed. `https://raw.githubusercontent.com/cockpit-project/cockpit/main/pkg/networkmanager/manifest.json`
* The page talks to `org.freedesktop.NetworkManager` over D-Bus
  (`cockpit.dbus("org.freedesktop.NetworkManager", { superuser: "try" })`), writes with
  `Settings.AddConnection2(..., TO_DISK | BLOCK_AUTOCONNECT)` and
  `Settings.Connection.Update`, and wraps risky changes in NM checkpoints (7 s rollback).
  Devices with `State == 10` (`NM_DEVICE_STATE_UNMANAGED`) are shown read-only in a
  separate "Unmanaged interfaces" card (`is_managed()` in `interfaces.js`,
  `network-main.jsx`).
  `https://raw.githubusercontent.com/cockpit-project/cockpit/main/pkg/networkmanager/interfaces.js`,
  `https://raw.githubusercontent.com/cockpit-project/cockpit/main/pkg/networkmanager/network-main.jsx`
* Debian packaging: `cockpit-networkmanager` `Depends: network-manager (>= 1.6)`;
  `cockpit` metapackage only Recommends it, so a networkd-based Andaru can install
  `cockpit` without pulling NM in (apt would still install Recommends by default unless
  told otherwise).
* Upstream position on networkd (cockpit issue #17040, closed 2023 as won't-fix):
  "Indeed that page talks to NetworkManager, by design. … we don't currently plan to work
  on this." "if you want to keep networkd as a backend, then there is simply no way to
  manage that through cockpit". `https://github.com/cockpit-project/cockpit/issues/17040`
* Consequence: with NM as backend, Cockpit's page will show and (unless devices are
  unmanaged) edit Andaru's interfaces — a second writer to the same profiles. With
  networkd, Cockpit's networking page is simply absent (or would need a custom
  Andaru Cockpit plugin).

## 6. Boot ordering relative to nftables

Unit files as shipped in trixie:

* `nftables.service` (nftables 1.1.3-1): `DefaultDependencies=no`,
  `Wants=network-pre.target`, `Before=network-pre.target shutdown.target`,
  `Type=oneshot`, `RemainAfterExit=yes`, `ExecStart=/usr/sbin/nft -f /etc/nftables.conf`,
  `[Install] WantedBy=sysinit.target`. Not enabled by default (see §1).
* `NetworkManager.service` (1.52.1-1): `After=network-pre.target dbus.service`,
  `Before=network.target`, `Wants=network.target`, `Type=dbus`, `BusName=org.freedesktop.NetworkManager`,
  `ExecReload=/usr/bin/busctl call … org.freedesktop.NetworkManager Reload u 0`,
  `WantedBy=multi-user.target`, `Also=NetworkManager-wait-online.service`.
  `NetworkManager-wait-online.service`: `ExecStart=/usr/bin/nm-online -s -q`,
  `Environment=NM_ONLINE_TIMEOUT=60`, `Before=network-online.target`,
  `WantedBy=network-online.target`. `nm-online -s` "waits until the point when
  NetworkManager logs 'startup complete'" (unit comment).
* `systemd-networkd.service` (257.13): `DefaultDependencies=no`,
  `After=systemd-networkd.socket systemd-udevd.service network-pre.target
  systemd-sysusers.service systemd-sysctl.service`, `Before=network.target multi-user.target
  shutdown.target initrd-switch-root.target`, `Type=notify-reload` (SIGHUP/`systemctl
  reload` supported), `User=systemd-network` with ambient caps, `BusName=org.freedesktop.network1`,
  `WantedBy=multi-user.target`, `Also=systemd-networkd.socket systemd-network-generator.service
  systemd-networkd-wait-online.service`. `systemd-networkd-wait-online.service`:
  `BindsTo=systemd-networkd.service`, `Before=network-online.target`,
  `WantedBy=network-online.target`; by default it "waits for all managed interfaces to be
  configured or failed, and for at least one to be online" (`degraded` or better), tunable
  per-interface with `[Link] RequiredForOnline=` and `--interface`/`--any`.
  `https://www.freedesktop.org/software/systemd/man/257/systemd-networkd-wait-online.service.html`
* `networking.service` (ifupdown): `After=network-pre.target …`, `Before=network.target
  … network-online.target`, `WantedBy=multi-user.target network-online.target`.
* Semantics (systemd.io/NETWORK_ONLINE): "network-pre.target is used to order services
  before any network interfaces start to be configured. Its primary purpose is for usage
  with firewall services that want to establish a firewall *before* any network interface
  is up." Firewalls should use "Before=network-pre.target and Wants=network-pre.target";
  network managers "should set After=network-pre.target, but not Wants=network-pre.target".
  `network-online.target` "actively waits until the network is 'up', where the definition
  of 'up' is defined by the network management software"; "the right 'wait' service must
  be enabled: NetworkManager-wait-online.service if NetworkManager is used …
  systemd-networkd-wait-online.service if systemd-networkd is used." `network.target` is
  passive and mainly orders shutdown. `https://systemd.io/NETWORK_ONLINE/`
* Net effect: **both** backends order themselves `After=network-pre.target`, and Debian's
  `nftables.service` is `Before=network-pre.target`, so `nftables.service` (once enabled,
  or an Andaru-specific unit copying that pattern) loads the ruleset before either
  manager brings interfaces up. Neither backend differs here. Difference: networkd's
  `DefaultDependencies=no` lets it start very early (before `sysinit.target` completes),
  NM needs `dbus.service`; both still respect `network-pre.target`.

## 7. Treatment of interfaces the backend does not own

### NetworkManager

* Unmanaged mechanisms (all documented in `NetworkManager.conf(5)` /
  `NetworkManager(8)`):
  * `[keyfile] unmanaged-devices=` device spec (`interface-name:`, `mac:`, `type:`,
    `except:` …): "A device unmanaged due to this option is strictly unmanaged and cannot
    be overruled by using the API like nmcli device set $IFNAME managed yes."
  * `[device*] managed=` sections (device-spec matched): "can be overruled at runtime via
    D-Bus" and "has higher priority than udev rules".
  * udev property `NM_UNMANAGED=1`; udev `ID_NET_MANAGED_BY` (honoured since NM 1.46:
    "If NM_UNMANAGED is set, this has no effect. Otherwise, if the attribute is set to
    anything but 'org.freedesktop.NetworkManager', the device is unmanaged.").
  * D-Bus `Device.Managed` (readwrite; not persistent across restart).
  * `[ifupdown] managed=false` (Debian default): interfaces in `/etc/network/interfaces`
    ignored.
  * `[main] no-auto-default=` stops NM creating the temporary "Wired connection N"
    profiles for otherwise unconfigured Ethernet devices (by default it creates one for
    "any Ethernet device that is managed and doesn't have a connection").
* Externally configured (managed but not activated by NM) devices: `[device]
  keep-configuration` (default yes, since 1.36): "On startup, NetworkManager tries to not
  interfere with interfaces that are already configured. It does so by generating a
  in-memory connection based on the interface current configuration. If this generated
  connection matches one of the existing persistent connections, the persistent connection
  gets activated. If there is no match, the generated connection gets activated as
  'external', which means that the connection is considered as active, but NetworkManager
  doesn't actually touch the interface." Such active connections carry
  `NM_ACTIVATION_STATE_FLAG_EXTERNAL 0x80`. `Device.Reapply` flag `preserve-external-ip`
  keeps externally added addresses/routes.
* Restart behaviour: NM restart keeps state in `/run/NetworkManager` and re-assumes
  connections rather than tearing them down (per `keep-configuration` text).
  `https://networkmanager.dev/docs/api/1.52.0/NetworkManager.conf.html`,
  `https://networkmanager.dev/docs/api/latest/NetworkManager.html`,
  `https://networkmanager.dev/docs/api/latest/gdbus-org.freedesktop.NetworkManager.Device.html`

### systemd-networkd

* Default is opt-in per interface: "systemd-networkd will manage network addresses and
  routes for any link for which it finds a .network file with an appropriate [Match]
  section … For those links, it will flush existing network addresses and routes when
  bringing up the device. Any links not matched by one of the .network files will be
  ignored." Explicit opt-out: `[Link] Unmanaged=yes` ("no attempts are made to bring up
  or configure matching links, equivalent to when there are no matching network files …
  useful for preventing later matching network files from interfering with certain
  interfaces that are fully controlled by other applications"). udev
  `ID_NET_MANAGED_BY=` set to anything but `io.systemd.Network` excludes an interface even
  if `[Match]` would hit ("declares intended ownership of the device, and permits ensuring
  that concurrent network management implementations do not compete").
* Global "foreign" state knobs in `networkd.conf(5)`: `ManageForeignRoutes=` (default
  yes: "systemd-networkd will remove routes that are not configured in .network files
  (except for routes with protocol kernel, dhcp when KeepConfiguration= is true or dhcp,
  and static when KeepConfiguration= is true or static)"),
  `ManageForeignRoutingPolicyRules=` (default yes), `ManageForeignNextHops=` (default
  yes). These apply to managed links; setting them to `no` lets other tools (e.g. a
  routing daemon or Andaru itself) add routes/rules without networkd removing them.
* Per-link `[Network] KeepConfiguration=`: `static` = "will not drop statically
  configured addresses and routes on starting up process"; `dynamic-on-stop` = DHCP/SLAAC
  addresses kept when networkd stops; `dynamic`; `yes` = both. Default: `no` (except
  initrd / network root fs).
* Stop/restart: "When systemd-networkd exits, it generally leaves existing network devices
  and configuration intact. This makes it possible to transition from the initrd and to
  restart the service without breaking connectivity." Netdevs whose `.netdev` was removed
  are not deleted (§3).
  `https://www.freedesktop.org/software/systemd/man/257/systemd-networkd.service.html`,
  `https://www.freedesktop.org/software/systemd/man/257/systemd.network.html`,
  `https://www.freedesktop.org/software/systemd/man/257/networkd.conf.html`

## 8. How a control plane writes desired state

### NetworkManager

* On-disk format: keyfile (`.nmconnection`, GLib key-file INI), directories
  `/etc/NetworkManager/system-connections/` (default `[keyfile] path`),
  `/usr/lib/NetworkManager/system-connections/`, `/run/NetworkManager/system-connections/`.
  "For security, it will ignore files that are readable or writable by any user other
  than 'root'" (mode 0600 root:root). Each section = setting name, each key = property;
  IPs written as `address1=192.0.2.1/24,192.0.2.254`. Missing `uuid` -> "NetworkManager
  creates the UUID by hashing the connection filename".
  `https://networkmanager.dev/docs/api/1.52.0/nm-settings-keyfile.html`
* Reload semantics: "Profiles from disk are never automatically reloaded"
  (`monitor-connection-files` is deprecated/no-op); after writing files call
  `Settings.LoadConnections(as filenames)` ("Array of paths to on-disk connection profiles
  in directories monitored by NetworkManager"; returns `failures`) to reload only Andaru's
  files, or `Settings.ReloadConnections()` for everything (both root-only in the D-Bus
  policy). SIGHUP / `Manager.Reload` "does not reload connections from disk". Reloaded
  profiles do not automatically re-activate on an active device; use
  `Device.Reapply` or `ActivateConnection` for that.
* Two write styles: (a) file-first + `LoadConnections`; (b) D-Bus-first
  `AddConnection2(TO_DISK)` / `Update2(TO_DISK, version-id=N)` and let NM write the
  keyfile (filename derived from `id`; `[keyfile] rename-connection` controls renaming;
  `Settings.Connection.Filename` reports the resulting path). Style (b) gives optimistic
  concurrency via `version-id` and atomic reapply; style (a) keeps files as the single
  source of truth.
* Ownership of only Andaru-written profiles can be expressed with:
  * the `user` setting: `[user]` `data` = "A dictionary of key/value pairs with user
    data. This data is ignored by NetworkManager and can be used at the users
    discretion" (e.g. `user.data` key `org.andaru.owner=1`; keyfile form
    `[user] org.andaru.owner=1`; NM itself tags initrd-generated profiles this way —
    NEWS 1.32.6 "initrd: tag generated profiles with origin in user data");
  * `Settings.Connection.Filename` (Since 1.12) to recognise profiles under an
    Andaru-owned path/prefix and skip everything else;
  * `connection.autoconnect-priority` / `connection.autoconnect=false` and
    `[main] no-auto-default=*` to keep NM from activating non-Andaru profiles or
    generating "Wired connection N";
  * `[device*] managed=`/`unmanaged-devices` for interfaces Andaru declares out of scope.
  Nothing stops root/Cockpit from editing an Andaru profile through NM; detection is by
  `Settings.Connection` `Updated`/`Removed` signals or `version-id` mismatch.
  `https://networkmanager.dev/docs/api/1.52.0/nm-settings-dbus.html`,
  `https://networkmanager.dev/docs/api/1.52.0/gdbus-org.freedesktop.NetworkManager.Settings.Connection.html`

### systemd-networkd

* On-disk format: `.link` (udev-applied), `.netdev`, `.network` INI files in
  `/etc/systemd/network/` (highest priority), `/run/systemd/network/`,
  `/usr/lib/systemd/network/`; "All configuration files are collectively sorted and
  processed in alphanumeric order, regardless of the directories in which they live.
  However, files with identical filenames replace each other." First matching `.network`
  wins ("The first (in alphanumeric order) of the network files that matches a given
  interface is applied, all later files are ignored"). Drop-ins `foo.network.d/*.conf`
  merge in. Empty file / symlink to `/dev/null` masks. Recommended numeric prefix < 70.
  `https://www.freedesktop.org/software/systemd/man/257/systemd.network.html`
* Reload semantics: write files, then `networkctl reload` (= D-Bus `Manager.Reload()` /
  `systemctl reload systemd-networkd`, `Type=notify-reload`), which creates/updates
  netdevs and reconfigures links whose `.network` changed; `networkctl reconfigure DEV`
  (`Manager.ReconfigureLink`) re-applies without reloading files. `networkctl edit
  --stdin [--runtime] [--drop-in=]` writes a file from stdin and reloads, "useful to
  'edit' configuration from scripts". Limits: some netdev properties immutable after
  creation; removed `.netdev` does not delete the interface.
  `https://www.freedesktop.org/software/systemd/man/257/networkctl.html`
* Ownership of only Andaru-written config is expressed structurally:
  * put Andaru files under a fixed prefix (e.g. `/etc/systemd/network/40-andaru-*.network`)
    or a dedicated drop-in and never touch other names; there is no per-file metadata
    field, but `[Match]`+`Description=` and the D-Bus `Network.SourcePath` /
    `DescribeLink` JSON (`NetworkFile`) tell which file currently owns a link;
  * interfaces Andaru must not touch: no matching `.network`, or an explicit
    `[Link] Unmanaged=yes` file, or udev `ID_NET_MANAGED_BY=` set to another owner;
  * because "first match wins", a foreign lower-numbered file (`10-*.network` from an
    admin or another package, e.g. `01-azure-unmanaged-sriov.network` from
    `azure-vm-utils`) silently pre-empts Andaru's file for the same interface — ownership
    is by filename ordering, not by declaration.
* No polkit/D-Bus write path exists, so the only writers are processes with root file
  access; Cockpit does not write these files.

## 9. Trade-off summary (facts as levers, no decision)

| Axis | NetworkManager 1.52 | systemd-networkd 257 |
| --- | --- | --- |
| Present on minimal trixie | No (`optional`); installing pulls dnsmasq-base, modemmanager, ppp, wpasupplicant, polkitd via Recommends unless suppressed | Yes (inside `systemd`, `important`), but not enabled |
| d-i default | Only chosen if NM already installed; else ifupdown | Never chosen by d-i |
| D-Bus API | Full CRUD on profiles + devices, ObjectManager, polkit, `Since` annotations, checkpoints | Read/introspect + Reload/Reconfigure/DNS-NTP setters; no CRUD; JSON `Describe` |
| API stability statement | "STABLE" reference; capabilities guaranteed | Interface Stability Promise: methods/attributes never removed |
| Timed rollback | Native `CheckpointCreate(timeout)` since 1.4; used by Cockpit | None; must be built from file snapshots + timer, and netdev deletion is manual |
| Ethernet/bridge/VLAN | Yes | Yes |
| Wi-Fi station / AP | Yes via wpa_supplicant, `wifi.mode=ap` | L3 only; needs external wpa_supplicant/iwd/hostapd; `Kind=wlan` netdev + `WLANInterfaceType` match exist |
| Cockpit networking page | Works (module requires NM); becomes a second writer unless devices are unmanaged | Page absent (won't-fix upstream); would need custom plugin |
| Ordering vs nftables | `After=network-pre.target`; nftables `Before=network-pre.target` | Same |
| Foreign interfaces | Managed by default; opt-out via unmanaged-devices / device.managed / udev; externals kept via keep-configuration | Ignored by default; opt-in per `.network`; `Unmanaged=yes`; foreign routes/rules removed on managed links unless `ManageForeign*=no` |
| Desired-state write | Keyfile + `LoadConnections`, or D-Bus `AddConnection2/Update2` with `version-id` | Files + `networkctl reload`; `edit --stdin` |
| Ownership tagging | `[user]` data, `Filename`, autoconnect knobs | Filename prefix/ordering only; no metadata field |
| Extra daemons/side effects | dnsmasq (shared mode), connectivity checks, resolv.conf management (`rc-manager`), default-route selection | Built-in DHCP server, RA, `IPMasquerade` (programs its own NAT rules, nftables backend), `NFTSet=` |
