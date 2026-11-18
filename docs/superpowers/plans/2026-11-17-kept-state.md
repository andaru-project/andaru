# Kept state and disaster LAN boot

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep a write-once first LAN and the last successful LAN on the root disk, let an operator store a post-reset network, and bring a LAN up when `/var/lib/andaru` is missing or unreadable.

**Architecture:** `internal/kept` owns the root-disk copies and refuses to nest them inside the wipeable tree. `internal/config` extracts a management-LAN closure from a saved declaration, because that type is unexported. Recording hooks the existing save and confirm paths. Disaster boot renders into `/run` and does not create a replacement state directory. This plan does not implement factory reset.

**Tech Stack:** Go, existing SQLite configuration store, existing generation realizer and renderer, root-only CLI. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-17-factory-reset-data-design.md`, approved 2026-11-17.

## Global Constraints

- `/var/lib/andaru` is the wipeable tree. Reset, which this plan does not build, deletes its contents and does not run `mkfs`.
- The filesystem at that path must be local. SQLite cannot live on NFS.
- Kept copies live at `/var/lib/andaru-kept/`, never inside the wipeable tree. Children are `recovery/`, `first/` and `last-lan/`.
- `recovery/` is the post-reset network settings plus the secrets required to realize that network. It holds no accounts or credential verifiers. Storing it is explicit. A normal save does not update it.
- `last-lan/` is only the LAN and management path from the last successful live configuration. An uplink is never written into it. The product already treats a static IPv4 gateway, DHCPv4, or PPPoE as an uplink.
- `first/` is that same LAN closure, written once when the first setup's LAN comes up, and never replaced. Factory reset does not read it or delete it.
- Success for `last-lan/` is an authenticated administrator changing a setting while that configuration is live. Apply confirmation is not that signal. A package upgrade is not that signal.
- A missing mount is not an empty live tree. Do not create a replacement `/var/lib/andaru` and do not record `first/` or `last-lan/` from it.
- Disaster boot uses `last-lan/` if it can be realized, otherwise `first/`. It does not apply `recovery/`, wipe surviving accounts, or create `admin` / `admin`.
- `/var/lib/andaru-upgrades` is not read or deleted. Package maintainer scripts must not delete `/var/lib/andaru-kept/` or `/var/lib/andaru-upgrades/`.
- Kept copies are root-owned, directories mode 0700, files mode 0600. They are not exposed by the API, a debug export, or a portable backup.
- No network endpoint, no default address, no factory-reset command, no add-on installer, no partition migrator.
- Until `andaru setup` exists, `first/` is written when the first confirmed generation's LAN comes up. That is a plan decision, not a second success signal.

## Review Focus

1. A missing `/var/lib/andaru` mount must not be created and then recorded as `last-lan/` or `first/`. Task 1 pins the refusal. Task 5 pins that disaster boot does not create it.
2. A management segment with a static IPv4 gateway, DHCPv4, or a PPPoE link must not be stored and must not become a listener. Task 2 pins the refusal. Task 3 pins that the recorder writes nothing.
3. A second write of `first/` must fail, including after a crash that left a partial temp file. The final name is what counts. Task 1 pins replace refusal and temp-file cleanup.
4. Disaster boot must not apply `recovery/` and must not create `admin` / `admin`, even when `recovery/` is the only intact copy. Task 5 pins the order and the account refusal.
5. A rewritten `last-lan/` is used before `first/` when the live tree is damaged. `first/` is the last resort, not the preferred copy. Task 5 pins that order.

## Structure

| Area | Files | Responsibility |
| --- | --- | --- |
| Kept copies | `internal/kept/store_linux.go` | Paths, modes, write-once, read |
| LAN closure | `internal/config/lan_closure.go` | Extract the management closure or refuse an uplink |
| Recording | `internal/engine/config_api.go`, `internal/generation/takeover.go` | `last-lan/` after a live admin save; `first/` after the first confirm |
| Store command | `cmd/andaru/kept.go`, `cmd/andaru/main.go` | Explicit `recovery/` store |
| Disaster boot | `internal/generation/takeover.go` | `/run` render when the live tree is unusable |
| Docs and package | `docs/dev/core-api.md`, `debian/andaru.postrm` | Mount note; do not delete the kept tree |

## Task 1: Kept directory

**Files:**
- Create: `internal/kept/store_linux.go`
- Test: `internal/kept/store_linux_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `const Root = "/var/lib/andaru-kept"`
  - `type Name string` with values `recovery`, `first`, `last-lan`
  - `func Open(stateDir string) (*Store, error)`
  - `func (s *Store) Put(ctx context.Context, name Name, body []byte, replace bool) error`
  - `func (s *Store) Read(name Name) ([]byte, error)`
  - `func LiveTreeUsable(stateDir string) bool`

