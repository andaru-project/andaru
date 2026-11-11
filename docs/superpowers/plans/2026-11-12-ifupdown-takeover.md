# ifupdown Takeover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Andaru takes over a host set up by the default Debian 13 installer (ifupdown, dhcpcd, no netplan): first by `andaru ctl apply network --takeover`, and through the takeover assistant.

**Architecture:**
- **Detection:** one reader, `host.ReadIfupdown`, parses `/etc/network/interfaces` and dhcpcd's state. It feeds three places: the apply gates, the assistant's proposal, and the realizer's baseline capture.
- **DHCP identity:** the renderer receives dhcpcd's identity per permanent MAC. It takes it from the live host until a takeover captures it, and from the captured baseline afterwards.
- **Realizer:** gains one hand-back routine to ifupdown, used by rollback and by a failed activation. It also gains the ifupdown variants of confirm and boot.

**Tech Stack:** Go 1.27, systemd-networkd, systemd-resolved, ifupdown 0.8, dhcpcd 10.

**Spec:** `docs/superpowers/specs/2026-11-11-ifupdown-takeover-design.md` (approved). The netplan takeover, static-DNS and assistant specs cover everything it does not restate.

## Global Constraints

- Nothing outside `/run` changes before confirmation. Andaru never edits `/etc/resolv.conf` or `/etc/network/interfaces`, and installs nothing.
- Commands are fixed argument vectors, run with full paths: `/usr/bin/systemctl`, `/usr/sbin/dhcpcd`, `/usr/bin/networkctl`, `/usr/bin/ip`, `/usr/bin/resolvectl`.
- Never run ifup or dhcpcd from a realize unit; the hand-back goes through `ifup@<nic>.service` and `networking.service` only.
- New finding codes, in both OpenAPI enums and the guide: `RESOLVED_NOT_IN_CHARGE`, `DNS_REQUIRED`, `DHCP_IDENTITY_UNKNOWN`, `TAKEOVER_LIMIT` (a warning).
- Every host reader takes a `root` prefix, so tests run against temporary trees. Fixtures are embedded, so a copied test binary still has them.

## Facts probed on VM 117 for this plan (2026-11-12)

- `networkctl list --json=short` exits 1 while systemd-networkd is stopped.
- ifup's dhcpcd writes `/run/dhcpcd/<nic>.pid`, and `/proc/<pid>/comm` is `dhcpcd`. The binary is `/usr/sbin/dhcpcd`.
- `/var/lib/dhcpcd/duid` is `00:01:00:01:32:87:03:54:bc:24:11:38:71:37`. `/etc/dhcpcd.conf` holds `hostname duid persistent vendorclassid option… require dhcp_server_identifier slaac private`.
- `networking.service` runs `ifup -a` and raises hotplug interfaces only after a restart. `ifup@.service` runs `ifup --allow=hotplug %I`. `/run/network/ifstate` lists the interfaces that are up.

## Rulings (where the plan departs from or sharpens the spec)

1. **The takeover kind is the captured baseline manifest's existing `kind`** (`netplan` or `ifupdown`), not a new `pointers.json` field. That manifest is already written before confirmation and is never rewritten afterwards, so a second record could only disagree with it.
2. **Rule 5's "NetworkManager is not active"** is checked as "NetworkManager is enabled": a `multi-user.target.wants/NetworkManager.service` link. File-based, so testable, and stricter at boot.
3. **Confirm's enable step stays shared with netplan:** it enables networkd *and* disables `systemd-networkd-wait-online`, for the same reason as on netplan.
4. **When the assistant has no DNS servers to pre-fill** on an all-static host (resolved reports none), the proposal omits `dns`. The review then shows `DNS_REQUIRED`, and the operator chooses "these servers".

## Review Focus

1. **A rollback retried by the timer after a partial hand-back** must not bounce an interface that already came back. Test: Task 5 `TestHandBackSkipsInterfacesAlreadyUp`.
2. **The engine's own inventory on an ifupdown host, with networkd stopped,** must answer, not return 503. Test: Task 1 `TestReadManagedWithoutNetworkd`.
3. **A later generation, after confirmation, when `/var/lib/dhcpcd/duid` changed or vanished,** must keep rendering the captured identity. Test: Task 4 `TestIdentityComesFromTheCapturedBaseline`.
4. **Boot with no usable generation on a confirmed ifupdown host** must not deadlock on `networking.service`, which it is ordered before. Test: Task 6 `TestBootFallsBackToIfupdown` asserts `--no-block`.
5. **`source` loops, symlinks and deep nesting in `interfaces`** must refuse, not hang. Test: Task 2 `TestReadIfupdownSources`.

