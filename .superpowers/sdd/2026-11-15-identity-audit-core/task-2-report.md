# Task 2 report

## Status

Task 2A1, Task 2A2, and Task 2B are implemented.

## Task 2A1 — database foundation

- Files: `internal/identity/model.go`, `sqlite.go`, `store_linux.go`, `store_linux_test.go`.
- Interfaces: root-only `Open(ctx,stateDir)`, `Close()`, `Revision(ctx)`; fixed application/layout IDs, secure SQLite setup, normalized identity tables.
- Tests: `TestOpenRestartAndSchema`, `TestOpenRefusesWrongApplication` (written, not run).

## Task 2A2 — accounts, passwords, admission

- Files: `internal/identity/store_linux.go`, `model.go`, `sqlite.go`, `password.go`, `account_password_test.go`, embedded password blocklist/provenance, `go.mod`.
- Interfaces: bootstrap/create/update/get/list/delete/disable account methods with sorted Task 1 built-in/custom role IDs; password set/verify; reusable administrator invariant validator. Mutations use the 2A1 global revision CAS, tombstone deleted IDs, increment password epochs, and revoke dependent sessions/tokens transactionally; role assignment changes invalidate sessions.
- Tests: account restart/limit/login/tombstone/CAS/service/invariant; empty display name and custom-role create/update/restart; bounded paged listing; password policy/hash/bounds/rehash/revocation; admission saturation/timeout/cancellation and concurrent wakeup contention.

## Task 2B — tokens, sessions, and bounded login protection

- Files: `internal/identity/token.go`, `session.go`, `limit.go`, `token_session_limit_test.go`.
- Interfaces: token issuance/list/revoke/authentication, password/session/token authentication, CSRF validation/rotation, logout.
- Tests: token/scope/expiry/revocation/restart, session epochs/timeouts/eviction/CSRF/restart, login buckets/backoff/expiry/saturation.

## Round 1 persistence fixes

- SQLite path now uses `url.URL` and encoded `url.Values`, with explicit `mode=rw` to avoid opening a different file.
- DB, WAL, and SHM members are checked as regular root-owned 0600 single-link files before and after open.
- Existing stores require the exact application ID and layout v1; required schema tables and limiter-key row are verified, as are WAL, FULL sync, foreign-key, and secure-delete pragmas.
- Limiter key is created by `randomblob(32)` inside the initial schema transaction. The synthetic v1→v2 migration and old-layout fixtures were removed; there was no released v1 schema.
- Follow-up: seeded the revision metadata row in the same initializer transaction and made schema verification reject missing initial revision/limiter rows.
## Round 1 account/password fixes

- Admission now uses a nonblocking permit send under the queue mutex, eliminating the len-check/send race; `TestAdmissionConcurrentWakeupsDoNotDeadlock` races queued and new callers across repeated drains.
- `ListAccounts` uses bounded keyset SQL pagination (`afterLogin`, maximum 100), then loads role rows in one batch for that page only.
- `BenchmarkArgon2ProductionAdmission` keeps the exact production Argon2 policy and reports latency, observed concurrent hashes, and estimated peak Argon workspace memory.
- `TestArgon2ProductionPolicyQualification` synchronizes exactly two hashes through the real gate, verifies max-active=2, samples `runtime.MemStats.HeapAlloc` above a pre-run GC baseline, and logs measured peak delta plus mean latency/wall time using v19, 65,536 KiB, t=3, p=1.
- Expected qualification command: `go test -tags=integration ./internal/identity -count=1`; expected test log includes `max-active=2`, measured heap delta (about two 64 MiB workspaces), mean hash latency, and total wall time.
- `TestBootstrapCreateUpdateAndRestart` now advances from the last returned login to prove the terminal page is empty; an empty cursor remains the first-page cursor.
- No tests, build, lint, formatting, or benchmark commands were run.


## Deferred verification

Not run per assignment: `go test -tags=integration ./internal/identity -count=1`, `go test -race ./internal/identity`, `go test -run '^$' -bench '^BenchmarkArgon2ProductionAdmission$' -benchmem ./internal/identity`, formatting, lint, and build checks.

## Concerns

No checks run in this fix round. No language-server references check was available. Future backup code must exclude limiter key/state and sessions; HTTP/cookie attributes and audit integration remain outside scope.

## Round 1 token/session/limiter fixes

