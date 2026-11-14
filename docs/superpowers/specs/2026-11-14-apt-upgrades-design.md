# Conventional Debian upgrades and explicit recovery

Status: approved by the owner on 2026-11-14. Slice 2 of issue #13.

## Intent and owner decisions

Andaru remains a conventional Debian package. It may report available updates, but neither downloads packages nor installs them automatically. An explicit Update action runs APT. Ordinary APT command-line administration remains supported.

The owner selected the simple scope after the builder check: recovery preparation is a separate explicit operation, not automatically wrapped around every upgrade. Do not build a custom APT transaction manager, deployment selector, stable recovery supervisor or automatic rollback system.

This supersedes the previous automatic-download and mandatory automatic-deployment-fallback directions for this Debian implementation. Failed installation is reported as failure, not as a successful rollback.

### Approaches considered

1. **Conventional APT, selected:** explicit installation, separate recovery preparation, normal package lifecycle and a manual repair procedure.
2. **APT with an orchestrated preparation/trial transaction:** writer fencing through installation, consent-bound package plans and automatic acceptance checks. Rejected in the builder-check discussion as more machinery than this slice needs.
3. **Side-by-side deployments or whole-system boot environments:** stronger automatic fallback, but require a different software/state/dependency boundary. Future investigation, not this slice. AppImage alone does not supply it.

Evidence: [Debian upgrade recovery research](../../research/debian-upgrade-recovery.md).

## Scope and non-goals

This slice adds a root-local availability check, an explicit recovery-preparation command and a qualified manual recovery runbook. It updates the installation/upgrade documentation and release compatibility notes.

It does not implement a web UI, authentication/RBAC, notifications framework, automatic package downloads, automatic installs, Debian security updates, portable backup ZIP/age format, extensions or users stores, or an APT transaction wrapper.

When the web UI exists, its Update action invokes ordinary APT through its authorized privileged-job boundary. This slice introduces no placeholder HTTP endpoint or shell-execution API. There is no separate Andaru installer replacing APT.

## Existing implementation

- The package installs `/usr/bin/andaru`, units and service integration; debhelper restarts the local engine on upgrade.
- The engine owns `configuration/config.db` and `configuration/secrets.db` in `/var/lib/andaru`. SQLite uses WAL. `engine.lock` excludes a second engine. `config.Open` may add missing tables; it is not a read-only compatibility probe.
- Realizer operations have a separate generation lock. Lease and prefix jobs also write state. Stopping the engine alone does not freeze all persistent state.
- Connectivity applies have independent rollback timers. Preparation must refuse a pending/activating/rolling-back apply, not disable its safety timer.
- Normal engine restarts do not reapply the network or flush the firewall. This boundary must remain intact.

## Availability and installation

Proposed root-local command:

```sh
sudo andaru upgrade check
```

It explicitly refreshes APT metadata, then reports the installed version, candidate version, repository/suite and release-information link. Metadata retrieval is not downloading an update package. A failed refresh is reported; stale indexes are not silently called current.

No new background timer is introduced. A later notification surface may read cached APT metadata, showing its freshness, without downloading packages.

The administrator installs normally:

```sh
sudo apt-get install andaru
```

APT presents its package/dependency transaction and controls confirmation, signature validation, locks and exit status. Do not add a second approval/plan-binding protocol around APT. Do not use `-y` on the administrator's behalf, bypass signatures, automatically reboot, or silently include a distribution-wide upgrade.

Existing `apt upgrade`, exact-version installs and `dpkg -i` remain ordinary supported package operations. None depends on running Andaru preparation first. They do not automatically create a recovery snapshot.

## Explicit preparation

```sh
sudo andaru upgrade prepare
```

This command preserves the current state and, when obtainable through trusted local artifacts or configured signed APT indexes, the exact currently installed `andaru` package. Downloading that recovery artifact occurs only because the administrator explicitly invoked preparation. It does not download the candidate update.

Use `/var/lib/andaru-upgrades/`, root-owned mode 0700, with files mode 0600. A recovery set contains:

- source package version, architecture, timestamp and installed dependency inventory;
- the exact previous Andaru `.deb` and its verified identity/checksum, if available;
- a consistent snapshot of `/var/lib/andaru`, including the paired configuration/secrets databases, generation files/pointers, prefix and next-hop state;
- enough metadata to identify the preserved state and the Andaru unit states that were temporarily paused.

These are local, secret-bearing internal recovery copies, not portable backups. Never expose them over the API, include them in a debug export, or upload them. The later ZIP/age format remains separate.

### Snapshot boundary

Preparation must not run concurrently with package installation. Check package-manager activity and inconsistent dpkg state and refuse clearly; do not delete locks or kill another package operation. This is a safety check, not a guarantee against an administrator concurrently starting another privileged operation.

Before pausing services, estimate the state/artifact copy space and require a filesystem reserve sufficient for ordinary state writes. Refuse insufficient space rather than deliberately filling the appliance's state filesystem. On any copy failure, remove only this operation's incomplete staging, restore paused units and preserve the previous completed recovery set.

Check for pending network applies, pause the engine and Andaru-owned lease/prefix activators, drain active realizer jobs, acquire the existing engine-state and generation locks with bounded waits, and recheck pending state while excluded from writers. Do not stop networkd, resolved, Kea, Unbound or SSH, change forwarding/filter rules, or disable a pending apply's recovery timer. If any safe exclusion step fails, abort and restore only the Andaru units that preparation paused.

