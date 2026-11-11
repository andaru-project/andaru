# Packet Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A stateful nftables filter with ordered input/forward rules, address and service objects, automatic outbound NAT and an anti-lockout management setting, realized in the same generation as the network.

**Architecture:**
- **Declaration:** gains `objects`, `policy`, `nat` and `management`. The store versions every rule, every ordered list and each singleton.
- **Rendering:** the renderer emits one atomic nft script for table `inet andaru`, stored in the generation as a non-file entry.
- **Realizer:** loads that script before networkd on activate, rollback and boot, and deletes the table last when going back to the baseline.

**Tech Stack:** Go 1.27, nftables 1.1 (`nft -f`, `nft -j`), systemd-networkd 257.

**Spec:** `docs/superpowers/specs/2026-11-12-packet-filter-design.md` (approved).

## Global Constraints

- Andaru touches only table `inet andaru`, never `flush ruleset`, and never another table.
- The engine never runs `nft`; only `realize` does, at `/usr/sbin/nft`.
- Ordered policy arrays keep their order in canonical form; addresses and services sort by ID.
- New ID prefixes: `adr`, `svc`, `rul`.
- New finding codes, in OpenAPI and the guide:
  - render: `FOREIGN_FILTER`, `MANAGEMENT_REQUIRED`;
  - `TOOL_MISSING`, which already exists, gains the nft case.

## Rulings (the plan sharpens the spec here)

1. **The filter switches on with `management.segment`.**
   - **When it is set:** the generation carries the table and forwarding.
   - **When it is not set:** no table and no forwarding, exactly as hosts run today. An apply whose declaration has rules or `nat.outbound` `automatic` but no management segment is refused with `MANAGEMENT_REQUIRED`.
   - **Why:** an upgraded host with an empty policy would otherwise drop everything at its next apply.
2. **The script is a generation entry, not a file.** Its path is the label `nft:inet/andaru`. `files()`/`place()` skip it, and `realize` reads it from the generation directory. Its mode stays `0644` for the existing manifest checks.
3. **Singleton and list versions are pseudo-objects** in the store's version map:
   - `{management, ""}`;
   - `{nat.outbound, ""}`;
   - `{policy.input, ""}` and `{policy.forward, ""}`. A list's version bumps on every create, delete or reorder in that list.
4. **CI installs `nftables`** on both runners, for the `nft -c` golden check and the namespace test.

## Review Focus

1. **A rule referencing an address group whose members later change family:** the save must re-validate the rule, not only the group. Test: Task 1 `TestRuleRevalidatedWhenGroupChanges`.
2. **A reorder racing a create in the same list:** the stale reorder gets `412`, and the create is not lost. Test: Task 2 `TestOrderIsGuardedByListVersion`.
3. **A rollback from a filtered generation to the baseline:** forwarding turns off before the table goes, never the reverse. Test: Task 5 `TestRollbackToBaselineDeletesTableLast`.
4. **`nft` refusing a script at boot:** boot falls back to last-known-good, files and filter together, never files without filter. Test: Task 5 `TestBootFallsBackWhenTheFilterDoesNotLoad`.
5. **A management segment whose interface is renamed after a takeover** (`eth0` becomes `ens18`): the rule must follow, so it matches the segment link's current name at render. Test: Task 4 `TestManagementFollowsTheLinkName`.

---

### Task 1: Declaration, schema and validation

**Files:** `internal/config/model.go`, `validate.go`, `schema.json`, `schema.go`, canonical form in `history.go`, and tests beside them.

**Produces (store types):**
- `address{identity; Kind, Address, Prefix, First, Last string; Members []string}`
- `service{identity; Protocol string; Ports []string; Types []string; Members []string}`
- `rule{ID, Name, Description string; Enabled bool; Action string; From, To, Services []string; Log bool}`
- `declaration` gains:
  - `Objects struct{Addresses []address; Services []service}` (json `objects`);
  - `Policy struct{Input, Forward []rule}` (json `policy`);
  - `NAT struct{Outbound struct{Mode string}}` (json `nat`);
  - `Management *struct{Segment string; AllowedSources []string}` (json `management`).

