# Network Rendering and Apply Preview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render a saved network declaration into systemd-networkd and pppd files and serve them, redacted, with host findings through `GET /api/v1/preview/network` and `andaru ctl preview network`, without touching host networking.

**Architecture:** A pure `internal/render` package turns the saved snapshot JSON, its paired secrets and observed host facts into files and findings. `internal/host` supplies the observations, `internal/config` supplies one saved version with its secret values, and `internal/engine` joins them behind the existing root-only handler. The renderer decodes the public snapshot JSON with its own small structs: the served JSON Schema is the contract, so `internal/config`'s private model stays private.

**Tech Stack:** Go 1.27.3 standard library only. Debian 13 tools for proof: systemd 257.13, ppp 2.5.2, iproute2 6.15.

**Spec:** [docs/superpowers/specs/2026-11-10-network-render-preview-design.md](../specs/2026-11-10-network-render-preview-design.md)

## Global Constraints

- No file is written under `/run`, nothing is reloaded or started, no state is stored. The only host reads are `ip -d -j address show`, directory listings of `/etc/systemd/network` and `/run/systemd/network`, and `stat` of pppd paths and `/dev/ppp`.
- Root Unix peer authorization happens before any store or host read, as on existing routes.
- Real secret values never reach an API response, log line, finding or error. Redaction is by construction (two renderings), never by string replacement.
- `name` and `description` never reach rendered output.
- Same inputs give byte-identical file content. Every file ends with exactly one newline.
- File prefix `50-andaru-`; derived names `vl`/`br`/`bo`/`pp` + first 12 hex digits of the link ID's UUID.
- No new Go module dependency. No declaration schema change. The `status/links` response is unchanged.
- Integration tests use the `integration` build tag, require root, embed their fixtures with `//go:embed`, and fail rather than skip when a prerequisite is missing.

## Probe results this plan relies on (lab VM 114, 2026-11-10)

- **Debian's cloud kernel has no PPP support.** `linux-image-cloud-amd64` ships no `pppoe`/`ppp_generic` modules and no `/dev/ppp`; pppd then fails before parsing options. The VM now boots `linux-image-amd64` 6.12.116 (snapshot `pre-ppp-20261110-185944` taken first). On that kernel `/dev/ppp` exists before any module is loaded. The spec carries a `KERNEL_SUPPORT_MISSING` finding for this.
- **pppd dry run:** `unshare -n sh -c 'ip link add <parent> mtu 1508 type dummy && pppd file <options> logfd 1 dryrun'` exits 0 and prints `pppd options in effect:` with `ifname`, `mtu`, `mru`, and `user a \"q\" \\ # b\nc` for a username containing quotes, a backslash, `#`, spaces and a newline. The password prints as `??????`. Without the dummy parent it exits 2 (`unrecognized option 'nic-…'`); an unknown option exits 2.
- **Private networkd:** under `unshare -n -m`, tmpfs over `/run/systemd/network` and `/run/systemd/netif`, then `SYSTEMD_LOG_LEVEL=debug SYSTEMD_LOG_TARGET=console /usr/lib/systemd/systemd-networkd` for five seconds. It logs `<file>: Successfully loaded.` per file and `<file>:<line>: Unknown key '…' in section […], ignoring.` / `Unknown section '…'. Ignoring.` for defects. The host's networkd and links were unchanged.
- **Unit verification:** `systemd-analyze verify` on the spec's unit exits 0 with empty output, including with a `BindsTo=` on a device that does not exist; an unknown key prints `Unknown key … ignoring.` and still exits 0, so the assertion is on output, not status. `systemd-escape --path /sys/subsystem/net/devices/enx-1.2` gives `sys-subsystem-net-devices-enx\x2d1.2`.

## Where the tool proofs run

