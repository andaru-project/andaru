# Inbound NAT Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** IPv4 port forwards and 1:1 NAT, pfSense semantics, saved, validated, served by the API and CLI, and rendered into table `inet andaru`.

**Architecture:** Two new ordered collections, `nat.portForwards` and `nat.oneToOne`, join `policy.input` and `policy.forward` in the store. That means the same version keys, `before`/`order` operations, change batches and history. Their versions live in a new change table, `nat_changes`, created beside the existing tables. The renderer adds a `prerouting` NAT chain and lines in `postrouting`. It derives each forward's associated filter rule into `forward` or `input`.

**Tech Stack:** Go, JSON Schema (santhosh-tekuri/jsonschema v6), SQLite, nftables 1.1 (Debian 13), Proxmox lab.

**Spec:** `docs/superpowers/specs/2026-11-12-inbound-nat-design.md`

## Global Constraints

- **Schema:** `schemaVersion` stays 1. A declaration without the two lists renders byte for byte as today; the existing goldens must not change.
- **Collections:**
  - collection names are `nat.portForwards` and `nat.oneToOne`;
  - ID prefixes are `pfw` and `oto`;
  - API paths are `/api/v1/config/nat/port-forwards` and `/api/v1/config/nat/one-to-one`;
  - CLI names are `nat/port-forwards` and `nat/one-to-one`.
- **IPv4 only.** `to` and `target` take `{"self": true}` or `{"address": …}`: for `to` an address object ID, for `target` a literal IPv4 address. `127.0.0.0/8` is refused as a target.
- **Rendered lines** carry the object's ID as `comment "<id>"`.
- **Nothing without a filter:** inbound NAT needs a management segment (`MANAGEMENT_REQUIRED`), like rules and outbound NAT.
- **Tests:** `go test ./internal/...`. Goldens are refreshed with `go test ./internal/render -run TestNetworkGolden -update`, and the diff is read before each commit.
- **Graph:** the pre-commit hook rebuilds the graph like a fresh checkout. After a rebase, refresh the graph and amend; never add a separate graph commit.

## Review Focus

1. **Old stores:** a store created before this slice, which has no `nat_changes` table, opens, saves and lists history. Task 2 pins it with a store made from today's DDL.
2. **`to: self`:** it must fire only on the address of the interface the connection arrived on. A LAN client connecting to the firewall's LAN address on the forwarded port is not redirected unless reflection is on (Task 4).
3. **1:1 and outbound masquerade:** an excluded (`noBinat`) host and every host without a mapping still leave through the outbound masquerade. The 1:1 `snat` lines precede it (Task 5).
4. **A 1:1 range holding a learned address:** a DHCP-learned WAN address inside an `external` range is refused at render time, so neither an apply nor a boot takes over the firewall's own traffic (Task 5).
5. **Ordering:** reordering one list must not move the version of the other, nor of either policy list (Task 2).

---

### Task 1: Declaration, schema and validation

**Files:**
- Modify: `internal/config/model.go` (`natSettings`), `internal/config/schema.json` (`$defs`: `portForward`, `portForwardWrite`, `oneToOne`, `oneToOneWrite`; `nat` gains both lists), `internal/config/schema.go:104` (`safePointer` fields).
- Create: `internal/config/nat.go` (`validateNAT(d *declaration) []FieldError`, called from `decodeDeclaration` beside `validatePolicy`).
- Test: `internal/config/nat_test.go`.

**Interfaces:**
- Produces `type portForward struct{ identity; Enabled bool; IngressZones []string; Protocol string; From []string; FromNegated bool; To natAddress; Ports string; NoRedirect bool; Target *natTarget; TargetPort *int; FilterRule string; Reflection, Log bool }`. JSON names are as in the spec; `filterRule` defaults to `auto` in `normalizeDeclaration`.
- Produces `type natAddress struct{ Self bool \`json:"self,omitempty"\`; Address string \`json:"address,omitempty"\` }`, used by `to` with an address ID. Produces `type natTarget struct{ Self bool; Address string }`, used by `target` with a literal IPv4 address.
- Produces `type oneToOne struct{ identity; Enabled bool; Zone string; External, Internal string; Destination []string; NoBinat, Reflection bool }`.
- Produces `natSettings{ Outbound outbound; PortForwards []portForward \`json:"portForwards,omitempty"\`; OneToOne []oneToOne \`json:"oneToOne,omitempty"\` }`.