- [ ] **Step 1: Write the failing tests**

`TestKeptRefusesNesting`: `Open` fails when `stateDir` is `/var/lib/andaru-kept` or a child of it, and when `Root` would be created inside `stateDir`.

`TestKeptModes`: `Put` of `last-lan` creates `Root` and the child at 0700, the file at 0600, both root-owned, not a symlink. A second `Put` with `replace` true changes the bytes. `Put` of `first` with `replace` false fails if the final name exists, and a sibling `*.tmp` does not count as the final name.

`TestMissingStateIsUnusable`: `LiveTreeUsable` is false when `stateDir` does not exist. The call does not create it.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/kept -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the store**

`Open` requires euid 0, a canonical absolute `stateDir`, and `filepath.Rel` proof that neither tree contains the other. Reuse `securefs.CheckAncestors` on `Root`'s parent. `Put` writes `name.json.tmp`, syncs, and renames onto `name.json` only when `replace` is true or the destination is absent. `Read` uses `Lstat`, refuses a non-regular file, a second link, or a mode other than 0600. `LiveTreeUsable` is true only when `stateDir/configuration` exists as a real directory. It never creates a path.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/kept -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/kept
git commit -m "feat: store kept recovery copies outside the wipeable tree"
```

## Task 2: Management LAN closure

**Files:**
- Create: `internal/config/lan_closure.go`
- Test: `internal/config/lan_closure_test.go`
- Modify: `internal/render/nat.go` only to add a comment on `uplink` pointing at this predicate. Do not export the renderer.

**Interfaces:**
- Consumes: the unexported `declaration` and the named-secret values already available to `*Store`
- Produces: `func (s *Store) ManagementLAN(ctx context.Context, version string) ([]byte, error)`

The JSON document is:

```json
{"kind":"lan-closure","schemaVersion":1,"management":{"segment":"...","allowedSources":[]},"links":[],"segments":[],"zones":[],"secrets":{}}
```

`links` is the management segment's link and its parent/member ancestry. `segments` is that one segment. `zones` is its zone. `secrets` holds only values bound by a link in that closure. No users, tokens, audit rows, NAT, DHCP, or DNS service.

- [ ] **Step 1: Write the failing tests**

`TestManagementLANClosure`: a static LAN segment with no gateway, not DHCP, and not on a PPPoE link, returns that segment, its link ancestry, its zone, and its allowed sources. A sentinel secret not bound by that ancestry is absent.

`TestUplinkManagementRefused`: three cases, each returning an error whose text contains `UPLINK_MANAGEMENT` and writing no bytes worth storing: IPv4 `gateway` set, IPv4 mode `dhcp`, and the segment's link kind `pppoe`.

`TestNoManagementRefused`: a declaration with an empty management segment returns `NO_MANAGEMENT`.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/config -count=1 -run 'TestManagementLAN|TestUplinkManagement|TestNoManagement'`

Expected: FAIL because `ManagementLAN` is undefined.

- [ ] **Step 3: Implement the closure**

