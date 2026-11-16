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
- Tests: account restart/limit/login/tombstone/CAS/service/invariant; empty display name and custom-role create/update/restart; password policy/hash/bounds/rehash/revocation; admission saturation/timeout/cancellation.

## Task 2B — tokens, sessions, and bounded login protection

- Files: `internal/identity/token.go`, `session.go`, `limit.go`, `token_session_limit_test.go`.
- Interfaces: token issuance/list/revoke/authentication, password/session/token authentication, CSRF validation/rotation, logout.
- Tests: token/scope/expiry/revocation/restart, session epochs/timeouts/eviction/CSRF/restart, login buckets/backoff/expiry/saturation.

## Round 1 persistence fixes

- SQLite path now uses `url.URL` and encoded `url.Values`, with explicit `mode=rw` to avoid opening a different file.
- DB, WAL, and SHM members are checked as regular root-owned 0600 single-link files before and after open.
- Existing stores require the exact application ID and layout v1; required schema tables and limiter-key row are verified, as are WAL, FULL sync, foreign-key, and secure-delete pragmas.
- Limiter key is created by `randomblob(32)` inside the initial schema transaction. The synthetic v1→v2 migration and old-layout fixtures were removed; there was no released v1 schema.
- Added real SQLite tests for invalid established layouts/schema/key, unsafe sidecars, wrong app, restart, and retry after failed initialization. Tests not run per assignment.

## Deferred verification

Not run per assignment: `go test -tags=integration ./internal/identity -count=1`, `go test -race ./internal/identity`, formatting, lint, and build checks.

## Concerns

No checks run in this fix round. No language-server references check was available. Future backup code must exclude limiter key/state and sessions; HTTP/cookie attributes and audit integration remain outside scope.
