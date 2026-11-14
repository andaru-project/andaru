# Conventional APT Upgrades Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Explicitly check for Andaru updates and prepare a consistent local recovery set, while leaving installation and manual package repair to ordinary APT.

**Architecture:** Add a small root-local `internal/upgrade` package and `andaru upgrade check|prepare`. Reuse existing state ownership and generation locks. A separate read-only configuration verifier checks copied databases without running initialization or migrations. No custom installer, background updater, recovery daemon or HTTP API.

**Tech Stack:** Go, existing modernc SQLite, Linux file locks and systemd, Debian APT/dpkg tools, existing Go/shellspec/VM qualification.

**Spec:** `docs/superpowers/specs/2026-11-14-apt-upgrades-design.md` (owner-approved).

## Global Constraints

- Debian 13 first; current Go toolchain and existing dependencies only. No Python, jq, sqlite3 CLI, Go APT binding or new package-manager library.
- Neither package download nor installation is automatic. `check` refreshes metadata only. Explicit `prepare` may fetch the *installed* version as a recovery artifact, never the candidate.
- Installation remains `apt-get install andaru`, `apt upgrade`, or ordinary direct package administration; no `andaru upgrade install` command, package hook requiring preparation, unattended `-y`, reboot, timer or migration framework.
- Production state is `/var/lib/andaru`; recovery is `/var/lib/andaru-upgrades/`. Recovery directories are root-owned 0700, files 0600, and contain secrets. No API, debug export or upload exposes them.
- Preserve the existing networkd/nftables boundary. Preparation must not stop/restart/reload networkd, resolved, Kea, Unbound or SSH, change the ruleset or forwarding, or disable connectivity rollback timers.
- Refuse pending/activating/rolling-back connectivity applies. Exclude both engine and generation writers while copying; a live `.db` alone is not a snapshot.
- A recovery set is timestamped, not a fence until installation. Subsequent accepted writes are outside it.
- Missing previous `.deb` means an explicitly reported state-only result, not a substituted version or an APT installation refusal. A state-only result cannot evict an existing software-and-state set.
- Keep one previous completed software-and-state set; publish and fsync its verified replacement before pruning. Remove only operation-owned staging.
- Standard APT signature checking stays on. No nested APT/dpkg from a maintainer script and no dependency/full-offline recovery promise.
- Preserve Native execution: inline tasks, one final condensed four-leg adversarial reviewer, then the repository's PR process. Do not install packages on the development host; package/VM checks use containers or the lab.

## Review Focus

1. **WAL-only committed changes and mismatched config/secrets:** the recovery copy contains the latest paired state, passes integrity checks, and verification never migrates or writes to the source (Task 2).
2. **An apply arrives as preparation starts:** the second check under both locks refuses it, restores only paused units and never disables its independent rollback timer (Task 3).
3. **Power loss/cancellation between pausing and publication:** a durable marker identifies the paused units; no partial set replaces the completed set, and a later invocation does not pretend preparation completed (Tasks 2–3).
4. **An old RC is no longer indexed or a same-named cached `.deb` is wrong:** preserve state, report missing software distinctly, and retain the last complete set (Tasks 1–3).
5. **Manual package reinstall starts the old engine too soon:** the qualified repair runbook inhibits all Andaru state writers until both databases and generation state are restored, without touching administrator masks (Task 5).

## File ownership and interfaces

- `internal/upgrade/apt_linux.go`: APT metadata checks and retrieval of the exact installed package; private command runner, no shell strings.
- `internal/upgrade/snapshot_linux.go`: safe cold filesystem copy, manifest/checksums and atomic recovery-set publication/retention.
- `internal/upgrade/prepare_linux.go`: root operation coordination, package-activity checks, unit pause/drain/resume, locks and interruption marker.
- `internal/config/recovery_linux.go`: read-only database-pair verifier using existing private schema/hash helpers.
- `cmd/andaru/upgrade.go`: root CLI parsing and safe output; `main.go` dispatches to it before ordinary engine/ctl argument parsing.
- Tests live beside their implementations; root-required scenarios use `_integration_linux_test.go` with `//go:build linux && integration` like existing store tests. Add the new root binary to CI rather than silently skipping its safety tests.
- `docs/dev/core-api.md`: operator commands, limits, actual repair instructions and lab evidence. No new user-facing documentation convention.

