# Authenticated LAN-recoverable factory reset

Status: approved by the owner on 2026-11-15. Implementation is blocked on the prerequisite identity/audit, setup and authenticated LAN-recovery projects described below.

## Intent

Factory reset removes Andaru-owned configuration, secrets, accounts, credentials, sessions, extensions and managed state while retaining Debian, unrelated host configuration and the installed core Andaru package. It returns the appliance to the same resumable `andaru setup` workflow as first installation.

A reset must not turn a remotely managed firewall into a console-only appliance merely because reset succeeded. Before deleting logical stores, Andaru stages an authenticated, default-deny recovery environment that first preserves the prior management path and otherwise uses only administrator-approved trusted-LAN fallback paths. WAN and unclassified interfaces are never automatic recovery listeners.

This design records the complete eventual contract. Implementation is prerequisite-first because the repository currently has only configuration/configuration-secret stores and generations. It has no `andaru setup`, user/credential stores, web authentication, audit store, GUI or extension registry. No partial reset command ships until its available stores and recovery claims can be fulfilled end to end.

## Decisions already made

Owner decisions from [issue #13](https://github.com/andaru-project/andaru/issues/13) and [issue #14](https://github.com/andaru-project/andaru/issues/14) remain binding:

- Reset affects Andaru only. Debian, the core package and unrelated Debian packages/configuration remain.
- Reset is available from an administrator-only GUI and from root-local `andaru ctl factory-reset`.
- GUI initiation requires the administrator permission, fresh password reauthentication, a typed confirmation, a backup offer and an audit event before destructive work.
- Console initiation is UID-0 only and requires typed confirmation. It has no network password bypass.
- No built-in `admin/admin` or other default credential exists. First setup records the administrator profile reset returns to, or reset accepts a different new administrator.
- Recovery first tries the prior management configuration. The owner’s current clarification supersedes “every interface”: fallback access is trusted-LAN only, never WAN or an unclassified interface.
- SSH is available only when `openssh-server` exists and setup/reset enables it. Andaru does not install, enable or change Debian SSH account credentials during reset.
- Management may share a logical interface with LAN and may depend on VLAN/bridge/bond/PPPoE topology. A physical parent alone is not a management path.
- Default-deny firewall rules protect every reset-recovery state. Nothing is reachable without authentication.

- Extension-owned state and registrations are removed; unrelated host software/data are not.
- Portable backup is offered before reset. Portable backup and local upgrade-recovery sets remain different products.

Primary owner-decision sources:

- [factory-reset and extension scope](https://github.com/andaru-project/andaru/issues/13#issuecomment-6707652073);
- [GUI and console authorization/confirmation](https://github.com/andaru-project/andaru/issues/13#issuecomment-6707769246);
- [post-reset management reachability](https://github.com/andaru-project/andaru/issues/13#issuecomment-6708268886);
- [one setup workflow and management recovery](https://github.com/andaru-project/andaru/issues/14#issuecomment-6700283805); and
- [latest pre-reset general recovery behavior](https://github.com/andaru-project/andaru/issues/14#issuecomment-6822128106), narrowed by the owner’s current LAN-only clarification.

The later console-recovery ruling keeps existing credentials for ordinary lockout recovery. Factory reset is different: it deliberately replaces Andaru users/credentials with the selected reset administrator, while Debian SSH credentials remain outside Andaru and unchanged.

## Current implementation facts

- `configuration/config.db` and `configuration/secrets.db` publish paired saved versions. Logical portable backup/restore exists.
- Generations under `/var/lib/andaru/generations` are independent of the databases so boot/rollback can work with the engine dead. A generation contains rendered root-only files, including secret material needed by services.
- Confirmed takeover state records that Andaru owns host networking and must continue suppressing competing netplan/cloud-init/ifupdown/nftables writers.
- A management declaration identifies one segment and optional allowed source-address objects. Rendering adds authenticated SSH reachability on that segment before user policy and then default-drops unmatched input/forward traffic.
- The interactive takeover assistant is not `andaru setup`. There is no current reset route, reset supervisor, user/credential store, web service or audit store.
- The package starts Andaru’s units but deliberately does not change live networking during installation.

Reset must reuse paired store publication, generation manifests, apply/rollback semantics, portable backup and trusted-path rules. It must not repurpose cold package-recovery sets, replace databases while the engine is running, or make package maintainer scripts destructive.

## Approaches considered

### 1. Prerequisite-first complete reset — selected

First build the management-recovery substrate, identity/auth/audit stores and one resumable setup workflow. Then implement one factory-reset transaction that can clear every store that exists and prove authenticated LAN recovery.

Advantages: no console-only partial product, no fake stores, no unauthenticated recovery, no later incompatible reset migration. Cost: factory reset itself waits for prerequisite work.

### 2. Current-store console reset

Reset only configuration, secrets and generations from the local console now. This is smaller, but cannot satisfy GUI reauthentication/audit, users/extensions cleanup, new administrator creation or return to setup. It was explicitly not selected.

### 3. Pull every prerequisite into one reset project

Implement setup, web auth, user stores, audit, extension ownership and reset together. This makes one very large security-sensitive change whose failures are difficult to isolate. It was not selected.

## Canonical terms

- **Reset plan:** immutable preview of scopes, backup disposition, chosen reset administrator, retained LAN paths and destructive actions, bound to current store/generation versions.
- **Management path:** elected management segment plus all required link ancestors, addressing, source policy, authentication listener and required runtime secrets.
- **Recovery LAN path:** an administrator-approved trusted LAN segment eligible only when the management path cannot be realized. It is explicit setup state, not inferred from interface names, zone names, DHCP, a default route or “not obviously WAN.”
- **Recovery profile:** purpose-bound root-only rendered network/firewall/listener state used while setup is incomplete. It is not normal saved configuration and cannot be exported as one.
- **Reset administrator:** the sole Andaru administrator recreated for post-reset setup, chosen from the first-setup reset profile or replaced during reset. Its password is newly entered; no default password exists.
- **Reset checkpoint:** complete old Andaru state retained locally only until the recovery profile is accepted. It is not a portable backup or long-term rollback deployment.
- **Reset receipt:** minimal durable, non-secret operation/audit record outside the state tree being swapped.

These terms must remain distinct. In particular, a recovery profile is temporary operational material, not a loophole that silently preserves wiped logical stores.

## Prerequisites and order

Factory reset implementation starts only after these observable contracts exist:

1. **Identity and audit core:** user store, credential store, administrator role/permission, password verification and reauthentication, session revocation, append-only audit events, and root-console administrator creation/reset. Passwords use the separately approved authentication design; this document does not invent a second credential scheme.
2. **One setup workflow:** `andaru setup` and GUI setup are two clients of one resumable engine workflow. It creates/replaces the administrator, management TLS identity, topology, management/recovery-LAN choices and initial confirmed generation.
3. **Management recovery substrate:** boot can realize a purpose-bound recovery profile independent of normal databases; console can explicitly re-elect a LAN management path. Setup persists an ordered allowlist of recovery-LAN paths. No automatic interface substitution exists outside that allowlist. This substrate adds a reset-purpose generation manifest/state epoch, reset-specific verifier, root-only secret-file modes and an expanded generation allowlist for required secret-bearing service files. The current normal apply gate refuses enabled PPPoE and its verifier protects unrelated live networking; neither is silently reused as if reset recovery already worked.
4. **Web management boundary:** authenticated HTTPS listener, authorization, CSRF/rate limiting/session policy and TLS fingerprint display. Recovery never exposes an unauthenticated setup page.
5. **Owned-state registry:** every Andaru subsystem/extension declares its state root, service units, secret scope, cleanup operation and reset ordering. Unknown/unhealthy owned state blocks reset rather than being abandoned silently.

Extensions do not block reset merely because none are installed. Once extension installation exists, shipping it requires the reset registration above in the same change.

After design approval, the next implementation project is prerequisites 1–3, split into independently reviewable plans. This document does not authorize a reset stub while those are absent.

## Recovery path selection

### Primary path: previous management

Use `Pointers.Live` and that generation’s `SavedVersion`: reset must preserve the management path actually serving the administrator. Bind and recheck `confirmed`, `lastKnownGood`, `pending` and the live generation in the reset plan; any pending apply refuses reset. A confirmed/live fallback mismatch is reported, not silently normalized. When `live` is nil, there is no primary Andaru management generation: use an explicitly approved recovery-LAN path or require console re-election. The primary candidate itself must be an approved trusted-LAN path; a management segment on WAN is not eligible merely because it is live. Never choose an unapplied saved head or a non-live confirmed generation. Build the transitive closure of:

- elected management object and allowed-source address objects;
- management segment, zone and complete link ancestry;
- static/dynamic addressing, VLAN/bridge/bond/PPPoE dependencies;
- TLS listener and reset-administrator authentication;
- only runtime secret values required to realize that closure;
- default-deny filter with management HTTPS and, when enabled, SSH.

Render this closure into a new recovery generation before logical stores are cleared. Required secrets may appear only in the root-only rendered recovery files/credential delivery produced for that generation. They are absent from the new logical configuration-secret store and are deleted when setup replaces the recovery profile. The design therefore honors both requirements: logical configuration secrets are reset, while the already-authorized management path remains usable.

The recovery profile does not retain DNS/DHCP service state, NAT, user policy, extension listeners, arbitrary rules, unrelated links or statistics merely because they were live before reset.

### Fallback: explicit trusted LAN only

If the primary path cannot be rendered or verified, try the ordered recovery-LAN paths the administrator explicitly approved during setup. Preserve each path’s required topology/addressing closure and allow authenticated HTTPS/optional SSH behind default deny.

Never infer recovery eligibility from:

- interface or zone names such as `lan`, `eth1` or `internal`;
- absence of a default route;
- DHCP versus static addressing;
- an interface being present but unassigned;
- “not currently known to be WAN.”

The current boot code derives permanent network ownership from the retained takeover baseline. Reset must not keep an entire pre-Andaru baseline merely to remember how to own the host. Before reset ships, factor a minimal **network-ownership record** from that baseline: baseline kind, takeover-confirmed state, ownership of each persistent mask/disable action, and only normalized DHCP client identities still required for Andaru-managed links. It contains no old netplan/interfaces files, addresses, rules or service secrets. The fresh state carries this record so boot keeps competing writers suppressed and preserves required DHCP identity; rollback uses the complete baseline only from `old-state` during the reset trial. After confirmation the old pre-Andaru baseline files/manifests are deleted with the checkpoint. Hand-back/uninstall is already unsupported; reset does not preserve a hidden restoration path. Missing ownership metadata is an error, never an inference that the host was netplan.

WAN/uplink and unclassified interfaces never listen for factory-reset recovery. If no approved recovery-LAN path is realizable, the reset plan is **console-required**. The local console must explicitly elect a present interface/path before reset can proceed. A GUI-initiated reset is refused until a LAN recovery path is available.

This current clarification supersedes the older general-recovery phrase “listen on every interface.” Ordinary non-reset lockout recovery also adopts the LAN-only boundary.

## Recovery addressing and authentication

Keep the exact previous management addressing where it can be reproduced. Static addresses stay exact. Dynamic paths reuse the currently rendered client identity and lease-preservation behavior; the preview warns that DHCP/RA cannot promise the same address. The reset confirmation is not accepted until a new authenticated connection reaches one displayed recovery address.

For an approved fallback LAN, setup stores the topology/addressing policy needed to realize it. Reset does not invent an address or start DHCP/SLAAC on arbitrary ports. A future setup design may define an explicitly approved fallback address, but this design has no hidden `192.168.1.1` default.

Before destructive work:

- create the reset administrator and password hash in staged new stores;
- create or select the reset HTTPS certificate and show its fingerprint;
- revoke all old Andaru sessions/tokens in the staged state;
- preserve Debian SSH users/keys unchanged; and
- include only the sources allowed by the selected management/recovery-LAN policy.

The reset administrator password never appears in arguments, environment, logs, the reset receipt or generated network files. Recovery web login receives the normal brute-force, CSRF, cookie, TLS and audit controls.

## Reset preview and confirmations

Preview is mandatory and non-destructive. It reports:

- exact Andaru stores, extension roots/units and runtime state to clear;
- software and Debian state retained;
- previous management path and ordered LAN fallback paths;
- addresses/URLs expected after reset and certificate fingerprint;
- whether SSH will be reachable and why;
- any secret-backed management material temporarily embedded in the recovery profile;
- backup status/selection;
- current saved/live generation and store preconditions;
- whether confirmation can occur remotely or requires console; and
- unrecognized state that blocks reset.

GUI flow:

1. administrator permission check;
2. fresh password reauthentication;
3. offer/download portable backup, with explicit skip or acknowledged failure;
4. choose and enter the post-reset administrator password;
5. type the exact confirmation phrase `FACTORY RESET`;
6. persist the audit intent/receipt before mutation; and
7. reconnect with the reset administrator through a displayed LAN URL to confirm the trial.

Console flow:

```text
andaru ctl factory-reset preview
andaru ctl factory-reset run
andaru ctl factory-reset status
andaru ctl factory-reset confirm ID
andaru ctl factory-reset rollback ID
```

`run` is UID-0 over the engine socket, offers a filesystem portable backup, requires hidden reset-password entry and typed `FACTORY RESET`, and prints recovery URLs/fingerprint. There is no `--yes`, password flag/environment source or network bearer bypass. Automation of destructive reset is not in scope.

Preview binds only semantic reset inputs: configuration/configuration secrets; declared users, roles and credentials; the chosen reset administrator; setup/recovery-LAN policy; extension registry and owned-resource identities; network-ownership epoch; live/confirmed/last-known-good/pending pointers; recovery plan; and backup disposition. Volatile sessions, rate-limit counters, statistics and append-only audit-tail position are not preconditions. The operation writes its required audit intent through the external reset receipt after validating the preview; that operation-owned write does not invalidate the plan. Any bound input change requires a new preview; reset never silently re-plans.

## Staged state and transactional cutover

Reset is a trial with automatic rollback, not delete-then-hope.

### Durable layout

Use a fixed root-owned allowlisted coordinator directory outside every swappable/clearable root, for example `/var/lib/andaru-reset/`:

```text
fence                 stable coordinator lock/fence honored by every mutator
operation.json        format, ID, phase, state epoch, boot ID, deadline, hashes
receipt.json          actor ID/type, timestamp, scopes, backup disposition, outcome
old-state             rename of the previous /var/lib/andaru tree
new-state             staged fresh tree until cutover
owned-checkpoints/    reversible snapshots of registered external extension state
cleanup.json          resumable post-commit deletion status
```

Exact paths are builder decisions, but all must be canonical immediate children, root-owned, mode 0700/0600, no links/hardlinks/special files. The operation journal names only allowlisted basenames, never arbitrary deletion paths. Sync every file/directory and parent before reporting phase changes. The fence inode is stable across state-tree renames and survives reboot; engine/web/setup/backup/restore/realize/extension mutation entry points check the active reset epoch and refuse normal writes while the operation owns it.

### Staging

Construct the complete new Andaru state without mutating the old tree:

- fresh configuration/configuration-secret pair with only reset/setup state;
- fresh user/credential stores with the reset administrator only;
- fresh audit store containing the reset intent;
- empty sessions/tokens/statistics/jobs;
- empty extension registry/data after owned cleanup planning;
- reset recovery generation as confirmed/live, preserving takeover ownership;
- reset profile/certificate material; and
- a durable setup-resume marker.

Version numbers may restart only if generation/store contracts support it without ambiguity. Prefer explicit new-state epoch/format identity over pretending old history continues. The reset generation and fresh configuration head must agree exactly. No old history row remains queryable after confirmed reset.

### Stable fence and writer/package exclusion

Refuse incomplete dpkg state, pending network apply, active backup/restore/reset and unknown extension operations. Before pausing a writer or renaming any state:

1. persist and sync `operation.json` in phase `planned`;
2. acquire the stable coordinator fence outside `/var/lib/andaru`; every normal mutation/startup path refuses while another reset epoch owns it;
3. acquire and hold exclusive POSIX locks on `/var/lib/dpkg/lock-frontend` and `/var/lib/dpkg/lock` for the vulnerable state-swap/recovery-activation transaction, never deleting lock files; a concurrently starting package operation either wins first and makes reset abort, or waits/fails on these locks;
4. start the package-provided reset supervisor and arm its independent rollback timer;
5. durably advance to `supervised`; only then pause engine, web, lease/prefix activators, setup jobs and all registered extension units;
6. drain active realizers and take engine/generation/user/audit/extension locks in one documented order with a bounded deadline; and
7. recheck every semantic preview precondition.

Locks inside `/var/lib/andaru` protect the old tree only; the external fence is what prevents a new mutator from opening replacement lock inodes after rename. The new recovery engine/web may start while the fence is active, but it recognizes the reset epoch and exposes only reset status/confirm/rollback, setup and required read-only status. Normal save/apply/backup/restore/extension mutation is unavailable.

Abort before the first rename restores only units this operation paused, cancels supervision/timer, releases package/fence locks and records the failed receipt. Do not stop networkd, resolved, SSH, Kea or Unbound merely to swap Andaru’s state. Their reset-specific handling comes from the staged recovery generation/registered ownership plan.

### Durable phases and supervision

One stable package-provided systemd helper owns the reset transaction. It has an `OnFailure`/restart path and a boot reconciliation unit independent of engine/web. Deadlines are absolute wall-clock values in the synced journal; reboot does not create a fresh confirmation window.

Phases are monotonic:

```text
planned -> supervised -> writers-paused
        -> old-rename-intent -> old-renamed
        -> new-rename-intent -> new-live
        -> recovery-live -> committed -> cleaning -> complete
```

Every state tree carries a root-owned immutable epoch/role marker included in the reset plan: `old`, `staged-new` or `current-new`. Before each rename, sync an intent phase naming the expected source/destination epochs. Reconciliation inspects the actual canonical current/old/new entries with no-follow ownership checks:

- intent + source present + destination absent means rename did not happen; retry it;
- intent + source absent + matching destination means rename happened; sync the parent and complete the phase;
- any both-present, both-absent, wrong-epoch, link/type/ownership or unexpected-tree combination stops normal startup and requires the stable helper/console to diagnose—never guess or restart writers.

Before `old-rename-intent`, interruption restores paused units and aborts. From `old-rename-intent` through `new-rename-intent`, the rules above complete or roll back the exact observed rename. From `new-live` through `recovery-live`, interruption/deadline rolls back to the complete checkpoint and previous live generation/listeners. At `committed`, rollback is permanently forbidden; reconciliation resumes setup/recovery mode and cleanup only. `cleaning` is idempotent per owned resource; `complete` means all required post-commit cleanup has succeeded.

The rollback timer is armed in `supervised`, before the first pause/rename. Its action and confirmation serialize on the stable fence and re-read the durable phase. Thus a timer already firing cannot race a successful confirmation into deleting/restoring different trees.
Package locks remain held through `recovery-live` and are released only by rollback or the durable `committed` transition. The stable reset fence remains through setup/cleanup, enforcing recovery-mode API restrictions without blocking Debian package locks indefinitely.

### Cutover and trial

Under supervision, the helper:

1. advances to `writers-paused`;
2. syncs `old-rename-intent` with expected current-old/old-absent epochs;
3. renames current state to `old-state`, syncs the coordinator parent and records `old-renamed`;
4. syncs `new-rename-intent` with expected staged-new/current-absent epochs;
5. renames fully synced `new-state` into the canonical state path, syncs its parent and records `new-live`;
6. realizes the reset recovery generation and starts fresh engine/web;
7. runs bounded reset-specific readiness/auth/firewall checks and records `recovery-live`; and
8. waits for explicit authenticated confirmation from a new LAN session or the local console.

Reset-specific realization is prerequisite work, not the normal apply verifier unchanged. It intentionally removes unrelated networking, verifies only the selected management/recovery-LAN closure, listener/authentication and default-deny firewall, and carries a reset purpose plus state epoch in its generation manifest. Every reset helper/timer request includes the epoch; stale pre-reset numeric generation operations are refused.

The recovery listener binds selected LAN addresses only. Its filter drops HTTPS/SSH arriving from every non-allowlisted ingress **before** any established/related accept rule, so an existing WAN connection does not survive into recovery. Qualification includes a WAN session established before reset, not only new connection attempts.

No component deletes its own executable, package files or the reset helper.

### Confirmation and deletion

Confirmation requires the operation ID, active staged-state epoch and a new authenticated recovery session (or local console). While holding the same stable fence used by rollback, it:

1. verifies phase `recovery-live`, deadline and reset-administrator session;
2. writes the reset receipt to the new audit store and coordinator;
3. durably syncs phase `committed`—the irreversible decision that permanently forbids rollback;
4. cancels the timer as cleanup, not as the authority deciding commit;
5. marks setup required; and
6. advances to resumable `cleaning`.

Only after `committed` may irreversible cleanup begin. It deletes the old checkpoint, old audit/history and secret-bearing cold upgrade-recovery sets under `/var/lib/andaru-upgrades`, plus registered external extension state, through allowlisted ownership checks. Portable backup files/remote objects are never reset-owned and remain. Cleanup status is synced per resource; a crash resumes cleanup and never attempts rollback to a partially deleted checkpoint.

Confirmation does **not** delete the active recovery generation, reset TLS identity, reset-administrator credential or management-path rendered credentials. They remain required until setup confirms a normal replacement generation. Deletion is logical, not guaranteed physical erasure from SSDs, journaled filesystems, snapshots or external backups. Secrets in the rollback checkpoint remain sensitive until committed cleanup completes; reset status exposes that state without revealing paths or values.

## Setup completion

The fresh system remains in authenticated recovery mode until the shared setup workflow completes and confirms a normal generation through a new LAN connection. Setup then:

- replaces the recovery profile with the administrator’s chosen topology/policy;
- creates final TLS material as selected;
- removes temporary rendered recovery credentials/files;
- disables fallback listeners;
- records setup completion in audit; and
- retains only the normal confirmed generation/state.

Interrupted setup resumes from its durable phase. Reboot never opens a broader listener than the approved recovery-LAN allowlist.

## Scope of cleared and retained state

### Cleared/recreated

- saved configuration and history;
- configuration secret store;
- Andaru users, roles/assignments, password/API-token/TOTP credentials;
- sessions, CSRF state, rate-limit/login state;
- Andaru certificates/private keys except staged reset TLS material;
- audit/log/statistics state except the reset receipt required by policy;
- backup jobs/destination credentials (external backup files remain untouched);
- extension registry, units/drop-ins, owned data, secrets, generated UI/navigation;
- rendered generations/prefix/lease/next-hop state except the temporary recovery profile;
- secret-bearing cold package/state recovery sets under `/var/lib/andaru-upgrades`, after reset commitment; and
- other state registered as Andaru-owned.

### Retained

- Debian installation, package database, kernel and unrelated packages/configuration;
- installed Andaru core package, keyring/source configuration and systemd unit files;
- Debian SSH accounts/authorized keys and administrator-owned SSH policy;
- portable backups and remote objects (never pruned by reset);
- administrator-mounted NFS/SMB configuration outside Andaru;
- minimal reset receipt/audit evidence; and
- temporary old-state checkpoint only until trial confirmation.

Reset does not uninstall Andaru, restore pre-Andaru network ownership, unmask competing network/firewall writers or alter unrelated files.

## Extension boundary

Every installable extension must register:

- immutable identity/source/version;
- units/drop-ins/processes;
- owned state/data/secret roots;
- dependencies needed to stop/remove safely;
- reset ordering and bounded cleanup operation;
- reversible checkpoint/copy semantics for every external owned resource; and
- post-commit irreversible cleanup/removal semantics.

Before phase `committed`, reset may only stop an extension and make a verified reversible checkpoint; rollback must restore every external root/unit as well as the central state. An extension whose pre-commit change cannot be checkpointed blocks reset. Package removal, destructive external-root deletion and other irreversible cleanup are deferred until `committed`, remain stopped throughout the trial, and are retried through `cleanup.json` until complete. Failed cleanup blocks setup completion and is visible/actionable, but can never resurrect a partially deleted old state.

Only registered, root-owned allowlisted resources are removed. A malformed, missing or unknown registration blocks reset. Extensions cannot nominate arbitrary host paths or another extension’s secrets. Recursive APT from inside extension cleanup is not allowed; extension package-removal semantics remain a prerequisite and must honor the reset’s package exclusion and final committed cleanup phase.

## API boundary after prerequisites

The network-facing API belongs to the authenticated web control plane, not the current root-only engine socket directly. Proposed resources:

```text
POST /api/v1/factory-resets/preview
POST /api/v1/factory-resets
GET  /api/v1/factory-resets/{id}
POST /api/v1/factory-resets/{id}/confirmation
POST /api/v1/factory-resets/{id}/rollback
```

The root engine exposes corresponding typed root-local operations to the web process/CLI. Requests use saved-head/generation/store epoch preconditions and idempotent operation IDs. Duplicate run requests with the same intent return the same operation; the same ID with changed scope is refused. In-flight duplicates receive conflict/pending status, never a second destructive operation.

No endpoint accepts arbitrary filesystem paths, shell commands, URLs or extension-provided cleanup scripts.

## Threat model

Assets: network reachability, firewall ownership, administrator credentials, configuration secrets, audit evidence and unrelated Debian data.

Trust boundaries and controls:

- **Web request:** authenticated administrator permission, fresh reauthentication, CSRF, rate limiting, typed confirmation, intent hash and audit.
- **Console:** kernel-verified UID 0, controlling terminal, hidden passwords, typed confirmation; no remote header impersonation.
- **Staged paths:** fixed roots, no-follow ancestry, ownership/mode/type/depth checks, operation receipt before rename, no broad fallback path.
- **Extension registrations:** untrusted declarations validated against allowlisted extension-owned roots; no cross-extension/host deletion.
- **Recovery listeners:** HTTPS/authentication/default deny; explicit LAN allowlist; never WAN/unclassified.
- **Power loss/process crash:** stable systemd helper, durable phase journal, old state retained until confirmation, boot reconciliation.
- **Repudiation:** reset receipt survives either commit or rollback and contains no passwords/secrets.
- **Denial of service:** one reset at a time, bounded staging/locks/verification/deadline; insufficient space refuses before stopping writers.

A root attacker can already bypass these controls. They defend administrators from mistakes, corrupted state, hostile archives/extensions and partial failure; they do not claim root compromise containment.

## Prior art and deliberate difference

[pfSense factory defaults](https://docs.netgate.com/pfsense/en/latest/config/factory-defaults.html) resets configuration, attempts package removal and reboots into defaults that may require console access. Andaru deliberately does not copy that behavior: Debian/core software remains, unrelated host files remain, and reset is a confirmed LAN-recoverable trial with rollback.

[OPNsense installation guidance](https://docs.opnsense.org/manual/install.html) relies on known installation/live-environment access paths. Andaru has no universal appliance image or known port layout, so it cannot safely invent a fixed LAN address or default credential. Setup records explicit LAN recovery paths instead.

## Qualification

Before shipping reset:

- pure tests for closure selection, explicit LAN eligibility, no WAN/unclassified fallback, secret-backed management and source restrictions;
- real-store tests proving all old logical histories/secrets/users/credentials are absent from new stores;
- extension registry path/ownership/link/cross-tenant hostile cases;
- no-op preview, stale precondition, duplicate intent, cancellation and insufficient-space cases;
- process-crash and boot reconciliation before/after each rename intent write, rename syscall, parent-directory sync and phase-completion write, plus every network activation/audit/commit/delete boundary;
- root CLI PTY tests for hidden reset password and exact confirmation;
- web tests for permission, reauthentication, CSRF, backup offer, session revocation and new-session confirmation;
- package/upgrade coexistence and no maintainer-script reset;
- both Debian 13 netplan and ifupdown VMs, behind stopped disk snapshots, with continuous LAN probes and console/QGA observation;
- successful previous-management recovery, approved-LAN fallback, secret-backed topology, missing management hardware, reset helper/engine/web kills and reboots before/after cutover/confirmation;
- evidence that WAN never listens in normal or fallback recovery; and
- snapshot rollback/cleanup to the intended base state after tests.

Do not claim physical secure erasure, arbitrary storage durability, recovery on unapproved hardware, or WAN recovery.

## Prerequisite handoff

This design is complete enough to constrain prerequisites, but reset implementation remains blocked. Recommended next projects, each independently designed/planned/reviewed:

1. identity/credential/audit core and reset-administrator profile;
2. shared `andaru setup` workflow plus explicit management/recovery-LAN selection;
3. authenticated web/degraded-recovery listener and stable recovery-generation supervisor;
4. factory reset transaction described here.

Issue #15 follows the completed #13 work as previously directed. Factory reset remains part of #13; its prerequisite projects do not silently narrow its contract.

## Author calls for approval

- Purpose-bound rendered recovery material may temporarily contain only secrets needed for the selected management/LAN path, while logical secret stores are reset.
- Trusted recovery LANs are explicitly selected during setup. WAN and unclassified interfaces are never automatic fallbacks.
- GUI reset is refused if neither prior management nor an approved LAN recovery path is realizable. Console may explicitly re-elect a LAN path before proceeding.
- Reset is a rollback-protected state-tree trial, not immediate deletion followed by reboot.
- One reset administrator and minimal reset receipt are recreated/retained by policy; all old Andaru users/credentials/audit history are cleared.
- No factory-reset product code ships before identity, setup and recovery prerequisites are usable.

## Builder-check ruling

The builder asked whether the original pre-Andaru baseline survives reset. Author call: no. It is Andaru-owned historical state and is deleted with the confirmed reset checkpoint. Only the normalized network-ownership record above survives, including required DHCP client identities but no original configuration files or secrets. Cost if wrong: a future supported hand-back would need a separately designed migration because reset deliberately destroys that old baseline.

## Design review result

The builder check found that baseline retention was ambiguous; the design now deletes original pre-Andaru files after confirmed reset and retains only normalized ownership/DHCP identity metadata. The four-pillar adversarial review found no Critical issue and seven Important transaction/selection gaps. Corrections now:

- retain active recovery material through setup confirmation;
- publish an irreversible `committed` phase before deletion and serialize confirmation/rollback;
- start supervision and rollback timing before the first pause/rename;
- use a stable external fence plus held dpkg locks;
- select the actual `Pointers.Live` generation and require LAN eligibility;
- bind semantic inputs rather than volatile audit/session/statistics state;
- checkpoint extensions reversibly before commit and defer destructive cleanup;
- introduce reset-specific generation epochs/verification and no-WAN established-session handling;
- clear secret-bearing cold recovery sets only after commit; and
- make new reset-password entry unconditional.

A scoped closure check found one remaining rename-acknowledgement window. The final design records durable rename intent first and reconciles actual epoch-marked trees, with explicit fault injection around each intent/rename/sync/completion boundary. No Important/Critical design contradiction remains from that review.

These are design reviews, not implementation evidence. Runtime durability, authentication, hardware recovery and physical erasure remain future qualification work.
