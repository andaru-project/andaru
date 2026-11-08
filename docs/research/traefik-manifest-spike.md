# Traefik: an exercised add-on manifest sketch

## Scope

A worked example for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), not an installable add-on or finalized manifest schema. The native Traefik process was exercised; no Andaru extension runner, generated GUI, Compose provider or systemd credential delivery was implemented.

The owner selected ordered arrays of objects for author-facing manifest collections and central log discovery. This example applies those decisions to Traefik. Standard JSON Schema is derived from property definitions; JSON Schema's own `properties` keyword remains an object.

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

## Illustrative manifest

The following defines the needed integration metadata, not a runnable package. Field names and API compatibility versions are proposed. The referenced workload and template files would be supplied by an actual packaged add-on.

```json
{
  "manifestVersion": 1,
  "id": "org.andaru.traefik",
  "name": "Traefik",
  "version": "3.7.17",
  "requires": [
    {
      "name": "andaru.extension-api",
      "version": ">=1.0.0 <2.0.0"
    }
  ],
  "runtime": {
    "type": "compose",
    "file": "compose.yaml"
  },
  "properties": [
    {
      "name": "hostname",
      "schema": {
        "type": "string",
        "format": "hostname",
        "title": "Public hostname"
      },
      "required": true
    },
    {
      "name": "upstreamUrl",
      "schema": {
        "type": "string",
        "format": "uri",
        "title": "Upstream service"
      },
      "required": true
    },
    {
      "name": "dashboardHostname",
      "schema": {
        "type": "string",
        "format": "hostname",
        "title": "Dashboard hostname"
      },
      "required": true
    },
    {
      "name": "certificate",
      "schema": {
        "type": "string",
        "title": "Certificate reference"
      },
      "required": true
    }
  ],
  "inputs": [
    {
      "name": "APP_HOST",
      "service": "traefik",
      "setting": "/hostname"
    },
    {
      "name": "APP_DASHBOARD_HOST",
      "service": "traefik",
      "setting": "/dashboardHostname"
    },
    {
      "name": "APP_UPSTREAM",
      "service": "traefik",
      "setting": "/upstreamUrl"
    }
  ],
  "secrets": [
    {
      "name": "httpAuth",
      "title": "HTTP authentication credentials",
      "delivery": "file"
    }
  ],
  "certificates": [
    {
      "name": "tls",
      "referenceSetting": "/certificate"
    }
  ],
  "storage": [
    {
      "name": "logs",
      "purpose": "logs",
      "backup": false
    }
  ],
  "endpoints": [
    {
      "name": "websecure",
      "service": "traefik",
      "port": 8443,
      "protocol": "https"
    },
    {
      "name": "telemetry",
      "service": "traefik",
      "port": 9080,
      "protocol": "http",
      "exposure": "internal"
    }
  ],
  "logs": [
    {
      "name": "application",
      "title": "Traefik application",
      "source": {
        "type": "file",
        "storage": "logs",
        "path": "traefik.jsonl"
      },
      "format": "json-lines"
    },
    {
      "name": "access",
      "title": "Traefik access",
      "source": {
        "type": "file",
        "storage": "logs",
        "path": "access.jsonl"
      },
      "format": "json-lines"
    }
  ],
  "metrics": [
    {
      "name": "application",
      "endpoint": "telemetry",
      "path": "/metrics",
      "format": "prometheus"
    }
  ],
  "webui": {
    "endpoint": "websecure",
    "hostnameSetting": "/dashboardHostname",
    "path": "/dashboard/"
  }
}
```

The singleton `runtime` and `webui` are objects; named collections are arrays. Array order is presentation order and names must be unique within their collection. Scalars, singleton descriptors, and object-valued settings are not converted into arrays merely because they contain JSON objects.

`manifestVersion` identifies the manifest format and `version` the add-on release. The separate `requires` entry declares the supported Andaru extension/API contract. Its name and range are illustrative, not a claim that API 1.0.0 exists. Compatibility preflight must enforce it before installation or core activation, retaining the approved rule that an incompatible installed extension blocks the core upgrade.

