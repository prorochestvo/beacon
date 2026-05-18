package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/twinj/uuid"
)

// NewRateUserSubscriptionRepository returns a repository for the rate_user_subscriptions table.
func NewRateUserSubscriptionRepository(db db) (*RateUserSubscriptionRepository, error) {
	return &RateUserSubscriptionRepository{db: db}, nil
}

// RateUserSubscriptionRepository persists and retrieves domain.RateUserSubscription records.
type RateUserSubscriptionRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *RateUserSubscriptionRepository) Name() string { return rateUserSubscriptionTableName }

// CheckUP verifies that the repository can read from the rate_user_subscriptions table.
func (r *RateUserSubscriptionRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateUserSubscriptionCount(tx, ctx, ";")
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

// ObtainRateUserSubscriptionsByUserID returns all subscriptions for the given user type and ID.
// Always returns a non-nil slice on success.
func (r *RateUserSubscriptionRepository) ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error) {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	rows, err := rateUserSubscriptionQueryContext(tx, ctx, "WHERE "+rateUserSubscriptionUserTypeFieldName+" = ? AND "+rateUserSubscriptionUserIdFieldName+" = ?;", userType, userID)
	if err != nil {
		return nil, err
	}

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	return rows, nil
}

// ObtainRateUserSubscriptionsBySource returns all subscriptions for the given source name.
// Always returns a non-nil slice on success.
func (r *RateUserSubscriptionRepository) ObtainRateUserSubscriptionsBySource(ctx context.Context, sourceName string) ([]domain.RateUserSubscription, error) {
	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	rows, err := rateUserSubscriptionQueryContext(tx, ctx, "WHERE "+rateUserSubscriptionSourceNameFieldName+" = ?;", sourceName)
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

// RetainRateUserSubscription inserts or updates the given subscription record.
// UpdatedAt is always set to the current UTC time; CreatedAt is set only on insert.
func (r *RateUserSubscriptionRepository) RetainRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error {
	if record == nil {
		err := errors.New("user subscription is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	now := time.Now().UTC()

	if record.ID == "" {
		record.ID = generateRateUserSubscriptionID()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateUserSubscriptionCount(tx, ctx, "WHERE "+rateUserSubscriptionIdFieldName+" = ?;", record.ID)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	var res sql.Result
	if count > 0 {
		cmd := "UPDATE" + " " + rateUserSubscriptionTableName + " SET " +
			rateUserSubscriptionUserTypeFieldName + " = ?, " +
			rateUserSubscriptionUserIdFieldName + " = ?, " +
			rateUserSubscriptionSourceNameFieldName + " = ?, " +
			rateUserSubscriptionConditionTypeFieldName + " = ?, " +
			rateUserSubscriptionConditionValueFieldName + " = ?, " +
			rateUserSubscriptionLatestNotifiedRateFieldName + " = ?, " +
			rateUserSubscriptionUpdatedAtFieldName + " = ? " +
			" WHERE " + rateUserSubscriptionIdFieldName + " = ?;"
		res, err = tx.ExecContext(
			ctx, cmd,
			record.UserType,
			record.UserID,
			record.SourceName,
			record.ConditionType,
			record.ConditionValue,
			record.LatestNotifiedRate,
			record.UpdatedAt.Format(time.RFC3339),
			record.ID,
		)
	} else {
		cmd := "INSERT INTO" + " " + rateUserSubscriptionTableName +
			" (" +
			rateUserSubscriptionIdFieldName + ", " +
			rateUserSubscriptionUserTypeFieldName + ", " +
			rateUserSubscriptionUserIdFieldName + ", " +
			rateUserSubscriptionSourceNameFieldName + ", " +
			rateUserSubscriptionConditionTypeFieldName + ", " +
			rateUserSubscriptionConditionValueFieldName + ", " +
			rateUserSubscriptionLatestNotifiedRateFieldName + ", " +
			rateUserSubscriptionUpdatedAtFieldName + ", " +
			rateUserSubscriptionCreatedAtFieldName +
			") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);"
		res, err = tx.ExecContext(
			ctx, cmd,
			record.ID,
			record.UserType,
			record.UserID,
			record.SourceName,
			record.ConditionType,
			record.ConditionValue,
			record.LatestNotifiedRate,
			record.UpdatedAt.Format(time.RFC3339),
			record.CreatedAt.Format(time.RFC3339),
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

// ObtainRateUserSubscriptionsBySourcePaged returns up to limit subscriptions for the
// given source, ordered by updated_at DESC with OFFSET = (page-1)*limit.
func (r *RateUserSubscriptionRepository) ObtainRateUserSubscriptionsBySourcePaged(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserSubscriptionDetail, error) {
	query := "SELECT " +
		rateUserSubscriptionIdFieldName + ", " +
		rateUserSubscriptionUserTypeFieldName + ", " +
		rateUserSubscriptionSourceNameFieldName + ", " +
		rateUserSubscriptionConditionTypeFieldName + ", " +
		rateUserSubscriptionConditionValueFieldName + ", " +
		rateUserSubscriptionUpdatedAtFieldName +
		" FROM " + rateUserSubscriptionTableName +
		" WHERE " + rateUserSubscriptionSourceNameFieldName + " = ?" +
		" ORDER BY " + rateUserSubscriptionUpdatedAtFieldName + " DESC" +
		" LIMIT ? OFFSET ?;"

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	rows, err := tx.QueryContext(ctx, query, sourceName, limit, offset)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	items := make([]domain.RateUserSubscriptionDetail, 0, limit)
	for rows.Next() {
		var item domain.RateUserSubscriptionDetail
		var updatedAt string
		if scanErr := rows.Scan(
			&item.ID, &item.UserType, &item.SourceName,
			&item.ConditionType, &item.ConditionValue, &updatedAt,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		var parseErr error
		item.LatestNotifiedAt, parseErr = time.Parse(time.RFC3339, updatedAt)
		if parseErr != nil {
			log.Print(errors.Join(
				fmt.Errorf("subscription %s has invalid updated_at %q: %w", item.ID, updatedAt, parseErr),
				internal.NewTraceError(),
			))
		}
		items = append(items, item)
	}

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	return items, nil
}

// ObtainSubscriptionSummaryBySource returns one row per (source_name, user_type) pair
// with aggregated subscription and event counts. user_id is never returned.
func (r *RateUserSubscriptionRepository) ObtainSubscriptionSummaryBySource(ctx context.Context, sourceName string) ([]domain.RateUserSubscriptionSummary, error) {
	const query = "SELECT" + `
    s.source_name,
    s.user_type,
    COUNT(DISTINCT s.user_id)                                AS subscription_count,
    MAX(e.sent_at)                                           AS last_sent_at,
    SUM(CASE WHEN e.status='sent'   THEN 1 ELSE 0 END)      AS success_count,
    SUM(CASE WHEN e.status='failed' THEN 1 ELSE 0 END)      AS failed_count
FROM rate_user_subscriptions s
LEFT JOIN rate_user_events e
    ON e.source_name = s.source_name AND e.user_type = s.user_type
WHERE s.source_name = ?
GROUP BY s.source_name, s.user_type;`

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	rows, err := tx.QueryContext(ctx, query, sourceName)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	result := make([]domain.RateUserSubscriptionSummary, 0)
	for rows.Next() {
		var s domain.RateUserSubscriptionSummary
		var lastSentAt *string
		if scanErr := rows.Scan(
			&s.SourceName, &s.UserType,
			&s.SubscriptionCount, &lastSentAt,
			&s.SuccessCount, &s.FailedCount,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		if lastSentAt != nil && *lastSentAt != "" {
			parsed, parseErr := time.Parse(time.RFC3339, *lastSentAt)
			if parseErr != nil {
				log.Print(errors.Join(
					fmt.Errorf("subscription summary for source %q has invalid last_sent_at %q: %w", s.SourceName, *lastSentAt, parseErr),
					internal.NewTraceError(),
				))
			} else {
				s.LastSentAt = parsed
			}
		}
		result = append(result, s)
	}

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	return result, nil
}

// RemoveRateUserSubscription deletes the given subscription record by ID.
func (r *RateUserSubscriptionRepository) RemoveRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error {
	if record == nil {
		err := errors.New("user subscription is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM" + " " + rateUserSubscriptionTableName + " WHERE " + rateUserSubscriptionIdFieldName + " = ?;"
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

const (
	rateUserSubscriptionTableName                   = "rate_user_subscriptions"
	rateUserSubscriptionIdFieldName                 = "id"
	rateUserSubscriptionUserTypeFieldName           = "user_type"
	rateUserSubscriptionUserIdFieldName             = "user_id"
	rateUserSubscriptionSourceNameFieldName         = "source_name"
	rateUserSubscriptionConditionTypeFieldName      = "condition_type"
	rateUserSubscriptionConditionValueFieldName     = "condition_value"
	rateUserSubscriptionLatestNotifiedRateFieldName = "latest_notified_rate"
	rateUserSubscriptionUpdatedAtFieldName          = "updated_at"
	rateUserSubscriptionCreatedAtFieldName          = "created_at"

	rateUserSubscriptionSqlSelect = "SELECT\n" +
		rateUserSubscriptionIdFieldName + ", " +
		rateUserSubscriptionUserTypeFieldName + ", " +
		rateUserSubscriptionUserIdFieldName + ", " +
		rateUserSubscriptionSourceNameFieldName + ", " +
		rateUserSubscriptionConditionTypeFieldName + ", " +
		rateUserSubscriptionConditionValueFieldName + ", " +
		rateUserSubscriptionLatestNotifiedRateFieldName + ", " +
		rateUserSubscriptionUpdatedAtFieldName + ", " +
		rateUserSubscriptionCreatedAtFieldName +
		"\nFROM " + rateUserSubscriptionTableName
)

func generateRateUserSubscriptionID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("RUS%04d%02d%02d%02d%02d%02dZ%dT%X", now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), uuid.NewV4().Bytes())
}

func rateUserSubscriptionCount(tx *sql.Tx, ctx context.Context, condition string, args ...any) (int64, error) {
	query := "SELECT\n" +
		" COUNT(*)\n" +
		"FROM " + rateUserSubscriptionTableName + "\n" + condition

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

func rateUserSubscriptionQueryContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (items []domain.RateUserSubscription, err error) {
	count, err := rateUserSubscriptionCount(tx, ctx, condition, args...)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return
	}
	if count == 0 {
		items = []domain.RateUserSubscription{}
		return
	}

	query := rateUserSubscriptionSqlSelect + "\n" + condition

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return
	}
	defer func(rows io.Closer) { err = errors.Join(err, rows.Close()) }(rows)

	items = make([]domain.RateUserSubscription, 0, count)

	for rows.Next() {
		var item domain.RateUserSubscription
		var createdAt, updatedAt string

		err = rows.Scan(
			&item.ID,
			&item.UserType,
			&item.UserID,
			&item.SourceName,
			&item.ConditionType,
			&item.ConditionValue,
			&item.LatestNotifiedRate,
			&updatedAt,
			&createdAt,
		)
		if err != nil {
			err = errors.Join(err, internal.NewTraceError())
			return
		}

		item.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			err = errors.Join(err, internal.NewTraceError())
			return
		}

		item.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
		if err != nil {
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}

		items = append(items, item)
	}

	return
}

func rateUserSubscriptionQueryRowContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (*domain.RateUserSubscription, error) {
	query := rateUserSubscriptionSqlSelect + "\n" + condition

	var item domain.RateUserSubscription
	var createdAt, updatedAt string
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&item.ID,
		&item.UserType,
		&item.UserID,
		&item.SourceName,
		&item.ConditionType,
		&item.ConditionValue,
		&item.LatestNotifiedRate,
		&updatedAt,
		&createdAt,
	)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	item.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	item.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	return &item, nil
}
