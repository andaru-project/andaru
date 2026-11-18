# Root-local factory reset

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `andaru ctl factory-reset` wipes the contents of `/var/lib/andaru`, keeps Andaru's ownership of host networking, brings the box up on `recovery/` or else on `last-lan/`, and leaves one Andaru account named `admin`.

**Architecture:** A new `internal/reset` package runs the whole reset as root, with the engine stopped, inside a transient systemd unit, `andaru-factory-reset`. It does not go through the engine socket, because the engine's state is what it deletes. The CLI only reads the prompts, writes the `planned` journal and starts that unit with `systemd-run`, so an SSH session dropped by the network change does not kill the reset. The WebUI will later start the same unit after writing the same journal. A journal at `/var/lib/andaru-reset/` makes every step resumable after a crash or reboot. Before the wipe, reset copies a normalized network-ownership record and the new `admin` verifier into that journal. After the wipe, it opens fresh stores in-process, writes the ownership record into the new `generations/`, saves the kept copy as saved version 1, and applies and confirms it with the existing generation verbs. A failed `recovery/` apply goes straight to `last-lan/`. It never rolls back to the empty pre-apply set.

**Tech Stack:** Go, existing configuration, identity, audit and generation packages, existing hidden-terminal readers. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-17-factory-reset-data-design.md`, approved 2026-11-17. It amends `docs/superpowers/specs/2026-11-15-factory-reset-design.md` (PR #59, branch `design/factory-reset`). Where they disagree, the 2026-11-17 spec wins.

## Author calls for approval

These are builder decisions the specs leave open, or places where the code forces a choice. Each needs owner approval before Task 1 starts.

1. **Console now, WebUI later.** The owner confirmed on 2026-11-18 that reset gets a GUI in the Andaru WebUI, built when that WebUI work arrives. This plan ships only `andaru ctl factory-reset` and adds no API route. The split between a prompt client and the `andaru-factory-reset` unit is the seam the WebUI will use: it writes the same `planned` journal after its own reauthentication, typed confirmation and backup offer, then starts the same unit.
2. **No independent dpkg lock-holder.** The 2026-11-15 lock-holder protected a rollback to the old tree against package changes. The 2026-11-17 spec removes that rollback. Reset therefore takes both dpkg locks (`/var/lib/dpkg/lock-frontend`, then `/var/lib/dpkg/lock`) with `F_SETLK` in its own process for the stop, wipe and bring-up. A crash releases them. Resume reacquires them before it continues. No step needs the old tree, so a package operation that ran in the gap cannot make resume unsafe. Resume still refuses on `dpkg --audit` output.
3. **The journal is the fence.** While `/var/lib/andaru-reset/operation.json` exists and its phase is not `done`, `andaru engine` refuses to start, and `andaru realize boot` resumes the reset instead of booting. No other mutator needs a new check: the engine is stopped and every realize verb takes `generations/lock`, which reset holds.
4. **Default password bypass.** Identity refuses `admin` as a password: it is shorter than 15 runes and contains the login (`internal/identity/password.go:110-117`). The spec requires `admin` / `admin` when the operator enters nothing. Reset gets a dedicated `identity.BootstrapResetAdmin`. It skips the policy only for that exact default. An entered password must still pass the normal policy. Nothing else can create a policy-violating password.
5. **The verifier rides in the journal.** The password is read before any destructive step, so a crash after the wipe must not lose it. The journal stores the password verifier, never the password. The journal file is 0600 root in a 0700 root directory, and is deleted at `done`. The verifier is the same one the identity store would hold, so this adds no new secret type.
6. **Ownership record outside the wipe.** A wipe deletes `generations/pointers.json` and `generations/baseline/`. Today those are the only record that Andaru took over host networking, which masks it owns, and which dhcpcd DUIDs it must keep (see Task 2). Reset copies a normalized ownership record into the journal before the wipe. It writes the record back into the fresh `generations/` as a baseline manifest with no stored files. The old netplan YAML and ifupdown files are not kept. The 2026-11-15 spec's builder-check ruling already deletes them at reset.
7. **"Came up" is the existing verify.** A kept copy came up when `Activate` passes its existing verification (`internal/generation/realize.go` `unmet`) and the rendered management path has a bound, configured link. There is no new reachability probe. The spec says to reuse the existing checks.
8. **Auto-confirm as root.** Reset confirms its own apply once verification passes. The 2026-11-17 spec removes the wait for an authenticated confirmation. A rollback timer is still armed for the apply. If reset dies before it confirms, the timer rolls the apply back, and resume applies `last-lan/`.
9. **Backup offer, not a backup step.** Before the confirmation phrase, the command asks for a path for a portable backup. Entering one runs the existing root-local backup export through the engine while it is still running. An empty answer skips it. The skip is recorded in the receipt.

## Global Constraints

- Root only. `andaru ctl factory-reset` refuses a non-zero euid, a non-terminal stdin, and any flag. There is no `--yes`, no password flag, and no environment source for the password.
- Typed confirmation is the exact phrase `FACTORY RESET`.
- The password is read twice with the existing hidden `/dev/tty` reader. An empty first entry means `admin`, and then it is not asked again. A non-empty entry must pass the identity policy.
- Reset refuses before any stop when: `/var/lib/andaru` is not usable (`kept.LiveTreeUsable` is false), `dpkg --audit` reports anything, a generation is pending, a realize job is running, or a reset journal already exists. An existing journal is resumed only by `andaru ctl factory-reset resume` or by boot, never by a second `run`.
- Wipe deletes the contents of `/var/lib/andaru`, never the directory itself, because it may be a mount point. It does not follow symlinks, does not cross into another filesystem below it, and does not run `mkfs`.
- Reset never reads or deletes `/var/lib/andaru-upgrades`. It never deletes `/var/lib/andaru-kept` or any child of it. It never reads `first/`.
- The only listener after reset is the one `recovery/` or `last-lan/` declares. No WAN listener and no default address are invented. `last-lan/` cannot contain an uplink, because the recorder already refuses one.
- On the console-required outcome, `admin` is still created, the journal records `console-required`, and the engine starts so the operator can use the existing root CLI.
- Every previous account, session, token, audit row, generation and saved version is gone after `done`. The new audit store's first event is the reset receipt.
- No password, verifier or secret appears in a log line, an argument, an environment variable, or the receipt.

## Review Focus

1. A crash at every phase boundary resumes to the same outcome, and never runs the wipe on a tree that already holds new state. Task 4 pins each boundary.
2. A failed `recovery/` apply never leaves the box with no LAN. It goes straight to `last-lan/`, and the existing rollback to an empty previous set is not used. Task 5 pins that.
3. The ownership record keeps nftables masked, keeps the takeover flag, and keeps every dhcpcd DUID after a wipe. Task 2 pins that the fresh tree boots as a taken-over host.
4. `admin` / `admin` is possible only through reset with an empty entry. Task 1 pins that every other caller still gets the policy error.
5. Missing mount: reset refuses up front and never wipes or populates a tree it did not first prove usable. Task 4 pins that.

## Structure

| Area | Files | Responsibility |
| --- | --- | --- |
| Reset admin | `internal/identity/store_linux.go`, `internal/identity/password.go` | `BootstrapResetAdmin`, default-password exception |
| Ownership record | `internal/generation/ownership.go` | Extract and restore the normalized record |
| Kept declarations | `internal/config/lan_closure.go` | Turn `last-lan/` into a saved declaration |
| Reset journal and phases | `internal/reset/journal_linux.go`, `internal/reset/reset_linux.go` | Fence, phases, stop, wipe, bring-up, resume |
| CLI | `cmd/andaru/reset.go`, `cmd/andaru/main.go`, `cmd/andaru/ctl.go` | Prompts, `run`, `resume`, `status` |
| Boot and engine hooks | `internal/generation/takeover.go`, `internal/engine/socket_linux.go` | Resume at boot, refuse engine start |
| Docs and package | `docs/dev/core-api.md`, `debian/changelog`, `docs/dev/vm-lab.md` | Operator text, lab qualification |

## Task 1: Reset administrator

**Files:**
- Modify: `internal/identity/store_linux.go`, `internal/identity/password.go`
- Test: `internal/identity/store_linux_test.go`

**Interfaces:**
- Produces:
  - `func HashResetPassword(ctx context.Context, password []byte) ([]byte, error)`, built on `hashPasswordWithPolicy` and `hashPassword`: an empty password means `admin`; any other value goes through `validatePassword` with login `admin`
  - `func (s *Store) BootstrapResetAdmin(ctx context.Context, verifier []byte) (Account, error)`: same preconditions as `BootstrapAdmin` (`count==0 && revision==0`), login `admin`, administrator role

- [ ] **Step 1: Write the failing tests**

`TestResetAdminDefault`: `HashResetPassword(ctx, nil)` succeeds, and `admin` authenticates against the result. `TestResetAdminPolicy`: `HashResetPassword(ctx, []byte("short"))` fails with the existing policy error, and a 15-rune password without the login succeeds. `TestBootstrapAdminStillRefusesAdmin`: `BootstrapAdmin` with login `admin` and password `admin` still fails. `TestResetAdminNeedsEmptyStore`: `BootstrapResetAdmin` fails once any account exists.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/identity -count=1 -run 'ResetAdmin|BootstrapAdminStill'`

