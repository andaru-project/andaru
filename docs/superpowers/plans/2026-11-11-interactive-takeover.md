# Interactive Takeover Assistant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `andaru ctl takeover`: detect a netplan host's network, show it pre-filled in terminal forms, review the exact result, then save and apply it behind the existing rollback timer; plus `--accept-detected` for unattended use.

**Architecture:** Two read-only engine endpoints (proposal, draft preview) and one new guard on change batches; everything that changes the host still goes through the existing save and apply. The CLI command is a client loop over those endpoints with a pure answers-to-batch function and `huh` forms on top.

**Tech Stack:** Go 1.27.3; `github.com/charmbracelet/huh` v1.0.0 (new, CLI only); Debian 13 lab VMs 114 and 117.

**Spec:** [docs/superpowers/specs/2026-11-11-interactive-takeover-design.md](../specs/2026-11-11-interactive-takeover-design.md)

## Global Constraints

- Nothing before the commit step writes to the store or the host. The draft endpoint publishes nothing.
- The proposal and the commit carry the same change batch; the draft takes that batch.
- Root Unix peer authorization before any read, as on every route.
- Apply is never the preselected action; closing the terminal before Apply writes nothing.
- Host-provided strings are shown with control characters escaped.
- From the save onwards the CLI ignores SIGHUP and SIGINT until the apply returns.
- Lab: start from the stopped snapshots; reset afterwards.

## Review Focus

1. **A save from another session between proposal and commit:** `412`, nothing applied (Task 1, Task 6).
2. **Netplan constructs the declaration cannot express** must be an error finding, never dropped silently (Task 2).
3. **Apply after the save fails or is refused:** the CLI prints the saved version and the retry command; the next run reports `saved-not-applied` (Task 5, Task 6).
4. **Terminal closed or Quit at any screen before Apply:** no write (Task 6).
5. **A DHCPv4-only netplan file on a host with a live SLAAC address:** proposal says `slaac`, not `none`, so the IPv6 address survives (Task 2).

---

## File map

| File | Change |
| --- | --- |
| `internal/config/changes.go`, `store_linux.go` | `ExpectedSavedVersion`; `Store.Draft` (prepare without publishing) |
| `internal/host/netplan_linux.go` (new) | parse netplan's generated files |
| `internal/engine/takeover_api.go` (new) | proposal and draft handlers; state detection |
| `internal/engine/applies_api.go` | extract `gateInputFor` from `create` |
| `internal/engine/api.go`, `preview_api.go`, `socket_linux.go`, `openapi.json` | routes and contract |
| `cmd/andaru/takeover.go` (new), `takeover_answers.go` (new), `ctl.go`, `main.go` | client loop, answers-to-batch, forms |
| `docs/dev/core-api.md` | guide, lab evidence |

---

### Task 1: Whole-batch head guard and dry-run save

**Files:** `internal/config/changes.go`, `internal/config/store_linux.go`, tests beside them, `internal/engine/openapi.json`.

**Produces:**

```go
// Changes gains: ExpectedSavedVersion *Version `json:"expectedSavedVersion,omitempty"`
// Draft prepares the batch against the head exactly as Save does and returns the resulting
// declaration and its secrets without publishing anything.
func (s *Store) Draft(ctx context.Context, input Changes) (RealizationInput, error)
```

- A stale `ExpectedSavedVersion` is `Fault{Code: "PRECONDITION_FAILED"}` at path `/expectedSavedVersion`, checked before any operation.
- `Draft` refuses any operation whose value contains `password` (`VALIDATION_FAILED` at that path).

- [ ] **Step 1:** Tests: `TestExpectedSavedVersion` (stale → 412 and head unchanged; current → saved; absent → unchanged behaviour); `TestDraftPublishesNothing` (head version and store files byte-identical after a valid draft; the returned value contains the created objects; a validation error carries the operation path; a password is refused).
- [ ] **Step 2:** `go test ./internal/config` and the root store suite — FAIL. Implement. PASS. Add the field to the OpenAPI `Changes` schema.
- [ ] **Step 3:** Commit `feat: guard a change batch by the saved head and prepare drafts`.