Public-to-the-module interfaces (all in `internal`, not a public extension API):

```go
// internal/upgrade
// Readable fields are Debian versions, not semver. ReleaseNotes is a fixed Andaru releases URL.
type Availability struct {
    Installed, Candidate, Architecture, Repository, Suite, ReleaseNotes string
    RefreshedAt time.Time
}
type Preparation struct {
    Directory, Version string
    PreparedAt time.Time
    PackageRetained bool
    Warnings []string
}
func Check(ctx context.Context) (Availability, error)
func Prepare(ctx context.Context) (Preparation, error)

// internal/config: verifies an existing COLD state directory; never config.Open.
func VerifyRecoveryState(ctx context.Context, stateDir string) error

// cmd/andaru
func upgradeCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int
```

Inside `upgrade`, share one private environment/runner seam for tests, following `Realizer.Run` rather than building an executor framework: `type commandRunner func(context.Context, string, ...string) ([]byte, error)`. A private options value supplies fixture state/recovery paths and a clock; production exported entry points use only fixed defaults. No test root or arbitrary command flag is exposed through the CLI.

---

### Task 1: Explicit metadata check and verified previous-package retrieval

**Files:** create `internal/upgrade/apt_linux.go`, `apt_linux_test.go`, `cmd/andaru/upgrade.go`, `upgrade_test.go`; modify `cmd/andaru/main.go`, `main_test.go`.

**Interfaces:** implement `Check`, `Availability` and `upgradeCommand` for `check`. Private `installedPackage(ctx, run) (packageInfo, error)` returns `packageInfo{Version, Architecture string}` for the installed `andaru`. Private `retainPackage(ctx, run, info, stage) (artifact, error)` returns `artifact{Path, SHA256 string; Available bool}`; unavailable indexed version is distinct from corruption or a command/network error. Task 3 consumes these helpers. Bound captured text to 1 MiB and each metadata/download command to a 5-minute context; reject truncated output rather than parsing it. Do not pass unvalidated strings as shell fragments.

- [ ] **Step 1: Write failing tests.** `TestCheckRefreshesMetadataOnly` supplies command fixtures and asserts installed/candidate/version/suite plus a refresh timestamp; the runner errors if asked to `install`, `upgrade`, `download` or change units. `TestCheckRefreshFailure` expects an error rather than a successful stale result. `TestInstalledVersionMissing`, `TestAptMetadataMalformed`, `TestCheckNoCandidate` cover absent package, malformed/oversized output, and no available candidate. CLI tests expect exit 2 on extra arguments/unknown operations and no execution before root validation.
- [ ] **Step 2: Run the targeted tests red:** `go test ./internal/upgrade ./cmd/andaru -run 'TestCheck|TestInstalledVersion|TestAptMetadata|TestUpgrade'`. Confirm failures are missing implementation or expected assertions, not fixture compilation mistakes.
- [ ] **Step 3: Implement.** Run fixed argv with `LC_ALL=C`: explicit `apt-get update` with error-on-any-refresh-failure, `dpkg-query` for the installed package, `apt-cache policy/show` for exact-version metadata. Do not parse terminal color/control sequences as trusted display values. Use the existing official releases page for `ReleaseNotes`, not a guessed semver/tag mapping. Use Debian version identity verbatim, with option-safe fixed package arguments. For retrieval, accept an exact metadata-matching cached artifact or explicitly `apt-get download andaru:<arch>=<installed-version>` in private staging; verify SHA-256 from trusted APT metadata plus `dpkg-deb` package/version/architecture before accepting it. No arbitrary URL fetching, authentication bypass, package substitution or dependency downloads. Missing trusted metadata/artifact is `Available=false`; a present artifact that fails verification is an error.
- [ ] **Step 4: Add artifact tests and watch them fail, then pass.** `TestRetainInstalledPackageNotCandidate`, `TestOldRCUnavailable`, `TestWrongCachedPackageRefused` (wrong version/architecture/hash), `TestPackageDownloadFails`, and `TestPackageVersionIsAnArgument` protect the recovery artifact boundary. `check` never downloads a `.deb`. Run the Task 1 tests green and the CLI's `--help`/invalid-operation scenarios.
- [ ] **Step 5: Commit:** `feat: explicitly check APT updates and retain verified recovery packages`.