---

### Task 1: Shared detection types and readers that work without networkd

**Files:** `internal/host/netplan_linux.go`, `realizer_linux.go`, `links_linux.go` and their tests. `internal/engine/takeover_api.go`, `applies_api.go`, `socket_linux.go`. `internal/generation/realize.go`. Rename package `internal/host/netplanfixture` to `internal/host/fixture`.

**Produces:**
- `type Detected struct` — the renamed `NetplanInterface`, same fields.
- `type Problem struct{ File, Key, Code, Message string }` — the renamed `NetplanProblem`. An empty `Code` means `TAKEOVER_UNSUPPORTED_BASELINE`.
- `func ParseResolvectl(data []byte) (global []string, links map[string][]string, err error)`. `ParseResolvedDNS` is rebuilt on top of it, with unchanged behaviour.
- `func ReadResolved(ctx context.Context) (servers, domains []string, err error)`. It reads `resolvectl dns` and `resolvectl domain`: the global entries first, then each link's in link order, de-duplicated.
- `hostReader.Resolved func(context.Context) ([]string, []string, error)`, wired to `host.ReadResolved`.

- [ ] **Step 1:** Rename the types and the package. Then `go build ./... && go test ./internal/... ./cmd/...` PASS, with no behaviour change.
- [ ] **Step 2:** Write the failing tests:
  - `TestReadManagedWithoutNetworkd`: with an injected `systemctl is-active` that exits 3, `readManaged` returns `[]Managed{}` and no error.
  - `TestParseResolvectl`: the existing `resolvectl-dns*.txt` fixtures, plus a `Global:` line with servers, plus `resolvectl domain` output.
  - `TestObserveWithoutNetworkd` in `generation`: a fake `Run` that fails `networkctl` and reports networkd inactive gives an observation with no managed links.
- [ ] **Step 3:** Implement:
  - `ReadManaged` first runs `/usr/bin/systemctl is-active --quiet systemd-networkd.service`. An `*exec.ExitError` means `[]Managed{}`.
  - `Realizer.observe` does the same through `r.Run`.
- [ ] **Step 4:** Run the tests again; PASS. Commit: `refactor: share detection types and read the host without networkd`.

### Task 2: Read an ifupdown host

**Files:**
- Create `internal/host/ifupdown_linux.go` and `ifupdown_linux_test.go`.
- Add fixtures `internal/host/fixture/ifupdown-dhcp/` and `ifupdown-static/`, copied from VM 117. They hold `etc/network/interfaces`, `etc/network/interfaces.d/`, `etc/dhcpcd.conf`, `var/lib/dhcpcd/duid`, `run/dhcpcd/ens18.pid`, `proc/<pid>/comm` and `run/network/ifstate`. The static fixture is the installer's static stanza for `10.0.1.13/16 gateway 10.0.0.1`, with no DUID and no pid.

**Produces:**
```go
type DHCPIdentity struct {
	DUID     []byte // dhcpcd's DUID, type bytes first
	Hostname string // the stanza's hostname option, "" if none
}
type IfupdownInterface struct {
	Detected
	Hotplug  bool          // allow-hotplug; false means auto
	DHCP     bool          // inet dhcp
	Identity *DHCPIdentity // set when DHCP
}
// ReadIfupdown applies spec rules 1-3 and rule 5's file checks; links and defaultRoutes are the live inventory.
func ReadIfupdown(root string, links []LinkStatus, defaultRoutes []string) ([]IfupdownInterface, []Problem)
func DhcpcdRunning(root, nic string) bool // run/dhcpcd/<nic>.pid names a live process whose comm is "dhcpcd"
```
- `Detected` mapping: `inet dhcp` is IPv4 `dhcp`; `inet static` is `static` with `address` and `gateway`; `inet6 auto` is IPv6 `slaac`; `inet6 static` is `static`; a missing family is `none`. `DefaultRoute` means the name is in `defaultRoutes`. `MTU` is the link's current MTU.
- Problem codes: rule 3 failures are `DHCP_IDENTITY_UNKNOWN`; every other failure has an empty code. `File` is the offending path, or `/etc/dhcpcd.conf`, or `/var/lib/dhcpcd/duid`.
- The DUID is accepted when it is colon-separated hex of 3 to 130 bytes.

