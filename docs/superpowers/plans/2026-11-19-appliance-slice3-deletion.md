# Appliance image, slice 3: delete Debian, takeover and upgrade

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** remove everything that existed only because Andaru installed onto a Debian host:
- the Debian packaging and AppArmor profiles;
- the APT upgrade package;
- the netplan and ifupdown takeover, with its baselines, hand-back, assistant, proposal API and host readers;
- the Debian VM lab.

What Andaru still does on the image stays, and behaves the same: applying and confirming generations, boot, the filters and catch-all, kept copies, and factory reset.

**Spec:** slice 3 of [Andaru as an appliance image](../specs/2026-11-18-appliance-image-design.md), and the boxes it already runs on: images from slice 1 (`v0.5.0-a.1`) and nightlies.

## Calls for the owner

1. **The Debian VM lab goes.** `dev/vm-lab/`, `docs/dev/vm-lab.md` and the `.gitignore` lab block build two Debian 13 golden images (netplan and ifupdown). Qualification now happens on the image, on Proxmox.
2. **The draft route goes.** `POST /api/v1/preview/network/draft` judges an unsaved batch, and only `andaru ctl takeover` uses it. Setup (slice 4) can add what it needs when it needs it.
3. **`config.DHCPOffer` and `config.Store.Draft` go** with their only callers, the takeover assistant and the draft route, and with their tests. The installer slice can bring `DHCPOffer` back from history if it needs it.
4. **Persisted state stays readable.** These files are decoded strictly (`DisallowUnknownFields`), and boxes already in the field carry these keys:
   - `generations/pointers.json`: `takeover`.
   - each generation's `manifest.json`: `takeover` and `baseline`.
   - `generations/baseline/manifest.json`, which every reset tree has: `kind`, `files`, `reset`, and possibly `ifupdown` and `nftablesMasked`.

   The reset tree is identified by that file's `"reset": true` (`resetTree()`), not by `.andaru-reset`, which a finished reset removes. Every one of these keys stays in its struct. The obsolete ones are typed `json.RawMessage`, so no host type outlives the deleted readers; they are ignored, and never written again. The reset journal's `ownership` is decoded leniently, so dropping its takeover fields is safe.
   - The reset marker keeps its path and meaning: older images on a bootc rollback read it there.
   - Existing fixtures in the old format (`internal/engine/live_authorization_integration_test.go:47`, `internal/engine/backup_integration_linux_test.go:323`) become compatibility tests.
5. **The applies API drops `takeover` in one commit.** The engine's request and response, the CLI's `--takeover` flag, the web client and `openapi.json` change together. They ship in one image, and the API is provisional until 1.0. A browser tab left open across the image update gets a 400 on apply until it reloads; that is accepted.
6. **No update fence in reset yet.** Reset's dpkg locks go. Nothing on the image updates by itself: zincati is off, and `image-smoke.sh` now also checks that `bootc-fetch-apply-updates.timer` and `rpm-ostreed-automatic.timer` are not enabled. So there is nothing to fence against until slice 5, which adds the fence with the update path itself. The appliance design's slice 3 text, which promised the fence here, is amended to say so.

## Global constraints

- Every commit builds, and passes `go vet`, `go test ./...`, shellcheck and shellspec.
- The root suites in the Fedora container (`scripts/ci/root-tests.sh`) pass.
- The image builds and passes `image-smoke.sh`.
- Behaviour on the image does not change. A test that covered kept behaviour on a netplan bench is moved to a factory or plain bench, not deleted.
- No new feature. Where kept code loses a branch, the branch is deleted, not stubbed.

## Tasks

### Task 1: reset stops importing `upgrade`, and `upgrade` goes
One commit: `upgrade.Prepare` uses the same helpers until it is deleted.
- [ ] Move into `internal/reset`, with their test:
  - `CheckIdle` (without dpkg), `StopWriters` and `WriterUnits`;
  - their helpers: `commandRunner`, `preparationUnits`, `realizationUnits`, `drainRealizers`, `idleRealizationJobs`, `idleApplications`, `textOutput` and `outputLimit`. `privateJSON` and `regularFile` are not moved: reset's existing `readPrivate` does the same job.
- [ ] Delete:
  - `DpkgConsistent`, `dpkgBinary`, `Env.PackageLocks`, `LockWait` and `holdPackageLocks`;
  - the dpkg lock list in `cmd/andaru/reset.go`, and `LockWait` in `cmd/andaru/reset_test.go`;
  - the `"dpkg"` case of `TestPlanRefuses`.
