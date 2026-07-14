package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// NewWeatherGismeteoCityRepository returns a repository for the weather_gismeteo_cities table.
func NewWeatherGismeteoCityRepository(db db) (*WeatherGismeteoCityRepository, error) {
	return &WeatherGismeteoCityRepository{db: db}, nil
}

// WeatherGismeteoCityRepository retrieves gismeteo coverage rows from the
// weather_gismeteo_cities table. It is the data-driven replacement for the
// former hand-curated gismeteoCities Go map.
type WeatherGismeteoCityRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *WeatherGismeteoCityRepository) Name() string { return weatherGismeteoCityTableName }

// CheckUP verifies the repository can read from the weather_gismeteo_cities table.
func (r *WeatherGismeteoCityRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	var probe int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM "+weatherGismeteoCityTableName+" LIMIT 1;").Scan(&probe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// ObtainGismeteoCoverage returns the full gismeteo coverage map keyed by
// location_id (the Open-Meteo geocoding id). Always returns a non-nil map on
// success; an empty map is a valid state meaning "gismeteo coverage disabled",
// not an error. The named return err captures the rows.Close error via the
// deferred join.
func (r *WeatherGismeteoCityRepository) ObtainGismeteoCoverage(ctx context.Context) (coverage map[string]domain.WeatherGismeteoCity, err error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	query := "SELECT " +
		weatherGismeteoCityLocationIDFieldName + ", " +
		weatherGismeteoCitySlugFieldName + ", " +
		weatherGismeteoCityGismeteoIDFieldName + ", " +
		weatherGismeteoCityLabelFieldName +
		" FROM " + weatherGismeteoCityTableName + ";"

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func(rows io.Closer) { err = errors.Join(err, rows.Close()) }(rows)

	coverage = make(map[string]domain.WeatherGismeteoCity)
	for rows.Next() {
		var city domain.WeatherGismeteoCity
		if scanErr := rows.Scan(
			&city.LocationID,
			&city.Slug,
			&city.GismeteoID,
			&city.Label,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		coverage[city.LocationID] = city
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return coverage, nil
}

const (
	weatherGismeteoCityTableName           = "weather_gismeteo_cities"
	weatherGismeteoCityLocationIDFieldName = "location_id"
	weatherGismeteoCitySlugFieldName       = "slug"
	weatherGismeteoCityGismeteoIDFieldName = "gismeteo_id"
	weatherGismeteoCityLabelFieldName      = "label"
)
