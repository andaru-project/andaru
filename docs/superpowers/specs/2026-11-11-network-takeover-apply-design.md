# Network takeover and confirmed apply

## Status and intent

Scope approved in conversation: take a cloud-image Debian host over from netplan and cloud-init without dropping the operator's session, and build the apply machinery every later area reuses: live generations, a rollback timer that outlives the engine, explicit confirmation, and boot from the confirmed generation only. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

This follows the network preview ([PR #27](https://github.com/andaru-project/andaru/pull/27)), which already renders the files and reports what blocks them. This slice activates them.

### Authorities

- [Issue #4](https://github.com/andaru-project/andaru/issues/4): networkd reads only `/run`; generations are rendered once and boot copies the confirmed one, doing no rendering; `systemd-run --on-active` is the rollback primitive; removing a `.netdev` does not remove the device, and networkd never updates an existing one.
- [Issue #6](https://github.com/andaru-project/andaru/issues/6): save and apply are separate; an apply names the saved version the operator saw; confirmed and last-known-good pointers; boot never loads unapplied or unconfirmed state.
- [Issue #14](https://github.com/andaru-project/andaru/issues/14): arm the rollback before any change that can disconnect; the first takeover rolls back to the pre-Andaru baseline; a competing writer must not remain; the cloud-image baseline is netplan plus cloud-init.
- [Network preview design](2026-11-10-network-render-preview-design.md): renderer, findings and file layout, reused unchanged except for the DNS decision below.

### Deviations from the authorities

- **#6 drafted generation rows in `config.db`.** Here the generation directory is the authority, because boot and rollback must not depend on opening a database.
- **#6 drafted idempotency keys.** Not in this slice; "one pending apply" and "saved version must be the head" already make a repeated request safe.
- **#4 asks that netplan be removed.** Here its YAML is moved away and the package stays; package removal is a packaging decision for #13.
- **#14's takeover installs a fail-closed firewall baseline before taking ownership and requires confirmation over the new management path.** Neither exists yet. This slice therefore refuses everything that forwards, and treats confirmation as the operator's assertion.

## Probe results (lab VM 114, Debian 13.7, systemd 257.13, 2026-11-10)

- **Swapping the `.network` file is hitless for IPv4.** Netplan's `10-netplan-eth0.network` was replaced by an Andaru-style file and `networkctl reload` run, then a 60-second `systemd-run` timer restored netplan's file. 420 pings at 5 per second across both swaps: none lost. networkd re-acquired the same DHCPv4 lease in about 60 ms and added the new routes before deleting the old ones.
- **IPv6 is interrupted for about 2 seconds** on each swap: networkd logged `DHCPv6 lease lost`, removed the RA routes and the DHCPv6 address, and restored them.
- **The timer is independent of what armed it:** it is owned by PID 1 and restored the baseline with the arming shell long gone.
- **A root process with the engine's restrictions can arm it.** From a transient unit with the engine's sandbox properties, `systemd-run` created and cancelled a timer and ran an unrestricted child that wrote `/run/systemd/network` and ran `networkctl reload`.
- **Netplan pins the name `eth0`** through a generated `.link` file. Without netplan the same card is `ens18` after a reboot. Andaru matches by permanent MAC, so only the displayed name changes.
- **DHCP-provided DNS reaches the host through systemd-resolved** and survived the swap because the probe file left `UseDNS` at its default.
- **`systemd-networkd` is enabled twice** on the cloud image: by a static symlink in `/etc/systemd/system/multi-user.target.wants`, and by netplan's generator in `/run/systemd/generator`. The generator also enables `systemd-networkd-wait-online`.
- The cloud image has no ifupdown (`/sbin/ifup` absent).

## Decisions and scope

| In this slice | Later slices |
| --- | --- |
| Live generations on disk with confirmed / last-known-good pointers | Firewall, DHCP, DNS and system areas; cross-area checks |
| `andaru ctl apply network`, with `--takeover` for the netplan baseline | ifupdown baseline (added by [the ifupdown takeover](2026-11-11-ifupdown-takeover-design.md)) |
| Rollback timer armed first; confirm; early rollback | Forwarding: RA, prefix delegation and PPPoE are refused until a fail-closed filter exists |
| Boot oneshot that copies the confirmed generation, with fallback | Management election and lockout analysis; setup wizard, administrator, TLS (#14) |
| Lab proof including reboots and an engine kill | Changing an existing virtual device; generation pruning; idempotency keys |

## Design

### One privileged actor, outside the engine

The engine renders and stores a generation in its own state directory, then asks PID 1 to run a short-lived command for every privileged step:

| Command | Started by | Does |
| --- | --- | --- |
| `andaru realize activate <n>` | engine, `systemd-run --wait` | writes generation `n` into `/run/systemd/network`, reloads, verifies |
| `andaru realize rollback <n>` | the rollback timer, or the engine for an early rollback | restores what was live before `n` |
| `andaru realize confirm <n>` | engine, `systemd-run --wait` | moves the pointers; on a takeover, makes it permanent |
| `andaru realize boot` | `andaru-network-boot.service` | copies the confirmed generation into `/run` |

Rules that hold for all four:

- **`realize` alone writes `pointers.json` and `state.json`, and only while holding an exclusive `flock` on `generations/lock`.** The engine never takes the lock; it writes a new generation directory and reads the rest. So `rollback` and `boot` work with the engine dead, and an engine crash cannot leave a lock or a pending pointer behind.
- **Every verb re-reads the state under the lock and does nothing unless the state is the one it expects.** A timer that fires after a confirmation is a no-op. A confirmation after the deadline is refused.
- **`realize` writes and deletes only `/run/systemd/network/50-andaru-*.network` and `.netdev`,** plus the takeover paths named below. A manifest naming any other path is refused, as is one whose files do not match their hashes.
- Every external command `realize` runs has a timeout, and the transient units carry `RuntimeMaxSec` as a backstop, so the lock is always released.

**The engine's sandbox is hardening, not a privilege boundary.** A root process that may ask PID 1 to start units is root-equivalent. Splitting the engine from a minimal privileged helper is future work; this design does not make it harder.

### Generations

Under `/var/lib/andaru/generations/`, root-only:

```
lock
pointers.json     {"confirmed": 3, "lastKnownGood": 2, "pending": null, "live": 3}
3/manifest.json   immutable, engine-written: id, savedVersion, createdAt, takeover, confirmTimeoutSeconds, files[{path, mode, sha256}]
3/state.json      realize-written: state, previous, deadline, reason
3/files/...       the rendered files
baseline/         the pre-Andaru files and their manifest, captured by a takeover
```

- Every write is a temporary file, `fsync`, rename, directory `fsync`.
- A generation is created per apply attempt, numbered from 1. Refused, failed and rolled-back attempts keep their number. A directory with no `state.json` was never started and is listed as `failed`.
- **`live` records what is in `/run` now**: a generation number or `"baseline"`. `previous` is the value of `live` when the generation was activated, so a rollback restores what was really running, not what was last confirmed.
- **No pruning yet.** Each generation is a few kilobytes.

### States

`activating` → `awaiting_confirmation` → `confirmed`, or → `rolled_back`, or → `failed`.

- `failed`: activation or verification did not succeed; the previous state was restored immediately.
- `rolled_back`: the deadline passed, the operator rolled back early, or the host rebooted before confirmation.
- Only one generation may be `activating` or `awaiting_confirmation`. `realize activate` enforces this under the lock; a second apply is `409 APPLY_IN_PROGRESS`.

### Apply

`POST /api/v1/applies` with `{"savedVersion": "7", "takeover": false, "confirmTimeoutSeconds": 120}`.

1. **Check the request.** `savedVersion` is required and must be the saved head, otherwise `412 PRECONDITION_FAILED`: the operator applies what they previewed. The timeout is 30–600 seconds, default 120.
2. **Render** with the preview renderer.
3. **Gate** (next section). A refusal is `422 APPLY_REFUSED` with the findings attached; nothing is stored.
4. **Store** the generation directory.
5. **Arm** `systemd-run --unit=andaru-rollback-<n> --on-active=<60 + timeout> --timer-property=AccuracySec=1s andaru realize rollback <n>`. If arming fails, nothing has changed.
6. **Activate** with `systemd-run --wait`. `realize activate` sets `pending` and state `activating`, swaps the files and verifies, all within an internal 45-second limit. On failure it restores the previous files itself and writes `failed` with a reason; the engine then stops the timer.
7. On success `realize activate` writes `awaiting_confirmation` with `deadline = now + timeout`. The engine responds `200` with the apply.

The timer is armed for longer than the deadline so it can never fire during activation. `confirm` enforces the deadline; the timer enforces the rollback at most 60 seconds after it.

The request is synchronous. If the client disconnects, the work continues and `GET /api/v1/applies/{id}` shows the outcome. If the engine dies after arming, the timer either finds nothing to do or rolls back.

### Gates

"Managed interface" below means a physical interface that networkd currently configures from a baseline or Andaru file, or that this generation would configure. Interfaces owned by anything else (containers, VPN clients) are ignored.

- **Any error finding** from the renderer, except `FOREIGN_CONFIGURATION` for a recognised baseline when `takeover` is true.
- **`APPLY_UNSUPPORTED`**, naming the field: an enabled PPPoE link, a segment with IPv6 `delegated`, or `ra.enabled`. These wait for the fail-closed filter (#4: no interface comes up unfiltered).
- **`WOULD_UNCONFIGURE`**: a managed interface that currently has a global address would not be configured by this generation. Addressing mode is not compared; verification and the timer cover that.
- **`NETDEV_CHANGE_UNSUPPORTED`**: a virtual device that exists in the live generation would get a different `.netdev`. networkd does not update an existing device, so the apply would report success over a stale one. Adding and removing devices is supported.
- **`TAKEOVER_MISMATCH`**: `takeover` is true but no baseline is in place, or false while one is.
- **`TAKEOVER_UNSUPPORTED_BASELINE`**: see Takeover.

### Activate and verify

1. On a takeover, capture the baseline (see Takeover).
2. Remove the files that `live` put in `/run/systemd/network`: the live generation's, or on a takeover the baseline's `10-netplan-*` files. Write this generation's files. Nothing else in the directory is touched.
3. `networkctl reload`.
4. `networkctl delete` each virtual device that the live generation created and this one does not.
5. Verify within 30 seconds, from `networkctl list --json`, `ip -j address show` and `ip -j route show default`:
   - every interface this generation renders is bound to its Andaru `.network` file;
   - every managed interface that had a global IPv4 address still has one;
   - if there was an IPv4 default route through a managed interface, there is one now.
6. Set `live` to this generation.

**Rollback** restores `previous` by the same file swap and device deletion, without verification, sets `live`, and writes `rolled_back`. If `previous` fails its hash check, it falls back to `lastKnownGood`, then to the baseline, and records which in `reason`. If nothing is restorable it leaves the files in place and writes `failed`; the host keeps running what it has.

### Confirm

`POST /api/v1/applies/{id}/confirmation`. `realize confirm`, under the lock:

1. refuses unless the state is `awaiting_confirmation` and the deadline has not passed (`409` with the current state);
2. sets `lastKnownGood` to the old `confirmed`, `confirmed` to this generation, clears `pending`, writes state `confirmed`;
3. on a takeover, makes it permanent (next section);
4. stops the rollback timer. A timer that fires anyway does nothing, by the state check.

**Confirm from a new connection.** A session that survived the change does not show that a new one can be opened. The CLI prints this instruction. The engine listens only on a Unix socket and cannot check it, so the confirmation is the operator's assertion.

`POST /api/v1/applies/{id}/rollback` rolls back before the deadline.

### Takeover

Recognised baseline in this slice: **netplan on networkd.** All of:

- `/usr/sbin/netplan` exists and `/sbin/ifup` does not;
- netplan YAML exists only in `/etc/netplan`, not in `/run/netplan`, `/lib/netplan` or `/usr/lib/netplan`;
- every foreign file is `/run/systemd/network/10-netplan-*`, with no drop-ins, and `/etc/systemd/network` is empty;
- a baseline `.network` file that sets `DNS=` or `Domains=` is accepted only when the saved configuration declares DNS servers for that interface ([static DNS servers for the host](2026-11-11-static-host-dns-design.md)); otherwise the host would be left without a resolver.

Anything else is `TAKEOVER_UNSUPPORTED_BASELINE`, naming what was found.

**Baseline capture.** `realize activate` copies the `10-netplan-*` files, never `50-andaru-*`, into `baseline/` with a hash manifest. While no generation has ever been confirmed, each takeover attempt replaces the capture atomically, so a netplan change between attempts is picked up. After the first confirmation the baseline is never rewritten.

**Until confirmation nothing outside `/run` and Andaru's own state is changed, so a reboot before confirmation returns the host to netplan untouched.** Making it permanent happens after the pointers move, in this order, and each step is safe to repeat:

1. `systemctl enable systemd-networkd.service`, because netplan's generator will stop enabling it;
2. write `/etc/cloud/cloud.cfg.d/99-andaru-network.cfg` containing `network: {config: disabled}`, so cloud-init does not write the YAML back;
3. move `/etc/netplan/*.yaml` into `baseline/netplan/`, so netplan's generator emits nothing.

A crash or reboot between any two steps is repaired by `realize boot`, which repeats all three whenever the baseline is a captured netplan one and a generation is confirmed.

`systemd-networkd-wait-online` is no longer enabled after takeover, and Andaru renders `RequiredForOnline=no`, so `network-online.target` does not wait for an uplink. Undoing a confirmed takeover is manual and documented: move the YAML back, delete the cloud-init file and the generations directory, reboot.

### Boot

`andaru-network-boot.service`, enabled by the package:

```
[Unit]
DefaultDependencies=no
RequiresMountsFor=/var/lib/andaru
ConditionPathExists=/var/lib/andaru/generations/pointers.json
Before=systemd-networkd.service network-pre.target shutdown.target
Conflicts=shutdown.target
[Service]
Type=oneshot
TimeoutStartSec=30
ExecStart=/usr/bin/andaru realize boot
[Install]
WantedBy=sysinit.target
```

It does nothing on a host that was never taken over, and `realize boot` exits 0 whatever happens, so it cannot hold up the boot.

1. A `pending` generation is marked `rolled_back` with reason `reboot`.
2. With no confirmed generation, stop: the host is still on its baseline.
3. Repeat the takeover's permanent steps, and remove any `10-netplan-*` file from `/run/systemd/network`: netplan's generator runs before this unit, and its files would win over Andaru's.
4. Check the confirmed generation against its manifest and copy it into `/run/systemd/network`; set `live`.
5. If the check fails, use `lastKnownGood`. If that fails too, use the captured baseline files. `live` then differs from `confirmed`, which the applies list reports as a fallback.
6. If nothing is usable, write nothing and log it: the host comes up with no managed interface and needs the local console, as #14 rules.

Boot does no rendering and opens no database.

### Host DNS

The preview renders `UseDNS=no` everywhere, because networkd must not become the authority for DNS. Applied as is, that would leave the host itself with no resolver. Until the DNS area exists, the renderer emits `UseDNS=yes` in `[DHCPv4]`, `[DHCPv6]` and `[IPv6AcceptRA]`; NTP, hostname and domains stay ignored. `shortcut: the host uses provider DNS for its own lookups; revisit when the DNS area owns the resolver.`

### API and CLI

| Resource | Result |
| --- | --- |
| `POST /api/v1/applies` | apply the saved head's network area |
| `GET /api/v1/applies` | the pointers and every apply, newest first |
| `GET /api/v1/applies/{id}` | one apply: state, deadline, reason, saved version, files (paths and modes only) |
| `POST /api/v1/applies/{id}/confirmation` | confirm |
| `POST /api/v1/applies/{id}/rollback` | roll back early |

Root Unix peer only, existing error envelope, described in the served OpenAPI document.

```
andaru ctl apply network --saved-version N [--takeover] [--confirm-timeout S]
andaru ctl applies list | get ID | confirm ID | rollback ID
```

`apply` prints the apply and, on stderr, the deadline and the exact confirm command to run from a new session. Exit 0 means the change is live and awaiting confirmation, not that it is confirmed.

## Acceptance and evidence

Unit tests, in CI on both architectures. The `realize` functions take the filesystem root and a command runner as parameters, so tests run them against a temporary tree with recorded commands; the CLI always passes `/`.

1. **Store:** atomic writes, numbering, manifest hash check, path allowlist, a torn `state.json` or `pointers.json` is refused rather than guessed.
2. **Gates:** one test per refusal with its finding and path; `savedVersion` not the head is `412`; non-root is denied before any read.
3. **State checks:** a second activate while one is pending; rollback after confirm does nothing; confirm after the deadline is refused; confirm after a rollback is refused.
4. **File swaps:** foreign files are left alone; takeover captures only netplan files and re-captures on a second attempt; rollback restores `previous` and falls back when it is corrupt.
5. **Confirm interrupted after each permanent step, then boot:** the takeover is completed and the confirmed generation is live.
6. **Boot:** pending marked rolled back; netplan leftovers removed; fallback to last-known-good, then baseline; exit 0 with nothing usable.
7. Renderer goldens updated for `UseDNS=yes`.

On the lab VM, behind a new snapshot, with a continuous ping and the Proxmox guest agent as the out-of-band path. The guest agent is used to observe; confirmations are sent over a new SSH connection. Each step records addresses, routes and `/run/systemd/network`:

8. **Takeover, not confirmed:** the timer restores netplan's files; no IPv4 ping lost.
9. **Takeover, reboot before the deadline:** the host returns on netplan, the generation reads `rolled_back`.
10. **Takeover, confirmed:** no IPv4 ping lost; the baseline matches the netplan files' hashes; `/etc/netplan` is empty; DNS resolves.
11. **Reboot after confirmation:** same address under the new interface name, bound to Andaru's file; no netplan file in `/run`; `systemd-networkd` is enabled and started after the boot unit; `systemd-analyze verify` is clean for the boot unit.
12. **`cloud-init clean`, then reboot:** `/etc/netplan` stays empty and the host returns as in 11.
13. **A second apply adding a VLAN with a static address, not confirmed:** the timer restores the previous generation and the VLAN device is gone. Repeated **with the engine killed** while awaiting confirmation.
14. **The same apply confirmed**, then a reboot: the VLAN is present.
15. **Corrupt the confirmed generation and reboot:** boot uses last-known-good and the applies list shows the fallback.
16. Refusals on the real host: an apply without `--takeover` before takeover, a PPPoE declaration, and a changed VLAN id.

The lab is left taken over and confirmed, as the baseline for the next slices.

## Limits the owner accepts with this slice

- **IPv6 sessions are interrupted for about 2 seconds** by any apply that touches an interface's file. IPv4 was hitless in the probe; that is one DHCP topology on virtio, not a guarantee.
- **Confirmation over a new connection is the operator's assertion.** Verification checks IPv4 addresses and the IPv4 default route only; a change that keeps both but breaks reachability is caught by the timer, not by the engine.
- **Only the netplan-on-networkd baseline.** ifupdown hosts were refused here; [the ifupdown takeover](2026-11-11-ifupdown-takeover-design.md) adds them. netplan hosts with static DNS are refused unless the saved configuration declares DNS servers.
- **No forwarding, no PPPoE, no change to an existing VLAN, bridge or bond** can be applied yet, so this cannot bring up your WAN.
- **Behaviour changes from netplan:** NTP servers and hostname from DHCP are ignored, `network-online.target` no longer waits for an uplink, and the interface name changes at the first reboot on cloud images.
- **Not tested:** a separate `/var` filesystem (the unit declares the mount dependency), and any baseline other than the lab's.
- **Generations are never pruned** in this slice.
