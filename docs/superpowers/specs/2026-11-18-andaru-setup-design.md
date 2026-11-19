# First setup: `andaru setup`

Partly superseded on 2026-11-18 by [Andaru as an appliance image](2026-11-18-appliance-image-design.md): the takeover step, the separate interactive takeover assistant, the package-removal decision, the OpenSSH rule, the postinst install hint (author call 10) and the `apt install` qualification no longer apply. On the image, setup is offered on the console at first boot; the setup slice designs it.

Status: approved by the owner on 2026-11-18, author calls and risks included. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

## Intent

A fresh Debian host with the `andaru` package installed has no way into the web UI. The engine runs, but the web service is installed disabled, with no certificate, no listener configuration and no filter rule for HTTPS. `andaru setup` is the one documented path from `apt install andaru` to a reachable, authenticated web UI on the management segment. It takes the host's network over, elects management, creates the first administrator, gives the web service a certificate and a listener, and hands over with the existing rollback.

## Owner decisions

From the 2026-11-18 conversation:

- **Console first, engine workflow.** The workflow lives in the engine and is resumable. `andaru setup` is its first and only client in this slice. A GUI setup becomes a second client later, once there is a way to reach it before setup.
- **TLS.** Setup generates a self-signed certificate and prints its SHA-256 fingerprint. It accepts an operator-supplied certificate and key instead when given their paths.
- **Listener.** The web UI listens only on the management segment's addresses, on port 8443. Port 443 stays free for a later reverse-proxy add-on.
- **Package removal follows Debian.** Removing the package deletes nothing setup wrote. Purging it deletes `/etc/andaru`, with the certificate, the key and both configuration files, as it deletes Andaru's state trees.
- **Recovery is the next slice.** Console re-election of the management segment and a recovery state for a missing management path are designed next. That design resolves the conflict between the 2026-11-12 "listen on every interface" ruling and the later rule that WAN and unclassified interfaces never listen.

## Still binding

From issue #14, the [identity and audit core](2026-11-15-identity-audit-core-design.md), the [2026-11-15 factory reset design](2026-11-15-factory-reset-design.md) and the [2026-11-17 wipeable-state design](2026-11-17-factory-reset-data-design.md), where they do not conflict with this document:

- One setup workflow, two possible entry points (offered after install, or run later). Package installation stays non-interactive and never changes networking.
- Nothing is reachable without authentication. The web service is HTTPS only, with no plaintext listener and no redirect. Host and Origin are checked against the configured origins.
- No default credentials outside factory reset. Setup creates the first administrator, with a password the operator types, and only while no account exists.
- Management is elected explicitly and recorded in the declaration. The SSH session's segment is a hint, never an automatic election. Setup warns before it puts management on an uplink.
- SSH is allowed on the management segment by default and never on WAN unless the administrator opts in. Andaru never installs, enables or changes OpenSSH; `openssh-server` stays a Recommends.
- The interactive takeover assistant is not `andaru setup`: it remains a separate front end for the network step and keeps working on its own.
- A connectivity-changing handover is armed with the rollback timer before anything can disconnect the operator, and is confirmed from a new session. A failed first takeover returns to the pre-Andaru baseline.
- `first/` is written once, when the first setup's LAN comes up, and never when management is an uplink.

## Author calls

These are the author's calls, for the owner's approval.

