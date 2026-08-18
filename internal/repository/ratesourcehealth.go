package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prorochestvo/loginjector"
)

// RateSourceHealthRepository stores which sources have already been alerted about.
//
// It holds a latch, not a measurement: whether a source is healthy is derived from
// execution_history on every run, and this table only remembers whether the current
// outage has already been announced.
type RateSourceHealthRepository struct {
	db db
}

// NewRateSourceHealthRepository returns a repository for the rate_source_health table.
func NewRateSourceHealthRepository(db db) (*RateSourceHealthRepository, error) {
	return &RateSourceHealthRepository{db: db}, nil
}

// Name returns the name of the underlying database table.
func (r *RateSourceHealthRepository) Name() string { return rateSourceHealthTableName }

// CheckUP verifies that the repository can read from the rate_source_health table.
func (r *RateSourceHealthRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	var count int64
	cmd := "SELECT COUNT(*) FROM " + rateSourceHealthTableName + ";"
	if err = tx.QueryRowContext(ctx, cmd).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return nil
}

// ObtainAlertedSources returns the set of source names currently latched as alerted,
// keyed by name with the moment the alert went out.
//
// The whole table is read at once because the caller evaluates every source on each run:
// one query beats one per source, and the table holds at most one row per source that is
// currently broken — normally none.
func (r *RateSourceHealthRepository) ObtainAlertedSources(ctx context.Context) (map[string]time.Time, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "SELECT " + rateSourceHealthSourceNameFieldName + ", " + rateSourceHealthAlertedAtFieldName +
		" FROM " + rateSourceHealthTableName + ";"

	rows, err := tx.QueryContext(ctx, cmd)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	result := make(map[string]time.Time)
	for rows.Next() {
		var (
			name  string
			rawTS string
		)
		if scanErr := rows.Scan(&name, &rawTS); scanErr != nil {
			return nil, errors.Join(scanErr, loginjector.NewTraceError())
		}
		parsed, parseErr := time.Parse(time.RFC3339, rawTS)
		if parseErr != nil {
			return nil, errors.Join(
				fmt.Errorf("rate source health %s: invalid alerted_at %q: %w", name, rawTS, parseErr),
				loginjector.NewTraceError(),
			)
		}
		result[name] = parsed.UTC()
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	return result, nil
}

// RetainAlertedSource latches a source as alerted, recording when. Re-latching an
// already-latched source overwrites the stamp rather than failing, so a caller that lost
// track of the state cannot deadlock on it.
func (r *RateSourceHealthRepository) RetainAlertedSource(ctx context.Context, sourceName string, at time.Time) error {
	if sourceName == "" {
		return errors.Join(errors.New("rate source health: source name is empty"), loginjector.NewTraceError())
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "INSERT INTO " + rateSourceHealthTableName +
		" (" + rateSourceHealthSourceNameFieldName + ", " + rateSourceHealthAlertedAtFieldName + ")" +
		" VALUES (?, ?)" +
		" ON CONFLICT(" + rateSourceHealthSourceNameFieldName + ") DO UPDATE SET " +
		rateSourceHealthAlertedAtFieldName + " = excluded." + rateSourceHealthAlertedAtFieldName + ";"

	if _, err = tx.ExecContext(ctx, cmd, sourceName, at.UTC().Format(time.RFC3339)); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	if err = tx.Commit(); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

// RemoveAlertedSource clears the latch. Removing a latch that is not there is not an
// error: the caller clears on recovery, and a source that was never alerted about has
// nothing to recover from.
func (r *RateSourceHealthRepository) RemoveAlertedSource(ctx context.Context, sourceName string) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM " + rateSourceHealthTableName +
		" WHERE " + rateSourceHealthSourceNameFieldName + " = ?;"

	if _, err = tx.ExecContext(ctx, cmd, sourceName); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	if err = tx.Commit(); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

const (
	rateSourceHealthTableName           = "rate_source_health"
	rateSourceHealthSourceNameFieldName = "source_name"
	rateSourceHealthAlertedAtFieldName  = "alerted_at"
)
