package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/domain/identity"
)

// NewRateUserEventRepository returns a repository for the rate_user_events table.
func NewRateUserEventRepository(db db) (*RateUserEventRepository, error) {
	return &RateUserEventRepository{db: db}, nil
}

// RateUserEventRepository persists and retrieves domain.RateUserEvent notification records.
type RateUserEventRepository struct {
	db db
}

// Name returns the name of the underlying database table.
func (r *RateUserEventRepository) Name() string { return rateUserEventTableName }

// CheckUP verifies that the repository can read from the rate_user_events table.
func (r *RateUserEventRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateUserEventCount(tx, ctx, ";")
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	if count < 0 {
		err = errors.New("unexpected result")
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// ObtainLastNRateUserEvents returns paginated events optionally filtered by one or more statuses,
// ordered by created_at ASC. Pass no status arguments to return all statuses.
// Always returns a non-nil slice on success.
func (r *RateUserEventRepository) ObtainLastNRateUserEvents(ctx context.Context, offset, limit int64, status ...domain.RateUserEventStatus) ([]domain.RateUserEvent, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	// whereClause is used for COUNT (no LIMIT/OFFSET — those must not be applied to COUNT).
	whereClause := ""
	var statusArgs []any
	if l := len(status); l > 0 {
		whereClause = fmt.Sprintf("WHERE %s in (%s)\n", rateUserEventStatusFieldName, strings.Repeat("?, ", l-1)+"?")
		for _, s := range status {
			statusArgs = append(statusArgs, s)
		}
	}

	count, err := rateUserEventCount(tx, ctx, whereClause+";", statusArgs...)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	if count == 0 {
		return []domain.RateUserEvent{}, nil
	}

	// Build full condition with ORDER BY / LIMIT / OFFSET for the SELECT query.
	fullCondition := whereClause + "ORDER BY " + rateUserEventCreatedAtFieldName + " ASC\nLIMIT ?\nOFFSET ?;"
	selectArgs := append(statusArgs, limit, offset)

	query := rateUserEventSqlSelect + "\n" + fullCondition
	dbRows, err := tx.QueryContext(ctx, query, selectArgs...)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	defer func() { err = errors.Join(err, dbRows.Close()) }()

	items := make([]domain.RateUserEvent, 0, count)
	for dbRows.Next() {
		var item domain.RateUserEvent
		var createdAt string
		var sentAt *string

		if scanErr := dbRows.Scan(
			&item.ID,
			&item.SourceName,
			&item.UserType,
			&item.UserID,
			&item.Message,
			&item.Status,
			&item.LastError,
			&createdAt,
			&sentAt,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}

		item.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, createdAt, err)
			return nil, errors.Join(err, internal.NewTraceError())
		}

		if sentAt != nil && *sentAt != "" {
			item.SentAt, err = time.Parse(time.RFC3339, *sentAt)
			if err != nil {
				err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, *sentAt, err)
				return nil, errors.Join(err, internal.NewTraceError())
			}
		}

		items = append(items, item)
	}

	return items, nil
}

