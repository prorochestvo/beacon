package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/domain/identity"
)

// NewExecutionHistoryRepository returns a repository for the execution_history table.
func NewExecutionHistoryRepository(db db) (*ExecutionHistoryRepository, error) {
	return &ExecutionHistoryRepository{db: db}, nil
}

// ExecutionHistoryRepository persists and retrieves domain.ExecutionHistory records.
type ExecutionHistoryRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *ExecutionHistoryRepository) Name() string { return executionHistoryTableName }

// CheckUP verifies that the repository can read from the execution_history table.
func (r *ExecutionHistoryRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := executionHistoryCount(tx, ctx, ";")
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	if count < 0 {
		err = errors.New("unexpected result")
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	return nil
}

// ObtainLastNExecutionHistoryBySourceName returns at most limit execution history records
// for the given source, ordered newest-first. When successOnly is true, only successful
// (success=1) rows are returned. Always returns a non-nil slice on success.
func (r *ExecutionHistoryRepository) ObtainLastNExecutionHistoryBySourceName(ctx context.Context, sourceName string, limit int64, successOnly bool) ([]domain.ExecutionHistory, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	whereClause := executionHistorySourceNameFieldName + " = ?"
	if successOnly {
		whereClause += " AND " + executionHistorySuccessFieldName + " = 1"
	}

	rows, err := executionHistoryQueryContext(tx, ctx, "WHERE "+whereClause+" ORDER BY "+executionHistoryTimestampFieldName+" DESC, "+executionHistoryIdFieldName+" DESC LIMIT ?;", sourceName, limit)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	return rows, nil
}

// ObtainLastNExecutionHistoryBySourceNameBefore is
// ObtainLastNExecutionHistoryBySourceName resumed from a cursor: up to limit rows for
// the source, newest first, strictly older than the (before, beforeID) pair. A zero
// before means no cursor, which makes it identical to the uncursored call.
//
// It exists so a tiered read can top up a short hot-tier answer from the archive without
// re-reading or duplicating what the hot tier already returned. successOnly is carried
// through because the cursor has to walk the same filtered sequence the first page did —
// resuming an errors-included page inside a successes-only one would skip rows.
func (r *ExecutionHistoryRepository) ObtainLastNExecutionHistoryBySourceNameBefore(
	ctx context.Context, sourceName string, before time.Time, beforeID string, limit int64, successOnly bool,
) ([]domain.ExecutionHistory, error) {
	if limit <= 0 {
		return []domain.ExecutionHistory{}, nil
	}

	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	condition := "WHERE " + executionHistorySourceNameFieldName + " = ?"
	args := []any{sourceName}
	if successOnly {
		condition += " AND " + executionHistorySuccessFieldName + " = 1"
	}
	if !before.IsZero() {
		condition += " AND (" + executionHistoryTimestampFieldName + ", " + executionHistoryIdFieldName + ") < (?, ?)"
		args = append(args, before.UTC().Unix(), beforeID)
	}
	condition += " ORDER BY " + executionHistoryTimestampFieldName + " DESC, " + executionHistoryIdFieldName + " DESC LIMIT ?;"
	args = append(args, limit)

	rows, err := executionHistoryQueryContext(tx, ctx, condition, args...)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	return rows, nil
}

// ObtainExecutionHistoryAfter returns up to limit records ordered oldest-first, starting
// strictly after the (timestamp, id) cursor. It is the read half of the archive
// reconciliation pass: the archive reports how far it has copied, and this walks the hot
// tier forward from there.
//
// The cursor is a pair rather than a timestamp because one collector tick writes a row
// per source at the same second. Resuming on the timestamp alone would either re-read
// that tick forever or skip whatever of it followed the cursor row.
//
// Pass a zero cursor to start from the beginning. Always returns a non-nil slice on
// success.
func (r *ExecutionHistoryRepository) ObtainExecutionHistoryAfter(
	ctx context.Context, after time.Time, afterID string, limit int64,
) ([]domain.ExecutionHistory, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	order := " ORDER BY " + executionHistoryTimestampFieldName + " ASC, " + executionHistoryIdFieldName + " ASC LIMIT ?;"

	var (
		condition string
		args      []any
	)
	if after.IsZero() && afterID == "" {
		condition = order
		args = []any{limit}
	} else {
		condition = "WHERE (" + executionHistoryTimestampFieldName + ", " + executionHistoryIdFieldName + ") > (?, ?)" + order
		args = []any{after.UTC().Unix(), afterID, limit}
	}

	rows, err := executionHistoryQueryContext(tx, ctx, condition, args...)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	return rows, nil
}

// ObtainLatestExecutionHistoryBySources returns the most recent execution_history
// row per source for every name in sourceNames, keyed by source_name. Sources
// without rows are absent. Used by ListSources to replace an N+1 of one
// ObtainLastNExecutionHistoryBySourceName per source with a single bulk read.
// Empty input is a no-op (no query issued).
func (r *ExecutionHistoryRepository) ObtainLatestExecutionHistoryBySources(ctx context.Context, sourceNames []string) (map[string]domain.ExecutionHistory, error) {
	if len(sourceNames) == 0 {
		return map[string]domain.ExecutionHistory{}, nil
	}

	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	// ROW_NUMBER() OVER (PARTITION BY source_name ORDER BY timestamp DESC, id DESC)
	// rides idx_execution_history_lookup_latest (source_name, timestamp DESC).
	// id DESC is the deterministic tie-break when two rows share the second-
	// resolution timestamp.
	placeholders := strings.Repeat("?,", len(sourceNames)-1) + "?"
	query := "SELECT " + executionHistoryIdFieldName + ", " +
		executionHistorySourceNameFieldName + ", " +
		executionHistorySuccessFieldName + ", " +
		executionHistoryErrorFieldName + ", " +
		executionHistoryTimestampFieldName + " FROM (\n" +
		"  SELECT " +
		executionHistoryIdFieldName + ", " +
		executionHistorySourceNameFieldName + ", " +
		executionHistorySuccessFieldName + ", " +
		executionHistoryErrorFieldName + ", " +
		executionHistoryTimestampFieldName + ",\n" +
		"  ROW_NUMBER() OVER (PARTITION BY " + executionHistorySourceNameFieldName +
		" ORDER BY " + executionHistoryTimestampFieldName + " DESC, " + executionHistoryIdFieldName + " DESC) AS rn\n" +
		"  FROM " + executionHistoryTableName +
		"  WHERE " + executionHistorySourceNameFieldName + " IN (" + placeholders + ")\n" +
		") AS ranked WHERE ranked.rn = 1;"

	args := make([]any, 0, len(sourceNames))
	for _, n := range sourceNames {
		args = append(args, n)
	}

	dbRows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}
	defer func() { err = errors.Join(err, dbRows.Close()) }()

	result := make(map[string]domain.ExecutionHistory, len(sourceNames))
	for dbRows.Next() {
		var item domain.ExecutionHistory
		var timestamp int64
		if scanErr := dbRows.Scan(
			&item.ID, &item.SourceName, &item.Success, &item.Error, &timestamp,
		); scanErr != nil {
			return nil, errors.Join(scanErr, loginjector.NewTraceError())
		}
		item.Timestamp = time.Unix(timestamp, 0).UTC()
		result[item.SourceName] = item
	}
	if err = dbRows.Err(); err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	return result, nil
}