With all state writers excluded, take a cold copy that includes SQLite WAL files where present. Do not copy a live `.db` alone or open the source through migration-capable `config.Open`. Verify the copied databases and their paired saved-version/hash invariants using a read-only, non-migrating path. Preserve the source unchanged.

Publish the recovery set atomically after verification and fsync, then restore only previously running activators and engine units. Never enable/start an administrator-disabled unit. A preparation failure must leave the last complete recovery set intact. A durable preparation marker outside `/run` records which units were paused so a later invocation can diagnose an interrupted preparation; no automatic boot-recovery daemon is added.

Preparation stops control-plane writes only while copying. It does not hold writers, package locks or network applies fenced until a later APT command. New accepted changes after the snapshot timestamp are not in that snapshot. Display that limit and advise preparing immediately before updating.

### Missing old package

The signed repository keeps limited versions, and an RC can disappear from its indexes after a stable release. Do not assume the previous `.deb` can always be downloaded.

If the exact old package cannot be verified and retained, state preservation may still complete, but the result must say: “State saved; previous package not retained; package recovery requires a separately verified package.” Mark the recovery set accordingly. Do not label it complete software recovery or block ordinary APT installation. Do not substitute another version or fetch an arbitrary untrusted URL.

Keep one previous completed recovery set. A replacement is pruned only after a new verified set is published; incomplete staging is never mistaken for a usable set. A state-only result must not evict an existing set containing both software and state. Prune only workflow-owned sets under the allowlisted directory.

## Package behavior, compatibility and failures

Maintainer scripts remain normal idempotent Debian scripts. They do not recursively run APT, perform network applies, restore databases on their own, or turn the preparation feature into a mandatory installation hook.

Before candidate startup performs a migration, it must reject explicitly unsupported store formats with an actionable error. Preserve existing schema checks; do not introduce a universal migration framework or new release compatibility-manifest format. Missing tables currently have additive migrations. Release notes must identify any incompatible storage change and the state/package pairing required for repair.

Preparation is the protection against losing pre-migration data; an older binary is not promised to read newer state. Snapshot integrity does not prove a future candidate is compatible.

An APT failure may leave the package half-configured and its new files installed. Say so in the runbook. Use existing engine and service status/journal commands to distinguish package configuration failure from a successfully installed but unhealthy service. Do not build a new production health-check framework in this slice.

After installation, the operator checks the existing engine control socket, saved configuration and generation status, expected DNS/DHCP services and management access. The package must preserve its established no-networkd-restart/no-ruleset-flush boundary. Local checks cannot prove remote reachability from every client.

## Manual repair

Publish an executed root-console/SSH runbook, not an automatic fallback service:

1. Inspect APT/dpkg status and recorded output. Repair interrupted configuration with the normal package tools when appropriate, after the original package transaction has exited.
2. If selecting the old version, identify the exact retained package and matching state, and warn that restoring state discards changes accepted after its timestamp. Retain the failed state separately.
3. Stop Andaru state writers and inhibit the engine, path/timer activators and realization workers from starting while installing the old package and restoring state. Preserve administrator masks and the original service state. Do not use an uncontrolled global service-start policy override.
4. Install the exact old package through normal package tooling, subject to dependency compatibility. Restore its compatible state before any old engine or background job opens the new databases. Restoring only one of the configuration/secrets databases is forbidden.
5. Remove only the temporary inhibition introduced by this procedure, start the previously intended units, inspect services/network ownership and test management access.

Concrete service-inhibition commands must be qualified on both Debian baselines before documentation claims they work. They must not re-run netplan's generator after restoring baseline files or leave an administrator's own masks removed.

The retained Andaru `.deb` plus state is not a complete offline deployment. Dependencies may require separately retained verified packages or Internet access. This slice promises neither arbitrary Debian downgrades nor full offline repair after dependencies are changed. Report an unavailable/incompatible dependency instead of bypassing APT.

### First upgrade from rc2

Rc2 has neither new command. Do not require it to execute a command it lacks or download an unsigned preparation script. Document a manual cold-copy procedure using the same writer-exclusion and paired-state principles, retain the verified rc2 artifact while it is still available, then run ordinary APT. Qualify this bootstrap procedure in the VM lab before describing the first transition as prepared.

## Qualification

Focused tests cover metadata-only checking, absent/stale indexes, no candidate downloads during checks, normal direct APT remaining usable, pending-apply refusal, writer exclusion, coherent SQLite/WAL pairs, preserved disabled units, incomplete snapshot publication, pruning only after success, missing old-package status and safe path handling.

On netplan and ifupdown Debian 13 VMs, qualify:

- preparation while configuration and lease/prefix writes occur, with no link or ruleset changes;
- normal upgrade and direct APT installation without preparation;
- failed package configuration and a candidate unable to open its state, reported without an automatic-rollback claim;
- manual reinstall with every old state writer inhibited until the matching state is restored;
- the rc2 bootstrap and preparation interrupted before completion.

Existing CI/namespace/VM suites remain the broad network correctness checks. Do not run exhaustive topology tests or require Internet reachability to decide whether a local engine started.

## Author's proposals for approval

- Root-local CLI and documentation now; actual web Update wiring belongs with the authenticated UI.
- Explicit metadata refresh, no new background checker.
- Preparation is optional and separate from APT; state accepted afterward is outside its snapshot.
- A missing previous package is reported, not silently substituted or made a blocker for normal APT.
- One local secret-bearing recovery set, no automatic rollback or complete dependency/offline-recovery guarantee.
