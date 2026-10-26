# Versioned data migrations

Astral upgrades data when software is updated, rather than carrying historical readers in normal business code. Application versions and data versions are independent. The first supported public baseline is **Host schema 3 / Core format 10**; earlier private formats and unknown future formats are rejected, not relabelled.

The newest unified Go binary carries every supported historical SQL/Go migration. Upgrading from an old release downloads only the latest release, then applies the missing data steps in order. It does not install intermediate Apps or fetch migration scripts from the network. A software release with no persisted-format change needs no empty migration.

## Ownership and version chains

Host owns `astral.db`, its current `documents`, historical `versions`, encrypted credentials and other business state. Its embedded SQL files live in `internal/persistence/migrations/`; `hostGoMigrations` explicitly registers frozen Go transformations. Host's `PRAGMA user_version` is its format marker.

Core owns `core.db`, including history, projections, runtime state, input identities, receipts and Attempts. Core's pinned module maintains its own embedded Goose chain and offline API. Host never rewrites Core's internal tables directly. Core's `format` table and `PRAGMA user_version` must agree.

Each database has its own `goose_db_version` ledger. Applied rows must form a complete ascending prefix beginning with Goose version 0 and the supported baseline. A valid pre-ledger baseline database can be adopted without transforming its business data; an interrupted initialization containing only version 0 can resume. Duplicates, gaps, downgrades, unsupported formats and inconsistent markers fail inspection.

SQL and Go steps share one sequence. The execution provider disables Goose's global registry and out-of-order execution. Each step's data changes, format markers and ledger entry commit in the same SQLite transaction. A failed step rolls back entirely and stops the chain; earlier successful steps remain committed until the coordinator restores a backup or retries. Nontransactional SQL/Go steps and SQL environment substitution are rejected.

Fresh databases atomically create the current schema and the complete migration ledger. They do not replay historical transformations against already-current data. `persistence.Open` and `sqlite.Open` never upgrade an existing old-format database; normal runtime and Session readers accept only their current format.

## Startup and coordinated recovery

The Server managed by Desktop and the standalone Server use the same startup path:

1. Obtain the Server data directory's exclusive process lock, before opening either database or starting work.
2. Recover a previous interrupted upgrade if `migration-state.json` exists, then inspect both migration chains. Inspection does not create database files, parent directories or original SQLite sidecars. A nonempty crash WAL is checked with a temporary database/WAL copy; this exceptional path needs extra temporary disk space.
3. If either database needs migration, verify the existing Host's original `master.key`, then copy the complete Server directory to a private sibling `upgrade-backups/` snapshot. The manifest records sizes and SHA-256 hashes. Runtime lock/status files, `startup-maintenance.json` and the migration journal are excluded. Computer work files outside the Server directory are not part of this backup.
4. Write the recovery journal before changing data, apply Host and Core migrations, and verify both reached their targets.
5. Open the current stores and finish persistent-state initialization in `Server.NewContext`. At the beginning of `Server.Start`, commit the upgrade and remove the journal **before** starting local control, IM workers or HTTP serving. The committed upgrade records its backup in `last-upgrade.json`.

If migration or persistent-state preparation fails before that commit, close database users and restore the verified snapshot before releasing ownership. A crash before commit leaves the journal so the next startup can retry recovery. After commit, automatic journal recovery no longer restores the old snapshot: acknowledged work accepted through local control, IM or HTTP must not disappear on a later restart. Restore validates all recorded hashes before replacing live data. Goose's per-database transaction alone cannot coordinate Host, Core, keys and files.

The VPS installer separately checks the attempted activation. If its health check fails after commit, it can deliberately restore this attempt's newly recorded snapshot before restarting the old program. It compares the upgrade record rather than restoring an arbitrary historical snapshot. Desktop does not promise to revert its installed App or restore old data for every post-commit startup failure.

This protects local persisted state during an attempted upgrade. It does not undo external messages, file writes on a Computer or other side effects. Restoring a snapshot deliberately returns Server data to that snapshot's time; do not restore an unrelated older snapshot because a later same-format binary update failed.

Desktop normally allows 30 seconds for managed Server startup. A `startup-maintenance.json` marker extends that budget to at most 20 minutes from startup only when its PID matches the managed child and its phase is `preparing-data`. A marker with a different PID or phase does not extend the wait. This temporary marker is removed after data preparation and is excluded from snapshots.

## Offline commands

Stop Desktop or the Server service first. Use the Server directory containing `astral.db`, `core.db` and `master.key`, rather than the parent Astral root or a Computer directory:

```sh
astral migrate --data /path/to/server --status
astral migrate --data /path/to/server
```

Both commands hold the Server's exclusive maintenance lock. `--status` reports Host/Core current and target formats and whether migration is pending. Missing databases stay missing; normal fresh startup initializes them. The explicit migration command commits after the offline conversion and its target checks, without starting a Server. Normal `serve` keeps the journal through store opening and persistent-state preparation, then commits before accepting external work.

For an interrupted upgrade, or a deliberate restore of a verified snapshot:

```sh
astral migrate recover --data /path/to/server
astral migrate restore --data /path/to/server --backup /path/to/upgrade-backups/snapshot
```

`recover` uses an existing interrupted-upgrade journal. `restore --latest` selects the most recently committed upgrade snapshot; it is a recovery tool, not a way to downgrade arbitrary current data. To return to an older data format, stop all users, restore the matching snapshot and use a compatible old program. An old program must never open newer data through a compatibility branch.

Public implementation APIs are `persistence.InspectMigration(ctx, serverDirectory)` / `Migrate(ctx, serverDirectory)` and Core `sqlite.InspectMigration(ctx, databasePath)` / `Migrate(ctx, databasePath)`. The coordinator in `internal/datamigrate` supplies the cross-database snapshot and journal. Library callers must provide the same offline process ownership themselves.

## Adding a migration

Increment the owning data format and add the next consecutive immutable step. Update the fresh schema separately. SQL suits table/index changes; Go suits JSON and history transformations. Register Go steps with `goose.NewGoMigration` and `RunTx`; keep the historical input definitions beside that step rather than importing changing current domain structures or processing helpers.

For Host JSON changes, consider both current documents and historical revisions. Preserve unknown values and integer precision where the conversion does not change them. For Core changes, preserve or deliberately reconstruct the relationships between Entries, Projections, State, Creation, input content digests, receipts and frozen requests. Merely rewriting table-header versions is not a migration.

Released steps cannot be edited, deleted or reordered. Correct a released conversion with a new step. The execution wrapper advances format markers in the same transaction; scripts should not manually advance them. Include tests for skipped releases, SQL/Go ordering, reentry, a failing intermediate step, cold reopen, relevant historical semantics and coordinated recovery. Current synthetic future-format tests validate the executor; they do not invent support for unpublished historical formats.

Actual release publication and a real N→N+1 update, including retained user state and failure recovery, require separate acceptance evidence. Passing migration fixtures or publishing update metadata is not that evidence.