Read the saved version through the existing store read. The uplink predicate is the same three conditions as `renderer.uplink`: `Gateway != ""`, IPv4 mode `dhcp`, or link kind `pppoe`. Refuse before building the document. Do not add a trusted-LAN flag.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/config -count=1 -run 'TestManagementLAN|TestUplinkManagement|TestNoManagement'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/lan_closure.go internal/config/lan_closure_test.go internal/render/nat.go
git commit -m "feat: extract a non-uplink management LAN closure"
```

## Task 3: Record the copies

**Files:**
- Create: `internal/kept/record.go`
- Test: `internal/kept/record_test.go`
- Modify: `internal/engine/config_api.go` at the successful `store.Save` return
- Modify: `internal/generation/takeover.go` at the successful `Confirm` return

**Interfaces:**
- Consumes: Task 1 `Put`/`Read`; Task 2 `ManagementLAN`
- Produces:
  - `func RecordLastLAN(ctx context.Context, stateDir, savedVersion string, actor authz.Actor) error`
  - `func RecordFirst(ctx context.Context, stateDir, savedVersion string) error`

- [ ] **Step 1: Write the failing tests**

`TestRecordLastLAN`: an `ActorAccount` save whose version equals the live generation's saved version replaces `last-lan/`. The same save does not replace an existing `first/`. An `ActorToken` save and an `ActorSystem` save whose ID is not `root` write nothing. A save whose version is not the live generation's saved version writes nothing.

`TestRecordFirstOnce`: the first call writes `first/`. The second call leaves those bytes unchanged and returns nil. An uplink version writes nothing and does not create `first/`.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/kept -count=1 -run 'TestRecord'`

Expected: FAIL because the recorders are undefined.

- [ ] **Step 3: Implement the recorders and the two hooks**

`RecordLastLAN` runs only for `ActorAccount`, or `ActorSystem` with ID `root`. Both are how an administrator changes a setting today; a token is not. It reads the live generation pointer and returns nil without writing when that saved version differs. `RecordFirst` writes only when `first/` is absent. `Confirm` calls `RecordFirst` for the confirmed generation's saved version and does not call `RecordLastLAN`. `config_api.go` calls `RecordLastLAN` only after `Save` returns nil. A closure error of `UPLINK_MANAGEMENT` or `NO_MANAGEMENT` is not a save failure: log it and leave the kept copy unchanged.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/kept ./internal/engine ./internal/generation -count=1 -run 'TestRecord|TestConfirm'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/kept/record.go internal/kept/record_test.go internal/engine/config_api.go internal/generation/takeover.go
git commit -m "feat: record the last LAN and the write-once first LAN"
```

## Task 4: Store the recovery network

**Files:**
- Create: `cmd/andaru/kept.go`
- Test: `cmd/andaru/kept_test.go`
- Modify: `cmd/andaru/main.go` usage string

**Interfaces:**
- Consumes: Task 1 `Put` with `replace` true and name `recovery`
- Produces: `andaru ctl kept store-recovery`, root-local, no flags for a password or a path

The stored body is the current saved-head declaration plus the secret values bound by that declaration. It is not a LAN closure. It includes a WAN when the head has one. It does not read the identity store.

- [ ] **Step 1: Write the failing test**

`TestStoreRecoveryReplaces`: the command writes `recovery.json`, a second run replaces it, and the body contains the bound secret name's value and no account field. `TestStoreRecoveryRefusesFlags`: an extra argument or a `--password` flag exits 2 and writes nothing.

- [ ] **Step 2: Run the test and see it fail**

Run: `go test ./cmd/andaru -count=1 -run 'TestStoreRecovery'`

Expected: FAIL because the command is unknown.

- [ ] **Step 3: Implement the command**

Add `kept` to the `ctl` verb list. The command takes the engine socket the other ctl verbs use, asks the engine for the saved head and its bound secrets through the existing root socket, and calls `Put`. It does not apply the network and does not call `RecordLastLAN` or `RecordFirst`.

- [ ] **Step 4: Run the test and see it pass**

Run: `go test ./cmd/andaru -count=1 -run 'TestStoreRecovery'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/andaru/kept.go cmd/andaru/kept_test.go cmd/andaru/main.go
git commit -m "feat: store the post-reset network on the root disk"
```

## Task 5: Disaster boot

**Files:**
- Modify: `internal/generation/takeover.go` `Boot`
- Test: `internal/generation/disaster_boot_test.go`

**Interfaces:**
- Consumes: Task 1 `LiveTreeUsable` and `Read`; Task 2 document shape
- Produces: no new CLI verb. `Boot` gains the damaged-tree branch.

- [ ] **Step 1: Write the failing tests**

`TestBootLeavesAUsableTree`: when `configuration/` exists, `Boot` does not read a kept copy.

`TestDisasterBootOrder`: missing `stateDir` selects `last-lan/` even when `first/` and `recovery/` differ. If `last-lan/` is absent, it selects `first/`. If both are absent, it returns an error containing `CONSOLE_REQUIRED` and creates no address. `recovery/` alone is not selected.

`TestDisasterBootDoesNotCreateState`: the missing `stateDir` still does not exist after the call. No file under a stand-in upgrades directory is removed. The rendered output is under the test's `/run` root, not under `stateDir`.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/generation -count=1 -run 'TestBootLeaves|TestDisasterBoot'`

