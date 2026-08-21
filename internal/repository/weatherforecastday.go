package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/domain/identity"
)

// WeatherForecastDayRepository persists and retrieves domain.WeatherForecastDay records —
// the long-range daily forecast behind the multi-week outlook view and digest.
type WeatherForecastDayRepository struct {
	db db
}

// NewWeatherForecastDayRepository returns a repository for the weather_forecast_days table.
func NewWeatherForecastDayRepository(db db) (*WeatherForecastDayRepository, error) {
	return &WeatherForecastDayRepository{db: db}, nil
}

// CheckUP verifies that the repository can read from the weather_forecast_days table.
func (r *WeatherForecastDayRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	query := "SELECT COUNT(*) FROM " + weatherForecastDayTableName + ";"
	var count int64
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}
	if count < 0 {
		return errors.Join(errors.New("unexpected result"), loginjector.NewTraceError())
	}
	return nil
}

// Name returns the name of the underlying database table.
func (r *WeatherForecastDayRepository) Name() string { return weatherForecastDayTableName }

// ObtainForecastDays returns the stored forecast for locationID and provider from fromDate
// (inclusive, YYYY-MM-DD) onwards, ascending by date and capped at limit rows.
//
// A forecast supersedes itself daily and is upserted in place, so this returns one row per
// day and never a history of revisions. An empty slice is a location whose first long-range
// fetch has not completed yet, not a failure.
func (r *WeatherForecastDayRepository) ObtainForecastDays(ctx context.Context, locationID, provider, fromDate string, limit int) ([]domain.WeatherForecastDay, error) {
	if limit <= 0 {
		limit = domain.WeatherOutlookHorizonDays
	}

	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	query := weatherForecastDaySQLSelect +
		" WHERE " + weatherForecastDayLocationIDFieldName + " = ?" +
		" AND " + weatherForecastDayProviderFieldName + " = ?" +
		" AND " + weatherForecastDayForecastDateFieldName + " >= ?" +
		" ORDER BY " + weatherForecastDayForecastDateFieldName + " ASC" +
		" LIMIT ?;"

	rows, err := tx.QueryContext(ctx, query, locationID, provider, fromDate, limit)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	items := make([]domain.WeatherForecastDay, 0, limit)
	for rows.Next() {
		item, scanErr := weatherForecastDayScan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, errors.Join(iterErr, loginjector.NewTraceError())
	}
	return items, nil
}

// ObtainLatestForecastCapture returns the newest captured_at stored for (locationID,
// provider) — the collector's throttle gate. Returns internal.ErrNotFound when the location
// has never been fetched, which the caller must read as "due", not as an error.
func (r *WeatherForecastDayRepository) ObtainLatestForecastCapture(ctx context.Context, locationID, provider string) (time.Time, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return time.Time{}, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	query := "SELECT MAX(" + weatherForecastDayCapturedAtFieldName + ")" +
		" FROM " + weatherForecastDayTableName +
		" WHERE " + weatherForecastDayLocationIDFieldName + " = ?" +
		" AND " + weatherForecastDayProviderFieldName + " = ?;"

	// MAX over an empty set is one row holding NULL, not zero rows, so the miss arrives as
	// a nil string rather than as sql.ErrNoRows.
	var capturedAt *string
	if scanErr := tx.QueryRowContext(ctx, query, locationID, provider).Scan(&capturedAt); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return time.Time{}, internal.ErrNotFound
		}
		return time.Time{}, errors.Join(scanErr, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}
	if capturedAt == nil || *capturedAt == "" {
		return time.Time{}, internal.ErrNotFound
	}

	at, err := time.Parse(time.RFC3339, *capturedAt)
	if err != nil {
		return time.Time{}, errors.Join(err, loginjector.NewTraceError())
	}
	return at.UTC(), nil
}

// RemoveForecastDaysBefore deletes every stored day whose forecast_date is earlier than
// date (YYYY-MM-DD). This is the table's whole retention policy: rows are superseded in
// place while the day is still ahead, and dropped once it is behind.
func (r *WeatherForecastDayRepository) RemoveForecastDaysBefore(ctx context.Context, date string) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM " + weatherForecastDayTableName +
		" WHERE " + weatherForecastDayForecastDateFieldName + " < ?;"
	if _, err := tx.ExecContext(ctx, cmd, date); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

