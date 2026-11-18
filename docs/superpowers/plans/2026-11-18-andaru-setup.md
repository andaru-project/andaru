# First setup: `andaru setup`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `sudo andaru setup` takes a fresh install to a confirmed takeover, a first administrator, and the web UI reachable over HTTPS on the management segment at port 8443, across reboots. It resumes after an interruption and adds only what an existing install lacks.

**Architecture:**
- **Engine-owned workflow.** The engine owns a journal at `/var/lib/andaru/setup/state.json` and root-only routes on its local socket. `andaru setup` is a client of those routes.
- **Nothing persists before the handover.** Until then only the journal changes. It holds the network step's change batch and the certificate choice, and never a secret.
- **The handover**, in one engine request that survives the client's session:
  1. creates the administrator through the existing `bootstrap-admin` path;
  2. saves the batch with `management.web` set;
  3. writes the web configuration;
  4. applies with the rollback timer, marking the generation as setup's;
  5. enables the web service.
- **Restart.** A transient unit restarts the engine with its web arguments. If that engine fails, the web arguments come off.
- **Completion.** Confirming the marked generation, through the existing confirm path, completes setup.
- **Rollback.** A rollback of that generation, by the timer, a command or a reboot, removes the web configuration.

**Tech Stack:** Go standard library (`crypto/ecdsa`, `crypto/x509`, `net`), the existing engine, config, identity, audit, generation and render packages, `huh` for forms, systemd. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-18-andaru-setup-design.md`, approved by the owner on 2026-11-18 with its author calls and risks.

**Depends on:** #67, which makes purge delete `/etc/andaru`. This plan merges after it.

## Builder calls

The spec leaves these to the builder. They need the owner's approval with this plan.

1. **A DHCP-addressed management segment gets no web service in this slice.** The web service needs an address to listen on, and a lease can change. Setup still takes over and creates the administrator. It then says the web UI needs a static management address, and its review shows that. The spec's "host name only" certificate case is therefore not built yet.
2. **Several listeners, bound before the address exists.** `andaru-web --listen` becomes repeatable, one listener per static management address. It binds with `IP_FREEBIND`/`IPV6_FREEBIND`, so a boot that starts it before networkd assigns the address, or while an IPv6 address is still tentative, does not fail. The unit passes one word-split `$ANDARU_WEB_ARGS`.
3. **The operator's certificate travels in the request.** The CLI reads the files and sends their PEM over the root-only socket. The engine never reads paths the operator names.
4. **The generation carries the marker, not the journal.** The handover writes `setup.json` into the new generation's directory before the timer is armed, as the apply path writes `lan-closure.json`. Realize's `Rollback` and `Boot`'s pending-generation branch both read that marker to remove the web configuration. Realize never reads the engine's journal.
5. **The engine's sandbox widens for the web mode.** The unit gains:
   - `ReadWritePaths=-/etc/andaru -/run/andaru-web`;
   - `SupplementaryGroups=andaru-web`, so the engine can give files and its web socket that group without `CAP_CHOWN`.

   Setup also writes some files outside the engine. tmpfiles.d creates `/run/andaru-web` (0710 root:andaru-web), and postinst creates `/etc/andaru/tls` (2750 root:andaru-web).
6. **The restart cannot leave the host without its root socket.** Before writing `engine-web.conf`, the engine checks its web arguments in-process the way `andaru engine` parses them. The restart is a uniquely named transient unit started after the handover's response. The engine unit gains `OnFailure=andaru-engine-web-off.service`, a one-shot that does three things when the restarted engine fails to start: moves `engine-web.conf` aside, runs `reset-failed` on the engine, and starts it again without the web.
7. **Completion is a hook in confirm.** When `Confirm` succeeds for a generation carrying the setup marker, the engine writes `setup.completed` under the deterministic operation ID `setup-<generation>` through the audit intent table, then removes the journal. A crash between the two cannot write a second event. The spec's "the web service answers" becomes "`andaru-web.service` is active and connected to the engine's web socket": the engine's sandbox cannot make an outbound connection to check more.

## Global Constraints

- Root only. The setup routes accept only the root actor on the local socket. The web API refuses `/api/v1/setup` as it refuses `bootstrap`.
- No password in the journal, an argument, the environment, a log line or any request body but the administrator's, which goes to the existing `bootstrap-admin` route.
- Quitting before the handover changes nothing outside the journal. The network step journals its change batch; it does not save it.
- The network step is the takeover assistant's, unchanged except for an allowed-sources question. The answers become `objects.addresses` creates plus the `management` replace. `andaru ctl takeover` keeps working on its own.
- Setup on a taken-over host applies with `takeover: false`, and its network step is replaced by one `management` replace that sets `web: true` at its current version.
- The web service listens only on the management segment's static addresses, at 8443. Never a wildcard address and never a link-local or zoned one. Origins use the canonical form, with brackets for IPv6, plus the host's FQDN and its short name.
- The filter opens 8443 only on the management segment, restricted to its allowed sources when it has any, and only while `management.web` is set.
- File modes: `web.conf` and `engine-web.conf` 0600 root, `tls/web.crt` 0644, `tls/web.key` 0640 root:andaru-web, with an explicit `fchmod` after each create, because the engine's `UMask=0077` strips group bits.
- The handover runs under `context.WithoutCancel`, as the apply route does, and under one mutex. Every step compares the journal's phase before it changes it.
- A rollback of setup's generation removes the web configuration, disables and stops the web service, and keeps the administrator and the certificate.
- Remove deletes nothing setup wrote; purge deletes `/etc/andaru` (#67).

## Review Focus

1. **The engine with its web arguments starts under its real unit.** A failed start falls back to no web without losing the root socket. Task 4 tests it under `systemd-run` with the unit's sandbox properties; the lab checks it on real hosts.
2. **A reboot or a command that rolls back setup's generation leaves no web configuration.** Task 5 pins both realize paths.
3. **8443 only on the management segment, and only with `management.web`.** Task 1.
4. **Setup on a taken-over host works.** Task 6 drives it against the real engine routes.
5. **No password in the journal or the logs, and exactly one `setup.completed`.** Task 6.

## Structure

| Area | Files | Responsibility |
| --- | --- | --- |
| Declaration and filter | `internal/config/schema.json`, `internal/config/schema.go`, `internal/config/model.go`, `internal/render/model.go`, `internal/render/nft.go` | `management.web`; the 8443 rule |
| Certificates | `internal/webtls/webtls.go` | Generate, check an operator pair, fingerprint |
| Web service | `cmd/andaru-web/main.go`, `internal/web/server.go`, `packaging/systemd/andaru-web.service` | Several listeners with free bind; one argument variable |
| Engine sandbox and fallback | `packaging/systemd/andaru-engine.service`, `packaging/systemd/andaru-engine-web-off.service`, `packaging/tmpfiles.d/andaru.conf`, `debian/andaru.postinst`, `debian/rules` | Web-mode writes; fall back without the web |
| Web configuration | `internal/engine/webconf_linux.go` | Write, check and remove `/etc/andaru` files |
| Setup workflow | `internal/engine/setup_api.go`, `internal/engine/setup_journal_linux.go`, `internal/engine/security.go`, `internal/engine/openapi.json`, `internal/engine/web_api.go`, `internal/engine/applies_api.go` | Journal, routes, handover, the confirm hook |
| Rollback cleanup | `internal/generation/realize.go`, `internal/generation/takeover.go` | Remove the web configuration for a marked generation |
| CLI | `cmd/andaru/setup.go`, `cmd/andaru/main.go`, `cmd/andaru/takeover_forms.go`, `cmd/andaru/takeover_answers.go` | Prompts, the assistant's forms, allowed sources, progress |
| Docs | `docs/dev/core-api.md`, `debian/changelog` | Operator text, lab evidence |

## Task 1: `management.web` and the 8443 rule

**Files:** `internal/config/schema.json` (`managementFields.web`, boolean), `internal/config/schema.go` (fields list), `internal/config/model.go`, `internal/render/model.go` (`Web`), `internal/render/nft.go`, tests beside them.

- [ ] **Failing tests:**
  - With `web` true, the input chain has `iifname "<mgmt>" tcp dport 8443 accept` beside port 22, or the same rule on the `mgmt_4`/`mgmt_6` sets when allowed sources exist.
  - Without `web`, no 8443 anywhere.
  - No 8443 rule names another interface.
  - `web: "yes"` fails validation.
- [ ] **Regression guard:** a declaration without `web` validates and renders exactly as before, byte for byte against a golden file.
- [ ] **Implement, run, commit:** `feat: open the web port on the management segment when the web service is configured`

## Task 2: Certificates

**Files:** `internal/webtls/webtls.go` and its test.

- `Generate(fqdn, short string, addresses []netip.Addr, now time.Time) (certPEM, keyPEM []byte, err error)`: ECDSA P-256, valid ten years.
- `Check(certPEM, keyPEM []byte, names []string, addresses []netip.Addr, now time.Time) error`.
- `Fingerprint(certPEM []byte) (string, error)`: SHA-256 of the DER, upper-case colon-separated hex.

- [ ] **Failing tests:**
  - A generated pair passes `Check`, and names every address, the FQDN and the short name.
  - `Check` refuses a mismatched key, an expired certificate, and one that names nothing of the host.
  - `Fingerprint` of a fixed test certificate equals a pinned string.
- [ ] **Implement with the standard library only, run, commit:** `feat: generate and check the web service certificate`

## Task 3: Several listeners with free bind

**Files:** `cmd/andaru-web/main.go`, `internal/web/server.go`, `packaging/systemd/andaru-web.service`, tests, and `tests/shell/web_packaging_spec.sh`.

- [ ] **Failing tests:**
  - `--listen` given twice serves both addresses.
  - A listen address not assigned to any interface binds, through free bind, and serves once the address appears. The test adds the address to a dummy interface in a private network namespace, and skips without root.
  - The unit's `ExecStart` uses `$ANDARU_WEB_ARGS`.
- [ ] **Regression guard:** a wildcard address is still refused.
- [ ] **Implement:** listen through `net.ListenConfig` with a `Control` that sets the free-bind option for the address family.
- [ ] **Commit:** `feat: let the web service listen on every management address, before the address exists`

## Task 4: Engine sandbox, web configuration and the fallback

**Files:** `packaging/systemd/andaru-engine.service`, a new `packaging/systemd/andaru-engine-web-off.service`, `packaging/tmpfiles.d/andaru.conf`, `debian/andaru.postinst`, `debian/rules`, a new `internal/engine/webconf_linux.go`, and tests.

**Interfaces:**
- `writeWebConfig(dir string, c webConfig) error`: atomic writes with the modes in the constraints. It checks the engine's web arguments before it writes `engine-web.conf`.
- `removeWebConfig(dir string) error`: moves both `.conf` files aside and keeps the certificate and key.

- [ ] **Failing tests:**
  - The files' contents, modes and group, created by an unprivileged test user given the `andaru-web` group in a user namespace.
  - Symlinks are refused.
  - Arguments `andaru engine` would refuse are refused before anything is written.
  - In a root integration test, run under `systemd-run` with the engine unit's sandbox properties:
    - the engine with web arguments starts, creates its web socket with the right group, and serves the root socket;
    - with web arguments that fail, the `OnFailure` unit removes `engine-web.conf` and the engine comes back without the web.
  - Shellspec: the unit lines, the tmpfiles line and the postinst directory.
- [ ] **Implement, run, commit:** `feat: let the engine run its web mode under its sandbox, and fall back without it`

## Task 5: Rollback cleanup in realize

**Files:** `internal/generation/realize.go` (`Rollback`), `internal/generation/takeover.go` (`Boot`'s pending branch), and tests.

- [ ] **Failing tests:**
  - **Timer rollback:** rolling back a generation whose directory holds `setup.json` runs `systemctl disable --now andaru-web.service` and moves both `.conf` files aside.
  - **Reboot rollback:** `Boot` finding that generation pending does the same.
  - **Unmarked generations:** a rollback of an unmarked generation does neither.
  - **A second rollback** is a no-op.
- [ ] **Implement, run, commit:** `feat: remove the web configuration when setup's takeover rolls back`

