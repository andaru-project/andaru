# Traefik: native runtime probe and binding lessons

## Scope

A worked example for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), not an installable add-on or finalized manifest schema. The native Traefik process was exercised; no Andaru extension runner, generated GUI, Compose provider or systemd credential delivery was implemented.

The owner selected ordered arrays of objects for author-facing manifest collections and central log discovery. The [agreed full manifest example](traefik-manifest-example.md) supersedes the original illustrative manifest: direct property types, inline bindings, one nested `menu`, explicit localization objects, and native/container variants. Standard JSON Schema is derived internally rather than authored on every property.

## Runtime evidence

Used the official [Traefik v3.7.17 Linux amd64 release](https://github.com/traefik/traefik/releases/tag/v3.7.17), checking its archive against the release's SHA-256 checksum list. This checks artifact consistency, not a separate signing identity. Started the binary and a throwaway HTTP backend on loopback-only high ports. No Docker socket, container daemon, host firewall, privileged port, or persistent service was changed.

| Behaviour | Observed result |
| --- | --- |
| Dynamic Go templates using `env` | Host rule and upstream URL resolved from environment; authenticated request reached the backend with its expected body |
| TLS certificate file loading | A temporary self-signed certificate was explicitly trusted by the client; certificate and requested server name were verified |
| File-based HTTP credentials | No credentials returned 401; supplied test credentials returned 200 |
| Application WebUI | Authenticated `/dashboard/` request returned its HTML; this was not a browser interaction test |
| Metrics | Separate loopback endpoint returned Prometheus service-request metrics after traffic |
| Dynamic reload | Atomic replacement of a file inside the watched directory changed a response header from `one` to `two`, without restarting Traefik |
| Logs | Separate JSON application and access files were readable; access records included successful and unauthorized requests |
| Sensitive fields in exercised logs | No supplied password, password hash, private-key content, or Authorization-header fields were found |

Both processes were stopped and the temporary binary, configuration, credentials, certificates and logs removed after the exercise. The log check covers the requests exercised, not every plugin, debug mode or failure path.

Traefik's [versioned file-provider documentation](https://doc.traefik.io/traefik/v3.3/providers/file/#go-templating) distinguishes dynamic Go templating from the main static configuration. The v3.7.17 run confirmed the dynamic `env` behaviour rather than assuming it from older documentation. The [current file-provider guide](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/file/) recommends mounting/watching the parent directory because replacing an individually bind-mounted file can break reload notifications. The probe used directory watching; it did not test bind mounts.

## Current manifest design

See the [agreed full Traefik example](traefik-manifest-example.md) for the current design and its limits. The original top-level property/schema/input sketch is no longer the author-facing format; it remains available in [the historical revision](https://github.com/andaru-project/andaru/blob/58bd843/docs/research/traefik-manifest-spike.md). Runtime observations above and in the binding notes remain evidence from the original probe, not a claim that the replacement manifest was executed.

Ephemeral checks of the original sketch parsed its named collections and checked unique names. A settings-schema check derived standard JSON Schema, validated sample settings and rejected missing required fields. It also exposed a validation limit: the local Python validator accepted `bad` followed by a backtick and `host` for `format: hostname`. Do not assume a schema's format annotation is enforced; qualify the production validator's format support and explicit semantic checks before allowing values into routing templates.

## Binding responsibilities

- **Environment values:** the packaged dynamic routing template uses Traefik's `env` function for the hostname and upstream. These are validated settings, not arbitrary user-authored template source. Template substitutions must produce valid routing rules/URLs; Go templates do not provide automatic YAML or routing-expression escaping. Format checks alone do not authorize arbitrary network access.

  **The environment-bound `/hostname`, `/dashboardHostname` and `/upstreamUrl` settings require a replacement process with updated environment, or container recreation for the container variant.** Watching/replacing a dynamic file does not change the running process's environment. [Docker Compose explicitly documents](https://docs.docker.com/reference/cli/docker/compose/restart/) that `compose restart` does not apply environment changes. The observed reload changed a literal response header in the file, not these environment values. To make routing-setting edits reloadable instead, render their validated values into the watched file rather than supplying them through environment variables; that is an alternative binding, not behaviour proved by this probe.
- **Credential files:** the packaged configuration points `basicAuth.usersFile` at an engine-assigned runtime file. The probe supplied an htpasswd record manually; creating records, selecting an assigned shared secret, rotation and Andaru/systemd delivery remain design work. Never return the record through the settings API or configuration export.
- **Certificates:** the selected reference resolves to certificate/key files accessible only to the permitted runtime. The packaged template uses `certFile` and `keyFile`; the GUI sees the reference and public metadata, not key material. Certificate rotation was not tested.
- **Log files:** this original probe wrote application and access output to two files. The later [native systemd spike](native-systemd-spike.md) exercised journal application output; the [current manifest](traefik-manifest-example.md) proposes journal/container-stream application output plus a separate access file. Container-stream collection is not qualified by the file-log Compose probe. Manifest paths are relative to declared storage, not arbitrary host paths. The central viewer can show Logs → Add-ons → Traefik → Application / Access. Backend authorization, safe file opening, cursor/rotation handling and bounded reads are required but not implemented by this example.
- **Metrics and WebUI:** an endpoint declaration requests a resource; it does not publish a host port or grant WAN access. The metrics collector uses the private telemetry endpoint. On the shared HTTPS listener, the proposed `/dashboardHostname` value supplies both `APP_DASHBOARD_HOST` for the dashboard router's `Host` rule and the WebUI link's `hostnameSetting`; the certificate must cover that hostname. The probe used `dashboard.andaru.test` explicitly, distinct from `app.andaru.test`. Declaring a link does not configure Traefik by itself. Andaru never forwards its own administrator cookie/token to the dashboard.

## What this changes in the design

1. A small `.env` interface is useful, but it is not a complete reverse-proxy model. Multiple routers, upstreams and middlewares need structured array settings and typed file generation/bindings, not a giant free-form text field or a growing expression language.
2. Classify the chosen binding, not merely the Traefik feature: these environment-bound routing settings require process replacement/container recreation even though the file provider is reloadable. Literal validated routing values written into the watched file can use the reload path; install/static settings still require restart as appropriate.
3. One application can expose several log streams and a separate private metrics endpoint. Keep their discovery and permissions explicit rather than assuming logs live in one global directory or metrics share the public application listener.
4. Start with the file provider, no Docker-socket discovery, and explicit listeners. Container-provider access is a separate privilege and networking decision.
5. No ACME account was created and no certificate was obtained over the Internet. If ACME is later enabled, its state contains private material and belongs under secret-aware backup/recovery policy, not ordinary unencrypted application-data export.

## Remaining qualification

Compose/Podman/Docker compatibility, rootless operation, systemd confinement/credentials, actual certificate rotation, log rotation, Andaru-generated forms/API validation, and offline image/state recovery are not proven by this native-binary probe. It establishes that the proposed bindings have real Traefik counterparts; it does not establish a production-ready Andaru integration.

A subsequent [Debian VM Podman/Compose spike](podman-compose-spike.md) exercises a rootless Compose subset, file secrets, systemd lifecycle, guest firewall interaction and offline image round trips. It also finds health-check argument and original-client-IP limitations. Those results qualify only the versions and topology documented there, not the full Andaru integration.
