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
- Tests: account restart/limit/login/tombstone/CAS/service/invariant; empty display name and custom-role create/update/restart; password policy/hash/bounds/rehash/revocation; admission saturation/timeout/cancellation (written, not run).

## Deferred verification

Not run per assignment:

- `go test -tags=integration ./internal/identity -count=1`
- `go test -race ./internal/identity`
- formatting, lint, and build checks

## Concerns

- Follow-ups corrected the missing test import and aligned the deletion assertion with `DeleteAccount`'s error-only signature. Checks remain deferred; no passing result is claimed.

## Task 2B — tokens, sessions, and bounded login protection

- Files: `internal/identity/token.go`, `session.go`, `limit.go`, `token_session_limit_test.go`; integration updates to `model.go`, `sqlite.go`, `store_linux.go`, `account_password_test.go`, and `store_linux_test.go`.
- Interfaces: `TokenIssue`, `Token`, `IssuedToken`, `LoginResult`, and `Principal`; `Store` now exposes token issue/list/revoke/authentication, password/session/token authentication, CSRF validation/rotation, and logout. Existing account lookup and `VerifyPassword`/Argon admission behavior are reused.
- Tokens: 128-bit selector plus independent 256-bit secret, SHA-256 secret hash, one-time secret return, issuer/target scope intersection, request-time role intersection, 50 active and 100/year limits, 90-day default/one-year maximum/explicit no-expiry, password-change revocation defaults and override, hourly last-used throttling, and disable/delete invalidation.
- Sessions: separate random 256-bit ID/CSRF selectors and secrets with stored hashes, 30-minute idle and 12-hour absolute expiry, transactional five-session oldest eviction, account/password/authorization epoch validation, CSRF rotation, durable restart behavior, and idempotent logout.
- Login protection: persistent local HMAC login keys before account lookup, identical dummy-Argon failure paths, rolling 15-minute failures with exponential backoff capped at 15 minutes, exact source and IPv6 `/64` buckets, 10,000/50,000 row caps, 24-hour source expiry, and durable bounded overload state.
- Tests written against real SQLite: `TestTokenScopeExpiryRevocationAndRestart`, `TestTokenActiveAndAnnualLimits`, `TestSessionEpochsTimeoutEvictionCSRFAndRestart`, `TestPasswordLoginBucketsIPv6AndCancellation`, `TestLoginBackoffThresholdCapAndRollingExpiry`, `TestLimiterRowsExpireSaturateAndSurviveRestart`, `TestOpenMigratesV1AndDropsUnusableSessions`, and `TestOpenRefusesMissingLimiterKey`; `TestOpenRestartAndSchema` also checks limiter-key persistence.
- Deferred checks (not run here): `go test -tags=integration ./internal/identity -count=1`, `go test -race ./internal/identity`, `gofmt -l`, lint, and build checks. Read-only `gofmt -d` was used to inspect the requested formatting.
- Parent gate findings were corrected without weakening policy: fixed the test variable redeclaration and unused import, replaced invalid password fixtures with unique non-blocklisted phrases, and cleared the prior source row before seeding exactly 50,000 saturation rows. No rerun here.
- Follow-up parent recheck reported only `model.go` error-column alignment; fixed via `edit`. No checks rerun here.
- Concerns: v1→v2 migration clears legacy session rows because their old schema cannot verify the new selector/secret form. Future backup code must exclude `limiter_key`, source/login limiter state, and sessions. HTTP/cookie attributes, audit integration, and later-task consumers remain out of scope. No language server was available for `Store` reference lookup.