Expected: FAIL because the functions do not exist.

- [ ] **Step 3: Implement**

Reuse the existing hash path. The exception is one comparison on the literal default, inside `HashResetPassword` only. `BootstrapResetAdmin` shares `BootstrapAdmin`'s transaction body and takes a verifier instead of a password, so the journal can carry it.

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
- Test: `internal/generation/ownership_test.go`

**Interfaces:**
- Produces:
  - `type Ownership struct { Kind string; Takeover bool; NftablesMasked bool; Ifupdown *IfupdownOwnership }`, with only the mask flags and per-interface DHCP identities from the baseline
  - `func (r Realizer) ReadOwnership() (Ownership, error)`: from `pointers.json` and `baseline/manifest.json`; fails when either is unreadable
  - `func (r Realizer) WriteOwnership(o Ownership) error`: into an empty `generations/`; writes `pointers.json` with only `Takeover` set and a manifest with no stored files

- [ ] **Step 1: Write the failing tests**

`TestOwnershipRoundTrip`: for a netplan fixture and an ifupdown fixture with two dhcpcd DUIDs, `ReadOwnership` then `WriteOwnership` into a fresh directory gives a tree where `ReadBaselineKind` returns the same kind, `ownsNftablesMask` is true when it was true before, and `identities()` returns the same DUIDs. `TestOwnershipHoldsNoFiles`: the written manifest lists no file and no netplan YAML. `TestOwnershipRefusesNonEmpty`: `WriteOwnership` fails when `generations/` already holds a generation. `TestBootAfterOwnership`: `Boot` on the written tree with no confirmed generation reaches the "no confirmed generation" path, not `disasterBoot`.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/generation -count=1 -run Ownership`

Expected: FAIL because the functions do not exist.

- [ ] **Step 3: Implement**

Read with the same no-follow helpers the baseline code uses. Write with the existing `writePointers` and manifest writer. A missing DUID for an ifupdown interface the baseline listed is an error, not an empty identity.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/generation -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/generation
git commit -m "feat: carry network ownership across a wipe"
```

