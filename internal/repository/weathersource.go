package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// NewWeatherSourceRepository returns a repository for the weather_sources table.
func NewWeatherSourceRepository(db db) (*WeatherSourceRepository, error) {
	return &WeatherSourceRepository{db: db}, nil
}

// WeatherSourceRepository retrieves domain.WeatherSource config rows from the
// weather_sources table. Read-only: the table is seeded and hand-edited by
// operators, never written by the service binaries.
type WeatherSourceRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *WeatherSourceRepository) Name() string { return weatherSourceTableName }

// CheckUP verifies the repository can read from the weather_sources table.
// SELECT 1 ... LIMIT 1 exits after the first row (or sql.ErrNoRows on an empty
// table — fine, the table exists) rather than scanning the whole table.
func (r *WeatherSourceRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	var probe int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM "+weatherSourceTableName+" LIMIT 1;").Scan(&probe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// ObtainAllWeatherSources returns every weather-source config row ordered by
// provider. Always returns a non-nil slice on success.
func (r *WeatherSourceRepository) ObtainAllWeatherSources(ctx context.Context) ([]domain.WeatherSource, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	return weatherSourceQueryContext(tx, ctx, "ORDER BY "+weatherSourceProviderFieldName+" ASC;")
}

// ObtainWeatherSourceByProvider returns the config row for the given provider
// token, or (nil, nil) when no row matches. Absence is not an error: callers
// treat a missing row as "default active" so a half-migrated or hand-edited DB
// degrades to current behaviour rather than going dark.
func (r *WeatherSourceRepository) ObtainWeatherSourceByProvider(ctx context.Context, provider string) (*domain.WeatherSource, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	items, err := weatherSourceQueryContext(tx, ctx, "WHERE "+weatherSourceProviderFieldName+" = ?;", provider)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

const (
	weatherSourceTableName                 = "weather_sources"
	weatherSourceProviderFieldName         = "provider"
	weatherSourceTitleFieldName            = "title"
	weatherSourceActiveFieldName           = "active"
	weatherSourceBaseURLFieldName          = "base_url"
	weatherSourceThrottleIntervalFieldName = "throttle_interval"
	weatherSourceOptionsFieldName          = "options"

	weatherSourceSQLSelect = "SELECT " +
		weatherSourceProviderFieldName + ", " +
		weatherSourceTitleFieldName + ", " +
		weatherSourceActiveFieldName + ", " +
		weatherSourceBaseURLFieldName + ", " +
		weatherSourceThrottleIntervalFieldName + ", " +
		weatherSourceOptionsFieldName +
		" FROM " + weatherSourceTableName
)

// weatherSourceQueryContext runs the canonical SELECT with the given condition
// and scans each row into a domain.WeatherSource. active (INTEGER) is converted
// to bool and options (TEXT JSON) is unmarshalled into WeatherSourceOptions;
// malformed options JSON returns a wrapped error, never a panic. The named
// return err captures the rows.Close error via the deferred join.
func weatherSourceQueryContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (items []domain.WeatherSource, err error) {
	query := weatherSourceSQLSelect + " " + condition

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func(rows io.Closer) { err = errors.Join(err, rows.Close()) }(rows)

	items = make([]domain.WeatherSource, 0, 2)
	for rows.Next() {
		var item domain.WeatherSource
		var active int
		var optionsJSON string
		if err = rows.Scan(
			&item.Provider,
			&item.Title,
			&active,
			&item.BaseURL,
			&item.ThrottleInterval,
			&optionsJSON,
		); err != nil {
			return nil, errors.Join(err, internal.NewTraceError())
		}
		item.Active = active != 0
		if err = json.Unmarshal([]byte(optionsJSON), &item.Options); err != nil {
			return nil, errors.Join(fmt.Errorf("unmarshal options for weather source %q: %w", item.Provider, err), internal.NewTraceError())
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}
