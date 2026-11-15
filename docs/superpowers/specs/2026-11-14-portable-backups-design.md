# Portable backup and restore: root-local foundation

Status: approved by the owner on 2026-11-15. The root-local foundation approach was selected on 2026-11-14. An implementation plan must be reviewed before product code changes.

## Intent and approved scope

Implement the first portable backup/restore stage of [issue #13](https://github.com/andaru-project/andaru/issues/13). An administrator can carry saved configuration to another Andaru installation without copying SQLite files or live rendered state. A backup may include configuration secrets only after an explicit request and mandatory whole-archive encryption.

The owner approved this prerequisite order after mapping the code:

- Build manual backup/restore for the existing configuration and configuration-secret stores, through the root-local engine API and CLI.
- For this console-only stage, verified OS-root access authorizes secret export instead of re-entering an Andaru account password: no Andaru account/credential store or password authentication exists yet.
- Future network/web secret export still requires the approved administrator permission, fresh password reauthentication and auditing. This exception does not authorize a network listener or a password-bypass endpoint.
- Users, user credentials, extension manifests/data/secrets, remote destinations and scheduled jobs remain separate planned stages. They are not represented by fake empty stores, stub backends or claims of full issue completion.

This is not the cold local package-recovery set from PR #56. Portable backups contain logical documents, not database copies, executables, rendered generations, runtime sessions, audit logs, usage statistics or local configuration history. They do not revert Debian packages or install extensions.

## Approaches considered

1. **Selected: existing-store foundation, root-local.** Supplies useful portable configuration/secret recovery now and reuses the running engine's authorization and transactional store. Requires the explicit console authorization exception above; cannot claim the future four-store/UI/scheduled feature is complete.
2. **Identity first.** Implement Andaru users, credentials, roles and fresh password authentication before exporting secrets. Avoids the console exception, but makes backup work depend on a separate security-sensitive project. The owner did not select this order.

Direct SQLite ZIPs, generic filesystem extraction, subprocess encryption and opening the live store from a second offline CLI are not alternatives: they violate the approved format, safety boundary or single engine ownership.

## Existing implementation and seams

- `internal/config/store_linux.go` owns serialized saved writes and publishes paired configuration/secret documents through its existing durable commit boundary.
- `internal/config/history.go` reads a coherent saved pair; normal history and object-version invariants must survive restore.
- `internal/config/views.go` emits secret-free views, but these include presentation metadata such as `isSet` and secret versions. They are not the canonical portable declaration. Add a narrow store export operation rather than exporting presentation JSON or raw database rows.
- `Store.RealizationInput` exports only referenced secret values for rendering. It must not become the backup API: backups with secrets preserve every existing configuration secret, including unused ones.
- There is currently no whole-declaration import operation. Add one atomic saved-state restore operation; do not simulate replacement with a sequence of public create/delete calls that regenerate object IDs or expose partial restores.
- `internal/generation/store.go` records live/pending generation pointers and their source saved versions. Existing preview/apply/confirm/rollback remains the only path to changing networking.
- `internal/engine/socket_linux.go` exposes a mode-0600 Unix socket with verified peer credentials, not an IP listener. Reuse this authorization boundary.
- Current declarations have schema version 1; maximum canonical configuration and secret documents are each 4 MiB. Missing referenced secrets currently make load/view/realization fail. Supporting readable, explicitly unresolved restored references is real work in this stage, not a placeholder.

Mapped against main `4463aa2`; recent changes affect configuration history/recovery verification and the upgrade CLI. Reuse those invariants, not the cold-copy recovery mechanism.

## Portable archive contract

The writer creates a ZIP containing only:

- `manifest.json`;
- `config.json` when the configuration scope is included;
- `configuration-secrets.json` when the configuration-secret scope is included.

Normal creation includes configuration. `--include-secrets` adds configuration secrets and makes the entire ZIP an age-encrypted `.zip.age` file. Creation without that flag produces a secret-free `.zip`; do not add a separate encryption-options framework in this stage. Restore can select either included store or both.

The manifest has its own integer `formatVersion: 1`, a random backup ID, creation time in UTC, source Andaru version, source saved version, included scopes, each document's schema version, and each payload member's exact size and SHA-256 digest. Payload metadata is an allowlisted mapping, not a list of extraction paths. The manifest does not checksum itself. It records the captured live/pending network generation identity, when present, and `savedButUnappliedAreas`.

Currently network realization is the only apply domain. Mark that area saved-but-unapplied when its live generation does not identify the exported saved version; report absent/live-baseline and pending states explicitly. This is conservative version-level information, not a claim of field-level semantic diffs or a portable rendered generation.

`config.json` is a schema-versioned logical declaration with stable object IDs and references, rule/list order and explicit unresolved-reference state when present. It contains no secret values, object-history records, local concurrency tokens or runtime generation files. Source saved-version provenance belongs in the manifest, not in the destination's numbering.

`configuration-secrets.json` is a separate schema-versioned document containing each owned core secret's name, kind and value. It does not replay source-local per-secret version counters. Import assigns destination-local versions and timestamps through the ordinary store commit. Preserve all supported core secrets, including unused ones. Do not claim support for not-yet-implemented shared/extension/user secrets.

Absent scopes are absent, not empty substitutes. This reader rejects unsupported scopes, unknown members and unsupported future document/manifest schemas rather than silently dropping data. Future stores can extend the format deliberately; this implementation is not a universal plugin registry.

## Coherent capture without service interruption

Export runs in the engine. Read the declaration and secrets from the same saved version under the store's existing serialized access boundary. Snapshot live/pending generation provenance under the existing generation lock, using a consistent lock order verified against current writers. Bound lock waits; fail rather than blocking ordinary management indefinitely.

Do not stop the engine, activators or network services, restart anything, change firewall rules, checkpoint/copy live databases, or pause writers while waiting for a passphrase or encrypting. Capture bounded immutable document bytes and metadata, release locks, then build/encrypt the archive.

Changes saved after capture are not included. Return the source saved version and capture time so that this boundary is visible.

## Encryption and secret handling

Use Go's `archive/zip` and the maintained `filippo.io/age` library with its native scrypt passphrase recipient. Do not implement age, ZIP encryption or a custom cryptographic wrapper. Pin/review the actual library version during implementation; do not install dependencies while this design is proposed.

The CLI prompts on the controlling terminal without echo. For creation, require two matching entries and a nonempty passphrase. Limit passphrase input to 1,024 UTF-8 bytes and reject NUL. No passphrase command-line flag, environment variable, persistent passphrase file, shell subprocess or default password is introduced. Without a controlling terminal, secret creation/decryption fails clearly; unattended backup credentials belong to the scheduling stage.

The engine receives the passphrase only in the authenticated Unix request body. Never log request bodies, passphrases, secret documents or archive contents. The CLI writes only status metadata to stdout; archive bytes go to its selected output file. Inspection and error responses do not return secret values.

Keep secret-bearing plaintext ZIP/document material in bounded memory, not scratch files. Read decrypted data to authenticated EOF before using it: a valid early chunk is not proof of an authentic complete archive. Do not commit a restore based on a partially decrypted stream.

Set a finite scrypt decryption work-factor ceiling before processing untrusted input. Author proposal: write at work factor 18 and reject greater factors, corresponding to roughly 256 MiB of scrypt working memory. Accept lower supported factors for interoperability. Allow only one expensive backup-encryption/restore operation at a time; return a clear busy error rather than accumulate work. Qualify this ceiling on the supported VM baselines. Do not silently weaken new-backup encryption to fit a smaller appliance.

Go does not promise complete erasure of every password/plaintext copy. This design prevents intentional disk/log/argument exposure, not disclosure to a compromised root process, swap inspector or core dump. Document that boundary.

Format reference: [age stable specification](https://c2sp.org/age), including authenticated final chunks and bounded scrypt work factors. Library reference: [filippo.io/age](https://pkg.go.dev/filippo.io/age).

## Archive safety and resource limits

Treat every archive, including an administrator-provided file, as hostile. Never call a generic ZIP extraction helper or let an archive name choose a filesystem path.

Author-proposed initial limits, fixed rather than user-configurable:

| Resource | Limit |
| --- | --- |
| Input archive, encrypted or plain | 16 MiB |
| Decrypted ZIP bytes | 12 MiB |
| Archive members | 3 |
| Manifest | 64 KiB |
| Each payload document | 4 MiB |
| Total expanded payload, including manifest | 12 MiB |
| Expansion ratio for compressed members | 100:1 |

The writer uses stored ZIP members, controlled names, regular-file mode 0600, no ownership metadata, executable bits, comments or arbitrary extra fields. Reader support is limited to stored/deflated regular files under these limits. The ratio does not substitute for streamed expanded-byte counting.

Reject absolute names, traversal/dot components, backslashes, NUL, noncanonical names, duplicate/conflicting entries, directory entries, symlinks/special files, unsupported compression, ZIP-native encryption, mismatched sizes, unexpected or unlisted members, missing declared members and checksum failures. Check actual reads/CRC/EOF, not only central-directory claims. Apply strict JSON decoding with duplicate-key, trailing-data, schema, collection/object and string limits. Manifest checksums detect corruption, not authorship; an unencrypted ZIP is not authenticated.

Do not create payload files from member names. Neither archived ownership nor archived mode is applied to the host. A wrong password, malformed input, oversized input or failed validation leaves saved state and live networking unchanged.

## Local output publication

The engine has no arbitrary output-path parameter: it returns archive bytes to the authorized client. The CLI creates the selected file as root with mode 0600 and does not overwrite an existing destination. Reject unsafe/symlink output targets and unsafe parent directories. Write a private same-directory staging file, sync it and publish without replacing an existing path, then sync the parent. Report durability failures rather than claiming success. Remove only this invocation's incomplete staging file.

Administrator-mounted filesystems can be used as local paths; Andaru does not mount or implement NFS/SMB. Their filesystem guarantees may differ and are not equivalent to a complete remote destination backend.

## Restore workflow and selection semantics

The CLI reads the bounded archive once and keeps the same immutable bytes through inspection, preview and commit. It obtains any decryption passphrase without echo. The engine parses/decrypts/validates the entire archive before planning a mutation.

1. Inspect manifest/scopes/provenance and show that restoring saved configuration does not apply it.
2. Select `configuration`, `configurationSecrets`, or both, explicitly. A selected scope must exist in the archive.
3. Preview replacement/deletion counts, unresolved secret references, present-day hardware mismatches, source/target schema compatibility and the current destination saved version. Never reveal secret values or infer that matching names imply consent to reuse secrets.
4. Require a typed `RESTORE` confirmation in the CLI. Keep archive bytes, selected scopes and destination precondition fixed during that confirmation.
5. Submit the same archive and selection with the expected destination saved version. Revalidate and commit atomically; a changed saved head returns a precondition conflict and requires a new preview. No silent retry against a newer head.
6. Return the new saved version and unresolved/warning summary. Use ordinary preview/apply/confirm/rollback separately when the operator chooses to activate it.

No durable restore sessions, approval tokens, database replacement, automatic network apply or automatic reboot are added. Direct API callers are already verified root and remain responsible for presenting a preview/confirmation; the engine always enforces validation, scope selection and saved-head preconditions.

### Configuration and secrets restore independently

- **Both scopes:** replace the current logical declaration and configuration-secret contents with the selected backup contents in one new paired saved version. Retain destination history; preserve imported object identities, references and list order, but assign new local history/version metadata. Removed objects receive proper history entries. A previously seen object ID is not silently regenerated or recorded as a duplicate first creation.
- **Configuration only:** replace the declaration; do not import or delete destination secret values. All imported references that lack imported secret material remain explicitly unresolved, even if a destination secret has the same name. Rebinding to an existing secret requires an explicit subsequent normal edit; names alone are not proof of identity or consent.
- **Secrets only:** replace the configuration-secret contents while preserving the current declaration. Preview lists replacements and missing keys. If replacement would remove a secret required by a currently bound reference, refuse the commit without mutation; the operator must explicitly edit those references first or choose a paired restore. Do not silently modify the unselected declaration or persist dangling bound references. A value from the selected secret scope may satisfy an already bound current reference by its declared key; references already marked unresolved are not silently rebound. The operator explicitly binds them after inspection. No opaque merge or automatic name remapping is provided.

An unresolved reference is a genuine saved state, not a fake empty password. Saved configuration remains readable, editable, exportable and reloadable after engine restart. Its public view reports the secret as unset. Rendering/apply refuses any selected configuration requiring unresolved/missing secrets, with an actionable path/name list that contains no values. Filling/binding the secret through a normal configuration edit resolves it and enables the existing apply path.

Implement explicit binding/unresolved state within the configuration model; the builder chooses the smallest coherent representation and any necessary schema revision. It must distinguish intentionally unresolved references from corrupted secret documents and must not weaken hash, ownership, object-history or secret validation. Supported old schemas need an explicit non-destructive conversion; new unsupported representations must be refused by older engines, with release compatibility notes. Do not relax normal loading into silently substituting empty credentials.

Restoring a malformed/missing secret blob is still corruption. Intentionally unresolved saved references are the only newly allowed incompleteness. Configuration+secrets continue to publish as one coherent generation pair even when only one logical store was selected for replacement.

### Hardware and management safety

Preserve hardware/link identity declarations rather than silently guessing replacements. Preview reports missing/mismatched hardware; saving can retain those unresolved bindings for explicit editing. Existing host-aware preview/apply gates continue to block an unsafe realization. No restore changes active network services, SSH, firewall or the elected management path; a subsequent explicit apply remains subject to normal review, independent rollback and confirmation.

Do not restore generations, live/pending pointers or rollback timers from the archive. An active network apply is not cancelled by backup/restore. The new saved version and current live generation remain separate and are reported as such.

## Root-local API and CLI surface

Proposed command family, following the existing `andaru ctl` convention:

```text
andaru ctl backup create --output FILE [--include-secrets]
andaru ctl backup inspect --input FILE
andaru ctl backup restore --input FILE --stores configuration[,configurationSecrets]
```

Passphrase prompts and the typed restore confirmation use the terminal, not stdout. Failure without a terminal is explicit; there is no unattended confirmation bypass in this stage. Status/preview output follows current JSON conventions; no new visual UI is needed.

Proposed API paths under the existing local `/api/v1` namespace:

- `POST /api/v1/backups`: bounded JSON options (`includeSecrets`, optional required passphrase); returns ZIP/age bytes, never a path or extracted secrets JSON.
- `POST /api/v1/restores/preview`: bounded multipart input containing one archive part and one strict JSON options part; returns a typed preview and expected saved version.
- `POST /api/v1/restores`: the same archive/options plus explicit selected stores and expected saved version; publishes one new saved version.

Inspection is a nonmutating preview with no selected stores. Options JSON is limited to 64 KiB; archive limits above apply before multipart materialization. Reject extra parts, fields, scopes and content types. Do not use automatic multipart disk spooling for secret-bearing data. Errors follow the existing engine error envelope and distinguish malformed input, unsupported schemas/scopes, wrong password/authentication failure, limits, busy, precondition conflict and internal storage failure without including secrets. Non-root peers remain denied.

The builder may settle exact field names and status codes consistently with the existing OpenAPI/CLI contracts, but must record typed request/response schemas with implementation. Add no IP listener, login/session system, remote URL fetch, local server-side path read, arbitrary shell execution or generic file-upload service.

## Qualification and completion criteria

Use failing consumer-visible tests for uncertain edges and focused real round trips, not tests of source text or test-only wiring.

- Round trip actual saved configurations containing every currently supported section, ordered lists, existing/previously deleted IDs, unused secrets and concurrent saves. Capture one coherent pair and restore as one new local version without losing history.
- Default ZIP and debug-safe inspection never contain secret values. `--include-secrets` requires a terminal, explicit selection and a passphrase; complete archive encryption interoperates with an independently used age implementation. Wrong password, tampering and final-chunk truncation cause no mutation.
- Hostile archives cover path/type/duplicate-name attacks, deceptive declared sizes/CRC, unsupported members/schemas, JSON duplicates/trailing data, expansion/member/byte limits and excessive scrypt work factors. Rejection must leave no extracted payload or partial saved state.
- Config-only restore with a colliding local secret name remains unresolved; the existing value is neither leaked nor silently used. Unresolved references survive restart/export and block apply until explicitly repaired. Secrets-only replacement that removes a bound reference's required secret fails atomically; compatible secrets-only and paired restore obey their independent selection contracts.
- Preview/commit races fail on changed saved versions; cancellation and faults before/after paired commit preserve valid history and report actual outcomes. Replaying a committed restore with its old precondition must not create another version.
- Real root-local CLI/API tests verify non-root denial, no passphrase/secret logging, no generic extraction, no destination overwrite, no archive mode/owner restoration and no plaintext scratch files. Do not claim secure memory erasure.
- Package-level smoke and both Debian baseline VMs verify export, altered saved configuration, inspect/restore, engine restart, unresolved-secret repair and ordinary reviewed apply/rollback. Capture evidence that backup and saved-only restore do not restart network services or mutate live rules. Do not execute a management-disrupting apply without the established VM safety snapshot/console procedure.

Before calling this stage complete: update OpenAPI, CLI help, backup/restore and release-compatibility docs, include the new dependency in normal security review, and run the affected suites plus existing required gates. Do not claim users, extensions, schedules, remote retention or full issue #13 completion.

## Remaining issue stages, not part of this implementation

The approved roadmap still includes independent users/credentials, extension restoration, filesystem/S3/SFTP/FTPS destinations, disabled-until-configured daily jobs, visible failures and per-job default retention of 30 successful backups. Their secret, trust, reauthentication and backup-password recovery policies remain in force. This foundation must not pretend those capabilities exist or lock in fake empty representations for them.

## Author calls for approval

- Existing-store root-local archive/restore first; explicit console root authorization for secret export, as selected by the owner.
- Source capture is online and logical, without stopping services; networking activation stays separate.
- Config-only import never automatically reuses same-named local credentials; unresolved state is explicit and apply-blocking.
- Store replacement is atomic and preserves identities/history; no SQLite replacement or bulk public-operation replay.
- Bounded in-memory ZIP/age handling, fixed initial size/work-factor limits and no plaintext scratch files.
- Manual terminal-only secret/passphrase/restore interactions now; no unattended password mechanism or network endpoint.

## Builder-check ruling

The builder asked whether secrets-only replacement may remove a currently referenced secret. Author call: refuse that commit, preserving both stores, rather than silently change an unselected declaration or classify a deliberate dangling reference as database corruption. The operator can first edit bindings or choose a paired restore. Cost: restoring only an older secret set can require an extra explicit step.

## Design review result

The independent builder check and condensed four-pillar review identified the same important secrets-only deletion ambiguity, resolved above. The review also found a nominally minor API-prefix mismatch; it was corrected to the existing `/api/v1` namespace because literal implementation would otherwise produce a second namespace or broken clients. No remaining Important/Critical design finding was reported.

These were read-only source/design reviews, not implementation qualification. Cryptographic interoperability, VM memory headroom, crash durability and network non-disruption remain acceptance work for implementation. No runtime tests, builds or package changes were performed for this design-only change.