// RetainWeatherForecastDays upserts a whole fetch on the natural key (location_id,
// provider, forecast_date), minting an ID for any record that lacks one.
//
// All rows go in ONE transaction, for two reasons. A day's forecast is a single observation
// of the future and half of it is not a usable answer; and the SQLite write lock is taken
// at BEGIN here (_txlock=immediate), so sixteen separate transactions would take and release
// it sixteen times per location against a collector, notifier and web server sharing the
// file.
func (r *WeatherForecastDayRepository) RetainWeatherForecastDays(ctx context.Context, records []domain.WeatherForecastDay) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	for i := range records {
		record := &records[i]
		if record.ID == "" {
			record.ID = identity.New(identity.KindWeatherForecastDay)
		}
		if _, err := tx.ExecContext(ctx, weatherForecastDaySQLUpsert,
			record.ID,
			record.LocationID,
			record.Provider,
			record.ForecastDate,
			record.CapturedAt.UTC().Format(time.RFC3339),
			record.TempMax,
			record.TempMin,
			record.RainSum,
			record.SnowfallSum,
			record.PrecipSum,
			record.PrecipProbMax,
			record.WeatherCode,
		); err != nil {
			return errors.Join(err, fmt.Errorf("SQL: %s", weatherForecastDaySQLUpsert), loginjector.NewTraceError())
		}
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

const (
	weatherForecastDayTableName              = "weather_forecast_days"
	weatherForecastDayIDFieldName            = "id"
	weatherForecastDayLocationIDFieldName    = "location_id"
	weatherForecastDayProviderFieldName      = "provider"
	weatherForecastDayForecastDateFieldName  = "forecast_date"
	weatherForecastDayCapturedAtFieldName    = "captured_at"
	weatherForecastDayTempMaxFieldName       = "temp_max"
	weatherForecastDayTempMinFieldName       = "temp_min"
	weatherForecastDayRainSumFieldName       = "rain_sum"
	weatherForecastDaySnowfallSumFieldName   = "snowfall_sum"
	weatherForecastDayPrecipSumFieldName     = "precip_sum"
	weatherForecastDayPrecipProbMaxFieldName = "precip_prob_max"
	weatherForecastDayWeatherCodeFieldName   = "weather_code"

	weatherForecastDaySQLSelect = "SELECT " +
		weatherForecastDayIDFieldName + ", " +
		weatherForecastDayLocationIDFieldName + ", " +
		weatherForecastDayProviderFieldName + ", " +
		weatherForecastDayForecastDateFieldName + ", " +
		weatherForecastDayCapturedAtFieldName + ", " +
		weatherForecastDayTempMaxFieldName + ", " +
		weatherForecastDayTempMinFieldName + ", " +
		weatherForecastDayRainSumFieldName + ", " +
		weatherForecastDaySnowfallSumFieldName + ", " +
		weatherForecastDayPrecipSumFieldName + ", " +
		weatherForecastDayPrecipProbMaxFieldName + ", " +
		weatherForecastDayWeatherCodeFieldName +
		" FROM " + weatherForecastDayTableName

	// weatherForecastDaySQLUpsert rewrites every measurement of an existing day in place.
	// id is absent from the SET clause on purpose: the row keeps the identifier it was
	// created with, so nothing that referenced it is invalidated by a refresh.
	weatherForecastDaySQLUpsert = "INSERT INTO " + weatherForecastDayTableName + " (" +
		weatherForecastDayIDFieldName + ", " +
		weatherForecastDayLocationIDFieldName + ", " +
		weatherForecastDayProviderFieldName + ", " +
		weatherForecastDayForecastDateFieldName + ", " +
		weatherForecastDayCapturedAtFieldName + ", " +
		weatherForecastDayTempMaxFieldName + ", " +
		weatherForecastDayTempMinFieldName + ", " +
		weatherForecastDayRainSumFieldName + ", " +
		weatherForecastDaySnowfallSumFieldName + ", " +
		weatherForecastDayPrecipSumFieldName + ", " +
		weatherForecastDayPrecipProbMaxFieldName + ", " +
		weatherForecastDayWeatherCodeFieldName +
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON CONFLICT(" +
		weatherForecastDayLocationIDFieldName + ", " +
		weatherForecastDayProviderFieldName + ", " +
		weatherForecastDayForecastDateFieldName +
		") DO UPDATE SET " +
		weatherForecastDayCapturedAtFieldName + " = excluded." + weatherForecastDayCapturedAtFieldName + ", " +
		weatherForecastDayTempMaxFieldName + " = excluded." + weatherForecastDayTempMaxFieldName + ", " +
		weatherForecastDayTempMinFieldName + " = excluded." + weatherForecastDayTempMinFieldName + ", " +
		weatherForecastDayRainSumFieldName + " = excluded." + weatherForecastDayRainSumFieldName + ", " +
		weatherForecastDaySnowfallSumFieldName + " = excluded." + weatherForecastDaySnowfallSumFieldName + ", " +
		weatherForecastDayPrecipSumFieldName + " = excluded." + weatherForecastDayPrecipSumFieldName + ", " +
		weatherForecastDayPrecipProbMaxFieldName + " = excluded." + weatherForecastDayPrecipProbMaxFieldName + ", " +
		weatherForecastDayWeatherCodeFieldName + " = excluded." + weatherForecastDayWeatherCodeFieldName + ";"
)

func weatherForecastDayScan(s weatherUserCityScanner) (domain.WeatherForecastDay, error) {
	var item domain.WeatherForecastDay
	var capturedAt string

	if err := s.Scan(
		&item.ID,
		&item.LocationID,
		&item.Provider,
		&item.ForecastDate,
		&capturedAt,
		&item.TempMax,
		&item.TempMin,
		&item.RainSum,
		&item.SnowfallSum,
		&item.PrecipSum,
		&item.PrecipProbMax,
		&item.WeatherCode,
	); err != nil {
		return domain.WeatherForecastDay{}, err
	}

	var err error
	if item.CapturedAt, err = time.Parse(time.RFC3339, capturedAt); err != nil {
		return domain.WeatherForecastDay{}, errors.Join(err, loginjector.NewTraceError())
	}
	return item, nil
}
