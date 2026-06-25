package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/domain/identity"
)

// NewRateValueRepository returns a repository for the rate_values table.
func NewRateValueRepository(db db) (*RateValueRepository, error) {
	return &RateValueRepository{db: db}, nil
}

// RateValueRepository persists and retrieves domain.RateValue records from the rate_values table.
type RateValueRepository struct {
	db db
}

// db is the minimal SQLite client surface every repository depends on.
// Transaction opens a read-write transaction; ReadOnlyTransaction opens a
// read-only one. SELECT-only methods must use the read-only variant so they
// don't compete with collector/notifier writers for the write lock at
// COMMIT/ROLLBACK time.
type db interface {
	Transaction(ctx context.Context) (*sql.Tx, error)
	ReadOnlyTransaction(ctx context.Context) (*sql.Tx, error)
}

// Name returns the name of the underlying database table.
func (r *RateValueRepository) Name() string { return rateValueTableName }

// CheckUP verifies that the repository can read from the rate_values table.
func (r *RateValueRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateValueCount(tx, ctx, ";")
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	if count < 0 {
		err = errors.New("unexpected result")
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// ObtainAllRateValueBySourceName returns all rate value records for the given source name.
// Always returns a non-nil slice on success.
func (r *RateValueRepository) ObtainAllRateValueBySourceName(ctx context.Context, sourceName string) ([]domain.RateValue, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	sqlCommand := "WHERE " + rateValueSourceNameFieldName + " = ?;"
	rates, err := rateValueQueryContext(tx, ctx, sqlCommand, sourceName)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	return rates, nil
}

// ObtainLatestRateValuesBySourceNames returns the most recent rate_value row
// per source for every name in sourceNames, keyed by source_name. Sources
// without rows are absent. Used by ListMeSubscriptions to replace an N+1 of one
// ObtainLastNRateValuesBySourceName per page item with a single bulk read.
// Empty input is a no-op (no query issued).
func (r *RateValueRepository) ObtainLatestRateValuesBySourceNames(ctx context.Context, sourceNames []string) (map[string]domain.RateValue, error) {
	if len(sourceNames) == 0 {
		return map[string]domain.RateValue{}, nil
	}

	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	// ROW_NUMBER() OVER (PARTITION BY source_name ORDER BY timestamp DESC, id DESC)
	// rides idx_rate_values_lookup (source_name, base_currency, quote_currency,
	// timestamp DESC). id DESC is the deterministic tie-break for rows sharing
	// the second-resolution RFC3339 timestamp.
	placeholders := strings.Repeat("?,", len(sourceNames)-1) + "?"
	query := "SELECT " +
		rateValueIdFieldName + ", " +
		rateValueSourceNameFieldName + ", " +
		rateValueBaseCurrencyFieldName + ", " +
		rateValueQuoteCurrencyFieldName + ", " +
		rateValuePriceFieldName + ", " +
		rateValueTimestampFieldName + " FROM (\n" +
		"  SELECT " +
		rateValueIdFieldName + ", " +
		rateValueSourceNameFieldName + ", " +
		rateValueBaseCurrencyFieldName + ", " +
		rateValueQuoteCurrencyFieldName + ", " +
		rateValuePriceFieldName + ", " +
		rateValueTimestampFieldName + ",\n" +
		"  ROW_NUMBER() OVER (PARTITION BY " + rateValueSourceNameFieldName +
		" ORDER BY " + rateValueTimestampFieldName + " DESC, " + rateValueIdFieldName + " DESC) AS rn\n" +
		"  FROM " + rateValueTableName +
		"  WHERE " + rateValueSourceNameFieldName + " IN (" + placeholders + ")\n" +
		") AS ranked WHERE ranked.rn = 1;"

	args := make([]any, 0, len(sourceNames))
	for _, n := range sourceNames {
		args = append(args, n)
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	result := make(map[string]domain.RateValue, len(sourceNames))
	for rows.Next() {
		var item domain.RateValue
		var timestamp string
		if scanErr := rows.Scan(
			&item.ID, &item.SourceName, &item.BaseCurrency, &item.QuoteCurrency, &item.Price, &timestamp,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		if parsed, parseErr := time.Parse(time.RFC3339, timestamp); parseErr == nil {
			item.Timestamp = parsed
		}
		result[item.SourceName] = item
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return result, nil
}

// ObtainLastNRateValuesBySourceName returns the most recent limit rate values for the given source,
// ordered newest-first. Always returns a non-nil slice on success.
func (r *RateValueRepository) ObtainLastNRateValuesBySourceName(ctx context.Context, sourceName string, limit int64) ([]domain.RateValue, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	// Secondary sort by rowid DESC keeps newest-first deterministic when rows
	// share the same second-precision RFC3339 timestamp string.
	sqlCommand := " WHERE " + rateValueSourceNameFieldName + " = ?" + " ORDER BY " + rateValueTimestampFieldName + " DESC, " + rateValueIdFieldName + " DESC LIMIT ?;"
	rates, err := rateValueQueryContext(tx, ctx, sqlCommand, sourceName, limit)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	return rates, nil
}

// RetainRateValue inserts or updates the given rate value record; Timestamp is always set to now.
func (r *RateValueRepository) RetainRateValue(ctx context.Context, record *domain.RateValue) error {
	if record == nil {
		err := errors.New("rate value is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	if record.ID == "" {
		record.ID = identity.New(identity.KindRateValue)
	}
	record.Timestamp = time.Now().UTC()

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateValueCount(tx, ctx, " WHERE "+rateValueIdFieldName+" = ?;", record.ID)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	var res sql.Result
	if count > 0 {
		cmd := "UPDATE" + " " + rateValueTableName + " SET " +
			rateValueSourceNameFieldName + " = ?, " +
			rateValueBaseCurrencyFieldName + " = ?, " +
			rateValueQuoteCurrencyFieldName + " = ?, " +
			rateValuePriceFieldName + " = ?, " +
			rateValueTimestampFieldName + " = ? " +
			"WHERE " + rateValueIdFieldName + " = ?;"
		res, err = tx.ExecContext(ctx, cmd, record.SourceName, record.BaseCurrency, record.QuoteCurrency, record.Price, record.Timestamp.Format(time.RFC3339), record.ID)
	} else {
		cmd := "INSERT INTO" + " " + rateValueTableName + " (" +
			rateValueIdFieldName + ", " +
			rateValueSourceNameFieldName + ", " +
			rateValueBaseCurrencyFieldName + ", " +
			rateValueQuoteCurrencyFieldName + ", " +
			rateValuePriceFieldName + ", " +
			rateValueTimestampFieldName +
			") VALUES (?, ?, ?, ?, ?, ?);"
		res, err = tx.ExecContext(ctx, cmd, record.ID, record.SourceName, record.BaseCurrency, record.QuoteCurrency, record.Price, record.Timestamp.Format(time.RFC3339))
	}
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}
	if rows <= 0 {
		err = errors.New("unexpected result: no rows affected")
		err = errors.Join(err, internal.ErrNotFound)
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	if err = tx.Commit(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// RemoveRateValue deletes the given rate value record by ID.
func (r *RateValueRepository) RemoveRateValue(ctx context.Context, record *domain.RateValue) error {
	if record == nil {
		err := errors.New("rate value is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM" + " " + rateValueTableName + " WHERE " + rateValueIdFieldName + " = ?;"
	_, err = tx.ExecContext(ctx, cmd, record.ID)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", cmd))
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	if err = tx.Commit(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// ObtainHistoryForPairsPaged returns rate_values rows for the given
// (source_name, base, quote) tuples, sorted by timestamp DESC then id DESC,
// paginated by limit and offset. Used by the per-pair history endpoint to load
// a page of one canonical pair's events across every source the caller is
// subscribed to for that pair.
//
// All three queries (COUNT(*), SELECT page, COUNT(DISTINCT title+timestamp))
// run in a single ReadOnlyTransaction for a consistent snapshot: a collector
// write between separate reads would make rowTotal disagree with groupedTotal.
//
// Returns:
//   - rows: the paginated rate_values for this page.
//   - rowTotal: the un-paginated row count (one per rate_values row).
//   - groupedTotal: the count of distinct (rate_sources.title || '|' || timestamp)
//     tuples. Two sibling BID/ASK sources sharing the same provider title at the
//     same scrape moment count as one. Use this as the pagination total shown to
//     the user; rowTotal is for internal diagnostics only.
//
// Empty pairs is a no-op (empty slice, totals=0).
//
// NOTE: SQLite's expression-tree limit is ~1000 terms by default; each pair
// tuple contributes 3. A user with ~333+ unique source subscriptions on one
// pair would overflow it. Vanishingly unlikely given current data; chunking is
// left for a future iteration.
func (r *RateValueRepository) ObtainHistoryForPairsPaged(
	ctx context.Context,
	pairs []domain.SourcePairKey,
	limit, offset int64,
) (rows []domain.RateValue, rowTotal int64, groupedTotal int64, err error) {
	if len(pairs) == 0 {
		return []domain.RateValue{}, 0, 0, nil
	}

	tx, txErr := r.db.ReadOnlyTransaction(ctx)
	if txErr != nil {
		return nil, 0, 0, errors.Join(txErr, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	tuples := make([]string, 0, len(pairs))
	args := make([]any, 0, len(pairs)*3)
	for _, p := range pairs {
		tuples = append(tuples, "(?, ?, ?)")
		args = append(args, p.SourceName, p.BaseCurrency, p.QuoteCurrency)
	}
	// inClause is for queries referencing rate_values alone, where column names
	// are unambiguous. The JOIN query below uses the rv-prefixed variant.
	inClause := "(" +
		rateValueSourceNameFieldName + ", " +
		rateValueBaseCurrencyFieldName + ", " +
		rateValueQuoteCurrencyFieldName + ") IN (" +
		strings.Join(tuples, ", ") + ")"
	// inClauseJoin qualifies every column with the rv alias to avoid "ambiguous
	// column name" where rate_sources shares column names with rate_values
	// (source_name, base_currency, quote_currency).
	inClauseJoin := "(" +
		"rv." + rateValueSourceNameFieldName + ", " +
		"rv." + rateValueBaseCurrencyFieldName + ", " +
		"rv." + rateValueQuoteCurrencyFieldName + ") IN (" +
		strings.Join(tuples, ", ") + ")"

	// Query 1: row-level COUNT(*) for the full WHERE.
	countQuery := "SELECT COUNT(*) FROM " + rateValueTableName + " WHERE " + inClause + ";"
	if scanErr := tx.QueryRowContext(ctx, countQuery, args...).Scan(&rowTotal); scanErr != nil {
		return nil, 0, 0, errors.Join(scanErr, fmt.Errorf("SQL: %s", countQuery), internal.NewTraceError())
	}

	if rowTotal == 0 {
		return []domain.RateValue{}, 0, 0, nil
	}

	// Query 2: paginated SELECT, newest first.
	selectArgs := make([]any, 0, len(args)+2)
	selectArgs = append(selectArgs, args...)
	selectArgs = append(selectArgs, limit, offset)
	pageQuery := rateValueSqlSelect + "\nWHERE " + inClause +
		" ORDER BY " + rateValueTimestampFieldName + " DESC, " + rateValueIdFieldName + " DESC" +
		" LIMIT ? OFFSET ?;"

	sqlRows, queryErr := tx.QueryContext(ctx, pageQuery, selectArgs...)
	if queryErr != nil {
		return nil, 0, 0, errors.Join(queryErr, fmt.Errorf("SQL: %s", pageQuery), internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, sqlRows.Close()) }()

	result := make([]domain.RateValue, 0, limit)
	for sqlRows.Next() {
		var item domain.RateValue
		var timestamp string
		if scanErr := sqlRows.Scan(
			&item.ID, &item.SourceName, &item.BaseCurrency, &item.QuoteCurrency, &item.Price, &timestamp,
		); scanErr != nil {
			return nil, 0, 0, errors.Join(scanErr, internal.NewTraceError())
		}
		parsed, parseErr := time.Parse(time.RFC3339, timestamp)
		if parseErr != nil {
			return nil, 0, 0, errors.Join(
				fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, timestamp, parseErr),
				internal.NewTraceError(),
			)
		}
		item.Timestamp = parsed
		result = append(result, item)
	}
	if iterErr := sqlRows.Err(); iterErr != nil {
		return nil, 0, 0, errors.Join(iterErr, internal.NewTraceError())
	}

	// Query 3: grouped count of distinct (title, timestamp) tuples, in the same
	// read-only transaction for a consistent snapshot. Pipe-delimited
	// concatenation because SQLite's COUNT(DISTINCT) takes a single expression;
	// '|' is safe as long as provider titles contain no '|' — see
	// plans/015-history-group-by-provider.md Assumption 2.
	groupedQuery := "SELECT COUNT(DISTINCT " +
		"rs." + rateSourceTitleFieldName + " || '|' || rv." + rateValueTimestampFieldName +
		") FROM " + rateValueTableName + " rv" +
		" JOIN " + rateSourceTableName + " rs ON rs." + rateSourceNameFieldName + " = rv." + rateValueSourceNameFieldName +
		" WHERE " + inClauseJoin + ";"

	if scanErr := tx.QueryRowContext(ctx, groupedQuery, args...).Scan(&groupedTotal); scanErr != nil {
		return nil, 0, 0, errors.Join(scanErr, fmt.Errorf("SQL: %s", groupedQuery), internal.NewTraceError())
	}

	return result, rowTotal, groupedTotal, nil
}

// ObtainValuesForPairsSince returns rate_value rows matching any of the given
// (source_name, base_currency, quote_currency) tuples whose timestamp is >=
// since, ordered by timestamp ASC then id ASC for deterministic ordering of
// rows that share a second-precision RFC3339 timestamp.
//
// The Kind field on each SourcePairKey is not used in the SQL filter because
// rate_values does not store kind — kind is a property of rate_sources, threaded
// through SourcePairKey for the service layer to use when grouping results.
//
// Empty pairs is a no-op (no query issued) to avoid an invalid IN () clause.
// SQLite's expression-tree limit is ~1000 terms by default; users with very
// many subscriptions may need chunking in a future iteration.
func (r *RateValueRepository) ObtainValuesForPairsSince(ctx context.Context, pairs []domain.SourcePairKey, since time.Time) ([]domain.RateValue, error) {
	if len(pairs) == 0 {
		return []domain.RateValue{}, nil
	}

	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	// WHERE (source_name, base_currency, quote_currency) IN ((?,?,?), ...) AND timestamp >= ?
	// Each tuple contributes 3 placeholders.
	tuples := make([]string, 0, len(pairs))
	args := make([]any, 0, len(pairs)*3+2)
	for _, p := range pairs {
		tuples = append(tuples, "(?, ?, ?)")
		args = append(args, p.SourceName, p.BaseCurrency, p.QuoteCurrency)
	}
	args = append(args, since.UTC().Format(time.RFC3339))

	// LIMIT caps the result set to prevent an unbounded scan on large data sets.
	// len(pairs)*2000 covers ~12 days of minute-grain data per pair and stays
	// well below SQLite's default expression-tree limit of ~1000 terms.
	limit := len(pairs) * 2000
	args = append(args, limit)

	query := rateValueSqlSelect + "\nWHERE (" +
		rateValueSourceNameFieldName + ", " +
		rateValueBaseCurrencyFieldName + ", " +
		rateValueQuoteCurrencyFieldName + ") IN (" +
		strings.Join(tuples, ", ") +
		") AND " + rateValueTimestampFieldName + " >= ?" +
		" ORDER BY " + rateValueTimestampFieldName + " ASC, " + rateValueIdFieldName + " ASC LIMIT ?;"

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	result := make([]domain.RateValue, 0, len(pairs)*200)
	for rows.Next() {
		var item domain.RateValue
		var timestamp string
		if scanErr := rows.Scan(
			&item.ID, &item.SourceName, &item.BaseCurrency, &item.QuoteCurrency, &item.Price, &timestamp,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		parsed, parseErr := time.Parse(time.RFC3339, timestamp)
		if parseErr != nil {
			return nil, errors.Join(fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, timestamp, parseErr), internal.NewTraceError())
		}
		item.Timestamp = parsed
		result = append(result, item)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, errors.Join(iterErr, internal.NewTraceError())
	}
	if result == nil {
		result = []domain.RateValue{}
	}
	return result, nil
}

const (
	rateValueTableName              = "rate_values"
	rateValueIdFieldName            = "id"
	rateValueSourceNameFieldName    = "source_name"
	rateValueBaseCurrencyFieldName  = "base_currency"
	rateValueQuoteCurrencyFieldName = "quote_currency"
	rateValuePriceFieldName         = "price"
	rateValueTimestampFieldName     = "timestamp"

	rateValueSqlSelect = "SELECT\n" +
		rateValueIdFieldName + ", " +
		rateValueSourceNameFieldName + ", " +
		rateValueBaseCurrencyFieldName + ", " +
		rateValueQuoteCurrencyFieldName + ", " +
		rateValuePriceFieldName + ", " +
		rateValueTimestampFieldName +
		"\nFROM " + rateValueTableName
)

func rateValueCount(tx *sql.Tx, ctx context.Context, condition string, args ...any) (int64, error) {
	query := "SELECT\n" +
		" COUNT(*)\n" +
		"FROM " + rateValueTableName + "\n" + condition

	var count int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&count)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	} else if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return 0, err
	}

	return count, nil
}

func rateValueQueryContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (items []domain.RateValue, err error) {
	count, err := rateValueCount(tx, ctx, condition, args...)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return
	}
	if count == 0 {
		items = []domain.RateValue{}
		return
	}

	query := rateValueSqlSelect + "\n" + condition

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return
	}
	defer func(rows io.Closer) { err = errors.Join(err, rows.Close()) }(rows)

	items = make([]domain.RateValue, 0, count)

	for rows.Next() {
		var item domain.RateValue
		var timestamp string

		err = rows.Scan(
			&item.ID,
			&item.SourceName,
			&item.BaseCurrency,
			&item.QuoteCurrency,
			&item.Price,
			&timestamp,
		)
		if err != nil {
			err = errors.Join(err, internal.NewTraceError())
			return
		}

		item.Timestamp, err = time.Parse(time.RFC3339, timestamp)
		if err != nil {
			err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, timestamp, err)
			err = errors.Join(err, internal.NewTraceError())
			return
		}

		items = append(items, item)
	}

	return
}