## Task 6: The setup workflow and `andaru setup`

**Files:**
- engine: `internal/engine/setup_journal_linux.go`, `internal/engine/setup_api.go`, `internal/engine/security.go` (capabilities), `internal/engine/openapi.json`, `internal/engine/web_api.go` (block `/api/v1/setup`), `internal/engine/applies_api.go` (the confirm hook);
- CLI: `cmd/andaru/setup.go`, `cmd/andaru/main.go`, `cmd/andaru/takeover_forms.go`, `cmd/andaru/takeover_answers.go`;
- tests beside each.

**Journal:**

```json
{"phase": "…", "batch": {}, "tls": {"generate": true}, "fingerprint": "…", "generation": 0, "rolledBack": false}
```

**Routes,** root only:
- `GET /api/v1/setup`: the phase, and what the host already has: a confirmed takeover, `identity revision != 0`, `management.web`, a static management address.
- `POST /api/v1/setup/network`: journals the batch; saves nothing.
- `POST /api/v1/setup/tls`: `{"generate": true}` or the operator's PEM pair. It checks the pair, journals the fingerprint, and keeps the pair in memory under a session token returned to the client.
- `POST /api/v1/setup/handover`: the token, under `WithoutCancel` and the mutex. It does, in order:
  1. saves the batch with `expectedSavedVersion` and `management.web`, or the single `management` replace on a taken-over host;
  2. writes the web configuration;
  3. creates the generation with the setup marker;
  4. arms the timer;
  5. activates with `takeover` set unless the host is taken over;
  6. enables the web service;
  7. journals the generation;
  8. answers;
  9. starts the restart unit.

  A journal whose generation was rolled back re-runs the handover from the saved head.