**Validation, field paths as JSON pointers:**
- **Kinds and ports:** every kind's required value; ports `1–65535` with `first-last`; ICMP type names from a fixed list.
- **Groups:** one level deep, non-empty.
- **References:** `from`/`to` hold zone or address IDs; `services` hold service IDs; an `input` rule may have no `to`.
- **Families:** a rule's addresses must all be of one family if `from` and `to` both name addresses.
- **Management:** `management.segment` exists; `allowedSources` are addresses.
- **NAT:** `nat.outbound.mode` is `automatic` or `none`.

- [ ] **Step 1: Failing tests** in `model_test.go`:
  - `TestPolicyValidation`: one table row per rule above.
  - `TestCanonicalKeepsPolicyOrder`: two rules saved in order B, A come back in order B, A, while addresses come back sorted by ID.
  - `TestRuleRevalidatedWhenGroupChanges`.
- [ ] **Step 2:** Run `go test ./internal/config`; it fails. Implement. Run again; PASS. Add the schema `$defs` and allow the four new top-level keys.
- [ ] **Step 3:** Commit `feat: declare filter objects, policy, outbound NAT and management`.

### Task 2: Store operations

**Files:** `internal/config/changes.go`, `store_linux.go`, `views.go`, and tests.

**Produces:**
- Collections:
  - `addresses` and `services`, ordinary;
  - `policy.input` and `policy.forward`, ordered;
  - singletons `management` and `nat.outbound`, written with op `replace` and an empty ID.
- `Operation` gains `Before string` (json `before`), valid only on a create in a policy collection.
- New op `order`: collection `policy.input|forward`, `ExpectedVersion` the list version, `Value` the ID array.
- The existing deletion refusal extends to every new reference.

- [ ] **Step 1: Failing tests:**
  - `TestCreateBefore`.
  - `TestOrderIsGuardedByListVersion`:
    - a reorder at the old list version after a create gets `412` at `/operations/0/expectedVersion`;
    - an order that is not a permutation is `VALIDATION_FAILED`.
  - `TestSingletonVersions`: absent is version `"0"`.
  - `TestReferencedObjectsCannotBeDeleted`: one case each for an address in a rule, a zone in a rule, a service in a group, and the management segment.
- [ ] **Step 2:** Run `go test ./internal/config` and the root store suite; they fail. Implement. Run again; PASS.
- [ ] **Step 3:** Commit `feat: store ordered rules, objects and singletons with versions`.

### Task 3: API, OpenAPI and CLI

**Files:** `internal/engine/config_api.go`, `openapi.json`, the contract test list, and `cmd/andaru/ctl.go` with its tests.

**Routes:**
- `GET/POST /api/v1/config/{addresses|services}` and `…/{id}`, as for zones.
- `GET/POST /api/v1/config/policy/{input|forward}`: a GET returns `{"items":[…],"version":…}`; a POST accepts `?before=<id>`.
- `GET/PUT/DELETE /api/v1/config/policy/{chain}/{id}`.
- `PUT /api/v1/config/policy/{chain}/order` (`If-Match`).
- `GET/PUT /api/v1/config/nat/outbound` and `/api/v1/config/management`.

**CLI:** `andaru ctl config order input|forward --version V <id>...`, plus `list|get|create|replace|delete` over the new collections. The `policy/input` collection argument passes through.

- [ ] **Step 1: Failing tests:**
  - `TestPolicyRoutes`: status codes and bodies for each route, including `428` without `If-Match` and `412` stale.
  - The CLI `TestConfigOrder`.
  - The contract test lists the new paths.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Commit `feat: serve filter configuration over the API and CLI`.

