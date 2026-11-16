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
- Follow-up: removed duplicate limiter table declarations; limiter table and key are initialized once within the atomic schema transaction.
## Round 1 account/password fixes

- Admission now uses a nonblocking permit send under the queue mutex, eliminating the len-check/send race; `TestAdmissionConcurrentWakeupsDoNotDeadlock` races queued and new callers across repeated drains.
- `ListAccounts` uses bounded keyset SQL pagination (`afterLogin`, maximum 100), then loads role rows in one batch for that page only.
- `BenchmarkArgon2ProductionAdmission` keeps the exact production Argon2 policy and reports latency, observed concurrent hashes, and estimated peak Argon workspace memory.
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