The three tool proofs (pppd, `systemd-analyze`, private networkd) run **on the lab VM only**. The CI runners are Ubuntu 24.04 with ppp 2.4.9 and systemd 255, not the Debian 13 versions the rendered files target, so a green or red result there would say nothing about the product. CI still runs every unit and golden test on amd64 and arm64, and the root suites for the API and CLI. The guide states this. `shortcut: tool proofs are not in CI; add a Debian 13 runner or container job when the apply slice makes them release-blocking.`

## Review Focus

1. **A NIC with a VLAN and a bridge on it**, all reporting the same MAC: must resolve to the NIC, not `HARDWARE_AMBIGUOUS` (Task 3).
2. **Password equal to, or contained in, the username** (the owner's `kpn@internet` / `kpn`): the username stays visible and intact, the password is `<redacted>` (Task 4).
3. **An empty saved declaration:** `files`, `findings` and `resolved` are `[]`, never `null`, and `realizable` is true (Tasks 3 and 6).
4. **`?savedVersion=` older than the head**, with a since-changed secret: the preview uses that version's own paired secret, proven through `CREDENTIAL_TOO_LONG` appearing for the old version only (Task 6).
5. **A physical NIC name with unusual characters** as a PPPoE parent: `enx-1.2` is escaped in the `.device` unit name exactly as `systemd-escape` does; a name containing `%`, `$`, `#`, `"`, `\` or a space is refused with a finding rather than written into a unit or options file (Tasks 4 and 5).

---

## File map

| File | Responsibility |
| --- | --- |
| `internal/host/links_linux.go` (modify) | add observed `Kind` and `PermanentAddress` |
| `internal/host/realizer_linux.go` (create) | `RealizerFacts`, `ReadRealizerFacts`: foreign networkd files, pppd, plugin, `/dev/ppp` |
| `internal/config/history.go`, `views.go` (modify) | `Store.RealizationInput` |
| `internal/render/model.go` | decode structs for the snapshot JSON; `Input`, `Result`, `File`, `Finding` |
| `internal/render/resolve.go` | NIC resolution, derived names, availability and omission rules |
| `internal/render/networkd.go` | `.netdev`/`.network` text for Ethernet, VLAN, bridge, bond and addressing |
| `internal/render/pppoe.go` | options file, service unit, PPP `.network`, pppd quoting, unit-name escaping |
| `internal/render/render.go` | `Network`: orchestration, sorting, findings |
| `internal/render/testdata/` | declarations, observed links and golden outputs |
| `internal/render/tools_integration_linux_test.go` | pppd, systemd-analyze and private-networkd proofs |
| `internal/engine/preview_api.go` (create); `api.go`, `socket_linux.go`, `openapi.json`, `api_test.go`, socket and config integration tests (modify) | route and contract |
| `cmd/andaru/ctl.go`, `main.go` (modify) | `preview network` |
| `docs/dev/core-api.md`, `README.md` (modify) | operator guide |

---

### Task 1: Observe link kind and permanent address

**Files:** modify `internal/host/links_linux.go`, `internal/host/links_linux_test.go`; create `internal/host/testdata/ip-d-j-address.json`.

**Interfaces — produces:** `host.LinkStatus` gains `Kind string` and `PermanentAddress string`, both tagged `json:"-"`. `Kind` is `linkinfo.info_kind` or empty. `PermanentAddress` is `permaddr` when present, otherwise `LinkAddress`. The argv is a package variable `linkInventoryArgs = []string{"-d", "-j", "address", "show"}` used by `ReadLinks`.

- [ ] **Step 1:** Capture a real fixture on the lab VM: in `unshare -n`, create a dummy `d0`, set a different current MAC on it, add VLAN `v6` and bridge `br0`, and save `ip -d -j address show` as the testdata file.
- [ ] **Step 2:** Add `TestParseLinksObservesKindAndPermanentAddress` over that fixture: `d0` has `Kind` `"dummy"` and `PermanentAddress` equal to its `permaddr`; `v6` is `"vlan"`, `br0` is `"bridge"`, `lo` is `""` with `PermanentAddress` equal to `LinkAddress`. Marshal the result and assert the JSON contains none of `Kind`, `PermanentAddress`, `permaddr`. Add `TestLinkInventoryArgs` asserting the variable equals `-d -j address show`.
- [ ] **Step 3:** `go test ./internal/host` — FAIL. Implement. PASS.
- [ ] **Step 4:** Commit `feat: observe link kind and permanent address`.

### Task 2: Read one saved version with its secret values

**Files:** modify `internal/config/history.go`, `internal/config/views.go`, `internal/config/store_integration_linux_test.go`.

**Interfaces — produces:**

```go
// RealizationInput carries secret values. It has no JSON form and must never be serialised or logged.
type RealizationInput struct {
    SavedVersion Version
    Value        json.RawMessage   // identical bytes to SnapshotView.Value for that version
    Secrets      map[string]string // name -> value, only for secrets a link references
}
// version nil means the saved head. Unknown version: Fault NOT_FOUND.
func (s *Store) RealizationInput(ctx context.Context, version *Version) (RealizationInput, error)
```

- [ ] **Step 1:** Add `TestStoreRealizationInput`: save the network fixture with `WAN_PASSWORD` set to `TEST_ONLY_FIRST` (version 1), replace the secret with `TEST_ONLY_SECOND` (version 2). Assert: `nil` returns version `2` and `TEST_ONLY_SECOND`; version `1` returns `TEST_ONLY_FIRST`; `Value` equals `Snapshot().Value` byte for byte at the head and contains neither secret; after removing the PPPoE link (version 3) `Secrets` is empty although the secret still exists; version `9` is a `NOT_FOUND` fault.
- [ ] **Step 2:** Build the tagged test — FAIL (undefined). Implement on the existing `s.read` path, reusing `publicSnapshot` for `Value`.
- [ ] **Step 3:** `go test ./internal/config` and the tagged test as root on the lab — PASS.
- [ ] **Step 4:** Commit `feat: read a saved version with its paired secrets`.

### Task 3: Render networkd files for Ethernet, VLAN, bridge and bond

**Files:** create `internal/host/realizer_linux.go` (type only in this task), `internal/render/{model,resolve,networkd,render}.go`, `render_test.go`, `testdata/`.

**Interfaces — consumes** Task 1's `host.LinkStatus`. **Produces:**

```go
// package host
type RealizerFacts struct {
    ForeignFiles []string // sorted absolute paths
    PPPDaemon, PPPoEPlugin, PPPDevice bool
}

// package render
type Input struct {
    Declaration json.RawMessage    // RealizationInput.Value
    Secrets     map[string]string
    Links       []host.LinkStatus
    Facts       host.RealizerFacts
}
type File struct {
    Path    string   `json:"path"`
    Mode    string   `json:"mode"`     // "0644" or "0600"
    Secret  bool     `json:"secret"`
    Content string   `json:"content"`  // redacted form
    Sources []string `json:"sources"`  // sorted JSON Pointers
    real    string
}
func (f File) Real() string            // equals Content when !Secret
type Finding struct{ Severity, Code, Path, Message string } // json: severity, code, path, message
type Resolution struct{ Link, Interface string }            // json: link, interface
type Result struct {
    Realizable bool         `json:"realizable"`
    Resolved   []Resolution `json:"resolved"`  // sorted by link ID
    Files      []File       `json:"files"`
    Findings   []Finding    `json:"findings"`
}
func Network(in Input) (Result, error) // error only for an undecodable declaration
```

Rendering rules are the spec's "Observed links and interface names", "networkd files" and "What is omitted" sections verbatim. Decisions the spec leaves to this plan:

- Section order in a `.network`: `[Match]`, `[Link]`, `[Network]`, `[DHCPv4]`, `[DHCPv6]`, `[IPv6AcceptRA]`, `[DHCPPrefixDelegation]`, `[IPv6SendRA]`, `[IPv6Prefix]`. `Address=` and `Gateway=` live in `[Network]`. Keys within a section in the order the spec lists them; repeated keys sorted by value.
- `SubnetId=` is `0x` + the canonical lowercase id. `[IPv6Prefix] Prefix=` is the masked /64.
- `PermanentMACAddress=` uses the declared lowercase colon form.
- `UplinkInterface=` is the upstream segment's link name (resolved or derived).

Key placement was checked against `systemd.network(5)` 257.13 on the lab VM: `RequiredForOnline=`, `MTUBytes=`, `ActivationPolicy=` in `[Link]`; `LLDP=`, `DHCP=`, `Address=`, `Gateway=`, `VLAN=`, `Bridge=`, `Bond=`, `LinkLocalAddressing=`, `IPv6AcceptRA=`, `IPv6SendRA=`, `DHCPPrefixDelegation=`, `KeepConfiguration=` in `[Network]`; `UseDNS=`/`UseNTP=`/`UseHostname=`/`UseDomains=` in `[DHCPv4]` and `[DHCPv6]`; `PrefixDelegationHint=`, `WithoutRA=` in `[DHCPv6]`; `UseDNS=`, `UseDomains=` in `[IPv6AcceptRA]`; `UplinkInterface=`, `SubnetId=`, `Assign=` in `[DHCPPrefixDelegation]`; `Managed=`, `OtherInformation=` in `[IPv6SendRA]`; `Prefix=` in `[IPv6Prefix]`. Task 5's private-networkd run is the mechanical check.

- [ ] **Step 1:** Create fixtures. Every object ID differs from the others in its first 12 hex digits.
  - `ethernet-uplink.json`: Ethernet `wan0` MTU 1500 (MAC `52:54:00:aa:bb:01`) with a segment IPv4 `dhcp`, IPv6 `dhcp6`, `requestPrefixLength` 56; Ethernet `lan0` (`52:54:00:aa:bb:02`) with a segment static `192.0.2.1/24`, IPv6 `delegated` from the WAN segment, `subnetId` `beef`, RA enabled, `managed` false, `otherInfo` true. Covers `DHCP=yes`, `PrefixDelegationHint=::/56`, `UplinkInterface=<Ethernet name>` and `resolved`.
  - `bond.json` (two NICs, `802.3ad`, a segment with DHCPv4 and SLAAC), `bridge.json` (bridge of one NIC and one VLAN, static IPv6 /64 with RA), `disabled.json` (a disabled NIC, a disabled VLAN and a segment on it).
  - One `<name>.links.json` per fixture with observed links.
- [ ] **Step 2:** Write `TestNetworkGolden` (table over fixtures; compares each `File.Real()` and the findings JSON with `testdata/<name>.golden/`, with a `-update` flag), plus:
  - `TestNetworkIsDeterministic`: shuffle `links`, `segments`, `zones` and `members` 20 times; the list of `(Path, Real())` is identical each time, and so are the findings with `path` blanked. Rendering the same input twice gives byte-identical marshalled `Result`.
  - `TestResolveIgnoresVirtualLinksSharingTheMAC` (Review Focus 1): observed `ens18`, plus a `vlan` and a `bridge` link with the same MAC → resolved to `ens18`, no finding.
  - `TestFindings`: one case per code with its exact path — `HARDWARE_MISSING` and `HARDWARE_AMBIGUOUS` at `/links/N/permanentMac`; `NAME_COLLISION` at `/links/N` when an observed `dummy` link has the derived name, and none when the observed link's kind matches; `FOREIGN_CONFIGURATION` with empty path, one per file; `DEPENDENCY_UNAVAILABLE` at `/links/N/parent`, `/links/N/members/M`, `/segments/N/link`, `/segments/N/addressing/ipv6/from`; `RA_PREFIX_UNSUPPORTED` at `/segments/N/addressing/ipv6/ra`.
  - `TestMissingMemberKeepsMaster`: a bond with one missing NIC still renders the bond and the present member.
  - `TestUnrelatedLinksRenderWhenOneNICIsMissing`.
  - `TestUserTextNeverRendered`: names and descriptions set to `X]\n[Network]\nDHCP=yes`; no file contains `X]`.
  - `TestEmptyDeclaration` (Review Focus 3): marshalled result is `{"realizable":true,"resolved":[],"files":[],"findings":[]}`.
- [ ] **Step 3:** `go test ./internal/render` — FAIL (package missing).
- [ ] **Step 4:** Implement. Generate goldens with `-update`, then read every golden line against the spec tables before accepting it.
- [ ] **Step 5:** `go test ./internal/render` — PASS. Mutation check: change `LLDP=no` to `LLDP=yes` in the renderer, confirm the golden test fails, restore.
- [ ] **Step 6:** Commit `feat: render networkd files from saved network configuration`.

### Task 4: Render PPPoE

**Files:** create `internal/render/pppoe.go`; modify `render.go`, `render_test.go`, `testdata/`; amend the spec.

**Interfaces — consumes** Task 3's types. **Produces** three more files per rendered PPPoE link and the findings `UNSUPPORTED_COMBINATION`, `CREDENTIAL_TOO_LONG`, `TOOL_MISSING`, `KERNEL_SUPPORT_MISSING`, `INTERFACE_NAME_UNSUPPORTED`; private `quoteOption(string) string` and `deviceUnit(ifname string) string`.

Exact content is the spec's PPPoE section. Decisions left to this plan:

- Options order: exactly as listed in the spec, then the IPv4 lines, then the IPv6 lines.
- `deviceUnit` returns `sys-subsystem-net-devices-` + the name escaped as `systemd-escape --path` does for one path component (ASCII alphanumerics, `:`, `_` and `.` kept; everything else `\xNN`) + `.device`.
- **Spec amendment, `INTERFACE_NAME_UNSUPPORTED` (error, `/links/N/parent`):** when a PPPoE parent is a physical NIC whose kernel name contains anything outside `[A-Za-z0-9_.:-]`, the PPPoE link is not rendered. Such a name would need three different quoting rules (`%` and `$` in a unit, `#` and quotes in an options file); refusing is simpler and such names do not occur with predictable naming. Add the row and one sentence to the spec in this task's commit.
- `KERNEL_SUPPORT_MISSING` message: `The running kernel has no PPP support (/dev/ppp is absent). Debian cloud kernels omit it; install the standard linux-image package.`
- `CREDENTIAL_TOO_LONG` paths: `/links/N/username`, `/links/N/password`; length in bytes of the unescaped value.
- `UNSUPPORTED_COMBINATION` path: the offending field (`/segments/N/addressing/ipv6/mode`, `/segments/N/addressing/ipv6/ra`, or `/segments/N/addressing` when both families are `none`); `/links/N` for a PPPoE link with no segment.