## Task 3: Kept copies as saved declarations

**Files:**
- Modify: `internal/config/lan_closure.go`
- Test: `internal/config/lan_closure_test.go`

**Interfaces:**
- Produces:
  - `func KeptDeclaration(body []byte) (decl []byte, secrets map[string]string, err error)`: accepts `recovery.json` (`declaration` + `secrets`) or a `lan-closure` document, and returns a declaration that `Store.Save` accepts

- [ ] **Step 1: Write the failing tests**

`TestKeptRecoveryDeclaration`: a `recovery.json` returns its declaration and secrets unchanged. `TestKeptLANDeclaration`: a `lan-closure` returns a declaration with the management segment, its allowed sources, the link ancestry, and the zone, and no WAN, NAT, DHCP service or user policy. Saving it into a fresh store succeeds and renders with `MANAGEMENT_REQUIRED` satisfied. `TestKeptLANRefusesUplink`: a hand-edited closure with a DHCPv4 management link fails.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/config -count=1 -run KeptDeclaration`

Expected: FAIL because the function does not exist.

- [ ] **Step 3: Implement**

Reuse the closure decoder and the existing uplink check. Validate the result with the existing declaration validator before returning it.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/config -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: turn a kept copy into a saved declaration"
```

## Task 4: Journal, stop and wipe

**Files:**
- Create: `internal/reset/journal_linux.go`, `internal/reset/reset_linux.go`
- Test: `internal/reset/reset_linux_test.go`

