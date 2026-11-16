# Identity, Authorization and Security-Audit Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver Andaru-owned human/service identities, live-generation capability RBAC, passwords, API tokens, browser sessions, bounded login protection, durable security audit, a root recovery CLI, and the minimum authenticated local web surface.

**Architecture:** The root engine remains the only authentication and authorization authority. New `internal/authz`, `internal/identity`, and `internal/audit` packages isolate stable capability contracts, the root-only identity database, and the separate append-oriented audit database. Configuration generations own role definitions; the engine coordinates identity, audit, generation, and backup publication under the approved lock order. An unprivileged Go `andaru-web` process terminates HTTPS, serves a small React build, and proxies requests through its own peer-credential-gated Unix socket.

**Tech Stack:** Go 1.27.4; existing `modernc.org/sqlite`, JSON Schema, Unix `SO_PEERCRED`, systemd, Debian packaging, and `golang.org/x/crypto/argon2`; React + TypeScript + Vite + Mantine for the packaged client, with exact compatible versions pinned in its lockfile after official-source verification.

**Spec:** `docs/superpowers/specs/2026-11-15-identity-audit-core-design.md`, approved 2026-11-15.

## Global Constraints

- Implement only the approved identity/audit prerequisite. Do not add setup, factory-reset, MFA, LDAP/OIDC, passkeys, general operational logging, WAN recovery, or clustered sessions.
- The engine authorizes every protected operation. UI visibility, web-process headers, and token names never grant authority.
- Keep `/run/andaru/engine.sock` root-only. The web process gets only `/run/andaru-web/engine.sock`, behind exact static UID verification before request parsing.
- Preserve one enabled, password-capable human assigned the immutable built-in administrator role after every identity mutation or restore.
- Roles follow `generation.Pointers.Live`. Authorization digest changes monotonically advance `authorizationEpoch`; A→B→A never revives a browser session.
- Password settings: Argon2id v19, 65,536 KiB, time 3, parallelism 1, 16-byte salt, 32-byte output; two active hashes, queue 16, wait at most five seconds.
- Passwords are 15–128 Unicode code points, at most 512 UTF-8 bytes, no NUL or normalization, with the versioned offline blocklist.
- Sessions: 30-minute idle, 12-hour absolute, maximum five per human; the sixth login revokes the oldest.
- Tokens use a 128-bit selector and independent 256-bit secret. Optional expiry defaults to 90 days and cannot exceed one year. No-expiry is explicit.
- Login backoff starts after five failures, uses account-independent HMAC keys and source buckets, and caps at 15 minutes. No permanent network lockout.
- Audit retains 365 days and at most 100,000 records: 80,000 general plus 20,000 protected reserve. Never claim malicious-root tamper resistance.
- Ordinary sensitive mutations fail closed when audit intent cannot commit. Automatic rollback and kernel-verified root recovery always proceed.
- Default backups include users, never credentials. Credential export requires encrypted whole-archive output and the approved human-password/root boundary. Sessions, login limits, and audit are never exported.
- All list/input/body/string/count limits from the design are enforced before allocation or mutation. Stable errors never include credentials, bodies, or attacker-controlled strings.
- The owner requested precisely scoped implementation subagents. One worker owns each task's files; the parent integrates, verifies, and runs one condensed four-pillar adversarial review.

## Review Focus

1. A custom admin-equivalent role disappears on A→B→A while another user was disabled: the built-in human-administrator invariant must still hold. Tasks 2, 4, and 5 pin this.
2. Unknown, disabled, passwordless, and wrong-password logins arrive from rotating IPv6 addresses under limiter saturation: response class and Argon admission stay bounded without account enumeration. Tasks 2 and 6 pin this.
3. A token issuer has more rights than the target, or the target later loses rights: issuance and request-time scope intersection must prevent escalation. Tasks 2 and 4 pin this.
4. Audit storage fills or a mutation commits before its reply/outcome record: protected capacity, idempotent result replay, and reconciliation must preserve truthful outcomes. Tasks 3 and 4 pin this.
5. Configuration plus identity restore loses power between publications: restart must expose one complete old or new outcome, never mixed stores or a missing recovery administrator. Task 8 pins this.

