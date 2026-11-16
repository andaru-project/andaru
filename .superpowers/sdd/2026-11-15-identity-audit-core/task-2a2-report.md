# Task 2A2 report

## Status

Implemented account/password/admission behavior in the identity store.

## Files

- `internal/identity/store_linux.go`: bootstrap/create/update/get/list/delete/disable account operations, role assignment persistence and session invalidation on role changes, revision CAS, account limit and tombstones, administrator invariant, password mutation epoch, transactional session/token revocation hooks, password verification and low-cost CAS rehash.
- `internal/identity/model.go`: typed account inputs/errors and expanded `Store` behavior contract, including sorted role IDs.
- `internal/identity/sqlite.go`: corrected unreleased v1 schema in place for empty display names and bounded built-in/custom role IDs in `account_roles.role_id`; no migration is needed.
- `internal/identity/password.go`: Argon2id v19 policy/hash parser, password validation, shared bounded admission, constant-time verify.
- `internal/identity/testdata/password-blocklist-v1.txt` and `password-blocklist-v1-provenance.txt`: embedded offline v1 denylist and provenance.
- `internal/identity/account_password_test.go`: real SQLite account/restart/role/cap/tombstone/CAS/invariant/password-epoch/revocation tests, plus password/admission tests.
- `go.mod`: corrected two `require` blocks and promoted existing `golang.org/x/crypto` and `github.com/google/uuid` dependencies; no new dependency or `go.sum` change.

## Interfaces

- `Store` now exposes `BootstrapAdmin`, `CreateAccount`, `UpdateAccount`, `SetPassword`, `GetAccount`, `ListAccounts`, `DeleteAccount`, `DisableAccount`, `ValidateAdministratorInvariant`, and `VerifyPassword`.
- Account mutations use the 2A1 global revision as their CAS token and return the resulting revision where applicable. Role IDs accept the Task 1 built-ins or canonical custom `rol_<UUIDv4>` IDs, are bounded/sorted, and persist across create/update/restart.
- The recovery invariant uses Task 1's immutable `authz.AdministratorRoleID`, independently of custom role assignments.
- Password hashing uses the fixed 65,536 KiB / time 3 / parallelism 1 policy, 16-byte random salt, 32-byte output. Stored costs are bounded before Argon2 allocation. Real, dummy, and rehash work share a 2-active/16-queued/5-second gate.

## Tests added

- `TestBootstrapCreateUpdateAndRestart`
- `TestAccountValidationTombstoneCASAndInvariant`
- `TestLoginCanonicalization`
- `TestOptionalDisplayAndCustomRolesSurviveRestart`
- `TestAccountLimitAtDatabaseBoundary`
- `TestStoreExposesReusableAdministratorInvariant`
- `TestPasswordMutationEpochAndTransactionalRevocationHooks`
- `TestPasswordPolicyAndHashing`
- `TestSuccessfulVerificationRehashesLowCostCredentialByCAS`
- `TestAdmissionRejectsExcessQueueAndHonorsCancellation`

## Deferred commands

Not run per assignment:

- `go test -tags=integration ./internal/identity -count=1`
- `go test -race ./internal/identity`
- `gofmt`/lint/build checks

## Concerns

- Follow-up restored the `testing` import reported missing by the parent compile gate; checks remain deferred, with no passing result claimed.
