# Portable Backup and Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Supply root-local portable backup and independent configuration/secret restore, with mandatory age encryption for exported secrets and no automatic network activation.

**Architecture:** The running engine exports coherent logical documents and commits restoration through the existing paired store publication boundary. A small archive package validates/encrypts bounded ZIP data; the Unix API and terminal CLI remain clients of that store, never offline database editors. Explicit unresolved secret references prevent accidental credential reuse and block realization until repaired.

**Tech Stack:** Go 1.27.4 (patch update for [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859)); existing SQLite, JSON Schema, root Unix peer authentication, `archive/zip`, existing `github.com/charmbracelet/x/term`; add `filippo.io/age v1.3.2` and its reviewed transitive dependencies. Debian 13 amd64/arm64 and existing VM/container qualification.

**Spec:** `docs/superpowers/specs/2026-11-14-portable-backups-design.md`, approved 2026-11-15.

## Global Constraints

- Existing configuration and configuration-secret stores only. No users/credentials/extensions placeholders, remote destinations, scheduled jobs, IP listeners or new authentication subsystem.
- Verified OS-root authorization is the approved console-only secret-export exception. Future web exports retain administrator permission, fresh password reauthentication and audit requirements.
- Default archive has no secret values. Explicit `--include-secrets` encrypts the whole ZIP with native age scrypt; no ZIP encryption, shell encryption or plaintext scratch files.
- Preserve every current configuration section, stable IDs, references, ordered lists and unused core secrets. Do not export SQLite, SQL, local history, generations, runtime sessions, logs or executable files.
- A configuration-only import never binds an imported name to an existing local secret automatically. Explicit unresolved state is readable/reloadable/exportable but cannot be realized.
- Secrets-only replacement refuses atomically if it removes a key needed by a bound current reference. Preserve the unselected declaration. Paired restoration may retain explicit unresolved references.
- Restore publishes one new local saved version and retains history. Never change live/pending generation pointers, rollback timers, network services, firewall rules or SSH. Activation is the existing separate reviewed apply path.
- Archive input 16 MiB; decrypted ZIP 12 MiB; at most 3 members; manifest 64 KiB; each document 4 MiB; total expanded content 12 MiB; maximum compressed-member expansion 100:1.
- Writer uses regular stored ZIP members with controlled names and mode 0600, no owner/executable metadata. Reader allows stored/deflated regular members only and never extracts member paths.
- Age write work factor 18; reject decryption factors above 18. Qualify roughly 256 MiB of KDF working memory. One expensive operation at a time. Do not silently reduce encryption strength.
- Passphrases are nonempty, at most 1,024 UTF-8 bytes, no NUL; read twice without echo for creation. No password flags, environment variables, saved password files, unattended bypass or claimed secure erasure.
- Read encrypted archives through authenticated EOF before mutation. Validate actual member bytes, CRC/size/checksum, strict JSON and supported schemas before committing.
- Every restore requires explicit selected stores and the exact previewed destination saved version. The CLI confirms `RESTORE`, sends the same immutable archive, and never silently retries a conflict or unknown commit outcome.
- No overwrite of backup output; private same-directory staging, sync file, no-replace publication and parent sync. Server never accepts arbitrary file paths.
- Execution amendment: the owner requested precisely scoped implementation subagents after Astra's review. Workers own disjoint files; the parent owns integration and verification. One final condensed reviewer covers contract, hostile correctness, test honesty and ponytail.

## Review Focus

1. A same-named local credential exists when config-only restoration runs: it must remain present locally but must never be silently used by the imported reference. Task 2 pins collision plus restart and explicit repair.
2. An imported ID existed previously but was deleted, or a rule list becomes empty: restoration must preserve identity, order/list history and future ordinary edits without duplicate-creation corruption. Task 2 covers resurrection, deletion and empty-list versions.
3. An encrypted stream has valid early chunks but a missing final chunk, or a ZIP lies about expanded size: no state may be published from partial authentication or headers alone. Task 3 tests truncation, false sizes and excessive KDF factors; Task 4 tests no mutation.
4. Capture overlaps configuration save and independent generation activation, or export is cancelled while waiting for the generation lock: manifest provenance must match one coherent observed pair and locks must be released without network changes. Task 4 tests real locks and concurrent observations.
5. A restore commits but its reply is lost, or output publication collides with an existing file: retry must not create a second saved version and output must not replace the earlier artifact. Tasks 4–5 pin stale-precondition replay, unknown outcomes and no-replace races.

## Structure and cross-task contracts

| Area | Files | Responsibility |
| --- | --- | --- |
| Explicit binding state | existing config model/schema/load/views/change/recovery files; renderer and CLI declaration validators | Support v1 and v2 documents; represent unresolved references and fail safely before realization |
| Logical export and restore | new `internal/config/backup_linux.go`, `internal/config/restore_linux.go` and adjacent tests | Export all selected logical data; validate/preview/commit full replacement through paired publication |
| Portable container | new `internal/backup/archive.go`, `crypto.go` and adjacent tests; `go.mod`, `go.sum` | Strict manifest/ZIP limits and age encryption, independent of engine/store packages |
| Engine coordination | new `internal/generation/observation_linux.go`, `internal/engine/backup_api.go` and tests; existing routing/OpenAPI; `debian/rules` | Coherent capture, typed preview/commit and true running-build provenance |
| Client | new `cmd/andaru/backup.go`, `backup_linux.go` and tests; existing dispatch/help | Terminal-only confirmation/passwords, binary HTTP, safe local input/output |
| Qualification | CI/install smoke plus docs/changelog | Package and two-baseline VM proof, compatibility and operator guidance |