## Structure and cross-task contracts

| Area | Files | Responsibility |
| --- | --- | --- |
| Stable authorization | new `internal/authz/*`; config model/schema/history tests | Capability catalog, immutable built-ins, custom role validation, actor type, canonical authorization digest |
| Identity runtime | new `internal/identity/*` | Root-only SQLite accounts, credentials, tokens, sessions, login limits, revisions, invariants |
| Security audit | new `internal/audit/*` | Separate SQLite events, category budgets, coalescing, intents/outcomes, reconciliation |
| Engine coordination | new `internal/engine/security*.go`; existing API/socket/apply/backup files | Authentication, route capability checks, idempotency, lock order, API contracts |
| Generation authority | `internal/generation/{store.go,realize.go}` and apply gates/tests | Manifest digest, authorization epoch publication, rollback/session invalidation |
| Root client | `cmd/andaru/{ctl.go,main.go}` plus new `identity.go` | Bootstrap, user/password/token/audit operations with terminal-safe secret input |
| Web boundary | new `cmd/andaru-web/*`, `internal/web/*`; socket/systemd/AppArmor/package files | HTTPS/static proxy, dedicated UID/socket, source-address forwarding, hardening |
| Browser client | new `web/*` | Login/session/users/tokens/roles/audit minimum UI; local assets and accessibility |
| Portable state | backup/config/identity/engine archive and restore files | Users/credentials members, `identityRevision`, durable multi-store restore coordinator |

Tasks 1–3 define independent stores/contracts. Task 4 consumes all three. Task 5 consumes Tasks 1, 2, and 4. Task 6 consumes Tasks 2–5. Task 7 consumes Task 6. Task 8 consumes Tasks 2–4. Task 9 qualifies the integrated result.

### Shared interfaces

`internal/authz` owns values shared by config, generation, identity, audit, and engine:

```go
type Capability string
type ActorKind string
type Actor struct { Kind ActorKind; ID string }
type Role struct { ID, Name, Description string; Capabilities []Capability; BuiltIn string }
type Snapshot struct { Digest string; Capabilities map[string][]Capability }

func Catalog() []Capability
func BuiltIns() []Role
func ValidateRoles(custom []Role) error
func Resolve(custom []Role, roleIDs []string) []Capability
func Digest(custom []Role) (string, error)
```

`internal/identity` exposes no SQL rows:

```go
type Store struct { /* private */ }
type Account struct { ID, Login, DisplayName, Kind string; Enabled bool; RoleIDs []string; Revision uint64 }
type CredentialState struct { PasswordCapable bool; PasswordEpoch uint64 }
type Revision uint64

type TokenIssue struct { AccountID, Name string; Scope []authz.Capability; ExpiresAt *time.Time; RevokeOnPasswordChange bool }
type IssuedToken struct { Selector, Secret string; Metadata Token }
type LoginResult struct { Account Account; SessionCookie, CSRFSecret string; ExpiresAt time.Time }

func Open(ctx context.Context, stateDir string) (*Store, error)
func (s *Store) Revision(ctx context.Context) (Revision, error)
func (s *Store) BootstrapAdmin(ctx context.Context, login, displayName, password string) (Account, error)
func (s *Store) CreateAccount(ctx context.Context, input CreateAccount, roles authz.Snapshot) (Account, error)
func (s *Store) UpdateAccount(ctx context.Context, id string, expected uint64, input UpdateAccount, roles authz.Snapshot) (Account, error)
func (s *Store) SetPassword(ctx context.Context, actor authz.Actor, id, password string, mode PasswordMode, roles authz.Snapshot) error
func (s *Store) IssueToken(ctx context.Context, actor authz.Actor, issuerCaps, targetCaps []authz.Capability, input TokenIssue) (IssuedToken, error)
func (s *Store) AuthenticatePassword(ctx context.Context, login, password, source string, authorizationEpoch uint64) (LoginResult, error)
func (s *Store) AuthenticateSession(ctx context.Context, selector, secret string, authorizationEpoch uint64) (Principal, error)
func (s *Store) AuthenticateToken(ctx context.Context, selector, secret string, roles authz.Snapshot) (Principal, error)
```

