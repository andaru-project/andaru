# Task 2A1 report

Implemented files:
- `internal/identity/model.go`: public account, role, credential/token/session input types and store-facing error/interface.
- `internal/identity/sqlite.go`: fixed application ID and normalized schema for accounts, tombstones, roles, password credentials, API tokens, sessions, login limits, metadata/revisions, and operation idempotency.
- `internal/identity/store_linux.go`: root-only secure SQLite open, WAL/FULL/foreign-key/secure-delete configuration and validation, initialization/version refusal, close, revision read.
- `internal/identity/store_linux_test.go`: real SQLite restart, schema, revision, identifiers, and wrong-application refusal.

Interfaces: `Open(ctx,stateDir)`, `Close()`, and `Revision(ctx)`; SQL rows remain private.

Tests added but not run (per assignment): `TestOpenRestartAndSchema`, `TestOpenRefusesWrongApplication`.

Deferred checks: `go test -tags=integration ./internal/identity -count=1`, `go test -race ./internal/identity`, format/lint/build.

Concerns: migration currently supports only fresh layout v1 and refusal of future versions; transactional framework is established for later forward migrations. Account business behavior and credential/token/session/limiter operations remain intentionally out of scope.
