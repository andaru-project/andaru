# Network Takeover and Confirmed Apply Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Activate a saved network configuration on a netplan cloud-image host through `andaru ctl apply network [--takeover]`, with a rollback timer armed first, explicit confirmation, and boot from the confirmed generation.

**Architecture:** The engine renders, gates and stores a generation directory, then asks PID 1 (`systemd-run`) to run `andaru realize <verb>`, the only code that touches `/run/systemd/network`, the pointers and the states. A new `internal/generation` package holds both the on-disk store and the realize verbs, parameterised by filesystem root and command runner so they are unit-tested against a temporary tree. The rollback is a PID-1 timer running `andaru realize rollback`, so it works with the engine dead.

**Tech Stack:** Go 1.27.3 standard library plus the existing `golang.org/x/sys` (flock). Debian 13: systemd 257.13, iproute2, netplan.io 1.1.2, cloud-init 25.1.4.

**Spec:** [docs/superpowers/specs/2026-11-11-network-takeover-apply-design.md](../specs/2026-11-11-network-takeover-apply-design.md)

## Global Constraints

- Only `realize` writes `pointers.json`, `state.json` and anything outside the engine's state directory, and only while holding an exclusive `flock` on `generations/lock`. The engine never takes the lock.
- **`pointers.json` is the single commit point.** A verb acts on a generation only if the pointers say so (`pending == id`); `state.json` is the record. Each verb's write order below is chosen so that a kill between the two writes leaves a state that rollback or boot resolves.
- `realize` writes and deletes only paths matching `^/run/systemd/network/50-andaru-[^/]+\.(network|netdev)$`, the baseline's `^/run/systemd/network/10-netplan-[^/]+$`, `/etc/netplan/*.yaml` and `/etc/cloud/cloud.cfg.d/99-andaru-network.cfg`. Any other manifest path is refused.
- Every state write is temporary file, `fsync`, rename, directory `fsync`. A torn or unparsable `state.json` or `pointers.json` is an error, never a guess.
- Nothing outside `/run` and Andaru's state changes before confirmation.
- Timeouts: confirm timeout 30–600 s, default 120; rollback timer `60 + timeout` s with `AccuracySec=1s`; lock wait 70 s; after the lock, `activate` works under 45 s (verification 30 s of it) and the other verbs under 60 s; every external command 10 s; transient units `RuntimeMaxSec=180`. `boot` waits 5 s for the lock and works under 20 s.
- Deviations from the spec's text, same intent: `live` is JSON `null` for the baseline; the engine, not `realize confirm`, stops the timer; `realize` takes `--state-dir` and `rollback` takes a reason; the boot unit adds `Before=cloud-init-local.service`; new error codes `APPLY_NOT_AWAITING` and `REALIZE_UNAVAILABLE`; an ifupdown host is refused with or without `--takeover`.
- Root Unix peer authorization happens before any store or host read, as on existing routes. No secret value reaches a generation in this slice: PPPoE is gated out and non-allowlisted files are refused.
- No new Go module dependency. No declaration schema change.
- Integration tests use the `integration` build tag, require root, and fail rather than skip when a prerequisite is missing. Nothing in CI changes host networking.

## Lab facts this plan relies on (VM 114, 2026-11-11)

- `networkctl list --json=short` gives `{"Interfaces":[{"Name":"eth0","AdministrativeState":"configured","NetworkFile":"/run/systemd/network/10-netplan-eth0.network",...}]}`; unmanaged links have no `NetworkFile`.
- `ip -j route show default` gives `[{"dst":"default","gateway":"10.0.0.1","dev":"eth0",...}]`.
- Netplan's files are `0640 root:systemd-network` (`.network`) and `0640 root:root` (`.link`). networkd runs as `systemd-network`, so a restored file with the wrong group is unreadable.
- The baseline `.network` matches `PermanentMACAddress=` **and** `Name=eth0`.
- `/lib/netplan` and `/usr/lib/netplan` exist and hold only `PLACEHOLDER`; `/run/netplan` does not exist.
- A process with the engine's sandbox properties can run `systemd-run` and `systemctl stop` for transient units.

## Review Focus

1. **A rollback timer that fires during or after a confirmation:** it must do nothing and leave the confirmed generation live (Tasks 3 and 4).
2. **An empty saved configuration applied with `--takeover`:** refused with `WOULD_UNCONFIGURE` for the interface carrying the session, not an "apply" that removes its address (Task 6).
3. **Baseline files restored with their original mode, owner and group** after a rollback, or networkd cannot read them and the host loses its address at the next reload (Task 4).
4. **The client disconnects during an apply:** the apply finishes and is readable through `GET`; the handler must not run under the request's cancellation (Task 7).
5. **Hostile or odd names:** a manifest path such as `/run/systemd/network/50-andaru-x/../../etc/y.network`, a non-numeric directory under `generations/`, and `/api/v1/applies/0`, `/-1`, `/abc`, `/99999999999999999999` are refused or ignored without a panic (Tasks 2 and 7).

---

## File map

