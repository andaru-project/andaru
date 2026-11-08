# Podman Compose: Debian VM qualification

## Conclusion

**Podman can run this Compose application, but the exact provider and supported features matter.** Rootless Podman 5.4.2 with explicitly selected Docker Compose 2.26.1 ran Traefik 3.7.17 and a Python HTTP backend successfully after changing the backend health check from inline source to a script file. Docker Engine was not installed or used.

This is a bounded runtime spike for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), extending the [native Traefik example](traefik-manifest-spike.md). It does not approve all Compose features or settle Andaru's container networking model. In particular, this rootless bridge did **not preserve the external client's IP address**, which matters for reverse-proxy ACLs, logging and future per-client DNS policy.

The follow-up [client-IP networking spike](container-client-ip-spike.md) distinguishes that named-bridge result from direct rootless pasta and source-preserving slirp. Both direct modes preserved IPv4 client addresses under an INPUT-policy check; rootful bridge also preserved them but required FORWARD filtering and changed host network state. These are bounded alternatives, not a claim that the original bridge now preserves addresses.

## Retained test environment

At the owner's request, the VM is a persistent development lab, not disposable:

| Item | Observed value |
| --- | --- |
| Hypervisor | Proxmox VE 9.2.26; node `proxmox-4`, VM 114 `andaru-lab` |
| Guest | Debian 13.7 genericcloud amd64; kernel `6.12.115+deb13.1-cloud-amd64` |
| Resources | 2 vCPUs, 4 GiB RAM, 32 GiB disk on snapshot-capable `local-zfs` |
| Runtime | Debian `podman 5.4.2+ds1-2+b2`, rootless, systemd cgroups |
| Providers tested | `podman-compose 1.3.0-1`; `docker-compose 2.26.1-4` |
| Network tools | netavark 1.14.0, aardvark-dns 1.14.0, pasta from Debian's passt package |
| Images | Traefik 3.7.17 and Python 3.13 Alpine; initial pulls pinned to resolved registry references |

The Debian image was downloaded through Proxmox with SHA-512 verification against the published [cloud-image checksum list](https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS). Its checksum was `d25999de0770d4cde1be72c299ba3ad94eac7ea8fd4d18f8cc86c69df69337e2e8a91d64465a9bfd8f2a78c566fcbcb0842eacc91c09efdef72d719d9f13b535`; the `latest` URL is moving, so the checksum identifies the tested image.

Retained disk snapshots:

- `clean-debian`: cloud-init and SSH configured, before Podman installation; cleanly shut down for the snapshot.
- `podman-ready`: Podman, Python Compose provider, network tools and guest agent installed; before workload fixtures. This snapshot predates the Docker Compose provider and later corrections.
- `compose-qualified`: Docker Compose provider, working fixture, retained offline image archives, systemd units and the guest firewall boundary; cleanly shut down for the snapshot.

The VM was started again after the final snapshot. Its fixtures are under the lab user's `~/andaru-podman-probe`; SSH access details are kept in the local lab inventory, not this public report. Snapshot creation succeeded, but snapshot rollback itself was not exercised. Restoring one would discard newer VM changes and must be deliberate.

## Workload and boundaries

The Compose definition runs a Python backend and Traefik on an application bridge. The backend has a named data volume containing a marker. Traefik receives environment settings, a read-only watched directory, a read-only install/static configuration, file-based TLS/authentication secrets, and a named log volume. Both containers drop capabilities and set no-new-privileges.

The test explicitly selects the provider:

```sh
export XDG_RUNTIME_DIR=/run/user/$(id -u)
export DBUS_SESSION_BUS_ADDRESS=unix:path=$XDG_RUNTIME_DIR/bus
export PODMAN_COMPOSE_PROVIDER=/usr/bin/docker-compose
systemctl --user start dbus.socket podman.socket
podman compose -p andaru_probe up -d --wait --wait-timeout 45
```

A minimal non-interactive guest session initially lacked a usable user bus, causing Podman to fall back to cgroupfs. Starting the user bus and setting its address yielded rootless systemd cgroups. The lab enables user lingering so the rootless services can start without an interactive login. This setup requirement must be explicit in a future runner.

[Podman documents](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) that its Compose command delegates to an external provider; installing more than one must not accidentally select Andaru's behaviour. The provider uses the rootless Podman API socket on the host. Neither container receives that socket or access to a Docker daemon.

## Provider limitations actually observed

An initial exec-form backend health check was:

```json
["CMD", "python", "-c", "import urllib.request; assert urllib.request.urlopen('http://127.0.0.1:8080/health').status == 200"]
```

- With `podman-compose` 1.3.0, inspection showed it had become a `CMD-SHELL` command with broken shell quoting. It failed with a shell syntax error, and startup did not complete within the test's 120-second limit.
- With Docker Compose 2.26.1 against Podman, inspection showed the Python source string had been split into multiple `CMD` arguments. The interpreter received only `import` as its source and the backend became unhealthy. The provider refused to start the dependent Traefik service.
- Executing the backend request directly returned 200: the application was not the cause. Moving exactly the same request into a mounted `healthcheck.py` and using `["CMD", "python", "/srv/healthcheck.py"]` allowed both services to become healthy under the Docker Compose provider.

