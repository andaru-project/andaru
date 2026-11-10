# Saved Network Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Save validated links, segments, zones and their named secrets through the existing root-local API/CLI, with durable history and optimistic concurrency, without activating networking.

**Architecture:** `internal/config` owns declaration validation, atomic change evaluation and the paired SQLite publication protocol. The existing engine authorizes Unix peers before dispatching to a concrete configuration handler; the CLI uses that same API. Configuration and secrets are immutable content-addressed documents, and only the configuration database's saved-head commit publishes a pair.

**Tech Stack:** Go 1.27.3, native `encoding/json/jsontext`, JSON Schema 2020-12 via `github.com/santhosh-tekuri/jsonschema/v6 v6.0.3`, `modernc.org/sqlite v1.62.0` with its exact `modernc.org/libc v1.80.1`, and `golang.org/x/sys v0.50.0` for Linux filesystem primitives. Standard-library tests; no ORM, frontend or new API framework.

**Spec:** [`../specs/2026-11-10-saved-network-configuration-design.md`](../specs/2026-11-10-saved-network-configuration-design.md), approved in conversation including the correction to named, scoped secrets. Read the complete spec before executing a task. This is a new implementation plan requiring its own owner review/execution choice, not permission to start product code now.

## Global Constraints

- Keep `/run/andaru/engine.sock`, UID 0 `SO_PEERCRED`, authorization before routing/body processing, private runtime/state directories `0700`, files/sidecars `0600`, and no IP listener. No identity headers or application-authentication stub.
- Default state root `/var/lib/andaru`; `configuration/config.db` and `configuration/secrets.db` publish together. State must be on a local filesystem supporting SQLite WAL and no-replace directory rename, not an NFS/SMB backup destination. Never erase, repair or initialize one member over existing state.
- WAL, `synchronous=FULL`, foreign keys and secret `secure_delete=ON` on every physical connection; one connection per database. Retain history and failed-publication orphans in this slice; no pruning/export/restore service or forensic-erasure claim.
- Secrets use case-sensitive names matching `[A-Za-z_][A-Za-z0-9_]{0,63}` in their owning scope, initially `core`; no secret UUID. `WAN_PASSWORD` may have several authorized core referrers. Values remain absent from API/CLI/history/errors/logs. Stored data, write commands and redacted responses have different schemas.
- Body limit 1 MiB, depth 32, 100 operations, 100 safe field details; each stored document at most 4 MiB and configuration at most 10,000 objects. Limit password values to 4096 UTF-8 bytes. Reject invalid UTF-8, lone surrogates, duplicate names, any null, trailing JSON and unknown fields.
- Preparation budget 3 s including SQLite busy waits capped at 2 s; final two-database publication shares an independent 2 s bound, not the HTTP request's cancellation lifetime. Mutation client timeout 10 s; reads retain 5 s. Store publication failure fences writes and exits the engine non-zero for systemd revalidation; it never claims a failed acknowledgement proves nothing committed.
- Versions are canonical decimal strings (`0` or no-leading-zero positive decimal, maximum signed SQLite integer), never JSON floating-point numbers. Initial saved version `0` is empty and has no live generation. ETags are quoted `"<network-object ID>.<last-changed version>"`, only on individual GET/HEAD; no ETag on transformed PUT responses.
- Per-object/secret stale version `412`; missing required version `428`; never-existing object `404`; validation/name conflict `422`; malformed JSON/precondition `400`; oversize `413`; unavailable store `503`. No blind wildcard writes or automatic mutation retries.
- Saves never write networkd/nft/service configuration, change interfaces/forwarding, or manufacture confirmed/live state. No draft, GraphQL, sessions, GUI scaffolding, generic secret CRUD, extension grants, runtime test flags or production fault-injection callbacks.
- Work in this linked worktree or a fresh task worktree based deliberately on current main. Product implementation gets its own PR and one final condensed adversarial implementation review; the earlier no-PR exception applies to exploratory design documents only.

## Review Focus

1. A same-object race or A→B→A transition must not revive stale tokens; disjoint edits must survive. A shared named-secret update must version every referrer while retaining unrelated fields. Task 2 owns pure transitions, Task 3 real database races, Task 4 response/ETag proof.
2. Secret-first/config-second publication and first initialization must survive termination without a published missing secret, overwritten existing directory, or dishonest success. Task 3 owns real child-process/crash/ENOSPC tests; Task 6 exercises the final executable.
3. Invalid Unicode/duplicate keys, unsafe schema references or value-bearing validator errors must not change or disclose a password. Task 1 owns strict parsing/offline schemas, Task 2 secret command validation, Task 4 actual HTTP failure output.
4. Numeric delegation collisions, host-bit masking, MTU layering and namespace/reference confusion must be rejected before persistence without treating missing NICs as a save failure. Tasks 1–2 own model/reference tests; Task 6 proves no host realization.
5. Root peer checks, shutdown, stdin/body bounds and SQLite connection settings must remain correct through the real CLI/systemd path, including a fenced store or cancelled client. Tasks 3–5 own regressions; Task 6 verifies the retained Debian VM.

