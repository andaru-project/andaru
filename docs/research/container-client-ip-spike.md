# Container client IPs: rootless direct networking versus rootful bridge

## Conclusion

**Direct rootless pasta preserved the real IPv4 client address in Traefik without adding host firewall tables.** Source-preserving slirp4netns worked too. This resolves the narrow source-IP problem from the [previous rootless bridge probe](podman-compose-spike.md), not every container networking requirement.

Prefer qualifying a direct-pasta ingress profile next, rather than switching the appliance to rootful containers by default. Rootful bridge also preserved source IPs, but changed host firewall/forwarding state and required filtering in FORWARD rather than INPUT. This remains research for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), not an approved runtime policy.

## Environment and method

Used the existing persistent Debian 13.7 VM 114 `andaru-lab` on `proxmox-4`: Podman 5.4.2, Docker Compose 2.26.1 as the explicit provider, Traefik 3.7.17, netavark 1.14.0 and Debian pasta. Installed slirp4netns 1.2.1 for the additional case. Created the `pre-client-ip` snapshot before changing the lab; no old snapshot was restored or newer VM work discarded.

Each new Compose project ran one Traefik instance with read-only configuration and credential mounts, no-new-privileges, dropped capabilities and its own logs. A temporary HTTP server served only a dedicated test-content directory. It was bound on the guest and protected against external access by a guest firewall rule. This avoided changing the original Compose workload while permitting each network mode to reach the same backend.

Requests came from a separate machine, not guest/container loopback. TLS certificates and hostnames were verified, authenticated root requests returned the expected backend body with 200, and unauthenticated requests returned 401. Unique request paths were correlated with Traefik's access logs. The initial check used `ClientHost`, but that field can be header-derived and is not sufficient peer evidence; review follow-up checked the retained `ClientAddr` values as described below. We also supplied forged `X-Forwarded-For` and `Forwarded` headers. The unique paths could return the static backend's 404; their correlation, not a fabricated application success, established which connection was logged.

### Peer-field and trust-setting verification

Traefik 3.7.17's [access logger](https://github.com/traefik/traefik/blob/v3.7.17/pkg/middlewares/accesslog/logger.go#L279-L284) records `req.RemoteAddr` in `ClientAddr`, but may overwrite `ClientHost` with `X-Forwarded-For`. The conclusions below therefore use the host parsed from `ClientAddr`, not `ClientHost`.

Inspection of the retained forged-header request records found:

| Mode | Correlated request path | `ClientAddr` host matches the external test client | Recorded source port |
| --- | --- | --- | --- |
| Direct pasta | `/source-pasta` | Yes | 33604 |
| Source-preserving slirp | `/source-slirp` | Yes | 56510 |
| Rootful bridge | `/rootful-source` | Yes | 33344 |

