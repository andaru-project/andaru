# Task 2A2 report

## Status

Implemented account/password/admission behavior in the identity store.

## Files

- `internal/identity/store_linux.go`: bootstrap/create/update/get/list/delete/disable account operations, revision CAS, account limit and tombstones, administrator invariant, password mutation epoch, transactional session/token revocation hooks, password verification and low-cost CAS rehash.
- `internal/identity/model.go`: typed account inputs/errors and expanded `Store` behavior contract.
- `internal/identity/password.go`: Argon2id v19 policy/hash parser, password validation, shared bounded admission, constant-time verify.
- `internal/identity/testdata/password-blocklist-v1.txt` and `password-blocklist-v1-provenance.txt`: embedded offline v1 denylist and provenance.
- `internal/identity/account_password_test.go`: real SQLite account/restart/cap/tombstone/CAS/invariant/password-epoch/revocation tests, plus password/admission tests.
- `go.mod`: promoted existing `golang.org/x/crypto` and `github.com/google/uuid` dependencies to direct imports; no new dependency or `go.sum` change.

## Interfaces

- `Store` now exposes `BootstrapAdmin`, `CreateAccount`, `UpdateAccount`, `SetPassword`, `GetAccount`, `ListAccounts`, `DeleteAccount`, `DisableAccount`, `ValidateAdministratorInvariant`, and `VerifyPassword`.
- Account mutations use the 2A1 global revision as their CAS token and return the resulting revision where applicable.
- The invariant uses Task 1's `authz.AdministratorRoleID`; identity persistence remains the 2A1 database/schema.
- Password hashing uses the fixed 65,536 KiB / time 3 / parallelism 1 policy, 16-byte random salt, 32-byte output. Stored costs are bounded before Argon2 allocation. Real, dummy, and rehash work share a 2-active/16-queued/5-second gate.

## Tests added

- `TestBootstrapCreateUpdateAndRestart`
- `TestAccountValidationTombstoneCASAndInvariant`
- `TestLoginCanonicalization`
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

- The existing 2A1 `account_roles` schema is constrained to its fixed role strings; this slice uses Task 1's immutable administrator ID for the recovery invariant but does not add custom-role assignment persistence. The approved design also calls display names optional, while the 2A1 schema requires a non-empty display name. Both need an explicit forward-schema decision to change without violating the instruction to preserve the 2A1 schema/open contract.
- Tests and formatting/build checks are intentionally deferred; behavior is not claimed verified by execution.