## File Ownership and Dependencies

| Owner | Files |
| --- | --- |
| Task 1: declaration boundary | `internal/config/model.go`, `json.go`, `schema.go`, `schema.json`, `validate.go`, `canonical.go`, `model_test.go`, `testdata/network.json`; `go.mod`, `go.sum` |
| Task 2: changes and public views | `internal/config/changes.go`, `secrets.go`, `views.go`, `changes_test.go`, `testdata/changes.json` |
| Task 3: durable store | `internal/config/store_linux.go`, `sqlite.go`, `history.go`, `store_integration_linux_test.go`; new `internal/securefs/path_linux.go`; move existing root path checks from `internal/engine/socket_linux.go`; `go.mod`, `go.sum` |
| Task 4: API and engine lifetime | `internal/engine/api.go`, `config_api.go`, `openapi.json`, `api_test.go`, `config_integration_linux_test.go`, `socket_linux.go`, `socket_integration_linux_test.go`; engine-mode flags in `cmd/andaru/main.go`; `packaging/systemd/andaru-engine.service` |
| Task 5: real CLI | `cmd/andaru/main.go`, `ctl.go`, `main_test.go`, `ctl_integration_linux_test.go`; `docs/dev/core-api.md`, `README.md` |
| Task 6: shipped proof | Operator evidence in `docs/dev/core-api.md`; compiled binaries only in the executor-owned ignored build directory; no new feature/service file |

Tasks run in this order. Existing `internal/host` behaviour is unchanged. Task 4 must migrate every `engine.Serve` caller in the same commit; Task 5 must migrate every internal `run` caller when adding stdin. Use native LSP references first when a Go server is available; the previous session found no Go LSP, in which case enumerate exact references with repository search. No compatibility wrapper or nil production backend.

## Shared Types and Interfaces

All configuration types below live in `internal/config`; secret documents and mutable prepared state stay private to that package. The engine never receives a plaintext snapshot. Use typed declarations matching the spec's flat object fields; JSON Schema gates the kind-specific union before typed semantic checks.

```go
type Version string // ParseVersion enforces the canonical signed-int64 range.
type FieldError struct { Path string `json:"path"`; Message string `json:"message"` }
type ObjectView struct { Value json.RawMessage `json:"value"`; Version Version `json:"version"` }
type CollectionView struct {
    State string `json:"state"`; SavedVersion Version `json:"savedVersion"`; Items []ObjectView `json:"items"`
}
type SnapshotView struct {
    State string `json:"state"`; SavedVersion Version `json:"savedVersion"`
    Value json.RawMessage `json:"value"`; UnusedSecrets []string `json:"unusedSecrets"`
}
type Operation struct {
    Op string `json:"op"`; Collection string `json:"collection"`
    ID string `json:"id,omitempty"`; LocalID string `json:"localId,omitempty"`
    ExpectedVersion *Version `json:"expectedVersion,omitempty"`; Value json.RawMessage `json:"value,omitempty"`
}
type Changes struct { Operations []Operation `json:"operations"` }
func DecodeChanges(data []byte) (Changes, error)
func ParseVersion(s string) (Version, error)
```

`SaveResult` contains `state`, `savedVersion`, `items` of `{collection,id,deleted?,value?,version?}` for explicit targets plus induced affected referrers (deduplicated), and `created` (local-create label → real ID). A no-op still returns the requested object's current view/version with the unchanged saved head; history/change counts include only actual changes. Single-object adapters select their response from this same read snapshot, never a racy post-save re-fetch. `VersionInfo` contains `id`, `parentId` (omitted for 0), `createdAt`, `actor`, `changedObjectCount`; `HistoryView` contains `state`, `items`, optional `nextBefore`; `VersionView` combines that metadata, its secret-free `value` and bounded changed identities. All empty arrays are `[]`.

`Fault` implements `error` with a safe `Code` and `Details []FieldError`, plus optional `Current`. `Current` is a concrete redacted union: object `{kind:"object",collection,id,value,version}`, tombstone `{kind:"object",collection,id,deleted:true}`, or secret `{kind:"secret",name,version}`. No private cause/value/hash is formatted by `Error()`. Map these codes in the engine, not SQL/HTTP objects in domain logic. Public response blobs are produced only by the redaction helpers in `views.go`.