- [ ] **Step 1: Failing tests**
  - `TestReadIfupdownInstallerFiles`: the DHCP fixture gives one `ens18` with IPv4 `dhcp`, IPv6 `slaac`, `Hotplug`, the identity bytes `00 01 00 01 32 87 03 54 bc 24 11 38 71 37` and no problems. The static fixture gives `static [10.0.1.13/16]` via `10.0.0.1`, `none`, and `Identity == nil`.
  - `TestReadIfupdownRefusals`, one case each, asserting `File` and `Code`:
    - `pointopoint`, `dns-nameservers`, `mapping` and `pre-up` options;
    - `iface br0 inet manual`, and a changed MAC;
    - a missing pid file, `clientid` / `iaid` / `anonymous` / an `interface` block in `dhcpcd.conf`, a missing `duid` directive;
    - a DUID of 2 bytes, and a DUID that is not hex;
    - `interfaces.d/50-cloud-init`, `dhcpcd.service` enabled, NetworkManager enabled.
  - `TestReadIfupdownSources`:
    - `source` and `source-directory` relative to `/etc/network`, and a glob matching nothing, are all fine;
    - a symlinked source, a source loop, and 9 levels of nesting each refuse;
    - continuation lines (`\`) are joined.
- [ ] **Step 2:** Run `go test ./internal/host -run Ifupdown`; it fails. Implement. Run again; PASS.
- [ ] **Step 3:** Commit: `feat: read a host the Debian installer set up with ifupdown`.

### Task 3: Render dhcpcd's identity

**Files:** `internal/render/model.go`, `networkd.go`, `render_test.go`.

**Consumes:** `host.DHCPIdentity`. **Produces:** `render.Input.DHCPIdentity map[string]host.DHCPIdentity`, keyed by lower-case permanent MAC.

When the segment's link is Ethernet and its MAC has an identity:

| Segment | Added lines |
| --- | --- |
| IPv4 `dhcp` | `[Network] KeepConfiguration=dynamic-on-stop`; `[DHCPv4] ClientIdentifier=duid`, `IAID=0x<last 4 MAC bytes, 8 lower-case hex digits>`, `DUIDType=<first 2 DUID bytes as decimal>`, `DUIDRawData=<the rest, colon-separated lower-case hex>`, `SendRelease=no`, and `Hostname=<hostname>` when set |
| IPv6 `slaac` or `dhcp6` | `[DHCPv6] IAID=`, `DUIDType=`, `DUIDRawData=`, the same values |

- [ ] **Step 1: Failing test** `TestRenderDHCPIdentity`: lab MAC `bc:24:11:38:71:37` with the probed DUID gives `IAID=0x11387137`, `DUIDType=1` and `DUIDRawData=00:01:32:87:03:54:bc:24:11:38:71:37` in both sections. A static segment gets none of these lines. A MAC without an identity renders exactly as today, byte for byte against the existing golden output.
- [ ] **Step 2:** Run it; it fails. Implement inside `addressing`, which receives the link's MAC. Run again; PASS. Commit: `feat: render dhcpcd's DHCP identity so leases survive a takeover`.

### Task 4: Gates and the identity source in the engine

**Files:**
- `internal/engine/apply_gates.go`, `takeover_api.go` (`gatherGate`, `serveDraft`), `preview_api.go`, `applies_api.go`, and their tests.
- `internal/host/realizer_linux.go`: `Baseline` gains `IfupdownMasked bool`, true when `/etc/systemd/system/networking.service` is a symlink to `/dev/null`.
- `internal/generation/takeover.go`: the baseline record below.
- `internal/engine/openapi.json`.

**Produces:**
```go
// generation: what an ifupdown takeover captured; Task 5 writes it.
type IfupdownBaseline struct {
	Interfaces       []host.IfupdownInterface `json:"interfaces"`
	NetworkdWasActive bool                    `json:"networkdWasActive"`
}
func ReadBaselineKind(dir string) (kind string, ifupdown *IfupdownBaseline, err error) // "" when nothing captured
// generation.Manifest gains Baseline string `json:"baseline,omitempty"`: "netplan" or "ifupdown" on a takeover generation.
// engine: gateInput gains Ifupdown []host.IfupdownInterface, IfupdownProblems []host.Problem, CurrentDNS []string.
func identities(dir string, ifupdown []host.IfupdownInterface) (map[string]host.DHCPIdentity, error) // captured baseline first, else live
```
- **Gate rules** (`applyFindings`), applied when `Baseline.Ifupdown && !IfupdownMasked`:
  - Each `IfupdownProblems` entry is refused with its code.
  - `!Facts.ResolvedInCharge` is `RESOLVED_NOT_IN_CHARGE` at `/etc/resolv.conf`, with the remedy `apt install systemd-resolved`.
  - A `Managed` entry bound to a file that is not Andaru's is `TAKEOVER_UNSUPPORTED_BASELINE`.
  - Every stanza interface without a rendered `.network` is `WOULD_UNCONFIGURE`, up or not.
  - No segment with IPv4 `dhcp` or IPv6 `slaac`/`dhcp6`, and no `dns`, is `DNS_REQUIRED`; the message lists `CurrentDNS`.
  - Without `--takeover`, it is `TAKEOVER_MISMATCH` "ifupdown still configures this host; apply with --takeover". With it, the "nothing to take over" case does not fire.
  - The unconditional `/sbin/ifup` refusal goes.
- **The draft** on such a host adds warnings with code `TAKEOVER_LIMIT`:
  - "SLAAC addresses change at the takeover" when a stanza is `inet6 auto`;
  - "if-up.d and if-down.d hooks stop running after confirmation".
- **Identity:** `gatherGate` and the preview pass `identities(...)` to `render.Input`.
- **Manifest:** the apply records `Manifest.Baseline` on a takeover generation.

- [ ] **Step 1: Failing tests**
  - `TestIfupdownGates`: one case per rule, with code and path, plus the recognised DHCP fixture with a matching declaration giving no findings.
  - `TestIdentityComesFromTheCapturedBaseline`: once a baseline file is captured, a changed live DUID does not change the rendered `DUIDRawData`.
  - `TestDraftShowsTakeoverLimits`.
  - The OpenAPI contract test lists the four new codes.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Update `docs/dev/core-api.md`'s findings table in the same commit.
- [ ] **Step 3:** Commit: `feat: judge an ifupdown takeover`.

### Task 5: Activate and hand back

**Files:**
- `internal/generation/takeover.go`: `captureBaseline` takes the kind.
- Create `internal/generation/ifupdown.go`.
- `realize.go`: the Activate and Rollback branches.
- Create `internal/generation/ifupdown_test.go`.

**Produces:**
- `func (r Realizer) handBack(ctx context.Context, b IfupdownBaseline) error`. The steps follow the spec's Hand-back section:
  1. If `!b.NetworkdWasActive`, run `systemctl stop systemd-networkd.service systemd-networkd.socket`; otherwise `networkctl reload`.
  2. For each interface that lacks a running dhcpcd (`DHCP`) or its first static address (static): restart `ifup@<nic>.service` if `Hotplug`. Restart `networking.service` once if any of them is `auto`.
  3. Poll `ip -j address` for up to `HandBackWait` (30 s; a new `Realizer` field, so tests shorten it) until every interface with a global IPv4 address before has one again.
  Every step runs even after an earlier one failed; the errors are joined.

**Activate** with `g.Baseline == "ifupdown"`:
1. Capture `IfupdownBaseline` from `host.ReadIfupdown(r.Root, links, routes)` and the networkd state into `baseline/manifest.json` (`kind: "ifupdown"`, `files: []`). This happens only while `!p.Takeover`, as for netplan.
2. Record the pre-change state: the stanza interfaces with a global IPv4 address, and whether the default route uses one.
3. Run `/usr/sbin/dhcpcd -p -x <nic>` for each DHCP interface.
4. Start `systemd-networkd.service`.
5. Run `swapAndVerify`. When no file declares DNS, the check also needs resolved to report at least one server, global or per link.

A failure after step 3 runs `handBack` instead of restore-and-settle, and records `failed` only when `handBack` returns nil.

**Rollback:** when `fallback` picks the baseline (`live == nil`) and the kind is `ifupdown`, it places the empty set, then runs `handBack` instead of `settle`. An error leaves the generation pending; the timer retries.

- [ ] **Step 1: Failing tests** against a temporary tree with a scripted `Run`:
  - `TestActivateIfupdownOrder`: the exact command sequence of steps 3 to 5.
  - `TestActivateIfupdownFailureHandsBack`: `systemctl start systemd-networkd` fails, then the hand-back commands run and the state is `failed`.
  - `TestHandBackRunsEveryStep`: a failing stop still restarts `ifup@ens18.service`.
  - `TestHandBackSkipsInterfacesAlreadyUp`: `DhcpcdRunning` is true, so no restart.
  - `TestRollbackStaysPendingUntilAddressesReturn`: no IPv4 address for the whole wait gives an error and `Pending` unchanged. A second call, now with an address, gives `rolled_back`.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Commit: `feat: activate an ifupdown takeover and hand back on failure or rollback`.

### Task 6: Confirm and boot on an ifupdown host

**Files:** `internal/generation/takeover.go` (`makePermanent`, `Boot`), `takeover_test.go`, `packaging/systemd/andaru-network-boot.service`.

- `makePermanent(ctx, reload, kind)` keeps the shared enable step. Then:
  - for `netplan`, it runs the cloud-init and netplan steps as today;
  - for `ifupdown`, it runs `systemctl mask networking.service ifup@.service`, with `--no-reload` when `!reload`.
- When `Boot` finds no usable generation and the kind is `ifupdown`, it runs:
  1. `systemctl unmask networking.service ifup@.service`;
  2. `systemctl start --no-block networking.service`;
  3. `systemctl start --no-block ifup@<nic>.service` for each `Hotplug` interface.
  Then it sets `p.Live = nil`. Boot is ordered before `networking.service`, so a blocking start would deadlock.
- The boot unit gains `networking.service` in `Before=`.

- [ ] **Step 1: Failing tests**
  - `TestConfirmIfupdownMasks`.
  - `TestConfirmIfupdownRepeatsAfterKill`: `failAt("permanent:mask")`, then `Boot` masks.
  - `TestBootFallsBackToIfupdown`: all three commands, each start with `--no-block`, and no netplan file placed.
  - The existing netplan confirm and boot tests still pass unchanged.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Commit: `feat: make an ifupdown takeover permanent and fall back to ifupdown at boot`.

### Task 7: The assistant on an ifupdown host

**Files:** `internal/engine/takeover_api.go` (`serveProposal`, `adapters`), `takeover_api_test.go`, `openapi.json`, `cmd/andaru/takeover.go`, `takeover_forms.go`, `takeover_forms_test.go`.

**Produces:**
- `adapterView.Required bool` (json `required`): the interface has an `iface` stanza and cannot be left out.
- CLI `adapter.Required`. The adapter screen refuses to drop such an adapter: "<name> is configured by ifupdown; Andaru masks ifupdown for every interface it configures".

**Proposal** on `Baseline.Ifupdown && !IfupdownMasked`:
1. Read `host.ReadIfupdown`. Its problems, the resolver rule and the managed rule (shared with Task 4 through one function `ifupdownFindings(...)`) make the state `unsupported`.
2. Otherwise build the operations from the `Detected` list with `proposalOperations`.
3. When no interface uses DHCP or `inet6 auto`, the wan segment gets `dns` with `read.Resolved`'s servers and domains, or none when those are empty (ruling 4).

- [ ] **Step 1: Failing tests**
  - `TestProposalForIfupdown`: the DHCP fixture gives one `wan` segment with `dhcp`/`slaac`, no `dns`, and `required: true`. The static fixture gives static addressing and `dns` from a fake `Resolved`. `RESOLVED_NOT_IN_CHARGE` makes the state `unsupported`.
  - CLI `TestAdapterRequiredCannotBeLeftOut`, a form-validation unit test on `formState`.
- [ ] **Step 2:** Run them; they fail. Implement. Run again; PASS. Commit: `feat: propose an ifupdown takeover in the assistant`.

### Task 8: Guide, gate, lab, review, PR

- [ ] **Step 1:** Update the documents:
  - `docs/dev/core-api.md`: the assistant section is no longer netplan-only; the ifupdown takeover's flow, limits and undo by hand; the boot unit change.
  - The takeover spec's "ifupdown hosts are refused" sentences (lines 39 and 252), each pointing to the new spec.
- [ ] **Step 2:** Run the full gate:
  - `go build ./... && go vet -tags=integration ./... && go test -race ./...`
  - the arm64 build;
  - the root integration suites, run from `/`.
- [ ] **Step 3:** Lab on VM 117, spec items 6 to 11, with a 5-per-second ping. Install the binary and both units each time.
  - Snapshot the static variant stopped, as `base-ifupdown-static-<date>`.
  - Record the results in a new "ifupdown takeover Debian lab qualification" section.
  - Reset VM 117 to `base-ifupdown-dhcp-20261111` (shut down, roll back, start).
- [ ] **Step 4:** One condensed adversarial `reviewer` round. Fix the findings with tests.
- [ ] **Step 5:** PR, pr-watcher, then Codex and CodeRabbit after green CI. Merge when everything passes (owner's standing instruction).
