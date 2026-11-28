# Slice 7 Design: Core PKI, ACME (Let's Encrypt), Static Routes, and Native VPN (WireGuard, IPsec/XFRM, OpenVPN)

Status: approved direction (2026-11-28) — lab-qualified on Fedora CoreOS 45 (`SELinux Enforcing`).  
Parent spec: [2026-11-18-appliance-image-design.md](2026-11-18-appliance-image-design.md) (Slice 7: VPN) and [2026-11-27-addons-design.md](2026-11-27-addons-design.md) (certificate-reference & event bus).  
Lab evidence: [vpn-fcos-lab-spike.md](../../research/vpn-fcos-lab-spike.md).

---

## Objective

Provide native, Andaru-managed firewall VPNs (**WireGuard**, **IPsec/XFRM route-based, policy-based & road-warrior**, **OpenVPN server & client**), **static routing**, and **core PKI + ACME (Let's Encrypt) certificate lifecycle management** inside Andaru's declarative configuration, isolated `0600` secret store (`age`-encrypted in portable backups), atomic apply/confirm/rollback generation cycle, and single-owner `table inet andaru` firewall model.

Key user outcomes:
1. **Certificates & ACME (`vpn-pki`, `vpn-acme`):** Manage internal CAs, generated/imported certificates, OpenVPN `tls-crypt` keys, and automated Let's Encrypt / ACME certificates (HTTP-01 and DNS-01) from **System → Certificates**. Bind managed certificates to `andaru-web` (hot-reload on renewal), IPsec, OpenVPN, and add-ons (`certificate-reference`). Publish non-secret `certificate/renewed` events on Andaru's event bus so core services and add-ons (such as HAProxy) reload TLS seamlessly on renewal.
2. **Static Routes (`vpn-routes`):** Declare IPv4/IPv6 static routes targeting physical segments or VPN tunnel interfaces from **Network → Routes**, rendered into `systemd-networkd` `[Route]` sections (or OpenVPN `route`/`route-ipv6` directives for OpenVPN targets).
3. **WireGuard (`vpn-wireguard`):** Configure WireGuard interfaces (`wg*`) and site-to-site or road-warrior peers natively via `systemd-networkd`, with server-side keypair/PSK generation, downloadable peer `.conf` + client-side SVG QR codes, live `wg show` handshake/transfer telemetry, and firewall zone membership.
4. **IPsec (`vpn-ipsec`):** Configure IKEv2 route-based (Linux XFRM `ipsec*` interfaces, dual-stack VTI successor), policy-based tunnel mode, and road-warrior (XFRM + virtual pool, cert/EAP) connections using Fedora's `strongSwan 6.1.0` (`charon-systemd` + `swanctl`), with live `swanctl` SA status, initiate/terminate controls, and firewall zone membership.
5. **OpenVPN (`vpn-openvpn`):** Configure OpenVPN `2.7.9` (with Linux kernel DCO `ovpn`) TLS servers (road-warrior + site-to-site, `.ovpn` client profile export) and OpenVPN clients (site-to-site or VPN uplink with optional outbound NAT), live management-socket status, and firewall zone membership.

---

## Capability Map & Module Build Order

| Module id | Responsibility | Depends on |
| --- | --- | --- |
| `vpn-pki` | Additive schema v4 PKI (`pki.cas`, `pki.certificates`, `pki.tlsCryptKeys`, `management.certificate`), `core` secret kinds + 8192-byte limit, path-keyed generation file storage in `internal/generation/store.go`, Go `crypto/x509` CA/leaf/PEM import & `tls-crypt` keygen, `andaru-web` `--tls-cert-override` + `SIGHUP` hot-reload, add-on `certificate-reference` property + `:ro,Z,U` mounts, `andaru realize cert-sync`, `certificate/renewed` bus event (`certificate/#` forbidden for add-on publish), Audit `LayoutVersion = 3`, **System → Certificates** UI | — |
| `vpn-acme` | Additive `acme.accounts` / `acme.orders`, runtime registration/attempt state under `/var/lib/andaru/acme/`, native Go RFC 8555 ACME via existing `golang.org/x/crypto/acme` (zero new Go deps) running in `andaru-outbound` over non-durable internal bus (`acme-order/#` / `acme-result/+`, forbidden to add-ons); Let's Encrypt prod/staging + custom ACME/EAB; HTTP-01 responder (`[::]:8480` + nft `redirect` in `table inet andaru`) + 6 zero-dep DNS-01 providers (`cloudflare`, `hetzner`, `digitalocean`, `porkbun`, `acme-dns`, `webhook` with SSRF protection); daily renewal scheduler + manual renew/staging test + notifications; **System → Certificates → ACME** UI | `vpn-pki` |
| `vpn-routes` | Additive `routes` collection, IPv4/IPv6 family/CIDR/duplicate/target validation, `systemd-networkd` `[Route]` rendering for segments (with extension hook for VPN targets), **Network → Routes** UI | — |
| `vpn-wireguard` | Additive `vpn.wireguard`, `systemd-networkd` `50-andaru-<ifname>.{netdev,network}` + `0640 root:systemd-network` key/PSK files, zone-bound `wg*` + listen-port rules in `table inet andaru`, Curve25519 keygen API, peer `.conf` + client-side SVG QR export, lock-free `wg-status`, **VPN → WireGuard** UI | `vpn-routes` |
| `vpn-ipsec` | `strongswan` in `image/Containerfile`, static `/etc/strongswan/strongswan.d/50-andaru.conf`, `strongswan.service.d/andaru.conf`, 3 VPN CIL rules in `andaru.cil`, `tmpfiles.d` dirs, additive `vpn.ipsec`, `50-andaru-<ifname>.{netdev,network}` (`Kind=xfrm`, `Independent=yes`), `/run/andaru-ipsec/swanctl.conf` + PEMs, zone/IKE/NAT-T/ESP rules in `table inet andaru`, clean reload/unload/rollback, lock-free `ipsec-status` + initiate/terminate APIs, **VPN → IPsec** UI | `vpn-pki`, `vpn-routes` |
| `vpn-openvpn` | Additive `vpn.openvpn`, `/run/andaru-openvpn/<ifname>/` (`openvpn_etc_t`) + `/run/andaru-openvpn/run/<ifname>/mgmt.sock` (`openvpn_var_run_t`), `/run/systemd/system/andaru-openvpn-<ifname>.service` (`Type=notify`, kernel DCO `ovpn` with `disable-dco` toggle), zone-bound `ovpn*` + server listen port + client `uplinkNat` (`@uplinks`) in `table inet andaru`, OpenVPN-native static `route`/`route-ipv6` rendering, CSRF-protected `.ovpn` profile export, lock-free `openvpn-status` + client disconnect, **VPN → OpenVPN** UI | `vpn-pki`, `vpn-routes` |

**Build order:**  
`Stage 0 (Lab Spike + Spec)` → `vpn-pki` → (`vpn-acme` ∥ `vpn-routes`) → `vpn-wireguard` → `vpn-ipsec` → `vpn-openvpn`

---

## Architecture & Module Contracts

### 1. Configuration Schema (Additive `schemaVersion: 4`) & Secret Store

Following Slice 6 (`addons`), all Slice 7 sections are added **additively** to `schemaVersion: 4` with empty defaults. Existing `schemaVersion: 4` configs and portable backups load and upgrade cleanly with zero migration prompts and zero risk of triggering `/run/andaru-boot/addons-held`. Each module adds its own schema properties, `internal/config/schema.go` `s.objects`/`safePointer` entries, and additive `*_changes` SQLite table in `internal/config/sqlite.go`.

#### Config Collection Table

| Module | `config.Store` collection | ID prefix | REST URL (`/api/v1/config/...`) | SQLite changes table | `config/changes` batch |
| --- | --- | --- | --- | --- | --- |
| `vpn-pki` | `pki.cas` | `ca_` | `/api/v1/config/pki/cas` | `pki_ca_changes` | Yes |
| `vpn-pki` | `pki.certificates` | `crt_` | `/api/v1/config/pki/certificates` | `pki_certificate_changes` | Yes |
| `vpn-pki` | `pki.tlsCryptKeys` | `tck_` | `/api/v1/config/pki/tls-crypt-keys` | `pki_tlscrypt_changes` | Yes |
| `vpn-acme` | `acme.accounts` | `aca_` | `/api/v1/config/acme/accounts` | `acme_account_changes` | Yes |
| `vpn-acme` | `acme.orders` | `aco_` | `/api/v1/config/acme/orders` | `acme_order_changes` | Yes |
| `vpn-routes` | `routes` | `rte_` | `/api/v1/config/routes` | `route_changes` | Yes |
| `vpn-wireguard` | `vpn.wireguard` | `wg_` (peer `wgp_`) | `/api/v1/config/vpn/wireguard` | `vpn_wireguard_changes` | Yes |
| `vpn-ipsec` | `vpn.ipsec` | `ips_` | `/api/v1/config/vpn/ipsec` | `vpn_ipsec_changes` | Yes |
| `vpn-openvpn` | `vpn.openvpn` | `ovp_` | `/api/v1/config/vpn/openvpn` | `vpn_openvpn_changes` | Yes |

All collection endpoints follow Andaru's uniform contract: `GET` → `SavedList<T>`, `GET /{id}` → `SavedObject<T>`, `POST` → `201 SavedObject<T>`, `PUT /{id}` / `DELETE /{id}` with `If-Match: "<id>.<version>"`, and standard `{ error: { code, message, details } }` error envelopes.

#### Declaration Schemas

- `pki`:
  - `cas[]`: `{ id, name, subject, serial, fingerprintSha256, notBefore, notAfter, certPem, keyRef? (secretReference; absent for trust-anchor-only CAs) }`
  - `certificates[]`: `{ id, name, caId?, subject, sans[], serial, fingerprintSha256, notBefore, notAfter, certPem, chainPem?, keyRef (secretReference), keyAlgorithm ("ecdsa-p256"|"ecdsa-p384"|"rsa-2048"|"rsa-4096") }`  
    *(Single source of truth for ACME linkage: `acme.orders[].certificateId` points here; `pki.certificates[]` does NOT store a duplicate `acmeOrderId`.)*
  - `tlsCryptKeys[]`: `{ id, name, keyRef (secretReference) }`
- `management.certificate` (optional `crt_<uuid>`): binds a managed certificate to `andaru-web`; when unset, `andaru-web` uses the appliance bootstrap certificate. Changing `management.certificate` requires `Apply` so a misconfigured certificate remains inside the automatic rollback window.
- `acme`:
  - `accounts[]`: `{ id, name, directoryUrl, email, eabKeyId?, eabHmacRef? (secretReference), accountKeyRef (secretReference) }`  
    *(Mutable runtime state—`registeredUri`, `status`, last attempt/error, `nextRenewAt`—lives outside `declaration` in `/var/lib/andaru/acme/state.json`, matching `internal/aliasfeed`, so ACME operations never churn `config.db` versions.)*
  - `orders[]`: `{ id, name, accountId, certificateId? (optional until first successful issuance; unique across all orders including disabled), domains[], keyAlgorithm ("ecdsa-p256"|"ecdsa-p384"|"rsa-2048"), renewBeforeDays (default 30, 1..60), enabled, challenge: { type ("http-01"|"dns-01"), http01?: { listenSegments[] }, dns01?: { provider ("cloudflare"|"hetzner"|"digitalocean"|"porkbun"|"acme-dns"|"webhook"), propagationTimeoutSeconds (default 120, 10..600), cloudflare?: { apiTokenRef }, hetzner?: { apiTokenRef }, digitalocean?: { apiTokenRef }, porkbun?: { apiKeyRef, secretKeyRef }, acmeDns?: { serverUrl, username, passwordRef, subdomain, fullDomain? }, webhook?: { url, authorizationRef?, allowPrivateIp? } } } }`
- `routes[]`: `{ id, name, destination (canonical masked IPv4/IPv6 CIDR), target: { kind ("segment"|"wireguard"|"ipsec"|"openvpn"), id }, gateway? (optional IP matching destination family; forbidden on point-to-point wireguard/ipsec targets), metric? (1..4294967295), enabled }`
- `vpn`:
  - `wireguard[]`: `{ id, name, ifname ("wg0".."wg99", globally unique), enabled, zone (zoneID, required when enabled), listenPort (1..65535), allowListenPort (bool, default true), allowListenSegments[]? (optional segmentIDs scoping listenPort input rule; empty = all uplinks), mtu? (1280..1500, default 1420), addresses[] (CIDR v4/v6), privateKeyRef (secretReference), publicKey (derived, non-secret), peers[]: { id ("wgp_<uuid>"), name, enabled, publicKey, presharedKeyRef?, allowedIps[] (CIDR v4/v6), endpoint? (host:port), persistentKeepalive? (0..65535), routeAllowedIps (bool, default true) } }`
  - `ipsec[]`: `{ id, name, enabled, mode ("route-based"|"policy-based"|"road-warrior"), ifname? ("ipsec0".."ipsec99", required for route-based and road-warrior), xfrmIfId? (1..65535, unique, required for route-based and road-warrior), zone (zoneID, required when enabled), tunnelAddresses[]? (CIDR v4/v6 for route-based), localSegment? (segmentID), localAddress?, remoteAddress? (IP/FQDN; "%any" for road-warrior), ikeVersion (2), proposals[], espProposals[], auth: { localType ("psk"|"cert"), remoteType ("psk"|"cert"|"eap-mschapv2"), localId, remoteId?, pskRef?, localCertId?, caId?, eapUsers[]?: { username, passwordRef } }, localSubnets[]?, remoteSubnets[]?, virtualPool[]? (road-warrior), dpdDelaySeconds (default 30), startAction ("trap"|"start"|"none"), natTraversal (bool, default true) }`
  - `openvpn[]`: `{ id, name, enabled, role ("server"|"client"), ifname ("ovpn0".."ovpn99", globally unique), zone (zoneID, required when enabled), protocol ("udp"|"tcp"), port (1..65535), allowListenPort (bool, default true, server), allowListenSegments[]? (server), localSegment? (segmentID), remoteHost? (client), remotePort? (client), uplinkNat (bool, default false, client — appends ifname to @uplinks for outbound NAT), dco (bool, default true), tunnelNetwork4? (CIDR, server), tunnelNetwork6? (CIDR, server), caId, certId?, tlsCryptKeyId?, username?, passwordRef? (client), dataCiphers[] (default ["AES-256-GCM","CHACHA20-POLY1305"]), pushRoutes[]? (server), pushDns[]? (server), redirectGateway? (bool, server), pullRoutes? (bool, client, default false), clientOverrides[]? ({ commonName, staticIp4?, remoteSubnets[]? }) }`

#### Secret Store Contract (`"core"` Scope + Typed `kind`)

To avoid duplicating scope plumbing across `RealizationInput`, `render.Input`, and backup schemas, all Slice 7 secrets live in the existing `"core"` scope separated by explicit `kind` values added to `internal/config/secret-schema.json`, `internal/config/backup-secret-schema.json`, and `internal/config/secrets.go`:
- Existing `"core"` kinds: `"password"`, `"notification"`
- New `"core"` kinds: `"pki"`, `"acme"`, `"wireguard"`, `"ipsec"`, `"openvpn"`
- Raise secret value `maxLength` from `4096` to `8192` bytes in `secret-schema.json`, `backup-secret-schema.json`, `schema.json`, and `secrets.go` so RSA-4096 PKCS#8 PEM private keys (~3.3 KiB) fit with generous headroom.

Secrets are stored in `/var/lib/andaru/secrets.db` (`0600`, `PRAGMA secure_delete = ON`, isolated from `config.db`), follow the write-only `secretWrite` contract, are redacted in all API/preview responses, are excluded from backups unless `--include-secrets` is requested (and `age`-encrypted inside portable backups), and are wiped by factory reset.

#### Backup Restore Unresolved-Secrets Matrix

When restoring a secrets-excluded backup (`unresolved: true`):
- **Blocks `Apply` (only when enabled/referenced):** `management.certificate` private key, enabled add-on `certificate-reference` private key, enabled WireGuard `privateKeyRef` and enabled peer `presharedKeyRef`, enabled IPsec `pskRef`/cert key/`eapUsers[].passwordRef`, enabled OpenVPN cert key/`tlsCryptKeyId` key/client `passwordRef`.
- **Does NOT block `Apply`:** `pki.cas[].keyRef` (public CA cert still verifies peers; only blocks signing new leaf certs until re-entered), disabled VPNs/peers/add-ons, and `acme.*` secrets (ACME skips scheduled renewal and raises a notification, matching `notifications.channels`).
- Restoring `management.certificate` never swaps `andaru-web`'s live cert until `Apply` runs.

#### Referential Integrity & Global Interface Namespace

- Add `"wg"`, `"ipsec"`, and `"ovpn"` to `validAddonInterfaceName` forbidden prefixes (`internal/addon/manifest.go`).
- Enforce global `ifname` uniqueness across physical/VLAN/bridge/bond links, PPPoE, add-on interfaces, WireGuard, IPsec XFRM, and OpenVPN.
- Block deletion when referenced (enabled or disabled) by:
  - `pki.cas`: `pki.certificates[].caId`, `vpn.ipsec[].auth.caId`, `vpn.openvpn[].caId`.
  - `pki.certificates`: `management.certificate`, `acme.orders[].certificateId`, `vpn.ipsec[].auth.localCertId`, `vpn.openvpn[].certId`, any add-on `certificate-reference` setting.
  - `pki.tlsCryptKeys`: `vpn.openvpn[].tlsCryptKeyId`.
  - `acme.accounts`: `acme.orders[].accountId`.
  - `zones`: `vpn.wireguard[].zone`, `vpn.ipsec[].zone`, `vpn.openvpn[].zone`.
  - `segments`: `routes[].target`, `acme.orders[].challenge.http01.listenSegments[]`, `vpn.wireguard[].allowListenSegments[]`, `vpn.ipsec[].localSegment`, `vpn.openvpn[].localSegment`, `vpn.openvpn[].allowListenSegments[]`.
  - VPN interfaces (`wireguard`, `ipsec`, `openvpn`): `routes[].target`.

---

### 2. Core PKI, Generation Storage, Hot-Reload, Add-on `certificate-reference`, and Event Bus (`vpn-pki`)

- **Path-keyed generation file storage (`internal/generation/store.go`):**
  - Update `generation.Create` / `Generation.content` to store generation blobs at `files/<sha256(f.Path)[:16]>-<filepath.Base(f.Path)>` while keeping a read fallback to legacy `files/<filepath.Base(f.Path)>` for existing generations on disk. This prevents basename collisions across multi-instance OpenVPN `openvpn.conf`/`ccd/<cn>`, IPsec `x509/` vs `private/`, and multi-add-on `certs/`.
- **Internal PKI (`internal/pki/`):**
  - Pure Go `crypto/x509`, `crypto/ecdsa`, `crypto/rsa`, `crypto/rand`.
  - Supports generating internal Root/Intermediate CAs, signing leaf server/client/both certificates (DNS/IP SANs, proper EKU, SHA-256 fingerprint), importing external PEM cert + optional chain + private key (strictly verifying key matches leaf public key, valid PEM blocks, and chain verifies if provided), and generating OpenVPN 2048-bit `tls-crypt` static keys.
  - **Imperative PKI endpoints:**
    - `POST /api/v1/pki/cas/generate` (`{ name, commonName, organization?, keyAlgorithm, validDays, parentCaId? }` → `201 SavedObject<PKICA>`)
    - `POST /api/v1/pki/certificates/generate` (`{ name, caId, commonName, sans[], usage: "server"|"client"|"both", keyAlgorithm, validDays }` → `201 SavedObject<PKICertificate>`)
    - `POST /api/v1/pki/certificates/import` (`{ name, certPem, chainPem?, privateKeyPem }` → `201 SavedObject<PKICertificate>`; never echoes `privateKeyPem`)
    - `POST /api/v1/pki/tls-crypt-keys/generate` (`{ name }` → `201 SavedObject<TlsCryptKey>`)
    - `GET /api/v1/pki/cas/{id}/pem` and `GET /api/v1/pki/certificates/{id}/pem` (`application/x-pem-file`, public cert/chain ONLY — never private keys).
    - Standard `/api/v1/config/pki/*` supports listing, fetching metadata, renaming/updating non-key metadata, and `DELETE` with `If-Match`.
- **`andaru-web` TLS hot-reload:**
  - `internal/web/server.go` accepts `--tls-cert-override /run/andaru-web/tls.crt --tls-key-override /run/andaru-web/tls.key`, falling back cleanly to `--tls-cert`/`--tls-key` (bootstrap cert) whenever the override files do not exist.
  - Switches `tls.Config` to `GetCertificate` backed by an `atomic.Pointer[tls.Certificate]`, reloaded on `SIGHUP` (retaining the previous valid certificate on read/parse error, never dropping live HTTPS sessions).
  - Add `ExecReload=/bin/kill -HUP $MAINPID` to `packaging/systemd/andaru-web.service`.
- **Add-on `certificate-reference` property type:**
  - Unlocks the reserved `certificate-reference` property type in `internal/addon/manifest.go`:
    - Setting wire value: plain string `"crt_<uuid>"` (matching `zone-reference`).
    - Manifest `Binding`: `type: "certificate"`, `name: "<slug_safe_name>"`, `format: "bundle" | "split"`, optional `certEnv`, `keyEnv`, `bundleEnv` (checked in `checkUnknownFields` and `reserveEnv`, defaulting to `ANDARU_CERT_<UPPER_NAME>_{CERT,KEY,BUNDLE}`).
    - `addon.ValidateSettings` accepts `validCerts` alongside `validZones`, rejecting dangling `crt_<uuid>` references on enabled and disabled add-ons.
  - Rendered into `/run/andaru-addons/<slug>/certs/` (`0400` PEM files, `Secret: true`, redacted in preview, mounted `:ro,Z,U` at `/run/andaru/certs/` inside the container, included in `addonSum` and preserving SELinux `:Z` MCS labels & `:U` ownership in `generation/addons.go`).
  - Only the exact `crt_<uuid>` selected in the add-on's settings is ever mounted into that add-on.
- **DB-free `andaru realize cert-sync` & Reboot/Rollback persistence:**
  - `andaru realize` never opens `config.db`/`secrets.db`. `andaru-engine` writes a staged `0600` JSON payload to `/run/andaru/cert-sync-<rand>.json` and invokes `andaru realize cert-sync <stagedFile>` (matching `addon-bus-tokens` in `internal/engine/addon_bus.go`). `cert-sync` reads and immediately unlinks the staged file.
  - `cert-sync` atomically refreshes:
    1. `/run/andaru-web/tls.{crt,key}` (`0400 andaru-web:andaru-web`) + `SIGHUP` to `andaru-web.service` (or removes override files + `SIGHUP` if `management.certificate` was cleared).
    2. `/run/andaru-ipsec/{x509,x509ca,private}/` + `swanctl --load-creds` (when `strongswan.service` is active).
    3. `/run/andaru-openvpn/<ifname>/` cert/key files + restarts only affected `andaru-openvpn-<ifname>.service` unit(s).
    4. Affected add-ons' `/run/andaru-addons/<slug>/certs/` preserving `:Z`/`:U` labels/ownership.
  - `andaru-engine` runs `cert-sync` on **startup** (after `addons-release-hold`, matching `addonBus.sync`), after every **`Activate`/`Rollback`**, and after every **certificate renewal/replacement**, ensuring a reboot or unrelated rollback never leaves daemons or add-ons on stale/expired pre-renewal certs. For every trigger (startup, `Activate`, `Rollback`, and renewal/replacement), `andaru-engine` builds the staged `cert-sync` payload strictly from the **live generation's declaration (`Pointers.Live`)** for consumer bindings (`management.certificate`, IPsec, OpenVPN, add-on `certificate-reference`), substituting the latest committed cert/key bytes for each referenced `certificateId` — so newly activated certificates are testable during the rollback window, reverted cleanly on rollback, and pending unapplied saved edits never deploy before `Apply`.
- **Certificate renewal event (`certificate/renewed`):**
  - Add `"certificate/#"`, `"acme-order/#"`, and `"acme-result/#"` to `forbiddenBusPublish` and `"acme-order/#"`, `"acme-result/#"` to `forbiddenBusSubscribe` in `internal/addon/manifest.go`. Add-ons **may** subscribe to `"certificate/renewed"` or `"certificate/+"`.
  - Published once on Andaru's single `*bus.Bus` instance (reaching both internal and subscribed add-on consumers) wrapped in standard `bus.Event` (`type: "certificate.renewed.v1"`, `durable: true`, non-secret payload):
    ```json
    {
      "certificateId": "crt_...",
      "name": "edge.example.com",
      "fingerprintSha256": "...",
      "serial": "...",
      "notBefore": "2026-11-28T00:00:00Z",
      "notAfter": "2027-02-26T00:00:00Z",
      "source": "acme"
    }
    ```
    (`source`: `"acme" | "manual" | "import"`).

---

### 3. ACME / Let's Encrypt (`vpn-acme`)

- **Zero new Go dependencies:** Built on `golang.org/x/crypto/acme` (already in `go.mod`).
- **Privilege, network, and bus separation:**
  - `andaru-engine` stays networkless and retains the leaf private key in memory during issuance.
  - Extend `andaru-outbound`'s `busGrants` in `internal/engine/events.go` with `Publish: [..., "acme-result/+"]` and `Subscribe: [..., "acme-order/#"]`.
  - Publish `acme-order/<orderId>` with **`durable: false`** so ACME account keys, EAB HMAC keys, and DNS-01 API tokens are **never persisted to `/var/lib/andaru/bus.db`**. (Orders are stored in `config.db` and re-triggered by the scheduler or user.)
  - Issuance flow:
    1. `andaru-engine` generates a fresh ECDSA/RSA keypair + X.509 CSR in memory (never sending the leaf private key to `andaru-outbound`), resolves the ACME account key + EAB + challenge provider credentials from `secrets`, and publishes the non-durable job to `acme-order/<orderId>`.
    2. `andaru-outbound` performs RFC 8555 account registration (if needed), order creation, challenge fulfillment, authorization polling, and CSR finalization, then publishes the issued leaf + issuer chain PEM + `accountUri` (or structured error) to `acme-result/<orderId>`.
    3. `andaru-engine` verifies the returned leaf cert's public key matches its staged private key and covers all requested SANs. Unless `stagingTest: true` (which validates without mutating stored certs or emitting events): if `order.certificateId` is unset (initial issuance), `andaru-engine` creates a new `pki.certificates` record (named from the order name) + private key in `secrets` and binds `order.certificateId` to the new `crt_<uuid>`; otherwise it updates the bound `pki.certificates` record + private key in place. It then runs `andaru realize cert-sync` (against the live generation's consumer bindings), updates `/var/lib/andaru/acme/state.json`, publishes `certificate/renewed`, and raises/clears notification `source: "acme"`, `key: "acme/<orderId>"`.
    4. When an automated renewal succeeds while `applies.unapplied == false` and only the managed cert PEM/dates rotated (deployed via `cert-sync`), `andaru-engine` keeps `applies.unapplied == false` so overnight renewals do not trigger a false "Pending changes" banner. When `applies.unapplied == true` (admin has pending edits), the renewed cert+key are still committed and `cert-sync` still refreshes already-applied consumers of that `certificateId`, while `applies.unapplied` remains `true` so pending admin edits are preserved untouched until `Apply`.
