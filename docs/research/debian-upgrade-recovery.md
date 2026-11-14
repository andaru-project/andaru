# Debian upgrade recovery: package safety versus deployment rollback

Research for issue #13. This is a comparison, not an approved implementation design.

## Current owner direction

The discussion supersedes the earlier automatic-download direction: neither downloading nor installation is automatic. Andaru may notify the administrator that an update exists. The explicit Update action invokes APT; normal command-line APT administration remains supported. Automatic recovery is desirable only if it is feasible without disproportionate machinery. Earlier side-by-side deployment requirements must not be treated as approval of that architecture in this renewed discussion.

## The essential distinction

A package transaction failing during unpacking is not the same as a completed installation whose service is unhealthy. Debian defines error-unwind calls during unpacking, but a failed configuration script may leave a package half-configured. Those hooks are not an application-health rollback protocol. Reinstalling an older package also does not restore the application databases that a newer version migrated. [Debian Policy, sections 6.5–6.7](https://www.debian.org/doc/debian-policy/ch-maintainerscripts.html).

APT supports selecting an exact package version, including an older version, but cautions that downgrading must be used carefully. Debian Reference describes downgrading as emergency recovery rather than a generally supported routine. Retained packages, dependency compatibility and matching application state remain necessary. [apt-get(8)](https://manpages.debian.org/trixie/apt/apt-get.8.en.html), [Debian Reference](https://www.debian.org/doc/manuals/debian-reference/ch02.en.html).

## What existing systems do

### Debian services: prevention and supervision, not transparent old-version recovery

The Debian 13 host inspected for this research has OpenSSH `1:10.0p1-7+deb13u4`. Its `ssh.service` uses `ExecStartPre=/usr/sbin/sshd -t`, validates configuration before reload, uses `KillMode=process`, and requests restart on failure. Its generated postinst restarts the service through `deb-systemd-invoke`. These are concrete safeguards around starting/reloading and preserving session processes; they do not select a previous OpenSSH package and restore application state after a runtime failure. The inspection is local source evidence, not a performed SSH upgrade. [Debian OpenSSH sources](https://sources.debian.org/src/openssh/), [sshd(8)](https://manpages.debian.org/trixie/openssh-server/sshd.8.en.html).

### Firewall appliances: stronger rollback comes from a stronger storage boundary

- **pfSense Plus 24.03 and later, on ZFS:** builds the upgrade in a new boot environment, checks it, activates it, and monitors reboot. Detected boot problems can automatically select the last known-good boot environment and reboot. The official overview does not enumerate all health checks, so this is not evidence of universal detection of broken routing or management. Traditional pfSense CE/UFS upgrades do not have that same mechanism. [Netgate upgrade process](https://docs.netgate.com/pfsense/en/latest/install/upgrade-guide-overview.html).
- **OPNsense:** documented snapshot recovery requires ZFS and involves activating a previous environment through the UI or boot menu. Do not label that an automatic application-health fallback without additional evidence. [OPNsense snapshots](https://docs.opnsense.org/manual/snapshots.html).
- **OpenWrt:** ordinary sysupgrade replaces the installation and preserves selected settings, not an entire prior working installation. Generic failsafe is a manual repair mode, not rollback from a broken kernel; dual-image recovery depends on hardware. [OpenWrt sysupgrade](https://openwrt.org/docs/techref/sysupgrade), [failsafe](https://openwrt.org/docs/guide-user/troubleshooting/failsafe_and_factory_reset).

### AppImage and alternatives do not supply the missing transaction

AppImage's updater can write a new application file beside the untouched old one. That preserves a binary, not the matching database, systemd units, AppArmor policy, service dependencies or a health-triggered rollback. It does not remove the need for an Andaru recovery design. [AppImage updates](https://docs.appimage.org/packaging-guide/optional/updates.html).

`update-alternatives` manages selection through symlinks and priorities. It does not provide health checks, state snapshots, dependency rollback or interrupted-upgrade recovery. A project-specific active-deployment symlink could be an implementation detail, but using alternatives does not itself solve recovery. [update-alternatives(1)](https://manpages.debian.org/trixie/dpkg/update-alternatives.1.en.html).

## Three concrete options for Andaru

### A. Remain a conventional APT package

Notify, then run APT only on explicit action. Make upgrades conservative: perform available compatibility/configuration checks, avoid unnecessarily disturbing network realization, preserve a consistent pre-upgrade state backup, and retain verified old package artifacts outside the disposable APT cache where feasible. Offer a documented explicit recovery procedure using the old package plus matching state.

This offers safer upgrades and manual recovery, **not guaranteed automatic rollback**. Database downgrade compatibility and any changed dependency must be evaluated per release. Do not promise that a saved `.deb` alone restores a working firewall.

### B. Use APT as delivery, with deliberate side-by-side deployment activation

An illustrative layout is:

```text
stable supervisor/recovery entry point
  active deployment: executable + matching state + runtime integration
  previous deployment: executable + matching state + runtime integration
```

APT still downloads and installs the candidate after administrator action. A separate supervisor activates it and can select the previous complete deployment if the candidate fails. Recovery cannot depend on the candidate binary or invoke another package transaction while the first transaction still holds package locks. A plain version-selecting symlink is insufficient.

This is custom deployment machinery, not a small maintainer-script trick. Shared Kea/Unbound/systemd dependencies, migration copies, interrupted activation, offline artifacts and the distinction between installed package version and active runtime version all require design. Keeping only the old Go binary would justify only a narrow engine-startup fallback claim.

### C. Whole-system boot environments or A/B

Retain a previous bootable root and switch deployments at boot, with success/failure signaling. This can preserve units and dependencies along with software. It requires a deliberate storage/boot design and a policy for shared `/var` state; a root snapshot that excludes application databases is insufficient. This is a plausible future appliance/image direction, not an inexpensive addition to installing Andaru on arbitrary Debian hosts.

## Recommendation for discussion

Prefer A for the current Debian package, unless automatic recovery is important enough to justify B as an explicit project. Keep C for an appliance/image installation option. AppImage alone is not a reason to choose B and does not make it easy.

No product code, dependencies, package scripts, host networking or installed services were changed for this research. No upgrade or recovery mechanism was executed; claims of behavior above come from the cited primary documentation and identified local source inspection.