1. **Setup reuses the takeover assistant's network step.** The network questions, the proposal, the review and the takeover apply are the existing assistant's, called as a step of the workflow. Setup adds one question there: the management segment's allowed sources. The default is the whole segment, as today.
2. **A host already taken over skips the network step.** Each step runs only when its result is missing: the takeover when no generation is confirmed, the administrator when no account exists, and TLS and the listener when the web service is not configured. Setup on an existing install therefore only adds what that install lacks.
3. **The workflow state lives in the wipeable tree.** It is a journal at `/var/lib/andaru/setup/state.json`, with the phase and the choices made so far, and never a password. A factory reset wipes it, so setup runs again afterwards, as the 2026-11-17 design expects when neither kept copy comes up.
4. **The engine writes the web configuration.** The engine unit gains `/etc/andaru` as a writable path. The engine writes `/etc/andaru/tls/web.crt` and `web.key` (0640, group `andaru-web`), `web.conf` and `engine-web.conf`. It then starts `andaru-web.service` and restarts itself with its web arguments through transient systemd units, as it already runs realize jobs. The CLI never writes those files, so a GUI client can later drive the same step.
5. **Certificate.** ECDSA P-256, valid for ten years. Its subject alternative names are the management segment's static addresses and the host name. A DHCP-addressed management segment gets the host name only, and setup says so. An operator-supplied pair must parse, match, and cover at least one management address or the host name.
6. **Origins.** One origin per management address and one for the host name, all `https://…:8443`. `andaru-web.service` changes from a single `--origin` to the list setup writes.
7. **The filter allows 8443 like 22.** The renderer adds `tcp dport 8443 accept` on the management segment, restricted to its allowed sources when it has any, whenever the web service is configured. A declaration flag `management.web` records that. A host without the web service gets no 8443 rule.
8. **Confirmation.** After the takeover apply, setup starts the web service and prints the URL and the fingerprint. The operator confirms from a new SSH session with `andaru ctl applies confirm N`, or from the web UI by logging in as the new administrator and confirming there. Either proves that the new management path works.
9. **Completion.** Once the generation is confirmed and the web service answers, setup writes `setup.completed` to the audit log, removes its journal, and prints the URL and the fingerprint again.
10. **Install hint.** The package's postinst prints, once, that the next step is `sudo andaru setup`. It asks nothing and changes no network.

## Workflow

```text
andaru setup
  1. inspect      read the host as the takeover assistant does; refuse what it refuses
  2. network      the assistant's questions, plus the management segment's allowed sources
  3. admin        login, display name, hidden password twice      (skipped when an account exists)
  4. tls          generate, or take an operator pair; show the fingerprint
  5. review       the network review, the administrator's login, the URL and the fingerprint
  6. handover     save; arm the rollback; apply with --takeover; write the web configuration; start the web service
  7. confirm      wait for confirmation from a new SSH session or from the web UI
  8. complete     audit setup.completed; drop the journal; print the URL and the fingerprint
```

The engine exposes the workflow as root-only routes on its local socket: status, then each step's answers, then the handover. The CLI is a client of those routes. Each completed step is journaled before the next one starts. An interrupted setup resumes from its journal and asks only the questions it has no answers for. Quitting before the handover changes nothing outside the journal.

A rollback, from the timer or from a reboot before confirmation, returns the network to the pre-Andaru baseline, as the takeover does today. It also stops the web service and removes the web configuration, so the host is back where it was before setup. The administrator account and the certificate stay, so a second attempt does not ask for them again.

## Non-goals

- A GUI setup client, and any listener before setup.
- Console re-election of the management segment, the degraded state, and the recovery state for a missing management path (the next slice).
- ACME, a reverse proxy on 443, or certificate rotation.
- debconf questions or an interactive postinst.
- Changing the network step's own questions beyond the allowed sources.

## Risks

- **DHCP management.** A management segment addressed by DHCP has no stable address for the certificate or the origins. Setup warns and uses the host name, so the operator needs a resolvable name for the browser to accept the origin.
- **The engine writing `/etc/andaru`.** The unit's sandbox widens to that directory. It holds no secret today besides what setup writes, but it widens what a compromised engine could change.
- **The engine restarting itself.** It has to come back with its web arguments. The plan must show that a failed restart leaves the previous engine running, or the host reachable over SSH.

## Qualification

On the Proxmox lab VMs, netplan and ifupdown, each from a clean install:

- a fresh `apt install andaru` prints the hint and changes no network;
- `andaru setup` over SSH, ending with a confirmed generation, the web UI reachable on the management address at 8443 with the printed fingerprint, 8443 and 22 closed on WAN, and `setup.completed` in the audit log;
- an interrupted setup resumed at each step;
- the timer rollback, with the network back on its baseline and the web service stopped;
- setup on an already taken-over host, which skips the network step;
- an operator-supplied certificate.
