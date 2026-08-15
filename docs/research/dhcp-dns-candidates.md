# DHCP and DNS implementation candidates for Andaru's first LAN

Research for issue #9. Target platform: Debian 13 "trixie" (stable as of 2026-08). This
document surfaces facts and trade-offs from primary sources only; it does not decide.
Every factual claim carries a URL. Facts checked on 2026-08-15.

Candidates surveyed: dnsmasq, ISC Kea, systemd-networkd's built-in DHCPv4 server,
Unbound, systemd-resolved, Knot Resolver, CoreDNS, and the status of isc-dhcp-server.

## Summary table

| Candidate | Role | trixie version (Debian tracker) | Upstream status | Config model | Reload without dropping leases/cache | Lease/cache persistence | Local recursion | DHCPv6 / RA | License |
|---|---|---|---|---|---|---|---|---|---|
| dnsmasq | DHCPv4/v6 + RA + DNS forwarder/cache | 2.91-1+deb13u1 (sid 2.93-1) | active; 2.93 released 2026 | text file(s), `--conf-dir`; D-Bus for upstream servers + cache clear only | SIGHUP re-reads hosts/dhcp-hosts/opts files, clears DNS cache, does NOT re-read main config; inotify dirs need no signal | lease file `/var/lib/misc/dnsmasq.leases`; survives restart | No (forwarder only) | Yes, both, in one daemon | GPL-2 or GPL-3 |
| ISC Kea | DHCPv4/v6 (+DDNS) | 2.6.3-1+deb13u1 (sid 3.0.4-1) | 2.6 not in ISC's current version table (3.0 LTS, 3.2 stable) | JSON file + JSON command channel over UNIX socket / HTTP(S) | `config-set`/`config-reload`/SIGHUP swap config in-process; leases live in backend | memfile CSV `/var/lib/kea/dhcp4.leases` (or MySQL/PostgreSQL) | n/a (DHCP only) | DHCPv6 yes; no RA daemon | MPL-2.0 |
| systemd-networkd `DHCPServer=` | DHCPv4 server only | systemd 257.13-1~deb13u1 | active (part of systemd) | `.network` INI file; `networkctl reload` | reload reconfigures matched interfaces; leases persisted by default (`PersistLeases=yes`) | persistent storage via `systemd-networkd-persistent-storage.service` | n/a | DHCPv6 server: none; RA: yes (`IPv6SendRA=`) | LGPL-2.1+ (systemd) |
| Unbound | recursive validating DNS resolver | 1.22.0-2+deb13u3 (sid 1.26.0-1) | active | text file + `include:`; `unbound-control` remote commands | `reload` flushes cache; `reload_keep_cache` keeps it; `fast_reload` (experimental) minimal downtime | cache in memory; `dump_cache`/`load_cache` for debugging | Yes | n/a (DNS) | BSD-3-Clause |
| systemd-resolved | stub resolver + cache for local host | separate binary pkg `systemd-resolved` from systemd 257.13 | active | `resolved.conf` + D-Bus/Varlink API | SIGHUP flushes caches, drops TCP, reloads config | cache in memory | No (stub/forwarding) | n/a | LGPL-2.1+ |
| Knot Resolver | recursive validating DNS resolver | 5.7.5-1 (sid 6.4.2-1) | active; 6.x is current upstream | 5.x: Lua; 6.x: YAML + HTTP API (`kresctl`) | 6.x: rolling restart of workers, "DNS service is not disrupted" | on-disk cache, persisted by default | Yes | n/a | GPL-3.0-or-later |
| CoreDNS | plugin DNS server/forwarder | not packaged in Debian | active | Corefile; `reload` plugin | graceful Corefile reload | cache plugin, in memory | No native recursion (needs libunbound plugin + recompile) | n/a | Apache-2.0 |
| isc-dhcp-server | DHCPv4/v6 (legacy) | 4.4.3-P1-8 in trixie; removed from sid | EOL Dec 2022 | text file | restart-based | `dhcpd.leases` | n/a | DHCPv6 yes; no RA | MPL-2.0 |

Sources for the version column: Debian tracker pages linked in each section below.

## Common context