- [ ] **Step 0: Probe loopback targets.** In a privileged `debian:trixie` container (`apt-get install iproute2 nftables ncat procps`), with a veth pair into a netns client, set up:
  - a `table ip` prerouting `dnat` from the client to `127.0.0.1:8080`, and another to a `/32` on `lo`;
  - listeners bound to each address;
  - `ip link set lo up`, `net.ipv4.conf.all.rp_filter=0`.

  Record whether the `127.0.0.1` case connects with `route_localnet` off and on, and whether the `lo` `/32` case connects. Write the result in the ledger. If `127.0.0.1` works without `route_localnet`, keep the refusal anyway (the spec's call) and correct the spec's sentence in the Task 6 docs commit.
- [ ] **Step 1: Write the failing tests.** `TestPortForwardValidation`, table-driven over `servicesFixture` plus a zone and an address object. It accepts:
  - a `self` forward of `tcp` port `8080` to `192.168.40.10` with `targetPort` 80;
  - an `address`-to forward;
  - an exclusion without a target;
  - a forward to `{"self": true}`.

  It refuses each of these at the listed path:

  | Case | Path |
  | --- | --- |
  | no `ingressZones` | `/nat/portForwards/0/ingressZones` |
  | unknown zone | `/nat/portForwards/0/ingressZones/0` |
  | `ports` `0`, `70000` or `9-8` | `/nat/portForwards/0/ports` |
  | `targetPort` 65530 with a 10-port range | `/nat/portForwards/0/targetPort` |
  | target `127.0.0.1` | `/nat/portForwards/0/target/address` |
  | target `2001:db8::1` | `/nat/portForwards/0/target/address` |
  | a non-exclusion without a target | `/nat/portForwards/0/target` |
  | an exclusion with a target | `/nat/portForwards/0/target` |
  | two enabled forwards with the same `from`, `to`, a shared zone and protocol, and overlapping ports | `/nat/portForwards/1/ports` |

  Two forwards that differ only in `from` are accepted.

  `TestOneToOneValidation` accepts `203.0.113.20` to `192.168.40.30`, and `203.0.113.16/28` to `192.168.40.16/28` with a `destination`. It refuses:

  | Case | Path |
  | --- | --- |
  | `/28` external against a `/29` internal | `/nat/oneToOne/0/internal` |
  | an IPv6 `external` | `/nat/oneToOne/0/external` |
  | `external` holding a segment's static address | `/nat/oneToOne/0/external` |
  | two overlapping externals | `/nat/oneToOne/1/external` |
  | `internal` holding the firewall's static address on that segment | `/nat/oneToOne/0/internal` |
  | `noBinat` with an `external` | `/nat/oneToOne/0/external` |
  | an unknown `zone` | `/nat/oneToOne/0/zone` |

  `TestNATReferencesBlockDeletion`: deleting a zone or an address object that a forward names is refused.
- [ ] **Step 2: Run the tests and watch them fail.**
  - Run: `go test ./internal/config -run 'TestPortForwardValidation|TestOneToOneValidation|TestNATReferencesBlockDeletion'`
  - Expected: FAIL (unknown properties).
- [ ] **Step 3: Implement** the schema, the model, `validateNAT` and the reference checks, reusing `validatePolicy`'s address and zone lookups. The overlap check compares `from` as a sorted ID list plus `fromNegated`, and `to` as equal values.
- [ ] **Step 4: Run** `go test ./internal/config` and confirm it passes.
- [ ] **Step 5: Commit:** `feat(config): port forwards and 1:1 NAT in the declaration`.

### Task 2: Store operations

**Files:**
- Modify: `internal/config/changes.go`:
  - `orderedCollections`, `newID` prefixes, `objects`, the list rewrite at about line 491, `resolveRefs` (`ingressZones`, `from`, `to.address`, `zone`, `destination`), and `owner` (`/nat/portForwards/N`, `/nat/oneToOne/N`).
  - Replace the two-way `policy.forward` switches with one helper, `orderedList(d *declaration, col string) []identity`, and a typed rewrite per collection.
- Modify: `internal/config/views.go:133` (list order view), `internal/config/sqlite.go` (`natDDL`, `changeTable`), `internal/config/store_linux.go:123` (execute `natDDL` on open), `internal/config/history.go:325` (UNION `nat_changes`).
- Test: `internal/config/nat_store_test.go`.

**Interfaces:**
- Produces collection names `nat.portForwards` and `nat.oneToOne`, accepted by `DecodeChanges`, `ObjectChange` and the history list. Produces the change table `nat_changes`, whose CHECK allows exactly these two collections.

- [ ] **Step 1: Write the failing tests.**
  - `TestNATListsBehaveLikePolicyLists`, using `seededState`, `mustRun` and `run`:
    - create two forwards, the second with `before` the first, and check the order;
    - `order` them back;
    - replace one; delete one;
    - assert the list version moves on create, delete and order, but not on replace, and that `policy.forward`'s version never moves.
    - Do the same for `nat.oneToOne`.
  - `TestStoreWithoutNATTableOpens`: create a store and save a version. Drop `nat_changes` with SQL to stand in for an older store, reopen it, save a forward, and list history. `Open` must recreate the table, and the version's changed objects must include the forward.
- [ ] **Step 2: Run the tests and watch them fail.**
  - Run: `go test ./internal/config -run 'TestNATListsBehaveLikePolicyLists|TestStoreWithoutNATTableOpens'`
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/config` and confirm it passes.
- [ ] **Step 5: Commit:** `feat(config): store the NAT lists`.

### Task 3: API, OpenAPI and CLI

**Files:**
- Modify:
  - `internal/engine/config_api.go:38`: route `config/nat/port-forwards` and `config/nat/one-to-one` through the ordered-list routes, mapping the path segments to the collection names.
  - `internal/engine/openapi.json`: paths, schemas, the `pfw`/`oto` ID patterns.
  - `cmd/andaru/ctl.go`: `collectionName`, `objectIDPattern` (`pfw|oto`), `validChanged`, and the `order` subcommand for both lists.
- Test: `internal/engine/config_api_test.go`, `cmd/andaru/ctl_test.go`.

- [ ] **Step 1: Write the failing tests.**
  - `TestNATListRoutes`, against the API bench:
    - `POST` a forward and get `201`, then `GET` the list and see it, with the version;
    - `PUT …/order` with a stale `If-Match` returns `412`, and with a non-permutation returns `422`;
    - `DELETE` returns `204`.
  - `TestCtlNATLists`: `andaru ctl config list nat/port-forwards`, `create`, and `order` form the right requests, and a `pfw_` ID validates.
- [ ] **Step 2: Run the tests and watch them fail.**
  - Run: `go test ./internal/engine ./cmd/andaru -run 'TestNATListRoutes|TestCtlNATLists'`
- [ ] **Step 3: Implement.** The OpenAPI schemas mirror `schema.json`. The existing contract test (`TestOpenAPI…`) must still pass.
- [ ] **Step 4: Run** `go test ./internal/engine ./cmd/andaru` and confirm it passes.
- [ ] **Step 5: Commit:** `feat(engine): NAT list API and CLI`.

### Task 4: Render port forwards

**Files:**
- Modify: `internal/render/model.go` (mirror types), `internal/render/nft.go`:
  - `filterChains` adds the lists to the `MANAGEMENT_REQUIRED` condition;
  - a `prerouting` chain;
  - the associated rules after the fixed rules in `forward` or `input`.
- Create: `internal/render/testdata/nat-port-forwards.json`, `.links.json`, `.golden`.
- Test: `internal/render/nat_test.go`, `internal/render/nft_integration_linux_test.go` (the existing namespace test loads every golden table).

**Interfaces:**
- Produces `(f *filter) portForwards() (prerouting, forward, input []string)`, with lines already formatted.

- [ ] **Step 1: Write the failing tests.**
  - **Fixture:** WAN `ens18` (DHCP, zone `wan`) and LAN `ens19` (`192.168.40.1/24`, zone `lan`), with a management segment, plus five forwards:
    1. `self:8080/tcp` to `192.168.40.10:80`, with `log`;
    2. `self:1000-1001/tcp` to `192.168.40.10:2000`;
    3. an exclusion from address object `198.51.100.7` on `self:1000-1001/tcp`;
    4. `self:2222/tcp` to target `self:22`;
    5. a disabled forward.

    Its golden must contain these lines, in this order:
    - `chain prerouting {` with `type nat hook prerouting priority dstnat;`
    - the exclusion before the range forward: `iifname @<wan zone set> ip saddr @<adr set> fib daddr . iif type local tcp dport 1000-1001 accept comment "<pfw id>"`
    - `iifname @<wan zone set> fib daddr . iif type local tcp dport 8080 log prefix "andaru <pfw id> " dnat ip to 192.168.40.10:80 comment "<pfw id>"`
    - `… dnat ip addr . port to tcp dport map { 1000 : 192.168.40.10 . 2000, 1001 : 192.168.40.10 . 2001 } …`
    - `… tcp dport 2222 redirect to :22 …`
    - in `forward`, after `ct state invalid drop`: `iifname @<wan zone set> ct status dnat ct original proto-dst 8080 ip daddr 192.168.40.10 tcp dport 80 accept comment "<pfw id>"`
    - in `input`, after the fixed rules: the associated rule for the `self` target with `ct original proto-dst 2222` and `tcp dport 22`.

    Nothing may name the disabled forward.
  - `TestForwardWithoutFilterRule`: `filterRule: none` renders the `dnat` and no associated rule.
  - `TestNATNeedsManagement`: the fixture without `management` is unrealizable, with `MANAGEMENT_REQUIRED`.
  - `TestTCPUDPForward`: `tcp-udp` renders `meta l4proto { tcp, udp } th dport 53`.
  - `TestAddressTo`: an address-object `to` renders `ip daddr @<set>` instead of `fib`, and its associated rule adds `ct original ip daddr @<set>`.
- [ ] **Step 2: Run the tests and watch them fail.**
  - Run: `go test ./internal/render -run 'TestNetworkGolden/nat-port-forwards|TestForwardWithoutFilterRule|TestNATNeedsManagement|TestTCPUDPForward|TestAddressTo'`
- [ ] **Step 3: Implement.** Confirm every new golden table with `nft -c -f` in the integration test: `go test -tags integration -run TestFilterInNamespace ./internal/render`, as root.
- [ ] **Step 4: Run** `go test ./internal/render` and the integration test, and confirm both pass. The existing goldens must be unchanged.
- [ ] **Step 5: Commit:** `feat(render): port forwards`.

### Task 5: Render 1:1 NAT and reflection

**Files:**
- Modify: `internal/render/nft.go` (1:1 `dnat` after the forwards in `prerouting`; `snat` before the masquerade in `postrouting`; reflection), `internal/engine/openapi.json` (finding codes `PROXY_ARP_REQUIRED`, `ONE_TO_ONE_OWN_ADDRESS`), `docs/dev/core-api.md` (finding rows).
- Create: `internal/render/testdata/nat-one-to-one.json`, `.links.json`, `.golden`.
- Test: `internal/render/nat_test.go`.

- [ ] **Step 1: Write the failing tests.**
  - **Fixture:** WAN `ens18`, static `10.0.1.19/16` and `10.0.1.29/16`. LAN `192.168.40.1/24`. Entries:
    1. a `noBinat` entry for `192.168.40.31`;
    2. 1:1 `10.0.1.29` to `192.168.40.30`;
    3. 1:1 `203.0.113.16/28` to `192.168.40.16/28` with `destination` an address object;
    4. a forward `self:8080` to `192.168.40.10:80` with `reflection`.

    The golden must contain:
    - in `prerouting`, after every forward line:
      - `iifname @<wan zone set> dnat ip to ip daddr map { 10.0.1.29 : 192.168.40.30 } comment "<oto id>"`;
      - the prefix form, `dnat ip prefix to ip daddr map { 203.0.113.16/28 : 192.168.40.16/28 }`, restricted with `ip saddr @<set>`.
    - the exclusion as negated matches on the later entries: `ip saddr != 192.168.40.31` on their `snat`, and `ip daddr !=` its mapped external on their `dnat`.
    - in `postrouting`, before `fib saddr type != local oifname @uplinks meta nfproto ipv4 masquerade`, the `snat ip to ip saddr map { 192.168.40.30 : 10.0.1.29 }` line and the prefix line.
    - for reflection: `iifname @<lan zone set> fib daddr type local ip daddr != @<other segments' static addresses set> tcp dport 8080 dnat ip to 192.168.40.10:80`, plus `oifname "ens19" ct status dnat ip saddr 192.168.40.0/24 masquerade`.
  - `TestProxyARPWarning`: `external` `10.0.1.50`, inside the WAN's `/16` and not assigned to it, gives warning `PROXY_ARP_REQUIRED` at `/nat/oneToOne/N/external`. `10.0.1.29` gives none.
  - `TestOneToOneLearnedAddress`: on a DHCP WAN whose live link address is `203.0.113.17`, an `external` of `203.0.113.16/28` gives error `ONE_TO_ONE_OWN_ADDRESS`, and the result is unrealizable.
- [ ] **Step 2: Run the tests and watch them fail.**
- [ ] **Step 3: Implement**, then run the integration test.
- [ ] **Step 4: Run** `go test ./internal/...` and confirm it passes.
- [ ] **Step 5: Commit:** `feat(render): 1:1 NAT and reflection`.

### Task 6: Guide, gate, lab, review, PR

- [ ] **Step 1: Write the guide.** Add an "Inbound NAT" section to `docs/dev/core-api.md`: the declaration, CLI examples, `self`, the associated rule, reflection, the 1:1 address sources, Proxy ARP waiting on #37, and the endpoint rows.
- [ ] **Step 2: Run the full gate:**
  - `go vet -tags=integration ./...`
  - `go test -race ./...`
  - the arm64 build
  - the root suites
  - the render integration test
- [ ] **Step 3: Run the lab.** Reset VM 114 (`base-netplan-static-20261111`) and VM 117 (`base-ifupdown-static-20261112`, `net0` on tag 4093). Add `10.0.1.29/16` to VM 114's WAN and check that it's free first. The WAN client is LXC 118's `eth0`. Run the spec's seven steps through `andaru ctl config`, then confirm. Record "Inbound NAT Debian lab qualification" in the guide, and reset all three guests.
- [ ] **Step 4: Review and land.** One condensed `reviewer` round, with fixes test-first. Then the PR, CI, Codex and CodeRabbit, and the merge.
