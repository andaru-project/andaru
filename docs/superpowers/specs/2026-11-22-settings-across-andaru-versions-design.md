# Settings across Andaru versions: rollback and upgrading again

Status: a draft for the owner, 2026-11-22. The direction is the owner's ([#121](https://github.com/andaru-project/andaru/issues/121)); the calls below are the author's and await review. Only "Step 1" is implemented. Contracts remain provisional until Andaru 1.0.

## The problem

An image can go back: `bootc rollback` boots the previous deployment. The settings do not go back with it. An older engine may then meet settings a newer Andaru wrote, in a form it does not know. Today it refuses the store and does not serve.

## The owner's direction

1. **Compatibility first.** A change avoids breaking readers in either direction wherever it can.
2. **The saved versions are segmented by the Andaru version that wrote them.** They are immutable and under version control already, so "the settings as this Andaru last had them" is the latest version it wrote.
3. **The user chooses, and is always told.** When an engine loads settings a newer Andaru wrote, the user chooses between going back to the latest configuration this Andaru wrote, and resolving the differences with the newer one. What an engine could not read is said, even when it could carry on.

## What the configuration store already gives

- Saved versions are immutable, hashed, and each names its parent ([saved configuration design](2026-11-10-saved-network-configuration-design.md)).
- A change of form rewrites nothing: versions of an earlier form are read through an upgrade on load ([firewall rule model](2026-11-22-firewall-rule-model-design.md)).
- A version's parent need not be the version before it, so two lines of history can live in one store.

## Step 1, implemented: every saved version names its writer

- A table beside the others, `version_writers`, holds the Andaru version that saved each version. An older engine never reads it and still opens the store.
- The history and each version's view serve it as `writtenBy`. A version saved before this has none.
- The engine writes its build's version; a development build writes `development`.

This lands before the next release, so that every version saved from then on can be told apart. Nothing else changes yet: an engine still refuses a head of a form it does not know.

## Step 2, proposed: an engine that finds a newer Andaru's settings

On opening the store, the engine looks at the saved head:

- **Written by this Andaru or an older one:** as today.
- **Written by a newer Andaru:** the engine does not refuse the store. It serves, in a state the API and the web UI show on every page, like the pending notice: "These settings were last saved by Andaru X, which is newer than this one."
  - It works from **the latest version this Andaru can read**: the newest version of a form it knows. That is normally the latest one it wrote itself.
  - Applying is not offered until the user has chosen.
- **The choice:**
  - **Go back to this Andaru's settings.** The engine saves a new version whose content is the latest version it wrote, with that version as its parent. The newer Andaru's versions stay in the store, untouched, as a second line of history.
  - **Keep the newer settings,** when the engine can read them: their form is one it knows, so nothing is lost. It then says what it does not understand, if anything, and carries on from the newer head.
  - **Resolve the differences,** when it can read both: the same comparison the Review and apply page makes between two versions, object by object, with the user picking a side for each object that differs.
  - When the newer head is of a form this engine cannot read, only the first is possible, and the page says why.

## Step 3, proposed: upgrading again

The newer Andaru boots again and finds that the head was saved by an older Andaru on top of a line it left behind. It offers the same three: carry on with the current settings, go back to the latest it wrote itself, or resolve the differences. Both lines are readable to it, so all three are always possible.

## Open for the owner

1. **Comparing Andaru versions.** "Newer" needs an order between `0.5.0-a.4`, `0.5.0`, a nightly and `development`. Proposed: the release order of the version strings, with a nightly and a development build treated as newer than any release they cannot be compared with, so that they ask instead of assuming.
2. **What "this Andaru" means.** Proposed: the exact version string. A patch release then asks once after an upgrade done again; the alternative is to group by the settings' form, which asks less often and is less exact.
3. **The other stores.** The account database, the audit log and the web preferences have no version history. Proposed: the account database gets the snapshot the issue first described (a copy at the old layout before a migration, which an older engine opens), since accounts must work for anyone to make the choice above; the audit log is append-only and is left alone; preferences are dropped when unreadable, and the user is told.
4. **Engines already released,** up to `0.5.0-a.4`, refuse a newer form. The first upgrade past them cannot be rolled back after a save, whatever is built here. Proposed: the release notes of the next release say so.
5. **Whether an apply is blocked until the choice is made,** as proposed, or only warned about.