No dependency goes from config to archive or from archive to config. The engine converts between their small raw-document structs. Tasks 1→2, 1/2/3→4, 4→5, all→6 are the shared seams.

### Type decisions shared by the tasks

In `internal/config/backup_linux.go` / `restore_linux.go`:

```go
type BackupDocuments struct {
    SavedVersion Version
    Configuration, ConfigurationSecrets json.RawMessage
}
type RestoreDocuments struct {
    Configuration, ConfigurationSecrets json.RawMessage // nil means not selected
}
type RestoreCounts struct { Created, Updated, Deleted int }
type RestorePreview struct {
    ExpectedSavedVersion Version
    ConfigurationChanges, SecretChanges RestoreCounts
    UnresolvedSecrets, BlockingSecrets []string
    Configuration json.RawMessage `json:"-"` // candidate declaration, never secret values
}
type RestoreResult struct {
    SavedVersion Version
    UnresolvedSecrets []string
}
func (s *Store) Export(ctx context.Context, includeSecrets bool) (BackupDocuments, error)
func ValidateBackupDocuments(input RestoreDocuments) error
func (s *Store) PreviewRestore(ctx context.Context, input RestoreDocuments) (RestorePreview, error)
func (s *Store) Restore(ctx context.Context, actor string, expected Version, input RestoreDocuments) (RestoreResult, error)
```

Engine-owned wire responses have explicit camelCase JSON tags; store types above are not serialized implicitly. `ValidateBackupDocuments` checks every present archive document, including unselected scopes, without requiring destination secrets. `PreviewRestore` requires at least one selected store. `BlockingSecrets` can be shown in a successful preview; commit refuses them. `Configuration` is the assembled secret-free candidate used to assess hardware, never a secret-material rendering shortcut.

In `internal/backup`:

```go
type Documents struct { Configuration, ConfigurationSecrets json.RawMessage }
type NetworkObservation struct {
    Live, Pending *int
    LiveSavedVersion string
}
type Source struct {
    AndaruVersion, SavedVersion string
    CapturedAt time.Time
    Network NetworkObservation
}
type Payload struct { SchemaVersion int; Size int64; SHA256 string }
type Manifest struct {
    FormatVersion int
    BackupID string
    CreatedAt time.Time
    AndaruVersion, SavedVersion string
    Scopes []string
    Payloads map[string]Payload
    Network NetworkObservation
    SavedButUnappliedAreas []string
}
type Archive struct { Manifest Manifest; Documents Documents }
func Encode(ctx context.Context, source Source, docs Documents, passphrase string) ([]byte, error)
func Decode(ctx context.Context, raw []byte, passphrase string) (Archive, error)
```

All archive wire structs receive explicit camelCase JSON tags. `Encode` computes metadata, hashes and the random backup ID; callers cannot supply forged precomputed checksums. `Decode` returns only fully validated bytes/metadata, not partially parsed documents. Typed archive errors expose stable codes, not input-bearing library error strings. Empty `passphrase` means plain input/output only when no secrets are present.

In `internal/generation/observation_linux.go`:

```go
type Observation struct {
    Live, Pending *int
    LiveSavedVersion string
}
func Observe(ctx context.Context, dir string, capture func(Observation) error) error
```

`Observe` holds the existing generation flock during `capture`; export takes that lock first, then the store's serialized read. No current config-store writer may wait on that lock while retaining the store mutex. It uses the same root-owned directory/lock trust boundary and a 3-second context-bound wait, never a process-lifetime lock. If absent, initialize only the root-private generation directory/lock so the first backup also excludes a racing first activation; absent pointers mean baseline provenance. Never create a generation, state record or pointer. Keep the read/capture API narrow; do not expose a generic lock manager.

---

### Task 1: Represent intentionally unresolved references without disguising corruption

**Files:** modify `internal/config/{model.go,schema.json,schema.go,history.go,views.go,changes.go,store_linux.go,recovery_linux.go}`; `internal/render/{model.go,pppoe.go}`; `cmd/andaru/ctl.go`; `internal/engine/openapi.json` for the changed existing declaration/secret-view schemas; adjacent existing/new tests `internal/config/backup_model_test.go`, `backup_state_integration_linux_test.go`, `internal/render/pppoe_test.go`, `cmd/andaru/main_test.go`.

**Interfaces:** consumes existing `secretReference`, `secretView`, `savedState`, `RealizationInput`, `Draft`, `VerifyRecoveryState`; produces an optional `secretReference.Unresolved bool` with JSON `unresolved,omitempty`, supported in configuration document schema 2. Configuration document schema 1 stays readable with its old meaning. SQLite `user_version` and secret document schema remain 1.

- [ ] **Step 1: Add failing pure-model and real-store tests.** Reuse `seededState`, `pppID`, `fixtureMap`, `openTestStore`, `ownedDir` and `networkInput`; do not invent an alternate store fixture convention. Pin these assertions in `TestUnresolvedReferenceView`:

```go
state := seededState(t)
state.Config.SchemaVersion = 2
for i := range state.Config.Links {
    if state.Config.Links[i].Password != nil { state.Config.Links[i].Password.Unresolved = true }
}
view, err := publicSnapshot(state)
if err != nil { t.Fatal(err) }
if bytes.Contains(view.Value, []byte("TEST_ONLY_OLD_VALUE")) { t.Fatal("secret exposed") }
// Decode link views: each marked reference has isSet=false and unresolved=true,
// even while the matching local secret value still exists.
```

`TestUnresolvedReferencePersists` uses the existing saved network fixture, explicitly marks a PPPoE reference unresolved by normal replace, closes/reopens the store, and asserts readable snapshot/history and a `VALIDATION_FAILED` realization error containing its safe reference path/name. `TestMissingBoundSecretIsStillCorruption` deletes an unmarked bound value and asserts view/load/recovery refusal. No empty-password substitutes.

- [ ] **Step 2: Run RED.** `go test ./internal/config ./internal/render ./cmd/andaru -run 'Unresolved|MissingBoundSecret'`; build the root config binary with `go test -tags=integration -c -o /var/tmp/andaru-backup-config.test ./internal/config`, then run it from `/` with `-test.run 'Unresolved|MissingBoundSecret'`. Expected: missing field/support or assertion failure before implementation; every new test must be observed red for its intended behavior.

- [ ] **Step 3: Implement schema/binding compatibility.** Add the flag to the model/schema/public view. Schema 1 rejects a true unresolved marker; schema 2 allows it. Preserve old stored documents exactly when reading; never convert a schema-1 document in memory before checking its original hash/canonical bytes. A normal edit introducing unresolved state promotes its resulting declaration to schema 2; existing v2 documents never downgrade implicitly. On restoration, preserve schema 1 when both imported and destination configuration are schema 1 and the resulting declaration needs no unresolved markers. Preserve imported or existing schema 2, and promote when unresolved state requires it. Secrets-only restoration preserves canonical declaration bytes and schema exactly. Existing `publishVersion` already records the declaration schema version; do not add a second version field or rewrite history.

Permit explicitly unresolved references only in v2. Keep all secret document/hash/version/ownership validation. Update load, view, recovery verification, ordinary edits, `Draft` and `RealizationInput` consistently. An unresolved view reports `isSet:false`, `unresolved:true`; it can expose existing named-key version metadata so an explicit edit can satisfy normal secret preconditions, but never its value. A later edit supplying/binding that key clears the flag only for the explicitly edited reference; changing the secret alone does not rebind other unresolved references. Preserve the flag when ordinary replacement omits password changes.

`RealizationInput` and `Draft` reject unresolved references with safe `VALIDATION_FAILED` details before accessing secret material. Renderer decoding also understands the flag and returns an error-severity `SECRET_UNRESOLVED` finding with no usable PPPoE output even if its input map happens to contain the same key. Read and CLI validators accept schema 1/2; the recovery verifier also checks row/document schema agreement. Unsupported schemas remain rejected. Add release notes in Task 6: older binaries must not open a v2 saved head, and downgrade repair requires their matching earlier state.

- [ ] **Step 4: Verify GREEN and nearest gate.** Run the RED commands, then `go test ./...`; root config suite from `/` with TMPDIR unset. Expected: all pass, existing v1 fixtures unchanged, secret redaction and corruption tests still pass. Existing OpenAPI declaration/view schemas agree with schema-2 support before this task commits; Task 4 adds the later endpoints. The renderer test must fail if only the flag check is removed while the same-named value remains available.