- **DHCP servers need a real L2 position on the LAN.** ISC's own Kea Docker README:
  "It is not recommended to bind the container to the 'host' network - this will collide
  any with other DHCP servers on any network that the host is connected to and may cause
  the connection to the host to be lost" and recommends an IPVlan network per physical
  interface. Andaru's model (Podman with host networking) means the container process is
  effectively a host daemon; the collision warning applies to *other* DHCP servers on the
  same host, not to Podman itself. Kea's readme also notes DHCPv4 "needs to receive traffic
  from clients without an IP address; simple port forwarding is not sufficient".
  <https://gitlab.isc.org/isc-projects/kea-docker/-/raw/master/README.md>
- Podman `--network=host`: "Use the host's network namespace for the container instead of
  creating an isolated namespace. Warning: This gives the container full access to
  abstract Unix domain sockets and to TCP/UDP sockets bound to localhost."
  <https://docs.podman.io/en/latest/markdown/podman-run.1.html>
- Two DNS listeners cannot share `*:53`; systemd-resolved's stub listens on
  127.0.0.53/127.0.0.54 only by default (`DNSStubListener=`), so it coexists with a LAN
  resolver bound to LAN addresses. <https://www.freedesktop.org/software/systemd/man/257/resolved.conf.html>

## dnsmasq

**Debian packaging.** tracker: stable 2.91-1+deb13u1, testing/unstable 2.93-1, maintainer
Simon Kelley (upstream author), uploader Sven Geuer; latest upload 2026-06-08 (2.93-1);
0 RC bugs; two low-priority CVEs listed for trixie needing triage.
<https://tracker.debian.org/pkg/dnsmasq>

**Upstream.** 2.93 changelog (2026): DNSSEC fixes, CVE-2026-2291 heap-overwrite fix via
domain-name storage rework, DHCPv6-relay regression fix. <https://thekelleys.org.uk/dnsmasq/CHANGELOG>

**Configuration model.** Text config `/etc/dnsmasq.conf` plus `--conf-dir=<dir>,*.conf`
("Files are loaded in alphabetical order of filename"). Directory options
`--dhcp-hostsdir`, `--dhcp-optsdir`, `--hostsdir`: "Changed or new files within the directory
are read automatically, without the need to send SIGHUP" (inotify). D-Bus
(`--enable-dbus`): "The configuration which can be changed is upstream DNS servers (and
corresponding domains) and cache clear." UBus exists for OpenWrt.
<https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html>

**Reload semantics.** Man page NOTES: "When it receives a SIGHUP, dnsmasq clears its cache
and then re-loads /etc/hosts and /etc/ethers and any file given by --dhcp-hostsfile,
--dhcp-hostsdir, --dhcp-optsfile, --dhcp-optsdir, --addn-hosts or --hostsdir. The DHCP
lease change script is called for all existing DHCP leases. ... SIGHUP does NOT re-read
the configuration file." So changing `--dhcp-range` or interfaces = full restart, which
clears the DNS cache; DHCP leases survive restart because they are on disk.
`--servers-file` is re-read on SIGHUP for upstream server changes.
<https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html>

**Lease persistence.** `--dhcp-leasefile=<path>`; FILES lists `/var/lib/misc/dnsmasq.leases`.
`--dhcp-script` runs on add/old/del lease events; `--leasefile-ro` supports external
lease databases. <https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html>