Ephemeral document checks parsed the named collections and checked unique names. A settings-schema check derived standard JSON Schema, validated sample settings and rejected missing required fields. It also exposed a validation limit: the local Python validator accepted `bad` followed by a backtick and `host` for `format: hostname`. Do not assume a schema's format annotation is enforced; qualify the production validator's format support and explicit semantic checks before allowing values into routing templates.

## Binding responsibilities

- **Environment values:** the packaged dynamic routing template uses Traefik's `env` function for the hostname and upstream. These are validated settings, not arbitrary user-authored template source. Template substitutions must produce valid routing rules/URLs; Go templates do not provide automatic YAML or routing-expression escaping. Format checks alone do not authorize arbitrary network access.

  **The environment-bound `/hostname`, `/dashboardHostname` and `/upstreamUrl` settings require a replacement process with updated environment, or container recreation for this Compose-backed sketch.** Watching/replacing a dynamic file does not change the running process's environment. [Docker Compose explicitly documents](https://docs.docker.com/reference/cli/docker/compose/restart/) that `compose restart` does not apply environment changes. The observed reload changed a literal response header in the file, not these environment values. To make routing-setting edits reloadable instead, render their validated values into the watched file rather than supplying them through environment variables; that is an alternative binding, not behaviour proved by this sketch.
- **Credential files:** the packaged configuration points `basicAuth.usersFile` at an engine-assigned runtime file. The probe supplied an htpasswd record manually; creating records, selecting an assigned shared secret, rotation and Andaru/systemd delivery remain design work. Never return the record through the settings API or configuration export.
- **Certificates:** the selected reference resolves to certificate/key files accessible only to the permitted runtime. The packaged template uses `certFile` and `keyFile`; the GUI sees the reference and public metadata, not key material. Certificate rotation was not tested.
- **Log files:** the application configuration writes to two files under the engine-assigned logs directory. Manifest paths are relative to declared storage, not arbitrary host paths. The central viewer can show Logs → Add-ons → Traefik → Application / Access. Backend authorization, safe file opening, cursor/rotation handling and bounded reads are required but not implemented by this example.
- **Metrics and WebUI:** an endpoint declaration requests a resource; it does not publish a host port or grant WAN access. The metrics collector uses the private telemetry endpoint. On the shared HTTPS listener, the proposed `/dashboardHostname` value supplies both `APP_DASHBOARD_HOST` for the dashboard router's `Host` rule and the WebUI link's `hostnameSetting`; the certificate must cover that hostname. The probe used `dashboard.andaru.test` explicitly, distinct from `app.andaru.test`. Declaring a link does not configure Traefik by itself. Andaru never forwards its own administrator cookie/token to the dashboard.

## What this changes in the design

1. A small `.env` interface is useful, but it is not a complete reverse-proxy model. Multiple routers, upstreams and middlewares need structured array settings and typed file generation/bindings, not a giant free-form text field or a growing expression language.
2. Classify the chosen binding, not merely the Traefik feature: these environment-bound routing settings require process replacement/container recreation even though the file provider is reloadable. Literal validated routing values written into the watched file can use the reload path; install/static settings still require restart as appropriate.
3. One application can expose several log streams and a separate private metrics endpoint. Keep their discovery and permissions explicit rather than assuming logs live in one global directory or metrics share the public application listener.
4. Start with the file provider, no Docker-socket discovery, and explicit listeners. Container-provider access is a separate privilege and networking decision.
5. No ACME account was created and no certificate was obtained over the Internet. If ACME is later enabled, its state contains private material and belongs under secret-aware backup/recovery policy, not ordinary unencrypted application-data export.

## Remaining qualification

Compose/Podman/Docker compatibility, rootless operation, systemd confinement/credentials, actual certificate rotation, log rotation, Andaru-generated forms/API validation, and offline image/state recovery are not proven by this native-binary probe. It establishes that the proposed bindings have real Traefik counterparts; it does not establish a production-ready Andaru integration.