- Wrong session secrets now fail without deleting the valid session; cleanup remains for independently expired or corrupt rows. The regression verifies the original secret still authenticates.
- Token and session selector/secret inputs are checked against exact unpadded base64url lengths before decoding.
- The 15-minute failure window keeps its first-failure timestamp across attempts and resets only after expiry; the regression includes failures spaced ten minutes apart.
- Security tests now isolate target-scope intersection, revoke a live session on password change, and seed recent/stale `lastUsedAt` timestamps to verify hourly throttling.
- Parent-reported issues were fixed; checks remain deferred to the parent.

## Formatting follow-up

- Applied only `gofmt`-equivalent layout changes to `internal/identity/account_password_test.go` and `internal/identity/model.go`; `gofmt -d` then produced no output.
- No semantic changes or test/build checks were performed.

## Fresh initializer correction

- Diagnosis: the schema transaction seeded `metadata('revision',0)`, `limiter_key(1,randomblob(32))`, and `limiter_overload(1,0,0)`, but it never seeded `revisions(revision=0)`. `verifySchema` correctly queried that table and rejected every fresh database with `identity initial revision missing`; the metadata key/value and revision-history row are separate records with separate identifiers.
- Fix: seed `revisions(0,unixepoch())` in the same schema DDL transaction. This preserves atomic creation of schema, revision metadata/history, limiter key, and overload state. Existing-layout verification still fails closed if any required row is deleted; no legacy-layout migration was added.
- Test coverage: existing `TestOpenRestartAndSchema` opens a fresh DB and therefore catches this mismatch; no test changes were needed. Checks were not run per assignment.

## Parent verification after round 1

- `go test -tags=integration ./internal/identity -count=1`: PASS.
- `go test -race ./internal/identity`: PASS.
- `gofmt -l internal/identity/*.go`: clean.
- `go test ./internal/identity -run '^$' -bench '^BenchmarkArgon2ProductionAdmission$' -benchtime=1x -benchmem`: PASS; amd64/i5-9500T measured 223.2 ms/op, 64 MiB peak estimate for one observed active hash, 67,118,752 B/op.

## Persistence re-review fixes

- Established-layout verification now compares the normalized `sqlite_master` DDL for every required table and explicit index to the fixed initializer layout. This covers column names/types/nullability/defaults/primary keys, STRICT, constraints, foreign keys, and indexes; missing, extra, or altered table/index definitions fail closed. Seed checks explicitly require revision metadata/history, limiter key, and overload state.
- Fresh databases are initialized in a private unique staging file in the state directory. Initialization is verified there, the DB is closed, and `renameat2(RENAME_NOREPLACE)` atomically publishes it as `identity.db`; the state directory is synced after publication. Failed initialization cleans staging files and never leaves a zero-version final DB. Existing database files are never treated as retryable staging state.
- `TestOpenRejectsInvalidEstablishedDatabase` now includes malformed columns, missing CHECK constraints, and a missing required index. `TestOpenInitializationFailureIsRetryable` injects an error before initialization commit, confirms no final DB was published, then retries through `Open` without deleting files.
- No tests/build/lint/format/graph checks were run per assignment.
## Round 2 rolling failure window

- Login and source limiter rows now store a fixed 40-byte, five-timestamp failure window. Initial schema and strict layout verification share the same DDL; no migration was added for the unreleased layout.
- Failure processing prunes timestamps at or before `now-900`, keeps the newest five, and applies existing exponential backoff when five failures remain in the trailing 15 minutes. Empty windows reset backoff state.
- `TestRollingFailureWindowBoundaryTimestamps` exercises offsets 0, 898, 899, 900, 901, and 902, verifies threshold timing, then confirms reset after expiry. Existing saturation fixtures now seed valid timestamp windows.
- Deferred: parent reruns `go test -tags=integration ./internal/identity -count=1`, `go test -race ./internal/identity`, benchmark, and formatting/lint/build checks. None were run in this fix.
- Concerns: no schema migration is included; Task 2B HTTP/cookie serialization and later-task work remain out of scope.

## Round 2 Argon qualification follow-up

- Fixed the qualification test's active-counter channel type; no production change.
- `gofmt -d internal/identity/account_password_test.go` produced no output.
- Expected parent command: `go test -tags=integration ./internal/identity -count=1`; qualification logs `max-active=2`, measured heap delta, mean hash latency, and wall time.
- No tests or builds were run in this follow-up.