### Task 2: Cold state verification and atomic recovery sets

**Files:** create `internal/config/recovery_linux.go`, `recovery_integration_linux_test.go`, `internal/upgrade/snapshot_linux.go`, `snapshot_linux_test.go`, `snapshot_integration_linux_test.go`.

**Interfaces:** implement `config.VerifyRecoveryState`. Private `copyState(ctx, source, destination string) ([]fileDigest, error)` copies an already-quiesced state tree; caller must hold the locks from Task 3. `fileDigest` has `Path string`, `Size int64` and `SHA256 string`. Private `publishRecovery(ctx, root, stage string, manifest recoveryManifest) (Preparation, error)` verifies, fsyncs and publishes. The version-1 JSON manifest records `formatVersion`, `preparedAt`, package version/architecture/dependency inventory, package-retained status, state-file digests and recorded Andaru unit states (`unitState{Name, EnabledState string; WasActive bool}`). It contains no secret values. Each owned set has `state/`, optional `package.deb` and `manifest.json`; `current.json` is an atomic record naming the latest complete set, not a symlink. Generated set identifiers and the ownership marker are validated before resolving/pruning paths. Do not turn it into a configurable archive framework.

- [ ] **Step 1: Write failing tests.** Root integration test `TestVerifyRecoveryStateWALPair` creates the normal two-store state in a subprocess that commits and exits without clean shutdown, leaving committed WAL content with no active writer. Verify the cold copy includes those writes. Assert original database and WAL bytes are unchanged by verification. `TestRecoveryStateMismatch` changes the saved secret reference/copy and expects refusal; `TestRecoveryStateCorruption` and `TestRecoveryDoesNotMigrate` cover a broken DB and an older store missing the newer additive change tables (the verifier must not create those tables).
- [ ] **Step 2: Run red:** `go test -tags integration ./internal/config -run 'TestVerifyRecovery|TestRecoveryState|TestRecoveryDoesNotMigrate'` as root, once. Non-root testing must not be presented as proof of the root-owned path checks.
- [ ] **Step 3: Implement the verifier without `Open`.** Reuse `checkMembers`, `probeDatabase`, strict document decoding and hash invariants. Open only the existing copies with SQLite `mode=ro`; run `PRAGMA integrity_check` on both and verify the saved config body/hash and its referenced secret body/hash. Do not run initialization, DDL, migrations, write pragmas or checkpoint commands on the source. If a helper needs to be factored from existing hash verification, run LSP references first and preserve all callers' checks.
- [ ] **Step 4: Write failing copy/publication tests, implement, run green.** `TestSnapshotIncludesGenerationAndWAL`, `TestSnapshotRefusesLinksAndSpecialFiles`, `TestSnapshotModes`, `TestSnapshotCancelledBeforePublish`, `TestSnapshotFailureKeepsPrevious`, `TestStateOnlyKeepsCompleteRecovery`, and `TestRecoveryPruneOwnership` exercise real filesystem contents. Copy directories as 0700 and files as 0600, accepting only root-owned regular files/directories and no symlinks, hardlinks or special files. The external recovery root cannot sit inside source state. Persist a format/ownership marker before making files eligible for pruning. Stage on the same filesystem; checksum, verify, fsync files/directories and atomically publish a `current` record naming the new set. Prune the old owned set only after that record is durable. State-only output cannot replace the current complete software-and-state set; retain at most one such state-only result, also replaced only after publication. Reserve space before pause/copy: state logical bytes plus artifact bytes and `max(64 MiB, stateBytes/10)` free-space reserve; fail cleanly on ENOSPC even after that estimate.
- [ ] **Step 5: Commit:** `feat: preserve and verify cold upgrade recovery state`.

### Task 3: Explicit preparation with bounded writer exclusion

**Files:** create `internal/upgrade/prepare_linux.go`, `prepare_linux_test.go`, `prepare_integration_linux_test.go`; extend `cmd/andaru/upgrade.go`, `upgrade_test.go`.

**Interfaces:** implement `Prepare` and `Preparation`, consuming Task 1's metadata/artifact helpers and Task 2's copy/publication functions. `prepare(ctx, env) (Preparation, error)` is private for fixture paths/runner injection. CLI `prepare` prints the directory, source version/time, secret-bearing-state warning and explicit package-retained/state-only result. A state-only preparation is a completed state snapshot (exit 0 plus a prominent warning), not a false full-recovery success. Any unsafe/corrupt copy or operational failure exits 1.