The records' `ClientHost` values also matched, but are not used as independent proof. All three retained static configurations were identical and set only the listening address under each entrypoint: no `forwardedHeaders` or `proxyProtocol` override. Their retained commands selected that configuration file, their environment bindings were only `APP_HOST` and `APP_UPSTREAM`, and the retained image's environment contained only `PATH`. In the [pinned entrypoint defaults](https://github.com/traefik/traefik/blob/v3.7.17/pkg/config/static/entrypoints.go), forwarded-header trust is insecure=false with no trusted IPs and PROXY protocol is unset; the [listener code](https://github.com/traefik/traefik/blob/v3.7.17/pkg/server/server_entrypoint_tcp.go#L547-L552) activates PROXY handling only when configured. No header/PROXY override was found that could explain these peer addresses.

This verification used the original retained logs/configuration after the spike containers were removed; no replacement traffic or runtime configuration change was needed. It confirms the existing IPv4 conclusions while correcting the original description of the log field.

## Compared modes

| Mode | Real external client IP | Host firewall path | Host effects observed | Topology |
| --- | --- | --- | --- | --- |
| Named rootless bridge, prior probe | No | INPUT | No added host tables in that probe | Normal Compose network/service DNS |
| Rootless `network_mode: pasta` | **Yes** | INPUT | No added host NAT/filter tables; original policy unchanged | Direct per-container namespace, not a named Compose bridge |
| Rootless `slirp4netns:port_handler=slirp4netns,allow_host_loopback=true` | **Yes** | INPUT | No added host NAT/filter tables; original policy unchanged | Direct slirp namespace; explicit host-loopback access |
| Rootful named bridge | **Yes** | FORWARD after DNAT | Created `inet netavark`; IPv4 forwarding observed changing from 0 to 1 | Normal named Compose bridge |

The [versioned Podman 5.4.2 run manual](https://docs.podman.io/en/v5.4.2/markdown/podman-run.1.html) documents these distinctions: rootlesskit forwarding rewrites source addresses and is used for user-defined rootless networks; the slirp4netns handler preserves them but cannot serve user-defined networks; direct pasta forwarding preserves original source addresses. It describes slirp's preserving handler as slower, but this exercise did not measure performance.

The source-preserving Compose definitions used these service-level settings:

```yaml
# Case A: rootless direct pasta
network_mode: pasta

# Case B: rootless direct slirp with the preserving port handler
network_mode: slirp4netns:port_handler=slirp4netns,allow_host_loopback=true
```

These are Podman-specific network modes, not a portable promise across Docker and Podman. They are alternatives to joining a named Compose network, not options added to one. Pasta reached the protected host backend through `host.containers.internal`; slirp used its explicitly enabled host gateway. We did not prove direct-mode service-name discovery between two Compose containers or qualify unrestricted access to host services.

## Firewall enforcement

### Rootless cases

A dedicated guest INPUT rule initially dropped external TCP connections to the test ports. Both pasta and slirp connections timed out. A preceding allow for the test source restored verified TLS and application access; removing the need for a proxy header did not bypass the firewall.

Only the original lab table and the test-owned table existed after both cases started. The original lab policy remained byte-identical in its stateless representation. Both payloads had zero effective capabilities and `NoNewPrivs: 1`.

### Rootful case

The same INPUT-only drop did **not** block the rootful published port: DNAT sent the traffic through FORWARD. An Andaru-owned, earlier-priority FORWARD hook then enforced the intended policy. The decisive fixture predicate was:

```nft
meta l4proto tcp ct direction original ct original proto-dst 19445 counter drop
```

A preceding source-specific allow with the same original-direction/port match restored access, while application authentication remained required. Matching the original published port handles DNAT; limiting the predicate to original direction prevents it from also dropping response packets. This fragment is not a complete firewall ruleset.

The rootful engine created its own `inet netavark` table and enabled IPv4 forwarding. Neither is authority Andaru should silently delegate under the current single-writer policy. After Compose removed the rootful network, generic netavark chains and the forwarding change still needed explicit cleanup; `compose down` was not a full host-state rollback.

### Credential ownership correction

The first rootful payload ran as container UID 0 with all capabilities dropped, while fixture credential files and log directories belonged to host UID 1000. Without DAC override it could not read/write those resources, and TLS verification rejected Traefik's fallback certificate. Health readiness alone had not caught that defect.

The corrected rootful service used `user: "1000:1000"`, matching the file owner, while retaining dropped capabilities and no-new-privileges. Verified TLS, logs, proxying and peer-address checks then passed. No private-key permissions were loosened. This is a lab UID choice, not a production extension identity design.

## Recommendation for Andaru

1. Keep a distinction between **ordinary isolated application networking** and a **client-IP-sensitive ingress profile**. The prior bridge can still suit applications that do not depend on client IPs.
2. Use direct rootless pasta as the next ingress candidate. It passed this IPv4/TCP source-IP and INPUT-policy test without rootful host mutations. Supported backends and inter-service connectivity still need a narrowly specified model.
3. Keep source-preserving slirp as a documented alternative, not another default or performance claim. Its host-loopback option requests real additional access and must not become an implicit general privilege grant.
4. Do not adopt rootful bridge merely to recover source IPs. If later required, model DNAT/FORWARD, runtime-owned rules, global forwarding changes and teardown explicitly; an INPUT-only policy is insufficient.
5. Preserve real socket identity rather than accepting client-supplied forwarding headers. This result removes an IPv4/TCP blocker for Traefik, not the qualification work for IPv6, UDP, DNS group policies, throughput or failure recovery.

## Cleanup and retained evidence

Stopped and removed only the three spike Compose projects and the temporary backend service. After verifying there were no other rootful containers, removed only tables created by this spike, restored both forwarding sysctls to their captured values, and stopped the root Podman API socket/service that had been inactive before the test.

The original guest stateless firewall ruleset matched its baseline exactly. Its existing rootless Compose workload remained healthy with TLS/authentication, metrics, logs and its persistent marker intact. The original systemd workload and firewall services were not replaced. No Proxmox host firewall or runtime was changed.

The persistent VM, prior snapshots and `pre-client-ip` snapshot remain. VM-local fixture definitions/logs and sanitized `~/client-ip-probe/evidence.json` are retained, as is the added slirp package and cached rootful image. The baseline network/runtime state was restored; the VM disk was not reset. Snapshot rollback itself was not tested.

This was a real network exercise, not implementation of an Andaru runner or manifest compiler. It does not certify sandbox isolation, all Compose features, full-appliance recovery or an Internet-facing production topology.
