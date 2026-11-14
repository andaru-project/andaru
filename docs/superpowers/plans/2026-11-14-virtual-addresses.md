# Virtual Addresses Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Addresses held by the firewall on no LAN (a dummy interface), the DNS service listening on them, and IPv6 port forwards to the firewall itself.

**Architecture:** A `virtualAddresses` collection, like `dhcp6`, stored in a new `vip_changes` table. The renderer writes `andaru-vip` netdev and network files, Unbound listeners, and IPv6 forwards in the existing `inet andaru` nat prerouting and input chains; virtual addresses count as the firewall's own and as internal for reflection. A change that drops or re-addresses a virtual address a forward targets is refused.

**Tech Stack:** Go, JSON Schema, SQLite, systemd-networkd 257, nftables, Unbound.

**Spec:** `docs/superpowers/specs/2026-11-14-virtual-addresses-design.md`

## Global Constraints

- **Names:** collection `virtualAddresses`, ID prefix `vip`, API `/api/v1/config/virtualAddresses`, CLI `virtualAddresses`; table `vip_changes`; device `andaru-vip`, files `/run/systemd/network/50-andaru-andaru-vip.netdev` and `.network`; DNS field `dns.virtualAddresses`; port forward field `family` (`ipv4` default, `ipv6`).
- **Limits:** at most 64 virtual addresses; single addresses only (`/32`, `/128` in networkd).
- **Messages (exact):** IPv6 forward to anything the firewall does not hold: "IPv6 port forwards to inside hosts are not offered (#43)."; gate: "The DNS service on virtual addresses needs the packet filter, which needs a management segment. Set management.segment."
- **No change without the new fields:** every existing golden stays byte for byte.
- **Tests:** `go test ./internal/... ./cmd/...`; goldens with `-update`, diff read before committing.

## Review Focus

1. **Deleting or re-addressing a virtual address a forward targets** is refused on the virtual address, for both families (Task 1).
2. **Reflection:** a LAN client to a virtual address reaches the service, never a reflected self-forward (Task 2).
3. **Removing every virtual address** removes the device (`networkctl delete andaru-vip`), not just its files (Task 3).
4. **Takeover and link inventory:** the `andaru-vip` dummy is never proposed or bound as a link (Task 3).
5. **Old stores** without `vip_changes` open and save (Task 1).

---

### Task 1: Declaration, validation, store, API

**Files:** `internal/config/model.go`, `schema.json`, `schema.go` (collection map, `safePointer` fields `virtualAddresses family`), `validate.go` or a new `virtual.go`, `nat.go` (IPv6 forwards), `changes.go` (collections, `vip` prefix, `objects`, `rewrite`, `resolveReferences` for `dns.virtualAddresses`, the re-address check), `sqlite.go` (`vipDDL`, `changeTable`), `store_linux.go`, `history.go` (UNION); `internal/engine/config_api.go`, `openapi.json` (collection enum, `family`), `cmd/andaru/ctl.go` (collection name, `objectIDPattern` adds `vip`).