**DNS.** "Dnsmasq is a DNS query forwarder: it is not capable of recursively answering
arbitrary queries starting from the root servers but forwards such queries to a fully
recursive upstream DNS server". Supports `--dnssec` validation with trust anchors.
DHCP leases automatically appear in DNS (Debian package description: "The DHCP server
integrates with the DNS server and allows machines with DHCP-allocated addresses to
appear in the DNS"). <https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html>,
<https://wiki.debian.org/dnsmasq>

**DHCPv6/RA.** "The dnsmasq DHCPv6 server provides the same set of features as the DHCPv4
server, and in addition, it includes router advertisements"; `--enable-ra` advertises a
prefix per `--dhcp-range` with RFC 6106 DNS parameters.
<https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html>

**Container-friendliness.** Single static-ish binary, single config, no database, no
D-Bus dependency required. Needs raw/broadcast sockets on the LAN interface (host
network namespace satisfies this) and `CAP_NET_BIND_SERVICE`/`CAP_NET_RAW`/`CAP_NET_ADMIN`
class privileges. Debian ships `dnsmasq-base` (binary) and `dnsmasq` (unit + `/etc/dnsmasq.d`).
<https://tracker.debian.org/pkg/dnsmasq>

**License.** "Dnsmasq is distributed under the GPL, version 2 or version 3 at your
discretion." <https://thekelleys.org.uk/dnsmasq/doc.html>

## ISC Kea

**Debian packaging.** tracker: stable 2.6.3-1+deb13u1, testing 3.0.3-3, unstable 3.0.4-1;
maintained by "Kea team" (uploaders Athos Ribeiro, Paride Legovini, Andreas Hasenack,
Adam Majer, Ondřej Surý, Jason Guy); binaries kea, kea-admin, kea-common,
kea-ctrl-agent, kea-dev, kea-dhcp-ddns-server, kea-dhcp4-server, kea-dhcp6-server,
kea-doc, python3-kea-connector; autopkgtest failures in unstable/testing; new upstream
3.2.0 available. <https://tracker.debian.org/pkg/isc-kea>

**Upstream lifecycle.** ISC's download page lists Kea 3.2.0 "Current Stable" (Jun 2026 -
EOL Jul 2027), 3.0.4 "Current Stable LTS" (EOL Jun 2028), 3.3.0 development. 2.6.x is
not in the table, i.e. trixie's 2.6.3 depends on Debian security support, not ISC.
<https://www.isc.org/download/>

**Configuration model.** JSON file per daemon (`Dhcp4`, `Dhcp6`, `DhcpDdns`). Management
API is JSON commands over a UNIX control socket or, "Kea version 3 extends servers by
HTTP/HTTPS sockets"; native HTTP in the DHCP daemons since 2.7.2, Control Agent (CA)
removed in 3.1.8. Consequence for trixie's 2.6.3: HTTP/REST requires the separate
`kea-ctrl-agent` process. Timeout on control connections is fixed at 10 s.
<https://kea.readthedocs.io/en/latest/arm/ctrl-channel.html>

**Reload semantics.** `config-set` replaces the running configuration from the API;
`config-test` validates; `config-reload` "instructs Kea to load again the configuration
file that was used previously"; SIGHUP: "the server rereads its configuration file and, if
the new configuration is valid, uses the new configuration. If the new configuration
proves to be invalid, the server retains its current configuration". Kea's rationale for
this API: restart-based reconfiguration "has the significant disadvantage of introducing
periods of downtime". `dhcp-disable`/`dhcp-enable` allow maintenance windows without
stopping. <https://kea.readthedocs.io/en/latest/arm/dhcp4-srv.html>,
<https://kea.readthedocs.io/en/latest/arm/ctrl-channel.html>

**Lease persistence.** Backends memfile (CSV on disk, in-memory index; example path
`/var/lib/kea/dhcp4.leases`, `"persist": true`), MySQL, PostgreSQL. `lfc-interval`
(default 3600 s) runs `kea-lfc` to compact the CSV. Leases are independent of config
reloads. <https://kea.readthedocs.io/en/latest/arm/dhcp4-srv.html>,
<https://kea.readthedocs.io/en/latest/arm/admin.html>

**DNS.** Kea has no DNS server; `kea-dhcp-ddns` "sends DNS update messages to the DNS
servers" (RFC 2136 style), so publishing lease names in DNS requires a DNS server that
accepts dynamic updates or custom glue. <https://kea.readthedocs.io/en/latest/arm/intro.html>

**DHCPv6/RA.** `kea-dhcp6` implements RFC 9915 DHCPv6 "with the exception of the
RECONFIGURE mechanism and ... IA_TA". Kea's component list (keactrl, kea-dhcp4,
kea-dhcp6, kea-dhcp-ddns, kea-admin, kea-lfc) has no router-advertisement daemon; RA must
come from elsewhere (e.g. networkd `IPv6SendRA=`, radvd).
<https://kea.readthedocs.io/en/latest/arm/dhcp6-srv.html>,
<https://kea.readthedocs.io/en/latest/arm/intro.html>

**Container-friendliness.** ISC publishes images at
`docker.cloudsmith.io/isc/docker/kea-dhcp4|kea-dhcp6` (Alpine based) and documents
IPVlan networking; see "Common context" above for its host-network warning.
<https://gitlab.isc.org/isc-projects/kea-docker/-/raw/master/README.md>. Multi-process
(dhcp4 + dhcp6 + optional ddns + optional ctrl-agent on 2.6) is heavier than dnsmasq.

