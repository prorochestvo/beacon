package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/domain/identity"
)

// NewWeatherObservationRepository returns a repository for the weather_observations table.
func NewWeatherObservationRepository(db db) (*WeatherObservationRepository, error) {
	return &WeatherObservationRepository{db: db}, nil
}

// WeatherObservationRepository persists and retrieves domain.WeatherObservation records.
type WeatherObservationRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *WeatherObservationRepository) Name() string { return weatherObservationTableName }

// CheckUP verifies that the repository can read from the weather_observations table.
func (r *WeatherObservationRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	query := "SELECT COUNT(*) FROM " + weatherObservationTableName + ";"
	var count int64
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	if count < 0 {
		return errors.Join(errors.New("unexpected result"), internal.NewStackTraceError())
	}
	return nil
}

// RetainWeatherObservation inserts a new observation snapshot. The ID is minted
// when empty. Each call stores a new row; deduplication (same location/provider/date)
// is the caller's responsibility via the RemoveWeatherObservationsOlderThan vacuum.
func (r *WeatherObservationRepository) RetainWeatherObservation(ctx context.Context, record *domain.WeatherObservation) error {
	if record == nil {
		return errors.Join(errors.New("weather observation is nil"), internal.NewTraceError())
	}

	if record.ID == "" {
		record.ID = identity.New(identity.KindWeatherObservation)
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	var sunriseStr, sunsetStr *string
	if record.Sunrise != nil {
		s := record.Sunrise.Format(time.RFC3339)
		sunriseStr = &s
	}
	if record.Sunset != nil {
		s := record.Sunset.Format(time.RFC3339)
		sunsetStr = &s
	}

	// Marshal the hourly block to JSON; nil bytes → SQL NULL (no hourly data for this row).
	hourlyJSON, err := record.MarshalHourlyJSON()
	if err != nil {
		return errors.Join(fmt.Errorf("weather observation: marshal hourly_json: %w", err), internal.NewTraceError())
	}
	var hourlyJSONArg interface{}
	if len(hourlyJSON) > 0 {
		hourlyJSONArg = string(hourlyJSON)
	}

	cmd := "INSERT INTO " + weatherObservationTableName + " (" +
		weatherObservationIDFieldName + ", " +
		weatherObservationLocationIDFieldName + ", " +
		weatherObservationProviderFieldName + ", " +
		weatherObservationLatitudeFieldName + ", " +
		weatherObservationLongitudeFieldName + ", " +
		weatherObservationCapturedAtFieldName + ", " +
		weatherObservationForecastDateFieldName + ", " +
		weatherObservationTempMaxFieldName + ", " +
		weatherObservationTempMinFieldName + ", " +
		weatherObservationPrecipSumFieldName + ", " +
		weatherObservationPrecipProbMaxFieldName + ", " +
		weatherObservationWeatherCodeFieldName + ", " +
		weatherObservationSunriseFieldName + ", " +
		weatherObservationSunsetFieldName + ", " +
		weatherObservationTempCurrentFieldName + ", " +
		weatherObservationTempFeelsFieldName + ", " +
		weatherObservationHumidityFieldName + ", " +
		weatherObservationWindSpeedFieldName + ", " +
		weatherObservationWindDirFieldName + ", " +
		weatherObservationPrecipFieldName + ", " +
		weatherObservationCloudCoverFieldName + ", " +
		weatherObservationHourlyJSONFieldName +
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);"

	if _, err := tx.ExecContext(ctx, cmd,
		record.ID,
		record.LocationID,
		record.Provider,
		record.Latitude,
		record.Longitude,
		record.CapturedAt.Format(time.RFC3339),
		record.ForecastDate,
		record.TempMax,
		record.TempMin,
		record.PrecipSum,
		record.PrecipProbMax,
		record.WeatherCode,
		sunriseStr,
		sunsetStr,
		record.TempCurrent,
		record.TempFeels,
		record.Humidity,
		record.WindSpeed,
		record.WindDir,
		record.Precip,
		record.CloudCover,
		hourlyJSONArg,
	); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

// ObtainLatestObservation returns the most recent snapshot for (locationID, provider),
// ordered by captured_at DESC. Returns internal.ErrNotFound when no row exists.
func (r *WeatherObservationRepository) ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	query := weatherObservationSQLSelect +
		" WHERE " + weatherObservationLocationIDFieldName + " = ?" +
		" AND " + weatherObservationProviderFieldName + " = ?" +
		" ORDER BY " + weatherObservationCapturedAtFieldName + " DESC" +
		" LIMIT 1;"

	row := tx.QueryRowContext(ctx, query, locationID, provider)
	item, err := weatherObservationScan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, internal.ErrNotFound
	}
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	return &item, nil
}