- [ ] **Step 1: Write failing tests.** `TestPrepareRefusesPackageActivity`, `TestPrepareRefusesBrokenDpkgState`, `TestPrepareRejectsPendingApply`, `TestPrepareRechecksPendingUnderLocks`, `TestPrepareDrainsRealizer`, `TestPrepareRestoresOnlyPausedUnits`, `TestPrepareLeavesNetworkServicesUntouched`, `TestPrepareInterruptedMarker`, and `TestPrepareMissingPackageReportsStateOnly`. Fakes assert no `networkctl`, `nft`, network/service stop/reload command, package install, or rollback-timer cancellation. POSIX record-lock tests use a separate child process holding the lock (same-process locks are not reported as conflicting); use real filesystem locks, not source-string assertions.
- [ ] **Step 2: Run red:** `go test ./internal/upgrade ./cmd/andaru -run 'TestPrepare|TestUpgrade'`; build/run the root-required cases with `go test -tags integration ./internal/upgrade -run TestPrepare` as root.
- [ ] **Step 3: Implement the exact operation sequence.** Check root and canonical root-controlled paths; reject malformed pointer JSON and inconsistent package state, not just a substring match. Detect APT/dpkg activity with Linux POSIX record-lock inspection on existing frontend/database lock files (not `flock`, which is a different lock namespace). Never delete or hold those locks while invoking APT tools. Treat the check as best-effort against other root operations, as the spec says. Take a private preparation-operation lock to exclude a second prepare command. Read installed version, inventory and prepare the artifact while the engine still runs. Estimate space. Record/fsync the exact unit states and current boot ID in `preparing.json` before the first stop. Stop only active Andaru lease/prefix path/timer activators, stop the engine gracefully, let already-running lease/prefix services drain, then acquire existing `engine.lock` and `generations/lock` (when that directory exists). Use a 30-second overall pause/drain/lock deadline; refuse and resume on timeout. Under the generation lock re-read pointers/state and refuse pending/activating/rolling-back applies. Copy/verify/publish, release locks, then start only the units this invocation stopped. Preserve masks and enabled/disabled states. A start/resume failure is reported even if the snapshot succeeded. On ordinary errors/cancellation attempt bounded cleanup with a fresh cleanup context; SIGKILL/power loss leaves the durable marker for explicit diagnosis, not silent recovery. If a marker exists on a later invocation, stop and show the recorded fixed unit names and snapshot status; do not auto-resume an obsolete boot/package context or silently overwrite the marker.
- [ ] **Step 4: Prove red→green, including failure paths.** Busy package lock and an apply registered during shutdown refuse safely; no independent rollback timer is disabled. A WAL-only config/secret write and realizer update appear together in the snapshot. Cancellation and injected copy/publication/start failures restore only paused components or leave an actionable marker. A pre-disabled unit never starts. Check both `PackageRetained` results and source timestamps. Re-run the targeted Task 3 tests green.
- [ ] **Step 5: Commit:** `feat: explicitly prepare local recovery before APT upgrades`.

### Task 4: Wire root checks and package smoke proof into CI

**Files:** modify `.github/workflows/ci.yml`, `scripts/ci/install-test.sh`, `scripts/ci/install-check.sh` only where the existing smoke proof needs the new CLI. Do not change maintainer scripts to require preparation.

**Interfaces:** build `internal/upgrade` as a root test binary beside the existing config/engine/ctl suites; no new service/unit dependency. Existing `andaru` package file selection already installs the complete Go binary, so no extra packaged executable or unit is needed.