- [ ] **Step 5: Commit.** `git add internal/config internal/render cmd/andaru/ctl.go cmd/andaru/main_test.go internal/engine/openapi.json && git commit -m 'feat: represent unresolved restored secret bindings'` (stage only this task's files).

### Task 2: Export logical documents and restore one atomic local saved version

**Files:** create `internal/config/backup_linux.go`, `restore_linux.go`, `backup_integration_linux_test.go`, `restore_integration_linux_test.go`; minimally modify `store_linux.go` to share transaction/publication setup, and `changes.go` only where existing object/history helpers are reused.

**Interfaces:** consumes Task 1 schema/binding rules and existing `pendingSave`/`preparedChange`/`publishVersion`; produces all `config` signatures/types in the shared block. Portable secrets schema 1 is `{schemaVersion:1,secrets:{core:{NAME:{kind:"password",value:"..."}}}}`, with no source-local versions/timestamps. Unknown scopes/kinds/fields remain errors.

- [ ] **Step 1: Add RED round-trip and independent-selection tests.** `TestBackupExportScopes` saves `networkInput`, adds an unused core secret through the existing secret-setting path, and asserts default exported documents contain neither secret material nor public `isSet`/version metadata, while explicit export contains both referenced and unused values. `TestRestoreIndependentStores` covers the following concrete assertions using real stores:

```go
before, err := destination.Export(ctx, true)
if err != nil { t.Fatal(err) }
preview, err := destination.PreviewRestore(ctx, RestoreDocuments{Configuration: source.Configuration})
if err != nil || len(preview.UnresolvedSecrets) == 0 { t.Fatal("missing unresolved preview", err) }
result, err := destination.Restore(ctx, "unix:uid:0", before.SavedVersion,
    RestoreDocuments{Configuration: source.Configuration})
if err != nil || result.SavedVersion == before.SavedVersion { t.Fatal("restore did not publish", err) }
after, _ := destination.Export(ctx, true)
if !bytes.Equal(after.ConfigurationSecrets, before.ConfigurationSecrets) { t.Fatal("unselected secrets changed") }
if _, err := destination.RealizationInput(ctx, nil); err == nil { t.Fatal("same-name secret silently reused") }
```

Use source and destination with the same secret key and distinct literal values. After restart the imported reference is still unresolved, and a deliberate normal edit repairs it without losing the local value.

`TestSecretsOnlyMissingBoundKeyRefused` previews a secret document that omits a required bound key, asserts that key in `BlockingSecrets`, then asserts commit failure leaves both exported documents and saved version unchanged. Compatible secrets-only replacement preserves the canonical declaration bytes and leaves already unresolved references unresolved. Paired restore restores selected values with destination-local versions and no source counters.

`TestSecretsOnlyPreservesDeclarationSchema` starts a schema-1 destination with `createZone("retained")` and restores a valid portable secret document without selecting configuration. Assert the original document is schema 1, the restore advances the saved version, and the exact configuration bytes remain unchanged:

```go
before, err := destination.Export(ctx, true)
if err != nil { t.Fatal(err) }
var declaration struct { SchemaVersion int `json:"schemaVersion"` }
if err := json.Unmarshal(before.Configuration, &declaration); err != nil || declaration.SchemaVersion != 1 {
    t.Fatal("fixture must begin with schema 1", err)
}
result, err := destination.Restore(ctx, "unix:uid:0", before.SavedVersion,
    RestoreDocuments{ConfigurationSecrets: json.RawMessage(`{"schemaVersion":1,"secrets":{"core":{"RESTORED":{"kind":"password","value":"TEST_ONLY_RESTORED"}}}}`)})
if err != nil || result.SavedVersion == before.SavedVersion { t.Fatal("secrets restore did not publish", err) }
after, err := destination.Export(ctx, true)
if err != nil || !bytes.Equal(after.Configuration, before.Configuration) {
    t.Fatal("secrets-only restore changed declaration or schema", err)
}
```

Close/reopen and repeat the byte/schema check, exercising agreement between the version-row metadata and the canonical document. Include an already-schema-2 destination case to prove secrets-only restoration preserves 2 rather than downgrading it.

`TestRestoreRetainsIdentityAndHistory` restores a previously deleted object ID and an ordered list reduced to empty; assert one new saved version, stable IDs/list order, readable old history, no duplicate creation row and successful subsequent ordinary edits. Also cover a byte-identical restore creating exactly one new saved version and a replay with its old precondition failing.

- [ ] **Step 2: Run RED.** `go test -tags=integration ./internal/config -run 'BackupExport|RestoreIndependent|SecretsOnly|RestoreRetains|RestoreReplay' -count=1` as root from a safe checkout or compiled binary from `/`. Expected: undefined interfaces or the specified state assertions fail before production changes.

- [ ] **Step 3: Implement export/preview/publication.** `Export` captures one coherent saved pair using the existing serialized read; serialize the canonical declaration and separately selected portable secrets, not `SnapshotView` or referenced-only `RealizationInput`. Return owned immutable bytes. Validate strict JSON, supported schemas, topology, object count, secret names/kinds/value bounds and exact portable secret shape in `ValidateBackupDocuments` without consulting live secret values.

Add a pure restoration preparer in `restore_linux.go` that computes the candidate, counts, unresolved/blocking keys and `preparedChange`; use it for preview and commit. Nil fields mean unselected; nil/nil is not a mutation. Configuration-only sets every imported reference unresolved and preserves the complete destination secret document. Paired restore binds only material supplied by the selected secret document, preserving any explicit unresolved flags. After assembling the candidate, preserve schema 1 unless imported/existing schema 2 or resulting unresolved markers require schema 2; never downgrade. Secrets-only follows the approved refusal rule, never clears unresolved flags and never changes the destination declaration's canonical bytes or existing schema version.

Preview reads the current head, but only commit allocates versions. Commit requires a valid nonempty expected saved version and checks it inside `BEGIN IMMEDIATE` under `s.acquire`, before preparing the mutation. Reuse/factor the existing transaction initialization and `pendingSave.publish` ordering; do not implement a second database publication protocol. Rebase selected secrets to local versions/timestamps, mark linked-object versions consistently, and insert correct list/singleton/tombstone history. Request metadata for imported IDs including previously deleted ones: `Created` is true only for a never-before-created ID. Never regenerate IDs or silently merge.

A restore is an explicit history event: publish one next version even when documents match. Preserve cancellation/fencing/ambiguous-commit semantics and the existing root actor validation. No direct generation/network calls.

- [ ] **Step 4: Verify GREEN, faults and nearest gate.** Run the RED selection and the full root config suite. Extend the existing real paired-publication crash/fault harness for restore: before secret commit, after secret commit/before pointer commit, and after committed pointer/before reply. Assert the reopened store exposes a complete old or new pair, never a mixed pair. Test concurrent Save versus Restore CAS and replay. Run `go test ./...` too. Expected: all pass; no lost history or secret output. Do not add tests that only assert helper calls.

- [ ] **Step 5: Commit.** `git add internal/config && git commit -m 'feat: export and atomically restore logical configuration stores'`.

### Task 3: Implement bounded portable ZIP and native age encryption

**Files:** create `internal/backup/{archive.go,crypto.go,archive_test.go,crypto_test.go}`; add only necessary small test fixtures under `internal/backup/testdata/`; update `go.mod` and `go.sum`.

**Interfaces:** consumes the approved portable document shapes, not the config package; produces `backup` shared signatures/types. Pin age v1.3.2, the maintained release verified during planning; inspect its release/dependency changes and vulnerability reachability before adding it. Recheck maintenance/security at execution rather than silently changing the pin. Existing installed terminal support needs no new password-reading dependency.

- [ ] **Step 1: Add RED independent-container tests.** `TestPlainArchiveContainsOnlyDeclaredData` calls `Encode` on literal valid versioned JSON and reads it with standard `zip.NewReader`, not `Decode`, to assert exact allowed member names, regular mode 0600, stored compression, correct payload SHA-256/size and no secret values. `TestSecretArchiveRequiresEncryption` must reject a secret document with empty passphrase. `TestEncryptedArchiveAuthenticatesToEOF` round-trips literal configuration plus one test-only secret; modifying/truncating ciphertext must fail and return no `Archive`.

```go
raw, err := Encode(ctx, source, Documents{Configuration: configJSON, ConfigurationSecrets: secretsJSON}, "TEST_ONLY_PASSPHRASE")
if err != nil { t.Fatal(err) }
if bytes.Contains(raw, []byte("TEST_ONLY_SECRET_VALUE")) { t.Fatal("plaintext escaped") }
_, err = Decode(ctx, raw[:len(raw)-1], "TEST_ONLY_PASSPHRASE")
if err == nil { t.Fatal("accepted missing authenticated final chunk") }
_, err = Decode(ctx, raw, "WRONG_TEST_ONLY_PASSWORD")
if err == nil { t.Fatal("accepted wrong password") }
```

`TestHostileArchiveRejected` supplies independently constructed ZIPs for absolute/traversal/backslash/NUL/noncanonical names, duplicates/conflicts, directory/symlink/device types, unknown/missing members, undeclared secrets, bad CRC/hash/size, strict-JSON duplicates/trailing values, unsupported schemas/compression/native encryption, 4 members, 4 MiB+1 documents, manifest 64 KiB+1, total/ratio violations, 16 MiB+1 input and 12 MiB+1 decrypted ZIP. Assert no returned documents, not source strings.

`TestAgeWorkFactorCeiling` uses a syntactically valid excessive-factor header and asserts rejection before expensive allocation. Prefer a child process/container for resource-abuse cases; never risk the developer host with an intentionally huge KDF.

- [ ] **Step 2: Run RED.** `go test ./internal/backup -count=1`. Expected: no package/interfaces or explicit assertions fail; capture each intended failure before filling production code.

- [ ] **Step 3: Implement ZIP/manifest and encryption.** Encode deterministic logical member ordering with stored compression; generate backup ID/time/provenance, declared scopes, source schema metadata and computed hashes. Do not treat filename suffix as content detection. Recognize binary age/plain ZIP by validated content; reject unsupported envelope forms rather than shell out. If an archive declares secret scope, plain ZIP is always refused, including on inspect.

`Decode` bounds input first, authenticates/decrypts completely into bounded memory, enforces actual expanded reads/CRC/hash/size/name/type limits, then returns documents. Check all declared/unlisted members even if the later restore selects only one store. Reject duplicate manifest mappings and unknown manifest fields. Keep no payload files. Respect cancellation between bounded stages; do not claim a non-interruptible library KDF can be instantly cancelled.

Use the library's scrypt work-factor setter/ceiling rather than parsing or implementing cryptography. Configure write factor 18 and max read factor 18. Validate passphrase bytes centrally. Keep source version and backup ID strings bounded/printable and manifest schema strict. Avoid parallel crypto tests that multiply KDF memory.

- [ ] **Step 4: Verify GREEN and interoperability.** Run `go test ./internal/backup -count=1`, then verify one generated encrypted archive with independent age CLI in a disposable Debian container and verify one compatible age-CLI archive with `Decode`. Use explicit public test passphrases, no host package installation, no persistent real secrets and no permanently weakened production KDF. Run `go test -race ./internal/backup` and `go test ./...`; inspect newly added dependency vulnerability reports, recording any advisory/limit rather than promising an independent crypto audit.

- [ ] **Step 5: Commit.** `git add internal/backup go.mod go.sum && git commit -m 'feat: encode and validate bounded age-encrypted backup archives'`.

### Task 4: Expose coherent root-local backup and restore APIs

**Files:** create `internal/generation/observation_linux.go`, `observation_linux_test.go`, `internal/engine/backup_api.go`, `backup_integration_linux_test.go`; modify `internal/engine/{api.go,preview_api.go,socket_linux.go,openapi.json}`, `internal/generation/realize.go` only to share a justified lock primitive/comment, and `debian/rules` for running-build provenance.

**Interfaces:** consumes Tasks 1–3; produces `generation.Observe` and the three `/api/v1` paths from the spec. Add `newBackupHandler(store *config.Store, stateDir string, readLinks func(context.Context) ([]host.LinkStatus,error)) http.Handler`. Route it inside the existing verified-root boundary; pass the same current store, not an extra `config.Open`.

Wire contract:

- `POST /api/v1/backups`, JSON `{includeSecrets:false, passphrase?:string}`, no unknown fields. Passphrase is required/nonempty only with explicit secret export; reject inconsistent combinations. Return `application/zip` or binary age `application/octet-stream`, `Cache-Control:no-store`, `X-Content-Type-Options:nosniff` and source saved version/capture time metadata.
- Preview/commit multipart has exactly `archive` and `options`, options at most 64 KiB. The total HTTP body cap is 16 MiB + 64 KiB + 16 KiB framing overhead; this does not raise the 16 MiB archive or 64 KiB options caps. Options are `{passphrase?:string,stores?:["configuration","configurationSecrets"],expectedSavedVersion?:string}`. Preview with no stores is inspect; commit requires a nonempty unique selection and valid precondition. No server-side path or remote URL options; MIME filename metadata never selects a path.
- Preview returns manifest plus typed replacement counts, unresolved/blocking keys, hardware mismatch findings and expected saved version. Commit returns new saved version and safe unresolved summary. Never return `RestorePreview.Configuration` or decrypted secret documents as a wire field.
- Malformed input 400, unsupported content 415, limits 413, unsupported schema/scopes and invalid restore 422, busy 409, stale precondition 412, missing precondition 428, existing non-root denial 403 and inventory/storage failures 503. Match the existing error envelope; no body-bearing library errors or values in logs.

- [ ] **Step 1: Add RED actual Unix/API tests.** `TestBackupAPIRoundTrip` drives the real store and Unix handler to export/create/inspect/preview/restore, asserts correct default/secret scopes and one new saved version, and verifies old live/pending pointer bytes and service/network command receipts unchanged. `TestBackupAPIDeniesNonRoot` drops privileges through the existing socket harness, including secret export and multipart restore.

In the malformed/archive and replay cases, assert actual returned status and current saved state:

```go
before, _ := store.Snapshot(ctx)
response := postRestore(t, client, archive, passphrase, before.SavedVersion)
if response.StatusCode != http.StatusUnprocessableEntity { t.Fatal(response.StatusCode) }
after, _ := store.Snapshot(ctx)
if after.SavedVersion != before.SavedVersion || !bytes.Equal(after.Value, before.Value) {
    t.Fatal("invalid authenticated archive changed saved state")
}
```

`postRestore` is a test-only HTTP multipart builder accepting the real Unix HTTP client, immutable archive bytes, passphrase and expected head. It does not replace parsing, store publication or authorization. Use the appropriate literal expected status per malformed/limit/precondition case, not 422 for all failures.

`TestBackupCaptureConcurrentSaveAndActivation` uses actual flock holders and controlled store/generation writers; archive source saved version, document hashes and generation observation must correspond to the held capture boundary, not a mixed sample. `TestBackupCaptureCancellationReleasesLocks` times out a held generation lock and proves subsequent normal operations proceed.

`TestRestoreAPINoMutationOnInvalidArchive` covers wrong password, truncated authenticated stream and oversized multipart before commit. `TestRestoreAPIStaleHeadAndReplyLoss` previews at version 1, saves another version and asserts stale restore cannot change it; separately drop a reply after one committed restore and assert resend with old expected version cannot publish twice. No mocked publication.

- [ ] **Step 2: Run RED.** Build root engine/generation integration binaries and run the new selected tests from `/`, with a real freshly built test engine where the existing harness requires it. Expected: missing paths/functions or specified safety assertions fail before implementation.

- [ ] **Step 3: Implement capture and endpoints.** `Observe` validates the same trusted generation path and takes the real flock with bounded context waits. Initialize only its private lock directory/file if absent, before capture, so a racing first activation cannot escape observation. Read live/pending pointers and the live manifest; within that boundary invoke `store.Export`. Absent pointers mean baseline provenance; malformed existing state fails, never guesses. Acquire generation lock before store access, release both before JSON container encoding, KDF or interactive client waits. Update the existing private-lock comment because a bounded engine observation now takes it; do not expose a process-lifetime engine-held lock.

In `backup_api.go`, use one `sync.Mutex.TryLock`-style admission gate for expensive export/decrypt/restore operations, not a persistent job system. Limit request body before multipart parsing, iterate parts with bounded readers and never call disk-spooling helpers. Decode/authenticate every archive document and call `config.ValidateBackupDocuments` on all payloads before selecting restore inputs. Inspect may omit selection; preview/commit with selection use the store interfaces, and commit revalidates under CAS.

Assess hardware against the existing link-identity matching rules and return actionable missing/mismatched bindings without guessing replacement interfaces. Reuse the existing renderer's matching policy, extracting a narrow pure helper only if needed rather than duplicating MAC/interface resolution. Never inject dummy secret values or treat incomplete rendering as proof that configuration is realizable. Existing apply gates remain authoritative on later realization.

Add `var buildVersion = "development"` in the engine backup implementation as the explicit source-build label. Set it for Debian builds using Go `-ldflags '-X github.com/andaru-project/andaru/internal/engine.buildVersion=<Debian package version>'` from `dpkg-parsechangelog -SVersion` in `debian/rules`. Capture the running binary's label, not `dpkg-query` after another package may have replaced files. Do not add an unrelated version-command framework. Update OpenAPI with the exact typed bodies, status envelopes, secret write-only fields, content types and limits.

- [ ] **Step 4: Verify GREEN and nearest gate.** Run new tests plus full root config/engine/generation suites, `go test ./...` and `go vet -tags=integration ./...`. Add a built-package assertion that archive `andaruVersion` equals the embedded package version; a development build must truthfully report `development`, not invent a release version. Verify logs/responses lack literal test passphrases and secret values. Expected: pass without IP listeners, state extraction or network mutation.

- [ ] **Step 5: Commit.** Stage only named task files and `git commit -m 'feat: expose root-local portable backup and restore APIs'`.

### Task 5: Add terminal CLI and durable no-overwrite output

**Files:** create `cmd/andaru/backup.go`, `backup_linux.go`, `backup_test.go`, `backup_integration_linux_test.go`; modify `cmd/andaru/{main.go,ctl.go}` only at command dispatch/help/HTTP-sharing seams.

**Interfaces:** consumes Task 4 API/OpenAPI. Add `runBackup(ctx context.Context, socket string, args []string, stdin io.Reader, stdout,stderr io.Writer) int` dispatched from `runCtl` when the first verb is `backup`. Keep parsing and binary/multipart handling out of the existing JSON-only response validator. Linux terminal/file operations live in `backup_linux.go`; reuse existing x/term state/restore primitives and unix polling. Qualification amendment: disable echo before displaying prompts and preserve a valid descriptor for restoring terminal state on cancellation.

Commands are exactly the three spec commands: `create --output FILE [--include-secrets]`, `inspect --input FILE`, `restore --input FILE --stores configuration[,configurationSecrets]`. Require a terminal for creation of secret archives, decryption and restore confirmation. Plain inspection and ordinary secret-free creation need no terminal. Reject extra/repeated/unknown arguments and scopes; do not add `--yes`, password flags, remote URLs or alternate-store paths.

- [ ] **Step 1: Add RED consumer tests using actual CLI/PTY.** `TestBackupCLITerminalSecretsAndRestore` uses the existing PTY dependency to enter matching literal test passphrases and `RESTORE`; assert those inputs never appear echoed or in stdout/stderr/status, and that wrong confirmation or passphrase mismatch leaves file/store unchanged. `TestBackupCLIRejectsNoninteractiveSecretsAndRestore` uses pipes and asserts clear refusal with no output artifact/mutation.

`TestBackupCLIUsesSameArchiveAfterPreview` replaces the source file after preview while the CLI waits at the PTY; commit must still use the original bounded immutable bytes. `TestBackupCLIOutputNoReplace` creates a prior artifact and races an output appearance during publication; assert its exact bytes survive and no symlink is followed. `TestBackupCLIOutputSyncFailure` injects a syscall-boundary failure and asserts no success message, without claiming simulated power loss.

```go
// In the real-file no-overwrite case, with existing content "KEEP_ME":
code := runBackup(ctx, socket, []string{"create", "--output", output}, input, &stdout, &stderr)
if code == 0 { t.Fatal("overwrote existing artifact") }
raw, err := os.ReadFile(output)
if err != nil || string(raw) != "KEEP_ME" { t.Fatal("existing backup changed", err) }
```

- [ ] **Step 2: Run RED.** `go test ./cmd/andaru -run Backup -count=1`; compiled root CLI suite from `/` with `-test.run Backup`. Expected: missing verb/helper or failed file/state/PTY assertions, not a test-only echo stub.

- [ ] **Step 3: Implement CLI and file boundary.** Read each input once into bounded owned bytes, opening a regular file without following an unsafe target; reject oversize before upload. Use `/dev/tty` as the controlling terminal, establish no-echo state before writing the prompt, read a bounded context-aware line, and restore original terminal settings before closing the descriptor on every path. Do not reuse `stdin` as a secret password source. Use the normal explicit JSON error envelope and status output, no archive bytes on stdout.

Write received backup bytes to an O_EXCL/O_NOFOLLOW private same-directory staging file, with root ownership/mode 0600 in a trusted non-writable-by-others parent. Sync, publish with no-replace semantics (existing `x/sys/unix` primitive, no path-stat-then-overwrite), and sync parent. Never overwrite or broaden to a different directory on error. Remove only owned incomplete staging. Do not claim all NFS/SMB mounts provide local fsync semantics.

Preview prints source/selected stores/counts/unresolved keys/hardware mismatches and the fact that saved restore does not apply networking. Read typed `RESTORE` from the terminal, then post exact bytes/options and expected saved version. No automatic conflict retry; after a lost reply report uncertainty and tell the operator to inspect the current saved head. Keep transport/password/input/output failures distinct without exposing sensitive bodies.

- [ ] **Step 4: Verify GREEN and nearest gate.** Run new unit/root CLI cases and the complete CLI suite with the real engine. Confirm archive files can be inspected independently, plaintext scratch files are absent and terminal echo state is restored on cancellation/errors. Run `go test ./...`. Expected: all pass; existing `ctl` JSON commands and socket options work unchanged.

- [ ] **Step 5: Commit.** `git add cmd/andaru/backup* cmd/andaru/main.go cmd/andaru/ctl.go && git commit -m 'feat: add terminal portable backup and restore commands'`.

### Task 6: Qualify packaged behavior and publish precise operator/compatibility guidance

**Files:** modify `.github/workflows/ci.yml`, `scripts/ci/install-test.sh`, `debian/changelog`, `docs/dev/core-api.md`; add `scripts/ci/backup-check.sh` only to keep a substantial disposable package scenario out of the existing driver. Add no production units or backup scheduler.

**Interfaces:** consumes all earlier contracts. Existing root config/engine/CLI/generation suites select adjacent new tests automatically; pure archive tests belong to the normal Go suite. `backup-check.sh` runs in the existing Debian 13 systemd package container, called from the install driver in both dependency installation orders. Existing upgrade smoke remains enabled.

- [ ] **Step 1: Add and observe a failing installed-package scenario.** Against the pre-feature package, run the new `ctl backup` command and record the expected unsupported-command failure. The completed scenario exports default and encrypted test archives, checks embedded source package identity, modifies saved state, inspects/restores through a real terminal session and verifies a new saved version without changing live pointer/service invocation IDs or rules. Do not count an absent command as a runtime feature pass or run destructive actions on the host.

For the independent old-package proof, create a task-owned detached linked worktree at baseline `4463aa2`, outside the primary repository under the usual worktree policy. Run its `scripts/ci/build-packages.sh` for `any`/`all` into a clean external `OLD_DIST`, then run the current feature tree's `scripts/ci/install-test.sh OLD_DIST` with the new backup scenario. Expected RED: `andaru ctl backup` is unsupported and the driver exits nonzero. Use the same current driver on the completed candidate's clean package directory for GREEN. The package helper requires `git ls-files`, so a `.git`-less export cannot drive that helper; never copy a linked worktree's `.git` into a scratch export. Clean up the detached build worktree under the normal policy after proof.

- [ ] **Step 2: Wire package proof and docs.** The disposable package test can install independent age/PTY fixture tools inside its test container, not the developer host. Use only literal public fixture secrets. Check default ZIP is secret-free, encrypted interoperability, config-only unresolved behavior, deliberate repair and normal separate preview/apply where safely qualified. Update CLI help/OpenAPI/user guide/changelog with exact commands, scope limits, root authorization exception, stored-vs-live semantics and no auto-apply/auto-network-change promise.

Compatibility notes must state: supported older documents/archives undergo validated conversion when their configuration is selected; secrets-only restoration preserves the destination declaration and its existing schema 1 or 2. A v2 saved head cannot be used by an older engine; installing old software alone does not revert its state; use the matching cold state/package recovery set when downgrading. No database backups masquerade as portable archives.

- [ ] **Step 3: Run the single final broad gate.** `go vet -tags=integration ./...`; `go test -race ./...`; `GOARCH=arm64 go build -o /dev/null ./cmd/andaru`; shellcheck/actionlint/shellspec for affected existing gates; native root config/engine/generation/CLI/upgrade integration binaries from `/` with TMPDIR unset; existing privileged render/Kea/networkd checks in their qualified container; Debian any/all builds/lintian; installed-package proof in both orders. Preserve output logs, diagnose failures once and fix causes, never weaken tests/limits. Expected: all pass and no host package installation/network change.

- [ ] **Step 4: Execute the two-baseline VM qualification.** Use the established netplan and ifupdown Debian 13 VMs and authenticated existing control channel. Before tests, shut each VM down, take a named disk-only snapshot with no RAM, and restart. Record exact software/build and fixture identity. Qualify default/secret archive, save-only concurrent writer, restore/restart, same-name collision, unresolved-secret repair, separate reviewed network apply/rollback and real lock cancellation. Compare live generation pointers, per-table firewall content and network-service invocation IDs across backup and saved-only restore.

Measure work-factor-18 memory/headroom and one-operation admission under load on both baselines; do not silently reduce the KDF or relax fixed caps if a baseline fails. Record the failure and obtain an explicit design ruling if limits must change. For actual applies, retain console access and the ordinary independent rollback timer. Restore both VMs to their intended pre-test disk snapshots when finished: shut down, rollback, restart. Do not snapshot live machines, include RAM, leave test secrets/archives or claim authenticated SSH/power-loss/arm64-hardware proof not executed.

- [ ] **Step 5: Commit qualified docs/gates.** Include executed evidence and explicit remaining limits, not invented completion. `git add .github/workflows/ci.yml scripts/ci/install-test.sh scripts/ci/backup-check.sh debian/changelog docs/dev/core-api.md && git commit -m 'test: qualify portable backup and saved-state restore'`.

## Plan self-review and final handoff

Coverage: Task 1 owns schema/unresolved/reference safety; Task 2 coherent logical stores, replacement/history/CAS and independent selection; Task 3 archive/encryption/hostile-input limits; Task 4 provenance/auth/API/coherent capture; Task 5 terminal/no-overwrite/filesystem boundary; Task 6 packages, full gates, VM reset, compatibility/operator docs. All five Review Focus cases have named consumer tests in their owning tasks. No requirement is delegated to fake users/extensions or deferred within this approved foundation.

Interfaces: store and archive raw-document types stay separate; the engine performs the conversion. Both preview and commit use one pure restoration preparation path. Source saved versions are provenance only, never destination preconditions. Generation observation takes the generation lock before store access and releases it before encryption. Runtime source identity comes from the running build, not mutable dpkg files.

Final review uses one fresh condensed reviewer after task completion, not a reviewer per task. Re-grade all findings including nits by consumer effect. For Important/Critical fixes, use one precisely scoped fix pass with regression proof and a green suite; no re-review loop. The owner requested subagents perform changes; the parent integrates and verifies them. Record author rulings and qualification limits. Publish through PR/pr-watcher and request eligible Codex/CodeRabbit reviews after CI is green. Do not infer quota-bypass permission from the separate #56 merge decision.

The owner approved this plan and subsequently authorized Astra's review corrections and scoped implementation subagents. Design documents do not themselves establish passing runtime qualification; execution evidence is recorded separately.
