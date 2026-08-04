package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// NewRateSourceArchiveRepository creates a repository over the archive database's
// rate_sources mirror. db must be a client opened on the archive DSN, not the hot
// one: both databases carry a table of this name, and nothing in the type system
// tells them apart.
func NewRateSourceArchiveRepository(db db) (*RateSourceArchiveRepository, error) {
	return &RateSourceArchiveRepository{db: db}, nil
}

// RateSourceArchiveRepository maintains the archive's copy of rate_sources: the
// metadata that says what each archived source_name actually is.
//
// It exposes an upsert and nothing else. Deletion is absent by design — a source
// dropped from the hot tier still has archived values that need a title, and the
// paged history query joins this table to produce one. Reads go through
// *RateSourceRepository opened on the same database, which works verbatim because
// the mirror's columns match the hot schema.
type RateSourceArchiveRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *RateSourceArchiveRepository) Name() string { return "rate_sources_archive" }

// CheckUP verifies that the repository can read from the archive's rate_sources table.
func (r *RateSourceArchiveRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	var count int64
	cmd := "SELECT COUNT(*) FROM " + rateSourceTableName + " LIMIT 1;"
	if err = tx.QueryRowContext(ctx, cmd).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return nil
}

// RetainRateSources mirrors the given sources into the archive, inserting rows that are
// new and overwriting the metadata of rows already there. It reports how many rows the
// statement touched.
//
// INSERT ... ON CONFLICT DO UPDATE rather than INSERT OR REPLACE: the latter deletes and
// re-inserts the row, which would fire delete triggers and churn the primary key for a
// mirror whose whole job is to be stable. Overwriting is the correct resolution here
// because the hot tier is authoritative for what a source currently is — the archive
// keeps sources the hot tier has forgotten, not versions of them.
//
// The whole batch lands in one transaction, so a failure leaves the mirror exactly as it
// was and the next reconciliation pass retries from scratch. An empty slice is a no-op.
func (r *RateSourceArchiveRepository) RetainRateSources(ctx context.Context, records []domain.RateSource) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}

	const columns = 12

	placeholders := make([]string, 0, len(records))
	args := make([]any, 0, len(records)*columns)
	for i := range records {
		record := &records[i]
		if record.Name == "" {
			return 0, errors.Join(
				fmt.Errorf("archive: rate source at index %d has an empty name", i),
				loginjector.NewTraceError(),
			)
		}

		opts, err := json.Marshal(record.Options)
		if err != nil {
			return 0, errors.Join(
				fmt.Errorf("archive: marshal options for rate source %q: %w", record.Name, err),
				loginjector.NewTraceError(),
			)
		}
		rules, err := json.Marshal(record.Rules)
		if err != nil {
			return 0, errors.Join(
				fmt.Errorf("archive: marshal rules for rate source %q: %w", record.Name, err),
				loginjector.NewTraceError(),
			)
		}
		ruleMetadata, err := json.Marshal(record.RuleMetadata)
		if err != nil {
			return 0, errors.Join(
				fmt.Errorf("archive: marshal rule_metadata for rate source %q: %w", record.Name, err),
				loginjector.NewTraceError(),
			)
		}

		placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args,
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

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "INSERT INTO " + rateSourceTableName + " (" +
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
		rateSourceRuleMetadataFieldName + ") VALUES " +
		strings.Join(placeholders, ", ") +
		" ON CONFLICT(" + rateSourceNameFieldName + ") DO UPDATE SET " +
		rateSourceTitleFieldName + " = excluded." + rateSourceTitleFieldName + ", " +
		reteSourceBaseCurrencyFieldName + " = excluded." + reteSourceBaseCurrencyFieldName + ", " +
		reteSourceQuoteCurrencyFieldName + " = excluded." + reteSourceQuoteCurrencyFieldName + ", " +
		rateSourceURLFieldName + " = excluded." + rateSourceURLFieldName + ", " +
		reteSourceIntervalFieldName + " = excluded." + reteSourceIntervalFieldName + ", " +
		rateSourceKindFieldName + " = excluded." + rateSourceKindFieldName + ", " +
		rateSourceActiveFieldName + " = excluded." + rateSourceActiveFieldName + ", " +
		rateSourceFetcherKindFieldName + " = excluded." + rateSourceFetcherKindFieldName + ", " +
		rateSourceOptionsFieldName + " = excluded." + rateSourceOptionsFieldName + ", " +
		rateSourceRulesFieldName + " = excluded." + rateSourceRulesFieldName + ", " +
		rateSourceRuleMetadataFieldName + " = excluded." + rateSourceRuleMetadataFieldName + ";"

	res, err := tx.ExecContext(ctx, cmd, args...)
	if err != nil {
		return 0, errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}

	if err = tx.Commit(); err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
	}

	return int(affected), nil
}