The administrator is created by the CLI through the existing `/api/v1/users/bootstrap-admin` route, at the start of the handover, only while `revision == 0`.

- [ ] **Failing tests, against the real engine routes on a temporary state tree:**
  - **Before the handover:**
    - each route refuses a non-root actor, and the web API refuses `/api/v1/setup`;
    - quitting after `network` or `tls` leaves the saved head and the identity store unchanged;
    - `tls` refuses a bad pair.
  - **The handover:**
    - two concurrent handovers: the second is refused;
    - a crash between each handover sub-step and the next resumes or rolls back to a state the next run handles;
    - a taken-over host gets one `management` replace and an apply with `takeover: false`.
  - **Completion:**
    - confirming the marked generation writes exactly one `setup.completed`, also after a crash between the audit write and the journal removal;
    - after a rollback, a second run re-applies without asking the network questions again.
  - **Secrets:** the journal and the engine's captured log never contain the password or the key.
  - **CLI, over PTY against the same engine:**
    - a fresh run asks the allowed sources, and its answers become address objects;
    - a DHCP management segment ends without the web service, with the message;
    - `--certificate`/`--key` send PEM, never paths.
  - **The review** warns when management is an uplink by the closure's definition, and names 8443 as well as 22.
- [ ] **Implement, run, commit:** `feat: add andaru setup`