- [ ] Reset's tests take over what the deleted `upgrade` tests covered for these helpers:
  - `TestPlanRefuses` gains `activating`, `awaiting_confirmation` and `rolling_back` `state.json` cases, and a running `andaru-realize-*` unit;
  - a stop test shows an active `andaru-prefixes.service` is drained, not killed.

  The reset `box` fake learns to answer `list-units` and `ActiveState` for these.
- [ ] Delete `internal/upgrade`, `cmd/andaru/upgrade.go` and its tests, and the `upgrade` dispatch and usage in `cmd/andaru/main.go`.
- [ ] Remove `upgrade.test` from `ci.yml`. Add `reset.test` to the root suites (`ci.yml`'s build list and `scripts/ci/root-tests.sh`): reset's tests need root, and nothing in CI runs them today.
- [ ] Delete core-api's "Upgrading on Debian".
- [ ] `image-smoke.sh` checks the two update timers (call 6), including after its `preset-all` step. Amend the appliance design's slice 3 and slice 5 text for the fence.

### Task 2: delete Debian packaging, AppArmor and the Debian lab
- [ ] Delete `debian/`, `packaging/apparmor/`, `dev/vm-lab/`, `docs/dev/vm-lab.md` and the `.gitignore` lab block.
- [ ] Delete the Debian-only CI scripts: `build-packages.sh`, `install-test.sh`, `install-check.sh`, `upgrade-check.sh`, `backup-check.sh`.
- [ ] Delete the Debian-only shell specs: `apparmor_include_spec.sh`, `apparmor_restart_spec.sh`, `maintainer_scripts_spec.sh`.
- [ ] Trim `kept_copies_spec.sh` (the postrm part) and `web_packaging_spec.sh` (postinst, `debian/rules`, `andaru.install`, AppArmor).
- [ ] Fix `ci.yml`'s shellcheck list.
- [ ] Drop the Debian-only `Before=` targets from `andaru-network-boot.service`: `networking.service` and `cloud-init-local.service`. Keep `Before=nftables.service`: Fedora ships that unit, and the ordering keeps it from flushing Andaru's table at boot if an admin enables it.

### Task 3: delete the CLI takeover assistant
- [ ] Delete `cmd/andaru/takeover.go`, `takeover_answers.go`, `takeover_forms.go`, their tests, and the `ctl takeover` dispatch and usage.
- [ ] `go mod tidy`, which drops `huh` and `bubbletea`.

### Task 4: delete the engine takeover API and `takeover` from applies, gates, CLI and web (call 5)
One commit: the API's handlers, the gates and the request field depend on each other, and the strict request decoder needs engine, CLI and web to change together.
- [ ] **Handlers:**
  - Move `gatherGate` (without its takeover and ifupdown parts) and the error values into `applies_api.go`.
  - Delete `newLiveHandler` and `isLivePath`: without the takeover handler, `newLiveHandler` only passes requests through, so its callers take the applies handler directly.
  - Delete the proposal handler, the draft route (call 2) with `config.Store.Draft`, `config.DHCPOffer` (call 3), the identity readers and `previewIdentities`.
  - Adapt `internal/config/backup_state_integration_linux_test.go`, which also calls `Draft`.
  - Remove `identitySource` from the preview handler, `render.Input.DHCPIdentity` and its networkd output. Its only sources were an ifupdown capture and a running dhcpcd, neither of which exists on the image, so no image box's DHCP lease identity changes. Declared DUIDs (`networkd.go`) stay.
- [ ] **Gates:**
  - `gateInput` loses its takeover and ifupdown fields, `fromIfupdown`, `ifupdownHost`, the baseline block, the FOREIGN_CONFIGURATION exemption, TAKEOVER_MISMATCH and `ifupdownFindings`.
  - It keeps BOOT_UNIT_DISABLED, APPLY_UNSUPPORTED, the managed-link WOULD_UNCONFIGURE with the factory file, NETDEV_CHANGE, and the refusal of every render error finding.
  - `TestFilterGates` (FOREIGN_FILTER, TOOL_MISSING, MANAGEMENT_REQUIRED) moves from the draft tests to the applies bench.
- [ ] **Host reader:** `hostReader` loses Route, Resolved and Root, and `Baseline` becomes `BootUnit`, read through `host.ReadBaseline().BootUnit` until Task 6 replaces the reader.
- [ ] **API surface:**
  - The applies request and view lose `takeover`, and so do the CLI flag, `web/src/api.ts`, `web/src/types.ts`, the web test and `openapi.json`.
  - `security.go` and `openapi.json` lose the proposal and draft routes. `internal/engine/config_integration_linux_test.go`'s expected contract paths change with them.
  - `openapi.json` loses the codes with no producer left: `TAKEOVER_LIMIT`, `TAKEOVER_MISMATCH`, `TAKEOVER_UNSUPPORTED_BASELINE`, `DNS_REQUIRED`, `RESOLVED_NOT_IN_CHARGE` and `DHCP_IDENTITY_UNKNOWN`.
- [ ] **Benches:** rebase the applies and gates test benches from a netplan host onto a plain one.

### Task 5: delete the generation takeover code
- [ ] Move `Confirm` (without the takeover tail), `copyKeptFirst`, `Boot` and `consoleRequired` out of `takeover.go`, with their core tests and helpers.
- [ ] `Boot` keeps:
  - disaster boot;
  - the pending-to-rolled-back record;
  - the `Confirmed == nil` fence and console-required paths;
  - the candidate loop with link renames, filter load, placement and prefixes;
  - the fence with `Live = nil` when every candidate fails.

  It loses the baseline, the netplan cleanup, the ifupdown boot and the host-firewall hand-back.
- [ ] `Activate` and `Rollback` lose ifupdown, hand-over and hand-back, `swapAndVerify` loses `needServers`, and `Realizer.HandBackWait` goes. Comments that mention ifupdown in `readManaged` and `observe` are reworded; their logic stays, since networkd can still be stopped. Then:
  - `live` becomes "the catch-all when set up", with no baseline;
  - `fallback` keeps today's three outcomes, each with a test:
    - with no previous generation (the rollback of a box's first apply), it returns the catch-all set with the bare filter, as today;
    - when every candidate fails on a reset tree (`reset: true`), it restores the catch-all set and records it as the baseline;
    - when every candidate fails on any other tree, it errors and keeps what runs;
  - `toward` drops `handsBack`;
  - `setUp` and `Teardown` drop `Takeover`.
- [ ] `Ownership` shrinks to `Live`. Reset and `cmd/andaru/reset.go` test `Live` only.
- [ ] Keep the obsolete JSON fields per call 4. Add golden files with the exact bytes the `v0.5.0-a.1` image writes, for `pointers.json`, a generation `manifest.json`, and a reset tree's `baseline/manifest.json`. Read them through `ReadPointers`, the generation reader, `ReadOwnership` and `Boot`.
- [ ] Delete `takeover.go`, `ifupdown.go`, `hostfirewall_test.go` and the netplan-only tests.
- [ ] Rebench the remaining filter, services, teardown, ownership and reset tests onto plain or reset trees.

### Task 6: delete the host readers
- [ ] Delete `netplan_linux.go`, `ifupdown_linux.go`, their tests and `internal/host/fixture`.
- [ ] Delete `ReadDefaultRoutes` and `ReadResolved`.
- [ ] `Baseline` and `ReadBaseline` become a `BootUnit` reader, tested on its own.

### Task 7: docs
- [ ] core-api:
  - delete the takeover, assistant and ifupdown sections, the proposal row, and the Debian qualification notes for deleted features;
  - rewrite "Applying" without `--takeover`, the factory-reset section without dpkg locks and takeover ownership, and the DHCP/DNS sections without `apt install` and AppArmor.
- [ ] README: drop the netplan takeover sentence and the Debian lab paragraph.
- [ ] The comment about `packaging/apparmor` in `internal/render/services_integration_linux_test.go` goes.
- [ ] Mark the superseded specs "deleted in slice 3".

### Task 8: qualification
- [ ] CI is green, including the Fedora-container root suites and the image build and smoke on both architectures.
- [ ] On a lab box with a confirmed generation, built from `main` before this slice, upgrade to an image of this branch. Check that:
  - the old `pointers.json` and manifests still load;
  - boot restores the confirmed generation;
  - an apply and confirm work;
  - a factory reset ends up on `last-lan/`.
- [ ] `bootc rollback` to the image from before this slice, then check that boot restores the generation and that a reset tree still reads, as call 4 promises.
- [ ] On a reset tree, which carries the baseline manifest with `reset: true`, check after the upgrade that boot restores the reset's confirmed generation (`last-lan/`) when it has one. With no usable generation, boot stays console-required behind deny-all and the catch-all, and never hands the host back.

## Out of scope

- The engine-web peer file that only Debian's postinst wrote: slice 4 writes it on the image.
- An update fence for reset: slice 5 (call 6).
- Archiving the `andaru-project/apt` repository, which the spec lists under slice 3: it is a repository setting, done by the owner after this slice merges, and the PR says so.
- New behaviour of any kind.
