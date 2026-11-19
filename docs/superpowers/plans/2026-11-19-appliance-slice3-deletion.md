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
3. **`config.DHCPOffer` stays.** It computes a segment's default DHCP offer. The installer's default pool for the management LAN needs exactly that, so deleting it now would only bring it back in the installer slice.
4. **Persisted state stays readable.** Boxes already in the field carry `"takeover"` in `generations/pointers.json` and in each generation's `manifest.json`, and a `baseline` in manifests. Reset trees carry `generations/baseline/manifest.json` with `"reset": true`.
   - Those files are decoded strictly (`DisallowUnknownFields`), so the fields stay in the structs, marked obsolete and ignored, and are never written as true again.
   - The reset marker keeps its path and meaning: older images on a bootc rollback read it there.
   - Existing fixtures in the old format (`live_authorization_integration_test.go:47`, `backup_integration_linux_test.go:323`) become the compatibility tests.
5. **The applies API drops `takeover` in one commit.** The engine's request and response, the CLI's `--takeover` flag, the web client and `openapi.json` change together. They ship in one image, and the API is provisional until 1.0.
6. **No update fence in reset yet.** Reset's dpkg locks go. Nothing on the image updates by itself (zincati is off, and slice 5 brings engine-driven updates), so there is nothing to fence against until slice 5. Slice 5 adds the fence with the update path itself.

## Global constraints

- Every commit builds, and passes `go vet`, `go test ./...`, shellcheck and shellspec.
- The root suites in the Fedora container (`scripts/ci/root-tests.sh`) pass.
- The image builds and passes `image-smoke.sh`.
- Behaviour on the image does not change. A test that covered kept behaviour on a netplan bench is moved to a factory or plain bench, not deleted.
- No new feature. Where kept code loses a branch, the branch is deleted, not stubbed.

## Tasks

The inventory behind these tasks, with file:line references, is in the PR description.

### Task 1: reset stops importing `upgrade`
- [ ] Move into `internal/reset`, with their test:
  - `CheckIdle` (without dpkg), `StopWriters` and `WriterUnits`;
  - their helpers: `preparationUnits`, `realizationUnits`, `drainRealizers`, `idleRealizationJobs`, `idleApplications`, `privateJSON`, `regularFile`, `textOutput` and their limits.
- [ ] Delete `DpkgConsistent`, `dpkgBinary`, `Env.PackageLocks`, `LockWait`, `holdPackageLocks` and the dpkg lock list in `cmd/andaru/reset.go`.
- [ ] Delete the `"dpkg"` case of `TestPlanRefuses`.

### Task 2: delete `internal/upgrade` and `andaru upgrade`
- [ ] Delete the package, `cmd/andaru/upgrade.go` and its tests, and the `upgrade` dispatch and usage in `cmd/andaru/main.go`.
- [ ] Remove `upgrade.test` from `ci.yml`, and delete core-api's "Upgrading on Debian".

### Task 3: delete Debian packaging, AppArmor and the Debian lab
- [ ] Delete `debian/`, `packaging/apparmor/`, `dev/vm-lab/`, `docs/dev/vm-lab.md` and the `.gitignore` lab block.
- [ ] Delete the Debian-only CI scripts: `build-packages.sh`, `install-test.sh`, `install-check.sh`, `upgrade-check.sh`, `backup-check.sh`.
- [ ] Delete the Debian-only shell specs: `apparmor_include_spec.sh`, `apparmor_restart_spec.sh`, `maintainer_scripts_spec.sh`.
- [ ] Trim `kept_copies_spec.sh` (the postrm part) and `web_packaging_spec.sh` (postinst, `debian/rules`, `andaru.install`, AppArmor).
- [ ] Fix `ci.yml`'s shellcheck list.
- [ ] Drop the Debian-only `Before=` targets from `andaru-network-boot.service`: `networking.service`, `cloud-init-local.service`, `nftables.service`.

### Task 4: delete the CLI takeover assistant
- [ ] Delete `cmd/andaru/takeover.go`, `takeover_answers.go`, `takeover_forms.go`, their tests, and the `ctl takeover` dispatch and usage.
- [ ] `go mod tidy`, which drops `huh` and `bubbletea`.