- [ ] **Step 1:** Fixtures: `owner.json` — Ethernet `wan0` 1508, VLAN 6 at 1508, PPPoE at 1500 (username `kpn@internet`, password secret `WAN_PASSWORD`) with a segment IPv4 `ppp`, IPv6 `dhcp6`, `requestPrefixLength` 48; Ethernet `lan0` with static IPv4, IPv6 `delegated` from the PPPoE segment, RA. `pppoe-1492.json` from `internal/config/testdata/network.json` with distinct ID prefixes. `pppoe-ethernet.json` — PPPoE directly on a NIC observed as `enx-1.2`, IPv4 `ppp`, IPv6 `none`. Tests supply `Secrets{"WAN_PASSWORD":"kpn"}`.
- [ ] **Step 2:** Add tests:
  - Goldens for the three fixtures: `.options` (real and redacted), `.service`, PPP `.network`; the delegated LAN's `UplinkInterface=` is the `pp…` name; `pppoe-ethernet` has `BindsTo=sys-subsystem-net-devices-enx\x2d1.2.device`, `nic-enx-1.2` and `noipv6`.
  - `TestRedactionByConstruction` (Review Focus 2): username `kpn@internet`, password `kpn`. The options file's `Content` contains `user "kpn@internet"` and `password "<redacted>"` and does not contain `password "kpn"`; `Real()` contains `password "kpn"`. Repeat with a sentinel `TEST_ONLY_PPP_SECRET` and assert it is absent from the marshalled result and from a captured `slog` buffer.
  - `TestQuoteOption`: `a "q" \ # b` + newline + `c`, tab, carriage return → only `\` and `"` are escaped, the result is one double-quoted word.
  - `TestDeviceUnit`: `enx-1.2` → `sys-subsystem-net-devices-enx\x2d1.2.device`; `eth0` unchanged.
  - `TestPPPoEFindings`: a 256-byte username and a 300-byte sentinel password each raise `CREDENTIAL_TOO_LONG`, the sentinel is absent from the marshalled result, and 255 bytes raise nothing; IPv6 `static`, `delegated`, RA enabled, and both-`none` each raise `UNSUPPORTED_COMBINATION` and render no PPPoE files; a segment-less link is a warning; each false `Facts` field raises its finding while files still render; a disabled PPPoE link raises none; parents observed as `a%b`, `a$b`, `a#b` and `a b` raise `INTERFACE_NAME_UNSUPPORTED` and render no PPPoE files (Review Focus 5).
  - `TestPPPoEMTU`: 1500 renders `mtu 1500`/`mru 1500`; 1492 renders 1492.