Expected: FAIL because `Boot` still requires the live tree.

- [ ] **Step 3: Implement the branch**

If `LiveTreeUsable` is true, keep the current `Boot`. If it is false, read `last-lan` then `first`. Decode the closure into the smallest declaration the existing renderer accepts, render to the run directory the boot unit already copies from, and return. Do not mkdir `stateDir`. Do not open the identity store. Do not read `recovery`. A closure that fails to render returns `CONSOLE_REQUIRED` and tries the next copy once.

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/generation -count=1 -run 'TestBootLeaves|TestDisasterBoot'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/generation/takeover.go internal/generation/disaster_boot_test.go
git commit -m "feat: boot the last LAN, then the first, when state is missing"
```

## Task 6: Document the mount and protect the copies

**Files:**
- Create: `tests/shell/kept_copies_spec.sh`
- Modify: `docs/dev/core-api.md`
- Modify: `debian/andaru.postrm`
- Modify: `debian/changelog`

- [ ] **Step 1: Write the failing shellspec**

`tests/shell/kept_copies_spec.sh` creates `$root/var/lib/andaru-kept/first.json` and `$root/var/lib/andaru-upgrades/recovery` before running `debian/andaru.postrm` with `DPKG_ROOT=$root` and argument `purge`. Assert both paths still exist and the script exits 0. Repeat for `remove`.

- [ ] **Step 2: Run it and see it fail**

Run: `shellspec tests/shell/kept_copies_spec.sh --shell bash`

Expected: FAIL because the example file is missing. The current `postrm` already leaves unknown trees alone, so the red proof is the missing example, not a deleting script.

- [ ] **Step 3: Update the script, the guide, and the changelog**

`postrm` keeps the comment that uninstall is unsupported, and states that neither purge nor remove deletes `/var/lib/andaru-kept` or `/var/lib/andaru-upgrades`. The guide says an operator may mount a local filesystem at `/var/lib/andaru` before first setup, or stop Andaru and move an existing tree onto that mount. There is no migration command. A missing mount must not be replaced by an empty directory. `debian/changelog` gets one bullet for the kept copies.

- [ ] **Step 4: Run the script test and see it pass**

Run: `shellspec tests/shell/kept_copies_spec.sh --shell bash`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add tests/shell/kept_copies_spec.sh docs/dev/core-api.md debian/andaru.postrm debian/changelog
git commit -m "docs: record the data-partition mount and the kept copies"
```

## Out of scope

- `andaru ctl factory-reset`, the wipe, and the `admin` / `admin` account.
- Applying `recovery/` after a wipe. That waits for the reset command.
- Add-ons, plugin upgrades, and `/var/lib/andaru/addons/`.
- A GUI control for storing `recovery/`.
- Deleting or reading `/var/lib/andaru-upgrades`.

## Self-review

- Spec coverage: layout and modes are Task 1. Closure and uplink exclusion are Task 2. Success recording and write-once `first/` are Task 3. Explicit `recovery/` store is Task 4. Disaster order and the missing-mount refusal are Task 5. Package and mount docs are Task 6. Factory reset, accounts, and add-ons are named as out of scope.
- `first/` is not a factory-reset boot. Disaster boot tries it only after `last-lan/`.
- The plan does not create a network endpoint or a default address.
