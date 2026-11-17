# Wipeable state and factory-reset boot

Status: approved by the owner on 2026-11-17, as the playback in that conversation. This amends the approved [LAN-recoverable factory-reset design](https://github.com/andaru-project/andaru/pull/59) (`docs/superpowers/specs/2026-11-15-factory-reset-design.md`, not on `main`). Where the two documents disagree, this one wins. A later merge of that file must not restore a superseded rule.

This document does not implement factory reset. It records where Andaru data lives, what a wipe deletes, and which kept copy boots afterwards. Shipping a reset command still waits for the prerequisites named in the 2026-11-15 design, except where this document changes the recovery contract those prerequisites must satisfy.

## Intent

All Andaru data, including add-on settings, lives in one directory. An operator may put that directory on its own partition. Wiping it must not destroy the copy Andaru needs in order to boot. Reset clears Andaru state. The exception is network configuration, and only within the limits below: the post-reset network settings the operator stored, or, if those are missing or do not come up, the last LAN setup on which an administrator could reach the box and change a setting.

The operator also wants the very first successful setup kept, as a record. It is not a third automatic boot. Old accounts are not part of the boot.

## Owner decisions

From the 2026-11-17 conversation:

- There is one directory for Andaru-related data, including add-ons and their settings. An operator may place it on a separate partition.
- Wiping that directory boots the recovery configuration the operator defined.
- Andaru also stores the first setup and the last successful configuration. Success means an administrator could reach the box and change a setting while that configuration was live.
- If the operator has stored the settings they want after a reset, Andaru applies those. If they have not, or that apply is not successful, Andaru reverts the LAN side to the last successful LAN setup.
- Plugin upgrades are add-on data. They are wiped with the rest of that directory. Bundled plugin programs are not stored there.
- Reset clears everything except that network configuration. Old Andaru accounts are not restored.
- The administrator password is the one the operator enters during the reset. If they enter none, the password goes back to `admin` / `admin`.

Earlier rulings that this conversation did not reopen remain binding. They are listed under "Still binding" below.

## Calls

These are the author's calls. The owner accepted the playback that contained them.

- `/var/lib/andaru` is the wipeable tree and the mount point when the operator uses a separate partition. Andaru does not create or format that partition. Reset deletes the tree's contents. It does not run `mkfs`.
- The filesystem at that path must be local. SQLite cannot live on NFS.
- The three kept copies live on the root disk, at `/var/lib/andaru-kept/`, never inside the wipeable tree. A formatted or missing data partition must still boot.
- `recovery/` is the post-reset network settings the operator stored, plus the secrets required to realize that network. It does not hold accounts or credential verifiers. Storing it is an explicit action. A normal save does not update it.
- `first/` is written once, at the first success signal, and never overwritten. It records that setup's network configuration. It does not store credential verifiers. Reset does not apply it and does not read it for the password.
- `last-lan/` is only the LAN and management path from the last successful live configuration. It is not a copy of the whole configuration.
- Reset applies `recovery/` when it exists. If it is missing, or that apply does not come up, Andaru applies `last-lan/` immediately. It does not wait for a person to declare the reset a failure.
- On the LAN fallback, WAN and add-ons stay down. Old identity stays down. The new administrator account is still created. The way back in is that account, and SSH from the management LAN using the Debian SSH account that reset does not change.
- The success signal updates `last-lan/` only. It does not overwrite `first/` or `recovery/`.
- `/var/lib/andaru-upgrades` stays on the root disk. Factory reset does not delete it, and restoring one of those copies is not how reset works. That tree remains the package-upgrade repair copy from the approved upgrade design.
- A bundled plugin's program files ship in the Andaru `.deb` under `/usr`. Reset does not uninstall them. Their settings live under `/var/lib/andaru` and are wiped, so the plugin comes back unconfigured.
- An installable plugin, including a downloaded newer version or a copy kept for a plugin rollback, lives under `/var/lib/andaru`. Reset deletes the plugin and that upgrade copy together.
- This design does not build add-ons, a partition migrator, or a reset command.
- An uplink is never written into `last-lan/` and never listens after reset. The product already treats a static IPv4 gateway, DHCPv4, or PPPoE as an uplink. That existing inference is an exclusion, not a new trusted-LAN flag. If the live management segment is an uplink, the success signal does not update `last-lan/` from it. A reset with no non-uplink `last-lan/` requires the console.
- The sole post-reset Andaru account is named `admin`. Reset deletes every previous account. It does not copy verifiers from `recovery/` or `first/`.
- If the operator enters a password during reset, that is the `admin` password. If they enter none, the password is `admin`. There is no further forced password-change step. The entered password is not a flag, an environment variable, or a log field.

## What this supersedes

In the 2026-11-15 factory-reset design, the following no longer hold:

- Recovery order is not "previous live management, then an ordered setup allowlist, then console re-election before reset may start." The order is `recovery/`, then `last-lan/`.
- The automatic failure path is not a rollback to the pre-reset tree. After reset starts, the pre-reset tree is not a product fallback.
- Waiting for an authenticated confirmation before choosing the LAN fallback no longer holds. The LAN fallback runs as soon as `recovery/` is missing or its apply does not come up.
- Deleting `/var/lib/andaru-upgrades` after reset commitment no longer holds.
- The ordered recovery-LAN allowlist is not the automatic fallback. `last-lan/` is the recorded last successful LAN setup, not a list the operator maintains separately.
- When `recovery/` exists, it is the post-reset network, not a temporary profile that exists only until `andaru setup` finishes, and not a restore of accounts. Setup remains the path only when `recovery/` was never stored and `last-lan/` cannot be realized. Reset itself creates the `admin` account. That is not a second setup workflow.
- Factory reset does not return previous Andaru accounts. The 2026-11-15 rule "no built-in `admin`/`admin`" does not hold for this reset. The account is the one named below.

## Still binding

From issue #13, issue #14, and the 2026-11-15 design, where they do not conflict with the sections above:

- Reset affects Andaru only. Debian, the installed core package, the keyring, unrelated packages and unrelated host configuration remain.
- Reset is available from an administrator-only GUI and from root-local `andaru ctl factory-reset`, once those clients exist. GUI initiation still requires the administrator permission, fresh password reauthentication, a typed confirmation, a backup offer and an audit event before destructive work. Console initiation is UID 0 only, requires typed confirmation, and has no `--yes`, password flag or network bypass.
- The no-default-credential rule does not apply to factory reset. Other lockout recovery still has no default password.
- WAN and unclassified interfaces never listen for recovery. Do not infer a LAN from an interface name, a zone name, DHCP, the absence of a default route, or "not currently WAN."
- Default-deny protects every recovery state. Andaru does not install OpenSSH or change Debian SSH accounts or keys.
- A management path may share an interface with a LAN and may depend on VLAN, bridge, bond or PPPoE. A physical parent alone is not a management path.
- Portable backup is offered before reset. Portable backup files and remote objects are not deleted. They remain a different product from `/var/lib/andaru-upgrades`.
- No reset command ships until its claims can be fulfilled end to end. Identity and audit already exist. Setup, the management-recovery substrate and the authenticated web boundary are still prerequisites for the GUI path. A root-local recording of `recovery/` may exist before the GUI does. It must not open a network endpoint.

## Goals

- One wipeable directory holds live Andaru data and, later, add-on settings and installable plugin files.
- An operator can mount a local filesystem on that directory without losing the boot copies.
- After a wipe, the box comes back on the operator's stored post-reset settings, or on the last LAN they could administer.
- The first successful setup remains readable and is never silently replaced or applied.
- A factory reset cannot destroy the package-upgrade repair copy.

## Non-goals

- Building add-ons, a plugin installer, or plugin rollback.
- Creating, formatting or migrating a data partition.
- An automatic third fallback to `first/`.
- Deleting or restoring `/var/lib/andaru-upgrades`.
- Debian security updates, unattended package installs, or a custom APT transaction.
- A network API for recording the kept copies before authentication exists.
- Physical erasure of deleted bytes from SSDs, snapshots or external backups.

## Anti-goals

- Putting `recovery/`, `first/` or `last-lan/` inside `/var/lib/andaru`, so formatting the data partition also destroys the boot copies.
- Treating a missing mount as an empty live tree and then recording that empty tree as `last-lan/` or `first/`.
- Restoring a wiped installable plugin from a copy outside the wipeable tree.
- Using a package-upgrade repair set as the factory-reset boot.
- Bringing WAN, add-ons or Andaru identity back on the LAN fallback.
- Waiting for an operator to notice that the recovery boot failed before applying `last-lan/`.

## Constraints

- Live configuration and secrets are SQLite databases under `/var/lib/andaru`. The wipeable path must stay a local filesystem.
- Generations, pointers, prefix and next-hop state, and the identity stores that exist today also live under `/var/lib/andaru`. A wipe removes them with the rest of that tree.
- Package maintainer scripts must not delete `/var/lib/andaru-kept/` or `/var/lib/andaru-upgrades/`. Purge already leaves `/var/lib/andaru` because uninstall is unsupported. That rule is unchanged.
- Kept copies contain secrets. They are root-owned, directories mode 0700, files mode 0600, and are not exposed by the API, a debug export, or a portable backup unless the operator explicitly exports them.
- The upgrade design's boundary still holds: preparing or keeping a package-upgrade copy must not restart networkd, resolved, Kea, Unbound or SSH, and must not disable connectivity rollback timers.

## Approaches considered

### 1. Three directories — selected

`/var/lib/andaru` is the wipeable data disk. `/var/lib/andaru-kept/` on the root disk holds `recovery/`, `first/` and `last-lan/`. `/var/lib/andaru-upgrades/` on the root disk remains the package-upgrade repair copy and is not part of reset.

This is the only layout that still boots after the operator formats the data partition, and that does not resurrect a wiped add-on from an upgrade cache. The owner accepted it.

### 2. Kept copies inside the wipeable tree

A `retained/` subdirectory, skipped by the wipe, is smaller and keeps one mount. It fails the operator's partition requirement: formatting or losing that partition deletes the recovery boot with the data. Rejected.

### 3. Plugin upgrades outside the wipe

Keeping installable plugin packages next to the package-upgrade repair copy would let a plugin roll back after a reset. It also puts the add-on back after the operator wiped it. The owner wants add-ons and their settings gone. Rejected.

## Layout

```text
/var/lib/andaru/                  wipeable tree; mount point for a local data partition
  configuration/                  live saved configuration and secrets, as today
  generations/                    rendered generations and pointers, as today
  ...                             identity and other Andaru stores, as they exist
  addons/                         later: installable plugin files, plugin upgrades, plugin settings

/var/lib/andaru-kept/             root disk; reset never deletes this directory
  recovery/                       explicit post-reset settings; absent until the operator stores them
  first/                          first successful setup; written once
  last-lan/                       last successful management-LAN closure

/var/lib/andaru-upgrades/         root disk; package-upgrade repair only; reset does not read or delete it
```

`addons/` is a reserved name so later plugin work does not invent a second data root. This design does not create plugin contents there.

If `/var/lib/andaru` is a mount point and the mount is absent at boot, Andaru does not create a replacement directory and use it as live state. It boots from the kept copies and reports the missing mount. It does not update `first/` or `last-lan/` from that boot.

## What each kept copy contains

`recovery/` is the network settings the operator stored for after a reset, including the secrets required to realize that network. It is not a copy of accounts, credential verifiers, or add-on state. The operator replaces it only by an explicit "store this as the post-reset network" action. If those settings include a WAN, the recovery boot may bring that WAN up, because that is the network they stored. If the apply does not come up, Andaru does not keep that attempt. It uses `last-lan/` instead.

`first/` is a record of the network configuration at the first success signal. It stores no password verifiers. No reset, boot or fallback path applies it, and the reset password does not come from it. Replacing it requires an explicit operator action that this design does not provide.

`last-lan/` is the management-path closure from the 2026-11-15 design, taken from the last successful live configuration, and only when that management segment is not an uplink:

- the elected management segment, its allowed sources, and the link ancestry required to realize it;
- the addressing on that closure;
- enough firewall state to default-deny and to allow SSH from that management segment;
- no WAN, no uplink, no add-on state, no Andaru users or credentials, no user policy, no NAT, and no DHCP or DNS service state beyond what that LAN closure itself needs in order to be reachable.

It does not include an Andaru login. On this fallback the administrator uses the Debian SSH account.

## Boot after a wipe

1. Stop Andaru writers. Delete the contents of `/var/lib/andaru`. Do not delete `/var/lib/andaru-kept/` or `/var/lib/andaru-upgrades/`.
2. If `recovery/` exists, apply that network configuration as the new live network.
3. If `recovery/` is absent, or that apply does not come up, discard that attempt as the running network and apply `last-lan/`.
4. If `last-lan/` is also absent or cannot be realized, stop and require the local console. Do not invent an address. Do not listen on WAN. Do not apply `first/`.
5. On every path, including console-required, create one Andaru account named `admin`. Its password is the one entered during reset, or `admin` if none was entered. Do not restore any previous account.

"Does not come up" means the apply does not produce a reachable management path. It does not mean the operator later failed to log in and change a setting. The success signal is only for recording `last-lan/` and `first/`. It is not the trigger that starts the fallback.

A crash before either boot is up is the builder's problem to make safe. Any short-lived checkpoint used for that crash must live outside both `/var/lib/andaru` and `/var/lib/andaru-upgrades`, and must be deleted once `recovery/` or `last-lan/` is the running configuration. It must not become a way to restore the pre-reset configuration after that boot is up.

## Success recording

Success is an authenticated administrator session changing a setting while that configuration is the live one. Apply confirmation alone is not success. A package upgrade is not success.

The first time that signal fires, Andaru writes `first/` if it does not already exist. Every time the signal fires, Andaru replaces `last-lan/` with the management-path closure of the configuration that was live for that change, unless that management segment is an uplink. An uplink session does not create or refresh `last-lan/`. A change made on the LAN fallback does not update `last-lan/` until Andaru identity is up, because the signal requires an Andaru administrator session.

Recording reads the live configuration. It does not take the package-upgrade repair copy, and it does not run during a reset apply that has not come up.

## Plugins

Bundled plugin code is part of the Andaru package. Replacing it is an Andaru package upgrade. Reset leaves `/usr` alone and wipes the plugin's settings under `/var/lib/andaru`.

Installable plugin files and plugin-upgrade copies are add-on data. They live under `/var/lib/andaru/addons/` and are deleted with that tree. Nothing outside the wipeable tree may reinstall them as part of reset.

Until add-ons exist, the only requirement is that the wipeable tree is the only legal place for their future files and settings, and that a reset of that tree removes them.

## Left to the builder

- The file format inside each kept directory, as long as the contents above can be applied or read without a second store.
- The root-local command that stores `recovery/`, and later the GUI control for the same action.
- How a crash between the wipe and a reached boot is made recoverable, within the checkpoint limits above.
- How "the apply came up" is tested for the recovery boot and for `last-lan/`, using the existing generation and reachability checks rather than a new probe framework.
- The exact pause and lock order, reusing the writer exclusion the 2026-11-15 design already requires, without adopting that design's rollback-to-old-state outcome.
- Operator documentation for mounting a local filesystem at `/var/lib/andaru` before first setup, or stopping Andaru and moving an existing tree onto that mount. No migration command.

The builder must not add a network endpoint, a default address, a use of `first/` as a boot, a reset deletion of `/var/lib/andaru-upgrades`, or a restore of previous password verifiers.
