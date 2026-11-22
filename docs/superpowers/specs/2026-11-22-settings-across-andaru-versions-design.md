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

## Implemented: every saved version names the Andaru that wrote it

- A table beside the others, `version_writers`, holds the Andaru version that saved each version. An older engine never reads it and still opens the store.
- The history and each version's view serve it as `writtenBy`. A version has none when its Andaru did not record it: every engine up to `0.5.0-a.4`, also when one runs again after a rollback and saves.
- A release writes its release version, and a nightly will write its date; an image built without one writes `0.0.0-dev`, a pull request's image `0.0.0-prN`, and a bare `go build` writes `development`.

It tells the user which Andaru saved each version. What an engine can load is decided by the schema version alone.

## To build

1. **The loading test (rule 1).** One fixture per released schema version of each store (the configuration, the account database, the audit log and the backup formats), loaded by the current engine in a test. A change that raises a schema version adds a fixture of the previous one. The configuration has one for each of its released schemas, 1 to 3; the other stores are still to be checked.
2. **The decision record (rule 3).** Next to the migrations, keyed by the schema version it leaves: the predicate over the settings, the question and its choices, and the change each choice makes. Built with the first breaking change that needs one.
3. **Safe mode (rule 6).** The engine serves sign-in and the decision pages and applies nothing new until the flows finish; the network keeps its last applied generation. On finishing, it saves the result as a new version and leaves safe mode. An automatic migration alone needs no safe mode, but its result is saved on start the same way, so the store stops holding the old form.
4. **Downgrades (rule 7).** An engine that finds a head of a schema newer than it knows does not refuse the store: it starts from the newest saved version of a schema it can read, saves it as the new head, and says so. With none, it asks for a backup. This also covers a failed boot after step 6 saved under the new schema.
5. **Breaking changes in releases (rule 4).** Each release's notes carry the list. Showing it before upgrading needs Andaru to drive upgrades, a feature of its own.
6. **Before the reboot (rule 5).** With that feature, the running Andaru runs the new image's engine against the saved settings before rebooting, so the new release's migrations and decisions run in the old system and save under the new schema. It ships only in an engine that already has step 4, so that a reboot into the old image before the switch loads the previous settings.

## Released engines

Engines up to `0.5.0-a.4` refuse settings of a form they do not know. Under rule 7 that is expected: going back to one means restoring a backup taken under it.