**License.** MPL-2.0 (COPYING). Note ISC also sells subscriber-only hook libraries; the
core daemons are open source. <https://gitlab.isc.org/isc-projects/kea/-/raw/master/COPYING>,
<https://www.isc.org/download/>

## systemd-networkd built-in DHCP server

**Debian packaging.** networkd is inside the `systemd` binary package (257.13-1~deb13u1 in
trixie). <https://tracker.debian.org/pkg/systemd>

**Configuration model.** `.network` file: `[Network] DHCPServer=yes` ("If set to 'yes',
DHCPv4 server will be started"), `[DHCPServer]` section (`ServerAddress=`, `PoolOffset=`,
`PoolSize=`, `EmitDNS=`/`DNS=` with `_server_address`, `BootServerAddress=`, ...),
`[DHCPServerStaticLease]` with `MACAddress=`/`Address=` (both mandatory) for reservations.
<https://www.freedesktop.org/software/systemd/man/257/systemd.network.html>

**Reload semantics.** `networkctl reload`: "If a new, modified, or removed .network file
is found, then all interfaces that matched the file are reconfigured." `networkctl
reconfigure` alone does not re-read files. <https://www.freedesktop.org/software/systemd/man/257/networkctl.html>

**Lease persistence.** `PersistLeases=` (added v256): "When true, the DHCP server will load
and save leases in the persistent storage. When false ... bound leases will be lost when
the interface is reconfigured e.g. by networkctl reconfigure, or systemd-networkd.service
is restarted. That may cause address conflict on the network." Default yes via
`networkd.conf`. The server waits for `systemd-networkd-persistent-storage.service`
before starting unless `RelayTarget=` or `PersistLeases=no`.
<https://www.freedesktop.org/software/systemd/man/257/systemd.network.html>,
<https://www.freedesktop.org/software/systemd/man/257/networkd.conf.html>

**DNS.** None; `EmitDNS=` only tells clients which resolver to use (defaults to uplink
propagation). Lease hostnames are not published anywhere as DNS names by networkd.
<https://www.freedesktop.org/software/systemd/man/257/systemd.network.html>

**DHCPv6/RA.** No DHCPv6 *server*. RA yes: `[IPv6SendRA]` with `Managed=`/`OtherInformation=`
flags, `[IPv6Prefix]`, and `DHCPPrefixDelegation=` to re-advertise a delegated prefix.
<https://www.freedesktop.org/software/systemd/man/257/systemd.network.html>

**Container-friendliness.** It is the host's network manager, not a containerisable
service; only relevant if Andaru already picks networkd (see the NM-vs-networkd
research). Cannot run in a Podman container.

**License.** systemd is LGPL-2.1-or-later. <https://github.com/systemd/systemd/blob/main/LICENSE.LGPL2.1>

## Unbound

**Debian packaging.** tracker: stable 1.22.0-2+deb13u3, testing/unstable 1.26.0-1;
maintainer "unbound packagers" (uploaders Michael Tokarev, Robert Edmonds), dns-team on
Salsa; binaries unbound, unbound-anchor, unbound-host, libunbound8, python3-unbound. The
tracker shows a long list of CVE entries; check
<https://security-tracker.debian.org/tracker/source-package/unbound> for the current
trixie status before deciding. <https://tracker.debian.org/pkg/unbound>

**Upstream.** "Unbound is a validating, recursive, caching DNS resolver."
<https://unbound.docs.nlnetlabs.nl/en/latest/>

**Configuration model.** `unbound.conf` with `include:`; sections `server`, `stub-zone`,
`forward-zone`, `auth-zone`, `view`; `local-zone:`/`local-data:` for LAN names; DNSSEC via
`auto-trust-anchor-file:` (RFC 5011) and `domain-insecure:` for private zones;
`control-interface:` for `unbound-control` (default localhost). Runtime `local_zone`,
`local_data`, `local_data_remove` commands add/remove records without a reload.
<https://unbound.docs.nlnetlabs.nl/en/latest/manpages/unbound.conf.html>,
<https://unbound.docs.nlnetlabs.nl/en/latest/manpages/unbound-control.html>

**Reload semantics.** `reload`: "This flushes the cache and reads the config file
fresh." `reload_keep_cache`: keeps RRset/message cache "if (re)configuration allows for
it. That means the caches sizes and the number of threads must not change".
`fast_reload`: "keep downtime to a minimum ... The cache is kept" but "experimental at
this time". <https://unbound.docs.nlnetlabs.nl/en/latest/manpages/unbound-control.html>

**Cache persistence.** In-memory; `dump_cache`/`load_cache` exist "in order to aid with
debugging" and are "Not supported in remote Unbounds in multi-process operation".
<https://unbound.docs.nlnetlabs.nl/en/latest/manpages/unbound-control.html>

**Pairing with DHCP.** Unbound has no RFC 2136 update receiver in its manual; lease names
would be injected via `unbound-control local_data` (or a generated `include:` file plus
`reload_keep_cache`) from the DHCP server's lease hook (dnsmasq `--dhcp-script`, Kea hook
or DDNS to an intermediary).

**Container-friendliness.** Single daemon, plain files, official images exist from
NLnet Labs on Docker Hub; needs port 53 on LAN addresses (host network) and the anchor file
writable for RFC 5011. **License.** BSD-3-Clause.
<https://raw.githubusercontent.com/NLnetLabs/unbound/master/LICENSE>

## systemd-resolved

**Debian packaging.** separate binary package `systemd-resolved` from source systemd
257.13-1~deb13u1. <https://tracker.debian.org/pkg/systemd>

**What it is.** "It implements a caching and validating DNS/DNSSEC stub resolver, as well
as an LLMNR and MulticastDNS resolver and responder." Not a recursive resolver: it
forwards to configured upstreams. APIs: D-Bus `org.freedesktop.resolve1`, Varlink
`/run/systemd/resolve/io.systemd.Resolve`, glibc NSS via `nss-resolve`, and stub
listeners 127.0.0.53 (full) and 127.0.0.54 (proxy mode). Serving the LAN requires
`DNSStubListenerExtra=<LAN address>`.
<https://www.freedesktop.org/software/systemd/man/257/systemd-resolved.service.html>,
<https://www.freedesktop.org/software/systemd/man/257/resolved.conf.html>

**Reload semantics.** SIGHUP: "flush all caches it maintains, drop all open TCP
connections (if any), and reload its configuration files" (v256+). SIGUSR2 /
`resolvectl flush-caches` flushes; caches are also flushed on any network change.
`StaleRetentionSec=` allows serving stale records.
<https://www.freedesktop.org/software/systemd/man/257/systemd-resolved.service.html>

**DNSSEC.** `DNSSEC=yes|allow-downgrade|no`; allow-downgrade is "vulnerable to 'downgrade'
attacks". <https://www.freedesktop.org/software/systemd/man/257/resolved.conf.html>

**Pairing.** No mechanism to publish DHCP lease names to LAN clients (it resolves for the
local host, LLMNR/mDNS aside). Container: host service, not containerisable.

## Knot Resolver

**Debian packaging.** stable 5.7.5-1, testing/unstable 6.4.2-1 (uploaded 2026-08-06);
maintainers "knot-resolver packagers" (Daniel Kahn Gillmor, Jakub Ružička, Santiago Ruano
Rincón); no RC bugs. Note the major-version gap: trixie ships 5.x (Lua config, `kresd@N`
instances) while upstream documents 6.x (YAML + manager). CZ.NIC also publishes its own
`.deb` repository. <https://tracker.debian.org/pkg/knot-resolver>,
<https://www.knot-resolver.cz/documentation/latest/manager-api.html>

**Configuration/API (6.x).** YAML `/etc/knot-resolver/config.yaml` with JSON schema; HTTP
API on `/run/knot-resolver/kres-api.sock` by default: `{GET,PUT,DELETE,PATCH} /v1/config`,
`POST /reload`, `/metrics/prometheus`. "The API does not provide authentication nor
authorization of any sort." <https://www.knot-resolver.cz/documentation/latest/manager-api.html>

**Reload semantics (6.x).** `kresctl reload`: "Old subprocesses are replaced by new ones
(with updated configuration) using rolling restarts, ensuring that the DNS service is not
disrupted during the reload operation." <https://www.knot-resolver.cz/documentation/latest/manager-client.html>

**Cache persistence.** "By default the cache is saved on a persistent storage device so
the content of the cache is persisted during system reboot"; tmpfs recommended for
flash-limited devices. <https://www.knot-resolver.cz/documentation/latest/config-cache.html>

**License.** GPL-3.0-or-later. <https://gitlab.nic.cz/knot/knot-resolver/-/raw/master/COPYING>

## CoreDNS

Not in Debian: only `golang-github-coredns-caddy-dev` (library) exists in forky/sid; no
`coredns` binary package. <https://packages.debian.org/search?keywords=coredns&searchon=names&suite=all&section=all>

"CoreDNS does not have a native (i.e. written in Go) recursive resolver, but there is an
(external) plugin that utilizes libunbound" requiring a recompile. Corefile config;
`reload` plugin polls the file's SHA512 and does graceful reloads. Apache-2.0.
<https://coredns.io/manual/toc/>, <https://coredns.io/plugins/reload/>,
<https://raw.githubusercontent.com/coredns/coredns/master/LICENSE>

## isc-dhcp-server (status only)

ISC: "This software has reached End of Life" — 4.4.3-P1 released Oct 2022, EOL Dec 2022;
client and relay development ended early 2022. <https://www.isc.org/download/>,
<https://www.isc.org/dhcp/>

Debian: 4.4.3-P1-8 is still in trixie (stable) but the source was removed from
unstable/testing (last removal 2026-08-05), so it will not be in Debian 14; open
CVE-2026-18103 (OMAPI buffer overflow) noted on the tracker.
<https://tracker.debian.org/pkg/isc-dhcp>,
<https://packages.debian.org/search?keywords=isc-dhcp-server&searchon=names&suite=all&section=all>

Not a candidate for new work; listed for completeness.

## How the pieces pair

1. **dnsmasq alone** — one process, one config, DHCPv4+DHCPv6+RA+DNS forwarder; lease
   names appear in DNS automatically; no local recursion (needs an upstream resolver,
   possibly Unbound on the same box or a public/DoT upstream); config-file changes need a
   restart, which clears the DNS cache but not leases; D-Bus surface is limited to
   upstream servers/cache clear.
2. **dnsmasq (DHCP + local names) + Unbound (recursion)** — dnsmasq forwards to
   `127.0.0.1#5335`-style Unbound; keeps dnsmasq's DHCP/DNS name integration; two
   daemons, two configs; Unbound gives DNSSEC recursion and `reload_keep_cache`.
