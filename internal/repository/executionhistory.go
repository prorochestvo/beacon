package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/domain/identity"
)

// ExecutionHistoryRepository persists and retrieves domain.ExecutionHistory records.
type ExecutionHistoryRepository struct {
	db db
}

// NewExecutionHistoryRepository returns a repository for the execution_history table.
func NewExecutionHistoryRepository(db db) (*ExecutionHistoryRepository, error) {
	return &ExecutionHistoryRepository{db: db}, nil
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

	// The id breaks ties on timestamp. One collector tick writes a row per source at the
	// same second, and the union of two tiers is sorted rather than walked in index order,
	// so a bare timestamp sort leaves rows in an order the planner is free to change
	// between calls.
	rows, err := executionHistoryQueryContext(tx, ctx,
		"WHERE "+whereClause+
			" ORDER BY "+executionHistoryTimestampFieldName+" DESC, "+executionHistoryIdFieldName+" DESC"+
			" LIMIT ?;",
		sourceName, limit)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
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
		"  " + executionHistorySqlFrom(executionHistoryTableName) + "\n" +
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

	// The id tie-break is what makes offset pagination stable here: one tick fails several
	// sources at the same second, so ordering on the timestamp alone can repeat a row on one
	// page and skip it on the next.
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
	if iterErr := dbRows.Err(); iterErr != nil {
		return nil, errors.Join(iterErr, loginjector.NewTraceError())
	}

	if items == nil {
		items = []domain.ExecutionHistory{}
	}
	return items, nil
}

