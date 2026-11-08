# Pasta: IPv6 HTTPS and dual-stack UDP qualification

## Conclusion

**Direct rootless pasta preserved the external IPv6 TCP peer and both IPv4/IPv6 UDP senders, including source ports.** Guest firewall drops and explicit source allows worked for all three protocol cases. This extends the [IPv4/TCP comparison](container-client-ip-spike.md), not the named rootless bridge mode that rewrote addresses.

The result supports further design of a direct-pasta ingress profile for Andaru. It does not establish production DNS behaviour, all Compose networking, privileged-port binding, performance, or a complete firewall/router policy.

## Environment and protection

Used the retained Debian 13.7 VM 114 `andaru-lab` on `proxmox-4`, rootless Podman 5.4.2, Docker Compose 2.26.1 as the explicitly selected provider, and Debian pasta. Before changes, created the `pre-pasta-dualstack` disk snapshot. The existing rootless Compose workload and its firewall remained in place.

Both the external test machine and the VM had global IPv6 addresses. An independent IPv6 TCP connection to the guest's SSH listener verified the external path before container tests. These were separate machines on the same lab LAN, not Internet-origin or guest-loopback requests. Source addresses were discovered from current interfaces/routes and bound explicitly by the test client.

Two new Compose services used `network_mode: pasta` and existing locally retained images; no registry downloads were required:

| Service | Image | Host publication | Container listener |
| --- | --- | --- | --- |
| Traefik | 3.7.17 | `[::]:19446:8443` | HTTPS on 8443 |
| UDP peer responder | Python 3.13 Alpine | `0.0.0.0:19553:5353/udp` | IPv4 UDP 5353 |
| Same UDP responder | Same | `[::]:19554:5353/udp` | IPv6 UDP 5353 |

The UDP responder opened separate AF_INET and IPv6-only AF_INET6 sockets. It accepted bounded JSON datagrams containing a random token and built replies/log records from the actual `recvfrom` peer tuple, not any address supplied in the packet. A script-based health check avoided the inline-healthcheck argument issue previously found on these runtime versions.

Traefik reused the read-only test certificate/authentication files and a watched configuration directory. Its upstream was a protected temporary IPv4 HTTP backend on the VM. This tests IPv6 **ingress** to Traefik, not an IPv6 upstream connection or multi-container service-name discovery. Both payloads dropped capabilities and set no-new-privileges; `/proc/1/status` confirmed zero effective capabilities and `NoNewPrivs: 1`.

## Source-address and protocol evidence

| Case | Application/transport check | Authoritative source evidence |
| --- | --- | --- |
| IPv6 TCP/HTTPS | Trusted test certificate and hostname verified; authenticated root returned expected backend body with 200; no credentials returned 401 | Traefik's retained `ClientAddr` host and port matched the external TCP socket's `getsockname()` |
| IPv4 UDP | Reply carried the matching random token and IPv4 family | Application `recvfrom` address and port matched the external IPv4 socket; reply and independently read server log agreed |
| IPv6 UDP | Reply carried the matching random token and IPv6 family | Application `recvfrom` address and port matched the external IPv6 socket; reply and independently read server log agreed |

For HTTPS peer correlation, the scripted client sent a unique request path and forged forwarding headers; those sent headers were not separately retained in the sanitized evidence or header-dropping access log. The static backend returned 404 for that unknown path; this was a correlation marker, not a fabricated successful page. Address proof used `ClientAddr`, which [Traefik 3.7.17 sets from `req.RemoteAddr`](https://github.com/traefik/traefik/blob/v3.7.17/pkg/middlewares/accesslog/logger.go#L273-L278), not header-derived `ClientHost`, and does not depend on the spoofed header values. The fixture did not enable forwarded-header trust or PROXY protocol; see the [previous trust-setting verification](container-client-ip-spike.md#peer-field-and-trust-setting-verification).

UDP observations came from the purpose-built responder, not Traefik's UDP proxy, Kea, Unbound or pfBlockerNG. The successful UDP messages were small single datagrams, not DNS protocol, EDNS fragmentation, QUIC, load or packet-loss tests.

## Firewall evidence

Before startup, a dedicated guest-only inet INPUT chain dropped external TCP to 19446 and UDP to 19553/19554. It also prevented external access to the temporary backend. Existing management and the original workload were untouched.

1. The external IPv6 HTTPS connection timed out under the TCP drop.
2. IPv4 and IPv6 UDP datagrams timed out under their drops. Their unique tokens did not appear in the server's retained peer log.
3. Adding source-specific IPv6 TCP, IPv4 UDP and IPv6 UDP accepts before those drops allowed the corresponding positive checks to pass.
4. Labeled allow counters recorded the permitted traffic, and aggregate drop counters were consistent with the denied requests. One UDP drop rule covered both ports; per-family UDP denial is established by each client's timeout and absent server nonce, not separately attributed drop counters.

At the check while the probe was active, the original lab table matched its pre-test stateless form and only that table plus the explicitly created probe table existed. Both host forwarding sysctls matched their zero baseline. After cleanup the entire stateless ruleset matched, as detailed below. These observations found no pasta-added host NAT/filter tables; they are not continuous monitoring of every instant. No Proxmox host firewall or container runtime was modified.

These are focused ingress checks with a default-accept test chain and explicit test-port drops, not a full Andaru default-deny policy or qualification of every IPv6 forwarding/ICMPv6 path.

## Restoration and retained evidence

Stopped and removed the two probe containers and temporary HTTP backend service, then deleted only the test-owned firewall table. The entire stateless guest ruleset matched its pre-test baseline, and IPv4/IPv6 forwarding values remained unchanged. The original Compose fixture passed its TLS/authentication, metrics, logs and persistent-marker checks afterward.

The VM and all snapshots remain. Fixture definitions, private test references, peer/access logs and sanitized `~/pasta-dualstack-probe/evidence.json` are retained for inspection. Only temporary local worktree fixtures are disposable. No snapshot rollback was needed or tested, and the VM disk was not reset.

## Design consequence and remaining scope

The narrow original-client-IP blocker is now exercised for **IPv4 TCP (prior report), IPv6 TCP, IPv4 UDP and IPv6 UDP** with direct pasta. These results justify drafting an ingress-profile contract rather than a blanket rootful switch or unbounded protocol probing; the limits below still gate adoption.

Keep these limits explicit:

- Direct pasta is not a named Compose bridge. Inter-service connectivity and allowed access to host services need a deliberate contract.
- Only high published ports were used. Binding DNS port 53 as an unprivileged service is a separate privilege/socket-activation question.
- UDP sender identity does not certify DNS server configuration, per-client policy semantics, DoH/DoT/DoQ, large packets, or sustained traffic performance.
- IPv6 prefix changes, packet fragmentation, upstream IPv6 proxying, startup failure recovery and all sandbox boundaries were not tested here.
- No Andaru runner, manifest compiler, generated GUI, or production network policy was implemented by this spike.