### Task 2: Read netplan's generated network

**Files:** `internal/host/netplan_linux.go`, test, `internal/host/testdata/netplan/` (captured from VM 114's two bases plus hand-made negative cases).

**Produces:**

```go
type NetplanInterface struct {
	File, Name, PermanentMAC string
	MTU                        int
	IPv4                       struct{ Mode string; Addresses []string; Gateway string } // dhcp | static | none
	IPv6                       struct{ Mode string; Addresses []string; Gateway string } // dhcp6 | slaac | static | none
	DNS                        []string
	Domains                    []string
	DefaultRoute               bool
}
type NetplanProblem struct{ File, Key, Message string }
// ReadNetplan maps every netplan-generated file under root/run/systemd/network to the interface networkd bound it to.
func ReadNetplan(root string, links []LinkStatus, managed []Managed, defaultRoute string) ([]NetplanInterface, []NetplanProblem)
```

Mapping and problems exactly as the spec's table and list. IPv6 `slaac` also when `IPv6AcceptRA=` is absent and the link has a global non-temporary IPv6 address that is not a DHCPv6 /128.

- [ ] **Step 1:** Tests, one per fixture: DHCP base, static base (address, gateway, two DNS, search domain), DHCPv4 with SLAAC; one problem each for a glob match, changed MAC, DHCP plus `Address=` on IPv4, extra `[Route]`, two default routes, DNS on two files, a `.netdev`.
- [ ] **Step 2:** `go test ./internal/host` — FAIL. Implement. PASS.
- [ ] **Step 3:** Commit `feat: read the network netplan generated`.

### Task 3: Proposal endpoint

**Files:** `internal/engine/takeover_api.go`, test; `internal/engine/api.go`, `preview_api.go`, `socket_linux.go`, `openapi.json`.

**Produces:** `GET /api/v1/takeover/proposal` → `{"state", "savedVersion", "operations", "adapters", "findings"}`.

- `state`: `taken-over` if `pointers.json` has `takeover` or a pending apply; `saved-not-applied` if the saved head declares any link; `unsupported` if the baseline is not netplan (ifupdown → finding "taking over an ifupdown host comes in a later release") or `ReadNetplan` reports problems; else `ready`.
- `operations`: create ops with `localId` `link1…`, `seg1…`, `wan`, `lan`; segments reference `@linkN` and `@wan`/`@lan`; `dns` on the segment with the default route when its file had `DNS=`.
- `adapters`: `{name, permanentMac, summary}`; `summary` like `DHCP, 10.0.1.19/16, default route`.
- Problems become error findings `TAKEOVER_UNSUPPORTED_BASELINE` with `path` the file and the key in the message.

- [ ] **Step 1:** Tests over a fake host reader: each state; the DHCP and static fixtures give exactly the expected batch; non-root refused before any read.
- [ ] **Step 2:** FAIL, implement, PASS. Add the route, OpenAPI path and schemas, and the path to the contract test list.
- [ ] **Step 3:** Commit `feat: propose a takeover from the detected network`.

### Task 4: Draft preview endpoint

**Files:** `internal/engine/takeover_api.go`, `applies_api.go`, tests, `openapi.json`.

**Produces:**

```go
// gateInputFor gathers what the apply gates need; create and the draft both use it.
func (h *appliesHandler) gateInputFor(ctx context.Context, value json.RawMessage, secrets map[string]string, takeover bool) (gateInput, error)
```

`POST /api/v1/preview/network/draft` with a change batch → `Store.Draft` → `render.Network` → `applyFindings` with takeover true → `{"state":"draft", …preview fields…, "applyFindings": [...]}`.

- [ ] **Step 1:** Tests: valid batch renders and the store is unchanged; field error carries its path; a gate finding appears (empty batch on the lab fixture → `WOULD_UNCONFIGURE`); password refused; non-root refused. The existing apply tests still pass after the extraction.
- [ ] **Step 2:** FAIL, implement, PASS.
- [ ] **Step 3:** Commit `feat: preview a draft configuration without saving it`.

### Task 5: CLI client loop and unattended mode

**Files:** `cmd/andaru/takeover.go`, `takeover_answers.go`, `ctl.go`, `main.go`, tests.

**Produces:**

```go
type takeoverClient interface {
	Proposal(ctx context.Context) (proposal, error)
	Draft(ctx context.Context, ops []config.Operation) (draft, error)
	Save(ctx context.Context, ops []config.Operation, head config.Version) (config.Version, error)
	Applies(ctx context.Context) (pendingID string, err error)
	Apply(ctx context.Context, saved config.Version, timeout int) (applyReply, error)
}
type answers struct {
	Adapters map[string]adapterAnswer // by localId
	DNS      *struct{ Servers, SearchDomains []string } // nil: use what DHCP offers
	Timeout  int
}
// batch turns the proposal and the operator's answers into the change batch to save.
func batch(p proposal, a answers) ([]config.Operation, error)
// commit runs spec step 4: pending check, second draft, save with the head guard, apply.
func commit(ctx context.Context, c takeoverClient, p proposal, ops []config.Operation, timeout int, out io.Writer) int
```

`andaru ctl takeover --accept-detected [--confirm-timeout S]` = proposal → draft → `commit` with the proposal's own batch. Timeouts: proposal 15 s, draft 10 s, apply 160 s. Signals: `signal.Ignore(SIGHUP, SIGINT)` just before the save.

- [ ] **Step 1:** Tests with a fake client: `batch` for keeping, leaving out (only address-less), mode and DNS changes; `commit` sends nothing when a pending apply exists, sends exactly save then apply, prints the retry advice after a refused or failed apply, returns to the caller on `412`; `--accept-detected` refuses on a non-`ready` state or an error finding without any write; no terminal and no `--accept-detected` → exit 2 with the pointer.
- [ ] **Step 2:** FAIL, implement, PASS.
- [ ] **Step 3:** Commit `feat: add andaru ctl takeover --accept-detected`.

### Task 6: Forms

**Files:** `cmd/andaru/takeover.go`, `go.mod`, `go.sum`, test.

Screens as in the spec, built with `huh`, each a function `func(proposal, *answers) *huh.Form`; the review screen's select lists Back, Quit, then Apply (when allowed) with Back preselected. Escaping helper `printable(string) string` (non-printable runes as `\x..` / `\u....`).

- [ ] **Step 1:** Add `huh` v1.0.0. Test `printable`. Pseudo-terminal test (via `github.com/creack/pty`, already a `huh` dependency) against a fake client: keys that accept the pre-filled values and choose Apply send exactly the expected save and apply; Quit and Back from every screen, and closing the pty before Apply, send no write.
- [ ] **Step 2:** FAIL, implement, PASS. `go build` for linux/arm64 too.
- [ ] **Step 3:** Commit `feat: interactive takeover forms`.

### Task 7: Guide, lab, review, PR

- [ ] **Step 1:** `docs/dev/core-api.md`: "Taking over with the assistant" (flow, states, `--accept-detected`, limits), the new endpoints, `expectedSavedVersion`.
- [ ] **Step 2:** Full gate: `go build ./... && go test -race ./... && go vet -tags=integration ./...` and the root suites.
- [ ] **Step 3:** Lab, spec items 8–11: VM 114 from `base-netplan-static-20261111` on the serial console (`qm terminal`) and over `ssh -t`, ping running, confirm from a new session, reboot; Quit at review; a concurrent save → `412`; `--accept-detected` on a DHCP netplan clone. Record results in the guide; reset the VMs.
- [ ] **Step 4:** One condensed adversarial `reviewer` round; fix findings with tests.
- [ ] **Step 5:** PR, pr-watcher, bots after green CI.

**Execution:** Native is recommended: seven tasks along one path, and the lab run needs this session's access.
