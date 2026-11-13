# DHCPv6 on Delegated Prefixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Kea DHCPv6 and prefix delegation on segments numbered from the WAN's delegated prefix, following a prefix change without a new generation, deprecating a replaced `/64`, and showing the current prefix and what fits in it.

**Architecture:** `dhcp6` entries may name `delegated` segments, with addresses as suffixes and pools and fixed leases as positions in the WAN's prefix. The generation renders Kea's file without those subnets plus a template for them. `andaru realize prefixes` (root, started by a path unit on networkd's link state, a 60 s timer, the apply and boot) reads the prefix from the kernel, fills the template into Kea's file, sends it with `config-set`, installs the pool routes, sends deprecating advertisements, and saves what it saw in `/var/lib/andaru/delegated-prefixes.json`, which the engine reads for a fit warning and the prefix view.

**Tech Stack:** Go, `golang.org/x/sys/unix` (raw ICMPv6), JSON Schema, Kea 2.6 DHCPv6, systemd-networkd 257, iproute2 JSON output, Proxmox lab.

**Spec:** `docs/superpowers/specs/2026-11-13-dhcpv6-delegated-design.md`

## Global Constraints

- **Files:**
  - template `/run/andaru-kea/kea-dhcp6.template.json` (in the generation);
  - Kea's file `/run/andaru-kea/kea-dhcp6.conf` (the generation's version holds only static subnets; root rewrites it filled);
  - state `/var/lib/andaru/delegated-prefixes.json` (current and last prefix per WAN interface) and `/var/lib/andaru/replaced-prefixes.json`.
- **Command and units:** `andaru realize prefixes`; `andaru-prefixes.path` (`PathChanged=/run/systemd/netif/links/`), `andaru-prefixes.timer` (`OnUnitActiveSec=60s`, `OnBootSec=10s`), `andaru-prefixes.service` (`Type=oneshot`, `After=systemd-networkd.service kea-dhcp6-server.service`).
- **Values:**
  - deprecation: preferred 0, valid `min(7200, remaining)`, router lifetime the segment's (default 1800), first send 5 s after the change is seen, repeated each run for 600 s;
  - routes for delegated pools: `unreachable <pool> proto 77`;
  - finding code `DELEGATED_PREFIX_TOO_SHORT` (warning).
- **API and CLI:** `GET /api/v1/status/prefixes`, `andaru ctl prefixes` (beside `andaru ctl links`).
- **No change without delegated `dhcp6` entries:** every existing golden stays byte for byte.
- **Tests:** `go test ./internal/... ./cmd/...`; goldens with `-update`, diff read before committing.

## Review Focus

1. **A shorter prefix than requested** (a `/56` for a `/48`): parts that do not fit are left out of Kea's file and reported; parts that fit are served (Tasks 2, 3).
2. **No prefix at boot or in an outage:** the saved prefix is served until its valid lifetime ends, and the lease sync keeps the next hops (Tasks 3, 4).
3. **Graceful renumbering:** two prefixes valid, the old one not preferred; the preferred one is served and the old one deprecated (Tasks 3, 5).
4. **A `config-set` Kea refuses:** the step fails, Kea keeps its old configuration, the file on disk is not left half-new (Task 3).
5. **The pool's unreachable route:** never removed by the lease sync's protocol 77 sweep while its pool is served, and the sweep parses `unreachable …` lines (Task 4).

---

### Task 1: Placement helper, declaration, validation

**Files:**
- Create `internal/delegation/delegation.go` and `_test.go` (pure, no I/O except `ReadState`).
- Modify `internal/config/model.go` (`staticLease6.PrefixID`, `prefixDelegation.SubnetID`, `.Length`), `schema.json` (the same fields in the dhcp6 shapes; `prefix` no longer required), `services.go` (`validateDHCP6`, a `servedV6` branch for delegated segments), `validate.go` (subnet ID width against `requestPrefixLength`).

**Interfaces:**
- Produces in `internal/delegation`:
  - `func Place(base netip.Prefix, id uint64, length int) (netip.Prefix, bool)`: the prefix of `length` at position `id` under `base`; false when `length < base.Bits()`, `length > 64`, or `id` needs more than `length − base.Bits()` bits.
  - `func Join(p netip.Prefix, suffix netip.Addr) netip.Addr`: the upper 64 bits of `p` with the lower 64 of `suffix`.
  - `type Held struct { Prefix netip.Prefix; Preferred, Valid time.Time }`, `type State struct { WANs map[string]WAN }` (key: WAN interface name), `type WAN struct { Requested int; Current *Held; Last *Held }`.
  - `func ReadState(path string) (State, error)` (a missing file is an empty state).
- Produces in config: `staticLease6{DUID, Address, Prefix, PrefixID, Hostname string}`; `prefixDelegation{Prefix string; SubnetID string; Length int; DelegatedLength int; ExcludedPrefix string}`.

