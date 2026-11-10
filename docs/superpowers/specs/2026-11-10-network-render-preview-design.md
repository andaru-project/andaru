# Network rendering and apply preview

## Status and intent

Scope approved in conversation: render a saved network declaration into realizer files and expose a read-only preview through the root-local API and CLI. The owner asked for PPPoE to be included and rendered as well as it can be without bringing up a PPP link; the owner tests a real PPPoE session. Not implemented behaviour. Contracts remain provisional until Andaru 1.0.

This follows saved configuration ([PR #25](https://github.com/andaru-project/andaru/pull/25)). It answers one question: **given saved version N and this host, exactly which files would Andaru hand to systemd-networkd and pppd, and what stops them from working?** It does not write those files to their destinations, reload anything, create live generations, or arm a rollback.

### Authorities

- [Issue #4](https://github.com/andaru-project/andaru/issues/4): networkd is the interface realizer and reads only `/run`; physical links match on `PermanentMACAddress=`; Andaru never renames interfaces; foreign files in `/etc/systemd/network` are refused; rendering is deterministic.
- [Issue #6](https://github.com/andaru-project/andaru/issues/6): findings point at declaration fields by JSON Pointer; rendered files can contain secrets and are root-only; per-area apply and live generations come later.
- [Issue #14](https://github.com/andaru-project/andaru/issues/14): PPPoE over a VLAN with a larger parent MTU is a required topology; unresolved hardware is an actionable state, never a guess; unrelated failures must not take down what still works.
- [Saved-configuration design](2026-11-10-saved-network-configuration-design.md): the declaration model this slice consumes unchanged.
- [systemd#481](https://github.com/systemd/systemd/issues/481) and [ppp#459](https://github.com/ppp-project/ppp/issues/459): the community reference for running pppd beside networkd.

## Decisions and scope

| In this slice | Later slices |
| --- | --- |
| Pure renderer: declaration + observed links + secrets → files and findings | Writing files into `/run`, `networkctl reload`, starting pppd |
| networkd `.netdev`/`.network` for Ethernet, VLAN, bridge, bond and the saved addressing modes | Live generations, `generations/<n>/`, confirmed / last-known-good pointers |
| pppd options file, service unit and `.network` per PPPoE link | Rollback timer, confirmation, boot oneshot |
| `GET /api/v1/preview/network` and `andaru ctl preview network` | Management election and lockout-risk findings |
| Host findings: missing or ambiguous NICs, foreign networkd files, missing pppd | Diff against a confirmed generation (none exists yet) |
| Tool-level syntax proof without a live link | Takeover of netplan/ifupdown, routes, DNS, firewall, DHCP server |

The declaration schema does not change. No new stored state is created.

## Renderer

New package `internal/render`. One entry point takes the validated saved declaration, the observed host links and the paired secret document. It returns files and findings and performs no I/O.

The renderer receives real secret values, because one finding depends on their length. Each secret-bearing file is produced in two forms from the same code path: the real content and a redacted content in which every secret value is the literal `<redacted>`. Only the redacted form is ever serialised by the API, logged or placed in a finding. Tests use the real form, so secret quoting is exercised now rather than first at apply time.

Rendering is deterministic: the same inputs give byte-identical output. Files are sorted by path; multi-valued keys are sorted; no timestamps, hostnames or map iteration order reach the output.

### Observed links and interface names

The host scan changes from `ip -j address show` to `ip -d -j address show`, and the observed link gains two fields the current adapter drops: `permaddr` and `linkinfo.info_kind`. Without them, a VLAN or bridge created on a NIC reports the NIC's own MAC and would make the NIC look ambiguous. The existing `status/links` response is unchanged.

Physical NICs keep whatever name the kernel chose. A declared Ethernet link resolves among observed links with `link_type` `ether` and no `info_kind`. Its permanent MAC is `permaddr` when present, otherwise `address`: per the #4 probe, `ip` omits `permaddr` when the two are equal. Zero matches is `HARDWARE_MISSING`; more than one is `HARDWARE_AMBIGUOUS`. Neither is resolved by guessing.

Virtual links need a kernel name of at most 15 bytes, and the user-chosen `name` may be 128 arbitrary code points, so it is never used. The name is a kind prefix plus the first 12 hexadecimal digits of the link ID's UUID: `vl…` VLAN, `br…` bridge, `bo…` bond, `pp…` PPPoE (14 bytes). `NAME_COLLISION` is raised when two declared links derive the same name, or when an observed link already has that name with a different kind. An observed link of the same kind is Andaru's own from an earlier apply.

User-supplied text (`name`, `description`) is never written into any rendered file, including comments, so it cannot inject configuration.

### networkd files

Destination `/run/systemd/network/50-andaru-<kernel-or-derived-name>.{netdev,network}`.

- **Ethernet:** `.network` with `[Match] PermanentMACAddress=`. No `.link` file and no `Name=` match.
- **VLAN, bridge, bond:** a `.netdev` (`Kind=`, `[VLAN] Id=`, `[Bond] Mode=`) and a `.network` matching the derived `Name=`. A VLAN's parent `.network` lists `VLAN=`; members list `Bridge=` or `Bond=`.
- **Every rendered link:** `[Link] RequiredForOnline=no`, so no link can block a system-wide wait-online. Every link except PPPoE also has `MTUBytes=`. Ethernet, VLAN, bridge and bond files set `LLDP=no`; the networkd default would otherwise listen.
- **A link with no segment** (pure parent or member) carries no addressing: `LinkLocalAddressing=no`, `IPv6AcceptRA=no`.
- **Disabled link:** a disabled Ethernet link renders `ActivationPolicy=always-down`. A disabled virtual link is not rendered.

Segment addressing on the link's `.network`. IPv4 and IPv6 together produce a single `DHCP=` key: `yes`, `ipv4`, `ipv6`, or absent.

| Declared | Rendered |
| --- | --- |
| IPv4 `none` | no IPv4 keys |
| IPv4 `static` | `Address=` per address, `Gateway=` when declared |
| IPv4 `dhcp` | DHCPv4 client on; `[DHCPv4] UseDNS=no`, `UseNTP=no`, `UseHostname=no`, `UseDomains=no` |
| IPv6 `none` | `LinkLocalAddressing=no`, `IPv6AcceptRA=no` |
| IPv6 `static` | `Address=`, `Gateway=`, `IPv6AcceptRA=no` |
| IPv6 `slaac` | `IPv6AcceptRA=yes`, the upstream block below |
| IPv6 `dhcp6` | `IPv6AcceptRA=yes`, DHCPv6 client on, the upstream block below, `[DHCPv6] PrefixDelegationHint=::/N` when `requestPrefixLength` is set |
| IPv6 `delegated` | `IPv6AcceptRA=no`, `DHCPPrefixDelegation=yes`; `[DHCPPrefixDelegation] UplinkInterface=<upstream name>`, `SubnetId=0x<id>`, `Assign=yes` |
| `ra.enabled` | `IPv6SendRA=yes`; `[IPv6SendRA] Managed=`, `OtherInformation=`; one `[IPv6Prefix]` per static /64. Delegated prefixes are announced by networkd's default. |

The upstream block makes networkd ignore DNS, NTP, hostname and search domains offered by a provider, since those belong to later areas and networkd must not become their authority:

```
[IPv6AcceptRA]
UseDNS=no
UseDomains=no

[DHCPv6]
UseDNS=no
UseNTP=no
UseHostname=no
UseDomains=no
```

A DHCP, RA or PPP default route is accepted; the explicit routes contract is later.

`IPv6SendRA=yes` makes networkd enable IPv6 forwarding on that interface by default. That is correct for a router LAN, and it is not overridden here. It is recorded because forwarding policy is otherwise a later area: the apply slice must not activate these files before a fail-closed packet filter is loaded.

RA on a static subnet that is not a /64 cannot offer SLAAC and is an `RA_PREFIX_UNSUPPORTED` warning. RA on `slaac` or `dhcp6` has nothing to announce and gets the same warning.

### PPPoE

networkd has no PPPoE client and will not get one: systemd#481 has been open since 2015, and the Debian ppp maintainer's position there is that networkd's only sensible integration is to start `pppd`. The arrangement that thread converged on, collected in ppp#459, splits the work three ways, and this slice renders exactly that split:

- **networkd** owns the parent link (VLAN, MTU) and, on the PPP interface, IPv6: router discovery, DHCPv6 and prefix delegation.
- **pppd**, started by a systemd service, owns the session: discovery, authentication, the IPv4 address and the IPv4 default route.
- **A `.network` file matching the PPP interface** tells networkd to leave pppd's configuration alone.

A PPPoE link is rendered when it is enabled and has a segment. Supported segment addressing is IPv4 `ppp` or `none` and IPv6 `none`, `slaac` or `dhcp6`, with at least one family in use and RA off. Anything else the saved model permits on a PPPoE segment (IPv6 `static` or `delegated`, RA, both families `none`) is an `UNSUPPORTED_COMBINATION` error and the link is not rendered. A PPPoE link without a segment is not rendered and gets the same code as a warning. A parent interface whose kernel name contains anything outside `[A-Za-z0-9_.:-]` is refused with `INTERFACE_NAME_UNSUPPORTED`: the name is written into a unit, a device-unit name and a pppd word, which each quote differently, and predictable interface names never need it. Each rendered link produces three files.

1. **`/run/andaru/ppp/<pp-name>.options`**, mode `0600`, marked secret:

   ```
   plugin pppoe.so
   nic-<parent kernel name>
   ifname <pp-name>
   linkname <pp-name>
   user "<escaped username>"
   password "<escaped password>"
   noauth
   nolog
   hide-password
   persist
   maxfail 0
   holdoff 10
   lcp-echo-interval 15
   lcp-echo-failure 4
   mtu <link mtu>
   mru <link mtu>
   ```

   plus `noipdefault` and `defaultroute` for IPv4 mode `ppp`, or `noip` for `none`; and `+ipv6` with `ipv6cp-use-persistent` when IPv6 is in use, otherwise `noipv6`. `ipv6cp-use-persistent` derives a stable link-local identifier instead of a random one per session. `usepeerdns` and `defaultroute6` are never set: DNS is a later area, and pppd's own manual warns against `defaultroute6` because the kernel installs the IPv6 default route from router advertisements.

   A username and password are always written, including placeholder credentials such as an ISP's fixed `user@realm`. pppd copies each into a 256-byte buffer and silently truncates, while the saved model allows longer values; a username or password over 255 bytes is a `CREDENTIAL_TOO_LONG` error, because the session would fail authentication with no local symptom.

   Option-file words are double-quoted with `\` and `"` backslash-escaped. That is sufficient: pppd keeps every other byte, including a newline, inside a quoted word. Usernames reject control characters; secret values reject only NUL, so the quoting tests include newline, carriage return, tab, `#` and `\`.

   pppd always reads `/etc/ppp/options` first. Debian 13's shipped file sets `auth`, serial-line options that the PPPoE plugin ignores, and `lcp-echo-interval 30` / `lcp-echo-failure 4`. The explicit `noauth` overrides `auth`, and valued options here win because they are parsed later. An administrator's edits to that file can still change behaviour; this slice does not police it. Debian's `ip-up.d/0000usepeerdns` hook does nothing without `usepeerdns`.

   A link MTU above 1492 relies on RFC 4638, which the plugin negotiates when `mru` exceeds 1492; the saved model already requires the parent MTU to be at least 8 larger.

   `persist` with `maxfail 0` retries indefinitely, ten seconds apart, including after an authentication failure. An ISP that locks out repeated failures would need a longer hold-off; none is configurable in this slice.

   **Credential delivery.** pppd 2.5.2 cannot read systemd credentials; ppp#459 requesting that is still open, and its author's workaround generates a temporary options file. A root-only `0600` file under `/run` is the same exposure with fewer moving parts, and matches #6's rule that rendered files may contain secrets. The `passwordfd.so` plugin was considered and not used: it needs a helper process to feed the descriptor.

2. **`/run/systemd/system/andaru-pppoe-<pp-name>.service`**, modelled on the `ppp@.service` Debian ships:

   ```
   [Unit]
   BindsTo=sys-subsystem-net-devices-<escaped parent>.device
   Wants=systemd-networkd.service
   After=sys-subsystem-net-devices-<escaped parent>.device systemd-networkd.service

   [Service]
   Type=notify
   ExecStartPre=/usr/lib/systemd/systemd-networkd-wait-online --interface=<parent>:carrier --timeout=60
   ExecStart=/usr/sbin/pppd file /run/andaru/ppp/<pp-name>.options nodetach up_sdnotify
   ExecReload=/bin/kill -HUP $MAINPID
   SuccessExitStatus=5
   Restart=on-failure
   RestartSec=5
   ```

   The parent name in the `.device` unit name is escaped by systemd's unit-name rules, since a physical NIC name may contain `-`.

   The `ExecStartPre` wait closes a race reported in systemd#481: the parent's `.device` unit is active as soon as the kernel creates the interface, before networkd has raised its MTU, so pppd would negotiate 1492 instead of 1500. `systemd-networkd-wait-online` with an explicit `:carrier` returns only once networkd reports the interface both configured and at carrier, which is after the MTU is set. The explicit state means the parent keeps `RequiredForOnline=no`. Exit status 5 is pppd's response to a stop signal. The unit is not hardened with `ProtectKernelTunables=`, which the same thread found prevents IPv6 address assignment. Not installed or started by this slice.

3. **`/run/systemd/network/50-andaru-<pp-name>.network`**:

   ```
   [Match]
   Name=<pp-name>
   Type=ppp

   [Link]
   RequiredForOnline=no

   [Network]
   KeepConfiguration=yes
   ```

   `KeepConfiguration=yes` is what stops networkd removing pppd's address and its `proto boot` default route. systemd 257.0–257.3 ignored it for foreign routes on a networkd restart ([systemd#36411](https://github.com/systemd/systemd/issues/36411)); the fix was backported to `v257-stable` on 2025-02-22, so Debian 13's 257.13 has it. `yes` also keeps DHCPv6 and SLAAC state across restarts; whether the narrower `static` suffices is for the real-line test. No `MTUBytes=` and no LLDP key are rendered: pppd sets the PPP interface MTU, and LLDP exists only on Ethernet.

   IPv6 `none` adds `LinkLocalAddressing=no` and `IPv6AcceptRA=no`. `slaac` and `dhcp6` add the table rows above, and `dhcp6` on PPP additionally sets `[DHCPv6] WithoutRA=solicit`.

   `WithoutRA=solicit` is PPP-specific. Reports in systemd#481 and [systemd#26681](https://github.com/systemd/systemd/issues/26681) show both kinds of ISP: some send router advertisements with the managed flag, others send none and expect a bare DHCPv6 solicit. `IPv6AcceptRA=yes` together with `WithoutRA=solicit` serves both, and with `IPv6AcceptRA=no` the interface was reported stuck in `configuring`. On Ethernet uplinks the DHCPv6 client stays RA-driven, the networkd default. The uplink does not set `DHCPPrefixDelegation=yes`: per the networkd maintainer in systemd#481 that assigns a /64 of the delegated prefix to the uplink itself, which is not declared here. Downstream segments name the PPP interface in `UplinkInterface=`.

When a PPPoE link is rendered and `/usr/sbin/pppd` or `/usr/lib/pppd/*/pppoe.so` is absent, the preview reports `TOOL_MISSING` (error) naming the `ppp` package. When `/dev/ppp` is absent it reports `KERNEL_SUPPORT_MISSING` (error): Debian's cloud kernel (`linux-image-cloud-*`) ships no PPP modules, so pppd cannot even parse its options there, and the standard `linux-image-*` kernel is required. The files are still rendered in both cases.

### Name-bound output

Some rendered values need a physical NIC's current kernel name: pppd's `nic-`, the service's references to its parent, and `UplinkInterface=` when the upstream is Ethernet. They are filled from the observed links at render time and the result lists each resolution. Issue #4 requires names to be resolved at point of use and boot to copy pre-rendered files; how a stored generation re-resolves these values at boot is **left to the apply slice**. A topology whose PPPoE parent and delegation upstream are virtual links, such as the owner's PPPoE over a VLAN, has no name-bound output: the VLAN's own physical parent is matched by MAC.

### What is omitted when something is unavailable

An object is unavailable when it is a missing or ambiguous NIC, a disabled link, or not rendered for a reason above.

- A VLAN or PPPoE link whose parent is unavailable is not rendered. Neither is a segment whose link is unavailable, nor a delegated segment whose upstream segment is.
- A bridge or bond is still rendered when some members are unavailable; only those members are left out. Losing one bond member or one bridge port must not take down the rest, and fewer ports is never more permissive.
- Each omission is a `DEPENDENCY_UNAVAILABLE` warning at the referencing field, for example `/links/3/parent` or `/links/5/members/1`.
- Unrelated links always render.

## Findings

```json
{"severity": "error", "code": "HARDWARE_MISSING", "path": "/links/0/permanentMac", "message": "No interface with this permanent MAC is present."}
```

`severity` is `error` or `warning`. `path` is a JSON Pointer into the saved snapshot `value`, or empty for host-wide findings. Messages never contain secret values. Findings are sorted by path, then code, then message. `realizable` is true when there are no errors.

| Code | Severity | Raised when |
| --- | --- | --- |
| `HARDWARE_MISSING` | error | no observed physical link has the declared permanent MAC |
| `HARDWARE_AMBIGUOUS` | error | several observed physical links share it |
| `NAME_COLLISION` | error | derived names clash, or an observed link of another kind has the name |
| `FOREIGN_CONFIGURATION` | error | a `.network`, `.netdev` or `.link` file exists in `/etc/systemd/network`, or one without the `50-andaru-` prefix exists in `/run/systemd/network` (netplan's output on cloud images), or a `<file>.d/*.conf` drop-in exists in either directory; one finding per file, naming it |
| `TOOL_MISSING` | error | a PPPoE link is rendered and pppd or its plugin is absent |
| `KERNEL_SUPPORT_MISSING` | error | a PPPoE link is rendered and `/dev/ppp` is absent; the message suggests loading `ppp_generic` before replacing the kernel |
| `INTERFACE_NAME_UNSUPPORTED` | error | a PPPoE link's parent interface has a kernel name outside `[A-Za-z0-9_.:-]` |
| `CREDENTIAL_TOO_LONG` | error | a rendered PPPoE username or password exceeds 255 bytes |
| `UNSUPPORTED_COMBINATION` | error; warning for a PPPoE link with no segment | addressing on a PPPoE segment that this slice cannot realize |
| `DEPENDENCY_UNAVAILABLE` | warning | an object or member was left out because something it references is unavailable |
| `RA_PREFIX_UNSUPPORTED` | warning | RA is enabled with nothing announceable |

A missing secret cannot occur: the saved store publishes configuration and secrets as one validated pair. If the pair is unreadable the request fails with the existing `503 STORE_UNAVAILABLE`.

## API and CLI

`GET /api/v1/preview/network` (and `HEAD`), root Unix peer only, same authorization order and error envelope as existing routes. Optional `?savedVersion=<n>`; the default is the saved head. Unknown or repeated query keys and a malformed version are `400 INVALID_REQUEST`; a version that does not exist is `404 NOT_FOUND`. The response carries `Andaru-Saved-Version`.

```json
{
  "state": "preview",
  "savedVersion": "7",
  "realizable": false,
  "resolved": [{"link": "lnk_…", "interface": "eth0"}],
  "files": [{"path": "/run/systemd/network/50-andaru-eth0.network", "mode": "0644", "secret": false, "content": "[Match]\n…", "sources": ["/links/0", "/segments/1"]}],
  "findings": []
}
```

`state` is `preview`, distinct from `saved`, so a client cannot mistake it for live or applied state. For a secret file, `content` is the redacted form. The host scan reads the link inventory, the two networkd directories and the pppd paths; inventory failure is the existing `503 INVENTORY_UNAVAILABLE`.

`andaru ctl preview network [--saved-version N]` prints the validated JSON response, with the existing exit codes and no success output on failure. There is no command that exports unredacted files; to test a real PPPoE session the operator copies the previewed files and replaces the `<redacted>` word.

The served OpenAPI document and operator guide describe the new resource. The engine unit's sandbox is unchanged: every path read is already readable under `ProtectSystem=strict`.

## Acceptance and evidence

1. **Golden files** for: the owner's topology from #14 (Ethernet 1508 → VLAN 6 at 1508 → PPPoE 1500 with a DHCPv6 prefix request, and a LAN with static IPv4, delegated IPv6 and RA); the existing saved-configuration fixture (PPPoE 1492 on a 1500 VLAN, bridged LAN); and bond, DHCP, disabled and missing-member cases. Each key in each golden file is checked against systemd 257's `systemd.network(5)`/`systemd.netdev(5)` and pppd 2.5.2's manual, with citations in the plan.
2. **Determinism:** shuffled declaration input and repeated renders are byte-identical.
3. **Findings:** one test per code with its exact pointer; unrelated links keep rendering when one NIC is missing; a NIC with a VLAN and a bridge on it, all reporting the same MAC, is not ambiguous.
4. **Redaction:** a sentinel secret never appears in preview responses, findings or captured logs, and appears in the real form of the secret file only.
5. **pppd parses the output, no link:** as root, in a private network namespace containing a dummy Ethernet interface named as the rendered parent, `pppd file <rendered> logfd 1 dryrun` exits 0 and its reported options show the expected `ifname`, `mtu`, `mru` and a `user` value containing quotes, backslashes, `#`, spaces and a newline. The password uses the same quoting routine, so the username read-back is its proof; pppd prints passwords as `??????`. The namespace is needed because the plugin validates `nic-` against a live interface while parsing; `dryrun` exits before any discovery packet. The trailing `logfd 1` overrides the rendered `nolog` for the test only. Probed on the lab VM on 2026-11-10: exit 0 with the report, exit 2 without the parent or with an unknown option. This check needs root and `/dev/ppp`. It runs on the lab VM; it runs in CI as well if the hosted runners' kernels provide PPP, which the first CI run establishes.
6. **systemd parses the unit:** `systemd-analyze verify` on the rendered service exits 0 with empty output, with pppd and wait-online present.
7. **networkd parses the files:** a private `systemd-networkd` instance in isolated network and mount namespaces loads the rendered directory and must log `Successfully loaded.` for every file with no parse warning. This is the only check that catches a misplaced key mechanically. Probed on the lab VM on 2026-11-10: unknown keys and sections are reported with file and line, and the host's networkd and links are untouched. The files are never loaded into the host's networkd.
8. **Real path:** the installed engine on Debian VM 114 serves the preview through the CLI; on that cloud image the result must include `FOREIGN_CONFIGURATION` for netplan's files, and the host's nft rules, forwarding and addresses are unchanged before and after.

The lab VM was snapshotted (`pre-ppp-20261110-185944`) before `ppp` and the standard kernel were installed. Items 5–7 run on the lab VM, not in CI: the hosted runners are Ubuntu 24.04 with ppp 2.4.9 and systemd 255, so their verdict would not describe Debian 13. CI runs the golden, findings, redaction and API/CLI tests on both architectures.

## Limits the owner accepts with this slice

- **PPPoE is rendered, not exercised.** No PADI is sent and no authentication happens. The split between pppd and networkd, `KeepConfiguration=yes`, and DHCPv6 prefix delegation over the PPP interface follow configurations reported working in systemd#481 and ppp#459, but those reports also show the IPv6 half breaking across systemd releases (251, 254) and differing by ISP. The owner's real-line test is the qualification; expect the `[DHCPv6]` keys to be the part that needs adjusting.
- **Permanent MAC by inference.** When `ip` shows no `permaddr`, the current address is taken as permanent. networkd reads the driver's permanent address itself, so a NIC whose driver reports none could match in the preview and not in networkd. Not observed on virtio or the #4 probes.
- **Name-bound values are resolved at preview time only** (see above).
- **No lockout analysis** until management election exists.
- **No diff view** until a confirmed generation exists; the preview is the full file set.
- **Foreign configuration is reported, not removed.** Taking over from netplan or ifupdown is the handover slice.