- **Supported ACME directories:**
  - UI presets for Let's Encrypt Production (`https://acme-v02.api.letsencrypt.org/directory`), Let's Encrypt Staging (`https://acme-staging-v02.api.letsencrypt.org/directory`), plus any custom HTTPS ACME v2 directory with optional EAB (`eabKeyId` + `eabHmacRef`).
- **HTTP-01 responder (capability-free `andaru-outbound` + single `table inet andaru`):**
  - Keep `andaru-outbound.service` strictly capability-free (`CapabilityBoundingSet=`).
  - During an active HTTP-01 challenge only, `andaru-outbound` binds a temporary read-only HTTP server on `[::]:8480` serving ONLY `GET /.well-known/acme-challenge/<token>` (strict token validation, `404` for everything else, closed immediately on completion).
  - When at least one enabled HTTP-01 order exists, `internal/render/nft.go` declares an **empty-by-default** dynamic interface set `set acme_http01_active { type ifname; }` and rules inside `table inet andaru`:
    - `prerouting`: `iifname @acme_http01_active fib daddr . iif type local tcp dport 80 ct mark set <acmeMark> redirect to :8480`
    - `input`: `iifname @acme_http01_active ct mark <acmeMark> tcp dport 8480 accept`
    When zero enabled HTTP-01 orders exist, neither the set nor any port-80 rule is rendered.
  - Strictly for the duration of an active HTTP-01 challenge, `andaru-engine` invokes `andaru realize acme-http01-open <ifaces...>` (adding only the order's applied `listenSegments` interfaces to `@acme_http01_active`) before dispatching the order to `andaru-outbound`, and unconditionally invokes `andaru realize acme-http01-close` (`nft flush set inet andaru acme_http01_active`) in a `defer` on completion, error, timeout, or engine startup. Normal port-80 traffic (e.g. a reverse-proxy add-on) is never redirected when no HTTP-01 challenge is active.
- **DNS-01 providers (6 zero-dep HTTP/REST providers):**
  1. `cloudflare` (API Token with Zone:DNS:Edit)
  2. `hetzner` (Hetzner DNS API Token)
  3. `digitalocean` (DigitalOcean DNS API Token)
  4. `porkbun` (API key + secret key)
  5. `acme-dns` (`POST /update` with `X-Api-User`/`X-Api-Key` — delegates any DNS provider via `_acme-challenge` CNAME)
  6. `webhook` (authenticated HTTPS POST `{ action: "present"|"cleanup", domain, fqdn, value }` — universal escape hatch for Route53/RFC2136/custom DNS; uses a dedicated SSRF-protected dialer blocking loopback, link-local, RFC1918/ULA, CGNAT, multicast, and redirects, matching `aliasClient` in `internal/outbound/outbound.go`, unless `allowPrivateIp: true` is explicitly set by the admin).
  - Bounded `net.LookupTXT("_acme-challenge." + domain)` propagation polling up to `propagationTimeoutSeconds` (default 120s), and unconditional `defer cleanup()` of the TXT record.
- **Imperative & Status APIs:**
  - `GET /api/v1/status/acme` → `{ items: [{ orderId, accountId, certificateId, state: "idle"|"running"|"succeeded"|"failed", lastAttemptAt?, lastSucceededAt?, lastError?, nextRenewAt? }], accounts: [{ accountId, registeredUri?, status }] }`
  - `POST /api/v1/acme/orders/{id}/renew` → `202 Accepted { orderId, started: true }` (`409` if disabled or already running)
  - `POST /api/v1/acme/orders/{id}/test-staging` → `202 Accepted { orderId, stagingTest: true }` (runs against Let's Encrypt Staging without mutating the live certificate or triggering `cert-sync`/`certificate/renewed`)

---

### 4. Static Routes (`vpn-routes`)

- **Validation (`internal/config/routes.go`):**
  - Canonicalizes `destination` to masked CIDR, validates `gateway` IP family matches `destination` IP family, forbids `gateway` on point-to-point `wireguard`/`ipsec` targets, rejects loopback/multicast destinations and duplicate `(destination, target.kind, target.id, metric)` tuples, and enforces target existence & deletion blocking.
- **Rendering:**
  - `segment`, `wireguard`, `ipsec` targets: rendered as `[Route]` sections (`Destination=`, optional `Gateway=`, optional `Metric=`) inside the target interface's `systemd-networkd` `.network` file.
  - `openvpn` targets: rendered as OpenVPN `route` / `route-ipv6` directives inside `/run/andaru-openvpn/<ifname>/openvpn.conf` (never a `systemd-networkd` `.network` file, since OpenVPN owns `ovpn*` as `unmanaged`).
  - `vpn-routes` registers a target-validation/render registry starting with `segment`; `vpn-wireguard`, `vpn-ipsec`, and `vpn-openvpn` plug into it without schema conflicts.

---

### 5. WireGuard (`vpn-wireguard`)

- **Rendering (`internal/render/wireguard.go`):**
  - Per enabled interface:
    - `/run/systemd/network/50-andaru-<ifname>.netdev` (`Kind=wireguard`, `ListenPort=`, `PrivateKeyFile=/run/systemd/network/50-andaru-<ifname>.key`)
    - `/run/systemd/network/50-andaru-<ifname>.network` (`Address=`, `IPv4Forwarding=yes`, `IPv6Forwarding=yes`, `[Route]` entries for `routeAllowedIps` peers + static `routes`)
    - `/run/systemd/network/50-andaru-<ifname>.key` and per-peer `/run/systemd/network/50-andaru-<ifname>-<peerId>.psk` (`Mode: "0640"`, `Owner: "root:systemd-network"`, `Secret: true`, redacted in preview).
  - Update `internal/generation/store.go` (`allowed`, `modeFor`) and `internal/generation/realize.go` (`files()` to apply `0640` + `chown(0, systemd-network gid)`, and `unmet()` to treat WireGuard interfaces without failing when peers are idle). Using `50-andaru-<ifname>.netdev` matches `realize.go:settle()`'s `networkctl delete <ifname>` cleanup automatically!
  - Adds `<ifname>` to its bound zone's interface-name set in `table inet andaru` (`internal/render/nft.go`). When `allowListenPort: true`, adds `udp dport <listenPort> accept` in `chain input` (scoped to `allowListenSegments` when set, otherwise `@uplinks`).
- **APIs:**
  - `POST /api/v1/vpn/wireguard/keypair` (`{ includePsk?: bool }` → `{ privateKey, publicKey, presharedKey? }`, `Cache-Control: no-store`, uses stdlib `crypto/ecdh.X25519()`).
  - `POST /api/v1/vpn/wireguard/{id}/peers/{peerId}/config` (`{ endpoint?, dns[]?, includePsk?: bool }` → `text/plain` `.conf` template with `PrivateKey = <PRIVATE_KEY>`, `Cache-Control: no-store`; `includePsk: true` requires `vpn.control` and emits audit `vpn.wireguard.psk_exported`). The Web UI injects any freshly generated client private key purely client-side before download or rendering a zero-dep SVG QR code.
  - `GET /api/v1/status/vpn/wireguard` → `{ items: [{ id, ifname, publicKey, listenPort, peers: [{ id?, name?, publicKey, endpoint?, latestHandshakeAt?, rxBytes, txBytes, online }] }] }` (lock-free `andaru realize wg-status <outFile>` running outside `Realizer.lock()`).

---

### 6. IPsec / XFRM (`vpn-ipsec`)

- **Image, Systemd & SELinux:**
  - Add `strongswan` (`6.1.0`) to `image/Containerfile` (disabled by default via preset; started/stopped by `internal/generation` only when IPsec is enabled).
  - Ship static `packaging/strongswan/50-andaru.conf` → `/etc/strongswan/strongswan.d/50-andaru.conf` (`charon { install_routes_xfrmi = no }`, matching `packaging/unbound/andaru.conf`).
  - Ship `packaging/systemd/strongswan.service.d/andaru.conf` (`After=andaru-network-boot.service systemd-networkd.service`, `ConditionPathExists=/run/andaru-ipsec/swanctl.conf`, `ExecStartPost=/usr/bin/swanctl --load-all --file /run/andaru-ipsec/swanctl.conf --noprompt`).
  - Add `/run/andaru-ipsec`, `/run/andaru-openvpn`, and `/run/andaru-openvpn/run` to `packaging/tmpfiles.d/andaru.conf`.
  - Add the 3 lab-verified CIL rules to `image/files/usr/share/andaru/selinux/andaru.cil`:
    ```cil
    (filecon "/run/andaru-ipsec(/.*)?" any (system_u object_r ipsec_conf_file_t ((s0) (s0))))
    (filecon "/run/andaru-openvpn(/.*)?" any (system_u object_r openvpn_etc_t ((s0) (s0))))
    (filecon "/run/andaru-openvpn/run(/.*)?" any (system_u object_r openvpn_var_run_t ((s0) (s0))))
    ```
- **Rendering (`internal/render/ipsec.go`):**
  - For each enabled `route-based` or `road-warrior` connection: renders `/run/systemd/network/50-andaru-<ifname>.netdev` (`Kind=xfrm`, `InterfaceId=<xfrmIfId>`, `Independent=yes`) + `/run/systemd/network/50-andaru-<ifname>.network` (`Address=`, `IPv4Forwarding=yes`, `IPv6Forwarding=yes`, static `routes`), and adds `<ifname>` to its bound zone's interface set in `table inet andaru`.
  - For each enabled `policy-based` connection: assigns a deterministic `reqid` in `swanctl.conf` and binds `meta ipsec exists ipsec in reqid <reqid>` / `ipsec out reqid <reqid>` into its configured zone policy in `table inet andaru`.
  - Renders `/run/andaru-ipsec/swanctl.conf` (`0600`, `Secret: true` when holding PSK/EAP secrets, redacted in preview) plus CA/cert/key PEMs under `/run/andaru-ipsec/{x509,x509ca,private}/` (`0400` keys) so `strongswan` never writes to `/etc/strongswan/`.
  - Opens IKE/NAT-T/ESP (`udp dport { 500, 4500 }`, `ip protocol esp`, `ip6 nexthdr esp`) in `table inet andaru` `chain input` when enabled IPsec connections exist.
- **Realizer Lifecycle & Rollback:**
  - Places `/run/andaru-ipsec/` files, runs `restorecon -RF /run/andaru-ipsec`, starts/reloads `strongswan.service` via `swanctl --load-all --clear --file /run/andaru-ipsec/swanctl.conf --noprompt` and terminates any SAs belonging to removed/disabled connections, or stops `strongswan.service` and removes `/run/andaru-ipsec/` files when zero IPsec connections are enabled.
- **APIs:**
  - `GET /api/v1/status/vpn/ipsec` → `{ serviceActive, items: [{ id, name, ikeSAs: [{ uniqueId, state, localHost, remoteHost, establishedSec, childSAs: [{ name, state, localTs[], remoteTs[], bytesIn, bytesOut, rekeySec }] }] }] }` (lock-free `andaru realize ipsec-status <outFile>`).
  - `POST /api/v1/vpn/ipsec/{id}/initiate` and `POST /api/v1/vpn/ipsec/{id}/terminate` → `200 { ok: true }` (`409` if disabled/not applied/service stopped; audited).

---

### 7. OpenVPN (`vpn-openvpn`)

- **Rendering (`internal/render/openvpn.go`):**
  - Per enabled instance (`<ifname>` = `ovpn0`..`ovpn99` everywhere):
    - `/run/andaru-openvpn/<ifname>/openvpn.conf` (`0600`; omits deprecated `--persist-key`; enables kernel DCO by default and emits `disable-dco` when `dco: false` or incompatible ciphers/options are selected; renders any `routes` targeting this instance as `route`/`route-ipv6` directives).
    - CA/cert/key/`tls-crypt`/auth-user-pass files under `/run/andaru-openvpn/<ifname>/` (`0400` keys, `Secret: true`, redacted in preview) and optional `ccd/<commonName>` files.
    - `/run/systemd/system/andaru-openvpn-<ifname>.service` (`Type=notify`, `ExecStart=/usr/bin/openvpn --config /run/andaru-openvpn/<ifname>/openvpn.conf`, `Restart=on-failure`, `RestartSec=5`).
    - Management socket at `/run/andaru-openvpn/run/<ifname>/mgmt.sock` (`openvpn_var_run_t`, `management-client-user root`).
  - Firewall (`table inet andaru`): adds `<ifname>` to its bound zone's interface-name set; opens `<proto> dport <port>` in `chain input` for `server` role when `allowListenPort: true`; when `role == "client"` and `uplinkNat == true`, appends `<ifname>` to `@uplinks` so automatic outbound NAT masquerades traffic egressing the VPN uplink.
- **Realizer Lifecycle & Rollback:**
  - Creates `/run/andaru-openvpn/<ifname>` and `/run/andaru-openvpn/run/<ifname>`, places files, runs `restorecon -RF /run/andaru-openvpn`, runs `systemctl daemon-reload`, starts/restarts enabled `andaru-openvpn-<ifname>.service` units.
  - On disable/delete/rollback: stops removed `andaru-openvpn-<ifname>.service` units **before** deleting unit files and running `daemon-reload`, then removes `/run/andaru-openvpn/<ifname>` and `/run/andaru-openvpn/run/<ifname>`.
- **APIs:**
  - `POST /api/v1/vpn/openvpn/{id}/client-profile` (`{ certId?, includePrivateKey?: bool, remoteHost? }` → `text/plain` inline `.ovpn`, `Cache-Control: no-store`, CSRF-protected, audited `vpn.openvpn.profile_exported`). Validates `certId` is signed by the instance's `caId`. `includePrivateKey` defaults to `false` (emits placeholder unless caller holds `certificates.manage` and explicitly requests `includePrivateKey: true`).
  - `GET /api/v1/status/vpn/openvpn` → `{ items: [{ id, ifname, role, activeState, dcoEnabled, cipher?, clients: [{ cid, commonName, realAddress, virtualAddress, bytesReceived, bytesSent, connectedSince }] }] }` (lock-free `andaru realize openvpn-status <outFile>`).
  - `POST /api/v1/vpn/openvpn/{id}/clients/{cid}/disconnect` → `200 { ok: true }` (`404`/`409` on unknown client or inactive instance; audited).

---

## RBAC, Audit, Safe Mode, Factory Reset & Web UI

### RBAC Capability Matrix

Following Slice 6's clean split, all `/api/v1/config/*` CRUD and `/api/v1/config/changes` batches remain under `configuration.read` / `configuration.write`. New capabilities govern non-config/imperative/runtime endpoints:

| Capability | Built-in roles | Gated endpoints |
| --- | --- | --- |
| `certificates.read` | `read-only`, `administrator` | `GET /api/v1/pki/cas/{id}/pem`, `GET /api/v1/pki/certificates/{id}/pem`, `GET /api/v1/status/acme` |
| `certificates.manage` | `administrator` | `POST /api/v1/pki/{cas,certificates,tls-crypt-keys}/generate`, `POST /api/v1/pki/certificates/import`, `POST /api/v1/acme/orders/{id}/{renew,test-staging}`, `includePrivateKey: true` on OpenVPN profile export |
| `vpn.read` | `read-only`, `administrator` | `GET /api/v1/status/vpn/{wireguard,ipsec,openvpn}` |
| `vpn.control` | `administrator` | `POST /api/v1/vpn/wireguard/keypair`, `POST /api/v1/vpn/wireguard/{id}/peers/{peerId}/config`, `POST /api/v1/vpn/ipsec/{id}/{initiate,terminate}`, `POST /api/v1/vpn/openvpn/{id}/client-profile`, `POST /api/v1/vpn/openvpn/{id}/clients/{cid}/disconnect` |

Every new engine route MUST be registered in both `internal/engine/openapi.json` and `buildEngineRoutes` (enforced by startup parity check).

### Audit (`LayoutVersion = 3`)

Bump `internal/audit/sqlite.go` `LayoutVersion` from `2` to `3` with `migrateAuditV2` adding these actions (all using durable `Begin`/`Finish` intents without `Target` on `Finish`; for creation actions, pass the pre-allocated resource ID as `Target` on `Begin`):
- `certificate.created`, `certificate.imported`, `certificate.deleted`, `certificate.renewed`
- `acme.order.triggered`
- `vpn.wireguard.psk_exported`, `vpn.ipsec.initiated`, `vpn.ipsec.terminated`, `vpn.openvpn.client_disconnected`, `vpn.openvpn.profile_exported`

### Safe Mode & Reset

- **Safe mode (`andaru.safe=1` / `/run/andaru-boot/safe-mode`):** Keeps native VPNs running (like physical links and PPPoE) so remote WireGuard/IPsec/OpenVPN admin access stays reachable during recovery; pauses the ACME renewal scheduler while in safe mode.
- **Factory / soft reset (`internal/reset/`):** Stops and removes `strongswan.service` runtime state, stops/removes all `andaru-openvpn-*.service` units, removes `wg*`/`ipsec*` netdevs, wipes `/run/andaru-ipsec`, `/run/andaru-openvpn`, `/run/andaru-web/tls.{crt,key}`, and `/var/lib/andaru/acme/`.

### Web UI Screens (Mantine UI, Strict CSP, Zero New npm Deps)

- **System → Certificates** (`/system/certificates`): tabs for **Certificates**, **Certificate Authorities**, **TLS-Crypt Keys**, and **ACME (Let's Encrypt)** (accounts + orders + live status + "Renew now" + "Test staging"). Adds HTTPS certificate picker (`management.certificate`) to **System → Management / Web** and `certificate-reference` picker to `AddonPropertyForm`.
- **Network → Routes** (`/network/routes`): static IPv4/IPv6 routes table + editor modal targeting segments and VPN interfaces.
- **VPN** top-level navigation group:
  - **VPN → WireGuard** (`/vpn/wireguard`): interfaces, live peer handshake/transfer badges, inline keypair/PSK generator, peer `.conf` download & pure zero-dep SVG QR modal.
  - **VPN → IPsec** (`/vpn/ipsec`): route-based XFRM, policy-based, and road-warrior connections, live IKE/CHILD SA status badges, Initiate/Terminate buttons.
  - **VPN → OpenVPN** (`/vpn/openvpn`): server & client instances, DCO badge, connected client table with Disconnect action, `.ovpn` client profile exporter.

---

## Commands, Testing Strategy, and Boundaries

### Commands

- Go tests: `TMPDIR=/tmp go test ./...`
- Focused package tests: `TMPDIR=/tmp go test ./internal/pki/... ./internal/acme/... ./internal/config/... ./internal/render/... ./internal/generation/... ./internal/outbound/... ./internal/engine/...`
- Web UI checks: `cd web && npm run lint && npm run typecheck && npm test && npm run build`
- Container image lint: `podman build --network=host -f image/Containerfile .` (`bootc container lint --fatal-warnings`)

### Testing Strategy

- **`vpn-pki`:** Path-keyed generation storage + legacy fallback tests; CA/leaf generation & PEM import validation tests; referential integrity on delete; `andaru-web` `SIGHUP` hot-reload without dropping connections; add-on `certificate-reference` rendering (`:ro,Z,U`, redacted preview); `cert-sync` staged payload + reboot persistence tests; `certificate/renewed` bus delivery (including add-on publish forgery rejection); audit v2→v3 migration tests. Vitest tests for Certificates UI & `AddonPropertyForm`.
- **`vpn-acme`:** Full end-to-end RFC 8555 ACME tests against an in-process mock ACME v2 directory server (account registration + EAB, HTTP-01 challenge + nftables redirect golden tests, all 6 DNS-01 providers against `httptest` mock provider APIs including SSRF private-IP blocking on webhook, renewal scheduler, staging dry-run non-mutation, failure notification, CSR/key-match verification, and non-durable `andaru-outbound` bus round-trip). Vitest tests for ACME UI.
- **`vpn-routes`:** CIDR normalization, family mismatch, duplicate detection, target existence, referential integrity, and `systemd-networkd` `[Route]` golden render tests. Vitest tests for Routes UI.
- **`vpn-wireguard`:** `50-andaru-<ifname>.{netdev,network}` + `0640` key/PSK golden & realizer tests, `table inet andaru` zone + listen-port rules, `wg show dump` parser tests, peer `.conf` + client-side QR SVG tests. Vitest tests for WireGuard UI.
- **`vpn-ipsec`:** `50-andaru-<ifname>.{netdev,network}` (`Kind=xfrm`) + `swanctl.conf` golden tests (route-based, policy-based, road-warrior), `swanctl --list-sas` parser tests, realizer start/reload/unload/stop/rollback tests, and live Proxmox FCOS E2E verification under SELinux `Enforcing`. Vitest tests for IPsec UI.
- **`vpn-openvpn`:** `openvpn.conf` (including static `route`/`route-ipv6`) + `andaru-openvpn-<ifname>.service` golden tests (server DCO, server non-DCO, client `uplinkNat`, CCD overrides), `status 3` management-socket parser tests, CSRF-protected `.ovpn` exporter tests, realizer start/stop/rollback/SELinux cleanup tests, and live Proxmox FCOS E2E verification under SELinux `Enforcing`. Vitest tests for OpenVPN UI.

### Boundaries

- **Always:**
  - Keep `andaru-engine` unprivileged and networkless; run all external ACME/DNS-01 calls in `andaru-outbound` (capability-free) and all privileged file/systemd/daemon operations in `andaru realize` (DB-free).
  - Keep all private keys, PSKs, ACME account keys, and DNS API tokens strictly inside the `0600` `secrets.db` store (`age`-encrypted in backups, redacted in all APIs/previews).
  - Keep `table inet andaru` as the sole host nftables table (`install_routes_xfrmi = no`, zero strongSwan/OpenVPN/WireGuard firewall scripts).
  - Keep every VPN/PKI/route change inside Andaru's atomic apply/confirm/rollback generation cycle and factory-reset cleanup.
  - Validate every certificate, ACME order, route, and VPN setting fail-closed at save, restore, and render/apply.
- **Ask first:**
  - Adding any new Go or npm dependency beyond existing `go.mod` / `web/package.json`.
  - Any schema bump beyond `schemaVersion: 4`.
  - Enabling IKEv1 or weak/deprecated ciphers (MD5, SHA1, 3DES, Blowfish, MODP1024).
- **Never:**
  - Send certificate private keys over the event bus (`certificate/renewed` carries metadata only; `acme-order/#` carries CSR only and is `durable: false` so ACME/DNS credentials are never written to `bus.db`).
  - Allow any add-on to subscribe to `acme-order/#` or `acme-result/#`, or publish to `certificate/#`.
  - Open port 80 for HTTP-01 unless an enabled HTTP-01 ACME order is explicitly configured.
  - Write generated VPN or PKI config files into `/etc` (keep all generated runtime config under `/run/andaru-*` and `/run/systemd/network`).
