# Task 2 report

## Status

Task 2A1 and Task 2A2 are implemented. Task 2B (tokens, sessions, and login limiting) remains outside these slices.

## Task 2A1 — database foundation

- Files: `internal/identity/model.go`, `sqlite.go`, `store_linux.go`, `store_linux_test.go`.
- Interfaces: root-only `Open(ctx,stateDir)`, `Close()`, `Revision(ctx)`; fixed application/layout IDs, secure SQLite setup, normalized identity tables.
- Tests: `TestOpenRestartAndSchema`, `TestOpenRefusesWrongApplication` (written, not run).

## Task 2A2 — accounts, passwords, admission

- Files: `internal/identity/store_linux.go`, `model.go`, `password.go`, `account_password_test.go`, embedded password blocklist/provenance, `go.mod`.
- Interfaces: bootstrap/create/update/get/list/delete/disable account methods; password set/verify; reusable administrator invariant validator. Mutations use the 2A1 global revision CAS, tombstone deleted IDs, increment password epochs, and revoke dependent sessions/tokens transactionally.
- Tests: account restart/limit/login/tombstone/CAS/service/invariant; password policy/hash/bounds/rehash/revocation; admission saturation/timeout/cancellation (written, not run).

## Deferred verification

Not run per assignment:

- `go test -tags=integration ./internal/identity -count=1`
- `go test -race ./internal/identity`
- formatting, lint, and build checks

## Concerns

- The 2A1 role table permits only its fixed role strings; custom role assignment persistence is not introduced in 2A2. The approved design also calls display names optional, while the existing 2A1 schema requires a non-empty display name. Resolve these through an explicit forward schema decision without silently changing the 2A1 open contract.
- Test/build/format verification remains deferred; no passing result is claimed.