- [ ] **Step 1: Write the failing tests.**
  - `TestPlace`: `Place(2001:db8::/48, 0x1, 64) = 2001:db8:0:1::/64`; `Place(2001:db8::/56, 0x8, 60) = 2001:db8:0:80::/60`; `Place(2001:db8::/56, 0x100, 64)` false; `Place(2001:db8::/56, 0, 56) = 2001:db8::/56` and `Place(2001:db8::/56, 1, 56)` false (no free bits); `Place(2001:db8::/56, 0, 48)` false.
  - `TestJoin`: `Join(2001:db8:0:1::/64, ::1000) = 2001:db8:0:1::1000`.
  - `TestDHCPv6OnDelegated` (config, table-driven over a WAN `dhcp6` with `requestPrefixLength: 48` and a delegated LAN `subnetId: "1"`). Accept ranges `::1000`–`::1fff`, a static lease `{duid, address: "::10", prefixId: "1", hostname}`, and `prefixDelegation {subnetId: "8", length: 60, delegatedLength: 64}`. Refuse, at the path given:
    - a range or address with non-zero upper 64 bits (`/ranges/0/first`, "Expected a suffix: an IPv6 address whose first 64 bits are zero.");
    - a literal static `prefix` (`/staticLeases/0/prefix`, the spec's message);
    - `prefixDelegation.prefix` or `excludedPrefix` on a delegated segment;
    - `length` 48 (not longer than the hint) or 65; `delegatedLength` below `length`;
    - `subnetId` `10000` with `length` 60 on a `/48` hint (needs 12 bits);
    - `prefixId` `10` with 4 free bits (`/60` to `/64`), and a duplicate `prefixId`;
    - a pool covering the LAN's `/64` (`subnetId: "0", length: 60` when the LAN is `subnetId: "1"`);
    - a WAN without `requestPrefixLength` and a pool (needs a hint of 63 or shorter).
  - `TestDelegatedSubnetIDWidth` (config): `subnetId: "100"` refused with `requestPrefixLength: 56` ("Expected 0 to ff for a /56 request."), accepted with 48.
- [ ] **Step 2:** Run `go test ./internal/delegation ./internal/config` and watch them fail.
- [ ] **Step 3:** Implement. Static-segment entries keep their current rules and messages.
- [ ] **Step 4:** Run `go test ./internal/... ./cmd/...`; existing goldens unchanged.
- [ ] **Step 5: Commit:** `feat(config): DHCPv6 on delegated segments`.

### Task 2: Render the template and judge the fit

**Files:** `internal/render/model.go` (mirrors), `services.go` (`dhcp6`: delegated entries go to the template, not `subnet6`), `render.go` (the finding), `internal/host/realizer_linux.go` (`RealizerFacts.Delegated delegation.State`, read with `ReadState("/var/lib/andaru/delegated-prefixes.json")`), `internal/engine/openapi.json` (finding enum), testdata `dhcp6-delegated.json`, `.links.json`, `.golden`, and `services_integration_linux_test.go`.

**Interfaces:**
- Produces the template's JSON, consumed by Task 3:
  `{"subnets":[{"id":int,"interface":string,"uplink":string,"subnetId":string,"ranges":[{"first":string,"last":string}],"reservations":[{"duid":string,"address":string,"prefixId":string,"hostname":string}],"pool":{"subnetId":string,"length":int,"delegatedLength":int}|null,"valid-lifetime":int,"preferred-lifetime":int,"option-data":[...],"user-context":{...}}]}`. Subnet IDs are allocated after the static entries' and stay stable for the same declaration.
- Produces `func FillTemplate(tpl []byte, prefixes map[string]netip.Prefix) (subnets []map[string]any, skipped []string, err error)` in `internal/delegation` (map key: uplink interface; `skipped` names each segment or pool that does not fit). Used by render (documentation prefix and fit), Task 3 and Task 6.

- [ ] **Step 1: Write the failing tests.**
  - The `dhcp6-delegated` golden: the template holds the suffixes, `prefixId`, the pool position and the uplink; `kea-dhcp6.conf` holds no delegated subnet; a static entry beside it is unchanged.
  - `TestFillTemplate`: with `2001:db8:aa00::/48` the LAN gets `subnet: 2001:db8:aa00:1::/64`, pool `"prefix":"2001:db8:aa00:80::","prefix-len":60,"delegated-len":64`, reservation `ip-addresses: ["2001:db8:aa00:1::10"]`, `prefixes: ["2001:db8:aa00:81::/64"]`; with a `/56` and `subnetId: "100"` that subnet is in `skipped`.
  - `TestDelegatedPrefixTooShort`: facts with a current `/56` for a `/48` request: subnet IDs `1` and `2` give no finding; `100` gives a `DELEGATED_PREFIX_TOO_SHORT` warning at `/segments/N/addressing/ipv6/subnetId` naming the current prefix; a pool that does not fit warns at `/dhcp6/N/prefixDelegation`. No saved state: no finding.
  - `TestDHCPv6GoldensPassKea` gains the template filled with `2001:db8::/48` and merged into the file, run through `kea-dhcp6 -t`.
- [ ] **Steps 2–4:** Watch them fail, implement, pass `go test ./internal/render` and the tool test.
- [ ] **Step 5: Commit:** `feat(render): DHCPv6 template for delegated segments`.

### Task 3: `andaru realize prefixes`

**Files:** create `internal/generation/prefixes.go` and `_test.go`; modify `cmd/andaru/realize.go` (`prefixes`, 0 args; usage string), `internal/generation/services.go` (after the generation's files are placed, and before Kea is checked and restarted, fill Kea's file from the template under the lock already held), `internal/generation/boot.go` only if boot touches Kea (boot runs before networkd and does not fill: the units below do).

**Interfaces:**
- Produces `func (r Realizer) SyncPrefixes(ctx context.Context) error` (takes the lock) and `func (r Realizer) syncPrefixes(ctx context.Context, sendRA bool) error` (lock held; the apply passes false).
- Reads the kernel with `r.Run(ctx, "/usr/sbin/ip", "-j", "-6", "route", "show", "type", "unreachable", "proto", "dhcp")` and `r.Run(ctx, "/usr/sbin/ip", "-j", "-6", "addr", "show", "scope", "global")`.
- Sends Kea `{"command":"config-set","arguments":<the whole filled config>}` over `kea6Socket` with the lease sync's socket helper; `result` 0 is success.

- [ ] **Step 1: Write the failing tests** (the bench's fake `Run` answering the two `ip -j` commands; the v6 Kea fake socket recording commands):
  - `TestPrefixesFirstFill`: one unreachable `/48` and the LAN address `2001:db8:aa00:1::1/64` (preferred 3600, valid 7200): Kea's file gains the filled subnet, one `config-set` is sent, the state file holds the current prefix with absolute times.
  - `TestPrefixesUnchangedSendsNothing`: a second run sends no `config-set` and leaves the file untouched.
  - `TestPrefixesChange`: a new prefix: one `config-set` with the same subnet IDs; the state's `Last` is the old one.
  - `TestPrefixesGracefulRenumbering`: two unreachable routes, the old prefix's LAN address with preferred 0: the preferred prefix is served.
  - `TestPrefixesNoneSeenKeepsSaved`: no unreachable route, state with a prefix still valid: the file keeps it; with its valid time passed: the delegated subnets are left out.
  - `TestPrefixesTooShort`: a `/56` and `subnetId: "100"`: that subnet is left out and a warning is logged naming it.
  - `TestPrefixesRefusedConfigSet`: Kea answers `result` 1: the step returns an error and the file on disk is the previous one.
  - `TestActivateFillsDelegatedSubnets`: activation with a saved prefix writes the filled file before `kea-dhcp6 -t` runs.
- [ ] **Steps 2–4:** Watch them fail, implement, pass `go test ./internal/generation ./cmd/...`. Write Kea's file only after `config-set` succeeds (or when Kea is not running), with `writeAtomic`.
- [ ] **Step 5: Commit:** `feat(generation): follow the delegated prefix`.

### Task 4: Pool routes, fixed prefixes and static names

**Files:** `internal/generation/prefixes.go`, `pdroutes.go` (`syncRoutes` counts held pool routes; parses `unreachable <prefix> …` lines from `ip -6 route show proto 77`), `leases.go` / `lease6Records` (reservations with `ip-addresses` in the filled file publish AAAA and PTR), and tests.

**Interfaces:** consumes the filled `kea6Config`; adds `IPAddresses []string` (already present) and `Prefixes []string` to its reservation struct for the next-hop checks.

- [ ] **Step 1: Write the failing tests.**
  - `TestPoolRouteInstalled`: after a fill, `ip -6 route replace unreachable 2001:db8:aa00:80::/60 proto 77` ran.
  - `TestLeaseSyncKeepsPoolRoute`: `ip -6 route show proto 77` listing `unreachable 2001:db8:aa00:80::/60 dev lo …` and a leased `/64` via `fe80::1`: neither is deleted; a pool no longer filled is deleted.
  - `TestFixedPrefixRouted`: a `prefixId` reservation's prefix, leased and with a believed next hop, gets its route like a dynamic one.
  - `TestStaticNameOnDelegated`: a reservation `{ip-addresses: ["2001:db8:aa00:1::10"], hostname: "nas"}` publishes `nas.home.arpa. … AAAA 2001:db8:aa00:1::10` and its PTR; after a prefix change, the new address.
  - `TestNextHopsSurviveExpiredPrefix`: the delegated subnets left out (Task 3's expiry) keep their entries in `pd-next-hops.json`.
- [ ] **Steps 2–4:** Watch them fail, implement, pass `go test ./internal/generation`.
- [ ] **Step 5: Commit:** `feat(generation): pool routes and names for delegated segments`.

### Task 5: Deprecating a replaced prefix

**Files:** create `internal/generation/deprecate.go` and `_test.go`; `prefixes.go` calls it when `sendRA`.

**Interfaces:**
- `func advertisement(old netip.Prefix, valid uint32, routerLifetime uint16, managed, other bool, pref string) []byte`: the ICMPv6 type 134 body (no checksum: the kernel fills it on a raw `IPPROTO_ICMPV6` socket) with one prefix option (type 3, length 4, L and A set, preferred 0, `valid`).
- `Realizer.SendRA func(ifname string, body []byte) error`: the real one opens `unix.Socket(AF_INET6, SOCK_RAW, IPPROTO_ICMPV6)`, sets hop limit 255 (`IPV6_MULTICAST_HOPS`), binds to the interface (`SO_BINDTODEVICE`), and sends to `ff02::1`; tests replace it.

- [ ] **Step 1: Write the failing tests.**
  - `TestAdvertisementBytes`: type 134, code 0, hop limit 64, M and O bits from the flags, router lifetime 1800, prefix option with preferred 0 and valid 5400 for 5400 remaining (7200 cap when more remains).
  - `TestDeprecationWindow`: a change recorded at T: no send before T+5 s; sends at each run until T+600 s; none after; the record is removed after.
  - `TestDeprecationOnlyAnnouncing`: segments in mode `router` or `disabled` send nothing; `unmanaged`, `managed`, `assisted`, `stateless` send.
  - `TestDeprecationAfterGracefulRenumbering`: the not-preferred prefix is recorded and sent.
- [ ] **Steps 2–4:** Watch them fail, implement, pass `go test ./internal/generation`.
- [ ] **Step 5: Commit:** `feat(generation): deprecate a replaced delegated prefix`.

### Task 6: The prefix view

**Files:** `internal/engine/api.go` and a new `prefixes_api.go` (`GET /api/v1/status/prefixes`), `openapi.json` (path and schema), `cmd/andaru/ctl.go` (`andaru ctl prefixes`), tests.

**Interfaces:** the response: `{"wans":[{"segment":string,"interface":string,"requested":int,"current":{"prefix":string,"preferredUntil":string,"validUntil":string}|null,"subnetIds":{"first":"0","last":string},"segments":[{"segment":string,"subnetId":string,"prefix":string|null}],"pools":[{"dhcp6":string,"prefix":string|null}],"notFitting":[string]}]}`. The engine reads `delegation.ReadState` and the confirmed declaration, and uses `FillTemplate`'s placement rules through `Place`.

- [ ] **Step 1: Write the failing tests.**
  - `TestPrefixesStatus` (engine): with a saved `/56` for a `/48` request and subnet IDs `1` and `100`: `subnetIds.last` is `ff`, segment `1` has its `/64`, segment `100` has `prefix: null` and is in `notFitting`; without a saved state, `current` is null and `last` follows the request (`ffff`). The response validates against the served schema.
  - `TestCtlPrefixes` (ctl): `andaru ctl prefixes` requests the path and prints the JSON.
- [ ] **Steps 2–4:** Watch them fail, implement, pass `go test ./internal/engine ./cmd/...` and the engine integration suite.
- [ ] **Step 5: Commit:** `feat(engine): the delegated prefix view`.

### Task 7: Packaging, guide, gate, lab, review, PR

- [ ] **Step 1: Packaging:** `packaging/systemd/andaru-prefixes.{path,timer,service}` with the values above; the path and timer `WantedBy=multi-user.target`. A shell spec is not needed (no script).
- [ ] **Step 2: Guide:** in `docs/dev/core-api.md`, a "DHCPv6 on delegated prefixes" section: suffixes, pool positions, `prefixId`, keeping IDs and pools inside the first `/56` when the provider may answer either way, the prefix view, deprecation, the two-hour valid limit. 
- [ ] **Step 3: Full gate:** vet, `-race`, arm64, the root suites, the namespace tests, `kea-dhcp6 -t` tool test, shellspec.
- [ ] **Step 4: Lab** (spec's four steps): VM 114 behind `pd-router` (LXC 118 delegating a `/48`, then changed), VM 117 on the delegated LAN, a downstream router LXC with a fixed `prefixId`. Record "Delegated DHCPv6 Debian lab qualification" in the guide; reset the guests.
- [ ] **Step 5: Land:** one condensed review, fixes test-first, the PR, CI, Codex and CodeRabbit, the merge; comment on #7 that the slices are complete.
