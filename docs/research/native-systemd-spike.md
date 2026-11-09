# Native add-ons: Traefik under systemd

## Conclusion

**A native Traefik add-on can run as a dedicated non-root service with systemd-delivered certificates/secrets, journald integration, and versioned offline replacement.** Containers are not required for these integration features. This complements the [Compose](podman-compose-spike.md) and [pasta ingress](container-client-ip-spike.md) examples rather than replacing either runtime.

This is a bounded VM spike for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), not an Andaru runner, approved manifest schema, package installer or production recovery controller.

## Environment and artifact provenance

Used the retained Debian 13.7 VM 114 `andaru-lab` on `proxmox-4`, with systemd `257.13-1~deb13u1`. Created `pre-native-systemd` before changes and retained `native-qualified` afterward. The VM was not reset.

Downloaded the official amd64 [Traefik 3.7.16](https://github.com/traefik/traefik/releases/tag/v3.7.16) and [3.7.17](https://github.com/traefik/traefik/releases/tag/v3.7.17) archives and checked their release SHA-256 lists before extraction. This verifies consistency with those release artifacts, not a separately authenticated signing identity. Both extracted ELFs were statically linked; do not generalize that to every Go build.

Root-owned release directories under `/var/lib/andaru-native-probe/releases/<version>/` held each executable, configuration and root-only credential sources. Extracted binary hashes matched after transfer to the VM:

| Version | Binary SHA-256 |
| --- | --- |
| 3.7.16 | `86ee2b427888d24475051fc5f5bcca94cf4fa033b6fa54205b7eb0de6914f31e` |
| 3.7.17 | `5c37f0d8dc4a29648e2daf57ed21665dc238f352801323e7c5416aec66dfee71` |

The native service did not call Podman or Docker. A separate temporary native HTTP backend served fixed test content on loopback. HTTPS and telemetry also bound loopback high ports; this was not an external-ingress or port 80/443 test.

## Service identity and confinement

The payload unit used `User=andaru-native-probe` (UID 999 in this lab), `Type=exec`, no-new-privileges, empty capability sets, a system-service syscall filter, restricted address families, strict read-only system protection, hidden home directories, private temporary/device views and `LimitCORE=0`. Assigned state/runtime directories and private temporary storage were available for application writes.

Live `/proc` checks confirmed the expected non-root UID, zero effective capabilities, `NoNewPrivs: 1` and active seccomp. A pre-start check could read its delivered credentials but could not open them for writing, read the root-only source key, or open the release-selection record for writing. These are focused checks, not a sandbox escape audit or proof that every public host file is hidden.

`Type=exec` is not application readiness. A bounded `ExecStartPost` probe checked the selected TLS hostname/certificate, an HTTP authentication boundary and `/ping`. The independent test client additionally checked authenticated backend content, dashboard/API version, access logs and Prometheus service-request metrics. The startup probe alone did not exercise every upstream or permission path.

## Plain and encrypted credential delivery

The tested directives had this shape:

```ini
User=andaru-native-probe
LoadCredential=tls.crt:/var/lib/andaru-native-probe/control/current/sources/tls.crt
LoadCredential=tls.key:/var/lib/andaru-native-probe/control/current/sources/tls.key
LoadCredentialEncrypted=auth-users:/var/lib/andaru-native-probe/control/current/sources/auth-users.cred
```

The dynamic file used Traefik's `env` function to locate files under `CREDENTIALS_DIRECTORY`; no secret value was configured in the unit's environment. Verified TLS and successful/denied Basic-auth requests exercised both delivery paths. Source files were root-only, while the service received only its three named credentials.

Inspection inside the service mount namespace found a credential tmpfs mounted `ro,nosuid,nodev,noexec,...,noswap`. This agrees with [systemd 257's credential contract](https://www.freedesktop.org/software/systemd/man/257/systemd.exec.html): per-unit read-only files accessible to the unit's UID and root, using non-swappable memory when supported. Unique service identities still matter; this does not hide secrets from root or mutually isolate processes sharing one UID.

The authentication blob was generated with `systemd-creds encrypt --with-key=host --name=auth-users`. No TPM binding was tested. The tool warned that the host credential key was on unencrypted media: encrypting a blob with a key on that same disk is not whole-disk protection. Host-bound credential blobs are delivery artifacts, not a substitute for Andaru's portable, password-encrypted backup. Recovery on a new host must restore suitable decryption material or regenerate blobs from its restored secret store; cross-host credential portability was not tested.

## Configuration, logs and activation

| Check | Observed result |
| --- | --- |
| Valid dynamic file replacement | Response header changed from `one` to `two`; payload PID unchanged |
| Malformed dynamic YAML | Application journal reported an error; previous good routing stayed active with the same PID |
| Unit environment change | `daemon-reload` alone left the old hostname active; service restart activated the new one, then the original was restored |
| Unknown top-level static key | A top-level `invalidProbeField: true` was accepted at startup, not rejected; this does not describe validation inside known sections |
| Malformed static YAML | Startup failed and the bounded offline fallback path recovered the earlier release |
| CLI validation surface | The tested `--help` listed `version` and `healthcheck`, not a general `check-config` command |

Consequently, **active service/ping is not proof a proposed configuration was applied successfully**. Andaru must validate its own declared settings, control rendering, surface application errors and verify intended state. The malformed dynamic file was restored by the test driver; keeping last-good runtime state does not make the invalid file on disk safe for a future restart.

Application JSON and credential/readiness markers were discoverable using the journal's unit metadata. Access logs were a separate protected file in the service state directory. Metrics were available on a loopback endpoint. These map naturally to manifest declarations for a journal unit, relative owned log file and internal metrics endpoint; no Andaru log viewer or authorization API was implemented.

The supplied password/hash and private-key content were absent from the exercised journal records, and access-log Authorization headers were omitted. This does not certify every application/debug/error path against disclosure. Optional Traefik version/usage checks were explicitly disabled. Other application security defaults, including header-alias warnings, were not comprehensively qualified.

## Offline replacement and bounded automatic fallback

Two real executables remained local. A root-controlled selection record and symlink selected the complete release directory. These were lab fixtures, not a general transactional deployment format.

The worker had bounded `Restart=on-failure` behaviour (two starts in a 30-second lab window) and `OnFailure=` selected a separate, trusted root one-shot. After the failed trial exhausted those attempts, that helper selected the confirmed release, cleared the trial, reset failure state and requested a new worker start. Selection happened **before that new start loaded credentials**, avoiding a new-binary/old-credential ordering mistake. The root helper was operator/platform code, not extension-supplied privileged callbacks.

With new non-loopback egress rejected by a temporary guest output rule, a direct external connection failed. The following completed locally across two invocations: step 1 in the preceding invocation, and steps 2–3 in an edited, faults-only invocation. There was no single uninterrupted end-to-end run; the retained evidence JSON summarizes, rather than captures, the earlier successful replacement.

1. Replaced 3.7.16 with the actual 3.7.17 binary/configuration, verified its API version and authenticated route, and confirmed it through the test driver.
2. Re-established 3.7.16, then tried 3.7.17 with syntactically malformed static YAML. Startup failed; the root recovery helper automatically selected 3.7.16 and healthy routing returned.
3. Tried 3.7.17 with an encrypted credential carrying the wrong embedded name. Credential setup failed with `243/CREDENTIALS`, before normal payload startup. The same helper automatically recovered 3.7.16 with its own sources.

The first fault-injection attempt used an unknown static field, which Traefik accepted; that was recorded as a validation limit, not a successful failure test. The corrected malformed-YAML and credential-failure cases were then exercised without repeating the already successful replacement. Journal events corroborated parser/credential failures and both fallback selections; the final live executable and API version were 3.7.16. Candidate files were restored afterward, and the egress restriction was removed.

Credential values were unchanged across the two release copies, so this does not prove rollback of changed secret values. There were no Andaru database migrations or stateful extension schemas. It does not prove full-appliance repair, offline reinstallation of Python/systemd, power-loss-safe selection, boot watchdogs, failed recovery of the old release, or package-manager rollback. A restart limit is not a general watchdog for later failures spaced beyond its accounting window. Production selection/state reconciliation and health thresholds remain design work.

## Retained state and design consequence

Both native units are now inactive and not boot-enabled; the temporary backend stopped. The original Compose lab remains healthy with its persistent marker. The entire guest stateless firewall and both forwarding sysctls matched the original baseline. No Proxmox host runtime/firewall was changed.

The VM, snapshots, dedicated test user, root-owned release/credential fixtures, journal/access logs and private `~/native-systemd-probe/evidence.json` remain for future investigation. The lab retains plaintext authentication source material alongside its encrypted delivery blob; neither these test credentials nor certificates define a production storage or lifetime policy.

The evidence supports **native Traefik as a reference add-on alongside the Compose variant**: common settings, permissions, secrets/certificate references, logs, metrics and recovery ownership; different artifact/lifecycle execution. Keep runtime declarations typed and platform-owned, not arbitrary root unit overrides.

OS-package-managed NGINX/Squid/HAProxy, interpreter dependency isolation, low-port grants/socket activation, credential/certificate rotation, Fedora SELinux/immutable installation and the actual manifest/UI implementation remain unqualified. These results justify drafting the native contract, not declaring all native packages supported.