```go
func Open(ctx context.Context, stateDir string) (*Store, error)
func (s *Store) Close() error
func (s *Store) Save(ctx context.Context, input Changes) (SaveResult, error)
func (s *Store) Snapshot(ctx context.Context) (SnapshotView, error)
func (s *Store) List(ctx context.Context, collection string) (CollectionView, error)
func (s *Store) Get(ctx context.Context, collection, id string) (ObjectView, Version, error)
func (s *Store) History(ctx context.Context, before *Version, limit int) (HistoryView, error)
func (s *Store) Version(ctx context.Context, id Version) (VersionView, error)
func (s *Store) Failure() <-chan error // buffered, safe fatal failure only; never validation conflicts.
```

`Get`'s second result is the snapshot version for `Andaru-Saved-Version`, not part of the object's ETag-covered JSON body. Read/write request preparation and errors preserve the spec's precedence. An unrecognizable operation envelope/identity is a shape error; precondition precedence applies once the target operation can be identified. A create rejects client-provided IDs; a replacement may omit `value.id` or repeat its route ID, but cannot change it.

### Task 1: Validate and canonicalize the network declaration

**Interfaces:** produces private `declaration`, `decodeStrict([]byte) (any,error)`, `decodeDeclaration([]byte) (declaration,error)`, `validateTopology(*declaration) []FieldError`, `canonical(any) ([]byte,error)` and lazy-compiled `schemaSet`; public `Version`, `FieldError`/`ParseVersion` and embedded `SchemaBundle []byte`. No persistence.

- [ ] Create the declaration fixture with Ethernet 1500 → VLAN 6/1500 → PPPoE 1492; WAN DHCPv6 prefix request 48; LAN on another VLAN with `192.0.2.7/24` and delegated `abba`; exactly one zone per segment. Use explicit test-only UUIDv4 network IDs and `WAN_PASSWORD` as the stored reference, never a secret UUID. This is a declaration fixture, not an assertion about the owner's actual hardware or ISP settings.
- [ ] Write `TestStrictJSONBoundary` with the assertions below, plus depth 32 accepted/33 rejected, any null rejected and 1 MiB+1 rejected before schema conversion. `TestNetworkConstraints` changes exactly one condition per valid fixture: wrong-kind fields, missing IDs/names, duplicate MAC/name/VLAN tag, topology cycles/enslaved parents, empty bridge accepted/non-empty bond required, MTU 1279 with IPv6 rejected, VLAN 1501 over 1500 rejected, PPPoE 1493 over 1500 rejected, family/prefix mismatch, /31 and /32 accepted, prohibited local addresses, allowed IPv6 link-local reuse, overlap and numeric delegation `1`/`0001` collision. Missing physical NICs do not cause a semantic error.

```go
func TestStrictJSONBoundary(t *testing.T) {
    for _, raw := range []string{`{"p":"\ud800"}`, `{"p":1,"p":2}`, `{} {}`, `{"p":null}`} {
        if _, err := decodeStrict([]byte(raw)); err == nil { t.Fatalf("accepted hostile JSON: %q", raw) }
    }
    if _, err := decodeStrict([]byte(`{"p":"\ud800\udc00"}`)); err != nil { t.Fatal(err) }
}
```

- [ ] Run `go test ./internal/config`; expect missing symbols/package implementation (RED), not a fixture/import typo. Add `jsonschema/v6@v6.0.3` with `go get`, then `go mod tidy`; commit real `go.sum`, not hand-edited lock content.
- [ ] Implement the strict pass using native Go 1.27.3 `jsontext.NewDecoder`, `ReadToken`, `StackDepth`: reject null/depth >32, require exactly one complete top-level value and EOF, with duplicate names and invalid UTF-8 disallowed. Do not write a string/escape parser. Convert the validated instance with `jsonschema.UnmarshalJSON` (preserves `json.Number`) and then typed models. `DecodeChanges` later reuses this boundary; do not accept library error strings as safe output.
- [ ] Embed a 2020-12 bundle with `$defs/declaration`, `objectWrite`, `changes`, `secretDocument` and kind-specific auxiliaries. Register it under the fixed absolute logical URI `https://andaru.invalid/api/v1/schema`; `DefaultDraft(Draft2020)`, `AssertFormat()`, and `UseLoader(jsonschema.SchemeURLLoader{})`. Compile once via `sync.OnceValues`, not per request. `TestSchemaLoaderIsOffline` uses a valid external temp-file schema: it must refuse compilation instead of loading it. Map causes/keywords to fixed safe messages and trusted-field JSON Pointers, maximum 100; raw syntax/validator errors and arbitrary unknown-key text are never echoed.
- [ ] Normalize domain addresses/MACs/subnet IDs and unordered arrays, then use native `jsontext.Value.Canonicalize(jsontext.CanonicalizeRawInts(false))` on serialized typed values and append exactly one newline; no alternate JSON file store. `TestCanonicalNetworkRoundTrip` compares equivalent input permutations for byte/hash equality, retains `192.0.2.7`, `<`/Unicode values and empty arrays, and refuses corrupted/unknown-version documents. Document-number fields are bounded integers; versions are strings.
- [ ] Run `go test ./internal/config`; expect all model/strict/schema/canonical cases PASS. Commit `feat: validate canonical saved network declarations` with only Task 1 files.