**Interfaces:**
- Consumes: Task 1 verifier bytes, Task 2 `Ownership`
- Produces:
  - `const Dir = "/var/lib/andaru-reset"`
  - phases, in order: `planned`, `stopped`, `wiped`, `fresh`, `applying-recovery`, `applying-last-lan`, `up` or `console-required`, `done`
  - `type Operation struct { ID string; Phase string; Ownership generation.Ownership; Verifier []byte; Backup string; Started time.Time }`
  - `func Run(ctx context.Context, env Env, verifier []byte, backup string) (Operation, error)`
  - `func Resume(ctx context.Context, env Env) (Operation, error)`
  - `func Active() (bool, error)`
  - `type Env` holds the state, kept and reset paths, the unit runner, the dpkg lock paths and a `failAt` seam

- [ ] **Step 1: Write the failing tests**

`TestRunRefusesUnusableTree`: a missing `configuration/` fails before any journal is written and before any unit stop. `TestRunRefusesPending`, `TestRunRefusesDpkgAudit`, `TestRunRefusesExistingJournal`: each fails before a stop. `TestRunStopsInOrder`: the fake runner sees the upgrade `preparationUnits` stopped, then the drain, then `engine.lock` and `generations/lock` taken, then the dpkg locks held. `TestWipeKeepsMountPoint`: after `wiped`, `/var/lib/andaru` exists, is empty, keeps its mode and owner, and a symlink inside it was removed without touching its target. A bind-mounted subdirectory fixture is refused, not crossed. `TestWipeLeavesKeptAndUpgrades`: sibling `andaru-kept` and `andaru-upgrades` fixtures are byte-identical afterwards. `TestResumeAtEveryPhase`: for each phase, `failAt` stops the run just before the next journal sync. `Resume` then reaches the same final tree as an uninterrupted run, and never wipes once the phase is `fresh` or later. `TestJournalHoldsNoPassword`: the journal file contains the verifier and not the plaintext.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/reset -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement**

Copy the stop, drain and lock sequence from `internal/upgrade/prepare_linux.go`. Reuse its unit lists and do not fork them. Take the dpkg locks with `F_SETLK` after `dpkg --audit` comes back clean. Write each journal phase with tmp, sync, rename and a parent-directory sync. Wipe with `openat` and `O_NOFOLLOW`, compare each child's device number with the state directory's, and remove depth first. Journal `wiped` only after the directory is empty and synced.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/reset -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/reset
git commit -m "feat: stop Andaru and wipe the state tree under a resumable journal"
```

## Task 5: Bring-up

**Files:**
- Modify: `internal/reset/reset_linux.go`
- Test: `internal/reset/bringup_linux_test.go`

**Interfaces:**
- Consumes: Tasks 1 to 4, `config.Open`, `identity.Open`, `audit.Open`, `generation.Create`, `Activate`, `Confirm`

- [ ] **Step 1: Write the failing tests**

`TestFreshStores`: after `fresh`, the state directory holds new configuration, identity and audit stores, the Task 2 ownership tree, and exactly one account, `admin`, with the journal's verifier. The first audit event is the reset receipt, with the actor `root`, the backup path or `skipped`, and no secret. `TestRecoveryUp`: with `recovery/` present and a fake verify that passes, saved version 1 is the recovery declaration. The generation is confirmed, carries the takeover flag, and the phase is `up`. `TestRecoveryFailsToLAN`: when the recovery verify fails, the next generation is the `last-lan/` declaration. The test asserts that no call restored the empty previous set between the two. `TestNoRecovery`: without `recovery/`, `last-lan/` is applied directly. `TestConsoleRequired`: with neither copy usable, the phase is `console-required`. `admin` still exists, no generation is confirmed, and `first/` was never opened. `TestDoneCleans`: at `done`, the journal directory is removed, the units are started, and the dpkg locks are released.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/reset -count=1 -run 'Fresh|Recovery|NoRecovery|Console|Done'`

Expected: FAIL

- [ ] **Step 3: Implement**