3. **Kea (DHCPv4/6) + Unbound or Knot (DNS) + RA from networkd/radvd** — full REST-style
   control (kea-ctrl-agent on trixie 2.6, native HTTP on 3.x), leases in memfile/DB,
   hot config swap; DNS names for leases need custom glue (Kea DDNS targets RFC 2136
   servers, Unbound has none) or a lease hook calling `unbound-control local_data`; three
   to five processes; RA lives outside Kea.
4. **networkd DHCPServer + Unbound/Knot** — zero extra DHCP daemon if networkd is the
   network manager, INI config, `networkctl reload`, persisted leases; DHCPv4 only,
   no lease-name DNS publication, static leases by MAC only.
5. **systemd-resolved** — host stub only; can serve the LAN via `DNSStubListenerExtra=`
   but is a forwarder with no lease-name integration; realistically the *host's* resolver
   in any of the above layouts, not the LAN's.

## Trade-off notes (facts, not a verdict)

- Only dnsmasq bundles DHCPv4, DHCPv6, RA and DNS in one process; the trade is that it is
  a forwarder and that most config changes are restart-only.
- Only Kea and Knot Resolver 6.x expose an HTTP API for live reconfiguration; Kea on
  trixie needs the extra Control Agent for that; Knot on trixie is 5.x without the manager.
- Unbound and Knot are the only packaged local recursors; both are in trixie, Unbound
  three minor releases behind sid, Knot one major behind.
- All licenses (GPL-2/3, MPL-2.0, LGPL-2.1+, BSD-3, GPL-3+, Apache-2.0) are compatible
  with aggregation next to AGPL code; MPL-2.0 (Kea) is file-level copyleft; Apache-2.0
  (CoreDNS) is one-way compatible with GPLv3/AGPLv3 only.