- [ ] **Step 3:** `go test ./internal/render` — FAIL. Implement; generate and read the goldens against the spec.
- [ ] **Step 4:** `go test ./internal/render` and `go test -race ./internal/render` — PASS.
- [ ] **Step 5:** Commit `feat: render PPPoE as pppd options, a service and a PPP network`.

### Task 5: Host facts and tool-level proof

**Files:** modify `internal/host/realizer_linux.go`; create `realizer_linux_test.go`, `internal/render/tools_integration_linux_test.go`.

**Interfaces — produces** `func ReadRealizerFacts() (RealizerFacts, error)`. Private `readRealizerFacts(etcDir, runDir, pppd, pluginGlob, device string)` takes the paths so tests use temp directories. Foreign means: any `.network`/`.netdev`/`.link` regular file or symlink directly in `/etc/systemd/network`; the same in `/run/systemd/network` unless its name starts with `50-andaru-`. A missing directory is empty, not an error; any other read error is returned. Plugin glob `/usr/lib/pppd/*/pppoe.so`.

- [ ] **Step 1:** `TestReadRealizerFacts`: temp dirs holding `10-netplan-eth0.network`, `50-andaru-x.network`, `notes.txt`, a `.link` in the etc dir, a subdirectory named `x.network`; assert exactly the two foreign paths, sorted. Missing directories → an empty non-nil slice. Tool booleans follow file presence.
- [ ] **Step 2:** Run — FAIL; implement; PASS. Commit `feat: observe foreign networkd files and pppd availability`.
- [ ] **Step 3:** Write the tagged root tests. Fixtures are embedded. Each test renders with real secrets into a `0755` root-owned temp directory under `/var/tmp`; the options file itself stays `0600`.
  - `TestPppdAcceptsRenderedOptions`: fail if `/dev/ppp`, `/usr/sbin/pppd` or `unshare` is missing. For `owner` and `pppoe-ethernet`, run the dry-run command from "Probe results" with the rendered parent name. Assert exit 0 and that output contains `ifname pp`, the rendered `mtu` and `mru`, and, for a render whose username is `a "q" \ # b`+newline+`c`, the line `user a \"q\" \\ # b\nc`. Assert the password sentinel is absent from the output. Append `bogusopt` and assert exit 2, proving the check can fail.
  - `TestSystemdAcceptsRenderedUnit`: write each rendered unit under its own file name; `systemd-analyze verify <path>` exits 0 with empty combined output. For `pppoe-ethernet`, assert the unit's `BindsTo=` value equals `systemd-escape --path /sys/subsystem/net/devices/enx-1.2` + `.device`. Add `Bogus=1` under `[Service]` and assert the output is non-empty.
  - `TestNetworkdAcceptsRenderedFiles`: one private networkd run **per fixture**, as a single `sh -c` under `unshare -n -m` that mounts tmpfs on `/run/systemd/network` and `/run/systemd/netif`, runs `install -d -o systemd-network -g systemd-network -m 0755` on the `netif` subdirectories `links`, `leases`, `lldp`, copies that fixture's `.netdev`/`.network` files in as root with mode `0644`, then runs networkd with a five-second `timeout`. Assert every file logs `Successfully loaded.` and no log line naming a `50-andaru-` file contains `Unknown`, `Failed`, `Invalid` or `ignoring`. Inject `BogusKey=1` into one file and assert the check fails. Before and after, `ip -j link show` on the host is identical.