## Task 7: Install hint, docs and lab qualification

**Files:** `debian/andaru.postinst`, `docs/dev/core-api.md`, `debian/changelog`, `tests/shell/maintainer_scripts_spec.sh`.

- [ ] **Hint:** on a first `configure` with no confirmed generation, postinst prints `andaru: next, run sudo andaru setup`; upgrades print nothing. Shellspec covers both.
- [ ] **Docs:** replace the stale "no web listener" text. Add "First setup": the command, what it asks, the URL and fingerprint, the confirmation, what a rollback undoes, and that DHCP management gets no web UI yet.
- [ ] **Lab:** on the Proxmox lab VMs 114 and 117, from clean snapshots, restored afterwards:
  - install prints the hint and changes no network;
  - setup over SSH ends confirmed, with `openssl s_client` on `https://<mgmt>:8443` showing the printed fingerprint, 8443 and 22 closed on WAN, and `setup.completed` in the audit log;
  - the web UI is back after a reboot;
  - an interrupted setup resumes at each step;
  - the timer rollback and a reboot before confirmation both leave no `web.conf` and nothing on 8443;
  - setup on an already taken-over host;
  - an operator certificate;
  - a forced bad `engine-web.conf` falls back without the web, with the root socket alive.
- [ ] **Record the evidence in `core-api.md` and commit:** `docs: document andaru setup and record its lab qualification`

## Out of scope

- A GUI setup client.
- Console re-election of the management segment, the degraded state and the recovery state (the next slice).
- The web service on a DHCP-addressed management segment.
- ACME, port 443, and certificate rotation.
