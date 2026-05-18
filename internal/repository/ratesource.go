package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/twinj/uuid"
)

// NewRateSourceRepository returns a repository for the rate_sources table.
func NewRateSourceRepository(db db) (*RateSourceRepository, error) {
	return &RateSourceRepository{db: db}, nil
}

// RateSourceRepository persists and retrieves domain.RateSource records from the rate_sources table.
type RateSourceRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *RateSourceRepository) Name() string { return rateSourceTableName }

// CheckUP verifies that the repository can read from the rate_sources table.
func (r *RateSourceRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateSourceCount(tx, ctx, ";")
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

// ObtainRateSourceByName returns the rate source with the given name, or nil if no row matches.
func (r *RateSourceRepository) ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error) {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	rows, err := rateSourceQueryRowContext(tx, ctx, "WHERE "+rateSourceNameFieldName+" = ?;", name)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	return rows, nil
}

// ObtainAllRateSources returns all rate sources ordered by name descending.
// Always returns a non-nil slice on success.
func (r *RateSourceRepository) ObtainAllRateSources(ctx context.Context) ([]domain.RateSource, error) {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	rows, err := rateSourceQueryContext(tx, ctx, "ORDER BY "+rateSourceNameFieldName+" DESC;")
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	return rows, nil
}

// RetainRateSource inserts or updates the given rate source record in the rate_sources table.
func (r *RateSourceRepository) RetainRateSource(ctx context.Context, record *domain.RateSource) error {
	if record == nil {
		err := errors.New("rate source is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	// Normalise and validate fetcher_kind. An empty string is treated as "plain"
	// rather than hard-failing: rows written before this column existed carry ""
	// until they round-trip through Go code, and hard-failing would break cmd/web
	// reads of in-flight data during a rolling deploy.
	switch record.FetcherKind {
	case "", "plain":
		record.FetcherKind = "plain"
	case "chromedp":
		// allowed; routes to ChromedpFetcher in cmd/rulegen — not used by
		// cmd/collector or cmd/web which do not perform fetching.
	default:
		return fmt.Errorf("rate source %q: unsupported fetcher_kind %q (allowed: plain, chromedp)",
			record.Name, record.FetcherKind)
	}

	rules, err := json.Marshal(record.Rules)
	if err != nil {
		err = fmt.Errorf("marshal rules for rate source %q: %w", record.Name, err)
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	opts, err := json.Marshal(record.Options)
	if err != nil {
		err = fmt.Errorf("marshal options for rate source %q: %w", record.Name, err)
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	ruleMetadata, err := json.Marshal(record.RuleMetadata)
	if err != nil {
		err = fmt.Errorf("marshal rule_metadata for rate source %q: %w", record.Name, err)
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateSourceCount(tx, ctx, "WHERE "+rateSourceNameFieldName+" = ?;", record.Name)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	var res sql.Result
	if count > 0 {
		cmd := "UPDATE" + " " + rateSourceTableName + " SET " +
			rateSourceTitleFieldName + " = ?, " +
			reteSourceBaseCurrencyFieldName + " = ?, " +
			reteSourceQuoteCurrencyFieldName + " = ?, " +
			rateSourceURLFieldName + " = ?, " +
			reteSourceIntervalFieldName + " = ?, " +
			rateSourceKindFieldName + " = ?, " +
			rateSourceActiveFieldName + " = ?, " +
			rateSourceFetcherKindFieldName + " = ?, " +
			rateSourceOptionsFieldName + " = ?, " +
			rateSourceRulesFieldName + " = ?, " +
			rateSourceRuleMetadataFieldName + " = ?" +
			" WHERE " + rateSourceNameFieldName + " = ?;"
		res, err = tx.ExecContext(
			ctx, cmd,
			record.Title,
			record.BaseCurrency,
			record.QuoteCurrency,
			record.URL,
			record.Interval,
			record.Kind,
			record.Active,
			record.FetcherKind,
			string(opts),
			string(rules),
			string(ruleMetadata),
			record.Name,
		)
	} else {
		cmd := "INSERT INTO" + " " + rateSourceTableName +
			" (" +
			rateSourceNameFieldName + ", " +
			rateSourceTitleFieldName + ", " +
			reteSourceBaseCurrencyFieldName + ", " +
			reteSourceQuoteCurrencyFieldName + ", " +
			rateSourceURLFieldName + ", " +
			reteSourceIntervalFieldName + ", " +
			rateSourceKindFieldName + ", " +
			rateSourceActiveFieldName + ", " +
			rateSourceFetcherKindFieldName + ", " +
			rateSourceOptionsFieldName + ", " +
			rateSourceRulesFieldName + ", " +
			rateSourceRuleMetadataFieldName +
			")" +
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);"
		res, err = tx.ExecContext(
			ctx, cmd,
			record.Name,
			record.Title,
			record.BaseCurrency,
			record.QuoteCurrency,
			record.URL,
			record.Interval,
			record.Kind,
			record.Active,
			record.FetcherKind,
			string(opts),
			string(rules),
			string(ruleMetadata),
		)
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

// RemoveRateSource deletes the given rate source record from the rate_sources table.
func (r *RateSourceRepository) RemoveRateSource(ctx context.Context, record *domain.RateSource) error {
	if record == nil {
		err := errors.New("rate source is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM" + " " + rateSourceTableName + " WHERE " + rateSourceNameFieldName + " = ?;"
	_, err = tx.ExecContext(ctx, cmd, record.Name)
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

const (
	rateSourceTableName              = "rate_sources"
	rateSourceNameFieldName          = "name"
	rateSourceTitleFieldName         = "title"
	rateSourceURLFieldName           = "url"
	reteSourceIntervalFieldName      = "interval"
	reteSourceBaseCurrencyFieldName  = "base_currency"
	reteSourceQuoteCurrencyFieldName = "quote_currency"
	rateSourceKindFieldName          = "kind"
	rateSourceActiveFieldName        = "active"
	// rateSourceFetcherKindFieldName identifies the fetch mechanism for the source.
	// Allowed values: "plain" (default plain HTTP fetch), "chromedp" (headless
	// Chrome via DevTools Protocol — handled by ChromedpFetcher in cmd/rulegen).
	rateSourceFetcherKindFieldName  = "fetcher_kind"
	rateSourceOptionsFieldName      = "options"
	rateSourceRulesFieldName        = "rules"
	rateSourceRuleMetadataFieldName = "rule_metadata"

	rateSourceSqlSelect = "SELECT\n" +
		rateSourceNameFieldName + ", " +
		rateSourceTitleFieldName + ", " +
		reteSourceBaseCurrencyFieldName + ", " +
		reteSourceQuoteCurrencyFieldName + ", " +
		rateSourceURLFieldName + ", " +
		reteSourceIntervalFieldName + ", " +
		rateSourceKindFieldName + ", " +
		rateSourceActiveFieldName + ", " +
		rateSourceFetcherKindFieldName + ", " +
		rateSourceOptionsFieldName + ", " +
		rateSourceRulesFieldName + ", " +
		rateSourceRuleMetadataFieldName + " " +
		"\nFROM " + rateSourceTableName
)

func generateRateSourceID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("RS%04d%02d%02d%02d%02d%02dZ%dT%X", now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), uuid.NewV4().Bytes())
}

func rateSourceCount(tx *sql.Tx, ctx context.Context, condition string, args ...any) (int64, error) {
	query := "SELECT\n" +
		" COUNT(*)\n" +
		"FROM " + rateSourceTableName + "\n" + condition

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

func rateSourceQueryContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (items []domain.RateSource, err error) {
	count, err := rateSourceCount(tx, ctx, condition, args...)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return
	}
	if count == 0 {
		items = []domain.RateSource{}
		return
	}

	query := rateSourceSqlSelect + "\n" + condition

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	defer func(rows io.Closer) { err = errors.Join(err, rows.Close()) }(rows)

	items = make([]domain.RateSource, 0, count)

	for rows.Next() {
		var optsJSON, rulesJSON, ruleMetadataJSON string

		var item domain.RateSource
		if err = rows.Scan(
			&item.Name,
			&item.Title,
			&item.BaseCurrency,
			&item.QuoteCurrency,
			&item.URL,
			&item.Interval,
			&item.Kind,
			&item.Active,
			&item.FetcherKind,
			&optsJSON,
			&rulesJSON,
			&ruleMetadataJSON,
		); err != nil {
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}

		if item.Kind != domain.RateSourceKindASK && item.Kind != domain.RateSourceKindBID {
			err = fmt.Errorf("unknown kind %s", item.Kind)
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}

		if err = json.Unmarshal([]byte(optsJSON), &item.Options); err != nil {
			err = fmt.Errorf("unmarshal options for source %q: %w", item.Name, err)
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}

		if err = json.Unmarshal([]byte(rulesJSON), &item.Rules); err != nil {
			err = fmt.Errorf("unmarshal rules for source %q: %w", item.Name, err)
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}

		if err = json.Unmarshal([]byte(ruleMetadataJSON), &item.RuleMetadata); err != nil {
			err = fmt.Errorf("unmarshal rule_metadata for source %q: %w", item.Name, err)
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}

		items = append(items, item)
	}

	return
}

func rateSourceQueryRowContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (*domain.RateSource, error) {
	query := rateSourceSqlSelect + "\n" + condition

	var item domain.RateSource
	var optionsJSON, rulesJSON, ruleMetadataJSON string
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&item.Name,
		&item.Title,
		&item.BaseCurrency,
		&item.QuoteCurrency,
		&item.URL,
		&item.Interval,
		&item.Kind,
		&item.Active,
		&item.FetcherKind,
		&optionsJSON,
		&rulesJSON,
		&ruleMetadataJSON,
	)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if err = json.Unmarshal([]byte(optionsJSON), &item.Options); err != nil {
		err = fmt.Errorf("unmarshal options for source %q: %w", item.Name, err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if err = json.Unmarshal([]byte(rulesJSON), &item.Rules); err != nil {
		err = fmt.Errorf("unmarshal rules for source %q: %w", item.Name, err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if err = json.Unmarshal([]byte(ruleMetadataJSON), &item.RuleMetadata); err != nil {
		err = fmt.Errorf("unmarshal rule_metadata for source %q: %w", item.Name, err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	return &item, nil
}