### Task 5: delete the engine takeover API
- [ ] Move the kept parts of `takeover_api.go` into `applies_api.go`: the error values, `gatherGate` without its takeover and ifupdown parts, `newLiveHandler` and `isLivePath`.
- [ ] Delete the proposal handler, the draft route (call 2), the identity readers and `previewIdentities`.
- [ ] Remove `identitySource` from the preview handler, `render.Input.DHCPIdentity` and its networkd output.
- [ ] Remove the routes' permissions in `security.go`, and their `openapi.json` entries and error codes.

### Task 6: drop `takeover` from applies, gates, CLI and web (call 5)
- [ ] Engine:
  - `gateInput` loses its takeover and ifupdown fields, `fromIfupdown`, `ifupdownHost`, the baseline block, the FOREIGN_CONFIGURATION exemption, TAKEOVER_MISMATCH and `ifupdownFindings`.
  - It keeps BOOT_UNIT_DISABLED, APPLY_UNSUPPORTED, the managed-link WOULD_UNCONFIGURE with the factory file, and NETDEV_CHANGE.
- [ ] `hostReader` loses Route, Resolved and Root, and `Baseline` becomes `BootUnit`.
- [ ] The applies request and view lose `takeover`, and so do the CLI flag, `web/src/api.ts`, `web/src/types.ts`, the web test and `openapi.json`.
- [ ] Rebase the applies and gates test benches from a netplan host onto a plain one.

### Task 7: delete the generation takeover code
- [ ] Move `Confirm` (without the takeover tail), `copyKeptFirst`, `Boot` and `consoleRequired` out of `takeover.go`, with their core tests and helpers.
- [ ] `Boot` keeps:
  - disaster boot;
  - the pending-to-rolled-back record;
  - the `Confirmed == nil` fence and console-required paths;
  - the candidate loop with link renames, filter load, placement and prefixes;
  - the fence with `Live = nil` when every candidate fails.

  It loses the baseline, the netplan cleanup, the ifupdown boot and the host-firewall hand-back.
- [ ] `Activate` and `Rollback` lose ifupdown, hand-over and hand-back. Then:
  - `fallback` and `live` become "the catch-all when set up", with no baseline;
  - `toward` drops `handsBack`;
  - `setUp` and `Teardown` drop `Takeover`.
- [ ] `Ownership` shrinks to `Live`. Reset and `cmd/andaru/reset.go` test `Live` only.
- [ ] Keep the obsolete JSON fields per call 4, with a test that an old `pointers.json` and manifest still load.
- [ ] Delete `takeover.go`, `ifupdown.go`, `hostfirewall_test.go` and the netplan-only tests.
- [ ] Rebench the remaining filter, services, teardown, ownership and reset tests onto plain or reset trees.

### Task 8: delete the host readers
- [ ] Delete `netplan_linux.go`, `ifupdown_linux.go`, their tests and `internal/host/fixture`.
- [ ] Delete `ReadDefaultRoutes` and `ReadResolved`.
- [ ] `Baseline` and `ReadBaseline` become a `BootUnit` reader, tested on its own.

### Task 9: docs
- [ ] core-api:
  - delete the takeover, assistant and ifupdown sections, the proposal row, and the Debian qualification notes for deleted features;
  - rewrite "Applying" without `--takeover`, the factory-reset section without dpkg locks and takeover ownership, and the DHCP/DNS sections without `apt install` and AppArmor.
- [ ] README: drop the netplan takeover sentence and the Debian lab paragraph.
- [ ] Mark the superseded specs "deleted in slice 3".

### Task 10: qualification
- [ ] CI is green, including the Fedora-container root suites and the image build and smoke on both architectures.
- [ ] On a lab box with a confirmed generation, built from `main` before this slice, upgrade to an image of this branch. Check that:
  - the old `pointers.json` and manifests still load;
  - boot restores the confirmed generation;
  - an apply and confirm work;
  - a factory reset ends up on `last-lan/`.
- [ ] Run the same on a reset tree, which carries the baseline manifest with `reset: true`.

## Out of scope

- The engine-web peer file that only Debian's postinst wrote: slice 4 writes it on the image.
- An update fence for reset: slice 5 (call 6).
- New behaviour of any kind.
