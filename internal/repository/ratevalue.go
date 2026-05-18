package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/twinj/uuid"
)

// NewRateValueRepository returns a repository for the rate_values table.
func NewRateValueRepository(db db) (*RateValueRepository, error) {
	return &RateValueRepository{db: db}, nil
}

// RateValueRepository persists and retrieves domain.RateValue records from the rate_values table.
type RateValueRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *RateValueRepository) Name() string { return rateValueTableName }

// CheckUP verifies that the repository can read from the rate_values table.
func (r *RateValueRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.Transaction(ctx)
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

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
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
	tx, err := r.db.Transaction(ctx)
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

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	return rates, nil
}

// ObtainLastNRateValuesBySourceName returns the most recent limit rate values for the given source,
// ordered newest-first. Always returns a non-nil slice on success.
func (r *RateValueRepository) ObtainLastNRateValuesBySourceName(ctx context.Context, sourceName string, limit int64) ([]domain.RateValue, error) {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	// Secondary sort by rowid DESC ensures deterministic newest-first ordering when
	// multiple rows share the same RFC3339 timestamp string (second precision).
	sqlCommand := " WHERE " + rateValueSourceNameFieldName + " = ?" + " ORDER BY " + rateValueTimestampFieldName + " DESC, " + rateValueIdFieldName + " DESC LIMIT ?;"
	rates, err := rateValueQueryContext(tx, ctx, sqlCommand, sourceName, limit)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
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
		record.ID = generateRateValueID()
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

// ChartPeriod specifies the time window for aggregated rate chart data.
type ChartPeriod string

const (
	// ChartPeriodWeek aggregates rate data over the past 7 days, grouped by day.
	ChartPeriodWeek ChartPeriod = "week"
	// ChartPeriodMonth aggregates rate data over the past 30 days, grouped by day.
	ChartPeriodMonth ChartPeriod = "month"
	// ChartPeriodYear aggregates rate data over the past 12 months, grouped by month.
	ChartPeriodYear ChartPeriod = "year"
)

// ChartPoint is one aggregated data point returned by ObtainRateValueChartBySourceName.
type ChartPoint struct {
	Label string  // "2026-04-03" (week/month) or "2026-04" (year)
	Price float64 // AVG(price) for the bucket
}

// ObtainRateValueChartBySourceName returns aggregated rate data grouped by day (week/month)
// or month (year) for the given source and period.
func (r *RateValueRepository) ObtainRateValueChartBySourceName(ctx context.Context, sourceName string, period ChartPeriod) ([]ChartPoint, error) {
	now := time.Now().UTC()

	var since time.Time
	var groupExpr string
	switch period {
	case ChartPeriodWeek:
		since = now.AddDate(0, 0, -7)
		groupExpr = "strftime('%Y-%m-%d', " + rateValueTimestampFieldName + ")"
	case ChartPeriodMonth:
		since = now.AddDate(0, -1, 0)
		groupExpr = "strftime('%Y-%m-%d', " + rateValueTimestampFieldName + ")"
	case ChartPeriodYear:
		since = now.AddDate(-1, 0, 0)
		groupExpr = "strftime('%Y-%m', " + rateValueTimestampFieldName + ")"
	default:
		return nil, fmt.Errorf("unknown chart period: %q", period)
	}

	query := fmt.Sprintf(
		"SELECT %s AS label, AVG(%s) AS avg_price "+
			"FROM %s WHERE %s = ? AND %s >= ? "+
			"GROUP BY label ORDER BY label ASC;",
		groupExpr, rateValuePriceFieldName,
		rateValueTableName,
		rateValueSourceNameFieldName,
		rateValueTimestampFieldName,
	)

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	rows, err := tx.QueryContext(ctx, query, sourceName, since.Format(time.RFC3339))
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var result []ChartPoint
	for rows.Next() {
		var p ChartPoint
		if scanErr := rows.Scan(&p.Label, &p.Price); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		result = append(result, p)
	}
	printRollbackError(tx)
	return result, nil
}

type db interface {
	Transaction(ctx context.Context) (*sql.Tx, error)
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

func generateRateValueID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("RV%04d%02d%02d%02d%02d%02dZ%dT%X", now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), uuid.NewV4().Bytes())
}

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

func rateValueQueryRowContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (*domain.RateValue, error) {
	query := rateValueSqlSelect + "\n" + condition

	var item domain.RateValue
	var timestamp string
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&item.ID,
		&item.SourceName,
		&item.BaseCurrency,
		&item.QuoteCurrency,
		&item.Price,
		&timestamp,
	)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	item.Timestamp, err = time.Parse(time.RFC3339, timestamp)
	if err != nil {
		err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, timestamp, err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	return &item, nil
}
