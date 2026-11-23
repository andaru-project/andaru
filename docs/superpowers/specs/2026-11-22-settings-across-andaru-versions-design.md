# Settings across Andaru versions: upgrades and downgrades

Status: the owner's rules, 2026-11-22 ([#121](https://github.com/andaru-project/andaru/issues/121)). Only "What exists" and "Implemented" below are built. Contracts remain provisional until Andaru 1.0.

## The rules

1. **Older settings always load on a newer Andaru.** This is a testing rule: the current engine must load settings of every schema version a release ever wrote.
2. **Settings name their schema version.** Every stored document carries its schema version, so the engine always knows which form it is loading.
3. **A breaking change has one of two plans, stated explicitly:**
   - **Migration or grandfathering:** automatic. The engine converts the settings, or keeps the old behaviour for settings that relied on it.
   - **User decision:** for each schema version that needs one, a record holds its predicates (which settings need a decision), the question with its choices, and what each choice does to the settings.
4. **Releases list their breaking changes**, and the user sees that list before upgrading.
5. **Upgrades need a reboot.** Where possible, migrations and decisions run before that reboot. The result is saved as a new version of the new schema, and the new Andaru loads it directly.
6. **Otherwise, the engine resolves it on start.** When the latest saved settings need a migration or a decision for the running Andaru, the engine starts in safe mode and runs those flows. Once the choices are made, it processes the settings, saves them for this Andaru, and the upgrade continues, ideally with at most one extra reboot.
7. **Downgrades are not guaranteed to keep the settings.** A downgrade means restoring settings from the version being returned to: a version that Andaru saved, or a backup taken under it.

Releases use semantic versioning; nightlies will be named by their date, as in pfBlockerNG. The settings' schema version, not the Andaru version, decides which migrations and decisions apply, so every breaking change raises it, even one that keeps the document's shape.

Each upgrade is a function of the saved settings and the running Andaru alone, with no further state to keep in step.

### Defaults and grandfathering

A release may change a setting's default, as pfBlockerNG does. A new default does not change the settings of existing users (the owner, 2026-11-22), and grandfathering is how they keep what they had.

- **A changed default may be a breaking change.** Where settings can leave the setting unset, an unset setting reads as the default, so changing it could change what existing settings do. When it is breaking, it raises the schema version like any other breaking change.
- **One grandfathering strategy writes the old default explicitly.** Say a setting defaulted to A and the better choice is now B. The migration writes A into every existing document that left the setting unset, and the new default B applies only to settings created after it, such as a new installation. Existing users keep A until they choose B.
- **Writing the old default is one strategy, not the only one.** Each changed default, added option or removed option states its own plan: a migration, another kind of grandfathering, or a user decision.

## pfBlockerNG

pfBlockerNG is a core part of Andaru, not an add-on, so its settings are Andaru's settings: the same schema versioning, migrations, user decisions and guarantees, with no separate scheme.

- Today pfBlockerNG uses its major.minor version as the settings' schema and has no user decision step: grandfathering has always been possible. Inside Andaru that separation does not exist.
- pfBlockerNG on other platforms has no such guarantees: the platform's settings may have constraints of their own.
- pfBlockerNG as a standalone application (the Core) follows the same rules for its own configuration.

## What exists

- The saved configuration names its form: `schemaVersion`, 4 since the firewall rule model ([firewall rule model](2026-11-22-firewall-rule-model-design.md)). Saved versions are immutable and hashed, and each names its parent ([saved configuration design](2026-11-10-saved-network-configuration-design.md)).
- Versions of schemas 1 to 3 load through an automatic migration on read (`decodeSaved` in `internal/config/legacy.go`); `TestLegacyDocumentReadsAsTheCurrentForm` checks it for schema 1, and `TestLegacyFormsLoad` for schemas 2 and 3. The migration runs on each read, and the store keeps the old bytes until the next save.
- The account database and the audit log name their layout in SQLite's `user_version`; the configuration and account backups carry a `schemaVersion` of their own.
- No user decisions exist yet, and Andaru does not drive upgrades: an image is upgraded with `bootc` directly.

## Implemented: an automatic migration is saved on start

