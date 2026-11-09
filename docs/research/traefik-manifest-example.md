# Traefik: agreed manifest example

## Status and scope

Recorded following owner approval of the example and subsequent corrections. This is the agreed author-facing design example for [Andaru #13](https://github.com/andaru-project/andaru/issues/13), not a finalized manifest schema, installable package or implemented Andaru runner. The referenced package files are proposed interfaces, not files shipped by this document. Manifest/API versions and the `andaru.services` menu identifier are illustrative, not existing compatibility guarantees.

The example describes one HTTPS reverse-proxy route and a separately authenticated dashboard. Its hostnames are Traefik application settings; native add-ons do not inherently require a hostname, listening port or WebUI. The integration package version is distinct from the upstream application version. Andaru assigns instance identity separately from either version or the selected runtime.

The [original Traefik probe](traefik-manifest-spike.md), [Podman/Compose qualification](podman-compose-spike.md) and [native systemd qualification](native-systemd-spike.md) provide bounded runtime evidence. None exercised this complete manifest or generated GUI.

## Full example

```json
{
  "manifestVersion": 1,
  "id": "org.andaru.traefik",
  "name": "Traefik",
  "version": "1.0.0",
  "application": {
    "name": "Traefik",
    "version": "3.7.17"
  },
  "requires": [
    {
      "name": "andaru.extension-api",
      "version": ">=1.0.0 <2.0.0"
    }
  ],
  "permissions": [
    { "name": "view", "title": "View configuration and status" },
    { "name": "configure", "title": "Edit configuration" },
    { "name": "apply", "title": "Apply configuration" },
    { "name": "manage", "title": "Start, stop and restart Traefik" },
    { "name": "logs", "title": "Read logs" },
    { "name": "metrics", "title": "Read metrics" }
  ],
  "menu": {
    "parent": "andaru.services",
    "id": "traefik",
    "title": "Traefik",
    "permissions": {
      "view": "view",
      "edit": "configure",
      "apply": "apply",
      "manage": "manage"
    },
    "properties": [
      {
        "name": "dashboardHostname",
        "type": "string",
        "format": "hostname",
        "required": true,
        "title": { "i18n": "settings.dashboardHostname.title" },
        "binding": {
          "type": "environment",
          "name": "APP_DASHBOARD_HOST"
        }
      },
      {
        "name": "certificate",
        "type": "certificate-reference",
        "required": true,
        "title": "TLS certificate",
        "binding": {
          "type": "certificate-files",
          "name": "tls"
        }
      },
      {
        "name": "dashboardCredentials",
        "type": "secret-reference",
        "required": true,
        "title": "Dashboard credentials",
        "binding": {
          "type": "file",
          "name": "dashboardAuth",
          "format": "htpasswd"
        }
      },
      {
        "name": "logLevel",
        "type": "enum",
        "title": { "i18n": "settings.logLevel.title" },
        "options": [
          { "value": "DEBUG", "title": { "i18n": "logLevels.debug" } },
          { "value": "INFO", "title": { "i18n": "logLevels.info" } },
          { "value": "WARN", "title": { "i18n": "logLevels.warning" } },
          { "value": "ERROR", "title": { "i18n": "logLevels.error" } }
        ],
        "minItems": 1,
        "maxItems": 1,
        "default": ["INFO"],
        "ui": {
          "control": "select"
        },
        "binding": {
          "type": "argument",
          "name": "--log.level"
        }
      }
    ],
    "children": [
      {
        "id": "proxy",
        "title": { "i18n": "pages.proxy.title" },
        "permissions": {
          "view": "view",
          "edit": "configure",
          "apply": "apply"
        },
        "properties": [
          {
            "name": "hostname",
            "type": "string",
            "format": "hostname",
            "required": true,
            "title": "Public hostname",
            "binding": {
              "type": "environment",
              "name": "APP_HOST"
            }
          },
          {
            "name": "upstreamUrl",
            "type": "string",
            "format": "uri",
            "pattern": "^https?://",
            "required": true,
            "title": "Upstream service",
            "binding": {
              "type": "environment",
              "name": "APP_UPSTREAM"
            }
          }
        ]
      }
    ]
  },
  "storage": [
    {
      "name": "logs",
      "purpose": "logs",
      "backup": false
    }
  ],
  "endpoints": [
    {
      "name": "https",
      "protocol": "https",
      "port": 8443,
      "exposure": "managed"
    },
    {
      "name": "telemetry",
      "protocol": "http",
      "port": 9080,
      "exposure": "internal"
    }
  ],
  "runtimes": [
    {
      "name": "native",
      "type": "native",
      "executable": "native/traefik",
      "identity": "dedicated-user",
      "settingsActivation": "restart",
      "applicationLog": { "type": "journal" }
    },
    {
      "name": "container",
      "type": "container",
      "provider": "compose",
      "file": "container/compose.yaml",
      "service": "traefik",
      "settingsActivation": "recreate",
      "applicationLog": { "type": "container" }
    }
  ],
  "logs": [
    {
      "name": "application",
      "title": "Application",
      "permission": "logs",
      "source": { "type": "runtime" },
      "format": "json-lines"
    },
    {
      "name": "access",
      "title": "Access",
      "permission": "logs",
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
      "title": "Traefik",
      "permission": "metrics",
      "endpoint": "telemetry",
      "path": "/metrics",
      "format": "prometheus"
    }
  ],
  "health": {
    "endpoint": "telemetry",
    "path": "/ping",
    "expectedStatus": 200
  },
  "webui": {
    "title": "Open Traefik dashboard",
    "permission": "view",
    "endpoint": "https",
    "hostnameSetting": "/dashboardHostname",
    "path": "/dashboard/"
  },
  "recovery": {
    "retainPreviousRuntime": true,
    "requiresInternet": false
  }
}
```

## Recorded decisions

### Identity, menus and localization

One add-on has one root `menu`, attached to an existing top-level Andaru page through `parent`. The root is itself a page. Nested `children` contain the page definitions themselves; there is no separate navigation tree, flat page registry or root `items` collection. Only the root declares `parent`. Array order controls sibling and property presentation. Page IDs are unique across the add-on; named collections reject duplicate names. The manifest parser must reject duplicate JSON object keys rather than silently keeping the last value.

`manifestVersion` identifies the manifest format; `version` identifies the integration release. Named collections are arrays; singleton descriptors and fixed-key maps, such as page action-to-permission mappings, are objects. Standard formats embedded or derived internally keep their own object/array conventions. Enforce `requires` before installation or activation, not only after launching a runtime.

Each page explicitly binds its view/edit/apply operations to declared permissions; there is no implicit inheritance or public-access default. The root's `manage` mapping authorizes add-on lifecycle operations. Reject missing access mappings or undeclared permission references; backend operations fail closed.

Property `name` is the stable stored-setting identifier, unique across this example's add-on and independent of page placement or translated titles. `/hostname` continues to identify the same setting if its property moves to another page. Grouping nested object-valued settings needs its own definition before implementation; this example uses flat setting names.

Every human-facing title, label, description and help text accepts either literal English or exactly `{ "i18n": "non-empty.key" }`, with no additional object fields. Option titles follow the same rule. No reserved string prefixes or escaping conventions apply. Keys resolve in the add-on's translation resources with English as the fallback locale. Translation resource packaging, locale selection and missing-key diagnostics remain a subsequent design step; this document does not supply a translation bundle.

### Property types, validation and process bindings

Authors declare `type` and constraints directly, not a JSON Schema fragment wrapped in `schema`. Andaru derives standard JSON Schema internally; localized metadata must resolve to ordinary strings when included in that schema. Data types and constraints drive API validation and default controls. UI overrides such as `select`, radio buttons, toggles, text inputs, numeric inputs and date pickers cannot change the data type or disable validation. Reject incompatible controls. Conditional visibility is presentation, not authorization or validation bypass.

`type: "enum"` means an array of selected option values. Duplicate option values invalidate the manifest; duplicate submitted selections invalidate settings, including API submissions. Uniqueness is invariant, with no `unique` option. `minItems` and `maxItems` constrain selection count. The example requires exactly one log level and stores it as, for example, `["INFO"]`. Its argument binding infers the sole scalar and produces two argument-vector elements, `--log.level` and `INFO`; there is no `encoding: single` setting. Multiple selections require explicit serialization such as a joined environment value or repeated arguments. The exact multi-selection syntax is not specified by this example.

Properties carry their own optional `binding`; there is no duplicate top-level `inputs` mapping. Environment variables and CLI arguments are the application integration boundary. An environment binding may omit `name` to use the property name unchanged. An unbound property is not automatically passed to the process. Arguments are passed as an argument vector, never through a shell. User-supplied custom argument overrides are deferred.

The approved serialization defaults for environment variables and value-taking arguments are:

| Value | Process representation |
| --- | --- |
| String | Unchanged text |
| Boolean | `true` or `false` |
| Integer / number | Locale-independent numeric text, without grouping separators |
| Date / date-time | ISO 8601 text matching the declared date/date-time type |
| Exactly one enum selection | Its sole scalar value, without an `encoding: single` setting |
| Multiple enum selections | Explicit joining or repeated arguments, not an implicit conversion |

Application-specific conversion remains the builder's responsibility. Presence-only CLI arguments use `style: "flag"` on a boolean property's argument binding:

```json
{
  "type": "argument",
  "name": "--accesslog",
  "style": "flag"
}
```

For this binding, `true` includes `--accesslog` and `false` omits it. Use this style only when absence has the intended application meaning; otherwise pass an explicit boolean value. Ordinary value-taking argument bindings must not silently treat booleans as presence-only flags.

Resolve declared defaults during settings validation and persist the resulting values, not silently at process startup. After default resolution, a missing required setting rejects the save; a missing optional setting without a default emits no binding. Explicit `false`, `0` and `""` are retained when valid under the property's constraints, not treated as missing by a truthiness check. Only `style: "flag"` intentionally omits `false`. Persisted settings do not silently change when a later manifest changes a default.

File/credential delivery also lives on the relevant property. `secret-reference` selects an extension-owned secret or an explicitly administrator-assigned shared secret, not arbitrary access to another extension's store. `certificate-reference` likewise requires ownership or explicit administrator assignment; access to a core-owned certificate is never implied by a `configure` grant. Re-check reference authorization on save and apply, including before delivering the private key. The `htpasswd` file binding delivers existing authorized credential material; it is not an instruction to put a password/hash in ordinary settings or generate one from a reference string. Certificate-file delivery supplies the certificate and key separately. The GUI sees references and permitted public metadata/fingerprints, never the private key or resolved secret. Opaque reference strings are not permissions merely because they validate syntactically.

### Runtime, networking and observability

`native` and `container` are the runtime types; Compose is a container workload provider, not a peer runtime type. An add-on may support one or both, with common identity, settings and permissions. Only one variant runs per instance. Switching variants requires storage/compatibility preflight, not an assumption of interchangeable execution.

The add-on builder owns application configuration generation, preprocessing and application-specific handling. The selected executable may be a packaged launcher that performs that work under the same restricted identity; this is not a privileged hook. Andaru does not add a preparation-script API, configuration-template engine, file-generation registry or separate native runtime descriptor. It passes validated bindings, delivers authorized credential files and provides owned directories. Resource paths reach the add-on through the environment/argument interface; their exact naming still needs definition.

A native executable runs as a dedicated restricted systemd service; the container backend supervises the container variant. Andaru exposes common start, stop, restart and status controls. The root's `manage` mapping governs manual lifecycle operations; viewing status requires `view`. Manual start/restart uses applied settings rather than silently promoting pending saved edits. Neither runtime declaration authorizes arbitrary root hooks, unit overrides, host mounts or runtime-socket access. Installation trust and privilege requests remain subject to platform approval. Package paths must stay inside the verified package; writable runtime paths are platform-assigned, not arbitrary host paths supplied by users.

Environment-bound routing changes require a native replacement process or container recreation. File-provider reload does not update a running process's environment. Application output is proposed to come from the selected runtime's journal/container stream; access output is a separate relative file under owned storage. The native journal path was exercised in the systemd spike; collection from a container output stream was not qualified by the earlier file-log Compose probe. Both sources are discoverable centrally at Logs → Add-ons → Traefik → Application / Access, with backend authorization, bounded reads, safe file opening and rotation handling. Metrics have a separate private endpoint.

Endpoint declarations request managed resources; `managed` does not itself publish a host port or grant WAN access. Port 8443 is an application listener request, not proof that host port 8443 is available. Exposure, listener conflicts, instance allocation and firewall policy must be resolved before activation. The selected TLS certificate must cover both configured hostnames. Andaru validates declared URI/hostname constraints; the builder is responsible for safely using those values in application configuration, including routing-template escaping. The URL pattern alone neither validates a URL completely nor authorizes network access. Reject URL userinfo, which would place credentials in ordinary settings, environment variables and exports; credentials require a separate secret reference. Upstream destination authorization, including loopback/core-service reachability, remains a platform preflight design decision. Andaru does not forward its own login cookie/token to the application WebUI; the link's `view` permission does not replace the application's separate authentication. Hiding menus never substitutes for API permissions. Declared permission names are scoped to the add-on and grant no rights by themselves.

### Apply, backups and offline recovery

Platform rules are mandatory, not extension-controlled opt-outs: page-level optimistic locking on saves and explicit page-scoped apply with a change preview and confirmation. Apply validates saved settings, resolves authorized bindings, then restarts the native process or recreates the container with updated environment/arguments. Runtime activation must account for dependencies between page changes without silently applying unrelated pending edits.

Verify readiness using the manifest's declared health check; process startup alone is insufficient. A healthy `/ping` also does not prove that every application setting was consumed correctly. The builder supplies a meaningful health declaration and application-specific correctness; Andaru does not invent application-specific tests or add lifecycle/preprocessing callbacks.

Report failed activation and restore the prior working configuration/runtime where supported and safe. Recovery must re-authorize secret/certificate access rather than treating a prior run as an authorization grant. A successful fallback is recovery from a failed apply, never a successful apply of the requested settings. If recovery is unsupported or fails, expose the failure rather than claiming the prior service is healthy. Saved configuration, applied configuration and observed runtime status must remain distinguishable.

Settings, related secrets, role definitions and permission associations follow the configuration generation together. User accounts, their role memberships and password hashes remain separate stores. Default exports omit resolved secrets. Including secrets or password hashes requires an authorized administrator's explicit option and a password-encrypted backup. Extension-owned secrets get the same backup choices and restrictions as core secrets; references in ordinary settings do not export their contents.

Extension-data backup is optional and enabled by default; this example declares only excluded log storage and no durable application-data store. Backups record installed extension/runtime versions, and restore asks before reinstalling extensions by default. Factory reset removes Andaru extensions and their managed state, not unrelated host software. Generated runtime configuration is disposable, not a competing source of truth.

`retainPreviousRuntime` and `requiresInternet: false` express recovery requirements, not evidence that the package implements them. Retain verified binaries/images by immutable identity and the dependencies/configuration/secret generation needed to run them. Repair must work without Internet access. These flags do not prove migration reversibility or safe application-state rollback. Compatibility preflight must retain the approved rule that an incompatible installed extension blocks the core upgrade.

## Next design step

Define manifest validation and preflight against this example: identifier and binding-name rules/collisions, compatible property/control/binding combinations, exact numeric/date wire formats and argument placement, multi-selection encoding syntax, and delivery of assigned credential/storage paths through the same interface. Null handling is not defined by the missing/empty rules and must not be silently equated with absence. Use the agreed lifecycle boundary before implementing a runner. Reuse existing binary/container probes; do not revive native descriptors, template machinery or preprocessing hooks.

Full i18n packaging, broad form/control vocabulary, OS-package installation, advanced multi-route configuration, ACME, container-socket discovery and runtime switching are outside this example. The next step should not silently add them.