- [ ] **Step 1: Add a consumer-level failing scenario to the existing package smoke test.** Installed package answers `andaru upgrade check`; a test repository advertises a newer `.deb`, and the check still leaves dpkg version and running engine invocation unchanged and downloads no candidate. Root `upgrade.test` executes the real ownership/locks/WAL-copy tests from Tasks 2–3. A bare APT reinstall without preparation still succeeds. Run the new scenario red before the implementation is wired into the installed package.
- [ ] **Step 2: Update the workflow/test driver minimally.** Use the existing trixie/systemd container and package build patterns. A local test repository is only a package test fixture, not appliance production infrastructure. `check` may use metadata from it; it cannot bypass signatures in the production command. Test setup must establish its own scoped signing trust. Add the root `upgrade.test` invocation from `/` with `TMPDIR` unset, like its neighbours, and ensure root config verifier tests are selected rather than skipped.
- [ ] **Step 3: Run one final full gate.** `go vet -tags=integration ./...`; `go test -race ./...`; `GOARCH=arm64 go build -o /dev/null ./cmd/andaru`; shellspec, shellcheck for changed scripts and actionlint. Build/run config, engine, generation, ctl and upgrade root integration binaries; run the existing render namespace/Kea/networkd checks in their qualified container environment. Run `build-packages.sh` for `any` and `all`, lintian and `install-test.sh`. Preserve failed output rather than piping it away; one failure is evidence to diagnose, not permission to rerun until green.
- [ ] **Step 4: Commit:** `ci: exercise upgrade checks and root recovery preparation`.

### Task 5: Qualify and document the manual Debian recovery path

**Files:** modify `docs/dev/core-api.md` (Installing/Upgrading and lab qualification), `docs/dev/vm-lab.md` only if its existing helper instructions need updating, and release notes/package changelog where this repository already records release-visible behavior. No new automation that starts a recovery supervisor.

**Interfaces:** all preparation uses Task 3. Repair is a documented, actually executed root procedure; it is not a new `recover` command or an HTTP endpoint.

- [ ] **Step 1: Write the runbook draft from the approved spec.** Show `upgrade check`, optional `upgrade prepare`, then ordinary interactive `apt-get install andaru`; explain candidate vs installed versions, state-only results, secret-bearing copies, the 30-second preparation bound, interrupted-marker diagnostics, snapshot timestamp/accepted-write loss, no automatic downloads/install/rollback, and no full dependency/offline guarantee. Use only evidence-backed commands in the final version. Preserve existing administrator masks during temporary startup inhibition; inhibit engine and every Andaru realization writer before reinstalling old software, then restore the matching full state before any old writer can start. Do not call `config.Open` on newer source data, run generic archive extraction into `/`, delete package locks, or use `policy-rc.d` as an uncontrolled global override.
- [ ] **Step 2: Qualify on netplan and ifupdown VMs from known snapshots, retaining console access.** Establish baseline package/generation/service/network facts. Test explicit check without a package download; optional preparation amid config and lease/prefix writes; pending-apply refusal; normal direct APT upgrade/reinstall with and without preparation; failed package configure; candidate startup with unsupported state; interrupted preparation; and repair with startup inhibited until the old package and paired state agree. Inspect that no state writer ran early, the restored config/secret saved version and confirmed generation match, management reconnects, nftables ownership remains correct, and administrator-disabled units/masks stay unchanged. Simulate missing prior RC metadata: preparation must preserve state and explicitly report the missing package, not fetch a different release.
- [ ] **Step 3: Bootstrap the first rc2 transition manually.** Rc2 lacks these commands. Execute the cold-copy writer-exclusion and verified rc2 artifact retention steps using existing tools, then upgrade normally. Do not claim the new helper protected an upgrade before it was installed. When Internet is disconnected, demonstrate only the already-satisfied-dependencies repair case and state that limitation.
- [ ] **Step 4: Replace draft commands/claims with observed evidence.** Record exact old/new versions, package hashes, source state version, commands, results and untested cases in the guide's existing lab-qualification style. If any runbook step cannot be safely qualified, stop and resolve it; do not replace proof with wording. No production code is considered complete until both baselines pass.
- [ ] **Step 5: Final condensed review and PR.** One `reviewer` covers contract, correctness/hostile, test honesty and ponytail/over-engineering over the whole branch and actual lab record. Resolve findings, commit/push, open/register the PR, use CI and available bot reviews under the current owner policy. Do not declare slice 2 complete before that process and the lab qualification finish.

## Self-review outcome

The five tasks cover all approved scope: explicit metadata checks (1), artifact availability (1), paired cold state and retention/security (2), writer exclusion/interruption behavior (3), installed/ordinary APT compatibility and CI (4), manual repair and rc2 bootstrap on both baselines (5). No UI, custom install transaction, automatic activation/rollback, extension framework or portable backup implementation is introduced. Every Review Focus item has a named owning test/scenario above. The only new filesystem/schema format is the private version-1 recovery manifest and operation marker.
