# Task Breakdown

## Overview

Every write transaction in the project opens as SQLite's DEFERRED mode
(`BeginTx(ctx, nil)`), so it starts as a reader and upgrades to a writer at its first
write statement. When another connection holds the WAL write lock at that moment, the
upgrade fails **immediately** with `SQLITE_BUSY`: SQLite deliberately refuses to invoke
the busy handler on a read-to-write promotion, because two connections both waiting to
promote would deadlock. `busy_timeout=5000` therefore never applies to this path and
cannot, which is why the failures land within a second of process start rather than after
five seconds of waiting. Opening write transactions with `BEGIN IMMEDIATE` takes the write
lock up front, which is not a promotion, so the busy handler is invoked and the existing
five-second retry window starts doing the job it was configured for. Issue #26 proposed
this as one of four options; measurement (see Assumptions) confirms it is the one that
matches the observed evidence.

## Assumptions

- **The mechanism is confirmed by reproduction, not inferred.** A probe against the same
  driver and PRAGMAs, with one connection holding the WAL write lock for 300 ms while a
  second runs the `SELECT`-then-`INSERT` shape of `RetainRateValue`:
  - DEFERRED: fails after **1 ms** with `database is locked (5) (SQLITE_BUSY)`.
  - `_txlock=immediate`: waits **332 ms** and commits cleanly.
  Both the error code and the sub-second timing match production exactly.
- **Issue #26 understates the damage.** It reports one lost `execution_history` row and
  states "the rate value survived". Across the production collector and notifier logs the
  real total is **12 lost rate values** (`could not keep the <N> rate value of <SOURCE>`),
  **5 lost `execution_history` rows**, and **3 lost `RetainRateUserSubscription` writes**
  in the notifier's check agent. The lost rate values are collected data, not audit rows.
- **The error code is not exclusively `517`.** Production carries 23 plain `(5)`, 2 `(517)`
  (`SQLITE_BUSY_SNAPSHOT`), and 2 `(261)` (`SQLITE_BUSY_RECOVERY`, both from client
  start-up PRAGMA application, a separate and much older event). `(5)` and `(517)` are the
  same upgrade race: `517` when the read snapshot has gone stale, `5` when the snapshot is
  still current but the write lock is simply held. Both bypass the busy handler.
- **`modernc.org/sqlite` supports `_txlock` and honours `sql.TxOptions{ReadOnly: true}`.**
  Its `newTx` applies the connection's begin mode only when `!opts.ReadOnly`, so
  `ReadOnlyTransaction` keeps plain `BEGIN` with no change in behaviour. Verified in the
  driver source (`sqlite.go`) and by probe.
- **The repository layer already separates reads from writes correctly.** All 44
  read call sites use `ReadOnlyTransaction`; all 26 `Transaction` call sites are genuine
  writes (`Retain*`, `Remove*`, `Upsert*`, `Advance*`, `Set*`, `Mark*`, `rollover`,
  `pruneArchive`, migrator `Run`). No call site has to be reclassified.
- Collector, notifier, and web all reach the database through the same `SQLiteClient`, so
  a change at that seam covers every writer.

## Tasks

### Task 1: Open write transactions with `BEGIN IMMEDIATE`

- Description: Append `_txlock=immediate` to the DSN built by `connectionOptions` in
  `internal/infrastructure/sqlitedb/config.go`, so every non-read-only `BeginTx` issues
  `BEGIN IMMEDIATE` on every pooled connection. Update the function's doc comment to say
  why the parameter is there, in the same register as the existing PRAGMA explanation.
- Acceptance Criteria:
  - `connectionOptions` emits `_txlock=immediate` alongside the two existing `_pragma`
    parameters, and a test asserts it is present.
  - `SQLiteClient.Transaction` yields a transaction that holds the write lock from `BEGIN`.
  - `SQLiteClient.ReadOnlyTransaction` is unchanged: still plain `BEGIN`, still non-blocking
    against a concurrent writer.
- Pitfalls & edge cases:
  - The parameter belongs on the DSN, not in `NewSQLiteClientEx`'s `db.Exec` PRAGMA loop —
    begin mode is connection state the driver reads at open, and the `db.Exec` path only
    ever reaches one pooled connection.
  - `connectionOptions` must keep working for a DSN that already carries a `?`; the
    existing separator logic covers this and must not regress.
- Complexity: Easy

### Task 2: Stop the health-check ping taking a write lock

- Description: `SQLiteClient.Rollback` opens `BeginTx(ctx, nil)` even though its doc
  comment says it exists "for read-only operations to avoid any unintended writes". With
  Task 1 in place that becomes a writer, and `Ping` — which runs through it, and which the
  `/health/check` inspector calls — would take the WAL write lock on every probe. Open the
  transaction with `sql.TxOptions{ReadOnly: true}` so the helper matches its contract.
- Acceptance Criteria:
  - `Rollback` opens a read-only transaction.
  - `Ping` does not block, and is not blocked by, a concurrent open write transaction.
  - Existing `TestSQLiteClient_Rollback` subtests still pass unchanged.