`internal/audit` exposes typed events and operation records only:

```go
type Category string
type Event struct { ID uint64; At time.Time; Actor authz.Actor; Source, Action, Target, Result, OperationID string; Category Category; Metadata map[string]string }
type Intent struct { OperationID string; Actor authz.Actor; Source, Action, Target, RequestHash string; Category Category }
type Outcome struct { OperationID, Result string; Metadata map[string]string }

func Open(ctx context.Context, stateDir string) (*Store, error)
func (s *Store) Begin(ctx context.Context, intent Intent) (RecordedOperation, error)
func (s *Store) Finish(ctx context.Context, outcome Outcome) error
func (s *Store) AppendSafety(ctx context.Context, event Event) error
func (s *Store) Reconcile(ctx context.Context, source OperationSource) error
```

Exact request structs receive explicit JSON tags in engine files. Store types are not serialized implicitly.

---

### Task 1: Capability catalog, roles, and configuration schema 3

**Files:** create `internal/authz/{catalog.go,roles.go,actor.go}` and tests; modify `internal/config/{model.go,schema.json,schema.go,validate.go,views.go,changes.go,history.go}` and adjacent tests; modify `internal/engine/openapi.json`.

**Interfaces:** produces the shared `authz` interfaces, immutable role IDs `administrator` and `read-only`, and configuration schema 3 custom roles. Schema 1/2 generations remain readable and synthesize the same built-ins.

- [ ] **Step 1: Write RED tests.** Pin catalog uniqueness, explicit read-only grants, dynamic administrator grants, custom-role canonical ordering/digest, built-in shadow refusal, unknown capability refusal, and schema 1/2 compatibility. Add config round-trip/history tests proving role edits are ordinary saved configuration and old canonical bytes remain unchanged.
- [ ] **Step 2: Run RED.** `go test ./internal/authz ./internal/config -run 'Capability|Role|Schema3' -count=1`. Expected: package/types or schema support missing.
- [ ] **Step 3: Implement minimal catalog and schema support.** Keep `internal/authz` free of config/engine imports. Add `roles` only to schema 3. Synthesize built-ins in views; never serialize them into declarations. Canonically sort capability strings before hashing.
- [ ] **Step 4: Verify GREEN.** Run the RED command, `go test ./internal/config ./internal/authz`, and OpenAPI/schema contract tests. Existing schema-1/2 fixtures must remain byte-compatible.
- [ ] **Step 5: Commit.** `git add internal/authz internal/config internal/engine/openapi.json && git commit -m 'feat: add capability roles to configuration'`.

### Task 2: Root-only identity store and credentials

**Files:** create `internal/identity/{model.go,sqlite.go,store_linux.go,password.go,token.go,session.go,limit.go}` and focused unit/integration tests; add the offline password blocklist plus provenance under `internal/identity/testdata`/embedded asset; modify `go.mod`/`go.sum` only to promote existing `golang.org/x/crypto` use if required.

**Interfaces:** produces the shared identity interfaces. Database path is `<stateDir>/identity.db`, application ID is fixed, layout migrations are forward-only and transactional, files/WAL are root-owned 0600.