// ObtainSourceCollectionHealth summarises every source that has ever run: when it last
// succeeded, when it last ran at all, how many attempts have failed since that success,
// and the most recent error message.
//
// Sources with no history at all are absent from the result rather than present with zero
// values — a source that has never been attempted has not failed, and the caller must be
// able to tell the two apart.
//
// This one reads the hot tier alone, unlike every other query here. Collection health is a
// question about the last few hours against a tier bounded at 180 days; a source whose
// last success predates that has a problem no alert threshold addresses, and unioning the
// archive would make the correlated sub-selects below scan history that cannot change the
// answer.
func (r *ExecutionHistoryRepository) ObtainSourceCollectionHealth(ctx context.Context) ([]domain.SourceCollectionHealth, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	// The aggregate establishes each source's last success and last run; the correlated
	// sub-selects then answer "since that success" questions that an aggregate alone
	// cannot, because they depend on the aggregate's own result.
	//
	// COALESCE on last_success makes the never-succeeded case fall out naturally: with no
	// success to be after, every failure counts and the newest error is the newest error.
	query := "SELECT s." + executionHistorySourceNameFieldName + ", s.last_success, s.last_run,\n" +
		"  (SELECT COUNT(*) FROM " + executionHistoryTableName + " f\n" +
		"    WHERE f." + executionHistorySourceNameFieldName + " = s." + executionHistorySourceNameFieldName + "\n" +
		"      AND f." + executionHistorySuccessFieldName + " = 0\n" +
		"      AND f." + executionHistoryTimestampFieldName + " > COALESCE(s.last_success, 0)) AS consecutive_failures,\n" +
		"  COALESCE((SELECT e." + executionHistoryErrorFieldName + " FROM " + executionHistoryTableName + " e\n" +
		"    WHERE e." + executionHistorySourceNameFieldName + " = s." + executionHistorySourceNameFieldName + "\n" +
		"      AND e." + executionHistorySuccessFieldName + " = 0\n" +
		"      AND e." + executionHistoryTimestampFieldName + " > COALESCE(s.last_success, 0)\n" +
		"    ORDER BY e." + executionHistoryTimestampFieldName + " DESC, e." + executionHistoryIdFieldName + " DESC\n" +
		"    LIMIT 1), '') AS last_error\n" +
		"FROM (\n" +
		"  SELECT " + executionHistorySourceNameFieldName + ",\n" +
		"    MAX(CASE WHEN " + executionHistorySuccessFieldName + " = 1 THEN " + executionHistoryTimestampFieldName + " END) AS last_success,\n" +
		"    MAX(" + executionHistoryTimestampFieldName + ") AS last_run\n" +
		"  FROM " + executionHistoryTableName + "\n" +
		"  GROUP BY " + executionHistorySourceNameFieldName + "\n" +
		") AS s;"

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	result := make([]domain.SourceCollectionHealth, 0, 64)
	for rows.Next() {
		var (
			item        domain.SourceCollectionHealth
			lastSuccess *int64
			lastRun     *int64
		)
		if scanErr := rows.Scan(&item.SourceName, &lastSuccess, &lastRun, &item.ConsecutiveFailures, &item.LastError); scanErr != nil {
			return nil, errors.Join(scanErr, loginjector.NewTraceError())
		}
		if lastSuccess != nil {
			item.LastSuccessAt = time.Unix(*lastSuccess, 0).UTC()
		}
		if lastRun != nil {
			item.LastRunAt = time.Unix(*lastRun, 0).UTC()
		}
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	return result, nil
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

	// Hot-only on purpose: this decides INSERT versus UPDATE against the hot table, and the
	// UPDATE below writes only there. Counting the union would send an id that lives solely
	// in the archive down the UPDATE path, which would match nothing and lose the write.
	count, err := executionHistoryCountHot(tx, ctx, "WHERE "+executionHistoryIdFieldName+" = ?;", record.ID)
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
	executionHistoryTableName = "execution_history"
	// executionHistoryArchiveTableName is the archive twin: same columns, same file. Rows
	// arrive here only by the roll-over move and are never written directly.
	executionHistoryArchiveTableName    = "execution_history_archive"
	executionHistoryIdFieldName         = "id"
	executionHistorySourceNameFieldName = "source_name"
	executionHistorySuccessFieldName    = "success"
	executionHistoryErrorFieldName      = "error"
	executionHistoryTimestampFieldName  = "timestamp"

	executionHistoryColumnList = executionHistoryIdFieldName + ", " +
		executionHistorySourceNameFieldName + ", " +
		executionHistorySuccessFieldName + ", " +
		executionHistoryErrorFieldName + ", " +
		executionHistoryTimestampFieldName
)

// executionHistorySqlSelect reads both tiers — see rateValueSqlSelect for why every read
// unions unconditionally instead of picking a tier by how far back the caller asked.
var executionHistorySqlSelect = "SELECT\n" + executionHistoryColumnList + "\n" +
	executionHistorySqlFrom(executionHistoryTableName)

// executionHistorySqlFrom returns the both-tiers FROM clause under the given alias.
func executionHistorySqlFrom(alias string) string {
	return "FROM (\n" +
		"SELECT " + executionHistoryColumnList + " FROM " + executionHistoryTableName + "\n" +
		"UNION ALL\n" +
		"SELECT " + executionHistoryColumnList + " FROM " + executionHistoryArchiveTableName + "\n" +
		") AS " + alias
}

// executionHistoryCountHot counts the hot table alone. Write paths use it: they decide what
// to do to execution_history, so they must ask about execution_history.
func executionHistoryCountHot(tx *sql.Tx, ctx context.Context, condition string, args ...any) (int64, error) {
	return executionHistoryCountIn(tx, ctx, "FROM "+executionHistoryTableName, condition, args...)
}

func executionHistoryCount(tx *sql.Tx, ctx context.Context, condition string, args ...any) (int64, error) {
	return executionHistoryCountIn(tx, ctx, executionHistorySqlFrom(executionHistoryTableName), condition, args...)
}

func executionHistoryCountIn(tx *sql.Tx, ctx context.Context, from, condition string, args ...any) (int64, error) {
	query := "SELECT\n" +
		" COUNT(*)\n" +
		from + "\n" + condition

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
	defer func() { err = errors.Join(err, rows.Close()) }()

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

	if err = rows.Err(); err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	return
}