| File | Responsibility |
| --- | --- |
| `internal/render/networkd.go`, `testdata/*.golden`, `render_test.go` (modify) | `UseDNS=yes` |
| `internal/generation/store.go` | types, atomic writes, `Create`, `Read`, `List`, path allowlist |
| `internal/generation/realize.go` | `Realizer`: lock, file swap, verify, `Activate`, `Rollback` |
| `internal/generation/takeover.go` | baseline capture and restore, `Confirm`, permanent steps, `Boot` |
| `internal/host/links_linux.go`, `realizer_linux.go` (modify) | export `ParseLinks`; `Managed`, `ParseManaged`, `ReadManaged`; `Baseline`, `ReadBaseline` |
| `internal/engine/apply_gates.go` | pure gate function |
| `internal/engine/applies_api.go`, `runner_linux.go` (create); `api.go`, `preview_api.go`, `socket_linux.go`, `openapi.json` (modify) | routes, `systemd-run` runner |
| `cmd/andaru/realize.go` (create); `main.go`, `ctl.go` (modify) | `andaru realize`, `ctl apply`, `ctl applies` |
| `packaging/systemd/andaru-network-boot.service` | boot oneshot, text exactly as in the spec |
| `docs/dev/core-api.md`, `docs/dev/vm-lab.md`, `README.md` (modify) | operator guide, undo procedure, lab evidence |

---

### Task 1: Host keeps DHCP-provided DNS

**Files:** modify `internal/render/networkd.go`, `internal/render/render_test.go`, `internal/render/testdata/*.golden`.

- [ ] **Step 1:** Change the assertion at `render_test.go:274` and add `TestHostKeepsOfferedDNS`: for fixture `ethernet-uplink`, every `[DHCPv4]`, `[DHCPv6]` and `[IPv6AcceptRA]` section contains `UseDNS=yes`, no rendered file contains `UseDNS=no`, and `UseNTP=no`, `UseHostname=no`, `UseDomains=no` are still present.
- [ ] **Step 2:** `go test ./internal/render` — FAIL.
- [ ] **Step 3:** In `networkd.go` set `UseDNS=yes` in `ignoreOffered` (line 42) and in the `[IPv6AcceptRA]` list (line 108). Replace the comment on line 41 with the spec's `shortcut:` line. Regenerate the goldens with the package's existing update mechanism and read the diff: only `UseDNS` lines change.
- [ ] **Step 4:** `go test ./internal/render` — PASS. Commit `feat: keep DHCP-provided DNS for the host`.

---

### Task 2: Generation store

**Files:** create `internal/generation/store.go`, `internal/generation/store_test.go`.

**Interfaces — produces:**

```go
package generation

const (
	Activating = "activating"; Awaiting = "awaiting_confirmation"
	Confirmed = "confirmed"; RolledBack = "rolled_back"; Failed = "failed"
)

type FileEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`   // "0644"
	SHA256 string `json:"sha256"` // hex
}
type Manifest struct {
	ID                    int         `json:"id"`
	SavedVersion          string      `json:"savedVersion"`
	CreatedAt             time.Time   `json:"createdAt"`
	Takeover              bool        `json:"takeover"`
	ConfirmTimeoutSeconds int         `json:"confirmTimeoutSeconds"`
	Files                 []FileEntry `json:"files"`
}
type State struct {
	State    string     `json:"state"`
	Previous *int       `json:"previous"` // nil: the baseline
	Deadline *time.Time `json:"deadline,omitempty"`
	Reason   string     `json:"reason,omitempty"`
}
// Pointers: nil Live means the pre-Andaru baseline is in /run.
type Pointers struct{ Confirmed, LastKnownGood, Pending, Live *int } // json: confirmed, lastKnownGood, pending, live
type Generation struct {
	Manifest
	State
}

