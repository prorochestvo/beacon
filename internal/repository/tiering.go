package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prorochestvo/loginjector"
)

// The append-only telemetry tables are each split into a bounded hot working set and an
// archive twin of identical schema, living in the same database file.
//
// The same file is the load-bearing choice. SQLite gives no atomicity to a transaction
// spanning attached databases under journal_mode=WAL, so tiers in separate files could
// only be reconciled by copying and verifying — leaving the archive a permanent superset,
// and every read responsible for deciding which tier owns a window. In one file the
// roll-over is an INSERT...SELECT followed by a DELETE inside a single transaction: a row
// is in exactly one tier at every observable instant, no duplication, and reads simply
// union the two.
//
// What it costs is that VACUUM operates on the whole file, so it needs transient free
// space on the order of the file's size. That is why the maintenance pass gates it on a
// cadence instead of running it whenever rows were freed.

// RolloverToArchive moves rate values older than cutoff into rate_values_archive.
// A zero cutoff is a no-op.
func (r *RateValueRepository) RolloverToArchive(ctx context.Context, cutoff time.Time) (int64, error) {
	return rollover(ctx, r.db,
		rateValueTableName, rateValueArchiveTableName,
		rateValueTimestampFieldName, rateValueColumnList,
		cutoff.UTC().Format(time.RFC3339), cutoff.IsZero(),
	)
}

// PruneArchive deletes archived rate values older than cutoff. A zero cutoff keeps
// everything, which is the configured behaviour.
func (r *RateValueRepository) PruneArchive(ctx context.Context, cutoff time.Time) (int64, error) {
	return pruneArchive(ctx, r.db,
		rateValueArchiveTableName, rateValueTimestampFieldName,
		cutoff.UTC().Format(time.RFC3339), cutoff.IsZero(),
	)
}

// RolloverToArchive moves execution records older than cutoff into
// execution_history_archive. A zero cutoff is a no-op.
func (r *ExecutionHistoryRepository) RolloverToArchive(ctx context.Context, cutoff time.Time) (int64, error) {
	return rollover(ctx, r.db,
		executionHistoryTableName, executionHistoryArchiveTableName,
		executionHistoryTimestampFieldName, executionHistoryColumnList,
		cutoff.UTC().Unix(), cutoff.IsZero(),
	)
}

// PruneArchive deletes archived execution records older than cutoff. A zero cutoff keeps
// everything, which is the configured behaviour.
func (r *ExecutionHistoryRepository) PruneArchive(ctx context.Context, cutoff time.Time) (int64, error) {
	return pruneArchive(ctx, r.db,
		executionHistoryArchiveTableName, executionHistoryTimestampFieldName,
		cutoff.UTC().Unix(), cutoff.IsZero(),
	)
}

// rollover moves every row older than cutoff from the hot table into its archive twin, in
// one transaction, and reports how many rows moved.
//
// The INSERT runs before the DELETE and both are bound to the same cutoff, so the pair is
// either wholly applied or wholly rolled back — the archive can never end up holding a row
// the hot tier still has, or the reverse.
//
// A plain INSERT rather than INSERT OR IGNORE: because the move is atomic there is no
// legitimate way for a row to already be in the archive, so a primary-key collision means
// something is wrong with an assumption and should abort the transaction loudly rather
// than be swallowed while the DELETE removes the hot copy anyway.
//
// A zero cutoff is a no-op returning 0. That is defence in depth against a caller that
// computed its window from a zero clock or a missing config value: with a zero cutoff the
// predicate would match nothing here anyway, but the explicit guard means a future change
// to the comparison cannot silently turn "no window configured" into "archive everything".
func rollover(
	ctx context.Context, db db, hotTable, archiveTable, timestampField, columnList string, cutoff any, zero bool,
) (int64, error) {
	if zero {
		return 0, nil
	}

	tx, err := db.Transaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	insert := "INSERT INTO " + archiveTable + " (" + columnList + ")" +
		" SELECT " + columnList + " FROM " + hotTable +
		" WHERE " + timestampField + " < ?;"
	if _, err = tx.ExecContext(ctx, insert, cutoff); err != nil {
		return 0, errors.Join(err, fmt.Errorf("SQL: %s", insert), loginjector.NewTraceError())
	}

	del := "DELETE FROM " + hotTable + " WHERE " + timestampField + " < ?;"
	res, err := tx.ExecContext(ctx, del, cutoff)
	if err != nil {
		return 0, errors.Join(err, fmt.Errorf("SQL: %s", del), loginjector.NewTraceError())
	}

	moved, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}

	if err = tx.Commit(); err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	return moved, nil
}

// pruneArchive deletes archived rows older than cutoff and reports how many went.
//
// A zero cutoff means keep everything, which is beacon's configured behaviour: the archive
// exists so history is never lost. The path is here as the escape hatch for a disk that
// genuinely runs out, and it is the only code in the project that can destroy history — it
// stays off unless a retention window is explicitly configured.
func pruneArchive(
	ctx context.Context, db db, archiveTable, timestampField string, cutoff any, zero bool,
) (int64, error) {
	if zero {
		return 0, nil
	}

	tx, err := db.Transaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	del := "DELETE FROM " + archiveTable + " WHERE " + timestampField + " < ?;"
	res, err := tx.ExecContext(ctx, del, cutoff)
	if err != nil {
		return 0, errors.Join(err, fmt.Errorf("SQL: %s", del), loginjector.NewTraceError())
	}

	pruned, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}

	if err = tx.Commit(); err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	return pruned, nil
}