When the engine starts, a head of an older schema is saved as a new version of the current one (`MigrateHead`), with the system as its actor and this Andaru as its writer. The version it came from keeps its bytes in the history, so a rollback starts from it. Until decision records exist, every older head needs a migration alone.

## Implemented: every saved version names the Andaru that wrote it

- A table beside the others, `version_writers`, holds the Andaru version that saved each version. An older engine never reads it and still opens the store.
- The history and each version's view serve it as `writtenBy`. A version has none when its Andaru did not record it: every engine up to `0.5.0-a.4`, also when one runs again after a rollback and saves.
- A release writes its release version, and a nightly will write its date; an image built without one writes `0.0.0-dev`, a pull request's image `0.0.0-prN`, and a bare `go build` writes `development`.

It tells the user which Andaru saved each version. What an engine can load is decided by the schema version alone.

## To build

1. **The loading test (rule 1).** One fixture per released schema version of each store (the configuration, the account database, the audit log and the backup formats), loaded by the current engine in a test. A change that raises a schema version adds a fixture of the previous one. The configuration has one for each of its released schemas, 1 to 3, and its database one as `0.5.0-a.4` left it. Archives of format 1 decode, and a restore takes a configuration of schema 1. The account database (layout 2) and the audit log (layout 1) have released no other layout, and each opens as it is.
2. **The decision record (rule 3).** Next to the migrations, keyed by the schema version it leaves: the predicate over the settings, the question and its choices, the change each choice makes, and the provisional choice that reads the settings until someone answers. The mechanism is built with safe mode (step 3) and tested with a record that exists only in the tests; the first real record comes with the first breaking change that needs one.
3. **Safe mode (rule 6).** Designed in [safe mode for settings that need a decision](2026-11-22-settings-safe-mode-design.md): the engine holds the settings, serves sign-in and the questions, and applies nothing new until they are answered. The network keeps its last applied generation. The answers are saved as a new version, and so is an automatic migration alone, on start (the owner, 2026-11-22).
4. **Downgrades (rule 7)** follow from the architecture: see [downgrades](#downgrades).
5. **Breaking changes in releases (rule 4).** Each release's notes carry the list. Showing it before upgrading needs Andaru to drive upgrades, a feature of its own.
6. **Before the reboot (rule 5).** With that feature, the running Andaru runs the new image's engine against the saved settings before rebooting, so the new release's migrations and decisions run in the old system and save under the new schema. A reboot into the old image before the switch is a downgrade, and starts from the version the old image saved.

## Downgrades

Saved versions are immutable, and an upgrade saves its migrated settings as a new version on top of the ones it read. The settings an older Andaru saved are therefore still in the history after an upgrade, and a downgrade starts from them (the owner, 2026-11-22):

- An engine that finds a head of a schema newer than it knows takes the newest version in the history of a schema it can read, and saves a copy of it as the new head. The newer versions stay in the history.
- The changes saved under the newer Andaru are lost. A rollback normally comes within weeks of the upgrade, so that is a minor inconvenience; a backup taken under the older Andaru remains the other way back.
- With no such version in the history (the store was created by the newer Andaru), the engine refuses the store as today, and only the backup remains.

Implemented: `Open` does this (`fallBack` in `internal/config/store_linux.go`). Every object and list of the copy is recorded as changed in it, so none reads its version from the newer versions in between. An object only the newer versions hold is deleted in it. The newer versions read as ones this Andaru cannot read, except for their roles: the live generation still names one until the next apply, and its roles are trusted only when they match the digest that generation recorded, so signing in keeps working. Engines up to `0.5.0-a.4` predate it and refuse such a store. A portable restore interrupted under the newer Andaru and not settled before the rollback is abandoned ([#160](https://github.com/andaru-project/andaru/issues/160)): its expected or restored version was set aside, so neither half is published again, the identities stay as they are, its audit operation ends not committed, and a replay answers `409 RESTORE_ABANDONED`. So is one whose configuration needs decisions in this Andaru. A journal of a format a newer Andaru wrote is left alone for that Andaru.

## Released engines

Engines up to `0.5.0-a.4` refuse settings of a form they do not know. Under rule 7 that is expected: going back to one means restoring a backup taken under it.
