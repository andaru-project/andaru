# Taking over a default Debian install (ifupdown)

## Status and intent

Requested in conversation: Andaru must take over a host installed with the default Debian installer, which configures the network with ifupdown and no netplan. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

This extends [network takeover and confirmed apply](2026-11-11-network-takeover-apply-design.md) (#28), [static DNS servers for the host](2026-11-11-static-host-dns-design.md) (#29) and [the interactive takeover assistant](2026-11-11-interactive-takeover-design.md) (#30). Generations, the rollback timer, confirmation, boot and the assistant's flow stay as they are; this slice adds a second recognised baseline and teaches the assistant to read it.

### Authorities

- [Issue #14](https://github.com/andaru-project/andaru/issues/14): the plain Debian installer is the second baseline; a competing writer must not remain; the first takeover rolls back to the pre-Andaru baseline.
- [Issue #4](https://github.com/andaru-project/andaru/issues/4): networkd is the realizer.
- The takeover and static DNS specs for everything not restated here.

### Owner decisions in conversation

- After a takeover systemd-resolved is the host's resolver (option 1 of two).
- The DHCP identity probe was run before writing this spec.
- Order: the assistant ships first, on netplan; this slice then feeds it the ifupdown baseline, so `andaru ctl takeover` is the way in on both.

## What a default Debian 13 install looks like

From the installer's source (`netcfg` 1.197, `write_interface.c`) and a Debian 13.7 lab VM converted to match it:

```
source /etc/network/interfaces.d/*
auto lo
iface lo inet loopback
allow-hotplug ens18
iface ens18 inet dhcp
iface ens18 inet6 auto
```

- A static install writes `iface <nic> inet static` with `address <a>/<len>` and `gateway`, or the `inet6` equivalent; a DHCP stanza may carry `hostname`.
- The DHCP client is **dhcpcd** (`dhcpcd-base` is Priority: important in Debian 13). `ifup` starts one dhcpcd per interface; `/etc/dhcpcd.conf` sets `persistent`, `duid` and `slaac private`.
- `allow-hotplug` interfaces are raised by udev (`80-ifupdown.rules`) through `ifup@<nic>.service`; `auto` ones by `networking.service`.
- `/etc/resolv.conf` is a plain file written by dhcpcd, or once by the installer on a static install. systemd-resolved is not installed.

## Probe results (lab VM 117, Debian 13.7, systemd 257.13, dhcpcd 10.1.0, 2026-11-11)

VM 117 is a clone of the netplan lab VM with its own MAC, machine ID and SSH keys, converted to the layout above and snapshotted while stopped as `base-ifupdown-dhcp-20261111`.

- **dhcpcd can be stopped without dropping the address:** `dhcpcd -p -x ens18`.
- **networkd with its own DHCP identity got a different IPv4 address** (twice) and removed the old one, cutting an IPv4 session.
- **networkd presenting dhcpcd's identity kept both addresses:** `ClientIdentifier=duid`, `IAID=` the last four MAC bytes, `DUIDType=1`, `DUIDRawData=` from `/var/lib/dhcpcd/duid` (text, colon-separated hex, mode 0640 root). Same IPv4 lease and DHCPv6 address; 1 ping in 450 lost at the handoff. Only a DUID-LLT was seen.
- **SLAAC addresses change regardless:** dhcpcd generates stable private addresses; networkd uses EUI-64 by default.
- **Handing back must go through ifupdown's own units.** dhcpcd started from a transient unit was killed when that unit ended. `systemctl restart ifup@ens18.service` restored the original lease each time, with a 7.4-second IPv4 gap.
- **Installing `systemd-resolved` on such a host already puts it in charge:** the package points `/etc/resolv.conf` at the stub, enables and starts the unit, and provides `resolvconf`, so dhcpcd keeps feeding it DHCP-provided servers.

## Decisions and scope

| In this slice | Later |
| --- | --- |
| ifupdown + dhcpcd hosts in the installer's shapes: physical interfaces, `inet dhcp` / `inet static`, `inet6 auto` / `inet6 static` | `dhclient` (upgraded hosts), bridges, VLANs, bonds, wireless, `pointopoint`, scripts, `mapping` |
| networkd presents dhcpcd's DHCP identity, so leases survive | Keeping SLAAC addresses stable across the handoff |
| systemd-resolved must already be in charge (`apt install systemd-resolved` does that) | Andaru installing packages or rewriting `/etc/resolv.conf` |
| ifupdown is kept installed and masked after confirmation; `/etc/network/interfaces` is left untouched | Removing ifupdown |

Because installing systemd-resolved already moves the host to it, Andaru never edits `/etc/resolv.conf`, and the takeover spec's rule that nothing outside `/run` changes before confirmation holds here too.

## Design

### Recognising the baseline

On a host with `/sbin/ifup` and no netplan, `--takeover` is accepted when all of these hold; otherwise one finding per failure, with the offending file or name in `path`:

1. **Files:** `/etc/network/interfaces` and everything it sources parse. Accepted: `auto`, `allow-hotplug`, `source` and `source-directory` (relative to `/etc/network`, globs that match nothing are fine, at most 8 levels deep, regular files only, no loops), `iface lo inet loopback`, and for other interfaces `iface <nic> inet dhcp|static` and `iface <nic> inet6 auto|static`, with only the options `address`, `gateway` and, on `inet dhcp`, `hostname`. Line continuations are joined. Anything else is `TAKEOVER_UNSUPPORTED_BASELINE`.
2. **Interfaces:** every `<nic>` is a physical interface whose current MAC equals its permanent MAC.
3. **DHCP client:** for every `inet dhcp` stanza a dhcpcd process is running for that interface, and `/etc/dhcpcd.conf` has the `duid` directive and none of `clientid`, `iaid`, `anonymous` or `interface` blocks. Otherwise `DHCP_IDENTITY_UNKNOWN`, naming what was found.
4. **Resolver:** systemd-resolved is in charge of `/etc/resolv.conf` (the static DNS spec's definition). Otherwise `RESOLVED_NOT_IN_CHARGE`, remedy `apt install systemd-resolved`.
5. **No competing writer:** no foreign networkd file, `systemd-networkd` configures no link, NetworkManager is not active, `dhcpcd.service` is not enabled, and no cloud-init file is under `/etc/network/interfaces.d`.
6. **Every stanza interface is declared:** each `<nic>` with an `iface` stanza must be configured by the generation, whether or not it is up now; otherwise `WOULD_UNCONFIGURE` with the interface name. Confirmation masks ifupdown for all of them, so an undeclared one would never come up again.

The existing gate changes:

- `TAKEOVER_MISMATCH` becomes kind-aware: on an ifupdown host without `--takeover` the message is "ifupdown still configures this host; apply with --takeover". With `--takeover` and a recognised ifupdown baseline, the "nothing to take over" case no longer fires.
- The unconditional `/sbin/ifup` refusal is replaced by rules 1–6 and applies only while ifupdown is not masked.

### DNS

resolved is in charge before the takeover (rule 4) and stays in charge. If the saved configuration declares `dns`, those servers are used; otherwise the servers DHCP or router advertisements offer. If no taken interface uses DHCP (`inet dhcp`) or router advertisements (`inet6 auto`) and no segment declares `dns`, the takeover is refused with `DNS_REQUIRED`, listing the servers resolved currently has for the host, so they can be declared.

### DHCP identity

The baseline capture records, per `inet dhcp` interface, dhcpcd's DUID and the interface's MAC. Every generation that configures that interface with DHCP, the takeover and every later one, adds to its `.network`:

```
[DHCPv4]
ClientIdentifier=duid
IAID=0x<last four MAC bytes, hex>
DUIDType=<first two DUID bytes as a decimal integer>
DUIDRawData=<remaining DUID bytes, colon-separated hex>
[DHCPv6]
IAID=<same>
DUIDType=<same>
DUIDRawData=<same>
```

and `Hostname=` when the stanza had `hostname`. The identity lives in the generations directory's baseline, so it survives confirmation and applies to later generations. The DUID is not secret; it appears in previews like any rendered value.

### Keeping the address across networkd starting and stopping

On the `.network` of an interface taken from ifupdown with DHCP, Andaru renders `KeepConfiguration=dynamic-on-stop` and `[DHCPv4] SendRelease=no`, so stopping networkd during a rollback neither removes the address nor releases the lease. The lab measures the effect; if it does not shorten the 7.4-second gap, the gap is recorded as a limit.

### Activate (takeover generation, ifupdown baseline)

After the rollback timer is armed, `realize activate`:

1. captures the baseline (kind `ifupdown`): which interfaces are `auto` and which `allow-hotplug`, the identities above, and whether `systemd-networkd` was running;
2. records the pre-change state for verification from `ip` and `/run/network/ifstate`: taken interfaces with a global IPv4 address and whether the default route uses one;
3. stops dhcpcd on each DHCP interface with `dhcpcd -p -x <nic>`, leaving addresses in place;
4. starts `systemd-networkd`, places the files, reloads;
5. verifies as today using the state from step 2, and additionally, when no segment declares `dns`, that resolved reports at least one server for the host.

If any step after 3 fails, the failure path runs the same hand-back as a rollback (below).

Nothing is enabled or masked yet, so a reboot before confirmation starts the host on ifupdown again.

### Hand-back (rollback, failed activation)

One routine, used by both. Each step is attempted even if an earlier one failed:

1. remove Andaru's files; if networkd was not running before, stop `systemd-networkd` and its socket, otherwise reload it;
2. for each taken interface that does not already have a running dhcpcd (DHCP) or its baseline address (static): `systemctl restart ifup@<nic>.service` for `allow-hotplug`, and `systemctl restart networking.service` once if any is `auto`. Never run ifup or dhcpcd from the rollback's own unit;
3. wait up to 30 seconds for every taken interface that had a global IPv4 address before to have one again.

Only when step 3 passes is the generation written `rolled_back` and the pointers moved. Otherwise the verb fails and stays pending; the timer's service retries it every 10 seconds, and step 2's skip makes the retry repeatable without bouncing an interface that came back.

### Confirm (making it permanent)

In this order, each step repeatable and repeated by boot, as for netplan:

1. `systemctl enable systemd-networkd.service`;
2. `systemctl mask networking.service ifup@.service`, so neither the boot service nor udev's hotplug raises a taken interface again.

`/etc/network/interfaces` stays as it was; ifupdown stays installed. `pointers.json` records the takeover kind, so boot knows which permanent steps to repeat.

### Boot

- A pending ifupdown takeover at boot is marked rolled back as today. Nothing else is needed: ifupdown is not masked and networkd is not enabled.
- A confirmed one: as today, plus the permanent steps above.
- When no generation is usable on a confirmed ifupdown host, boot removes the two masks and starts `networking.service`, so the host comes up on its unchanged ifupdown configuration. This replaces the netplan baseline-file fallback for this kind.
- The boot unit gains `Before=networking.service` so that it runs before ifupdown on every path.

### Undo by hand

`systemctl unmask networking.service ifup@.service`, `systemctl disable systemd-networkd.service`, delete the generations directory, reboot.

### The takeover assistant on an ifupdown host

`GET /api/v1/takeover/proposal` answers on an ifupdown host instead of refusing it. Its state rules (`taken-over`, `saved-not-applied`, `unsupported`) are unchanged.

- **Detection** reads the same files rules 1–3 accept, with the same parser, and produces the same per-interface description the netplan reader does: name, permanent MAC, IPv4 (`inet dhcp` is DHCP, `inet static` gives addresses and gateway), IPv6 (`inet6 auto` is SLAAC, `inet6 static` is static), and whether the interface carries the default route. A stanza the parser does not accept, or any failure of rules 2–5, is an error finding and the state is `unsupported`. Apply is not offered, and `--accept-detected` refuses.
- **Zones and adapters** follow the netplan rules: `wan` for the default-route interface, `lan` for the others; an interface with an address now cannot be left out. Rule 6 adds that every interface with an `iface` stanza must be kept, because confirmation masks ifupdown for all of them. The adapter screen does not let the operator drop one.
- **DNS:** when no taken interface uses DHCP or `inet6 auto`, the proposal declares `dns` with the servers and search domains resolved reports for the host now. The operator sees them pre-filled and can change them, so a static install is never refused with `DNS_REQUIRED` from the assistant. With DHCP or SLAAC, the proposal declares none, as on netplan.
- **Review** shows, besides the files, the DHCP identity lines (`ClientIdentifier`, `IAID`, `DUID…`) that keep the lease, and the limits below that apply: SLAAC addresses change, and `if-up.d` hooks stop running after confirmation.

Nothing else in the assistant changes: the draft, the head-guarded save and the apply are the same calls.

### Documents and contract updated in the same change

The takeover spec's "ifupdown hosts are refused" sentences, the assistant guide's netplan-only wording, the `TAKEOVER_UNSUPPORTED_BASELINE` and new rows in `docs/dev/core-api.md`, the undo procedure, and both finding enums in the OpenAPI document (`RESOLVED_NOT_IN_CHARGE`, `DNS_REQUIRED`, `DHCP_IDENTITY_UNKNOWN`).

## Acceptance and evidence

Unit tests, in CI on both architectures:

1. **Parser and recognition:** the installer's DHCP and static files and an empty `interfaces.d` are accepted; one refusal per unsupported stanza, option, `pointopoint`, symlinked or looping source, unparsable `dhcpcd.conf` directive, missing dhcpcd process, changed MAC, competing writer and resolved not in charge, each with its code and path.
2. **Gates:** an `iface` stanza for an interface that is down and not declared is `WOULD_UNCONFIGURE`; all-static without `dns` is `DNS_REQUIRED`; ifupdown without `--takeover` is `TAKEOVER_MISMATCH` with the ifupdown message.
3. **Identity:** rendered keys for a captured DUID and MAC, in the takeover generation and in a later one; a DUID that is absent, short or not hex refuses.
4. **Realize, against a temporary tree:** the hand-back runs every step after a failing one, skips interfaces that are already up, writes `rolled_back` only after addresses return, and stays pending otherwise; networkd failing to start runs the hand-back; confirm's steps repeat after a kill; boot with nothing usable unmasks and starts ifupdown.
5. **Proposal:** the installer's DHCP file gives one `wan` segment with IPv4 DHCP and IPv6 SLAAC and no `dns`; the static file gives static addressing and `dns` with resolved's current servers; every refusal of rules 1–5 makes the state `unsupported` with the finding; a down interface with a stanza is kept and cannot be left out.

On VM 117 with a 5-per-second ping from another host, reset to its snapshot afterwards:

6. From `base-ifupdown-dhcp-20261111`: `andaru ctl takeover` reports `unsupported` with `RESOLVED_NOT_IN_CHARGE`. Install systemd-resolved, shut down, snapshot as `base-ifupdown-resolved-<date>`.
7. `andaru ctl takeover` over `ssh -t`, every detected value accepted, not confirmed: at most one ping lost at the handoff, same IPv4 and DHCPv6 addresses, names resolve; the timer hands back to ifupdown with the same lease; the IPv4 gap is recorded with and without `KeepConfiguration`.
8. Reboot before confirmation: up on ifupdown, same address.
9. The same takeover on the serial console, confirmed from a new session, then rebooted: same IPv4 address, networkd enabled, both ifupdown units masked, names resolve; a later apply (adding a VLAN) keeps the address.
10. A static variant (installer static stanza, snapshotted stopped as its own base): `andaru ctl takeover --accept-detected` proposes `dns` with the current servers, takes over and is confirmed with the address kept; a hand-built batch without `dns` is refused with `DNS_REQUIRED`.
11. networkd made to fail to start (masked for the test): activation fails and the host is back on ifupdown with its address.

## Limits the owner accepts with this slice

- **SLAAC IPv6 addresses change** at the handoff; DHCPv4 and DHCPv6 addresses do not. An IPv6 session on a SLAAC address is cut.
- **A rollback may cut IPv4 for several seconds** (7.4 s in the probe) while ifupdown reacquires the lease.
- **Only the installer's shapes**, only dhcpcd with its default identity settings, and systemd-resolved must be installed first.
- **`if-up.d` and `if-down.d` hooks stop running** after confirmation.
- **One DHCP server tested** (the lab's). Whether a lease survives depends on the server honouring the client identity.