- [ ] **Step 1: Write RED database/invariant tests.** Use real SQLite and restart. Cover account limit 10,000, canonical login validation, tombstones, CAS revisions, service-account restrictions, and refusal of every mutation/restore that removes the last enabled password-capable built-in administrator.
- [ ] **Step 2: Write RED credential tests.** Pin Argon2 wire parameters and corrupted-cost refusal; password limits/blocklist; bounded two-worker/16-queue/five-second admission; token selector/secret hashing, issuer∩target scope, optional expiry, `revokeOnPasswordChange` default/override, hourly `lastUsedAt`, one-time secret, 50-active and 100/year limits; session epochs, idle/absolute timeout, five-session eviction, account-disable revocation, and restart.
- [ ] **Step 3: Write RED limiter tests.** Unknown/disabled/passwordless/wrong-password flows use the same HMAC login bucket and dummy Argon work. Cover five-failure threshold, 15-minute cap, IPv6 `/64`, 10,000 login rows, 50,000 source rows, 24-hour expiry, saturation overload bucket, and no permanent lockout.
- [ ] **Step 4: Run RED.** `go test -tags=integration ./internal/identity -count=1`. Expected: package/interfaces absent.
- [ ] **Step 5: Implement store and security primitives.** Keep SQL private. Use constant-time hash comparison, strict stored-parameter bounds before allocation, FULL synchronous/WAL/foreign keys/secure-delete, and transactional revocation/invariant checks. Never log credential material.
- [ ] **Step 6: Verify GREEN and race/resource behavior.** Run the RED suite, `go test -race ./internal/identity`, and a benchmark/test that records Argon latency and peak admission memory without weakening fixed parameters.
- [ ] **Step 7: Commit.** `git add internal/identity go.mod go.sum && git commit -m 'feat: add root identity and credential store'`.

### Task 3: Durable bounded security audit

**Files:** create `internal/audit/{model.go,sqlite.go,store_linux.go,retention.go,reconcile.go}` and tests.

**Interfaces:** produces the shared audit interfaces, monotonic IDs, 80,000 general/20,000 protected budgets, retention watermark, and operation reconciliation. It does not expose a hash-chain claim.

- [ ] **Step 1: Write RED tests.** Cover strict metadata allowlists/redaction, intent-before-outcome, duplicate operation ID with same/different request hash, committed result replay, login coalescing, category budgets, 365-day expiry, protected-event refusal, audit-health events, corruption/gap reporting, and restart reconciliation.
- [ ] **Step 2: Add failure tests.** Inject ENOSPC/read-only/commit-loss boundaries. Ordinary sensitive intent must fail closed; `AppendSafety` failure must be reportable without becoming a rollback/root-recovery gate. Automatic rollback/root recovery attempts the bounded emergency receipt and safe journal event, but receipt failure still cannot block recovery.
- [ ] **Step 3: Run RED.** `go test -tags=integration ./internal/audit -count=1`. Expected: package/interfaces absent.
- [ ] **Step 4: Implement the separate root-only `audit.db`.** Use typed columns/metadata, FULL synchronous/WAL, category-aware pruning after append, and a retention watermark. Store no request body or arbitrary error text.
- [ ] **Step 5: Verify GREEN.** Run the RED suite and `go test -race ./internal/audit`; prove a full general pool still permits protected revocation and blocks new non-safety general operations.
- [x] Task 3 follow-up: classify cancellation artifacts from automatic transaction rollback without latching storage health; the real SQLite test accepts the driver's `sql.ErrTxDone` or context cancellation/deadline result and proves healthy Begin/Append afterward. Tests not run per assignment.
- [ ] **Step 6: Commit.** `git add internal/audit && git commit -m 'feat: add bounded security audit store'`.

### Task 4: Root API, CLI, and audited authorization coordinator

**Files:** create `internal/engine/{security.go,identity_api.go,audit_api.go}` and tests; modify `internal/engine/{api.go,socket_linux.go,openapi.json}`; create `cmd/andaru/identity.go` and tests; modify `cmd/andaru/{ctl.go,main.go}`.

**Interfaces:** consumes Tasks 1–3. Produces engine middleware that constructs `authz.Actor` only from verified root/session/token context, declares one capability per protected route, and coordinates authoritative operation records with audit intent/outcome.

