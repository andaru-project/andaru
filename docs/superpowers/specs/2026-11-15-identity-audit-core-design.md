# Identity, authorization and security-audit core

Status: approved 2026-11-15. This is prerequisite 1 of the approved [LAN-recoverable factory-reset design](https://github.com/andaru-project/andaru/pull/59). It does not implement the web UI, setup workflow, factory reset or general operational logging.

## Intent

Give Andaru one authoritative application identity system for humans and automation:

- Andaru-owned users and passwordless service accounts, never PAM/system users;
- capability-based RBAC enforced by the privileged control plane API;
- versioned roles/permissions activated and rolled back with the live configuration generation;
- passwords, scoped API tokens and server-side browser sessions;
- durable security-audit events; and
- root-console administrator bootstrap/reset.

The browser frontend remains a replaceable client. The UI may hide unavailable actions, but every request is authorized by the engine. Generic Debian accounts, SSH authentication and filesystem permissions remain outside Andaru identity.

This core is intentionally useful before the full UI: typed root-local APIs/CLI and a dedicated future web-socket boundary permit qualification without shipping an insecure placeholder login page.

## Owner decisions

From [issue #12](https://github.com/andaru-project/andaru/issues/12), plus current conversation:

- Andaru has its own users/RBAC, not PAM.
- Authentication, sessions, CSRF, rate limiting and authorization live in the control-plane API, not React or the web process.
- Roles/permissions belong to versioned configuration. Users and role assignments are an unversioned logical store. Password/API-token hashes are a separate logical credential store.
- Configuration roles and configuration secrets move with generations; user and credential stores restore independently.
- Passwords use Argon2id with per-user salt and parameters stored with the verifier.
- Browser sessions are opaque server-side sessions in `HttpOnly`, `Secure`, `SameSite=Strict` cookies. Password or role-assignment changes revoke browser sessions.
- API tokens belong to users, carry a subset of that user’s capabilities, are revocable and stored hashed.
- Administrator-password recovery is root-console only, never network recovery.
- TOTP and external identity providers are later optional work.
- Selected frontend remains Go + React/TypeScript/Vite/Mantine, locally served, with no runtime CDN/Node server.

New owner choices, 2026-11-15:

- RBAC uses stable resource/action capabilities, not endpoint or page privileges.
- Browser session idle timeout is 30 minutes; absolute lifetime 12 hours; maximum five sessions per human user. A successful sixth login revokes the oldest.
- Per-account and per-source login backoff starts after five failures, grows exponentially and caps at 15 minutes. There is no permanent network lockout.
- Security audit retention is 365 days, capped at 100,000 events. Repeated blocked-login failures are coalesced so attackers cannot cheaply evict useful history, and so are one actor's repeated reads of the log, so a screen that polls it does not either. An export of the log counts as a read: engines up to `0.5.0-a.4` take an `audit.queried` event with a target as damage to the log, so none carries one. A coalesced event keeps its first ID and takes the time of its latest occurrence.
- API token expiration is optional. New-token default is 90 days; when expiration is set it may be at most one year. No-expiry requires explicit selection and warning.
- Password-change revocation is optional per API token, default on. Account disable/delete always invalidates tokens.
- Service accounts have no password and cannot create browser sessions. Their roles may change without invalidating tokens; every request dynamically intersects token scope with currently effective role capabilities.
- Role permissions become effective with the live generation. Saved role edits are inert until activation; rollback restores prior permissions. Independently, every identity state must retain at least one enabled password-capable human assigned the immutable built-in administrator role. Service accounts and custom administrator-equivalent roles do not satisfy that recovery invariant.

Primary decision sources:

- [API-first control plane](https://github.com/andaru-project/andaru/issues/12#issuecomment-6680220311);
- [own web UI](https://github.com/andaru-project/andaru/issues/12#issuecomment-6680226666);
- [Andaru users/RBAC and security baseline](https://github.com/andaru-project/andaru/issues/12#issuecomment-6680283522);
- [four-store amendment](https://github.com/andaru-project/andaru/issues/12#issuecomment-6680490592); and
- [selected UI stack](https://github.com/andaru-project/andaru/issues/12#issuecomment-6752595186).

## Approaches considered

### 1. Engine-owned identity core behind separate local sockets — selected

The privileged engine owns identity persistence, password/token verification, sessions, RBAC and audit. The existing root socket remains mode 0600 and grants a kernel-verified root actor. A second socket admits only the dedicated unprivileged `andaru-web` UID; browser cookies/tokens are still parsed and authorized by the engine.

The unprivileged web process terminates TLS, serves embedded/local UI assets and proxies API requests. It is trusted with plaintext HTTP credentials/session cookies because it terminates TLS, but it cannot assert an actor or bypass authorization. Compromise can steal/replay user credentials; it does not directly grant root operations without a valid credential/capability. Keep it minimal and sandboxed.

Advantages: matches owner ruling, one authorization implementation for UI/automation, root host effects remain in the engine, no signed identity-header protocol. Cost: the root engine parses authenticated management requests, so strict input limits/security review are mandatory.

### 2. Authentication in the web process

Simpler network server, but the engine would trust user identity/permissions asserted by another process. This violates the selected API authority and was rejected.

### 3. Separate identity service with signed assertions

Could reduce credential handling in the engine, but adds key management, assertion expiry/replay and another critical daemon before there is a need. Rejected as speculative complexity.

## Trust boundaries and process model

### Local sockets

- `/run/andaru/engine.sock`: root-owned 0600, current kernel `SO_PEERCRED` root actor. Root-local operations are audited as `system.root`; user cookies/headers never replace root identity.
- `/run/andaru-web/engine.sock`: a separate root-owned parent directory, mode 0710 and group `andaru-web`, contains only this root-owned 0660 socket and its root-owned 0600 lock file, `engine.sock.lock` (2026-11-21), which the engine holds while it serves. The existing `/run/andaru` directory stays 0700, so the web UID cannot traverse to `engine.sock`. The engine verifies the exact static web UID with `SO_PEERCRED` **before** reading HTTP headers/body. Every web-socket route then requires an authenticated browser session or API token except exact login/bootstrap-status endpoints.

Do not widen `engine.sock`, put the web user in a privileged group, trust `X-User`/role headers or share a bearer secret that turns the web daemon into root. The web socket accepts a canonical source-address field only from the verified web peer for rate limiting/audit; ordinary clients cannot supply it. The web process validates the kernel connection’s real remote address and overwrites, never forwards, user-provided proxy headers. Direct root requests ignore it.

The web process can observe login passwords/cookies while proxying. Its sandbox, logging rules and minimal code reduce exposure but do not make it a confidentiality boundary. Password/session values never enter access logs, metrics, crash reports or UI telemetry.

### Network-facing HTTP

- HTTPS only. No plaintext management listener or automatic HTTP redirect on WAN/LAN.
- Same-origin application; no credentialed wildcard CORS. Initial implementation sends no CORS allowance.
- `Host` and `Origin` checked against configured management origins. Proxy metadata is accepted only on `web.sock`.
- Security headers include strict CSP with local assets only, HSTS after HTTPS configuration, frame denial, MIME sniffing denial and a restrictive referrer policy.
- Browser cookie name `__Host-andaru_session`, `Path=/`, no `Domain`, `Secure`, `HttpOnly`, `SameSite=Strict`.
- A request carrying both the session cookie and `Authorization: Bearer` is rejected as ambiguous; there is no credential precedence rule an attacker can exploit.

## Domain model

### Capability catalog

Capabilities are compile-time stable strings with resource/action semantics, for example:

```text
status.read
configuration.read
configuration.write
configuration.apply
configuration.confirm
backups.create
backups.restore
backups.exportSecrets
users.read
users.manage
tokens.manageSelf
tokens.manageAll
audit.read
```

Add a capability only with the owning feature. Do not pre-register factory-reset, extension or logging capabilities before their endpoints exist. Every protected route declares one required capability beside its registration; startup/test validation refuses an unclassified protected route.

Capabilities are API concepts, not page names and not raw HTTP paths. Removing/renaming one requires an explicit configuration migration and release note.

### Roles

Roles live in the versioned configuration declaration:

Built-ins are synthesized by the engine under fixed reserved IDs and are never serialized as editable custom roles. Configuration schema 3 adds only custom roles; old schema-1/2 generations resolve the same built-ins without rewriting their canonical historical bytes. Role-list APIs return built-ins plus the live generation’s custom roles, clearly marking immutable entries. Imported configuration cannot shadow a built-in ID/name.
```text
Role {
  id            stable generated role ID
  name          unique human label
  description   optional bounded text
  capabilities  sorted unique capability strings
  builtIn       administrator | read-only | empty
}
```

Two built-ins always exist:

- `administrator`: immutable ID/name and dynamic grant of all current capabilities. This is the only role that automatically gains new capabilities.
- `read-only`: immutable ID/name and an explicit safe-read list maintained with code. It does not automatically gain audit/user/backup/export reads merely because a new capability ends in `.read`.

Custom roles have explicit capabilities. Unknown/removed capability strings are rejected on save/import; they are never silently granted. Names are presentation, IDs are references.

### Accounts

Users live outside generations:

```text
Account {
  id            stable UUID
  login         canonical lowercase ASCII login
  displayName   optional Unicode display name
  kind          human | service
  enabled       boolean
  roleIds       sorted unique stable IDs
  revision      optimistic concurrency version
  createdAt     timestamp
  updatedAt     timestamp
}
```

Login is 3–64 ASCII characters: starts alphanumeric, then lowercase letters, digits, `.`, `_`, `-`. Input is lowercased once; no Unicode/confusable login identifiers. Display names support validated UTF-8 separately.

The appliance supports at most 10,000 non-deleted accounts. Creation and user-restore refuse growth beyond that bound before mutation; pagination never materializes the full account set.

Human accounts may hold a password and browser sessions. Service accounts may hold API tokens only. Login/password/session endpoints return the same generic failure for unknown, disabled, passwordless and wrong-password accounts.

Role references that do not exist in the active role generation grant nothing and produce an administrator-visible mismatch. They are not silently deleted/remapped. A user restore without credentials creates human accounts with no usable password. Network restore is refused if its selected result would remove the last enabled password-capable human assigned the immutable built-in administrator role. Root-console restore may proceed only when it atomically creates/resets such a built-in administrator credential in the same operation—not “restore now, recover later.” Credential-only restore for a missing account is refused; paired restore binds by stable account ID.

Deleted account IDs remain tombstoned and are never reassigned to a different identity; this prevents an old credential/token backup from binding to a new account. Login names may be reused only through an explicit administrator operation that cannot reuse the old account ID and never revives old credentials.

### Effective authorization

Authorization uses the role configuration named by `generation.Pointers.Live`, not the saved head or merely `Confirmed`. A boot fallback therefore uses the permissions that are actually live. Before the first live generation, only the narrow setup/bootstrap policy is available.

For each request:

1. load/validate account enabled state and current role assignments;
2. resolve those role IDs in the live role version;
3. union effective capabilities;
4. for a token, intersect with the immutable token scope; and
5. require the route capability.

Saved role edits have no effect. Every generation manifest carries a canonical `authorizationDigest` of its effective built-in/custom roles. A separate root-owned authorization state records `{liveDigest, authorizationEpoch}`. Whenever activate, rollback or boot changes to a different digest, the realizer atomically increments the monotonic epoch under the generation lock before publishing the corresponding live authorization pointer. Browser sessions store that epoch plus account/password epochs; any mismatch rejects them. Therefore A→B→A cannot resurrect a session that was never used during B. Rollback restores capabilities, never old session epochs/IDs.

API tokens are not invalidated by role changes. Their effective permissions shrink/grow dynamically within their fixed token scope. They never gain a capability outside that scope. Disabling/deleting the account invalidates all its tokens.

Every identity mutation/restore transaction verifies that at least one enabled human account with a usable password remains assigned the immutable built-in administrator role. This identity invariant is independent of custom live role definitions, so an A→B→A role rollback cannot remove the final recovery administrator. Before any authorization-changing generation activates, affected browser sessions are invalidated and confirmation must come from a newly authenticated human browser session that is administrator under the live candidate, or from the local root console. API tokens/service accounts cannot confirm authorization changes. Network/configuration activations whose effective authorization digest is unchanged retain the existing confirmation actors, including `system.root` and a token/session with `configuration.confirm`. A failed/unconfirmed apply rolls roles back with the generation timer; revoked sessions remain revoked.

Serialize candidate creation/activation and account edits with one stable authorization lock and explicit order: generation lock, then authorization lock, then identity transaction; account-only edits take authorization lock then identity transaction and never generation lock. A candidate manifest records the identity revision used for authorization/session-impact checks. Activation rechecks that revision under the authorization lock before changing live pointers; mismatch fails before network/role activation. Rollback/boot can always restore the previous digest and advance `authorizationEpoch` without opening the identity database, preserving engine-dead safety; the independent built-in-administrator identity invariant remains true across either role version. A corrupt/missing authorization state prevents network-facing authentication and raises console recovery; it never falls back to a saved head.

Replace free-form configuration-history actor strings with one internal bounded `Actor{Kind, ID}` value constructed only from verified root/session/token context. Stores serialize a safe canonical actor label; request JSON/headers never supply it. This migrates the current `unix:uid:`-only save seam without adding trusted user headers.

## Persistence layout

Physical layout was previously open. Use one root-only SQLite `identity.db` for logical user and credential stores plus runtime security state:

- `accounts`, `account_roles` — default-exported user store;
- `password_credentials`, `api_tokens` — encrypted-backup-only credential store;
- `sessions` — runtime, never backed up;
- `login_limits` — runtime anti-abuse state, never backed up;
- metadata/revisions/idempotency records.

One database gives atomic account/credential/session revocation and avoids a second cross-database publication protocol. Logical backup/restore scopes remain independent and strictly allowlisted; “store” does not require a separate SQLite file. Database and WAL are root-owned 0600 under the existing private state tree, with application ID/layout version, foreign keys, WAL and FULL synchronous policy. Credential pages use SQLite secure-delete as defense in depth, without promising physical erasure.

Use separate root-only `audit.db` because security events are append-oriented, queried/retained independently and survive identity transaction failures. It has its own application ID/layout version, WAL/full sync and root-only lock.

Sessions and login limits survive engine/web restart but are excluded from every portable backup. Factory reset recreates both empty.

## Passwords and password changes

Use `golang.org/x/crypto/argon2` already in the dependency graph. New verifiers use Argon2id with a random 16-byte salt, 32-byte output and parameters stored per hash:

```text
algorithm = argon2id
version   = 19
memory    = 65536 KiB
time       = 3
parallel  = 1
```

Implementation must benchmark these exact settings on both supported Debian VM baselines and record latency/peak memory. A bounded global admission gate prevents concurrent password hashes from exhausting appliance memory. Stored parameters have strict upper bounds before allocation; corrupted/high-cost records fail closed. Successful login rehashes parameters below current policy through a compare-and-swap credential update.
Initial admission policy permits two concurrent Argon2 operations, queues at most 16 for no more than five seconds, then returns a generic `429`/busy response. Real, dummy-user and rehash work share the same gate. Qualify these values on both Debian baselines; changing them is a measured security/resource decision, not an automatic RAM-derived scale-up.

Password policy follows NIST/OWASP direction rather than composition rules:

- 8–128 Unicode code points (owner decision 2026-11-21, was 15), valid UTF-8, maximum 512 bytes, no NUL; no trim/normalization or forced character classes;
- allow spaces, paste and password managers;
- reject login/display name and a bundled versioned blocklist of common/compromised passwords;
- no security questions or network password reset; and
- require the password twice for root bootstrap/reset/change where appropriate.

References: [NIST password guidance](https://pages.nist.gov/800-63-4/sp800-63b/passwords) and [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html). The bundled blocklist is offline and its provenance/version is documented; the appliance does not submit passwords or hashes to an Internet service.

Password change requires the current password over the authenticated web API. Root console reset does not. Either increments the password epoch, revokes every browser session and revokes only API tokens whose `revokeOnPasswordChange` flag is true.

## API tokens and service accounts

Token wire form contains a public random 128-bit selector and independent random 256-bit secret. The secret is shown once. Store selector, SHA-256 secret hash, account ID, name, sorted scope, creation/last-used/expiry timestamps, optional `revokeOnPasswordChange`, password epoch at issue and revision. Compare hashes in constant time. Never store/log/return the secret again.

Token creation:

- human users may create tokens for themselves with `tokens.manageSelf`, scoped to a subset of current effective capabilities;
- `tokens.manageAll` may create/revoke a token for another human or service account, but the requested scope must be a subset of **both** the issuer’s effective capabilities and the target account’s effective capabilities. It does not let a restricted operator mint an administrator token;
- service accounts cannot call login/session/password endpoints; and
- each account has a bounded token count (author proposal: 50) to limit storage/listing abuse.

Expiration is optional. UI/API default is 90 days. A supplied expiration must be future and no later than one year. Explicit no-expiry is represented separately—not a zero timestamp—and returns a warning/audit field.

`revokeOnPasswordChange` defaults true for human-account tokens and is not applicable to passwordless service accounts. Explicit revocation is always available. Role/permission changes leave token records valid; request-time intersection enforces current capabilities. Account disable/delete rejects immediately and revokes/removes all sessions/tokens transactionally.

Update token `lastUsedAt` at most once per hour per token so read-heavy automation does not turn every request into a SQLite write. This is activity metadata only; expiry, account state and authorization are evaluated on every request.

## Browser sessions and CSRF

Session IDs and CSRF tokens are separate random 256-bit values with selector+secret wire forms; only selectors and SHA-256 secret hashes are stored. Rotate at login; never accept unknown client-supplied IDs. `GET /api/v1/session` on a valid same-origin cookie session rotates/returns a new CSRF secret after page reload; same-origin policy and `SameSite=Strict` prevent another site from reading it.

- idle timeout: 30 minutes, server-enforced. A read the web UI sends with no user input (a pointer press, key press or wheel turn) since its previous request carries `X-Andaru-Background: 1` and does not count as activity, so polling does not keep an unattended screen signed in. A write always counts, and so does `GET /api/v1/session`, which the web UI sends when the page loads;
- absolute lifetime: 12 hours, server-enforced;
- maximum five sessions per human user; successful sixth login revokes the oldest transactionally;
- password or account-role assignment change revokes all sessions;
- live role capability change/rollback invalidates affected sessions through the capability digest;
- account disable/delete revokes all sessions/tokens; and
- logout is idempotent.

State-changing cookie-authenticated requests require both exact trusted `Origin` and a session-bound synchronizer token in `X-Andaru-CSRF`. SameSite is defense in depth, not the sole control. Token-authenticated automation requests do not use cookies and do not require CSRF. References: [OWASP session management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html) and [OWASP CSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html).

Capabilities that require password reauthentication—initially encrypted secret/credential export and future factory reset—accept only a human cookie session plus a write-only password in that exact request. API tokens and service accounts cannot exercise them even when their role/token scope names the capability. Root-local console keeps the separately approved OS-root exception. No reusable “recently reauthenticated” flag substitutes for the owner’s at-the-moment password entry.

## Login rate limiting

Enforce independent normalized-login-key and source-address buckets in the engine’s shared store. Derive the login key with a local keyed HMAC over the canonical login **before account lookup** and update it identically for unknown, disabled, passwordless and wrong-password accounts. A fresh source therefore cannot distinguish existence from retained account backoff. The source address comes only from the verified `andaru-web` peer’s kernel-observed client connection metadata, never an incoming forwarding header. Track exact addresses and an aggregate IPv6 `/64` bucket so trivial privacy-address rotation does not bypass throttling; do not aggregate unrelated IPv4 clients behind a guessed prefix.

The rolling failure window is 15 minutes. After five failures, delay exponentially and cap at 15 minutes. Successful authentication resets the login-key bucket but does not erase source abuse history. Unknown/nonusable accounts run the same fixed dummy Argon2id verification and return the same status/body/timing class.

Limiter rows store counters/window/backoff/last-seen timestamps, never individual requests. Login-key rows are bounded by the account limit plus recent unknown keys; source exact/prefix rows expire 24 hours after their last active backoff. Initial hard caps are 10,000 login-key rows and 50,000 source rows. Expired rows prune first. At source saturation, do not insert an untracked new exact row or allow a bypass: use a bounded global-overload bucket and return the same generic `429` until capacity recovers, while raising a health alert. Qualify these values under IPv4/IPv6 rotation and restart.

No permanent network lockout. Local root-console bootstrap/reset is outside network limits and remains audited. Bound global Argon2 work separately from logical backoff so many source addresses cannot exhaust memory.

## Security audit store

Audit state-changing/security-relevant events, not every ordinary read:

- login success/failure/backoff, logout/session revocation;
- user/role/password/token create/change/disable/delete;
- permission denial and sensitive secret export;
- configuration save/apply/confirm/rollback actor identity;
- backup restore, upgrade, setup/recovery and future factory reset;
- audit query/export and audit-storage failures.

Each event is strict typed data: event ID, UTC timestamp, actor kind/account/token selector, verified source, action, target IDs, result, request/operation ID and allowlisted metadata. Never record passwords, password hashes, token secrets/hashes, cookies, CSRF values, configuration secrets, request bodies or arbitrary error strings.

Append an intent before ordinary sensitive mutation and an outcome afterward. Audit failure before mutation blocks that operation. The authoritative identity/configuration transaction also stores the operation ID, actor, canonical request hash and committed result metadata; audit outcome append is reconciled from that record after a crash. A retry with the same ID/body returns the recorded outcome, while changed content is refused and an in-flight duplicate conflicts. Never report “unchanged” after the authoritative mutation committed.

One-time API-token issuance cannot replay the secret: if the state commits but the response is lost, a retry returns committed metadata stating that the secret is unavailable. The operator revokes/recreates it; Andaru never stores recoverable plaintext merely to replay a response.

Automatic rollback of an unconfirmed generation and kernel-verified local-root recovery always take precedence over audit availability. They attempt a minimal emergency receipt outside `audit.db`, but receipt failure—including full/read-only storage—does **not** block restoring connectivity/administration. They emit a safe system journal message, preserve/raise audit-health state when storage permits, and reconcile evidence later. Other session creation and sensitive user/password/token mutations fail closed if their required audit intent cannot be stored.

Audit uses monotonic event IDs and strict SQLite/application invariants, but makes no hash-chain/tamper-evidence claim. Selective retention and malicious root make a single hash chain misleading; SQLite integrity and sequence/watermark checks detect corruption/gaps only within the data promised to remain.

Retention:

- retain events up to 365 days and at most 100,000 records;
- reserve 20,000 slots for administrator/password/role changes, token revocation, secret export, setup/recovery/factory reset and audit-health events; ordinary events cannot consume this reserve;
- use the remaining 80,000-event general pool for login/session/permission and token-creation lifecycle events;
- repeated blocked-login failures, and one actor's repeated reads of the log, are coalesced before storage;
- self-service token creation is limited to 100 issued tokens per account per rolling 365 days, in addition to the 50-active-token cap; when its general audit budget is unavailable, new creation fails but revocation remains available through the reserve;
- protected events younger than 365 days are never count-pruned; reaching a category budget rejects new non-safety operations in that category and raises a visible health alert;
- prune only expired/eligible lower-value events after a successful append transaction; and
- record a retention watermark/typed prune summary so expected expiry is distinguishable from unexplained missing IDs, without claiming malicious-root tamper resistance.

Issue #15 may integrate this security audit into the broader log viewer/export pipeline, but cannot weaken these retention/redaction requirements silently.

## API and CLI surface

### Authentication/session

```text
POST   /api/v1/session                    login
GET    /api/v1/session                    current actor/capabilities/timeouts
DELETE /api/v1/session                    logout
GET    /api/v1/sessions                   own sessions
DELETE /api/v1/sessions/{id}              revoke own session
GET    /api/v1/session/preferences        what the web UI keeps for this account
PUT    /api/v1/session/preferences        replace it
```

Login and logout use generic responses that do not disclose account existence. Session creation returns CSRF token in JSON and sets the cookie; it never returns session ID.

Preferences are one JSON object of at most 16 KiB per account, such as a dashboard layout. The content is the client's: the engine checks that it is one JSON object with unique names and valid UTF-8, and acts on nothing in it. It is not configuration (no apply, no generation), not an identity mutation (no revision, no audit event, no Idempotency-Key: the write replaces the whole document), and it is in no backup. Any signed-in human reads and writes their own; no capability governs it. API tokens have no preferences.

Each document is a file under `preferences/` in the state directory, named by the account's ID, not a row in the identity database. A new table would change that database's layout, and the previous image, booted again after a rollback, refuses a layout it does not know. Deleting an account removes its file. A users restore keeps the files: an account restored under its ID has its preferences again, and a file whose account the restore removed stays, unused, at 16 KiB at most.

### Users, roles and tokens

```text
GET/POST        /api/v1/users
GET/PUT/DELETE  /api/v1/users/{id}
POST            /api/v1/users/{id}/password
GET/POST        /api/v1/users/{id}/tokens
DELETE          /api/v1/users/{id}/tokens/{selector}
GET/POST        /api/v1/config/roles
GET/PUT/DELETE  /api/v1/config/roles/{id}
GET              /api/v1/capabilities
GET              /api/v1/audit-events
```

All inputs are strict, bounded, version/precondition guarded and paginated where lists can grow. Password/token fields are write-only. User and credential logical restores use the existing portable archive preview/CAS model: default backups include `users.json`; encrypted explicit-secret backups may include `user-credentials.json`; sessions/login limits/audit are excluded.

Portable backup integration extends the archive deliberately: format/version and member-count limits must support `users.json` and encrypted-only `user-credentials.json` while retaining safe v1 reads. Default backup includes users; credential scope requires human administrator session, `backups.exportSecrets`, password re-entry and whole-archive age encryption. Sessions, login limits and audit are never archive members. User restore preserves stable IDs/tombstones and previews missing roles/credentials; it never binds credentials to a reused login name.

Identity restore has its own `identityRevision` precondition; it never reuses configuration `savedVersion`. User-only and credential-only selections publish one identity transaction and advance destination-local account/password/session/token epochs. A restore selecting configuration plus identity stores uses one durable restore coordinator: prepare both sides, journal intent and request hash, publish with documented lock order, and reconcile old-or-new complete outcomes after a crash. It never reports atomic success while one selected store remains old. Replaying a committed restore returns its recorded result; changed content under the same operation ID is refused.

Root-local CLI mirrors typed operations needed before UI/setup:

```text
andaru ctl users bootstrap-admin
andaru ctl users list|get|create|update|disable|delete
andaru ctl users password-reset ID
andaru ctl users tokens ...
andaru ctl audit list
```

`bootstrap-admin` is permitted only while no user exists, requires a controlling terminal, hidden repeated password and typed confirmation, and creates one enabled human assigned the immutable built-in administrator role. Self password change requires the human’s current password. A network administrator with `users.manage` may initialize/reset another **non-administrator** human’s password only after re-entering the acting administrator’s own password in that request; the target’s sessions and configured password-sensitive tokens are revoked. Resetting an account assigned the built-in administrator role is local-console only. Network administrators may create a new human, initialize its password while it is non-administrator, and later assign/promote roles through the normal saved/apply flow only if the separate built-in recovery-administrator invariant remains satisfied. Root-console bootstrap/reset remains the escape path; service accounts never receive passwords.

## Role/user consistency and recovery

- User role-assignment edits are immediate and CAS guarded; affected sessions are revoked.
- Role definition edits are saved configuration and take effect only when their generation becomes live.
- Missing role references grant nothing and are visible in user/restore previews. Nothing is silently remapped.
- User-only restore leaves human accounts passwordless/noninteractive. Network restore refuses a result without an enabled password-capable human assigned the immutable built-in administrator role; root restore must create/reset one atomically.
- Credential-only restore refuses credentials for missing account IDs. Paired restore binds exact IDs.
- Every identity mutation/restore preserves at least one enabled password-capable human assigned the built-in administrator role. Root console can create/reset one if stores are damaged. Custom administrator-equivalent roles and service accounts do not satisfy this invariant.
- Service-account tokens can hold powerful scopes but never satisfy interactive-management recovery invariants.

## Threat model

Assets: password/token verifiers, sessions, role authority, administrator availability, audit evidence and root operations.

Controls by threat:

- **Spoofing:** Argon2id passwords, 256-bit tokens/sessions, exact account/token state, generic failures, dedicated peer socket.
- **Tampering:** CAS revisions, foreign keys, canonical role generation, strict audit schemas/sequence watermarks and root-only files.
- **Repudiation:** intent/outcome audit with actor/source/request IDs; no secret-bearing metadata.
- **Information disclosure:** write-only credentials, hashed tokens/sessions, redacted errors/logs, encrypted credential backups.
- **Denial of service:** source/account backoff, Argon admission cap, pagination/count caps, no permanent lockout, audit coalescing.
- **Elevation:** route capability declarations, engine-side checks, token-scope intersection, no trusted user headers, no page-based auth.

A compromised web process can steal credentials/cookies it proxies and replay them. It still cannot invent capabilities or bypass the engine’s current account/session/token checks. Root compromise is outside this boundary.

## Qualification

Before calling the identity core usable:

- Argon2 parameter, malformed/high-cost verifier, dummy-user timing and concurrent-memory tests;
- password policy/blocklist, hidden terminal input and no secret logging tests;
- real store crash/ENOSPC/concurrency/CAS and user+credential atomicity tests;
- role save-vs-live, activation/rollback including A→B→A, missing role and built-in last-human-admin refusal tests;
- session idle/absolute/oldest-revocation, cookie flags, CSRF/origin and restart tests;
- human/service token scope intersection, optional expiry, optional password-revocation and account-state tests;
- rate-limit account/source IPv4/IPv6, unknown-user timing, restart and distributed web-process assumptions;
- audit intent/outcome crash reconciliation, SQLite/sequence corruption, retention/category-budget/coalescing and emergency-receipt failure tests;
- backup/restore independent users/credentials, encrypted-only credentials and no session/audit export;
- root versus dedicated-web Unix peer authorization and forged identity/source-header tests;
- actual HTTPS browser checks for cookie/security headers, login/logout/CSRF and no runtime external assets;
- native amd64/arm64 CI, dependency audit, Debian packages, and both snapshotted Debian 13 lab baselines.

Do not claim HSM-grade token protection, malicious-root audit integrity, MFA, LDAP/OIDC, passkeys, secure physical erasure or clustered/multi-appliance sessions.

## Delivery slices

This design is one security project but implementation remains incremental:

1. identity/audit databases, capability catalog, root CLI bootstrap/user/token/audit operations;
2. roles in configuration plus live-generation authorization snapshot and apply/rollback/session invalidation;
3. dedicated web socket, session/login/CSRF/rate limiting and thin local login/users UI;
4. portable users/credentials backup/restore and factory-reset/setup integration;
5. full security review and two-baseline qualification.

Each slice must produce real usable behavior with no unauthenticated placeholder network API. The network socket does not ship before authentication and authorization are complete.

## Design review

A fresh builder review identified four underspecified recovery/confirmation cases. The design now requires:

- user-only restore to preserve or atomically recreate the built-in human administrator;
- automatic rollback and kernel-verified root recovery to proceed when audit storage fails;
- current-password verification for self-change, actor-password re-entry for network reset of a non-administrator, and console-only reset of a built-in administrator; and
- a newly authenticated human administrator or root console to confirm authorization-changing activation.

An adversarial security review covered authorization transitions, token delegation, account enumeration, limiter/audit exhaustion, restore consistency and recovery availability. It found no remaining Critical or Important issue after corrections for:

- monotonic authorization epochs across A→B→A rollback and serialized identity/generation publication;
- token scope limited by issuer and target capabilities;
- account-independent login buckets, bounded limiter state and bounded Argon2 admission;
- protected audit capacity without misleading tamper-evidence claims;
- durable multi-store restore coordination and authoritative idempotency results; and
- the immutable built-in human-administrator invariant across every identity mutation and restore.

These are design-review results, not implementation verification.

## Author calls for approval

- One physical identity database implements two independently backed-up logical stores; audit is separate.
- The engine remains authentication/authorization authority; `andaru-web` is trusted with credential confidentiality but not actor/capability assertions.
- Role permissions follow `Pointers.Live`; session revocation uses capability digests, while tokens dynamically intersect current roles.
- Built-in administrator is dynamic-all; read-only is an explicit safe list; custom roles are explicit.
- Password policy is 8–128 code points, offline blocklist, Argon2id 64 MiB/time 3/parallel 1 with measured admission limits.
- API token expiration and password-change revocation are optional; service accounts are token-only and role changes do not invalidate tokens.
- Security audit retains 365 days/100,000 events with protected high-value evidence and fail-closed sensitive writes when full.