### Task 2: Evaluate atomic object and named-secret changes

**Interfaces:** consumes Task 1; produces `Changes`, `Operation`, all public view/fault types and `DecodeChanges`; private `prepareChanges(current *savedState, input Changes, next Version, now time.Time) (preparedChange,error)`. The caller exclusively owns this loaded state; no unnecessary whole-state clone or shared mutable cache. `savedState` holds version, typed declaration, secret document and relevant indexed object-version/tombstone metadata. `preparedChange` contains resulting state, actual affected identities, explicit result identities (including no-ops), create-ID mapping and `Noop`.

- [ ] Write `TestChangePreconditionsAndAtomicity`: known object version 7 → save at 8; stale 7 afterward fails; A→B→A gets 9 rather than 7; missing version gives 428; never-existing ID gives 404; tombstone/stale gives 412; duplicate name gives 422; missing/weak/wildcard/malformed conditions never become a blind write. Unit helpers construct owned valid states, not fake repositories.
- [ ] Write `TestNamedSecretSharing`: two PPPoE links share core `WAN_PASSWORD` at version 7. Setting at expected secret 7/next saved 8 changes that entry and both object versions; a prior unrelated rename remains; stale secret 7 loses; expected 0 loses when the name exists; omission of set-preconditions is 428. Binding existing names does not require a secret write. Equal set values leave the whole pair/version unchanged. Deleting one referrer retains the named value and historical states; the other still resolves it. Test these actual transitions, not forwarding/copy counts.
- [ ] Write `TestBatchLocalReferences` for atomic Ethernet/zone/segment creates with `@localId`, matched generated UUIDs, unresolved/wrong-kind labels, duplicate targets and a refused referenced deletion. A name/description containing `@uplink` must not be rewritten as a reference. `TestSecretViewsAndErrors` asserts names/version/updatedAt are available but the test password and secret-document hash never appear, including malformed `set`, attempted scope selection, read-back metadata, duplicate secret sets, invalid Unicode and unused-name metadata.
- [ ] Run `go test ./internal/config -run 'Test(Change|Named|Batch|Secret)'`; expect the new evaluator/view symbols to fail (RED).
- [ ] Implement one evaluation path: parse recognizable envelope/precondition syntax; check all expected object/secret versions before mutations/no-op decisions; allocate network UUIDs with `crypto/rand`; resolve only declared network-reference fields; apply operations; update named secret metadata and every referrer; validate final schemas/topology/reference ownership; canonicalize and determine a true pair no-op. New names require explicit expected secret version `0`; secret names have no UUID, local-label or cross-scope interpretation. All operations are evaluated before any database publication. Read-body metadata is not accepted as a command.
- [ ] Implement safe public view construction. Snapshot `value` redacts PPPoE bindings to named metadata and outer `unusedSecrets` is sorted names only; secret values never leave this package. Direct and induced referrer changes are recorded once each. Private metadata carries every required changed version; it is not reconstructed from wall-clock order.
- [ ] Run `go test ./internal/config`; expect all transitions PASS. Commit `feat: evaluate version-checked network and named-secret saves`.

### Task 3: Publish durable configuration/secret pairs with real crash proof

**Interfaces:** consumes Tasks 1–2; produces concrete `Store` and methods above; private genuine protocol phases `prepareSave`, `commitSecrets`, `commitConfiguration` for production ownership and test coordination (not callbacks/options). Extract existing filesystem policy into `securefs.CheckAncestors(path string) error` and `securefs.RootOwned(os.FileInfo) bool`; migrate engine callsites without changing socket behaviour or duplicating policy.

Private protocol signatures are `(s *Store).prepareSave(ctx context.Context,input Changes) (*pendingSave,error)`, `(p *pendingSave).commitSecrets(ctx context.Context) error`, `(p *pendingSave).commitConfiguration(ctx context.Context) (SaveResult,error)` and `(p *pendingSave).Close() error`. The pending owner holds the gate, leased config connection and transaction-active flag; preparation errors release everything, and the Save orchestrator always closes it before reporting a result. A no-op returns its current views without a publication. Never return a connection with an active manual transaction to the pool: rollback within the remaining operation budget, and on rollback/publication uncertainty discard the physical connection using the standard `sql.Conn.Raw`/`driver.ErrBadConn` invalidation path and fence the Store rather than reuse it. The close path is shared by production and deterministic protocol-helper tests.