- Pitfalls & edge cases:
  - The driver does not enforce read-only (`newTx` only skips the begin mode), so any
    hypothetical write inside a `Rollback` action would still execute rather than error —
    the change is strictly about which lock is acquired at `BEGIN`.
  - `SQLiteClient.Commit` must stay a writer; only `Rollback` moves.
- Complexity: Easy

### Task 3: Regression test for the contended write

- Description: Add a test in `internal/infrastructure/sqlitedb` that reproduces the
  production shape — a file-backed WAL database, one connection holding the write lock
  while a second runs a `SELECT`-then-`INSERT` transaction — and asserts the second one
  commits instead of failing with `SQLITE_BUSY`. Include a sibling assertion that a
  read-only transaction still proceeds concurrently, so a future change that makes
  everything a writer is caught.
- Acceptance Criteria:
  - The test fails (`SQLITE_BUSY`) with `_txlock=immediate` removed and passes with it,
    which is what pins the fix rather than the DSN string alone.
  - The database is file-backed under `t.TempDir()`: `:memory:` with
    `SetMaxOpenConns(1)`, as the existing helpers use, cannot express two concurrent
    connections and would pass vacuously.
  - Runs clean under `-race`.
- Pitfalls & edge cases:
  - The holding goroutine must signal that the write lock is actually held (after its first
    `INSERT`, not after `BEGIN`) before the contending transaction starts, or the test is a
    coin flip.
  - Hold the lock for a bounded interval well under `busy_timeout` so the test asserts
    success rather than racing the 5 s ceiling.
  - No `time.Sleep`-based synchronisation for the handshake; the hold interval itself is a
    sleep by necessity, and should be short.
- Complexity: Medium

### Task 4: Correct the `busy_timeout` narrative in `CLAUDE.md`

- Description: The Database section calls `busy_timeout` "the driver-level retry window for
  concurrent writers", which is true only once the transaction is a writer from the start —
  precisely what was not the case. Rewrite that paragraph to state the begin-mode split
  (writes immediate, reads deferred) and why the retry window depends on it. Keep the
  existing `busy_timeout < Timeout` invariant.
- Acceptance Criteria:
  - The Database section names `_txlock=immediate`, its purpose, and the read/write split.
  - No claim survives that `busy_timeout` protects a deferred upgrade.
- Pitfalls & edge cases:
  - The same misleading sentence is echoed in the doc comments of `connectionOptions` and
    `NewSQLiteClientEx`; both must agree with the corrected text.
- Complexity: Easy

## Execution Order

1. Task 3 (write the failing test first — it is the proof the fix is load-bearing)
2. Task 1
3. Task 2
4. Task 4

## Risks

- **Write transactions now serialise from `BEGIN` rather than from their first write.**
  The lock is held for however long the transaction spends reading beforehand. In this
  codebase that prelude is a single indexed `COUNT` in the `Retain*` paths, so the added
  hold is sub-millisecond. The long transactions (`rollover`, `pruneArchive`, migrations)
  already held the lock for their duration and are unaffected.
- **Two write transactions can no longer be open at once in one process.** The second
  `BEGIN` waits for the first, and deadlocks outright if the first is waiting on the
  second. Every repository write opens, writes and commits inside one function — verified
  across all 26 call sites — so no production path does this. One test did, holding four
  concurrent transactions at a barrier to prove per-connection `foreign_keys=ON`; it now
  reserves the pool connections up front instead, which proves the same thing without the
  shape that immediate mode forbids.
- **A genuine `SQLITE_BUSY` becomes a 5-second stall instead of an instant failure.** That
  is the intended trade: the collector's per-source tombstone already tolerates a slow
  source, and `Timeout` (60 s in production) still bounds the whole transaction. It does
  mean a pathological writer degrades latency rather than erroring loudly.
- **SQLite's own `VACUUM` is not covered.** `SQLiteClient.Vacuum` runs outside any
  transaction, so begin mode does not reach it. It has not failed in production, and the
  maintenance pass stamps `last_vacuum_at` only on success, so a busy run retries next
  tick. The five `runners: vacuum failed: database is locked` lines in the notifier log are
  *not* this: they are `RateDispatchAgent.Vacuum` pruning old events through
  `RemoveRateUserEventOlderThan`, an ordinary write transaction, and this change covers
  them.
- **Schedule collision is untouched.** Option 1 of issue #26 (move the notifier off `:00`)
  lives in the host's root crontab, which is not readable or writable from a session and
  needs the user at a root shell. It remains available and independent.

## Trade-offs

- **`_txlock=immediate` on the DSN over `BEGIN IMMEDIATE` at each call site.** The DSN
  applies to every pooled connection the driver opens and cannot be forgotten by a new
  repository method; a per-call-site `Exec("BEGIN IMMEDIATE")` would bypass
  `database/sql`'s transaction bookkeeping entirely. The cost is that the mechanism is a
  string in a DSN rather than a visible keyword at the call site, which Task 4 addresses by
  documenting it where the reader will be.
- **Option 2 over option 3 (retry the whole transaction).** Retrying requires every write
  path to be idempotent on replay — a much larger claim than the fix is worth, and one that
  would have to be re-proved for each new repository method. Taking the lock up front needs
  no such property.
- **Option 2 over option 4 (leave it).** Leaving it was defensible while the cost was
  believed to be one audit row. At 12 lost rate values it is not.