### Task 4: Render the filter

**Files:**
- Create `internal/render/nft.go` and `nft_test.go`.
- `networkd.go`, for forwarding.
- `internal/host/realizer_linux.go`: `RealizerFacts` gains `NftTool bool` (`/usr/sbin/nft` exists) and `FilterUnits []string` (enabled among `nftables.service`, `firewalld.service`, `ufw.service`).
- Golden files under `internal/render/testdata/`.
- An integration test, `internal/render/nft_integration_linux_test.go` (tag `integration`).
- `.github/workflows/ci.yml`: install `nftables`, and build and run `render.test` in the root suites.

**Produces:** the file `{Path: "nft:inet/andaru", Mode: "0644"}`, rendered only when `management.segment` is set. Its content follows the spec's "Rendering" section exactly. When it is rendered, `IPv4Forwarding=yes` and `IPv6Forwarding=yes` are added to the `[Network]` section of each segment's file.

**Content details:**
- **Table comment:** `sha256` of the script with the comment line removed.
- **Set names:**
  - `zone_<id>` and `adr_<id>_4|_6`, with IDs used verbatim (nft accepts `-`/`_`);
  - `mgmt_4|_6` for `allowedSources`;
  - `uplinks` for the masquerade.
- **Interface names:** zone sets and the management rule use the link's current interface name, as the renderer already resolves it.
- **Findings:**
  - `TOOL_MISSING` when `!NftTool` and the table is rendered;
  - `FOREIGN_FILTER` per unit in `FilterUnits`, path the unit;
  - `MANAGEMENT_REQUIRED` at `/management` when rules or `automatic` exist without a management segment.

- [ ] **Step 1: Failing tests:**
  - `TestNftGolden`, with fixtures `filter-two-zones`, `filter-objects`, `filter-management-sources` and `filter-disabled-logged`.
  - `TestNftScriptIsValid`: runs `nft -c -f` on each golden; skipped without `nft`; CI has it.
  - `TestNoFilterWithoutManagement`: byte-identical to today's output, and no forwarding lines.
  - `TestManagementFollowsTheLinkName`.
  - Findings tests.
- [ ] **Step 2: Integration test** `TestFilterInNamespace` (root, tag `integration`):
  - Use `ip netns` with veth pairs `wan`/`lan`/`mgmt` and load a rendered script.
  - Assert with `ping`, and with `nc` for TCP, that:
    - WAN-to-firewall SSH is dropped;
    - LAN-to-WAN traffic is forwarded with its source rewritten (seen by a WAN-side `tcpdump -c1`, or by `ip netns exec wan nc -l` reading the peer address);
    - management SSH is accepted;
    - a drop above an accept wins;
    - a pre-existing `table inet podman` is unchanged.
- [ ] **Step 3:** Run them; they fail. Implement. Run again; PASS. Commit `feat: render the packet filter and outbound NAT`.

### Task 5: Realize the filter

**Files:** `internal/generation/store.go` (`allowed` accepts `nft:inet/andaru`), `realize.go`, `takeover.go`, `ifupdown.go`, and tests with the scripted fake.

**Produces:**
- `func (r Realizer) loadFilter(ctx context.Context, g Generation) error`: runs `nft -f <generation dir>/<id>/files/<entry>`, then checks that `nft -j list table inet andaru` reports the script's comment.
- `func (r Realizer) dropFilter(ctx context.Context) error`: runs `nft delete table inet andaru`. "No such table" is success.
- `files()`/`place()` skip the entry.

**Order of operations:**
- **Activate:** `loadFilter` comes before `place`; a load failure stops before any file moves.
- **Rollback to a generation:** its `loadFilter` comes first, or `dropFilter` first if it has no entry. Then its files.
- **Rollback to the baseline:** files and `settle` first, then `dropFilter`. On an ifupdown baseline, `dropFilter` comes after the hand-back.
- **Boot:** `loadFilter` comes before `place`. A load error marks the candidate unusable; boot tries the next, then the baseline (`dropFilter`).