- [ ] **Step 1: Write RED route-contract tests.** Startup refuses unclassified protected routes. Root peer maps only to `system.root`; request headers cannot supply actor/source. Test users/roles/capabilities/tokens/audit pagination, strict bodies, CAS errors, scope intersection, invariant refusal, generic auth failures, audit query/export auditing, and no credential fields in responses/logs.
- [ ] **Step 2: Write RED CLI/password tests.** Add `andaru ctl users bootstrap-admin|list|get|create|update|disable|delete|password-reset`, `tokens`, and `audit list`. Bootstrap works only when no users exist and requires root, controlling terminal, hidden repeated password, and typed confirmation. Self-change requires the current password. Network reset of a non-administrator requires the acting administrator's password in that exact request. Resetting a built-in administrator remains console-only.
- [ ] **Step 3: Write RED idempotency tests.** Same operation ID/body returns recorded metadata; changed body refuses; lost token-creation reply returns metadata saying secret unavailable; committed identity mutation with missing audit outcome reconciles truthfully.
- [ ] **Step 4: Run RED.** `go test -tags=integration ./internal/engine ./cmd/andaru -run 'Identity|User|Token|Audit|Bootstrap|Operation' -count=1`.
- [ ] **Step 5: Implement one coordinator.** Open identity and audit beside config in `engine.Serve`; route root requests through the same capability declarations as future web requests, with the explicit root bypass represented as an actor policy, not scattered conditions. Preserve current root-only API behavior.
- [ ] **Step 6: Verify GREEN.** Run RED, `go test ./...`, root socket integration from `/`, and secret-output scans. Remove any duplicate CLI/API validation that belongs in stores.
- [ ] **Step 7: Commit.** `git add internal/engine cmd/andaru && git commit -m 'feat: expose audited identity administration'`.

### Task 5: Live-generation authorization epochs and confirmation

**Files:** modify `internal/generation/{store.go,realize.go}` and tests; modify `internal/config/store_linux.go`/generation creation seam; modify `internal/engine/{apply_gates.go,applies_api.go,takeover_api.go,security.go}` and tests; add root-owned authorization-state helpers under `internal/authz` if they remain filesystem-only.

**Interfaces:** manifests gain canonical `AuthorizationDigest`; root-owned state stores `{liveDigest, authorizationEpoch}`. Lock order is generation, authorization, identity. Candidate records identity revision; activation rechecks it.

- [ ] **Step 1: Write RED generation tests.** Schema-1/2 generations resolve built-ins; A→B→A increments epoch twice; rollback/boot works with identity DB unavailable; unchanged digest does not invalidate sessions; failed activation restores prior digest while advancing safely.
- [ ] **Step 2: Write RED concurrency/confirmation tests.** Candidate identity revision mismatch refuses before activation. Authorization-changing activation invalidates affected sessions and accepts confirmation only from a newly authenticated human administrator under the candidate or local root. Token/service actors refuse.
- [ ] **Step 3: Run RED.** `go test ./internal/generation ./internal/engine -run 'Authorization|Epoch|Role|Confirmation|IdentityRevision' -count=1` plus the root integration variants.
- [ ] **Step 4: Implement digest publication under existing generation flock.** Keep boot/rollback database-independent. Do not place SQLite access in `internal/generation`. Engine performs identity revision checks and session invalidation around the filesystem realizer contract.
- [ ] **Step 5: Verify GREEN.** Run RED, existing activate/rollback/boot suites, `go test ./...`, and race tests for account edit versus activation. Assert current networking behavior is unchanged when role digest is unchanged.
- [ ] **Step 6: Commit.** `git add internal/authz internal/config internal/generation internal/engine && git commit -m 'feat: bind authorization to live generations'`.

### Task 6: Dedicated web socket, sessions, CSRF, and login protection

**Files:** refactor `internal/engine/socket_linux.go` and tests; create `internal/engine/web_api.go` and tests; create `internal/web/{proxy.go,server.go,headers.go}` and tests; create `cmd/andaru-web/main.go`; modify `go.mod` only if standard library cannot meet the selected TLS/static needs.

**Interfaces:** root and web listeners use explicit policies. Web parent `/run/andaru-web` is root-owned 0710 group `andaru-web`; socket is root-owned 0660; engine verifies exact configured web UID before reading HTTP. Web proxy overwrites source metadata from the kernel-observed client address.

