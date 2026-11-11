# Interactive takeover assistant

## Status and intent

Requested in conversation: a takeover should not be fully automatic. Like a firewall's console setup (pfSense's "assign interfaces"), Andaru detects what the host does today, shows it to the operator pre-filled, lets them change it, shows exactly what will happen, and only then saves and applies. It is a front end, not a gate: quitting it changes nothing, nothing waits for it, and `andaru ctl apply network --takeover` keeps working. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

Owner decisions: build it first on the netplan baseline that already works, then add the ifupdown baseline as the next slice ([its draft](https://github.com/andaru-project/andaru/blob/wip/research/ifupdown-takeover/docs/superpowers/specs/2026-11-11-ifupdown-takeover-design.md) keeps its probe findings); use Charm's `huh` forms library for the terminal UI.

### Authorities

- [Network takeover and confirmed apply](2026-11-11-network-takeover-apply-design.md): apply, rollback timer, confirmation and boot are unchanged; the assistant only drives them.
- [Static DNS servers for the host](2026-11-11-static-host-dns-design.md): the host DNS the assistant fills in.
- [Issue #14](https://github.com/andaru-project/andaru/issues/14): first-boot setup and lockout prevention; this is its console step for the network.
- `docs/research/web-ui-foundation.md`: the engine API is the single source of truth and every UI is a client. A web UI can reuse the same endpoints later.

## When it runs

Only for a first takeover. The engine reports one of:

| `state` | Meaning | Assistant |
| --- | --- | --- |
| `ready` | netplan host, saved topology empty, nothing applied | runs |
| `taken-over` | a takeover is confirmed or pending | stops: "already taken over; use `andaru ctl apply network`" |
| `saved-not-applied` | the saved topology is not empty and nothing is applied | stops: lists what is saved and points to `apply network --takeover` or deleting it with `config` |
| `unsupported` | ifupdown, or anything the gates refuse outright | stops with the findings |

Refusing a non-empty saved topology is deliberate: replacing it would delete hand-made objects (and orphan their secrets) with no restore command.

## Flow

```
andaru ctl takeover
  1 detect   GET  /api/v1/takeover/proposal                state, change batch, findings
  2 ask      per-adapter forms, pre-filled                  nothing sent
  3 review   POST /api/v1/preview/network/draft  {batch}    validates and renders, saves nothing
  4 commit   POST /api/v1/config/changes  {batch, expectedSavedVersion}
             POST /api/v1/applies         {savedVersion, takeover: true}
  5 tell     deadline, and the confirm command for a new session
```

## Design

### 1. Proposal

`GET /api/v1/takeover/proposal` returns `{"state", "savedVersion", "operations", "adapters", "findings"}`.

- `operations` is a create-only change batch, exactly what step 4 will send: one Ethernet link, one segment and the zones, with `localId`s `link1`, `seg1`, `wan`, `lan` and `@` references between them. A takeover starts from an empty topology, so there is nothing to delete.
- `adapters` is display data per detected physical interface: kernel name, permanent MAC, and what it does now ("DHCP, 10.0.1.19/16, default route").
- Detection reads netplan's generated files in `/run/systemd/network` (`.network`, `.link`, `.netdev`), `ip -d -j address show`, `ip -j route show default` and `networkctl list`. All of them the engine already runs or reads.

Mapping, per physical interface bound to a netplan `.network` file:

| From | To |
| --- | --- |
| `DHCP=ipv4/yes` | IPv4 `dhcp` |
| `Address=` (IPv4) without DHCP for IPv4 | IPv4 `static` with those addresses; `Gateway=` or the default `[Route]` gives the gateway |
| `DHCP=ipv6/yes` | IPv6 `dhcp6` |
| `IPv6AcceptRA=yes`, or absent while the interface has a global SLAAC address | IPv6 `slaac` |
| otherwise | IPv6 `none` |
| `DNS=`, `Domains=` | `dns` on that segment |
| the interface with the IPv4 default route | zone `wan`; every other zone `lan` |

Anything the declaration cannot express is an **error finding naming the file and key**, never silently dropped: a `[Match]` that binds several interfaces, a current MAC that differs from the permanent one, `Address=` together with DHCP for the same family, routes other than the default one, routing policy rules, `[DHCP*]` overrides other than the ones netplan emits by default, more than one interface with a default route, `DNS=` on more than one file, and every `.netdev`. MTU comes from the `.link` or `.network` file and is kept as detected.

### 2. Ask

`andaru ctl takeover` on a terminal uses `huh` forms. Screens, each pre-filled from the proposal:

1. **Adapters:** one row per detected adapter with what it does now. An adapter without an address may be left out; one with an address cannot, since the apply would refuse it (`WOULD_UNCONFIGURE`).
2. **Per adapter:** zone (`wan` or `lan`); IPv4 mode (DHCP, static, none) and, when static, addresses and gateway; IPv6 mode (none, SLAAC, DHCPv6). Static IPv6 addresses, MTU and other zone names are kept as detected and not editable in this slice.
3. **Host DNS:** "use what DHCP offers" or servers and search domains, pre-filled from the netplan files (so a DHCP host is pre-filled with "use what DHCP offers").
4. **Confirm timeout:** pre-filled with 120 seconds.

Every string the host provides (interface names, domains, file paths) is shown with control characters escaped.

The command needs a terminal. Without one it exits 2, changing nothing, and points to `--accept-detected`. There is no line-prompt mode: `huh`'s accessible mode reads past its prompt on a pipe and accepts defaults on end of input, so it is not used. If the forms prove unusable on a serial console in the lab, a fallback is added then, with that failure as its reason.

### 3. Review (draft, saves nothing)

`POST /api/v1/preview/network/draft` takes the change batch the operator built. The engine runs the save's own preparation against the saved head without publishing, renders the result, and runs the takeover gates, returning the preview shape plus the gate findings. The gate inputs are assembled by the same function the apply uses, extracted from the apply handler. A batch with a `password` field is refused: the draft never carries a secret.

The review screen shows, per adapter, what changes ("DHCP → static 192.0.2.10/24"), the exact files and every finding. Actions: **Back**, **Quit**, and **Apply** only when no finding is an error. Apply is never the preselected action.

### 4. Commit

1. Before writing, the assistant checks that no apply is pending, runs the draft once more, and prints: "If this session drops, reconnect and run `andaru ctl applies list`."
2. Save: `POST /api/v1/config/changes` with the batch and the new **`expectedSavedVersion`**: the save is refused with `412` if the head moved since step 1. That field is added to change batches in this slice (one comparison before the batch is prepared); it guards the whole batch, where `expectedVersion` guards one object.
3. Apply: `POST /api/v1/applies` with `takeover: true`, the new saved version and the timeout.

From step 2 on the process ignores SIGHUP and SIGINT, so a dropping SSH session does not stop it between save and apply; the apply itself already survives a dropped connection. If the apply is refused or fails after the save, the assistant prints the saved version, that it is not live, and the command to retry (`andaru ctl apply network --saved-version N --takeover`); a later `andaru ctl takeover` reports `saved-not-applied` with the same advice.

### 5. Tell

As `apply network` does today: the deadline and "from a NEW session, run `andaru ctl applies confirm ID`". The assistant never confirms by itself.

### Non-interactive mode

`andaru ctl takeover --accept-detected [--confirm-timeout S]` runs steps 1, 3 and 4 with the proposal as is and prints the apply like `apply network` does. It refuses, changing nothing, when the state is not `ready` or any finding is an error.

### Engine and CLI plumbing

- Routes `/api/v1/takeover/proposal` and `/api/v1/preview/network/draft` are added to the root-only dispatcher and the OpenAPI document; `docs/dev/core-api.md` gains the assistant and the new finding codes.
- The proposal may take longer than a single read; the CLI gives it 15 seconds. `takeover` is the first multi-request CLI command, so it gets its own small client loop rather than the one-request `runCtl`.
- `huh` (MIT) and its Bubble Tea dependencies are linked into the single `andaru` binary, so the engine binary grows too; only `ctl takeover` executes them.

## Acceptance and evidence

Unit tests, in CI on both architectures:

1. **Detection:** fixtures captured from the lab (DHCP uplink; static address, gateway, two DNS servers and a search domain; DHCPv4 with a live SLAAC address) produce the exact batch. One error finding each for: a glob `[Match]`, a changed MAC, DHCP plus static on one family, an extra route, two default routes, DNS on two files, a `.netdev`, an ifupdown host.
2. **States:** `ready`, `taken-over` (pending and confirmed), `saved-not-applied`, `unsupported`.
3. **Head guard:** a batch with a stale `expectedSavedVersion` is `412` and changes nothing; existing batches without it behave as before.
4. **Draft:** a valid batch renders and the store's head and files are unchanged; a field error carries its path; gate findings appear; a `password` is refused; non-root is refused.
5. **Answers to batch:** a plain function; keeping and leaving out adapters, mode changes, DNS choices.
6. **Forms end to end in a pseudo-terminal** against a fake engine: the answers complete the flow and send exactly the batch; Back and Quit from every screen, and closing the terminal at any point before Apply, send no write.
8. **After save:** apply refused, apply failed, and the process dying between save and apply each leave the reported state and printed advice the spec gives.

On the lab, from snapshots, reset afterwards:

9. Static netplan base (`base-netplan-static-20261111`): `andaru ctl takeover` on the VM's serial console and over `ssh -t`; the address, gateway and DNS are pre-filled; accepting them takes the host over with no ping lost; confirmed from a new session; a reboot keeps it.
10. Quit at review: no saved version, no generation, host unchanged.
11. A second save from another session during review: the commit is refused with `412` and nothing is applied.
12. `--accept-detected` on a DHCP netplan host (a clone of the DHCP base, reset afterwards): taken over without questions.

The owner then runs the forms interactively himself; what he wants changed becomes the next change, not a criterion of this one.

## Limits

- **netplan hosts only** in this slice; an ifupdown host is reported `unsupported` with a "later release" finding.
- **First takeover only**, from an empty saved topology.
- **Only what the declaration can express**: physical Ethernet adapters, one uplink with a default route, one DNS segment; anything else is an error finding to fix by hand first.
- **Edits limited** to zone, address modes, IPv4 addresses and gateway, host DNS and the timeout.
- **The `huh` libraries ship in the engine binary.**