// ObtainExecutionHistoryErrorCount returns the total number of failed execution history records.
func (r *ExecutionHistoryRepository) ObtainExecutionHistoryErrorCount(ctx context.Context) (int64, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	count, err := executionHistoryCount(tx, ctx, "WHERE "+executionHistorySuccessFieldName+" = 0;")
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}

	return count, nil
}

// ObtainLastNExecutionHistoryErrors returns the most recent failed execution history records,
// ordered newest-first, with LIMIT/OFFSET pagination.
func (r *ExecutionHistoryRepository) ObtainLastNExecutionHistoryErrors(ctx context.Context, offset, limit int64) ([]domain.ExecutionHistory, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	// The id breaks ties on timestamp. One collector tick fails several sources at the
	// same second, so without it two adjacent pages can repeat a row and skip another.
	query := executionHistorySqlSelect +
		"\nWHERE " + executionHistorySuccessFieldName + " = 0" +
		" ORDER BY " + executionHistoryTimestampFieldName + " DESC, " + executionHistoryIdFieldName + " DESC" +
		" LIMIT ? OFFSET ?;"

	dbRows, err := tx.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}
	defer func() { err = errors.Join(err, dbRows.Close()) }()

	var items []domain.ExecutionHistory
	for dbRows.Next() {
		var item domain.ExecutionHistory
		var timestamp int64
		if scanErr := dbRows.Scan(
			&item.ID, &item.SourceName, &item.Success, &item.Error, &timestamp,
		); scanErr != nil {
			return nil, errors.Join(scanErr, loginjector.NewTraceError())
		}
		item.Timestamp = time.Unix(timestamp, 0).UTC()
		items = append(items, item)
	}

	if items == nil {
		items = []domain.ExecutionHistory{}
	}
	return items, nil
}