- [ ] Before the first VM write, create and verify a new `pre-saved-config-<UTC timestamp>` snapshot of proxmox-4/114 using `/root/omp-transport-test/pve.sh POST /nodes/proxmox-4/qemu/114/snapshot snapname=<name> vmstate=0`, wait for its returned UPID and require task `OK`, then read back the snapshot entry. Credentials remain in the existing private helper. Keep NICs/workloads unchanged. Retain this snapshot for the later deployment proof.
- [ ] Add root-required `//go:build integration` tests `TestStoreInitializationAndPaths`, `TestStorePublicationAndConcurrency`, `TestStoreCrashBoundaries`, `TestStoreENOSPCAndFencing`, `TestStoreConnectionPragmas`. A non-root explicit tagged run fails rather than skips. Cover existing empty published directory, unsafe path/owner/symlink, stale bootstrap marker/tree, both/one missing member, unknown layout/app ID, corrupted head/document, second engine with another socket, reconnect pragmas, consistent reads during writes, disjoint and same-object races, shared secret versions, and cancelled preparation versus detached publication.
- [ ] Build the tagged test binary before implementation with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/config.test" ./internal/config`; expect missing Store/protocol symbols (RED). Use the executor's ignored plan workspace as `BUILD_DIR`. Add `modernc.org/sqlite@v1.62.0`, `modernc.org/libc@v1.80.1`, `golang.org/x/sys@v0.50.0`; run `go mod tidy`. Keep libc exactly matched to sqlite; do not upgrade it independently.
- [ ] Implement a context-aware one-token store gate (queue time is in the preparation/read deadline), root-only canonical state paths, root-owned private `engine.lock` held with `unix.Flock(LOCK_EX|LOCK_NB)` for the Store lifetime, and fixed `.configuration-init` bootstrap/marker/no-replace rename/fsync. Published database members must be regular root-owned 0600 files, not symlinks or hardlinks. Probe existing application/layout identity through SQLite `mode=ro` without mutating DSN pragmas; close all probe connections before opening the verified pair read-write. Unknown stores must not be converted to WAL before refusal. Bootstrap uses URI `mode=rwc`, verified members `mode=rw`. Use `unix.Renameat2(..., RENAME_NOREPLACE)`; unsupported/EEXIST is refusal, never overwrite.
- [ ] Build SQLite file URIs with `net/url`, not string concatenation. Fixed `_pragma` values: `busy_timeout(2000)`, `foreign_keys(1)`, `journal_mode(WAL)`, `synchronous(FULL)` and secret-only `secure_delete(1)`. Apply on every physical connection via the DSN, verify read-back when leasing a connection, and set maximum open/idle connections to 1. Never open/close a live database file with `os.File` for hashing/inspection: doing so can release SQLite's POSIX locks. Hash canonical document blobs and use SQLite APIs for database checks.
- [ ] Use `sql.Conn` with explicit parameterized `BEGIN IMMEDIATE`/`COMMIT`/`ROLLBACK`, not a `BeginTx` tied to the HTTP context. A private prepared-save owner holds the leased config connection and gate across both phases. Abort preparation on cancellation; final publication gets its own shared 2 s context. Any uncertain publication I/O/timeout closes/fences the Store, sends one safe failure notification and prevents success. Busy/cancelled preparation with a confirmed rollback does not invent history. Cleanup cannot leak a live transaction back to the pool.
- [ ] Create layout version 1 via `PRAGMA user_version=1`; application IDs `0x414e4443` (ANDC) and `0x414e4453` (ANDS) distinguish members. `config.db`: `documents(sha256 BLOB PRIMARY KEY CHECK(length(sha256)=32),body BLOB NOT NULL)`; immutable `versions(id INTEGER PRIMARY KEY,parent_id REFERENCES versions,config_sha256 REFERENCES documents,secrets_sha256 BLOB,created_at,actor,changed_object_count)`; saved `pointers(name PRIMARY KEY CHECK(name='saved'),version_id REFERENCES versions)`; indexed `object_changes(collection,object_id,version_id REFERENCES versions,deleted, PRIMARY KEY(collection,object_id,version_id))`; `identities(object_id PRIMARY KEY,collection,created_version REFERENCES versions)` prevents ever reusing a network ID. `secrets.db` has its own immutable `documents` with the same hash/body columns. Store schema version lives in each document; do not add a second migration framework/table.
- [ ] Commit an empty secret document first and configuration/version/pointer 0 before bootstrap directory publication. On saves allocate `head+1` under config write ownership, commit canonical secrets first, then insert config/version/change records and advance head in one config commit. Secret-only changes may reuse a config blob but must still advance the saved head and all affected object events. Parameterize every value. Load versions for bounded active/requested IDs in batched indexed queries (a bound JSON ID array with SQLite `json_each(?)` is suitable), not per-object queries or an unbounded tombstone scan. History lists metadata/count only, default 50/max 100, `before` exclusive; full version reads fetch their bounded change list and validated pair lazily.
- [ ] Test coordination stays in the integration binary: invoke the real private protocol phases, exchange stage notifications over inherited pipes, and SIGKILL child processes at before/after secret/config commit and bootstrap rename. Separately kill-sample the unmodified shipped binary later. For ENOSPC use `unshare --mount --propagation private` and task-owned size-limited tmpfs in the VM; the private coordinator retains the mount while restarting engine children. Fill at each publication boundary, remove only the test filler to reopen, and assert fenced exit plus old/new complete state. No mounts or fault flags in the production unit. Report helper-boundary proof separately from shipped-binary sampling; neither is a power-loss proof.
- [ ] Build/transfer `config.test` to a uniquely task-owned `/var/tmp/andaru-saved-config-<commit>/` directory on `andaru-lab` and run through sudo with `-test.timeout=120s`. Expect all store/model tests PASS, including actual child reap/exit results and pragma values. `go test ./internal/config ./internal/engine` must still PASS. Commit `feat: persist crash-safe paired configuration history`.