- [ ] **Step 1: Write RED peer/listener tests.** Wrong UID, root socket traversal, symlink/replacement races, forged source/identity headers, oversized headers/body, and disconnect/shutdown all fail safely. Existing root socket remains 0600 under 0700 parent.
- [ ] **Step 2: Write RED HTTP auth tests.** Login uses cookie flags exactly, generic failures, limiter and Argon admission. Session reload rotates CSRF. State changes require exact Origin plus session-bound token. Cookie+Bearer is rejected. Tokens never use CSRF. Password reauthentication is exact-request only for protected actions.
- [ ] **Step 3: Run RED.** `go test -tags=integration ./internal/engine ./internal/web ./cmd/andaru-web -run 'Web|Session|CSRF|Origin|Peer|Login' -count=1`.
- [ ] **Step 4: Split listener setup without weakening pre-parse peer checks.** Keep request limits in the engine and web proxy. Use HTTPS only, configured management origins, no wildcard CORS, local assets, CSP/HSTS/frame/MIME/referrer headers, and no credential-bearing logs.
- [ ] **Step 5: Verify GREEN with real browser transport.** Run RED, `go test ./...`, launch against temporary cert/state/socket fixtures, and use browser DevTools to observe cookie flags, CSRF, login/logout, forbidden Origin, and zero external runtime requests.
- [ ] **Step 6: Commit.** `git add internal/engine internal/web cmd/andaru-web go.mod go.sum && git commit -m 'feat: add authenticated web boundary'`.

### Task 7: Minimum local identity UI and Debian service hardening

**Files:** create `web/{package.json,package-lock.json,tsconfig*.json,vite.config.ts,index.html,src/**}`; embed built assets through `internal/web`; add `packaging/systemd/andaru-web.service`, tmpfiles/sysusers/AppArmor entries and tests; modify `debian/{control,rules,andaru.install,andaru.postinst}` and CI build files.

**Interfaces:** consumes Task 6 JSON/OpenAPI contracts. Produces login/logout, current-session, users, roles, tokens, and audit screens only. It does not add dashboard/network/factory-reset/setup screens.

- [ ] **Step 1: Verify and pin current official React/Vite/Mantine guidance and licenses.** Record selected versions in lockfile and package copyright data. Add no router/state/chart/date dependency until the minimum screens need it.
- [ ] **Step 2: Write RED frontend tests.** Use the repository-selected Vite test stack. Pin login errors without account disclosure, CSRF use, capability-hidden controls plus server 403 handling, one-time token display, no-expiry warning, last-admin refusal display, session expiry, keyboard focus, labels, and local-only assets.
- [ ] **Step 3: Build the minimum UI.** Use Mantine controls directly with small theme tokens; no universal wrappers. API errors remain typed and redacted. Never retain passwords/token secrets in persistent browser storage.
- [ ] **Step 4: Add RED package/service tests.** Assert dedicated system user/group, runtime directory/socket modes, `NoNewPrivileges`, private temp/home, restricted capabilities/filesystem, engine dependency ordering, packaged static assets, and upgrades that never start an insecure plaintext listener.
- [ ] **Step 5: Verify UI and package.** Run frontend test/build/lint/typecheck, Go tests, Debian package build/install smoke, and browser keyboard/network/CSP checks on the actual packaged service.
- [ ] **Step 6: Commit.** `git add web internal/web packaging debian .github && git commit -m 'feat: ship local identity administration UI'`.

### Task 8: Users/credentials backup and crash-safe multi-store restore

**Files:** modify `internal/backup/{archive.go,limits_test.go}` and tests; add `internal/identity/backup_linux.go`, `restore_linux.go` and tests; modify `internal/config/restore_linux.go`; modify `internal/engine/backup_api.go` and integration tests; modify CLI backup validation/output and OpenAPI.

**Interfaces:** archive documents gain `Users` and encrypted-only `UserCredentials`. Identity restore uses `identityRevision`. A new engine-owned restore coordinator journals one operation across config and identity prepare/publish phases under generation→authorization→identity ordering.