- [ ] **Step 4:** Build `render.test` with `CGO_ENABLED=0 go test -c -tags=integration`, copy it to the lab VM and run as root — PASS. Record the three results for the guide.
- [ ] **Step 5:** Commit `test: prove rendered files against pppd, systemd and networkd`.

### Task 6: Preview API

**Files:** create `internal/engine/preview_api.go`, `preview_integration_linux_test.go`; modify `internal/engine/api.go`, `socket_linux.go`, `openapi.json`, `api_test.go` (the `newHandler` call sites and `assertRequiredSchema`, which must learn `boolean` and `object`), the socket integration test's denial list.

**Interfaces — consumes** `Store.RealizationInput`, `host.ReadLinks`, `host.ReadRealizerFacts`, `render.Network`. **Produces:**

```go
type previewResponse struct {
    State        string         `json:"state"`        // "preview"
    SavedVersion config.Version `json:"savedVersion"`
    render.Result                                    // inlined
}
func newPreviewHandler(store *config.Store, readLinks func(context.Context) ([]host.LinkStatus, error), readFacts func() (host.RealizerFacts, error)) http.Handler
```

Routed for exactly `/api/v1/preview/network` from the existing top-level handler, after peer authorization. Methods `GET`, `HEAD`; others `405` with `Allow: GET, HEAD`. Query: only `savedVersion`, at most once, parsed with `config.ParseVersion`; otherwise `400 INVALID_REQUEST`. Order: read the store first, so an unknown version is `404` even when the host cannot be read; store faults go through `writeConfigurationError`. Then links and facts: failure is `503 INVENTORY_UNAVAILABLE`. A `render.Network` error is `503 STORE_UNAVAILABLE` with a secret-free diagnostic. Success sets `Andaru-Saved-Version`. `Serve` wires the real readers.

