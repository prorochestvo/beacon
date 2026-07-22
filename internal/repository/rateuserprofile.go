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
)

// NewRateUserProfileRepository returns a repository for the rate_user_profiles table.
func NewRateUserProfileRepository(db db) (*RateUserProfileRepository, error) {
	return &RateUserProfileRepository{db: db}, nil
}

// RateUserProfileRepository persists and retrieves domain.RateUserProfile records.
// Identity is composite (user_type, user_id); there is no surrogate ID.
type RateUserProfileRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *RateUserProfileRepository) Name() string { return rateUserProfileTableName }

// CheckUP verifies that the repository can read from the rate_user_profiles table.
func (r *RateUserProfileRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	query := "SELECT COUNT(*) FROM " + rateUserProfileTableName + ";"
	var count int64
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}
	if count < 0 {
		return errors.Join(errors.New("unexpected result"), loginjector.NewTraceError())
	}
	return nil
}

// ObtainRateUserProfileByUserID returns the profile row for (userType, userID)
// or nil with internal.ErrNotFound when no row exists. Callers treat absence
// as "use UTC" — that is, ErrNotFound is not an error condition for the
// notification pipeline.
func (r *RateUserProfileRepository) ObtainRateUserProfileByUserID(ctx context.Context, userType domain.UserType, userID string) (*domain.RateUserProfile, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	query := "SELECT " +
		rateUserProfileUserTypeFieldName + ", " +
		rateUserProfileUserIdFieldName + ", " +
		rateUserProfileTimezoneFieldName + ", " +
		rateUserProfileLocaleFieldName + ", " +
		rateUserProfileUpdatedAtFieldName + ", " +
		rateUserProfileCreatedAtFieldName +
		" FROM " + rateUserProfileTableName +
		" WHERE " + rateUserProfileUserTypeFieldName + " = ? AND " +
		rateUserProfileUserIdFieldName + " = ?;"

	var item domain.RateUserProfile
	var updatedAt, createdAt string
	err = tx.QueryRowContext(ctx, query, userType, userID).Scan(
		&item.UserType,
		&item.UserID,
		&item.Timezone,
		&item.Locale,
		&updatedAt,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, internal.ErrNotFound
	}
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), loginjector.NewTraceError())
	}

	if item.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt); err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	if item.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	return &item, nil
}

// UpsertRateUserProfile inserts or updates the profile row for record's
// (UserType, UserID). Timezone is validated via time.LoadLocation before any
// write; an unknown IANA name returns a PublicError so callers can surface
// the failure to end users. UpdatedAt is set to now on every call; CreatedAt
// is set only on insert.
func (r *RateUserProfileRepository) UpsertRateUserProfile(ctx context.Context, record *domain.RateUserProfile) error {
	if record == nil {
		return errors.Join(errors.New("user profile is nil"), loginjector.NewTraceError())
	}
	if record.UserType == "" || record.UserID == "" {
		return internal.NewPublicError("Invalid user identity.")
	}
	if _, err := time.LoadLocation(record.Timezone); err != nil {
		return internal.NewPublicError("Invalid timezone.")
	}

	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "INSERT INTO " + rateUserProfileTableName + " (" +
		rateUserProfileUserTypeFieldName + ", " +
		rateUserProfileUserIdFieldName + ", " +
		rateUserProfileTimezoneFieldName + ", " +
		rateUserProfileLocaleFieldName + ", " +
		rateUserProfileUpdatedAtFieldName + ", " +
		rateUserProfileCreatedAtFieldName +
		") VALUES (?, ?, ?, ?, ?, ?) " +
		"ON CONFLICT(" + rateUserProfileUserTypeFieldName + ", " +
		rateUserProfileUserIdFieldName + ") DO UPDATE SET " +
		rateUserProfileTimezoneFieldName + " = excluded." + rateUserProfileTimezoneFieldName + ", " +
		rateUserProfileLocaleFieldName + " = excluded." + rateUserProfileLocaleFieldName + ", " +
		rateUserProfileUpdatedAtFieldName + " = excluded." + rateUserProfileUpdatedAtFieldName + ";"

	if _, err := tx.ExecContext(ctx, cmd,
		record.UserType,
		record.UserID,
		record.Timezone,
		record.Locale,
		record.UpdatedAt.Format(time.RFC3339),
		record.CreatedAt.Format(time.RFC3339),
	); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	if err := tx.Commit(); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

const (
	rateUserProfileTableName          = "rate_user_profiles"
	rateUserProfileUserTypeFieldName  = "user_type"
	rateUserProfileUserIdFieldName    = "user_id"
	rateUserProfileTimezoneFieldName  = "timezone"
	rateUserProfileLocaleFieldName    = "locale"
	rateUserProfileUpdatedAtFieldName = "updated_at"
	rateUserProfileCreatedAtFieldName = "created_at"
)
