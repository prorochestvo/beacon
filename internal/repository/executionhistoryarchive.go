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

// NewExecutionHistoryArchiveRepository creates a repository over the archive database's
// execution_history table. db must be a client opened on the archive DSN, not the hot
// one: both databases carry a table of this name, and nothing in the type system tells
// them apart.
func NewExecutionHistoryArchiveRepository(db db) (*ExecutionHistoryArchiveRepository, error) {
	return &ExecutionHistoryArchiveRepository{db: db}, nil
}

// ExecutionHistoryArchiveRepository is the append-only historical tier of collector
// outcomes: every run ever recorded, kept forever, in a separate SQLite file from the
// hot database.
//
// Like its rate-value counterpart it exposes no update and no delete. It is written by
// the reconciliation pass and read by the failed-run view, whose total is unbounded by
// definition — a count of every failure since the beginning is exactly the number the
// hot tier stops being able to produce once it is pruned.
type ExecutionHistoryArchiveRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *ExecutionHistoryArchiveRepository) Name() string { return "execution_history_archive" }

// CheckUP verifies that the repository can read from the archive's execution_history table.
func (r *ExecutionHistoryArchiveRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	var count int64
	cmd := "SELECT COUNT(*) FROM " + executionHistoryTableName + " LIMIT 1;"
	if err = tx.QueryRowContext(ctx, cmd).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return nil
}

// RetainExecutionHistories copies records into the archive and reports how many were new.
//
// Writes are INSERT OR IGNORE keyed on the id carried over from the hot tier, which is
// what makes the reconciliation pass idempotent: re-copying a window that overlaps what
// the archive already holds is free and cannot duplicate or mutate a row. All records
// land in one transaction, so a batch is either wholly archived or wholly retried on the
// next tick.
//
// Timestamps are written as Unix seconds, matching the hot tier's storage, so the
// integer comparison the indexes perform orders identically in both databases.
func (r *ExecutionHistoryArchiveRepository) RetainExecutionHistories(ctx context.Context, records []domain.ExecutionHistory) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "INSERT OR IGNORE INTO " + executionHistoryTableName + " (" +
		executionHistoryIdFieldName + ", " +
		executionHistorySourceNameFieldName + ", " +
		executionHistorySuccessFieldName + ", " +
		executionHistoryErrorFieldName + ", " +
		executionHistoryTimestampFieldName +
		") VALUES (?, ?, ?, ?, ?);"

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
			rec.ID, rec.SourceName, rec.Success, rec.Error, rec.Timestamp.UTC().Unix(),
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
// The id is part of the watermark, not decoration: a collector tick writes one row per
// source at the same second, so resuming on the timestamp alone would either re-read the
// whole tick forever or skip the rest of it. The pair is a keyset cursor.
func (r *ExecutionHistoryArchiveRepository) ObtainArchiveWatermark(ctx context.Context) (ts time.Time, id string, ok bool, err error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return time.Time{}, "", false, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "SELECT " + executionHistoryTimestampFieldName + ", " + executionHistoryIdFieldName +
		" FROM " + executionHistoryTableName +
		" ORDER BY " + executionHistoryTimestampFieldName + " DESC, " + executionHistoryIdFieldName + " DESC" +
		" LIMIT 1;"

	var (
		rawTS int64
		rawID string
	)
	switch scanErr := tx.QueryRowContext(ctx, cmd).Scan(&rawTS, &rawID); {
	case errors.Is(scanErr, sql.ErrNoRows):
		return time.Time{}, "", false, nil
	case scanErr != nil:
		return time.Time{}, "", false, errors.Join(scanErr, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	return time.Unix(rawTS, 0).UTC(), rawID, true, nil
}

// CountExecutionHistory returns the number of rows in the archive. It exists for the
// reconciliation log line and for tests asserting the tier actually filled.
func (r *ExecutionHistoryArchiveRepository) CountExecutionHistory(ctx context.Context) (int64, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	var count int64
	cmd := "SELECT COUNT(*) FROM " + executionHistoryTableName + ";"
	if err = tx.QueryRowContext(ctx, cmd).Scan(&count); err != nil {
		return 0, errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return count, nil
}