**This is a tested restriction, not a repaired compatibility bug.** The successful fixture avoids inline arguments containing embedded spaces; it does not establish general exec-form health-check equivalence. The precise upstream fix/version was not investigated. The adjusted fixture was not requalified under the Python provider.

## Successful behaviour checks

| Check | Evidence |
| --- | --- |
| Compose networking and service discovery | Traefik reached `http://backend:8080` by service name and returned the backend's expected JSON |
| TLS and credentials | Explicitly trusted test certificate passed hostname verification; no credentials returned 401 and valid credentials returned 200 |
| Dashboard | Authenticated dashboard HTML returned 200 on its distinct hostname |
| Secrets and configuration mounts | Inspection showed read-only credential and dynamic-directory mounts; values were not injected as plaintext environment secrets |
| Runtime privilege | Neither container was privileged; `/proc/1/status` showed zero effective/bounding capabilities and `NoNewPrivs: 1` |
| Logs | Separate JSON application/access files contained successful and denied requests; exercised logs contained no supplied password/hash, private key or Authorization-header fields |
| Metrics | A loopback-only telemetry port returned Prometheus service-request metrics; an external connection to that port failed |
| Watched directory reload | Atomic replacement of a file in the mounted directory changed a response header without container recreation |
| Environment changes | Editing `.env` then `compose restart` kept the old hostname and container identity; recreation activated the new hostname and stopped routing the old one |
| Persistence | Backend recreation retained the named-volume marker |
| systemd lifecycle and boot | A user unit performed Compose startup/shutdown; user lingering and the Podman socket allowed restart and subsequent VM boot to restore healthy services and the marker |

Compose file secrets in this fixture were read-only file mounts: `podman secret ls` was empty. This is not proof of native Podman secret storage, systemd credential delivery, per-extension ownership, secret rotation or revocation. Likewise, the observed log result does not certify every application/debug failure path against disclosure.

### Firewall and client identity

A dedicated guest-only nftables input rule dropped external connections to the published application port. A real external connection timed out. Adding an explicit allow for the test source restored TLS reachability, still returning 401 without application credentials. The private metrics port remained bound to guest loopback.

The guest's stateless ruleset was byte-identical across the tested Compose recreations, offline recovery and user-service restart. The lab's own boot-time firewall unit restored the same policy before networking on a subsequent boot. This does not mean Podman never writes firewall rules: the result applies to this **rootless** topology, not rootful bridge networking, IPv6 policy, or a complete router dataplane.

An external request with a unique path was then correlated to its access-log entry. A later inspection of that retained record confirmed that the peer field `ClientAddr` contained the rootless bridge address `10.89.0.3`, not the external client's address (`ClientHost` matched too, but can be header-derived). Do not use this topology for source-IP-based controls without a separately tested client-identity solution. Accepting untrusted forwarded headers is not a solution.

## Offline image recovery

New non-loopback egress was rejected by a temporary guest nftables output chain, while loopback traffic and established management remained available. A direct connection to an external IP failed. In this condition:

1. Removing/recreating the workload from cached images succeeded with `pull_policy: never`; its volume marker remained.
2. Both workload images were removed from local image storage while their archives and data volumes were retained.
3. Loading the original OCI archives failed: they had been exported under registry-digest references, but the archived manifest no longer matched the digest embedded in the destination name (`Digest of source image's manifest would not match destination reference`).
4. With egress still blocked, `skopeo` copied those local archives to explicit local image names. Recovered image IDs matched the recorded originals. Exporting under those local names produced archives that passed actual `podman load` round trips after another image removal.
5. Compose was rebound to those controlled local names, restarted without downloads, and both actual container image IDs were checked against the recorded originals. Routing and the volume marker still passed. The output restriction was then removed.

The normal corrected archive round trip used `podman save/load`. Skopeo 1.18.0 was installed before the second offline trial to repair the initially misnamed archives; it was not downloaded during recovery. It need not be the production restore mechanism if exports use tested, controlled local names from the start.

**Recovery metadata must preserve both provenance and local identity:** registry/platform reference, expected image ID, archive checksum and a controlled local alias. A mutable tag alone is insufficient, but a registry digest is also not automatically a loadable local archive name. Verify an actual export/import round trip before marking a recovery set usable.

This proves workload image recovery with existing runtime packages and retained volumes, not recovery from an empty disk, reinstalling Podman offline, restoring arbitrary extension data or repairing a complete Andaru installation. Those remain required future qualification.

## Recommendation and retained state

Use an explicitly pinned Docker Compose provider against rootless Podman as the next runner candidate for ordinary applications, with a documented tested feature subset. Do not yet promise Docker equivalence or approve this bridge mode for client-IP-dependent proxy/DNS workloads. Container networking and health-check argument fidelity need decisions before a production Traefik add-on is claimed.

The persistent VM, rootless workload, its private test credentials, local image archives, evidence files and snapshot baselines remain available for further Andaru tests. A guest-only firewall fixture limits the published test port to the test runner; no Proxmox host firewall or runtime was changed. These are lab fixtures, not a product installer, runner or public management service.
