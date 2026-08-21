---
name: beacon-storage
description: Beacon's SQLite storage rules beyond the basics — the hot/archive tiering of rate_values and execution_history (why one file, why reads UNION both tiers and writes touch only hot, roll-over, retention, VACUUM), the migrator contract and the immutable migration filename convention, columns that look droppable but are not, why weather_forecast_days is bounded rather than tiered, why historical migration tests must not seed through a repository, and how to read production data out of a gzipped snapshot. Load before writing or reviewing any query in internal/repository or internal/infrastructure/sqlitedb, adding or altering a migration under ./migrations, touching collection.MaintenanceAgent, sqlitedb.Migrator, Transaction/ReadOnlyTransaction, RetainRateSource, rate_source_health, weather_observations, weather_forecast_days or RetainWeatherForecastDays, writing a test against stubSQLiteDBThrough, or inspecting the production database.
---

# Beacon storage

Read this before any repository query, any migration, or any attempt to look at
production data.

## Hot / archive tiering

The two append-only telemetry tables are each split into a bounded **hot** working set and
an **`*_archive`** twin of identical schema — `rate_values`/`rate_values_archive`,
`execution_history`/`execution_history_archive` — **in the same database file**.

The same file is the load-bearing choice. SQLite gives no atomicity to a transaction
spanning attached databases under `journal_mode=WAL`, so tiers in separate files could only
be reconciled by copy-and-verify, leaving the archive a permanent superset and every read
responsible for deciding which tier owns a window. In one file the roll-over is
`INSERT...SELECT` + `DELETE` inside a single transaction: a row is in exactly one tier at
every observable instant, and reads just union the two. Separate files were tried and
abandoned (PRs #10/#11/#12, closed unmerged).

- **Reads span both tiers, unconditionally.** `rateValueSqlSelect` /
  `executionHistorySqlSelect` read a `UNION ALL` of hot and archive rather than picking a
  tier by how far back the caller asked, so results match the untiered behaviour wherever
  the boundary sits and however far the roll-over has fallen behind — there is no horizon
  for a read to disagree with. SQLite pushes the `WHERE` into both branches, so each rides
  its own compound index (pinned by `TestTieredReadsUseIndexesOnBothBranches` via `EXPLAIN
  QUERY PLAN`). The union gives up index-ordered output, which is why every ordered read
  carries an `id` tie-break and is bounded by a window, a limit or a page.
- **Writes are hot-only**, and their INSERT-vs-UPDATE existence checks use
  `rateValueCountHot` / `executionHistoryCountHot`. Counting the union there would send an
  id that lives solely in the archive down the UPDATE path, which matches nothing.
- **`rate_values_archive` carries no foreign key** to `rate_sources`. History outlives the
  sources that produced it; `ON DELETE CASCADE` would erase it when a dead source is
  removed. The hot row still cascades — that is the point.
- **`collection.MaintenanceAgent`** (collector tick, after collection) runs three ordered
  steps: roll over rows older than `DefaultHotWindow` (180 days), apply
  `DefaultArchiveRetention` (**0 — keep forever**, the configured value), then
  `MaybeVacuum` on `DefaultVacuumInterval` (7 days).
- **VACUUM is not optional.** Deleting rows frees pages *inside* the file; SQLite never
  returns them to the OS on its own, so without VACUUM the roll-over shows up as exactly
  zero change in `df`. Cadence-gated through `service_meta.last_vacuum_at` because it
  rebuilds the database into a temporary copy, needing transient free space on the order of
  the file's own size. The stamp is written **only after a successful run** — stamping
  first would turn a transient `SQLITE_BUSY` into a skipped week.
- Retention is a **compile-time constant, not an env var**: it is the only setting in the
  project that can destroy history, and a value that changes only through a reviewed commit
  is harder to get wrong than one that changes through a typo in an env file.

The roll-over has never fired in production — the oldest data is younger than the 180-day
window. Watch for `maintenance: archived N row(s)` with a non-zero N.

## Migrations

`cmd/migrator` is the **only** thing that mutates schema. It embeds
`migrations.MigrationsFS` at build time, opens the DB via `BEACON_SQLITEDB_DSN`, and calls
`sqlitedb.Migrator.Run(ctx)`. Idempotent: applied filenames are tracked in
`__schema_migrations`.

After applying, the migrator calls `Migrator.Verify(ctx)` and exits non-zero when the
ledger does not account for every embedded migration, or when any migration file is empty
(`Run` skips empty content silently, so a truncated `.sql` would otherwise be a permanent
invisible no-op). The `beacon-migrate` unit is `Type=oneshot` with `RemainAfterExit=no`, so
`systemctl start` propagates that exit code and the release job fails — schema drift
surfaces at deploy time rather than at the first query against a missing column.

Service binaries (`cmd/web`, `cmd/collector`, `cmd/notifier`) DO NOT migrate on startup.
They call `sqlitedb.RequireMigratedSchema(ctx, db)` immediately after opening the DB; a
missing or empty `__schema_migrations` table is fatal:

```
log.Fatalf("schema not initialised: run cmd/migrator before starting the service")
```

Migration files live at `./migrations/*.sql`. Filename convention:
`<YYYYMM>.<NNN>.<table>.<description>.sql` (e.g.
`202605.001.rate_sources.table_initiate.sql`). The `<NNN>` segment is a **global**
zero-padded counter across all tables — files are applied in lexicographic order, which the
naming makes the execution order. Once applied to any production database the filename is
**immutable**: renaming triggers a duplicate apply.

The sibling Go file `./migrations/embed.go` (`package migrations`) exposes those files as
`var MigrationsFS embed.FS` so they can be consumed without disk I/O at runtime.

Repository files in `internal/repository/` reference table and column names exclusively
through `const` declarations (e.g. `rateSourceTableName`, `rateSourceNameFieldName`) so a
schema rename surfaces at compile time and via `grep`, never via a runtime "no such column"
error.

### `weather_forecast_days` is bounded, not tiered

The long-range forecast table is a **bounded working set**, `locations × 16` rows, upserted
in place on the natural key `(location_id, provider, forecast_date)`. The tiering rule above
governs append-only telemetry and does not apply here: there is nothing an `*_archive` twin
could hold, no roll-over, and no reason for a read to union two branches. Do not "fix" that.

Three things about it that are decisions rather than omissions:

- **A whole fetch is one transaction.** `RetainWeatherForecastDays` writes all sixteen rows
  under one `BEGIN`: a day's forecast is a single observation of the future, and the write
  lock is taken at `BEGIN` (`_txlock=immediate`), so sixteen transactions would take and
  release it sixteen times per location against three processes sharing the file.
- **Retention is keyed on `forecast_date`, never on `captured_at`.** Rows are superseded
  while the day is still ahead and dropped once it is behind. A `captured_at` sweep — which
  is what `weather_observations` uses — would delete a still-future day the moment its
  location stopped being refreshed.
- **No foreign key to `weather_user_cities`.** A location whose last subscriber leaves stops
  being refreshed and ages out within the horizon; cascading would tie the lifetime of
  public meteorological data to one user's subscription row.

### Historical migration tests must not go through a repository

`weatherusercity_backfill_test.go` and `weatherusercity_backfillrain_test.go` exercise
migrations 021 and 026 against a snapshot of the schema **as it was when those migrations
were written** (`stubSQLiteDBThrough`), because both reference columns that later migrations
drop. Seeding or reading such a snapshot through `WeatherUserCityRepository` fails on every
column added afterwards: its SQL always names the current schema. Use
`seedHistoricalWeatherUserCity` / `obtainHistoricalWeatherUserCities` in `main_test.go`,
whose column list (`weatherUserCityEraColumns`) is frozen to that era on purpose.

### Two columns a migration must not "clean up"

- **`weather_observations.provider`** now only ever holds `'open-meteo'`, so it reads as
  dead weight. It is not: it partitions two composite indexes. It was retained rather than
  dropped precisely to avoid rebuilding the largest weather table for zero functional gain,
  and dropping it degrades those indexes silently.
- **`rate_sources` holds configuration, never runtime state.** `RetainRateSource` rewrites
  those rows wholesale — `cmd/doctor rulegen` does exactly that — so any runtime column
  added there is destroyed by an unrelated config write. That is why the source-health latch
  lives in its own `rate_source_health` table, and why the next piece of per-source runtime
  state needs its own table too.

Deploy flow:

```
make build         # builds all binaries including ./build/migrator
make migrate       # applies any pending .sql files (no-op if up to date)
make run           # starts collector, notifier, web
```

## Reading production data

The live DB is root-owned `0600`; the daily gzipped snapshots in `/opt/beacon/backups/` are
world-readable, so inspection goes through those. `sqlite3 -readonly <snapshot>` **fails**
with `attempt to write a readonly database (8)` — `journal_mode=WAL` lives in the file
header, so SQLite wants `-shm`/`-wal` sidecars the backup directory does not allow. Use a
URI with `immutable=1`, which skips WAL setup:

```
sqlite3 "file:/opt/beacon/backups/beacon.<YYYYMMDD>.sqlite?immutable=1" "<query>"
```

`make db-inspect` does the whole dance (stream, decompress, open locally, so the host needs
neither `sqlite3` nor scratch space); `ARGS="<sql>"` runs one query instead of opening a
shell. `make backups` pulls the same snapshot plus the logs into `./backups/`, which is the
off-host restore point. Both print the snapshot's age, because snapshots are cut at 00:00
UTC: one older than the last deploy cannot confirm that deploy's migrations.

**Cutting a snapshot on demand needs root on the host**, so it cannot be driven from a
workstation and has deliberately no Make target: the live DB and `sqlite_dump.sh` are `0600
root:root`, and the SSH account (`pi5_aide`) has no passwordless sudo. Run
`/opt/beacon/backups/sqlite_dump.sh` as root, then `make backups` locally. The narrow
`NOPASSWD` sudoers line that would automate it is documented in `deploy/README.md` and is
not installed on purpose.