// ObtainRateUserEventsBySourceName returns paginated events for one source,
// optionally filtered by status. Pass no status args to get all statuses.
// Events produced by cross-source dedup are stored with source_name = NULL and
// will NOT appear in this query; use the global events view for those.
func (r *RateUserEventRepository) ObtainRateUserEventsBySourceName(ctx context.Context, sourceName string, offset, limit int64, status ...domain.RateUserEventStatus) ([]domain.RateUserEvent, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	args := []any{sourceName}
	where := "WHERE " + rateUserEventSourceNameFieldName + " = ?"

	if len(status) > 0 {
		placeholders := strings.Repeat("?, ", len(status)-1) + "?"
		where += fmt.Sprintf(" AND %s IN (%s)", rateUserEventStatusFieldName, placeholders)
		for _, s := range status {
			args = append(args, s)
		}
	}

	count, err := rateUserEventCount(tx, ctx, where+";", args...)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	if count == 0 {
		return []domain.RateUserEvent{}, nil
	}

	fullCond := where + "\nORDER BY " + rateUserEventCreatedAtFieldName + " DESC\nLIMIT ?\nOFFSET ?;"
	rows, err := tx.QueryContext(ctx,
		rateUserEventSqlSelect+"\n"+fullCond,
		append(args, limit, offset)...,
	)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	items := make([]domain.RateUserEvent, 0, count)
	for rows.Next() {
		var item domain.RateUserEvent
		var createdAt string
		var sentAt *string
		if scanErr := rows.Scan(
			&item.ID, &item.SourceName, &item.UserType, &item.UserID,
			&item.Message, &item.Status, &item.LastError,
			&createdAt, &sentAt,
		); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		var parseErr error
		item.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
		if parseErr != nil {
			log.Print(errors.Join(
				fmt.Errorf("rate user event %s has invalid created_at %q: %w", item.ID, createdAt, parseErr),
				internal.NewTraceError(),
			))
		}
		if sentAt != nil && *sentAt != "" {
			item.SentAt, parseErr = time.Parse(time.RFC3339, *sentAt)
			if parseErr != nil {
				log.Print(errors.Join(
					fmt.Errorf("rate user event %s has invalid sent_at %q: %w", item.ID, *sentAt, parseErr),
					internal.NewTraceError(),
				))
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// ObtainDailyEventSummaryBySource returns aggregated event counts grouped by (user_type, date)
// for the given source, excluding pending events. Ordered by date DESC with pagination.
func (r *RateUserEventRepository) ObtainDailyEventSummaryBySource(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserEventDailySummary, error) {
	query := `SELECT ` +
		rateUserEventUserTypeFieldName + `, ` +
		`date(` + rateUserEventSentAtFieldName + `) AS event_date, ` +
		`SUM(CASE WHEN ` + rateUserEventStatusFieldName + ` = 'sent'   THEN 1 ELSE 0 END) AS success_count, ` +
		`SUM(CASE WHEN ` + rateUserEventStatusFieldName + ` = 'failed' THEN 1 ELSE 0 END) AS failed_count ` +
		`FROM ` + rateUserEventTableName + ` ` +
		`WHERE ` + rateUserEventSourceNameFieldName + ` = ? ` +
		`AND ` + rateUserEventStatusFieldName + ` != 'pending' ` +
		`AND ` + rateUserEventSentAtFieldName + ` IS NOT NULL ` +
		`GROUP BY ` + rateUserEventUserTypeFieldName + `, event_date ` +
		`ORDER BY event_date DESC ` +
		`LIMIT ? OFFSET ?;`

	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, errors.Join(err, internal.NewStackTraceError())
	}
	defer printRollbackError(tx)

	rows, err := tx.QueryContext(ctx, query, sourceName, limit, offset)
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("SQL: %s", query), internal.NewTraceError())
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	items := make([]domain.RateUserEventDailySummary, 0, limit)
	for rows.Next() {
		var item domain.RateUserEventDailySummary
		var date *string
		if scanErr := rows.Scan(&item.UserType, &date, &item.SuccessCount, &item.FailedCount); scanErr != nil {
			return nil, errors.Join(scanErr, internal.NewTraceError())
		}
		if date != nil {
			item.Date = *date
		}
		items = append(items, item)
	}

	return items, nil
}

// ObtainUnprocessedRateUserEvents returns all events in pending or failed status,
// ordered by created_at ASC. Always returns a non-nil slice on success.
func (r *RateUserEventRepository) ObtainUnprocessedRateUserEvents(ctx context.Context) ([]domain.RateUserEvent, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	rows, err := rateUserEventQueryContext(tx, ctx, "WHERE "+rateUserEventStatusFieldName+" in (?, ?) ORDER BY "+rateUserEventCreatedAtFieldName+" ASC;", domain.RateUserEventStatusPending, domain.RateUserEventStatusFailed)
	if err != nil {
		return nil, err
	}

	return rows, nil
}

// ObtainRateUserEventById returns the event with the given ID, or nil if no row matches.
func (r *RateUserEventRepository) ObtainRateUserEventById(ctx context.Context, id string) (*domain.RateUserEvent, error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	defer printRollbackError(tx)

	row, err := rateUserEventQueryRowContext(tx, ctx, "WHERE "+rateUserEventIdFieldName+" = ? ORDER BY "+rateUserEventCreatedAtFieldName+" ASC;", id)
	if err != nil {
		return nil, err
	}

	return row, nil
}

// RetainRateUserEvent inserts or updates the given notification event record.
func (r *RateUserEventRepository) RetainRateUserEvent(ctx context.Context, record *domain.RateUserEvent) error {
	if record == nil {
		err := errors.New("notification record is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	if record.ID == "" {
		record.ID = identity.New(identity.KindRateUserEvent)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.Status == "" {
		record.Status = domain.RateUserEventStatusPending
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	count, err := rateUserEventCount(tx, ctx, " WHERE "+rateUserEventIdFieldName+" = ?;", record.ID)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	var sentAt *string = nil
	if !record.SentAt.IsZero() {
		s := record.SentAt.Format(time.RFC3339)
		sentAt = &s
	}

	// source_name is nullable and carries an FK to rate_sources(name). Empty
	// string would violate the FK (no rate_source has name=''); send NULL when
	// the event is not bound to a source.
	sourceName := sourceNameForDB(record.SourceName)

	var res sql.Result
	if count > 0 {
		cmd := "UPDATE" + " " + rateUserEventTableName + " SET " +
			rateUserEventSourceNameFieldName + " = ?, " +
			rateUserEventUserTypeFieldName + " = ?, " +
			rateUserEventUserIdFieldName + " = ?, " +
			rateUserEventMessageFieldName + " = ?, " +
			rateUserEventStatusFieldName + " = ?, " +
			rateUserEventLastErrorFieldName + " = ?, " +
			rateUserEventSentAtFieldName + " = ?, " +
			rateUserEventCreatedAtFieldName + " = ? " +
			"WHERE " + rateUserEventIdFieldName + " = ?;"
		res, err = tx.ExecContext(ctx, cmd,
			sourceName,
			record.UserType,
			record.UserID,
			record.Message,
			record.Status,
			record.LastError,
			sentAt,
			record.CreatedAt.Format(time.RFC3339),
			record.ID,
		)
	} else {
		cmd := "INSERT INTO" + " " + rateUserEventTableName + " (" +
			rateUserEventIdFieldName + ", " +
			rateUserEventSourceNameFieldName + ", " +
			rateUserEventUserTypeFieldName + ", " +
			rateUserEventUserIdFieldName + ", " +
			rateUserEventMessageFieldName + ", " +
			rateUserEventStatusFieldName + ", " +
			rateUserEventLastErrorFieldName + ", " +
			rateUserEventSentAtFieldName + ", " +
			rateUserEventCreatedAtFieldName +
			") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);"
		res, err = tx.ExecContext(ctx, cmd,
			record.ID,
			sourceName,
			record.UserType,
			record.UserID,
			record.Message,
			record.Status,
			record.LastError,
			sentAt,
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

// RemoveRateUserEvent deletes the given notification event record by ID.
func (r *RateUserEventRepository) RemoveRateUserEvent(ctx context.Context, record *domain.RateUserEvent) error {
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

	cmd := "DELETE FROM" + " " + rateUserEventTableName + " WHERE " + rateUserEventIdFieldName + " = ?;"
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

// RemoveRateUserEventOlderThan deletes all non-pending events created more than duration ago.
func (r *RateUserEventRepository) RemoveRateUserEventOlderThan(ctx context.Context, duration time.Duration) error {
	if duration < 0 {
		duration = time.Duration(math.Abs(float64(duration)))
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer printRollbackError(tx)

	cmd := "DELETE FROM" + " " + rateUserEventTableName +
		" WHERE " + rateUserEventCreatedAtFieldName + " < ?" +
		" AND " + rateUserEventStatusFieldName + " != 'pending';"
	before := time.Now().UTC().Add(-duration)

	_, err = tx.ExecContext(ctx, cmd, before.Format(time.RFC3339))
	if err != nil {
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
	rateUserEventTableName           = "rate_user_events"
	rateUserEventIdFieldName         = "id"
	rateUserEventSourceNameFieldName = "source_name"
	rateUserEventUserTypeFieldName   = "user_type"
	rateUserEventUserIdFieldName     = "user_id"
	rateUserEventMessageFieldName    = "message"
	rateUserEventStatusFieldName     = "status"
	rateUserEventLastErrorFieldName  = "last_error"
	rateUserEventCreatedAtFieldName  = "created_at"
	rateUserEventSentAtFieldName     = "sent_at"

	// rateUserEventSqlSelect emits IFNULL(source_name, '') so callers can scan
	// into a non-pointer string field; source_name is nullable to support events
	// that are not bound to a source.
	rateUserEventSqlSelect = "SELECT\n" +
		rateUserEventIdFieldName + ", " +
		"IFNULL(" + rateUserEventSourceNameFieldName + ", '') AS " + rateUserEventSourceNameFieldName + ", " +
		rateUserEventUserTypeFieldName + ", " +
		rateUserEventUserIdFieldName + ", " +
		rateUserEventMessageFieldName + ", " +
		rateUserEventStatusFieldName + ", " +
		rateUserEventLastErrorFieldName + ", " +
		rateUserEventCreatedAtFieldName + ", " +
		rateUserEventSentAtFieldName +
		"\nFROM " + rateUserEventTableName
)

// sourceNameForDB returns nil for empty source name so the INSERT/UPDATE
// produces SQL NULL and skips FK enforcement; SQLite only checks the FK on
// non-NULL values.
func sourceNameForDB(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func rateUserEventCount(tx *sql.Tx, ctx context.Context, condition string, args ...any) (int64, error) {
	query := "SELECT\n" +
		" COUNT(*)\n" +
		"FROM " + rateUserEventTableName + "\n" + condition

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

func rateUserEventQueryContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (items []domain.RateUserEvent, err error) {
	count, err := rateUserEventCount(tx, ctx, condition, args...)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return
	}
	if count == 0 {
		items = []domain.RateUserEvent{}
		return
	}

	query := rateUserEventSqlSelect + "\n" + condition

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("SQL: %s", query))
		err = errors.Join(err, internal.NewTraceError())
		return
	}
	defer func(rows io.Closer) { err = errors.Join(err, rows.Close()) }(rows)

	items = make([]domain.RateUserEvent, 0, count)

	for rows.Next() {
		var item domain.RateUserEvent
		var createdAt string
		var sentAt *string

		err = rows.Scan(
			&item.ID,
			&item.SourceName,
			&item.UserType,
			&item.UserID,
			&item.Message,
			&item.Status,
			&item.LastError,
			&createdAt,
			&sentAt,
		)
		if err != nil {
			err = errors.Join(err, internal.NewTraceError())
			return
		}

		item.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, createdAt, err)
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}

		if sentAt != nil && *sentAt != "" {
			item.SentAt, err = time.Parse(time.RFC3339, *sentAt)
			if err != nil {
				err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, *sentAt, err)
				err = errors.Join(err, internal.NewTraceError())
				return nil, err
			}
		} else {
			item.SentAt = time.Time{}
		}

		items = append(items, item)
	}

	return
}

func rateUserEventQueryRowContext(tx *sql.Tx, ctx context.Context, condition string, args ...any) (*domain.RateUserEvent, error) {
	query := rateUserEventSqlSelect + "\n" + condition

	var item domain.RateUserEvent
	var createdAt string
	var sentAt *string
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&item.ID,
		&item.SourceName,
		&item.UserType,
		&item.UserID,
		&item.Message,
		&item.Status,
		&item.LastError,
		&createdAt,
		&sentAt,
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
		err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, createdAt, err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if sentAt != nil && *sentAt != "" {
		item.SentAt, err = time.Parse(time.RFC3339, *sentAt)
		if err != nil {
			err = fmt.Errorf("rate %s has invalid timestamp %s: %w", item.ID, *sentAt, err)
			err = errors.Join(err, internal.NewTraceError())
			return nil, err
		}
	} else {
		item.SentAt = time.Time{}
	}

	return &item, nil
}