Open the fresh stores in-process while reset holds `engine.lock`, as the engine would. Write the ownership record before any generation exists. Save the kept declaration, create the generation with the takeover flag the ownership record carries, arm the rollback timer through the existing runner, and call `Activate`. On a verify failure, apply the next copy over the failed one as a new generation. Do not call the empty-previous rollback. Confirm as root once verification passes.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/reset -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/reset
git commit -m "feat: bring a reset box up on the recovery or last LAN copy"
```

## Task 6: Command, boot resume and engine fence

**Files:**
- Create: `cmd/andaru/reset.go`, `cmd/andaru/reset_test.go`
- Modify: `cmd/andaru/main.go`, `cmd/andaru/ctl.go`, `internal/generation/takeover.go`, `internal/engine/socket_linux.go`

**Interfaces:**
- Produces: `andaru ctl factory-reset run|resume|status`. `status` prints the phase and the operation ID and nothing secret. `run` writes the `planned` journal and starts `systemd-run --unit=andaru-factory-reset --property=Type=exec andaru realize factory-reset`, then follows `status` until a final phase. The hidden verb `andaru realize factory-reset` calls `reset.Resume`.

- [ ] **Step 1: Write the failing tests**

`TestResetRefusesFlags`: any argument after `run` fails with usage. `TestResetPrompts`: a PTY test enters an empty password, skips the backup, types `FACTORY RESET`, and sees `reset.Run` called with the default verifier. A wrong phrase exits 1 without stopping anything. `TestResetPasswordMismatch`: two different non-empty entries exit 1. `TestEngineRefusesDuringReset`: `serveEngine` fails with a reset-in-progress error while an active journal exists. `TestBootResumes`: `Realizer.Boot` calls the resume hook instead of booting while an active journal exists. `TestRunStartsUnit`: after the prompts, the fake runner sees exactly one `systemd-run` with unit `andaru-factory-reset`, and the CLI process itself stops no unit and deletes nothing.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./cmd/andaru ./internal/engine ./internal/generation -count=1 -run 'Reset|Resumes'`

Expected: FAIL

- [ ] **Step 3: Implement**

Reuse `readNewIdentityPassword` and `readIdentityTerminalValue`. Allow an empty first entry only in this command. The backup offer calls the existing backup export client before `reset.Run`. Boot gets the resume hook as a `Realizer` field set in `cmd/andaru/realize.go`, so `internal/generation` does not import `internal/reset`.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./... -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/andaru internal/engine internal/generation
git commit -m "feat: add the root-local factory reset command"
```

## Task 7: Docs and lab qualification

**Files:**
- Modify: `docs/dev/core-api.md`, `docs/dev/vm-lab.md`, `debian/changelog`

- [ ] **Step 1: Document the command**

`core-api.md` describes `run`, `resume`, `status`, the recovery order, the `admin` / `admin` default, and the console-required outcome. It states that `/var/lib/andaru-upgrades` and `/var/lib/andaru-kept` are untouched. `debian/changelog` gets one bullet.

- [ ] **Step 2: Qualify in the lab**

On the Debian 13 netplan and ifupdown VMs from `docs/dev/vm-lab.md`, behind stopped disk snapshots, with a continuous LAN probe:
- a reset with `recovery/` stored ends on that network;
- a reset whose `recovery/` names a missing link ends on `last-lan/`;
- a reset with neither copy ends console-required, with `admin` able to log in at the console;
- a power-off at `stopped`, at `wiped` and at `applying-recovery` resumes at boot to the same outcome;
- the ifupdown VM keeps its DHCP address, so its DUID was kept;
- a WAN probe sees no listener in any outcome.

Record the evidence in `vm-lab.md`. Restore the snapshots afterwards.

- [ ] **Step 3: Commit**

```bash
git add docs/dev debian/changelog
git commit -m "docs: document factory reset and record its lab qualification"
```

## Out of scope

- The WebUI reset, its reauthentication, and its API route. They come with the WebUI work and reuse the `planned` journal and the `andaru-factory-reset` unit.
- `andaru setup`. The console-required outcome uses the existing root CLI.
- Add-ons, the owned-state registry and `addons/`. None exist yet, so none block reset.
- Reading or deleting `/var/lib/andaru-upgrades` or `first/`.
- Keeping the old netplan YAML or ifupdown files after a reset.
- Physical erasure.

## Self-review

- Spec coverage: wipe and kept trees are Task 4. The recovery, then `last-lan/`, then console order is Task 5. The `admin` account and the default are Tasks 1 and 5. The console command, typed confirmation and backup offer are Task 6. The crash checkpoint lives outside both trees and is deleted at `done` (Task 4 and 5). `first/` is never read by reset.
- The plan adds no network endpoint, no default address, no factory-reset boot from `first/`, no deletion of `/var/lib/andaru-upgrades` or `first/`, and no restore of previous verifiers.
- Open risk: the prerm guard that refuses package removal on a confirmed host is off between the wipe and the first confirm. Reset holds the dpkg locks for that whole window, so no package operation can run in it.