### Task 4: Expose the saved contract and integrate the engine lifecycle

**Interfaces:** `newHandler(readLinks func(context.Context)([]host.LinkStatus,error), configuration http.Handler) http.Handler`; `newConfigurationHandler(store *config.Store) http.Handler`; exported `engine.Serve(ctx context.Context, socketPath, stateDir string) error`. Existing `serveUnix(ctx,path,handler)` stays the transport owner; no optional/nil production store. The API depends on the concrete Store; no single-implementation repository interface.

- [ ] Write root `TestConfigurationHTTPContract` against a real Store: schema/snapshot/history, collection/object create/GET/PUT/DELETE, atomic batch, exact path/method set, GET/HEAD bodies/headers, `state:"saved"`, only individual-read ETags, `Andaru-Saved-Version`, transformed write responses without validators, and 400/403/404/405/412/413/422/428/503. `TestConfigurationHTTPConcurrencyAndSecrets` uses two clients, disjoint/same/shared-secret races, A→B→A, deleted markers, safe current state and error precedence; no mock database or response-echo test.
- [ ] Run `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags=integration -o "$BUILD_DIR/engine.test" ./internal/engine` before adding handlers; expect missing handler/new-signature integration symbols (RED).
- [ ] Implement explicit collection/item/schema/changes/history routing and one-operation adapters into `Store.Save`. Dispatch configuration paths after peer authorization but before the old read-only GET/HEAD guard; each resource owns its methods. Map safe Fault fields into the common error envelope plus optional `error.current`; preserve `details:[]` and headers. Bound reads with `http.MaxBytesReader` before buffering; expose no raw errors, request bodies or private store DTOs. Validate methods before body decoding, but authorization before any route/body work.
- [ ] Extend embedded OpenAPI 3.1.1 to every implemented resource/method and actual input/output/failure DTO. Refer to `/api/v1/schema#/$defs/...` for domain write constraints rather than maintaining another domain schema. Contract tests register both served resources with absolute logical IDs and a deny-all external loader. Update the old exact-two-path/read-method-only assertion: keep the old inventory assertions, move whole-API path/method/schema verification into the real-store integration test; do not just weaken it to “at least two”. Ordinary inventory tests pass `http.NotFoundHandler()` for the absent configuration subsystem under test, not a fake production backend.
- [ ] Change `Serve` and all real/test callers together, including engine-mode `--state-dir` (default `/var/lib/andaru`) in `cmd/andaru/main.go`; it is not a ctl option. Open/validate Store before binding; default CLI `links` cannot appear ready while store initialization failed. Monitor `Store.Failure()` under a bounded context, drain using the existing 3 s shutdown path, return a non-zero cause on fencing and close all monitor goroutines/Store resources. Ordinary signal cancellation remains clean. The HTTP request context must not own an already-started publication.
- [ ] Add `StateDirectory=andaru`/`StateDirectoryMode=0700` to the service while retaining its sandbox, runtime permissions, zero capabilities and host network namespace. Do not grant mount/network capabilities for tests. Reuse `securefs` path policy and existing real UID65534 helper; expand its denied request cases to config writes/schema/history with forged headers. Migrate socket-lifecycle Serve callers to isolated owned state directories, never the real default path.
- [ ] Run `go test ./internal/engine ./cmd/andaru`, then build/run tagged `engine.test` on the retained VM with `-test.timeout=120s`; expect real API/store/peer/lifecycle tests PASS. Commit `feat: expose durable saved configuration over the local API`.

