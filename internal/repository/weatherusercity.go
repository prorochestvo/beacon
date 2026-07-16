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

// NewWeatherUserCityRepository returns a repository for the weather_user_cities table.
func NewWeatherUserCityRepository(db db) (*WeatherUserCityRepository, error) {
	return &WeatherUserCityRepository{db: db}, nil
}

// WeatherUserCityRepository persists and retrieves domain.WeatherUserCity records.
type WeatherUserCityRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *WeatherUserCityRepository) Name() string { return weatherUserCityTableName }

// CheckUP verifies that the repository can read from the weather_user_cities table.
func (r *WeatherUserCityRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	query := "SELECT COUNT(*) FROM " + weatherUserCityTableName + ";"
	var count int64
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	if count < 0 {
		return errors.Join(errors.New("unexpected result"), internal.NewStackTraceError())
	}
	return nil
}

// RetainWeatherUserCity inserts or updates a city subscription. On insert the ID
// is minted when empty. On conflict on (user_type, user_id, location_id, notify_kind)
// the row is updated in place so a re-subscribe refreshes resolved fields.
// UpdatedAt is always set to now; CreatedAt is set only on insert. AlertLatched is
// written only on insert (from record.AlertLatched) and is never touched by the
// ON CONFLICT DO UPDATE path — callers that need to change an existing row's latch
// use SetWeatherAlertLatched or MarkWeatherAlertFired instead.
func (r *WeatherUserCityRepository) RetainWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error {
	if record == nil {
		return errors.Join(errors.New("weather user city is nil"), internal.NewTraceError())
	}

	now := time.Now().UTC()
	if record.ID == "" {
		record.ID = identity.New(identity.KindWeatherUserCity)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	var lastNotifiedAt *string
	if !record.LastNotifiedAt.IsZero() {
		s := record.LastNotifiedAt.Format(time.RFC3339)
		lastNotifiedAt = &s
	}

	// AlertLatched is insert-only: it is deliberately absent from the ON CONFLICT
	// DO UPDATE SET clause below, so a re-subscribe or ensure-thaw retain never stomps
	// a real latch state back to the caller's default.
	var alertLatched int
	if record.AlertLatched {
		alertLatched = 1
	}

	cmd := "INSERT INTO " + weatherUserCityTableName + " (" +
		weatherUserCityIDFieldName + ", " +
		weatherUserCityUserTypeFieldName + ", " +
		weatherUserCityUserIDFieldName + ", " +
		weatherUserCityLocationIDFieldName + ", " +
		weatherUserCityDisplayNameFieldName + ", " +
		weatherUserCityLatitudeFieldName + ", " +
		weatherUserCityLongitudeFieldName + ", " +
		weatherUserCityTimezoneFieldName + ", " +
		weatherUserCityCountryFieldName + ", " +
		weatherUserCityAdmin1FieldName + ", " +
		weatherUserCityGismeteoCityIDFieldName + ", " +
		weatherUserCityNotifyKindFieldName + ", " +
		weatherUserCityNotifyHourFieldName + ", " +
		weatherUserCityConditionValueFieldName + ", " +
		weatherUserCityLastNotifiedAtFieldName + ", " +
		weatherUserCityAlertLatchedFieldName + ", " +
		weatherUserCityUpdatedAtFieldName + ", " +
		weatherUserCityCreatedAtFieldName +
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON CONFLICT(" +
		weatherUserCityUserTypeFieldName + ", " +
		weatherUserCityUserIDFieldName + ", " +
		weatherUserCityLocationIDFieldName + ", " +
		weatherUserCityNotifyKindFieldName +
		") DO UPDATE SET " +
		weatherUserCityDisplayNameFieldName + " = excluded." + weatherUserCityDisplayNameFieldName + ", " +
		weatherUserCityLatitudeFieldName + " = excluded." + weatherUserCityLatitudeFieldName + ", " +
		weatherUserCityLongitudeFieldName + " = excluded." + weatherUserCityLongitudeFieldName + ", " +
		weatherUserCityTimezoneFieldName + " = excluded." + weatherUserCityTimezoneFieldName + ", " +
		weatherUserCityCountryFieldName + " = excluded." + weatherUserCityCountryFieldName + ", " +
		weatherUserCityAdmin1FieldName + " = excluded." + weatherUserCityAdmin1FieldName + ", " +
		weatherUserCityGismeteoCityIDFieldName + " = excluded." + weatherUserCityGismeteoCityIDFieldName + ", " +
		weatherUserCityNotifyHourFieldName + " = excluded." + weatherUserCityNotifyHourFieldName + ", " +
		weatherUserCityConditionValueFieldName + " = excluded." + weatherUserCityConditionValueFieldName + ", " +
		weatherUserCityUpdatedAtFieldName + " = excluded." + weatherUserCityUpdatedAtFieldName +
		// RETURNING ensures record.ID reflects the actually-stored id: the original on
		// conflict (id is not in the SET clause so it is never overwritten) and the
		// newly minted id on a clean insert.
		" RETURNING " + weatherUserCityIDFieldName

	if err := tx.QueryRowContext(ctx, cmd,
		record.ID,
		record.UserType,
		record.UserID,
		record.LocationID,
		record.DisplayName,
		record.Latitude,
		record.Longitude,
		record.Timezone,
		record.Country,
		record.Admin1,
		record.GismeteoCityID,
		record.NotifyKind,
		record.NotifyHour,
		record.ConditionValue,
		lastNotifiedAt,
		alertLatched,
		record.UpdatedAt.Format(time.RFC3339),
		record.CreatedAt.Format(time.RFC3339),
	).Scan(&record.ID); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

// ObtainWeatherUserCitiesByUserID returns all city subscriptions for the given user.
// Always returns a non-nil slice on success.
func (r *WeatherUserCityRepository) ObtainWeatherUserCitiesByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.WeatherUserCity, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	condition := "WHERE " + weatherUserCityUserTypeFieldName + " = ? AND " + weatherUserCityUserIDFieldName + " = ?;"
	return weatherUserCityQueryContext(tx, ctx, condition, userType, userID)
}

// ObtainWeatherUserCityByID returns the city subscription row for the given primary key.
// Returns (nil, internal.ErrNotFound) when no matching row exists.
func (r *WeatherUserCityRepository) ObtainWeatherUserCityByID(ctx context.Context, id string) (*domain.WeatherUserCity, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	condition := "WHERE " + weatherUserCityIDFieldName + " = ?;"
	items, err := weatherUserCityQueryContext(tx, ctx, condition, id)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, internal.ErrNotFound
	}
	return &items[0], nil
}

// RemoveWeatherUserCity deletes the given city subscription by ID.
func (r *WeatherUserCityRepository) RemoveWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error {
	if record == nil {
		return errors.Join(errors.New("weather user city is nil"), internal.NewTraceError())
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM " + weatherUserCityTableName + " WHERE " + weatherUserCityIDFieldName + " = ?;"
	if _, err := tx.ExecContext(ctx, cmd, record.ID); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

// RemoveWeatherUserCitiesByLocation deletes every subscription row (all notify kinds,
// including the forced alert_thaw row) for one (userType, userID, locationID) in a single
// atomic check-and-delete. Used by the explicit "remove city" action so the whole city,
// thaw included, is cleared atomically. Returns internal.ErrNotFound when no row matches
// (missing location or a location owned entirely by a different user).
func (r *WeatherUserCityRepository) RemoveWeatherUserCitiesByLocation(
	ctx context.Context, userType domain.UserType, userID, locationID string,
) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM " + weatherUserCityTableName +
		" WHERE " + weatherUserCityUserTypeFieldName + " = ? AND " +
		weatherUserCityUserIDFieldName + " = ? AND " +
		weatherUserCityLocationIDFieldName + " = ?;"
	res, err := tx.ExecContext(ctx, cmd, userType, userID, locationID)
	if err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	if affected == 0 {
		return errors.Join(errors.New("unexpected result: no rows affected"), internal.ErrNotFound, internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

// ObtainDistinctWeatherLocations returns one entry per distinct location_id across all
// active city subscriptions. The collector uses this to determine which locations to fetch.
func (r *WeatherUserCityRepository) ObtainDistinctWeatherLocations(ctx context.Context) ([]domain.WeatherUserCity, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	// MIN(...) on non-grouped columns satisfies standard SQL and ensures consistent
	// results when multiple users share the same location_id.
	query := "SELECT MIN(" +
		weatherUserCityIDFieldName + "), " +
		weatherUserCityLocationIDFieldName + ", MIN(" +
		weatherUserCityLatitudeFieldName + "), MIN(" +
		weatherUserCityLongitudeFieldName + "), MIN(" +
		weatherUserCityGismeteoCityIDFieldName + ")" +
		" FROM " + weatherUserCityTableName +
		" GROUP BY " + weatherUserCityLocationIDFieldName + ";"

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var items []domain.WeatherUserCity
	for rows.Next() {
		var item domain.WeatherUserCity
		if scanErr := rows.Scan(
			&item.ID,
			&item.LocationID,
			&item.Latitude,
			&item.Longitude,
			&item.GismeteoCityID,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.WeatherUserCity{}
	}
	return items, nil
}

// ObtainDueWeatherUserCities returns all city subscriptions matching the given
// notify kind. The caller applies IsMorningDue to filter by the current time because
// that requires loading each city's timezone.
func (r *WeatherUserCityRepository) ObtainDueWeatherUserCities(ctx context.Context, notifyKind domain.WeatherNotifyKind) ([]domain.WeatherUserCity, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	condition := "WHERE " + weatherUserCityNotifyKindFieldName + " = ?;"
	return weatherUserCityQueryContext(tx, ctx, condition, notifyKind)
}

// AdvanceLastNotifiedAt updates last_notified_at for the given city ID so that
// IsMorningDue will not fire again on the same local calendar day. This is the
// morning-summary cursor only; alert kinds use MarkWeatherAlertFired instead, which
// updates last_notified_at with the forecast_date fire cursor semantics.
func (r *WeatherUserCityRepository) AdvanceLastNotifiedAt(ctx context.Context, id string, when time.Time) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	cmd := "UPDATE " + weatherUserCityTableName +
		" SET " + weatherUserCityLastNotifiedAtFieldName + " = ?" +
		" WHERE " + weatherUserCityIDFieldName + " = ?;"
	if _, err := tx.ExecContext(ctx, cmd, when.Format(time.RFC3339), id); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

// SetWeatherAlertLatched persists the edge-trigger latch for one alert subscription.
// It updates only alert_latched (not updated_at — the latch is system state, not a user
// edit, and not last_notified_at — a re-arm is not a fire). Callers write only on a latch
// transition, never per tick (see WeatherCheckAgent).
func (r *WeatherUserCityRepository) SetWeatherAlertLatched(ctx context.Context, id string, latched bool) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	var latchedInt int
	if latched {
		latchedInt = 1
	}
	cmd := "UPDATE " + weatherUserCityTableName +
		" SET " + weatherUserCityAlertLatchedFieldName + " = ?" +
		" WHERE " + weatherUserCityIDFieldName + " = ?;"
	if _, err := tx.ExecContext(ctx, cmd, latchedInt, id); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

// MarkWeatherAlertFired records that an alert fired for a given forecast_date: it sets
// alert_latched = 1 and stores firedForDate in last_notified_at (the per-forecast_date fire
// cursor for alert kinds). Pass domain.ForecastDateKey(obs.ForecastDate) as firedForDate.
// Does not touch updated_at. Call only after the notification is successfully enqueued.
func (r *WeatherUserCityRepository) MarkWeatherAlertFired(ctx context.Context, id string, firedForDate time.Time) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	cmd := "UPDATE " + weatherUserCityTableName +
		" SET " + weatherUserCityAlertLatchedFieldName + " = 1, " +
		weatherUserCityLastNotifiedAtFieldName + " = ?" +
		" WHERE " + weatherUserCityIDFieldName + " = ?;"
	if _, err := tx.ExecContext(ctx, cmd, firedForDate.Format(time.RFC3339), id); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), internal.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, internal.NewStackTraceError())
	}
	return nil
}

const (
	weatherUserCityTableName               = "weather_user_cities"
	weatherUserCityIDFieldName             = "id"
	weatherUserCityUserTypeFieldName       = "user_type"
	weatherUserCityUserIDFieldName         = "user_id"
	weatherUserCityLocationIDFieldName     = "location_id"
	weatherUserCityDisplayNameFieldName    = "display_name"
	weatherUserCityLatitudeFieldName       = "latitude"
	weatherUserCityLongitudeFieldName      = "longitude"
	weatherUserCityTimezoneFieldName       = "timezone"
	weatherUserCityCountryFieldName        = "country"
	weatherUserCityAdmin1FieldName         = "admin1"
	weatherUserCityGismeteoCityIDFieldName = "gismeteo_city_id"
	weatherUserCityNotifyKindFieldName     = "notify_kind"
	weatherUserCityNotifyHourFieldName     = "notify_hour"
	weatherUserCityConditionValueFieldName = "condition_value"
	weatherUserCityLastNotifiedAtFieldName = "last_notified_at"
	weatherUserCityAlertLatchedFieldName   = "alert_latched"
	weatherUserCityUpdatedAtFieldName      = "updated_at"
	weatherUserCityCreatedAtFieldName      = "created_at"

	weatherUserCitySQLSelect = "SELECT " +
		weatherUserCityIDFieldName + ", " +
		weatherUserCityUserTypeFieldName + ", " +
		weatherUserCityUserIDFieldName + ", " +
		weatherUserCityLocationIDFieldName + ", " +
		weatherUserCityDisplayNameFieldName + ", " +
		weatherUserCityLatitudeFieldName + ", " +
		weatherUserCityLongitudeFieldName + ", " +
		weatherUserCityTimezoneFieldName + ", " +
		weatherUserCityCountryFieldName + ", " +
		weatherUserCityAdmin1FieldName + ", " +
		weatherUserCityGismeteoCityIDFieldName + ", " +
		weatherUserCityNotifyKindFieldName + ", " +
		weatherUserCityNotifyHourFieldName + ", " +
		weatherUserCityConditionValueFieldName + ", " +
		weatherUserCityLastNotifiedAtFieldName + ", " +
		weatherUserCityAlertLatchedFieldName + ", " +
		weatherUserCityUpdatedAtFieldName + ", " +
		weatherUserCityCreatedAtFieldName +
		" FROM " + weatherUserCityTableName
)

func weatherUserCityQueryContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) ([]domain.WeatherUserCity, error) {
	query := weatherUserCitySQLSelect + " " + condition

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var items []domain.WeatherUserCity
	for rows.Next() {
		item, scanErr := weatherUserCityScan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.WeatherUserCity{}
	}
	return items, nil
}

// weatherUserCityScanner is satisfied by *sql.Row and *sql.Rows.
type weatherUserCityScanner interface {
	Scan(dest ...any) error
}

func weatherUserCityScan(s weatherUserCityScanner) (domain.WeatherUserCity, error) {
	var item domain.WeatherUserCity
	var lastNotifiedAt *string
	var alertLatched int
	var updatedAt, createdAt string

	if err := s.Scan(
		&item.ID,
		&item.UserType,
		&item.UserID,
		&item.LocationID,
		&item.DisplayName,
		&item.Latitude,
		&item.Longitude,
		&item.Timezone,
		&item.Country,
		&item.Admin1,
		&item.GismeteoCityID,
		&item.NotifyKind,
		&item.NotifyHour,
		&item.ConditionValue,
		&lastNotifiedAt,
		&alertLatched,
		&updatedAt,
		&createdAt,
	); err != nil {
		return domain.WeatherUserCity{}, errors.Join(err, internal.NewTraceError())
	}
	item.AlertLatched = alertLatched != 0

	var err error
	if item.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt); err != nil {
		return domain.WeatherUserCity{}, errors.Join(err, internal.NewTraceError())
	}
	if item.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return domain.WeatherUserCity{}, errors.Join(err, internal.NewTraceError())
	}
	if lastNotifiedAt != nil && *lastNotifiedAt != "" {
		if item.LastNotifiedAt, err = time.Parse(time.RFC3339, *lastNotifiedAt); err != nil {
			return domain.WeatherUserCity{}, errors.Join(err, internal.NewTraceError())
		}
	}

	return item, nil
}