**Interfaces:** produces `virtualAddress{identity; Address string; Description string}`, `dnsService.VirtualAddresses []string`, `portForward.Family string` (empty means `ipv4`), and `firewallHolds(d *declaration, a netip.Addr) bool` (a segment's static address or a virtual address).

- [ ] **Step 1: failing tests.**
  - `TestVirtualAddressValidation` (table-driven): accept `172.16.53.53` and `fd00:53:53:53:53:53:53:53`; refuse at `/virtualAddresses/N/address`: `0.0.0.0`, `224.0.0.1`, `127.0.0.2`, `::1`, `fe80::1`, `::ffff:1.2.3.4`, a `/32` with a length, a duplicate, a segment's static address, an address inside a segment's subnet; a 65th entry; a link named `andaru-vip`.
  - `TestDNSVirtualAddresses`: an unknown ID refused at `/dns/virtualAddresses/0`; deleting a referenced virtual address refused.
  - `TestIPv6PortForwards`: accept `family: ipv6` with `to.self` and target `self`, with target a static IPv6 segment address, with target an IPv6 virtual address, with a port range and `targetPort`; refuse a target `2001:db8::5` with the exact #43 message, `reflection: true`, an IPv4 `from` object, an IPv4 target; an IPv4 forward still refuses IPv6 objects; two IPv6 forwards on the same ports conflict, an IPv4 and an IPv6 one do not.
  - `TestVirtualAddressInUse` (store, `mustRun`): with a forward targeting `172.16.53.53`, deleting its virtual address and replacing its address are refused at the virtual address; deleting the forward first, then the address, succeeds.
  - `TestStoreWithoutVIPTableOpens` (integration): drop `vip_changes`, reopen, save, list history.
  - `TestVirtualAddressRoutes` (engine integration) and `TestConfigVirtualAddresses` (ctl): list, create, get.
- [ ] **Steps 2–4:** watch them fail, implement, pass `go test ./internal/... ./cmd/...` and the config and engine integration suites.
- [ ] **Step 5: commit** `feat(config): virtual addresses and IPv6 port forwards`.

### Task 2: Render

**Files:** `internal/render/model.go`, `networkd.go` (the dummy's files), `services.go` (Unbound), `nat.go` (`ownStatic` adds IPv4 virtual addresses; IPv6 forwards), `nft.go` (`internal_4` adds IPv4 virtual addresses), testdata `virtual.json`, `.links.json`, `.golden`.

- [ ] **Step 1: failing tests.**
  - The `virtual` golden: `50-andaru-andaru-vip.netdev` with `[NetDev] Name=andaru-vip Kind=dummy`; the `.network` with `[Match] Name=andaru-vip`, `RequiredForOnline=no`, `LinkLocalAddressing=no`, `Address=172.16.53.53/32`, `[Address] Address=fd00:53:53:53:53:53:53:53/128`; Unbound `interface: 172.16.53.53`, `interface-action: 172.16.53.53 allow` and the IPv6 pair; an IPv6 forward to `self` (`meta nfproto ipv6 fib daddr . iif type local … redirect`) and to the IPv6 virtual address (`dnat ip6 to fd00:53:53:53:53:53:53:53`), each with its input rule (`meta nfproto ipv6`, the mark, the port); an IPv4 forward to `172.16.53.53` with an input rule, not a forward rule; `internal_4` holding `172.16.53.53`.
  - `TestVirtualAddressesNotWithoutThem`: existing goldens unchanged.
  - The namespace test loads the `virtual` golden's ruleset (`nft -c`).
- [ ] **Steps 2–4:** watch them fail, implement, pass `go test ./internal/render` and the namespace tests.
- [ ] **Step 5: commit** `feat(render): virtual addresses and IPv6 port forwards`.

### Task 3: Apply gate, generation, inventory

**Files:** `internal/engine/apply_gates.go` and test; `internal/generation` (a test that dropping every virtual address runs `networkctl delete andaru-vip`; the fake networkd binds the dummy's network file); `internal/host` link inventory and the takeover proposal (skip `andaru-vip`).

- [ ] **Step 1: failing tests.**
  - `TestVirtualDNSGate`: `dns.virtualAddresses` without `management.segment` refused at `/dns/virtualAddresses` with the exact message; with it, no refusal.
  - `TestDropVirtualAddressesDeletesDevice`: a live generation with the dummy's files, a new one without them: `networkctl delete andaru-vip` runs after `networkctl reload`.
  - `TestActivateWithVirtualAddresses`: the apply verifies with the dummy's network file bound.
  - `TestTakeoverSkipsVirtualDevice`: a host whose inventory lists `andaru-vip` (kind dummy) gets no link or segment for it in the proposal.
- [ ] **Steps 2–4:** watch them fail, implement, pass.
- [ ] **Step 5: commit** `feat: apply virtual addresses`.

### Task 4: Guide, gate, lab, review, PR

- [ ] **Step 1: guide:** a "Virtual addresses" section in `docs/dev/core-api.md`: the collection, `dns.virtualAddresses`, naming them in `dnsServers`, IPv6 forwards to the firewall, what is refused and why, #43.
- [ ] **Step 2: full gate:** vet, `-race`, arm64, the root suites, the namespace tests, shellspec.
- [ ] **Step 3: lab** (the spec's): VM 114 and VM 117; record "Virtual addresses Debian lab qualification" in the guide; reset the guests.
- [ ] **Step 4: land:** one condensed review, fixes test-first, the PR, CI, Codex and CodeRabbit, the merge; comment on #37 and close it in favour of #43.
