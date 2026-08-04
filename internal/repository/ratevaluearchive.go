package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// NewRateValueArchiveRepository creates a repository over the archive database's
// rate_values table. db must be a client opened on the archive DSN, not the hot one:
// the two databases carry the same table name in different files, and nothing in the
// type system distinguishes them.
func NewRateValueArchiveRepository(db db) (*RateValueArchiveRepository, error) {
	return &RateValueArchiveRepository{db: db}, nil
}

// RateValueArchiveRepository is the append-only historical tier of rate values: every
// value ever recorded, kept forever, in a separate SQLite file from the hot database.
//
// It deliberately exposes no update and no delete. The archive is written by one
// reconciliation pass that copies rows the hot tier already holds, and read by the
// history queries whose window reaches past the hot horizon. Anything that could
// rewrite or remove history would break the property the whole tier exists for, and
// the property the hot tier's pruning depends on for safety.
type RateValueArchiveRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *RateValueArchiveRepository) Name() string { return "rate_values_archive" }

// CheckUP verifies that the repository can read from the archive's rate_values table.
func (r *RateValueArchiveRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	var count int64
	cmd := "SELECT COUNT(*) FROM " + rateValueTableName + " LIMIT 1;"
	if err = tx.QueryRowContext(ctx, cmd).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return nil
}

// RetainRateValues copies records into the archive and reports how many were new.
//
// Writes are INSERT OR IGNORE keyed on the id carried over from the hot tier, which
// is what makes the reconciliation pass idempotent: re-copying a window that overlaps
// what the archive already holds is free and cannot duplicate or mutate a row. All
// records land in one transaction, so a batch is either wholly archived or wholly
// retried on the next tick.
//
// Timestamps are written in RFC3339, matching the hot tier's format, so the string
// comparison the compound index performs orders identically in both databases.
func (r *RateValueArchiveRepository) RetainRateValues(ctx context.Context, records []domain.RateValue) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "INSERT OR IGNORE INTO " + rateValueTableName + " (" +
		rateValueIdFieldName + ", " +
		rateValueSourceNameFieldName + ", " +
		rateValueBaseCurrencyFieldName + ", " +
		rateValueQuoteCurrencyFieldName + ", " +
		rateValuePriceFieldName + ", " +
		rateValueTimestampFieldName +
		") VALUES (?, ?, ?, ?, ?, ?);"

	stmt, err := tx.PrepareContext(ctx, cmd)
	if err != nil {
		return 0, errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	defer func() { _ = stmt.Close() }()

	inserted := 0
	for i := range records {
		rec := records[i]
		if rec.ID == "" {
			return 0, errors.Join(
				fmt.Errorf("archive: record %d has no id; the archive is keyed on the hot tier's id", i),
				loginjector.NewTraceError(),
			)
		}
		res, execErr := stmt.ExecContext(ctx,
			rec.ID, rec.SourceName, rec.BaseCurrency, rec.QuoteCurrency,
			rec.Price, rec.Timestamp.UTC().Format(time.RFC3339),
		)
		if execErr != nil {
			return 0, errors.Join(execErr, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
		}
		affected, affErr := res.RowsAffected()
		if affErr != nil {
			return 0, errors.Join(affErr, loginjector.NewTraceError())
		}
		inserted += int(affected)
	}

	if err = tx.Commit(); err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	return inserted, nil
}

// ObtainArchiveWatermark returns the newest (timestamp, id) pair the archive holds,
// which is where the next reconciliation pass resumes. ok is false when the archive is
// empty, meaning the pass must start from the beginning of the hot tier.
//
// The id is part of the watermark, not decoration: a collector tick writes many rows
// sharing one timestamp, so resuming on the timestamp alone would either re-read the
// whole tick forever or skip the rest of it. The pair is a keyset cursor.
func (r *RateValueArchiveRepository) ObtainArchiveWatermark(ctx context.Context) (ts time.Time, id string, ok bool, err error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return time.Time{}, "", false, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "SELECT " + rateValueTimestampFieldName + ", " + rateValueIdFieldName +
		" FROM " + rateValueTableName +
		" ORDER BY " + rateValueTimestampFieldName + " DESC, " + rateValueIdFieldName + " DESC" +
		" LIMIT 1;"

	var rawTS, rawID string
	switch scanErr := tx.QueryRowContext(ctx, cmd).Scan(&rawTS, &rawID); {
	case errors.Is(scanErr, sql.ErrNoRows):
		return time.Time{}, "", false, nil
	case scanErr != nil:
		return time.Time{}, "", false, errors.Join(scanErr, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	parsed, err := time.Parse(time.RFC3339, rawTS)
	if err != nil {
		return time.Time{}, "", false, errors.Join(
			fmt.Errorf("archive: parse watermark timestamp %q: %w", rawTS, err),
			loginjector.NewTraceError(),
		)
	}
	return parsed.UTC(), rawID, true, nil
}

// CountRateValues returns the number of rows in the archive. It exists for the
// reconciliation log line and for tests asserting the tier actually filled.
func (r *RateValueArchiveRepository) CountRateValues(ctx context.Context) (int64, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	var count int64
	cmd := "SELECT COUNT(*) FROM " + rateValueTableName + ";"
	if err = tx.QueryRowContext(ctx, cmd).Scan(&count); err != nil {
		return 0, errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return count, nil
}
