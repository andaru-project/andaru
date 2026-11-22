# Safe mode for settings that need a decision

Status: proposed, 2026-11-22, for step 3 of [settings across Andaru versions](2026-11-22-settings-across-andaru-versions-design.md) ([#121](https://github.com/andaru-project/andaru/issues/121)). Decision records are built; the hold, its API and the web page are not. The owner asked for safe mode to be built now, so the mechanism comes before the first breaking change that needs it, tested with a decision record that exists only in the tests. The owner's decisions are under "Owner decisions".

## The problem

Rule 6 of the settings design: when the newest saved settings need a user decision before the running Andaru can use them, the engine starts in safe mode, asks, saves the result for this Andaru and carries on.

A version that needs a decision is not only the head. The live generation names the version it was built from, sign-in reads the custom roles from that version, and backups, the history and kept LAN copies all hold versions too. Every one of them must still read on the new engine (rule 1), before anyone answers and whether or not anyone ever does.

## Decision records

A record sits next to the migrations in `internal/config`, keyed by the schema version it upgrades from. It has:

- **predicate:** a function of a document of that schema. It returns the objects the decision is about, or none when the document does not need it.
- **question** and **choices:** text for the user. Each choice has an ID, a label, and a description of what it does.
- **apply:** for each choice, the change it makes to the document. The result is still of that schema.
- **provisional choice:** the choice used to read the document while nobody has answered. It is the one that keeps the old behaviour closest, and where none does, the one that lets less traffic through.

The upgrade from schema *n* to *n+1* applies each matching record of schema *n* with its choice, then the automatic migration from *n* to *n+1*. An upgrade over several schemas repeats this per step, so each predicate sees the document that the steps before it produced with their choices.

## Reading a version nobody has answered for

Every read of a stored document of an older schema (`decodeSaved`: the history, the live version's roles, an export, a backup, a kept copy) upgrades it with the provisional choices. A version always reads, and a reader can tell that it read provisionally: a version's view says so, with the questions it holds.

Implemented: the records are `decisions` in `internal/config/decisions.go`, applied by `decodeSavedWith`, and a version's view lists its `questions`. None exists outside the tests. Schemas 1 to 3 migrate in one step, so a test record of one of them sees the document as saved, with the earlier records applied; a real record only ever comes with a migration from schema 4 on, which adds its own step. An answer to a question the version does not raise is refused.

## The hold

On start, the engine works out the questions for the head, with the provisional choices. With none, it starts as it does today. With some, it holds the settings:

- **The network keeps its last applied generation.** The boot brings the network up from the generation directory, not from the settings, so nothing there changes. An apply that was waiting for confirmation was already rolled back by the reboot.
- **Nothing is applied from the provisional reading.** Applies and saves of the configuration answer `409 SETTINGS_UPGRADE_PENDING`. Reads still work, so the user can see what the settings mean before answering.
- **Sign-in, status, accounts and the audit log work as usual.** Custom roles come from the live version's provisional reading.
- **Restoring a backup works.** A restore saves a new head, and so does a reset that brings back a kept LAN copy. When that head also needs a decision, the hold carries on with its questions.
- **Nothing is kept between starts.** The questions come from the head each time, and the answers are used all at once, so a reboot in the middle asks again. The upgrade stays a function of the saved settings and the running Andaru alone.

## Answering

- `POST /api/v1/config/upgrade/preview` takes the choices given so far and returns the questions still open. A later step's questions can depend on an earlier choice, so the web UI asks again after each answer.
- `POST /api/v1/config/upgrade` takes a choice for every question and `expectedSavedVersion`, the head the questions were worked out from. The engine upgrades the head with those choices, validates the result, and saves it as a new version. The account that answered is its actor, and this Andaru is its writer. An audit event records each choice. The hold then ends in place, without a reboot, since the network was never touched.
- Answering needs `configuration.write`, the capability that writes the configuration. While no account holds it, the hold lasts until an account manager grants it, which works because roles read provisionally, or until a backup is restored. That is no different from a store nobody can write today.

The web UI opens on a "Finish the upgrade" page after sign-in, with one card per question listing its objects and choices, the provisional one marked. Accounts without `configuration.write` see the page read-only. The console's status says the settings are waiting for a decision in the web UI.

## Owner decisions

Decided by the owner, 2026-11-22.

1. **An automatic migration is saved on start** (rule 6). A head of an older schema that needs only a migration is upgraded and saved as a new version for this Andaru when the engine starts, with no hold. The version it came from stays in the history unchanged, so a rollback to the older Andaru starts from it ([downgrades](2026-11-22-settings-across-andaru-versions-design.md#downgrades)). The changes saved under the newer Andaru are not carried back; a rollback normally comes within weeks of the upgrade, so little is lost.
2. **Add-ons stay off during the hold.** The hold is safe mode, and add-ons are off in safe mode. None exist yet; when they arrive, the engine starts them once the hold ends. The boot flag of the [first boot design](2026-11-19-first-boot-safe-mode-resets-design.md) is checked by systemd before the engine has read the settings, so the add-on slice builds that ordering.

## Tests

- A test-only record on a fixture of the previous schema:
  - the predicate matches and the engine holds;
  - reads, sign-in with a custom role, and an export work with the provisional choice;
  - applies and saves refuse;
  - answering with the other choice saves a version whose document matches that choice.
- Two test records on consecutive steps, where the first choice decides whether the second question is asked: the preview shows it, and an answer set that misses it is refused.
- A head that no record matches starts as today, and one that needs only a migration is saved as a new version of the current schema, its parent left unchanged.
- A stale `expectedSavedVersion`, a missing or unknown choice, and an account without `configuration.write` are refused, and nothing is saved.
- A restore of a backup needing the decision keeps the hold, and a reboot during the hold asks the same questions again.

## Not in this design

- The first real decision record, which comes with the first breaking change that needs one.
- Running the flows before the reboot (step 6), which needs Andaru to drive upgrades.
