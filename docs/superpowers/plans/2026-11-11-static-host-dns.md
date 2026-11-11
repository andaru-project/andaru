# Static Host DNS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one segment declare the DNS servers and search domains the host itself uses, render them for systemd-networkd, verify them at apply, and accept the takeover of a netplan host that sets DNS statically.

**Architecture:** An optional `addressing.dns` object flows through the existing layers unchanged in shape: `internal/config` validates and stores it, `internal/render` writes `DNS=`/`Domains=` and turns `UseDNS` off on that segment, `internal/host` reports whether systemd-resolved manages `/etc/resolv.conf`, the apply gate maps netplan's static-DNS files to interfaces through networkd's own binding, and `realize` checks that resolved reports exactly the declared servers.

**Tech Stack:** Go 1.27.3 standard library. Debian 13: systemd 257.13 (`resolvectl`), netplan.io 1.1.2.

**Spec:** [docs/superpowers/specs/2026-11-11-static-host-dns-design.md](../specs/2026-11-11-static-host-dns-design.md)

## Global Constraints

- Branch from `feature/network-takeover` (PR #28); rebase onto `main` once that merges.
- `schemaVersion` stays 1; a saved version without `dns` must read back byte-identical.
- At most one segment declares `dns`. Server and domain order is the declared order everywhere; nothing sorts them.
- Servers are stored in `netip.Addr.String()` form.
- "Resolved is in charge" has one implementation, `host.ResolvedInCharge(root string) bool`, used by the preview and by `realize`.
- Verification compares the exact ordered list. Baseline values are never compared with declared ones.
- No new Go module dependency. Existing golden files change only by gaining nothing: they must be byte-identical.
- Lab: start from snapshot `base-netplan-20261111`; snapshot a stopped VM at each new base state; shut down, roll back and start to reset when done.

## Review Focus

1. **Netplan's file id differs from the interface name** (`10-netplan-wan.network` bound to `eth0`): the gate must go through networkd's binding, never parse the name (Task 4).
2. **Stale DHCP servers still listed after the apply:** a superset must fail verification, not pass as "contains the declared ones" (Task 5).
3. **A search domain carrying a newline or `[Section]` text** must be refused at save, so nothing can inject into the `.network` file (Task 1).
4. **`dns` declared on a link that cannot be realised** (missing NIC): no `DNS=` is rendered, so a static-DNS takeover must still be refused rather than leave the host without a resolver (Tasks 2 and 4).
5. **`/etc/resolv.conf` as a relative symlink** (`../run/systemd/resolve/stub-resolv.conf`, the Debian default) must count as managed by resolved (Task 3).

---

## File map

| File | Change |
| --- | --- |
| `internal/config/schema.json`, `model.go`, `validate.go`, `schema.go` | `dns` shape, model field, validation, field-path allowlist |
| `internal/render/model.go`, `networkd.go`, `render.go`, `testdata/` | decode, render, `DNS_NOT_APPLIED`, two new fixtures |
| `internal/host/realizer_linux.go` | `ResolvedInCharge`, `RealizerFacts.ResolvedInCharge`, `ParseResolvedDNS` |
| `internal/engine/apply_gates.go`, `openapi.json` | conditional static-DNS gate; finding code in both enums |
| `internal/generation/realize.go` | DNS verification |
| `docs/dev/core-api.md`, takeover spec | guide, corrected sentences, lab evidence |

---

### Task 1: Declare and validate `dns`

**Files:** modify `internal/config/schema.json`, `model.go`, `validate.go`, `schema.go`, and the tests beside them.

**Interfaces — produces:** JSON `addressing.dns = {"servers": [string], "searchDomains": [string]}`. Go:

```go
type dns struct {
	Servers       []string `json:"servers"`
	SearchDomains []string `json:"searchDomains,omitempty"`
}
// addressing gains: DNS *dns `json:"dns,omitempty"`
```

Schema: `$defs/dns` with `servers` (array of strings, 1–8, required) and `searchDomains` (array of strings, 0–8), `additionalProperties: false`; referenced as an optional property of `$defs/addressing`. Add `dns servers searchDomains` to the allowlist in `safePointer` (`schema.go:98`).

Validation in `validateTopology`, messages exact:

| Path | Condition | Message |
| --- | --- | --- |
| `/segments/N/addressing/dns` | both families `none` | `A segment without addresses cannot reach DNS servers.` |
| `/segments/N/addressing/dns` | an earlier segment declares `dns` | `Only one segment may declare DNS servers.` |
| `…/dns/servers/J` | not `netip.ParseAddr`, or has a zone, or `Is4In6`, unspecified, loopback, multicast, link-local | `Invalid DNS server address.` |
| `…/dns/servers/J` | equals an earlier server after parsing | `DNS server is listed twice.` |
| `…/dns/searchDomains/J` | does not match the grammar below, or longer than 253 | `Invalid search domain.` |
| `…/dns/searchDomains/J` | equals an earlier one | `Search domain is listed twice.` |

Domain grammar: `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`.

Valid servers are rewritten in place to `Addr.String()` where the existing code canonicalises addresses.

- [ ] **Step 1:** Add `TestDNSValidation`, a table over one otherwise valid static segment, asserting the single field error's path and message for: both families none; a second segment with `dns`; servers `""`, `"dns.example"`, `"192.0.2.53%eth0"`, `"fe80::1%eth0"`, `"::ffff:192.0.2.53"`, `"0.0.0.0"`, `"127.0.0.1"`, `"224.0.0.1"`, `"fe80::1"`; the pair `"2001:db8::53"`, `"2001:0DB8::53"`; domains `"Example.org"`, `"example.org."`, `"~example.org"`, `"exa mple.org"`, `"example.org\n[Match]"`, `"-a.org"`, a 64-character label, a 254-character name; a duplicate domain; zero servers and nine servers (schema errors, path `…/dns/servers`).
- [ ] **Step 2:** Add `TestDNSRoundTrip`: save `servers ["2001:0DB8::53", "192.0.2.53"]`, `searchDomains ["b.example", "a.example"]`; the read-back value has `["2001:db8::53", "192.0.2.53"]` and the domains in that order. A declaration without `dns` marshals without a `dns` key.
- [ ] **Step 3:** `go test ./internal/config` — FAIL. Implement. PASS, including the root store suite.
- [ ] **Step 4:** Commit `feat: declare the host's DNS servers on a segment`.

---

### Task 2: Render `DNS=` and `Domains=`

**Files:** modify `internal/render/model.go`, `networkd.go`; add `testdata/static-dns.{json,links.json,golden}` and `testdata/dhcp-dns.{json,links.json,golden}`.

**Interfaces — consumes:** Task 1's JSON. **Produces:** in the declaring segment's `.network`, after the `Address=` and `Gateway=` lines of `[Network]`: one `DNS=<server>` per server in order, then `Domains=<a> <b>` when there are domains. On that segment every `UseDNS=` line is `UseDNS=no`.

- [ ] **Step 1:** Fixture `static-dns`: one Ethernet, IPv4 static `192.0.2.10/24` gateway `192.0.2.1`, IPv6 none, `dns` with two servers and two domains. Fixture `dhcp-dns`: one Ethernet, IPv4 dhcp, IPv6 slaac, `dns` with one server and no domains; plus a second Ethernet on DHCP without `dns`.
- [ ] **Step 2:** Tests: `TestDeclaredDNSReplacesOffered` — in `dhcp-dns` the declaring file has `DNS=…`, no `Domains=` line, `UseDNS=no` in `[DHCPv4]`, `[DHCPv6]` and `[IPv6AcceptRA]`, and the other uplink's file still has `UseDNS=yes` and no `DNS=`. `TestDNSOrderIsDeclared` — in `static-dns` the `DNS=` lines and the `Domains=` value are in declared order. `TestDNSOnUnrealisableLink` — with the NIC absent, no file contains `DNS=`.
- [ ] **Step 3:** `go test ./internal/render` — FAIL. Implement. Generate the two new goldens with `-update`; `git diff --stat` shows no existing golden changed. PASS.
- [ ] **Step 4:** Commit `feat: render declared DNS servers`.

---

### Task 3: Know whether resolved is in charge

**Files:** modify `internal/host/realizer_linux.go`, `realizer_linux_test.go`, `internal/render/render.go`, `render_test.go`, `internal/engine/openapi.json`.

**Interfaces — produces:**

```go
// ResolvedInCharge reports whether <root>/etc/resolv.conf, with every symbolic link followed inside root,
// is a file under /run/systemd/resolve/ or is /usr/lib/systemd/resolv.conf.
func ResolvedInCharge(root string) bool
// RealizerFacts gains: ResolvedInCharge bool   (ReadRealizerFacts fills it with ResolvedInCharge(""))
```

Renderer: for a segment that declares `dns` when `!Facts.ResolvedInCharge`, a `warning` finding `DNS_NOT_APPLIED` at `/segments/N/addressing/dns`, message `This host's /etc/resolv.conf is not managed by systemd-resolved, so these servers are saved but not used.` Add the code to the `Finding` and `ApplyFinding` enums in `openapi.json`.

- [ ] **Step 1:** `TestResolvedInCharge` over a temp root: absolute link to `/run/systemd/resolve/stub-resolv.conf` → true; relative link `../run/systemd/resolve/stub-resolv.conf` → true; link to `/usr/lib/systemd/resolv.conf` → true; a chain of two links ending there → true; a plain file → false; a dangling link → false; a link loop → false; no file → false.
- [ ] **Step 2:** `TestDNSNotAppliedFinding`: with `static-dns` and `ResolvedInCharge: false`, exactly one warning with that code and path and `realizable` still true; with `true`, none; a fixture without `dns` and `false`, none. Update the shared test facts so existing tests keep `ResolvedInCharge: true`.
- [ ] **Step 3:** `go test ./internal/host ./internal/render ./internal/engine` — FAIL. Implement. PASS.
- [ ] **Step 4:** Commit `feat: warn when declared DNS servers will not be used`.

---

### Task 4: Take over a static-DNS netplan host

**Files:** modify `internal/engine/apply_gates.go`, `apply_gates_test.go`.

**Interfaces — consumes:** `host.Baseline.StaticDNS` (baseline file paths), `gateInput.Managed` (interface → bound file), `gateInput.Result.Files`.

Rule, replacing the unconditional refusal: for each path in `Baseline.StaticDNS`, collect the `Managed` entries whose `NetworkFile` is that path. The file is acceptable when that set is non-empty and, for every entry, `Result.Files` has `/run/systemd/network/50-andaru-<Name>.network` whose content has a line starting `DNS=`. Otherwise one `TAKEOVER_UNSUPPORTED_BASELINE` finding with `path` the baseline file and message `This file sets DNS servers. Declare "dns" on the segment of <interfaces, or "the interface it configures">; without it the host would lose its resolver.`

- [ ] **Step 1:** Rows in `TestApplyFindings`, baseline file `/run/systemd/network/10-netplan-wan.network` bound to `eth0`: rendered `50-andaru-eth0.network` has `DNS=192.0.2.53` → no finding; the only `DNS=` is in `50-andaru-eth1.network` → refused; bound to `eth0` and `eth1` with `DNS=` only in `eth0`'s file → refused; bound to nothing → refused; no `DNS=` anywhere → refused, and the message names `eth0`.
- [ ] **Step 2:** `go test ./internal/engine` — FAIL. Implement. PASS.
- [ ] **Step 3:** Commit `feat: accept the takeover of a netplan host with static DNS`.

---

### Task 5: Verify what resolved reports

**Files:** modify `internal/host/realizer_linux.go` and test, `internal/generation/realize.go`, `realize_test.go`, `cmd/andaru/realize.go`; add `internal/host/testdata/resolvectl-dns.txt`.

**Interfaces — produces:**

```go
// ParseResolvedDNS reads `resolvectl dns`: interface name → servers in order. The Global line is ignored.
func ParseResolvedDNS(data []byte) (map[string][]netip.Addr, error)
```

`realize`: when any placed `.network` file has a `DNS=` line and `host.ResolvedInCharge(r.Root)`, `observe` also runs `/usr/bin/resolvectl dns` (add it to the commands the CLI's runner may execute) and `unmet` requires, for the link bound to each such file, that its servers equal the file's `DNS=` values in order, compared as `netip.Addr`. A failing `resolvectl` is an observation error, retried until `VerifyWait` and then a failed activation. With resolved not in charge, or no `DNS=` in the generation, `resolvectl` is not run.

- [ ] **Step 1:** Capture the fixture on the lab VM: `resolvectl dns` on the netplan base (a `Global:` line, `lo` with no servers, `eth0` with one IPv4 and one IPv6 server). `TestParseResolvedDNS` asserts that map exactly, and that a line that is not `Global:` or `Link N (name):` is an error.
- [ ] **Step 2:** Extend the fake networkd in `realize_test.go`: on applying a reload it records each bound file's `DNS=` values per interface, plus settable `extraDNS` (appended) and `resolvectlFails`; `Root/etc/resolv.conf` is a link into `run/systemd/resolve/` by default. Tests:
  - `TestActivateVerifiesDNS`: exact match → awaiting confirmation, and `resolvectl dns` was run.
  - `TestActivateFailsOnWrongDNS`: rows superset (`extraDNS`), subset, reversed order, `resolvectlFails` → activation fails and the previous files are restored.
  - `TestDNSCheckSkipped`: `resolv.conf` a plain file → succeeds without running `resolvectl`; a generation without `DNS=` → `resolvectl` not run.
- [ ] **Step 3:** `go test -race ./internal/host ./internal/generation` — FAIL. Implement. PASS.
- [ ] **Step 4:** Commit `feat: verify the host's DNS servers at apply`.

---

### Task 6: Documents, lab, review, PR

**Files:** modify `docs/dev/core-api.md`, `docs/superpowers/specs/2026-11-11-network-takeover-apply-design.md`.

- [ ] **Step 1:** Guide: a "Host DNS servers" subsection with the JSON example, the one-segment rule, the resolved dependency and `DNS_NOT_APPLIED`; correct the `TAKEOVER_UNSUPPORTED_BASELINE` row and the three takeover-spec sentences that say static-DNS hosts are refused.
- [ ] **Step 2:** Full local gate: `go build ./... && go test -race ./... && go vet -tags=integration ./...` and the root integration suites.
- [ ] **Step 3:** Lab, spec acceptance items 6–11 in order from `base-netplan-20261111`, recording for each `resolvectl dns`, `resolvectl query debian.org`, the apply JSON and the ping loss count in the guide's qualification section. Item 7's static base is snapshotted with the VM stopped as `base-netplan-static-<date>`. Reset the VM to `base-netplan-20261111` at the end.
- [ ] **Step 4:** One condensed adversarial `reviewer` round on the branch; fix findings with tests.
- [ ] **Step 5:** Push, open the PR (base `feature/network-takeover` while #28 is open, else `main`), register it with pr-watcher, and after green CI request Codex and CodeRabbit reviews.

**Execution:** Native is recommended: five small tasks along one data path, and the lab run needs this session's access.
