# Safe mode for settings that need a decision

Status: proposed, 2026-11-22, for step 3 of [settings across Andaru versions](2026-11-22-settings-across-andaru-versions-design.md) ([#121](https://github.com/andaru-project/andaru/issues/121)). Nothing here is built. The owner has yet to settle the choices under "Owner decisions".

## The problem

Rule 6 of the settings design: when the newest saved settings need a user decision before the running Andaru can use them, the engine starts in safe mode, asks, saves the result for this Andaru and carries on. No breaking change needs a decision yet, so this builds the mechanism and tests it with a decision that exists only in the tests. The first real breaking change then adds a record and nothing else.

## Decision records

A record sits next to the migrations in `internal/config`, keyed by the schema version it upgrades from:

- **predicate:** a function of the saved document in that schema. It returns the objects the decision is about, or none when the settings do not need it.
- **question** and **choices:** text for the user. Each choice has an ID, a label, and a description of what it does.
- **apply:** for each choice, the change it makes to the document. The result must be a valid document of the next schema.

An upgrade from schema *n* to the current one runs, for each step, the automatic migration and then each record of that step whose predicate matches. Every predicate sees the document as the steps before it left it, so the questions are worked out once and in order.

## Starting with a pending decision

On start, the engine reads the head version and works out the questions for it. With none, it carries on as it does today. With some, it holds the settings:

- **The network keeps its last applied generation.** The boot already brings the network up from the generation directory, not from the settings, so nothing there changes.
- **Nothing new is applied.** Every write to the configuration and every apply answers `409 SETTINGS_UPGRADE_PENDING`. The configuration's views answer the same, because the settings have no current form until the questions are answered.
- **What still works:** sign-in, status, the dashboard's status widgets, accounts, the audit log, backups, and restoring a backup. Restoring is the way out for a user who would rather go back to settings saved by another version (rule 7).
- **The questions:** `GET /api/v1/config/upgrade` lists the schema being left, the schema being reached, and each question with the objects it is about and its choices. `POST /api/v1/config/upgrade` takes one choice per question and the head version the questions were worked out from, as `If-Match`.
- **Finishing:** the engine runs the migrations with the choices, validates the result, and saves it as a new version. The account that answered is its actor, and this Andaru is its writer. An audit event records each choice. Then the hold ends without a reboot, since the network was never touched.
- **Nothing is kept between starts.** The questions come from the head each time, and the answers are used all at once, so a reboot in the middle asks again. This keeps the upgrade a function of the saved settings and the running Andaru alone.

Answering needs `configuration.write`, the capability that saves a version. An account without it sees the hold and who can lift it.

The web UI opens on a "Finish the upgrade" page after sign-in, with one card per question listing its objects, and the navigation shows only the pages that still work. The console's status says the settings are waiting for a decision in the web UI.

## Owner decisions

1. **Add-ons during the hold.** Recommended: they stay off, as in the safe mode of the [first boot design](2026-11-19-first-boot-safe-mode-resets-design.md), and start once the hold ends. Their units are rendered from settings the engine has not accepted yet, so it is one safe mode with two causes. The alternative keeps them running on what was last applied, like the network.
2. **Saving an automatic migration on start.** The settings design's step 3 says that settings needing only an automatic migration are also saved as a new version on start. Recommended: drop that and keep today's behaviour, where the old bytes stay until the next save. Saving on start writes schema 4 at the first boot of the new image. If that image then fails and `bootc rollback` boots `0.5.0-a.4`, which knows schemas 1 to 3, the older engine refuses the store, and the box needs a factory reset and a restored backup. Without the save, the rollback keeps working until someone saves. A decision has to save, so the hold accepts that cost only when a question was answered.
3. **No extra reboot.** Recommended as above: the hold ends in place. Rule 6 allows one extra reboot; it is only needed if decision 1 goes the other way and add-ons cannot simply be started.

## Tests

- A test-only record on a fixture of the previous schema: the predicate matches, the engine holds, the writes and applies refuse, and answering saves a version whose document matches the choice's change.
- A head that no record matches starts as today.
- A stale `If-Match`, a missing or unknown choice, and an account without `configuration.write` are refused, and nothing is saved.
- A reboot during the hold asks the same questions again.

## Not in this design

- The first real decision record, which comes with the first breaking change that needs one.
- Running the flows before the reboot (step 6), which needs Andaru to drive upgrades.