// RetainExecutionHistory inserts or updates the given execution history record.
func (r *ExecutionHistoryRepository) RetainExecutionHistory(ctx context.Context, record *domain.ExecutionHistory) error {
	if record == nil {
		err := errors.New("execution history is nil")
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	if record.ID == "" {
		record.ID = identity.New(identity.KindExecutionHistory)
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := executionHistoryCount(tx, ctx, "WHERE "+executionHistoryIdFieldName+" = ?;", record.ID)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	var res sql.Result
	if count > 0 {
		cmd := "UPDATE" + " " + executionHistoryTableName + " SET " +
			executionHistorySourceNameFieldName + " = ?, " +
			executionHistorySuccessFieldName + " = ?, " +
			executionHistoryErrorFieldName + " = ?, " +
			executionHistoryTimestampFieldName + " = ?" +
			" WHERE " + executionHistoryIdFieldName + " = ?;"
		res, err = tx.ExecContext(
			ctx, cmd,
			record.SourceName,
			record.Success,
			record.Error,
			record.Timestamp.Unix(),
			record.ID,
		)
	} else {
		cmd := "INSERT INTO" + " " + executionHistoryTableName +
			" (" +
			executionHistoryIdFieldName + ", " +
			executionHistorySourceNameFieldName + ", " +
			executionHistorySuccessFieldName + ", " +
			executionHistoryErrorFieldName + ", " +
			executionHistoryTimestampFieldName +
			")" +
			" VALUES (?, ?, ?, ?, ?);"
		res, err = tx.ExecContext(
			ctx, cmd,
			record.ID,
			record.SourceName,
			record.Success,
			record.Error,
			record.Timestamp.Unix(),
		)
	}
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}
	if rows <= 0 {
		err = errors.New("unexpected result: no rows affected")
		err = errors.Join(err, internal.ErrNotFound)
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	if err = tx.Commit(); err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	return nil
}

// RemoveSourceExecutionHistory deletes the given execution history record by ID.
func (r *ExecutionHistoryRepository) RemoveSourceExecutionHistory(ctx context.Context, record *domain.ExecutionHistory) error {
	if record == nil {
		err := errors.New("execution history is nil")
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM" + " " + executionHistoryTableName + " WHERE " + executionHistoryIdFieldName + " = ?;"
	_, err = tx.ExecContext(ctx, cmd, record.ID)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", cmd))
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	if err = tx.Commit(); err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	return nil
}

const (
	executionHistoryTableName           = "execution_history"
	executionHistoryIdFieldName         = "id"
	executionHistorySourceNameFieldName = "source_name"
	executionHistorySuccessFieldName    = "success"
	executionHistoryErrorFieldName      = "error"
	executionHistoryTimestampFieldName  = "timestamp"

	executionHistorySqlSelect = "SELECT" + "\n" +
		executionHistoryIdFieldName + ", " +
		executionHistorySourceNameFieldName + ", " +
		executionHistorySuccessFieldName + ", " +
		executionHistoryErrorFieldName + ", " +
		executionHistoryTimestampFieldName +
		"\nFROM " + executionHistoryTableName
)

func executionHistoryCount(tx *sql.Tx, ctx context.Context, condition string, args ...any) (int64, error) {
	query := "SELECT\n" +
		" COUNT(*)\n" +
		"FROM " + executionHistoryTableName + "\n" + condition

	var count int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&count)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	} else if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, loginjector.NewTraceError())
		return 0, err
	}

	return count, nil
}

func executionHistoryQueryContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (items []domain.ExecutionHistory, err error) {
	count, err := executionHistoryCount(tx, ctx, condition, args...)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return
	}
	if count == 0 {
		items = []domain.ExecutionHistory{}
		return
	}

	query := executionHistorySqlSelect + "\n" + condition

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, loginjector.NewTraceError())
		return
	}
	defer func(rows io.Closer) { err = errors.Join(err, rows.Close()) }(rows)

	items = make([]domain.ExecutionHistory, 0, count)

	for rows.Next() {
		var item domain.ExecutionHistory
		var timestamp int64

		err = rows.Scan(
			&item.ID,
			&item.SourceName,
			&item.Success,
			&item.Error,
			&timestamp,
		)
		if err != nil {
			err = errors.Join(err, loginjector.NewTraceError())
			return
		}

		item.Timestamp = time.Unix(timestamp, 0).UTC()

		items = append(items, item)
	}

	return
}