### Task 5: Ship usable configuration/history CLI commands

**Interfaces:** change internal `run` to `run(ctx context.Context,args []string,stdin io.Reader,stdout,stderr io.Writer) int`; main passes `os.Stdin`. `ctl.go` owns typed command parsing/Unix requests, not a second validation/storage engine. Migrate all existing test callers; no legacy wrapper.

- [ ] Use the following fixed grammar for the tests below (global `--socket` stays before the command; leaf flags precede leaf arguments); do not implement it before the red test step:

```sh
andaru ctl config show
andaru ctl config list links
andaru ctl config get links ID
andaru ctl config create links < request.json
andaru ctl config replace --version 7 links ID < request.json
andaru ctl config delete --version 7 links ID
andaru ctl config changes < changes.json
andaru ctl versions list --before 42 --limit 50
andaru ctl versions get 42
andaru ctl schema
```

- [ ] First write `TestCtlConfigurationRoundTrip` under `integration` using the real Unix engine/Store and stdin: create link/zone/segment, read returned stable versions, rename under explicit version, reject stale writes, perform a batch, inspect history, restart and verify persistence. A PPPoE set command carries name/value/secret expected version in stdin, never argv. Delete synthetic records atomically at the end so the saved head is empty and cannot be mistaken for a future intended lab network; retained history is expected.
- [ ] Write ordinary CLI failure tests for missing/malformed object versions, invalid command/collection, bounded stdin, malformed/truncated/unexpected response envelopes, redirects, non-success and unavailable socket. Errors produce no success stdout; no happy fake-server forwarding echoes. Run `go test ./cmd/andaru -run 'TestCtl|TestInvalidArguments'` before implementation; expect new-signature or new-behaviour failures (RED).
- [ ] Implement leaf `flag.FlagSet`s and a fixed operation descriptor for method/path/expected success (200 or create 201)/response shape. Build paths/query parameters from validated IDs/versions/collections, not arbitrary URLs. Use the existing Unix-only dialer, no redirects, 8 MiB response bound, 1 MiB+1 stdin limit, 10 s mutation/5 s read timeout. Construct quoted object If-Match from explicit ID and `config.ParseVersion`; never fetch/substitute a newest version or retry a mutation. Parse public envelopes before stdout, preserve one final newline, check output errors. A timeout says outcome unknown; tell the operator to read current state without printing the submitted body.
- [ ] Update operator guide/README with real commands, schema/password shapes, scope/permissions, saved-not-live labels, persistence/fencing/restart, growth/orphan limitations and local-filesystem requirement. Remove outdated “engine is entirely read-only” statements but keep inventory/host-observation distinctions. Do not imply encrypted backups, apply or recovery exists.
- [ ] Run `go test ./cmd/andaru`; build/run tagged CLI tests on the VM with `-test.timeout=120s`. Expect actual round-trip/history/version/error behaviour PASS. Commit `feat: manage saved network configuration from the CLI`.

### Task 6: Verify the shipped binary in the persistent Debian lab

**Interfaces:** consumes the final binary/unit and the saved snapshot from Task 3. No new feature code; fix defects only through their owning tests and code, never a relaxed check.

