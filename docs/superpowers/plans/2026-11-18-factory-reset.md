# Root-local factory reset

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `andaru ctl factory-reset` clears the old Andaru network state, wipes the contents of `/var/lib/andaru`, keeps Andaru's ownership of host networking, restarts Andaru on fresh stores, brings the box up on `recovery/` or else on `last-lan/`, and leaves one Andaru account named `admin`.

**Architecture:** The CLI only asks the questions, writes the `planned` journal at `/var/lib/andaru-reset/`, and starts the static unit `andaru-factory-reset.service`. That unit runs `andaru realize factory-reset`, which drives a new `internal/reset` package as root. Reset rechecks every precondition, then stops Andaru and tears down the old live generation: its `/run` files, the services it started, and its filter, replaced by deny-all. It copies the network-ownership record and the new `admin` verifier into the journal, wipes the tree, and opens fresh stores. It then applies the kept copy through the same create, activate and confirm path the engine uses, and starts Andaru again. A reboot is never required. The operator can choose one at the end. A power loss resumes through the same unit, which is ordered after `systemd-networkd`.

**Tech Stack:** Go, existing configuration, identity, audit and generation packages, existing hidden-terminal readers, systemd. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-17-factory-reset-data-design.md`, approved 2026-11-17. It amends `docs/superpowers/specs/2026-11-15-factory-reset-design.md` (PR #59, branch `design/factory-reset`). Where they disagree, the 2026-11-17 spec wins.

## Approved calls

The owner approved these on 2026-11-18. The adversarial review of the first draft changed calls 3, 6, 8 and 10. Those changes are recorded here.

1. **Console now, WebUI later.** Reset gets a GUI in the Andaru WebUI, built when that WebUI work arrives. This plan ships only `andaru ctl factory-reset` and adds no API route. The CLI's split, prompts and a `planned` journal on one side and the unit on the other, is the seam the WebUI will use. It writes the same journal after its own reauthentication, typed confirmation and backup offer, then starts the same unit.
2. **No independent dpkg lock-holder.** The 2026-11-15 lock-holder protected a rollback to the old tree. The 2026-11-17 spec removes that rollback. The reset unit takes `/var/lib/dpkg/lock-frontend`, then `/var/lib/dpkg/lock`, with `F_SETLK` and a bounded wait, and holds them until `done`. A crash releases them. Resume reacquires them and reruns `dpkg --audit` before it continues.
3. **The fence lives outside the tree.** The wipe unlinks `engine.lock` and `generations/lock`, so they cannot be the fence. The fence is the flock on `/var/lib/andaru-reset/lock`, held by the reset unit. It is backed by the journal: while `operation.json` exists, `andaru engine` and every `andaru realize` verb except `boot` and `factory-reset` refuse. The engine, leases and prefixes units get `ConditionPathExists=!/var/lib/andaru-reset/operation.json`, so systemd does not burn their restart limit. After the wipe, reset lets `config.Open`, `Activate` and `Confirm` take the new in-tree locks themselves.
4. **Default password bypass.** Identity refuses `admin` as a password: it is shorter than 15 runes and contains the login (`internal/identity/password.go:110-117`). Reset gets `HashResetPassword`, which skips the policy only for an empty entry, which means `admin`. An entered password must still pass the normal policy. `BootstrapAdmin`, `CreateAccount` and `SetPassword` keep refusing `admin`.
5. **The verifier rides in the journal.** The password is read before any destructive step. The journal stores the verifier, never the password. It is 0600 root in a 0700 root directory, and it is deleted at `done`.
6. **Ownership record, no hand-back.** The wipe deletes `generations/pointers.json` and `generations/baseline/`. Those hold the takeover flag, the baseline kind, the nftables mask ownership, and each ifupdown interface's name, hotplug flag and dhcpcd DUID. Reset copies that normalized record into the journal and writes it back into the fresh `generations/`, as `pointers.json` with only `Takeover` set and a baseline manifest that stores no files and has an empty `Connected` set. The manifest also carries `reset: true`. On such a tree, `Boot` never unmasks ifupdown or nftables and never runs `/etc/network/interfaces`. When no generation is usable, it loads deny-all and stays console-required. The ifupdown files under `/etc` are left alone. They are Debian files, the masks keep them inert, and nothing hands back to them.
7. **"Came up" is the existing verify.** A kept copy came up when `Activate` passes its existing verification (`internal/generation/realize.go` `unmet`). The old generation is torn down first, so the old WAN is no longer a managed link and does not enter `connected()`. There is no new reachability probe.
8. **One attempt per copy.** Reset confirms its own apply as root once verification passes. A rollback timer is still armed. If `Activate` fails verification, its existing restore of the previous set, the empty set after the teardown, is the spec's "discard that attempt". Reset then applies `last-lan/`. A crash or power loss during `applying-recovery` also counts as "did not come up". Resume applies `last-lan/` and does not retry `recovery/`. Resume trusts the generation pointers: a confirmed generation is restored with `Realizer.Restore` and verified before the phase becomes `up`. A boot during the reset loaded only deny-all, so the confirm alone is not proof the network is up.
9. **Backup offer, not a backup step.** Before the confirmation phrase, the CLI asks for a path for a portable backup. Entering one runs the existing root-local export, the same content as `andaru ctl backup export`, while the engine still runs. A path under `/var/lib/andaru`, `/var/lib/andaru-kept` or `/var/lib/andaru-reset` is refused. An empty answer skips the backup. The receipt records the choice.
10. **No reboot needed; the operator may ask for one.** Reset tears down the old realization and restarts Andaru in place. The CLI also asks "Reboot when the reset is done? [y/N]". The answer goes in the journal. At `done`, the unit runs `systemctl reboot` only if the answer was yes.

## Global Constraints

- Root only. `andaru ctl factory-reset run` refuses a non-zero euid, a non-terminal stdin, and any flag. There is no `--yes`, no password flag, and no environment source for the password.
- The typed confirmation is the exact phrase `FACTORY RESET`.
- The first password entry is read with `readIdentityTerminalValue` in hidden mode. An empty entry means `admin`, and no second entry is asked for. A non-empty entry is read a second time and must match.
- `planned` is written only after every check passes: `/var/lib/andaru` is usable (`kept.LiveTreeUsable`), `dpkg --audit` is clean, no generation is pending, no `andaru-realize-*` job is running, and no journal exists. `Plan` also writes a root-owned 0600 marker, `/var/lib/andaru/.andaru-reset`, holding the operation ID. It is the tree's identity across reboots and works on any filesystem. The wipe skips it, and `done` deletes it. The unit repeats every check under its locks before the first stop.
- `andaru ctl factory-reset abort` is allowed only at `planned`. It deletes the journal and stops nothing, because nothing was stopped yet. A refusal by the unit at `planned` does the same.
- Every resume refuses unless `/var/lib/andaru/.andaru-reset` exists as a regular root-owned file holding the journal's operation ID. A missing data mount leaves the root-disk directory without that marker, so resume stops there. It never creates `/var/lib/andaru`. The mountinfo mount ID is used only inside one boot, by the `STATX_MNT_ID` pre-scan before the wipe.
- Wipe deletes the contents of `/var/lib/andaru`, never the directory itself. It pre-scans the whole tree with `statx` `STATX_MNT_ID` before the first unlink, and refuses when any entry sits on another mount, bind mounts on the same filesystem included. It does not follow symlinks and does not run `mkfs`.
- Reset never reads or deletes `/var/lib/andaru-upgrades`. It never deletes `/var/lib/andaru-kept` or any child of it. It never reads `first/`. A confirm through the normal path may write `first/` when none exists. That is the existing write-once rule, and reset does not change it.
- `/etc/andaru/web.conf`, `/etc/andaru/engine-web.conf` and the web TLS key are host configuration under `/etc`, outside the wipe. They are kept. `andaru-web` stops with the engine, because it `Requires=` it.
- The only listener after reset is the one `recovery/` or `last-lan/` declares. Deny-all covers every moment between the teardown and the first confirmed generation, and the console-required outcome.
- `debian/andaru.prerm` refuses removal when `pointers.json` has `"takeover": true`, as well as when a generation is confirmed.
- Every previous account, session, token, audit row, generation and saved version is gone after `done`. The new audit store's first event is `factory_reset.completed`.
- No password, verifier or secret appears in a log line, an argument, an environment variable, or the receipt.

## Review Focus

1. A crash at every phase boundary, inside `Activate` and `Confirm`, and in the middle of the wipe, resumes to the outcome call 8 defines. It never wipes a tree that already holds new state. Tasks 4 and 5 pin each boundary.
2. Without a reboot, the old WAN, Kea, Unbound and the old filter are gone before the first apply. Task 5 pins this with a real `Realizer` and a fake host. The lab checks addresses and services.
3. The ownership record keeps nftables masked, keeps the takeover flag and keeps every dhcpcd DUID. A reset tree never hands back to ifupdown. Task 2 and the engine test in Task 2 pin that.
4. `admin` / `admin` is possible only through reset with an empty entry. Task 1 pins that `BootstrapAdmin`, `CreateAccount` and `SetPassword` still refuse it.
5. A missing mount, or a tree without the operation's marker, stops reset before the wipe and on every resume. Task 4 pins that.
6. While the journal exists, a second process cannot start the engine or run a realize verb. Task 6 pins that with a real second process.

## Structure

| Area | Files | Responsibility |
| --- | --- | --- |
| Reset admin | `internal/identity/password.go`, `internal/identity/store_linux.go`, `internal/identity/model.go` | `HashResetPassword`, `BootstrapResetAdmin` |
| Ownership record | `internal/generation/ownership.go`, `internal/generation/takeover.go` | Read and write the record; no hand-back on a reset tree |
| Teardown | `internal/generation/realize.go` | Tear down the live generation to deny-all |
| Kept declarations | `internal/config/lan_closure.go`, `cmd/andaru/realize.go` | One closure-to-declaration conversion for reset and disaster boot |
| Shared apply | `internal/engine/applies_api.go`, `internal/engine/apply_create.go` | The engine's create pipeline as a function reset can call |
| Audit action | `internal/audit/model.go` | `factory_reset.completed` |
| Reset | `internal/reset/journal_linux.go`, `internal/reset/reset_linux.go` | Fence, journal, checks, stop, teardown, wipe, bring-up, resume |
| CLI and units | `cmd/andaru/reset.go`, `cmd/andaru/main.go`, `cmd/andaru/ctl.go`, `packaging/systemd/*`, `debian/andaru.prerm` | Prompts, `run`, `abort`, `status`, unit, conditions, prerm guard |
| Docs | `docs/dev/core-api.md`, `docs/dev/vm-lab.md`, `debian/changelog` | Operator text, lab qualification |

## Task 1: Reset administrator

**Files:**
- Modify: `internal/identity/password.go`, `internal/identity/store_linux.go`, `internal/identity/model.go`
- Test: `internal/identity/store_linux_test.go`

**Interfaces:**
- Produces:
  - `func HashResetPassword(ctx context.Context, password []byte) ([]byte, error)`: empty means `admin` and uses `hashPassword`; anything else uses `hashPasswordWithPolicy` with login `admin`
  - `BootstrapResetAdmin(ctx context.Context, verifier []byte) (Account, error)` on `*store`, added to the `Store` interface (`model.go:173`). Same preconditions as `BootstrapAdmin` (`count==0 && revision==0`), login `admin`, administrator role.

- [ ] **Step 1: Write the failing tests**

`TestResetAdminDefault`: `HashResetPassword(ctx, nil)` succeeds, and `admin` authenticates against the result. `TestResetAdminPolicy`: `HashResetPassword(ctx, []byte("short"))` fails with the existing policy error, and a valid 15-rune password succeeds. `TestAdminStillRefusedElsewhere`: `BootstrapAdmin`, `CreateAccount` and `SetPassword` with login `admin` and password `admin` each still fail. `TestResetAdminNeedsEmptyStore`: `BootstrapResetAdmin` fails once any account exists.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/identity -count=1 -run 'ResetAdmin|StillRefused'`

Expected: FAIL because the functions do not exist.

- [ ] **Step 3: Implement**

`BootstrapResetAdmin` shares `BootstrapAdmin`'s transaction body and takes a verifier instead of a password. The policy exception is one empty-input branch inside `HashResetPassword` only.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/identity -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/identity
git commit -m "feat: create the reset administrator from a stored verifier"
```

## Task 2: Network-ownership record

**Files:**
- Create: `internal/generation/ownership.go`
- Modify: `internal/generation/takeover.go`
- Test: `internal/generation/ownership_test.go`, `internal/engine/takeover_api_test.go`

**Interfaces:**
- Produces:
  - `type Ownership struct { Kind string; Takeover bool; NftablesMasked bool; Interfaces []IfupdownInterface }`, where each interface keeps `Name`, `Hotplug` and `Identity`
  - `func (r Realizer) ReadOwnership() (Ownership, error)`: from `pointers.json` and `baseline/manifest.json`; fails when either is unreadable
  - `func (r Realizer) WriteOwnership(o Ownership) error`: into an empty `generations/`; writes `pointers.json` with only `Takeover`, and a manifest with no files, an empty `Connected` set and `reset: true`

- [ ] **Step 1: Write the failing tests**

`TestOwnershipRoundTrip`: for a netplan fixture and an ifupdown fixture with two DUIDs, read then write into a fresh directory. `ReadBaselineKind` returns the same kind, `ownsNftablesMask` matches, and the manifest stores no files and no netplan YAML. `TestOwnershipRefusesNonEmpty`: `WriteOwnership` fails when `generations/` already holds a generation. `TestResetTreeNeverHandsBack`: `Boot` on a written ifupdown tree with no usable generation runs no `systemctl unmask` and no `ifup`, loads deny-all, and returns `CONSOLE_REQUIRED`. In `internal/engine`, `TestIdentitiesAfterOwnership`: `identities()` on the written tree returns both DUIDs.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/generation ./internal/engine -count=1 -run 'Ownership|NeverHandsBack'`

Expected: FAIL because the functions do not exist.

- [ ] **Step 3: Implement**

Read with the no-follow helpers the baseline code uses. Write with `writePointers` and the existing manifest writer. A listed ifupdown interface without a DUID is an error. In `Boot`, the `reset` flag takes the console-required branch before the ifupdown hand-back branch.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/generation ./internal/engine -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/generation internal/engine
git commit -m "feat: carry network ownership across a wipe without a hand-back"
```

## Task 3: Teardown, kept declarations and the shared apply

**Files:**
- Modify: `internal/generation/realize.go`, `internal/config/lan_closure.go`, `cmd/andaru/realize.go`, `internal/engine/applies_api.go`, `internal/audit/model.go`
- Create: `internal/engine/apply_create.go`
- Test: `internal/generation/teardown_test.go`, `internal/config/lan_closure_test.go`, `internal/engine/applies_api_test.go`

**Interfaces:**
- Produces:
  - `func (r Realizer) Teardown(ctx context.Context) error`: under `generations/lock`, take `live(p)` and run `place(live, nil)`, `settle`, the `services(live, nil)` stop path, and a deny-all filter load
  - `func KeptDeclaration(body []byte) (decl []byte, secrets map[string]string, err error)`: accepts `recovery.json` or a `lan-closure`. `renderDisaster` in `cmd/andaru/realize.go` is changed to use it.
  - `func CreateApply(ctx context.Context, deps ApplyDeps, saved int, confirmTimeout time.Duration) (generation int, err error)`: the body of the `appliesHandler.create` pipeline from `applies_api.go:285-386` (gate, findings, authorization transition, `generation.Create`, `lan-closure.json`, `Arm`). The handler calls it. Reset generations pass `Takeover=false` and an empty `Baseline`.
  - `audit.ActionFactoryResetCompleted Action = "factory_reset.completed"`

- [ ] **Step 1: Write the failing tests**

`TestTeardownClearsLive`: on a fake host with a live WAN and LAN generation, Kea and Unbound active, after `Teardown` no Andaru file remains under `/run/systemd/network`, `/run/andaru-kea` or `/run/andaru-unbound`, both services were stopped, the loaded filter drops all input except loopback, and a following `Activate` of a LAN-only generation passes verify with no WAN in `connected()`. `TestKeptRecoveryDeclaration`: a `recovery.json` returns its declaration and secrets unchanged. `TestKeptLANDeclaration`: a `lan-closure` returns a declaration with the management segment, its allowed sources, the link ancestry and the zone, and no WAN, NAT, DHCP service or user policy. It saves into a fresh store and renders with `MANAGEMENT_REQUIRED` satisfied. `TestKeptLANRefusesUplink`: a DHCPv4 management link fails. `TestDisasterRenderUnchanged`: the existing disaster boot tests pass against the shared conversion. `TestCreateApplySharedWithHandler`: the existing applies API tests pass unchanged after the extraction.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/generation ./internal/config ./internal/engine ./cmd/andaru -count=1 -run 'Teardown|KeptDeclaration|KeptLAN|DisasterRender|CreateApply'`

Expected: FAIL

- [ ] **Step 3: Implement**

Move the closure conversion out of `renderDisaster` into `KeptDeclaration` and call it from both places. Extract `CreateApply` without changing the handler's behaviour.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./... -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal cmd/andaru
git commit -m "feat: tear down the live generation and share the apply pipeline"
```

## Task 4: Journal, fence, checks, stop and wipe

**Files:**
- Create: `internal/reset/journal_linux.go`, `internal/reset/reset_linux.go`
- Test: `internal/reset/reset_linux_test.go`, `internal/reset/wipe_root_integration_linux_test.go`

**Interfaces:**
- Consumes: Task 1 verifier bytes, Task 2 `Ownership`, Task 3 `Teardown`
- Produces:
  - `const Dir = "/var/lib/andaru-reset"`
  - phases, in order: `planned`, `stopped`, `torn-down`, `wiped`, `fresh`, `applying-recovery`, `applying-last-lan`, then `up` or `console-required`, then `done`
  - `type Operation struct { ID string; Phase string; Ownership generation.Ownership; Verifier []byte; Backup string; Reboot bool; Started time.Time }`
  - `func Plan(ctx context.Context, env Env, verifier []byte, backup string, reboot bool) (Operation, error)`: checks, then writes `planned`; called by the CLI
  - `func Abort(env Env) error`: only at `planned`
  - `func Resume(ctx context.Context, env Env) (Operation, error)`: called by the unit; runs from the journal's phase to `done`
  - `func Active(dir string) (bool, error)`

- [ ] **Step 1: Write the failing tests**

`TestPlanRefuses`: an unusable tree, a pending generation, `dpkg --audit` output, a running realize job and an existing journal each fail without writing a journal. `TestResumeRechecksAtPlanned`: a check that fails only at `Resume` time aborts to no journal, and stops nothing. `TestAbortOnlyAtPlanned`. `TestResumeStopsInOrder`: the fake runner sees the fence lock, the dpkg locks, `dpkg --audit`, then the upgrade `preparationUnits` and `andaru-web.service` stopped, the drain, `Teardown`, and only then the wipe. `TestMarkerRequired`: a resume refuses when the marker is missing, holds another operation ID, or is a symlink. It does not create `/var/lib/andaru`. A resume after a simulated reboot, with a new mount ID and the same marker, continues. `TestWipeKeepsMarker`: after `wiped` only the marker remains. `TestWipeKeepsMountPoint`: after `wiped` the directory exists, is empty, keeps its mode and owner, and a symlink inside it was removed without touching its target. `TestWipeLeavesKeptAndUpgrades`: sibling fixtures are byte-identical afterwards. `TestResumeAtEveryPhase`: `failAt` stops the run before each journal sync, and in the middle of the wipe. `Resume` then reaches call 8's outcome and never wipes once the phase is `fresh` or later. `TestJournalHoldsNoPassword`. In `wipe_root_integration_linux_test.go`, which runs as root in a private mount namespace like the existing `*_root_integration_linux_test.go` files, `TestWipeRefusesBindMount`: a bind mount inside the tree from the same filesystem is found by the pre-scan, and nothing is unlinked.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/reset -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement**

Reuse the stop, drain and job-idle helpers and the unit lists from `internal/upgrade/prepare_linux.go`. Do not fork them. Write each journal phase with tmp, sync, rename and a parent-directory sync. Wipe with `openat`, `O_NOFOLLOW` and depth-first removal, after the `STATX_MNT_ID` pre-scan. Journal `wiped` only after the directory holds only the marker and is synced. Fresh-store creation is all-or-nothing: a resume at `wiped` runs the wipe again, which still skips the marker, before it creates the stores. That clears a partial bootstrap, so `BootstrapResetAdmin` always sees an empty store and the audit event is written once. Once `fresh` is journaled, nothing wipes again.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/reset -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/reset
git commit -m "feat: stop Andaru, tear down its network and wipe the state tree under a resumable journal"
```

## Task 5: Bring-up

**Files:**
- Modify: `internal/reset/reset_linux.go`
- Test: `internal/reset/bringup_linux_test.go`

**Interfaces:**
- Consumes: Tasks 1 to 4, `config.Open`, `identity.Open`, `audit.Open`, Task 3 `CreateApply`, `Activate`, `Confirm`

- [ ] **Step 1: Write the failing tests**

All bring-up tests use a real `Realizer` against the existing fake host and runner, not a faked verify.

`TestFreshStores`: after `fresh`, the directory holds new configuration, identity and audit stores, the Task 2 ownership tree, and exactly one account, `admin`, with the journal's verifier. The first audit event is `factory_reset.completed`, with actor `root`, the backup path or `skipped`, and no secret. `TestRecoveryUp`: with `recovery/` present, saved version 1 is the recovery declaration, the generation is confirmed with `Takeover=false`, `pointers.json` keeps `"takeover": true`, and the phase is `up`. `TestRecoveryFailsToLAN`: a `recovery/` naming a missing link fails verify, `Activate` restores the empty set, and the next generation is the `last-lan/` declaration, confirmed. `TestNoRecovery`: without `recovery/`, `last-lan/` is applied directly. `TestCrashDuringRecovery`: `failAt` inside `Activate` and inside `Confirm` during `applying-recovery`, then `Resume`, ends on `last-lan/` and does not retry `recovery/`. A crash after the confirm write ends `up` on `recovery/`. `TestConsoleRequired`: with neither copy usable, the phase is `console-required`, `admin` exists, no generation is confirmed, deny-all is loaded, and `first/` was never opened. `TestCrashInFresh`: `failAt` after `BootstrapResetAdmin` commits and before `fresh` is journaled, then `Resume`, ends with exactly one `admin` account and exactly one `factory_reset.completed` event. `TestResumeRestoresConfirmed`: `failAt` after `Confirm` writes the confirmed pointer and before `up` is journaled, then a boot with the journal present (deny-all only), then `Resume`. The confirmed generation's `.network` files are back under `/run/systemd/network`, its filter replaced deny-all, its services run, the existing verify passes, and only then is the phase `up`. When that verify fails, resume follows call 8 as if the confirm had not happened. `TestDoneCleans`: at `done` the journal directory and the marker are gone, the dpkg and fence locks are released, the Andaru units are started, and `systemctl reboot` runs only when the journal said `reboot: true`.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/reset -count=1 -run 'Fresh|Recovery|NoRecovery|Crash|Console|Done'`

Expected: FAIL

- [ ] **Step 3: Implement**

Open the fresh stores in-process. Write the ownership record before any generation exists. Bootstrap `admin` and write the audit event. Save the kept declaration, call `CreateApply`, then `Activate`, then `Confirm` as root. Before each apply, journal the phase, so a resume knows which copy was tried. A resume that finds a confirmed generation restores it with `Realizer.Restore`. `Restore` reuses `Boot`'s placement of the confirmed generation, then `settle` and the services and filter path, and waits on the existing `unmet` check. It journals `up` only after that passes. `Restore` lives in `internal/generation/realize.go` and needs its own test there.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/reset -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/reset
git commit -m "feat: bring a reset box up on the recovery or last LAN copy"
```

## Task 6: Command, unit, fence hooks and prerm

**Files:**
- Create: `cmd/andaru/reset.go`, `cmd/andaru/reset_test.go`, `cmd/andaru/reset_root_integration_linux_test.go`, `packaging/systemd/andaru-factory-reset.service`
- Modify: `cmd/andaru/main.go`, `cmd/andaru/ctl.go`, `cmd/andaru/realize.go`, `internal/engine/socket_linux.go`, `packaging/systemd/andaru-engine.service`, `packaging/systemd/andaru-leases.service`, `packaging/systemd/andaru-prefixes.service`, `debian/andaru.prerm`, `debian/andaru.install`, `tests/shell/`

**Interfaces:**
- Produces: `andaru ctl factory-reset run|abort|status`, and the hidden verb `andaru realize factory-reset`, which calls `reset.Resume`. `status` prints the phase and the operation ID and nothing secret. `run` calls `reset.Plan`, then `systemctl start --no-block andaru-factory-reset.service`, then follows `status` until a final phase or until the session drops.
- `andaru-factory-reset.service`: `Type=oneshot`, `After=systemd-networkd.service`, `ConditionPathExists=/var/lib/andaru-reset/operation.json`, `WantedBy=multi-user.target`, no `Restart`

- [ ] **Step 1: Write the failing tests**

`TestResetRefusesFlags`. `TestResetPrompts`: a PTY test enters an empty password, skips the backup, answers no to the reboot, types `FACTORY RESET`, and sees `reset.Plan` called with the default verifier and `reboot: false`. A wrong phrase exits 1 with no journal. `TestResetPasswordMismatch`. `TestBackupPathRefused`: a path under any of the three trees is refused. `TestRunStartsUnit`: after the prompts the fake runner sees exactly one `systemctl start --no-block andaru-factory-reset.service`, and the CLI stops no unit and deletes nothing. In `reset_root_integration_linux_test.go`, `TestFenceRefusesSecondProcess`: with a journal present, a separately executed `andaru engine` and `andaru realize leases` each exit non-zero with the reset-in-progress error. `TestBootDuringReset`: `Realizer.Boot` with an active journal loads deny-all, runs no `Activate`, and returns. A shellspec example checks that `prerm remove` refuses when `pointers.json` has `"takeover": true` and no confirmed generation.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./cmd/andaru ./internal/engine ./internal/generation -count=1 -run 'Reset|Fence|BootDuring|Backup'` and `shellspec tests/shell --shell bash`

Expected: FAIL

- [ ] **Step 3: Implement**

Reuse `readIdentityTerminalValue` and the hidden reader. Boot gets the journal check as a `Realizer` field set in `cmd/andaru/realize.go`, so `internal/generation` does not import `internal/reset`. Add the `ConditionPathExists=!` lines to the three units. Install the new unit and enable it in the package.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./... -count=1` and `shellspec tests/shell --shell bash`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/andaru internal packaging debian tests/shell
git commit -m "feat: add the root-local factory reset command"
```

## Task 7: Docs and lab qualification

**Files:**
- Modify: `docs/dev/core-api.md`, `docs/dev/vm-lab.md`, `debian/changelog`

- [ ] **Step 1: Document the command**

`core-api.md` describes `run`, `abort` and `status`, the recovery order, the `admin` / `admin` default, the optional reboot, and the console-required outcome. It states that `/var/lib/andaru-upgrades`, `/var/lib/andaru-kept` and `/etc/andaru` are untouched. `debian/changelog` gets one bullet.

- [ ] **Step 2: Qualify in the lab**

On the Debian 13 netplan and ifupdown VMs from `docs/dev/vm-lab.md`, behind stopped disk snapshots, with a continuous LAN probe, run each case over SSH from the LAN, without a reboot unless the case says otherwise:
- a reset with `recovery/` stored ends on that network;
- a reset whose `recovery/` names a missing link ends on `last-lan/`;
- a reset with neither copy ends console-required, with deny-all loaded and `admin` able to use the root CLI;
- a power-off at `stopped`, at `torn-down`, in the middle of the wipe and at `applying-recovery` resumes at boot to call 8's outcome;
- a reset with the reboot answer yes reboots once and comes back to the same outcome;
- the ifupdown VM keeps its DHCP address on a link that stays managed, so its DUID was kept;
- after a `last-lan/` or console-required outcome, the old WAN has no Andaru-configured address, Kea and Unbound are stopped, and a WAN probe sees no listener;
- the dropped SSH session does not stop the reset.

Record the evidence in `vm-lab.md`. Restore the snapshots afterwards.

- [ ] **Step 3: Commit**

```bash
git add docs/dev debian/changelog
git commit -m "docs: document factory reset and record its lab qualification"
```

## Out of scope

- The WebUI reset, its reauthentication, and its API route. They come with the WebUI work and reuse `reset.Plan` and `andaru-factory-reset.service`.
- `andaru setup`. The console-required outcome uses the existing root CLI.
- Add-ons, the owned-state registry and `addons/`. None exist yet, so none block reset.
- Reading or deleting `/var/lib/andaru-upgrades` or `first/`.
- Keeping the old netplan YAML or ifupdown baseline copies from `generations/baseline/`.
- Physical erasure.

## Self-review

- Spec coverage: the wipe and the kept trees are Task 4. The recovery, then `last-lan/`, then console order is Task 5. The `admin` account and the default are Tasks 1 and 5. The console command, typed confirmation and backup offer are Task 6. The crash checkpoint lives outside both trees and is deleted at `done` (Tasks 4 and 5). Default deny in every recovery state is Tasks 2, 3, 5 and 6. `first/` is never read by reset.
- The plan adds no network endpoint, no default address, no factory-reset boot from `first/`, no deletion of `/var/lib/andaru-upgrades` or `first/`, and no restore of previous verifiers.
- Review of the first draft: C1 is fixed by Task 3 `Teardown`, C2 by the static unit after networkd and the deny-only boot hook, and C3 by the fence outside the tree. Of the Important findings, I1 is fixed by `Plan`, `Resume` and `Abort`; I2 by the in-tree marker; I3 by the `STATX_MNT_ID` pre-scan and the root test; I4 by `Takeover=false` on reset generations and the full interface record; I5 by `reset: true` and no hand-back; I6 by `CreateApply`; I7 by call 8; I8 by the prerm guard and the deny-all console-required boot; I9 by accepting `Activate`'s restore as the discard; and I10 by the unit conditions.