- [ ] **Step 1: Write RED archive tests.** Safe v1 reads remain valid. New format/member limits accept users and encrypted credentials only in encrypted archives; sessions/login limits/audit never appear. Unknown/duplicate members and plaintext credentials refuse before mutation.
- [ ] **Step 2: Write RED identity restore tests.** Stable IDs/tombstones, missing roles, passwordless user-only restore, missing-account credential refusal, paired exact-ID binding, destination-local epochs, account cap, and built-in administrator invariant all survive restart.
- [ ] **Step 3: Write RED coordinator crash tests.** Inject failure before/after each config and identity publication boundary. Restart/retry yields complete old or complete new selected stores and the recorded result. Same ID with changed request refuses. No selected store activates networking.
- [ ] **Step 4: Run RED.** `go test -tags=integration ./internal/backup ./internal/identity ./internal/config ./internal/engine ./cmd/andaru -run 'Users|Credentials|IdentityRevision|RestoreCoordinator' -count=1`.
- [ ] **Step 5: Implement format extension and coordinator.** Reuse existing bounded ZIP/age stream and config prepare primitives; do not add a second archive or generic transaction framework. Default export includes users; credential scope retains existing whole-archive encryption and human-password/root policy.
- [ ] **Step 6: Verify GREEN.** Run RED, all backup/config/identity crash suites, `go test ./...`, encrypted secret scans, and existing v1 fixture tests.
- [ ] **Step 7: Commit.** `git add internal/backup internal/identity internal/config internal/engine cmd/andaru && git commit -m 'feat: back up and restore identity stores'`.

### Task 9: Integrated qualification, operator documentation, and adversarial review

**Files:** modify relevant `README.md`, operator/recovery docs, release notes, `.github/workflows/*`, `dev/vm-lab/*`, and test manifests only where qualification requires them.

**Interfaces:** consumes all tasks; produces recorded amd64/arm64 CI and Debian 13 two-baseline evidence. No new product behavior belongs here.

- [ ] **Step 1: Add only missing end-to-end tests.** Cover root bootstrap→web login→role activation→session invalidation→rollback→token intersection→audit query→encrypted identity backup/restore. Each test must fail for a plausible user-visible regression, not source text or wiring.
- [ ] **Step 2: Run repository gates once.** Run formatting, unit/integration/race tests, frontend lint/typecheck/test/build, OpenAPI/schema checks, vulnerability/dependency/license scans, Debian package build/install/upgrade, and native amd64/arm64 CI.
- [ ] **Step 3: Qualify both snapshotted Debian 13 lab baselines.** Record Argon latency/peak memory, login saturation, session restart, browser cookie/CSRF/CSP behavior, role A→B→A, audit full/ENOSPC, service sandbox/socket ownership, backup/restore crash recovery, and package upgrade/reboot. Restore snapshots afterward.
- [ ] **Step 4: Run one condensed adversarial review.** Reviewer covers contract, hostile correctness, test honesty, and ponytail/over-engineering. Evaluate nits for underestimated impact. Fix every confirmed finding and rerun only affected gates plus final integration smoke.
- [ ] **Step 5: Update docs and commit.** Document exact operator commands, limits, recovery boundary, audit guarantees/non-guarantees, and backup scopes. `git add README.md docs .github dev && git commit -m 'docs: qualify identity and audit core'`.

## Self-review result

- Every approved design section maps to Tasks 1–9; setup and factory reset remain explicit non-goals.
- Shared types and lock/publication ownership are defined once. Later tasks consume earlier interfaces rather than inventing parallel authorities.
- Each task has a RED/GREEN cycle, a bounded deliverable, and an atomic commit. Task 9 owns only integration evidence/docs.
- Review Focus cases are pinned in Tasks 2–6 and 8.
- The plan intentionally uses standard library HTTP/TLS, existing SQLite/JSON Schema/age infrastructure, and one small React client. It adds no identity service, signed-assertion layer, generic transaction framework, or speculative capabilities.