// RemoveWeatherObservationsOlderThan deletes all observations whose captured_at is
// older than now minus the given duration. Called from a housekeeping path to vacuum
// stale snapshots.
func (r *WeatherObservationRepository) RemoveWeatherObservationsOlderThan(ctx context.Context, age time.Duration) error {
	cutoff := time.Now().UTC().Add(-age)

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM " + weatherObservationTableName +
		" WHERE " + weatherObservationCapturedAtFieldName + " < ?;"
	if _, err := tx.ExecContext(ctx, cmd, cutoff.Format(time.RFC3339)); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

const (
	weatherObservationTableName              = "weather_observations"
	weatherObservationIDFieldName            = "id"
	weatherObservationLocationIDFieldName    = "location_id"
	weatherObservationProviderFieldName      = "provider"
	weatherObservationLatitudeFieldName      = "latitude"
	weatherObservationLongitudeFieldName     = "longitude"
	weatherObservationCapturedAtFieldName    = "captured_at"
	weatherObservationForecastDateFieldName  = "forecast_date"
	weatherObservationTempMaxFieldName       = "temp_max"
	weatherObservationTempMinFieldName       = "temp_min"
	weatherObservationPrecipSumFieldName     = "precip_sum"
	weatherObservationPrecipProbMaxFieldName = "precip_prob_max"
	weatherObservationWeatherCodeFieldName   = "weather_code"
	weatherObservationSunriseFieldName       = "sunrise"
	weatherObservationSunsetFieldName        = "sunset"
	weatherObservationTempCurrentFieldName   = "temp_current"
	weatherObservationTempFeelsFieldName     = "temp_feels"
	weatherObservationHumidityFieldName      = "humidity"
	weatherObservationWindSpeedFieldName     = "wind_speed"
	weatherObservationWindDirFieldName       = "wind_dir"
	weatherObservationPrecipFieldName        = "precip"
	weatherObservationCloudCoverFieldName    = "cloud_cover"
	// weatherObservationHourlyJSONFieldName is the nullable column storing the
	// Open-Meteo hourly forecast block as a compact JSON array (migration 014).
	// NULL for rows stored before the migration was applied.
	weatherObservationHourlyJSONFieldName = "hourly_json"

	weatherObservationSQLSelect = "SELECT " +
		weatherObservationIDFieldName + ", " +
		weatherObservationLocationIDFieldName + ", " +
		weatherObservationProviderFieldName + ", " +
		weatherObservationLatitudeFieldName + ", " +
		weatherObservationLongitudeFieldName + ", " +
		weatherObservationCapturedAtFieldName + ", " +
		weatherObservationForecastDateFieldName + ", " +
		weatherObservationTempMaxFieldName + ", " +
		weatherObservationTempMinFieldName + ", " +
		weatherObservationPrecipSumFieldName + ", " +
		weatherObservationPrecipProbMaxFieldName + ", " +
		weatherObservationWeatherCodeFieldName + ", " +
		weatherObservationSunriseFieldName + ", " +
		weatherObservationSunsetFieldName + ", " +
		weatherObservationTempCurrentFieldName + ", " +
		weatherObservationTempFeelsFieldName + ", " +
		weatherObservationHumidityFieldName + ", " +
		weatherObservationWindSpeedFieldName + ", " +
		weatherObservationWindDirFieldName + ", " +
		weatherObservationPrecipFieldName + ", " +
		weatherObservationCloudCoverFieldName + ", " +
		weatherObservationHourlyJSONFieldName +
		" FROM " + weatherObservationTableName
)

func weatherObservationScan(s weatherUserCityScanner) (domain.WeatherObservation, error) {
	var item domain.WeatherObservation
	var capturedAt string
	var sunriseStr, sunsetStr *string
	var hourlyJSONStr *string

	if err := s.Scan(
		&item.ID,
		&item.LocationID,
		&item.Provider,
		&item.Latitude,
		&item.Longitude,
		&capturedAt,
		&item.ForecastDate,
		&item.TempMax,
		&item.TempMin,
		&item.PrecipSum,
		&item.PrecipProbMax,
		&item.WeatherCode,
		&sunriseStr,
		&sunsetStr,
		&item.TempCurrent,
		&item.TempFeels,
		&item.Humidity,
		&item.WindSpeed,
		&item.WindDir,
		&item.Precip,
		&item.CloudCover,
		&hourlyJSONStr,
	); err != nil {
		return domain.WeatherObservation{}, err
	}

	var err error
	if item.CapturedAt, err = time.Parse(time.RFC3339, capturedAt); err != nil {
		return domain.WeatherObservation{}, errors.Join(err, internal.NewTraceError())
	}
	if sunriseStr != nil && *sunriseStr != "" {
		t, parseErr := time.Parse(time.RFC3339, *sunriseStr)
		if parseErr != nil {
			return domain.WeatherObservation{}, errors.Join(parseErr, internal.NewTraceError())
		}
		item.Sunrise = &t
	}
	if sunsetStr != nil && *sunsetStr != "" {
		t, parseErr := time.Parse(time.RFC3339, *sunsetStr)
		if parseErr != nil {
			return domain.WeatherObservation{}, errors.Join(parseErr, internal.NewTraceError())
		}
		item.Sunset = &t
	}
	// Unmarshal hourly_json; NULL / empty yields an empty (non-nil) slice via UnmarshalHourlyJSON.
	var hourlyRaw []byte
	if hourlyJSONStr != nil {
		hourlyRaw = []byte(*hourlyJSONStr)
	}
	if unmarshalErr := item.UnmarshalHourlyJSON(hourlyRaw); unmarshalErr != nil {
		return domain.WeatherObservation{}, errors.Join(unmarshalErr, internal.NewTraceError())
	}
	return item, nil
}