// Dir is <stateDir>/generations.
func Dir(stateDir string) string
// Create allocates the next number, writes <n>/files/<basename> and <n>/manifest.json. It writes no state and no pointer.
func Create(dir string, m Manifest, contents map[string][]byte) (int, error)
// Read and List reconcile the record with the pointers: id == confirmed reads as Confirmed;
// Activating or Awaiting with id != pending reads as RolledBack, reason "interrupted".
func Read(dir string, id int) (Generation, error)          // fs.ErrNotExist when absent; no state.json reads as State{State: Failed, Reason: "never started"}
func List(dir string) (Pointers, []Generation, error)      // newest first; absent dir or pointers.json is zero Pointers and no error; a numbered directory without manifest.json is skipped
func writeAtomic(path string, data []byte, mode fs.FileMode) error
func allowed(path string) bool                              // the 50-andaru regexp from Global Constraints
func (g Generation) verify(dir string) error                // every manifest file exists, is allowed, and matches its sha256
```

`Create` computes each `SHA256` itself, creates `dir` and `<n>` with mode 0700 and files with 0600, numbers from 1 as `max(every numbered directory)+1` using `os.Mkdir` as the claim (retry on `EEXIST`), and refuses a path that is not `allowed` or a mode other than `"0644"`. On any error it removes the directory it claimed.

- [ ] **Step 1:** Write tests:
  - `TestCreateNumbersAndReadsBack`: two `Create` calls give 1 then 2; `Read(1)` returns the manifest with hashes equal to `sha256` of the contents; state is `failed` / `never started`.
  - `TestCreateRefusesForeignPath`: table of `/etc/passwd`, `/run/systemd/network/10-netplan-eth0.network`, `/run/systemd/network/50-andaru-x/../../etc/y.network`, `/run/systemd/system/andaru-pppoe-x.service`, `/run/systemd/network/50-andaru-a.link` — each errors and leaves no numbered directory.
  - `TestVerifyDetectsCorruption`: flip one byte in `1/files/…`; `verify` errors. Delete a file; errors.
  - `TestTornFilesAreErrors`: `pointers.json` containing `{"confirmed":` makes `List` error; `1/state.json` containing `{` makes `Read` error.
  - `TestListIgnoresStrangers`: directories `abc`, `0`, `-1`, a file `7`, plus `lock` and `baseline/` are ignored; result is newest first.
  - `TestListSkipsIncompleteGeneration`: a directory `3/` with no manifest does not break `List`, and the next `Create` returns 4.
  - `TestReadReconcilesWithPointers`: state `awaiting_confirmation` with `confirmed=1` reads `confirmed`; state `awaiting_confirmation` or `activating` with `pending=nil` reads `rolled_back` / `interrupted`.
  - `TestCreateRefusesMode`: mode `0666` or `0755` errors.
- [ ] **Step 2:** `go test ./internal/generation` — FAIL. Implement. PASS.
- [ ] **Step 3:** Commit `feat: store live generations on disk`.

---

### Task 3: Realize — activate and roll back

**Files:** create `internal/generation/realize.go`, `internal/generation/realize_test.go`; modify `internal/host/links_linux.go`, `internal/host/realizer_linux.go` and their tests.

**Interfaces — consumes:** Task 2.

**Interfaces — produces:**

```go
// package host
func ParseLinks(data []byte) ([]LinkStatus, error)               // the existing parseLinks, exported
type Managed struct{ Name, NetworkFile, State string }            // State is AdministrativeState
func ParseManaged(data []byte) ([]Managed, error)                 // entries of networkctl JSON that have a NetworkFile
func ReadManaged(ctx context.Context) ([]Managed, error)          // /usr/bin/networkctl list --json=short

// package generation
var ErrWrongState = errors.New("generation is not in the expected state")

type Realizer struct {
	StateDir   string                                                              // "/var/lib/andaru"
	Root       string                                                              // "" in production; prefix for every path outside StateDir
	Run        func(ctx context.Context, name string, args ...string) ([]byte, error)
	Chown      func(path string, uid, gid int) error                               // os.Chown
	Now        func() time.Time
	VerifyWait time.Duration                                                       // 30 * time.Second
	LockWait   time.Duration                                                       // 70 * time.Second
	failAt     func(step string) error                                             // tests only: called before every pointer write, state write and makePermanent step
}
func (r Realizer) Activate(ctx context.Context, id int) error
func (r Realizer) Rollback(ctx context.Context, id int, reason string) error
func (r Realizer) lock() (unlock func(), err error)                                // LOCK_EX|LOCK_NB retried every 100 ms for LockWait
func (r Realizer) place(from, to *Generation) error                                // files only; nil means the baseline (Task 4), or no files when none was captured
func (r Realizer) settle(ctx context.Context, from, to *Generation) error          // reload, then delete devices
```

`ctx` bounds the work after the lock is taken. Commands `realize` runs through `Run`, exactly: `networkctl reload`; `networkctl delete <name>`; `networkctl list --json=short`; `ip -d -j address show`; `ip -j route show default`.

`place`: delete from `/run/systemd/network` every file of `from` that `to` does not have; write `to`'s files with `writeAtomic` (temporary name prefix `.tmp-`, so networkd never parses it) and their mode. `settle`: `networkctl reload`, whose error is returned; then `networkctl delete` each device whose `50-andaru-<name>.netdev` is in `from` and not in `to`, `<name>` being the basename without prefix and suffix. A failing `delete` is logged and ignored (the device may never have been created).

`Activate`, under the lock:
1. `Read` and `verify` the generation; `ErrWrongState` if it has a `state.json` or `pending` is set.
2. On a takeover, `captureBaseline` (Task 4).
3. Snapshot: physical (`Kind == ""`) managed interfaces with a global IPv4 address, and whether an IPv4 default route uses a managed interface.
4. Write state `activating` with `previous = live`, **then** pointers `pending = id`.
5. `place(live, this)`, `settle`. Then poll once per second up to `VerifyWait` until all hold: for every `.network` path in the manifest some link has that `NetworkFile`, `State == "configured"`, and carries every `Address=` value the file contains; every snapshot interface has a global IPv4 address; a default route exists if one did.
6. Success: write state `awaiting_confirmation` with `deadline = Now() + ConfirmTimeoutSeconds`, **then** pointers `live = id`.
7. Failure, or `ctx` ended: `place(this, previous)` and `settle` under a fresh 20-second context, pointers `pending = nil`, **then** state `failed` with the reason. Return the error.

A change that keeps the file path and has no static address (DHCP to DHCP) cannot be told apart from the old configuration by this check; the timer and the confirmation cover it.

`Rollback`, under the lock: `ErrWrongState`, with no command run, unless `pending == id`. `from` is generation `id`, whatever `live` says. The target is the first that verifies of `previous`, `lastKnownGood`, the captured baseline; with no baseline captured the target is "no files". `place(from, target)`; `settle`, whose error is only appended to the reason; pointers `pending = nil`, `live = target`; **then** state `rolled_back` with `reason`, suffixed ` (restored <what>)` when the target was not `previous`. If a baseline exists but nothing verifies: leave the files, pointers `pending = nil`, state `failed`.

- [ ] **Step 1:** `internal/host`: add fixture `testdata/networkctl-list.json` (captured from the lab with a managed and an unmanaged link) and `TestParseManaged`: only the managed link is returned with its file and state. Rename `parseLinks`. `go test ./internal/host` — FAIL, implement, PASS.
- [ ] **Step 2:** Write `realize_test.go` with a helper that builds a temp `StateDir` and `Root`, and a fake networkd: `Run` records argv; `networkctl reload` schedules the files then on disk under `Root` to become the bound, `configured` set with their `Address=` values **after K further polls** (K settable, default 0); the read commands answer from that model plus settable overrides. `VerifyWait: 2s` with the poll interval injectable down to 10 ms. Tests:
  - `TestActivateSwapsAndAwaits`: live generation 1 (confirmed) with `a.network`; generation 2 has `a.network` (new content) and `vl0.netdev`, `vl0.network`. After `Activate(2)`: files on disk equal generation 2, a foreign `10-other.network` is untouched, argv contains `networkctl reload`, pointers are `pending=2, live=2, confirmed=1`, state is `awaiting_confirmation` with the deadline `Now()+120s`, `previous=1`.
  - `TestActivateWaitsForReload`: generation 2 changes a static `Address=` in the same-named file and the fake applies the reload after 3 polls: `Activate` succeeds only after the new address is reported (at least 4 list calls recorded). With the fake never applying it: `failed`, generation 1 restored.
  - `TestActivateFailsVerificationAndRestores`: fake reports the address gone; result is an error, disk equals generation 1, state `failed`, `pending=nil`, `live=1`. Same for a missing default route, an unbound `.network`, a link stuck in `configuring`, and a failing `networkctl reload`.
  - `TestActivateRefusesSecondPending` and `TestActivateRefusesReuse` (generation already has a state): `ErrWrongState`, nothing on disk changes.
  - `TestActivateRefusesCorruptGeneration`: flipped byte; error before any file is touched.
  - `TestRollbackRestoresPreviousAndDeletesDevices`: after the first test's activation, `Rollback(2, "deadline")` restores generation 1's files, argv contains `networkctl delete vl0`, state `rolled_back`, `pending=nil`, `live=1`.
  - `TestRollbackSurvivesFailingReload`: `Run` fails every `networkctl` call; the files are restored, state is `rolled_back`, the reason mentions the reload error, `pending=nil`.
  - `TestRollbackIsNoOpUnlessPending`: on a `confirmed`, `rolled_back`, `failed` or unknown generation it returns `ErrWrongState` and records no command.
  - `TestRollbackFallsBack`: `previous` corrupt → restores `lastKnownGood`, reason ends `(restored last-known-good)`.
  - `TestFirstApplyWithoutBaselineRollsBackToNothing`: no baseline, `previous=nil`; after `Rollback` the generation's files are gone from `/run`, a foreign file remains, state `rolled_back`.
  - `TestKillPoints`: a table over `failAt` steps of `Activate` (each of its four writes) and `Rollback` (each of its two); after the injected failure run `Rollback(id, "deadline")` and then `Boot` is left to Task 4. Assert for every row: `pending` ends `nil`, `/run` equals generation 1's files, and `Read(id)` is `failed` or `rolled_back`, never `awaiting_confirmation`.
  - `TestLockSerialises`: hold the flock from the test; `Activate` with `LockWait: 300ms` returns a lock error and changes nothing.
- [ ] **Step 3:** `go test ./internal/generation` — FAIL. Implement. PASS, also with `-race`.
- [ ] **Step 4:** Commit `feat: activate and roll back network generations`.

---

### Task 4: Realize — takeover, confirm and boot

**Files:** create `internal/generation/takeover.go`, `internal/generation/takeover_test.go`; modify `realize.go`.

**Interfaces — consumes:** Tasks 2 and 3.

**Interfaces — produces:**

```go
func (r Realizer) Confirm(ctx context.Context, id int) error
func (r Realizer) Boot(ctx context.Context) error       // returns errors for logging; the CLI exits 0 regardless
func (r Realizer) captureBaseline() error                // called by Activate when manifest.Takeover
func (r Realizer) makePermanent(ctx context.Context, reload bool) error
```

Baseline layout under `generations/baseline/`: `manifest.json` (`{"kind":"netplan","files":[{path,mode,uid,gid,sha256}]}`), `files/<sha256>` for the content of each `/run/systemd/network/10-netplan-*`, and `netplan/<name>.yaml` after confirmation. Restoring a baseline file sets its recorded mode and calls `Chown` with its uid and gid. `place` treats a `nil` generation as these files; baseline files are never device-deleted.

`captureBaseline`: when `pointers.confirmed` and `lastKnownGood` are both nil, write the content files, then rename a new `manifest.json` into place (the manifest is the commit; stale content files are harmless). Otherwise leave the capture alone.

`Confirm`, under the lock: `ErrWrongState` unless `pending == id`, the state is `awaiting_confirmation` and `Now()` is not after the deadline. Write pointers `lastKnownGood = confirmed`, `confirmed = id`, `pending = nil`, **then** state `confirmed`. Then, when the manifest says takeover, `makePermanent(ctx, true)`; if it fails, rewrite the state with reason `takeover not yet permanent: <error>; completes at the next boot` and return the error. The confirmation stands either way.

`makePermanent` runs every step even if an earlier one failed, returns the joined errors, and each step is idempotent, in this order:
1. `systemctl enable systemd-networkd.service` (`--no-reload` added when `reload` is false);
2. write `/etc/cloud/cloud.cfg.d/99-andaru-network.cfg` with exactly `network: {config: disabled}\n`, mode 0644, if the directory exists;
3. move each `/etc/netplan/*.yaml` to `baseline/netplan/` (copy, fsync, remove). If the destination exists with different content, keep both by suffixing the new one `.<unix-seconds>`.

`Boot`, under the lock:
1. A `pending` generation: pointers `pending = nil`, then state `rolled_back`, reason `reboot`.
2. No `confirmed`: set `live = nil`, stop.
3. If a baseline manifest of kind `netplan` exists: `makePermanent(ctx, false)`, **logging its error and continuing**; remove `/run/systemd/network/10-netplan-*`.
4. `mkdir -p /run/systemd/network`; `place` the first that verifies of `confirmed`, `lastKnownGood`, baseline files; set `live` accordingly. No `networkctl` call: networkd has not started.
5. Nothing verifies: write nothing, return an error.

- [ ] **Step 1:** Write tests (same helper; `Root` holds `run/systemd/network/10-netplan-eth0.{network,link}` with mode 0640, `etc/netplan/50-cloud-init.yaml`, `etc/cloud/cloud.cfg.d/`):
  - `TestTakeoverCapturesBaselineOnly`: a stale `50-andaru-old.network` is also present; after `Activate` of a takeover generation the baseline manifest lists exactly the two netplan files with their hashes and mode `0640`; no `10-netplan-*` file remains in `/run`; the stale file is neither captured nor removed; `etc/netplan` is unchanged.
  - `TestTakeoverRollbackRestoresBaselineExactly` (Review Focus 3): the baseline manifest is edited to uid 0, gid 192; after `Rollback` the two files are back byte-identical with mode 0640 and the recording `Chown` saw `(path, 0, 192)` for each; `live=nil`.
  - `TestSecondTakeoverAttemptRecaptures`: change the netplan file, activate a new takeover generation; the baseline has the new hash.
  - `TestBaselineFrozenAfterConfirm`: after a confirmed takeover a later takeover-flagged activate does not rewrite the baseline.
  - `TestConfirmMovesPointersAndMakesPermanent`: pointers `confirmed=1, lastKnownGood=nil, pending=nil`; argv has `systemctl enable systemd-networkd.service`; the cloud-init file has the exact content; `etc/netplan` has no YAML and `baseline/netplan/50-cloud-init.yaml` has the original bytes.
  - `TestConfirmRefusals`: after the deadline (advance `Now`), after a rollback, twice, and for a non-pending id — `ErrWrongState`, pointers unchanged.
  - `TestRollbackAfterConfirmDoesNothing` (Review Focus 1): `Rollback` after `Confirm` returns `ErrWrongState`, files and pointers unchanged. `TestRollbackQueuedBehindConfirm`: start `Rollback` in a goroutine while the test holds the lock, run the pointer and state writes of a confirm, release: `Rollback` returns `ErrWrongState`.
  - `TestConfirmKillPoints`: `failAt` between the pointer and state writes of `Confirm`; then `Boot` on a fresh `/run`: the generation is live and reads `confirmed`, not rolled back.
  - `TestBootCompletesInterruptedConfirm`: three sub-tests, `failAt` failing `makePermanent` step 1, 2 and 3 in turn during `Confirm` (which returns an error and records the reason); then a fresh `Root/run` with netplan's files regenerated, and `Boot`: all three steps are done, no `10-netplan-*` remains, the confirmed files are in `/run`, `live=confirmed`.
  - `TestBootContinuesWhenPermanentStepFails`: `systemctl` fails during `Boot`; the confirmed files are still placed.
  - `TestBootBeforeAnyConfirmation`: pending takeover generation → `rolled_back` / `reboot`, `/run` and `/etc/netplan` untouched, `live=nil`.
  - `TestBootFallsBack`: confirmed corrupt → last-known-good copied and `live` says so; both corrupt → baseline files copied; nothing usable → error and nothing written.
  - `TestBootRunsNoNetworkctl`: recorded argv contains no `networkctl`.
- [ ] **Step 2:** `go test -race ./internal/generation` — FAIL. Implement. PASS.
- [ ] **Step 3:** Commit `feat: confirm takeovers and boot from the confirmed generation`.

---

### Task 5: `andaru realize` and the boot unit

**Files:** create `cmd/andaru/realize.go`, `packaging/systemd/andaru-network-boot.service`; modify `cmd/andaru/main.go`, `cmd/andaru/main_test.go`.

**Interfaces — produces:** `andaru realize [--state-dir PATH] activate N | rollback N REASON | confirm N | boot`; `--state-dir` defaults to `/var/lib/andaru`. Arguments are parsed first (usage errors exit 2 for any user), then euid 0 is required (exit 1). Builds `generation.Realizer{StateDir, Root: "", Run: <exec with a 10 s timeout per command, absolute paths /usr/bin/networkctl, /usr/bin/ip, /usr/bin/systemctl>, Chown: os.Chown, Now: time.Now, VerifyWait: 30s, LockWait: 70s}` with the contexts from Global Constraints. The verb logic lives in `runRealize(ctx, r generation.Realizer-like interface, verb, …) int` so exit codes are testable with a fake. Exit codes: 0 done **or** `ErrWrongState` (logged; a late timer must not leave a failed unit); 1 other errors; 2 usage. `boot` logs its error and exits 0.

- [ ] **Step 1:** Tests in `main_test.go`: `realize` with no verb, an unknown verb, `activate` without a number, `activate 0`, `activate 01`, `activate abc`, `rollback 3` without a reason, and a relative `--state-dir` all exit 2 with usage on stderr; the top-level usage line names `realize`. With a fake verb runner: an error from `boot` exits 0, `ErrWrongState` from `rollback` exits 0, another error from `activate` exits 1.
- [ ] **Step 2:** `go test ./cmd/andaru` — FAIL. Implement; the unit file is the spec's text plus `Description=Andaru confirmed network generation` and `cloud-init-local.service` added to `Before=`. PASS.
- [ ] **Step 3:** Commit `feat: add the privileged realize command and boot unit`.

---

### Task 6: Apply gates

**Files:** create `internal/engine/apply_gates.go`, `internal/engine/apply_gates_test.go`; modify `internal/host/realizer_linux.go`, `internal/host/realizer_linux_test.go`.

**Interfaces — consumes:** `render.Result`, `host.Managed`, `host.LinkStatus`, `generation.Pointers`, `generation.Generation`.

**Interfaces — produces:**

```go
// package host
type Baseline struct {
	Netplan   bool     // /usr/sbin/netplan exists
	Ifupdown  bool     // /sbin/ifup exists
	StrayYAML []string // *.yaml under /run/netplan, /lib/netplan, /usr/lib/netplan
	StaticDNS []string // 10-netplan-*.network files in /run/systemd/network with a DNS= or Domains= line
}
func ReadBaseline() (Baseline, error) // readBaseline(root string) underneath, for tests

// package engine
type gateInput struct {
	Takeover    bool
	Declaration json.RawMessage
	Result      render.Result
	Links       []host.LinkStatus
	Managed     []host.Managed
	Facts       host.RealizerFacts
	Baseline    host.Baseline
	Live        *generation.Generation // nil: nothing of Andaru's is live
	LiveFiles   map[string][]byte      // content of Live's files by path
}
// applyFindings returns the findings that refuse this apply; empty means go.
func applyFindings(in gateInput) []render.Finding
```

Rules, all with severity `error`:
- every error finding in `Result.Findings`, except `FOREIGN_CONFIGURATION` when `Takeover` and the baseline is recognised;
- `TAKEOVER_UNSUPPORTED_BASELINE` for `/sbin/ifup` whenever `Ifupdown`, with or without `Takeover`;
- recognised baseline: `Netplan && !Ifupdown`, `len(StrayYAML) == 0`, `len(StaticDNS) == 0`, and every `Facts.ForeignFiles` entry matches the baseline regexp. With `Takeover` and not recognised: `TAKEOVER_UNSUPPORTED_BASELINE`, one finding per reason, `path` the offending file;
- `TAKEOVER_MISMATCH`: `Takeover` with no foreign files, or `!Takeover` with at least one `10-netplan-*` foreign file;
- `APPLY_UNSUPPORTED`: for each enabled link of kind `pppoe` (`/links/N`), each segment with `addressing.ipv6.mode == "delegated"` (`/segments/N/addressing/ipv6/mode`), each with `ra.enabled` (`/segments/N/addressing/ipv6/ra/enabled`). Decode with a local struct for just these fields;
- `WOULD_UNCONFIGURE`: for each `Managed` whose link has `Kind == ""`, a `NetworkFile` under `/run/systemd/network/`, and a global address of either family, when `Result.Files` has no `/run/systemd/network/50-andaru-<Name>.network`. `path` is the interface name;
- `NETDEV_CHANGE_UNSUPPORTED`: a `.netdev` path present in both `LiveFiles` and `Result.Files` with different content. `path` is the file path.

- [ ] **Step 1:** `host`: `TestReadBaseline` over a temp root — netplan binary present, `ifup` absent, `PLACEHOLDER` files ignored, a `run/netplan/x.yaml` reported, a `10-netplan-eth0.network` with `DNS=1.1.1.1` reported, one with only `DHCP=ipv4` not.
- [ ] **Step 2:** `engine`: one table test `TestApplyFindings`, a row per rule above asserting the exact code and path, plus:
  - the lab's good case (netplan baseline, one DHCP Ethernet declared, takeover) → empty;
  - **empty declaration with takeover** on that host → `WOULD_UNCONFIGURE` for `eth0` (Review Focus 2);
  - a `podman0` bridge with a global address that networkd does not manage → no finding;
  - a VLAN from the live generation, with an address, absent from the new one → no finding;
  - a `HARDWARE_MISSING` error is passed through unchanged;
  - `FOREIGN_CONFIGURATION` survives when a foreign file is `/etc/systemd/network/x.network` even with takeover.
- [ ] **Step 3:** `go test ./internal/host ./internal/engine` — FAIL. Implement. PASS. Commit `feat: gate network applies`.

---

### Task 7: Applies API

**Files:** create `internal/engine/applies_api.go`, `internal/engine/runner_linux.go`, `internal/engine/applies_api_test.go`; modify `api.go`, `preview_api.go`, `socket_linux.go`, `openapi.json`, `api_test.go`, and the integration tests that construct handlers.

**Interfaces — consumes:** Tasks 2, 3 (types and `Read`/`List` only), 6; `config.Store.RealizationInput`.

**Interfaces — produces:**

```go
type realizeRunner interface {
	Arm(ctx context.Context, id, seconds int) error
	Disarm(ctx context.Context, id int) error
	Realize(ctx context.Context, verb string, id int, extra ...string) error
}
type hostReader struct {
	Links    func(context.Context) ([]host.LinkStatus, error)
	Facts    func() (host.RealizerFacts, error)
	Managed  func(context.Context) ([]host.Managed, error)
	Baseline func() (host.Baseline, error)
}
func newAppliesHandler(store *config.Store, read hostReader, runner realizeRunner, stateDir string) http.Handler
func newSystemdRunner(stateDir string) realizeRunner
```

`systemdRunner` argv, exact (binary `/usr/bin/systemd-run`, `/usr/bin/systemctl`). The timer's service and the on-demand units must never share a name, or systemd refuses the second:
- `Arm`: `systemd-run --quiet --collect --unit=andaru-rollback-<n> --on-active=<seconds> --timer-property=AccuracySec=1s -p RuntimeMaxSec=180 /usr/bin/andaru realize --state-dir <dir> rollback <n> deadline`
- `Realize`: `systemd-run --quiet --wait --collect --unit=andaru-realize-<verb>-<n> -p RuntimeMaxSec=180 /usr/bin/andaru realize --state-dir <dir> <verb> <n> [extra…]`
- `Disarm`: `systemctl stop andaru-rollback-<n>.timer`; an error is logged, not returned.

Responses. An apply is `{"id":"3","savedVersion":"7","takeover":true,"state":"awaiting_confirmation","previous":null,"deadline":"…","reason":"","createdAt":"…","confirmTimeoutSeconds":120,"files":[{"path":"…","mode":"0644"}]}`; the list is `{"pointers":{"confirmed":…,"lastKnownGood":…,"pending":…,"live":…},"items":[…]}` with ids as strings or `null`. `errorBody` gains `Findings []render.Finding` with `json:"findings,omitempty"`.

| Request | Outcome |
| --- | --- |
| `POST /api/v1/applies`, body over 4 KiB, unknown field, missing or malformed `savedVersion`, timeout outside 30–600 | `400 INVALID_REQUEST` |
| `savedVersion` is not the head | `412 PRECONDITION_FAILED` |
| `pointers.pending` set | `409 APPLY_IN_PROGRESS` |
| gate findings | `422 APPLY_REFUSED` with `findings`; no generation directory is created |
| host or store unreadable | the preview handler's `503` codes |
| `Arm` fails | `503 REALIZE_UNAVAILABLE`; state stays "never started" |
| activation ran | `200` with the apply, whether `awaiting_confirmation` or `failed`; on `failed` the handler calls `Disarm` |
| `Realize` returned an error and the generation still reads "never started" (lock timeout, `systemd-run` failure) | `Disarm`, `503 REALIZE_UNAVAILABLE` |
| `GET /api/v1/applies`, `GET /api/v1/applies/{id}` | `200`; an id not matching `^[1-9][0-9]{0,8}$` or unknown is `404 NOT_FOUND` |
| `POST …/{id}/confirmation` | `Realize("confirm")`, re-read: `confirmed` → `Disarm`, `200` (a `reason` reports a takeover that is not yet permanent); otherwise `409 APPLY_NOT_AWAITING` with the state in the message |
| `POST …/{id}/rollback` | `Realize("rollback", id, "requested")`, re-read: `rolled_back` → `Disarm`, `200`; otherwise `409 APPLY_NOT_AWAITING` |
| other methods | `405` with `Allow` |

The POST handlers run under `context.WithoutCancel(r.Context())` with a 100-second timeout and a handler-wide mutex. OpenAPI version becomes `0.4.0-dev`.

- [ ] **Step 1:** Write `applies_api_test.go` with a fake runner whose `Realize` drives a real `generation.Realizer` over a temp root (fake `Run` as in Task 3), a real `config.Store` in a temp dir as the existing engine tests build it, and a fixed `hostReader`. One test per table row, plus:
  - `TestApplyHappyPath`: `200`, state `awaiting_confirmation`, runner calls in order `Arm(1, 180)`, `Realize(activate, 1)`; `GET` returns the same apply; `files` carry no `content`.
  - `TestApplyRefusedStoresNothing`: `422`, the `generations` directory has no numbered entry, the runner saw no call.
  - `TestApplyFailedActivationDisarms`: `200` with `failed`, runner saw `Disarm(1)`.
  - `TestApplySurvivesClientDisconnect` (Review Focus 4): the fake `Realize` blocks, then returns `ctx.Err()` if its context was cancelled; cancel the request context while it blocks; release it; `GET` shows `awaiting_confirmation`.
  - `TestConfirmThenTimerIsHarmless`: confirm → `200`; then call the fake's `Realize(rollback, id, "deadline")` directly → state still `confirmed`.
  - `TestMalformedIDs` (Review Focus 5): `/applies/0`, `/-1`, `/+1`, `/01`, `/abc`, `/99999999999999999999`, `/1/unknown` → `404`.
  - `TestAppliesRequireRoot`: a non-root peer gets `403` and neither store, host reader nor runner is called.
  - `TestSystemdRunnerUnitNamesAreDistinct`: with the exec function replaced, collect `--unit=` from `Arm(3, …)` and `Realize` for `activate`, `confirm` and `rollback` on id 3: four different names; `Arm`'s argv has `--on-active=`, `AccuracySec=1s` and ends `rollback 3 deadline`.
  - Extend the existing OpenAPI contract test: the five operations exist and `info.version` is `0.4.0-dev`.
- [ ] **Step 2:** `go test ./internal/engine` — FAIL. Implement. Wiring: add `/api/v1/applies` and its prefix to the path condition at `api.go:52`; `newSavedHandler` gains an `applies http.Handler` parameter and dispatches that prefix to it; `Serve` builds it with `newSystemdRunner(stateDir)` and the real readers; update the tests that call `newSavedHandler`. PASS; `go vet -tags=integration ./...` clean.
- [ ] **Step 3:** Commit `feat: serve confirmed network applies`.

---

### Task 8: CLI

**Files:** modify `cmd/andaru/ctl.go`, `cmd/andaru/main.go`, `cmd/andaru/main_test.go`, `cmd/andaru/ctl_integration_linux_test.go`.

**Interfaces — produces:**

```
andaru ctl apply network --saved-version N [--takeover] [--confirm-timeout S]
andaru ctl applies list | get ID | confirm ID | rollback ID
```

`ctlRequest` gains a literal `payload []byte` (the apply body is built from flags, not stdin). `--saved-version` is required. The client timeout for `apply`, `confirm` and `rollback` is 110 seconds. Envelope kinds `apply` (id and state present, files non-nil) and `applies` (pointers and items present). For `apply network`: state `awaiting_confirmation` → exit 0 and stderr gets exactly

```
Live until <deadline>. From a NEW session, run: andaru ctl applies confirm <id>
```

state `failed` → the body on stdout, `apply failed: <reason>` on stderr, exit 1. `applies confirm` prints a non-empty `reason` on stderr. For the apply verbs only, the connection-error message is `response unavailable; outcome may be unknown; run: andaru ctl applies list`; configuration saves keep theirs.

- [ ] **Step 1:** Tests in `main_test.go` against the existing fake-server helper: flag parsing rows (missing `--saved-version`, `--saved-version ""`, `--confirm-timeout abc`, extra argument, `applies get` without id, `applies confirm x` → exit 2); the request body for `apply network --saved-version 7 --takeover --confirm-timeout 60` is `{"savedVersion":"7","takeover":true,"confirmTimeoutSeconds":60}`; the stderr line above; exit 1 on `failed`; `applies confirm 3` posts to `/api/v1/applies/3/confirmation`.
- [ ] **Step 2:** `go test ./cmd/andaru` — FAIL. Implement; update `errUsage` and the top-level usage. PASS.
- [ ] **Step 3:** Commit `feat: add apply and applies commands`.

---

### Task 9: Guide, lab qualification, review, PR

**Files:** modify `docs/dev/core-api.md`, `docs/dev/vm-lab.md`, `README.md`.

- [ ] **Step 1:** Guide: the apply flow in the order an operator meets it (preview, apply, confirm from a new session, what the timer does), the takeover and its limits from the spec, the manual undo procedure, installing and enabling `andaru-network-boot.service`, and that the lab proofs below are not in CI.
- [ ] **Step 2:** Full local gate: `go build ./... && go test -race ./... && go vet -tags=integration ./...`, and the root integration suites as CI runs them.
- [ ] **Step 3:** Lab: snapshot `pre-takeover-<timestamp>`; install the binary, the engine unit and the boot unit (enabled); save a declaration with the lab NIC (`bc:24:11:89:01:10`) as one DHCPv4 Ethernet uplink. First prove the three assumptions no unit test can: `systemd-run --wait` returns the command's exit status; an early `applies rollback` starts while the timer is armed; right after `networkctl reload` of a changed static address the link does not report `configured` with the old address set (if it does, add a one-second settle before the first poll, with a test). Then run the spec's acceptance items 8–16 in order, with `ping -i 0.2 10.0.1.19` running from this host and the guest agent for observation only. Every confirmation is sent over a new `ssh andaru-lab` connection. For each item record the command, the apply JSON, `networkctl status`, `ip -br a`, `ls -l /run/systemd/network`, `systemd-analyze verify` output for the boot unit, and the ping loss count in `docs/dev/vm-lab.md`. A failed item is fixed with a failing unit test first, then the item is rerun from the snapshot.
- [ ] **Step 4:** One condensed adversarial `reviewer` round on the whole branch; fix findings with tests.
- [ ] **Step 5:** Refresh the graph, push, open the PR against `main`, register it with pr-watcher, and after green CI request Codex and CodeRabbit reviews.

## Known limits carried into the code

- The last-resort boot fallback copies the netplan baseline files, but they match `Name=eth0` and the name is no longer pinned once udev has run; that fallback may come up unconfigured. It exists so the host has the best available files, and the outcome is console recovery as the spec states.
- `Disarm` failing leaves a timer that will fire and do nothing.
- **Execution:** Native is recommended. Tasks 2–4 share one set of types and one test helper, and Task 9's lab run needs the session's lab access.
