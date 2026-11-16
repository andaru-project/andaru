# Task 2 report

## Status

Task 2A1 and Task 2A2 are implemented. Task 2B (tokens, sessions, and login limiting) remains outside these slices.

## Task 2A1 — database foundation

- Files: `internal/identity/model.go`, `sqlite.go`, `store_linux.go`, `store_linux_test.go`.
- Interfaces: root-only `Open(ctx,stateDir)`, `Close()`, `Revision(ctx)`; fixed application/layout IDs, secure SQLite setup, normalized identity tables.
- Tests: `TestOpenRestartAndSchema`, `TestOpenRefusesWrongApplication` (written, not run).

## Task 2A2 — accounts, passwords, admission

- Files: `internal/identity/store_linux.go`, `model.go`, `sqlite.go`, `password.go`, `account_password_test.go`, embedded password blocklist/provenance, `go.mod`.
- Interfaces: bootstrap/create/update/get/list/delete/disable account methods with sorted Task 1 built-in/custom role IDs; password set/verify; reusable administrator invariant validator. Mutations use the 2A1 global revision CAS, tombstone deleted IDs, increment password epochs, and revoke dependent sessions/tokens transactionally; role assignment changes invalidate sessions.
- Tests: account restart/limit/login/tombstone/CAS/service/invariant; empty display name and custom-role create/update/restart; password policy/hash/bounds/rehash/revocation; admission saturation/timeout/cancellation (written, not run).

## Deferred verification

Not run per assignment:

- `go test -tags=integration ./internal/identity -count=1`
- `go test -race ./internal/identity`
- formatting, lint, and build checks

## Concerns

- Follow-up restored the test file's missing `testing` import reported by the parent compile gate. Checks remain deferred; no passing result is claimed.