- [ ] After integration run `go test ./...`, `go test -race ./...`, `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`, then `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$BUILD_DIR/andaru-amd64" ./cmd/andaru` and the equivalent `GOARCH=arm64` command producing `andaru-arm64`. Require green tests, successful builds and no reachable unmitigated advisories. If an advisory needs a new pin, revise that decision and verify, not force-remediate unrelated packages. Arm64 remains compile-only unless run. Never commit binaries/probe scaffolds.
- [ ] Stage final binary, unit and tagged config/engine/CLI executables under the owned VM directory. Before replacing, verify current installed files are the previous owned artifacts: observed binary SHA256 `91af0318e90d2d161f0d989931aec87d87af7d38472fc470c58c151c5f4e6e77`, unit SHA256 `dc38d8a9c40fa8ffedeff1ef0117ad10ea265efafca7d2ab7acd2f2e0f61a43b`. Investigate any drift, never overwrite it blindly. Keep task-owned previous copies and the snapshot for rollback; promote root-owned binary 0755/unit 0644, run `systemd-analyze verify`, daemon-reload and restart. Leave boot-enable state unchanged.
- [ ] Under one overall 5 s readiness deadline including each attempt, require successful real CLI links/config reads. Exercise the actual installed command with synthetic network declarations and named secrets, stale and atomic saves, a second core referrer, history and service restart. Independently check persisted values/version state through later API reads, root/non-root access, directory/file modes, zero effective capabilities and Unix-only listener. Run the tagged suites on the VM, not only a cross-compile. No Go compiler is needed in the guest.
- [ ] Kill-sample the unmodified installed engine during eight owned-state saves, with delays 0/1/2/5/10/20/50/100 ms after request transmission. Use an isolated test state directory, never the deployed unit's directory, and a fresh owned socket path per restart so stale sockets are not mistaken for database failure. Reopen and verify head/document hashes and pair references after every attempt; do not rerun until a preferred outcome appears. Distinguish this sampling from deterministic private-phase tests. Clean synthetic saved heads separately from host configuration; no arbitrary hardware-power-cut or full-restore claim.
- [ ] Compare the pre-deployment stateless nft rules (ignore counters), IPv4/IPv6 forwarding and management addresses against the final VM. No interfaces/routes/realizers were activated. Verify the lab saved head is empty after the explicit cleanup batch and current unit active but not newly boot-enabled. Record actual binary commit/Go metadata/hash, snapshot name and exercised results in the guide; no screenshots/UI test or reboot claim is needed.
- [ ] Complete one fresh condensed adversarial review of the product branch, fixing should-fix findings with red/green evidence and only affected checks. If a fix changes product code, rebuild/redeploy and repeat the affected VM proof before publishing. Publish committed implementation as a PR, register pr-watcher, request available authorized bot reviews after readiness and handle results. Do not merge without owner approval. Commit actual evidence as `docs: record saved-configuration lab qualification`.

## Self-Review and Execution Handoff

Spec coverage: Tasks 1–2 cover all declaration, strict schema, identity, named secret and view rules; Task 3 owns durable initialization/history/locking/publication; Task 4 covers HTTP errors/ETags/root authorization and process fencing; Task 5 covers real stdin/CLI; Task 6 proves the actual VM and unchanged networking. No accepted spec behaviour is satisfied by a stub, unused store or assumed test pass.

Shared interfaces are defined once above; Task 4 carries the complete Serve signature cutover, Task 5 the run/stdin cutover. All five Review Focus classes have named owning tests. A statement about phase-helper proof is not a claim of instrumenting the shipped product. No external schema loading, secret logging, SQL interpolation, NFS state, broad capability changes or mutable config file fallback is permitted.

Recommend **Native**, as for the prior core slice: these six tasks share schema/state/lifetime interfaces and have sequential dependencies, so implementation fan-out would mostly add rereading and coordination. Preserve one fresh adversarial whole-branch review at the end. The owner reviews this written plan and chooses/confirms execution before product changes.

## Research Evidence and Limits

- `go list -m -json ...@latest` resolved the exact versions pinned above; each accepts Go 1.27.3. `modernc.org/sqlite v1.62.0` requires libc v1.80.1; x/sys is needed for native no-replace rename (the standard `syscall` package has no `Renameat2`). Driver/x/sys BSD-3-Clause and schema Apache-2.0 are compatible with this AGPLv3 project; retain notices. No product dependency was installed during planning.
- A throwaway Go 1.27.3 probe with default `GOEXPERIMENT` accepted valid Unicode/surrogate pairs and depth 32, and rejected lone surrogates, invalid UTF-8, duplicate keys, trailing values, null and depth 33. Native `jsontext` avoids a custom parser; deployments explicitly disabling `jsonv2` are outside this selected toolchain configuration.
- [Go JSON documentation](https://pkg.go.dev/encoding/json/jsontext), [schema compiler](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6@v6.0.3), [SQLite driver](https://pkg.go.dev/modernc.org/sqlite@v1.62.0), [driver changelog](https://gitlab.com/cznic/sqlite/-/blob/v1.62.0/CHANGELOG.md), [SQLite WAL](https://sqlite.org/wal.html), [SQLite transactions](https://sqlite.org/lang_transaction.html), [rename](https://man7.org/linux/man-pages/man2/rename.2.html) and [flock](https://man7.org/linux/man-pages/man2/flock.2.html) informed the exact ownership/context rules.
- Read-only SSH confirmed the retained VM has `unshare`, `mount`, the hashes above and an active existing engine. No service/database/firewall/VM state changed in planning. SQLite publication, ENOSPC, schema error redaction and actual new endpoints have not yet been tested because their implementation does not exist.