- [ ] **Step 1:** Tests against a real `Store` (tagged, root) with stub readers:
  - contract: owner fixture → `200`, `state` `preview`, files present, the options file `secret` true with `<redacted>`; response body and captured logs never contain the password sentinel; every field named in the OpenAPI schema is present with its type.
  - `HEAD` has headers and no body; `POST` is `405`.
  - `?savedVersion=x`, `?savedVersion=1&savedVersion=2`, `?other=1` → `400`; `?savedVersion=99` → `404`, also when the link reader fails.
  - Review Focus 4: version 1 with a 300-byte password, version 2 with `kpn`; `?savedVersion=1` reports `CREDENTIAL_TOO_LONG`, the default does not.
  - Review Focus 3: empty store → arrays are `[]`.
  - reader failure → `503 INVENTORY_UNAVAILABLE`, no partial body.
  - extend the existing UID 65534 denial list with `GET /api/v1/preview/network`, asserting neither the store nor a reader was called.
- [ ] **Step 2:** Build tagged — FAIL. Implement. Add the path and the `PreviewResponse`, `RenderedFile`, `Finding`, `Resolution` schemas to `openapi.json`; bump `info.version` to `0.3.0-dev`.
- [ ] **Step 3:** `go test ./...`, then the tagged engine suite as root on the lab — PASS.
- [ ] **Step 4:** Commit `feat: preview rendered network files over the local API`.