- [ ] **Step 1: Failing tests:**
  - `TestActivateLoadsFilterFirst`.
  - `TestActivateFilterFailureMovesNothing`.
  - `TestRollbackToBaselineDeletesTableLast`.
  - `TestRollbackToGenerationLoadsItsFilterFirst`.
  - `TestBootFallsBackWhenTheFilterDoesNotLoad`.
  - `TestVerifyChecksTableComment`.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Commit `feat: load the packet filter with its generation`.

### Task 6: Gates and engine wiring

**Files:** `internal/engine/apply_gates.go`, `takeover_api.go` (`gatherGate`), `openapi.json`, and tests.

**Changes:**
- The renderer's `FOREIGN_FILTER`, `TOOL_MISSING` and `MANAGEMENT_REQUIRED` refuse the apply (they are errors).
- The `filter` sentence in `APPLY_UNSUPPORTED` refers only to what is still unsupported: PPPoE, delegated prefixes, router advertisements.
- The draft response gains `rules`: the rendered rule table, one row per rule with position, action, from, to and services as names, for the assistant's review.

- [ ] **Step 1: Failing tests:**
  - `TestFilterGates`: one case each for an enabled `nftables.service`, a missing `nft`, and missing management.
  - `TestDraftShowsRules`.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Commit `feat: judge filter applies`.

### Task 7: The assistant

**Files:**
- `internal/engine/takeover_api.go` (`proposalOperations`, `proposeIfupdown`, `serveProposal`).
- `cmd/andaru/takeover*.go`: the CLI sends `?via=<local SSH address>` from `SSH_CONNECTION`'s third field.
- The tests beside them.

**Produces:**
- **Proposal operations:**
  - starter rules for each zone without an uplink segment: forward from it to any; input from it for `echo-request` (ICMP and ICMPv6) and DNS;
  - services `dns` (53 tcp+udp, as a group of two) and `ping`, created in the batch;
  - `nat.outbound` `automatic` when an uplink exists;
  - `management.segment` set to the segment whose link has the `via` address, otherwise the first non-uplink segment, otherwise the uplink.
- **Forms:** a "Management segment" select. Choosing a segment in an uplink zone shows the warning text from the spec. The review prints `draft.rules` as a table.

- [ ] **Step 1: Failing tests:**
  - `TestProposalStarterRules`, with the exact operations JSON for the netplan lab base with a second NIC.
  - `TestManagementFollowsTheSSHSession`.
  - The pty test `TestAssistantAppliesExactlyTheDetectedBatch`, updated to walk the new screen.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Commit `feat: propose starter rules and management in the takeover assistant`.

### Task 8: Guide, gate, lab, review, PR

- [ ] **Step 1: Documentation.** `docs/dev/core-api.md` gets:
  - a "Packet filter" section covering objects, rules, ordering, outbound NAT, management, the fixed rules and limits;
  - the new endpoints and CLI command;
  - the new findings in the table.
- [ ] **Step 2: Full gate.** `go build ./... && go vet -tags=integration ./... && go test -race ./...`, the arm64 build, and the root suites run from `/`, including `render.test`.
- [ ] **Step 3: Lab, spec items 5–9.**
  - Add `net1` to VM 114 on `vmbr0` with VLAN tag 4093, and a second NIC on a clone of VM 117 on the same VLAN as the LAN client.
  - Record the results in a new "Packet filter Debian lab qualification" section.
  - Reset both VMs (shut down, roll back, start). Remove the clone and the added NIC.
- [ ] **Step 4: Review.** One condensed adversarial `reviewer` round; fix its findings with tests.
- [ ] **Step 5: PR.** Open it, register it with pr-watcher, request Codex after green CI, and merge when everything passes (owner's standing instruction).