### Task 7: CLI, guide and lab qualification

**Files:** modify `cmd/andaru/ctl.go`, `main.go` (usage), `main_test.go`, `ctl_integration_linux_test.go`, `docs/dev/core-api.md`, `README.md`.

**Interfaces — consumes** the Task 6 route. Grammar: `andaru ctl [--socket PATH] preview network [--saved-version N]`.

- [ ] **Step 1:** Tests: usage errors (`preview`, `preview other`, `preview network extra`, `--saved-version x`) exit 2 with no stdout; a stub server returning a body without `"state":"preview"`, with `files: null`, or with a non-array `findings` exits 1 with no stdout; the tagged round trip saves the owner fixture through the CLI, runs `preview network`, and checks valid JSON, `<redacted>` present and the password sentinel absent.
- [ ] **Step 2:** Run — FAIL. Implement `validEnvelope` for kind `preview` and the request mapping. PASS.
- [ ] **Step 3:** Operator guide: the command and response fields; every finding code with its remedy; how to test a real PPPoE line (copy the three files to their paths, replace `<redacted>`, `systemctl daemon-reload`, `networkctl reload`, start the unit; undo by stopping the unit, deleting the files and reloading both); the standard-kernel requirement; that preview changes nothing; that the tool proofs run on the lab only. Correct the line stating the engine runs `ip -j address show`. README: one line.
- [ ] **Step 4:** `go test ./... && go test -race ./... && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`; build amd64 and arm64 with `CGO_ENABLED=0 -trimpath`.
- [ ] **Step 5:** Lab qualification on VM 114. Capture `nft list ruleset`, forwarding sysctls and `ip -j address show`. Install the binary over the previous one after checking its hash; restart `andaru-engine`. Save the owner topology with the VM's real `eth0` MAC on the **WAN** NIC and an absent MAC on the LAN NIC; run `andaru ctl preview network`. Require: `FOREIGN_CONFIGURATION` naming both netplan files; the WAN NIC resolved to `eth0`; the VLAN, PPPoE options, unit and PPP `.network` present with `<redacted>`; no `KERNEL_SUPPORT_MISSING` or `TOOL_MISSING`; `HARDWARE_MISSING` for the LAN NIC and `DEPENDENCY_UNAVAILABLE` for its segment. Re-capture and require the three host observations byte-identical. Delete the synthetic objects, leaving an empty saved topology. Record the commit, binary hash and results in the guide.
- [ ] **Step 6:** Commit `feat: preview network realization from the CLI`, then `docs: record network preview lab qualification`.
- [ ] **Step 7:** One condensed adversarial review of the branch; fix findings with red/green evidence; open the PR, register pr-watcher, and after green CI request Codex and CodeRabbit. Do not merge without owner approval.

---

## Self-review

- **Spec coverage:** observed links and names → Tasks 1, 3; networkd files and omission rules → Task 3; PPPoE → Task 4; findings → Tasks 3–5; API and CLI → Tasks 6–7; acceptance 1–4 → Tasks 3–4, 6; 5–7 → Task 5 (lab); 8 → Task 7.
- **Spec changes made by this plan:** `KERNEL_SUPPORT_MISSING` and the probe results (already in the spec); `INTERFACE_NAME_UNSUPPORTED` (Task 4); tool proofs on the lab rather than CI.
- **One adversarial plan review** was applied: determinism assertion, embedded fixtures and per-fixture networkd runs, a lab check that can fail, an Ethernet-parent PPPoE fixture, and the CI decision above.
- **Execution:** Native is recommended. Tasks 3–6 share one set of types and the design has had three review rounds; one whole-branch review at the end is the gate.
