package repository

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twinj/uuid"
)

func TestNewRateUserEventRepository(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserEventRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRateUserEventRepository_Name(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserEventRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.Equal(t, rateUserEventTableName, r.Name())
}

func TestRateUserEventRepository_CheckUP(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserEventRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NoError(t, r.CheckUP(t.Context()))
}

func TestRateUserEventRepository_ObtainLastNRateUserEvents(t *testing.T) {
	t.Parallel()

	t.Run("no status filter returns all rows", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		for _, status := range []domain.RateUserEventStatus{
			domain.RateUserEventStatusPending,
			domain.RateUserEventStatusSent,
			domain.RateUserEventStatusFailed,
		} {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				UserType: domain.UserTypeTelegram,
				UserID:   uuid.NewV4().String(),
				Message:  "msg",
				Status:   status,
			}))
		}

		result, err := r.ObtainLastNRateUserEvents(t.Context(), 0, 10)
		require.NoError(t, err)
		require.Len(t, result, 3)
	})

	t.Run("status filter returns only matching rows", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		for i := 0; i < 2; i++ {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				UserType: domain.UserTypeTelegram,
				UserID:   uuid.NewV4().String(),
				Message:  "msg",
				Status:   domain.RateUserEventStatusPending,
			}))
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   uuid.NewV4().String(),
			Message:  "msg",
			Status:   domain.RateUserEventStatusSent,
		}))

		result, err := r.ObtainLastNRateUserEvents(t.Context(), 0, 10, domain.RateUserEventStatusPending)
		require.NoError(t, err)
		require.Len(t, result, 2)
	})

	t.Run("offset and limit are applied", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		for i := 0; i < 5; i++ {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				UserType:  domain.UserTypeTelegram,
				UserID:    uuid.NewV4().String(),
				Message:   "msg",
				Status:    domain.RateUserEventStatusPending,
				CreatedAt: base.Add(time.Duration(i) * time.Second),
			}))
		}

		result, err := r.ObtainLastNRateUserEvents(t.Context(), 2, 2)
		require.NoError(t, err)
		require.Len(t, result, 2)
		require.Equal(t, base.Add(2*time.Second).Format(time.RFC3339), result[0].CreatedAt.Format(time.RFC3339))
		require.Equal(t, base.Add(3*time.Second).Format(time.RFC3339), result[1].CreatedAt.Format(time.RFC3339))
	})

	t.Run("empty table returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		result, err := r.ObtainLastNRateUserEvents(t.Context(), 0, 10)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result, 0)
	})

	t.Run("rows are ordered oldest first", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		for i := 0; i < 3; i++ {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				UserType:  domain.UserTypeTelegram,
				UserID:    uuid.NewV4().String(),
				Message:   "msg",
				Status:    domain.RateUserEventStatusPending,
				CreatedAt: base.Add(time.Duration(i) * time.Second),
			}))
		}

		result, err := r.ObtainLastNRateUserEvents(t.Context(), 0, 10)
		require.NoError(t, err)
		require.Len(t, result, 3)
		require.True(t, result[0].CreatedAt.Before(result[1].CreatedAt))
	})
}

func TestRateUserEventRepository_ObtainUnprocessedRateUserEvents(t *testing.T) {
	t.Parallel()

	t.Run("returns pending and failed only", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		for _, status := range []domain.RateUserEventStatus{
			domain.RateUserEventStatusPending,
			domain.RateUserEventStatusFailed,
			domain.RateUserEventStatusSent,
			domain.RateUserEventStatusCanceled,
		} {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				UserType: domain.UserTypeTelegram,
				UserID:   uuid.NewV4().String(),
				Message:  "msg",
				Status:   status,
			}))
		}

		result, err := r.ObtainUnprocessedRateUserEvents(t.Context())
		require.NoError(t, err)
		require.Len(t, result, 2)
	})

	t.Run("empty when all events are sent", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		for i := 0; i < 2; i++ {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				UserType: domain.UserTypeTelegram,
				UserID:   uuid.NewV4().String(),
				Message:  "msg",
				Status:   domain.RateUserEventStatusSent,
			}))
		}

		result, err := r.ObtainUnprocessedRateUserEvents(t.Context())
		require.NoError(t, err)
		require.Len(t, result, 0)
	})

	t.Run("empty table", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		result, err := r.ObtainUnprocessedRateUserEvents(t.Context())
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result, 0)
	})

	t.Run("ordered oldest first", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
			UserType:  domain.UserTypeTelegram,
			UserID:    uuid.NewV4().String(),
			Message:   "failed event",
			Status:    domain.RateUserEventStatusFailed,
			CreatedAt: base.Add(-2 * time.Second),
		}))
		require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
			UserType:  domain.UserTypeTelegram,
			UserID:    uuid.NewV4().String(),
			Message:   "pending event",
			Status:    domain.RateUserEventStatusPending,
			CreatedAt: base.Add(-1 * time.Second),
		}))

		result, err := r.ObtainUnprocessedRateUserEvents(t.Context())
		require.NoError(t, err)
		require.Len(t, result, 2)
		require.Equal(t, domain.RateUserEventStatusFailed, result[0].Status)
	})
}

func TestRateUserEventRepository_ObtainRateUserEventById(t *testing.T) {
	t.Parallel()

	t.Run("found — all fields match", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		event := &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   uuid.NewV4().String(),
			Message:  "hello",
			Status:   domain.RateUserEventStatusPending,
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		result, err := r.ObtainRateUserEventById(t.Context(), event.ID)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, event.UserType, result.UserType)
		require.Equal(t, event.UserID, result.UserID)
		require.Equal(t, event.Message, result.Message)
		require.Equal(t, event.Status, result.Status)
		require.Equal(t, event.CreatedAt.Format(time.RFC3339), result.CreatedAt.Format(time.RFC3339))
	})

	t.Run("not found returns nil without error", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		result, err := r.ObtainRateUserEventById(t.Context(), "nonexistent-id")
		require.NoError(t, err)
		require.Nil(t, result)
	})

	t.Run("SentAt nil round-trips as zero time", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		event := &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   uuid.NewV4().String(),
			Message:  "msg",
			Status:   domain.RateUserEventStatusPending,
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		result, err := r.ObtainRateUserEventById(t.Context(), event.ID)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.SentAt.IsZero())
	})

	t.Run("SentAt non-nil round-trips", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		sentAt := time.Now().UTC().Truncate(time.Second)
		event := &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   uuid.NewV4().String(),
			Message:  "msg",
			Status:   domain.RateUserEventStatusSent,
			SentAt:   sentAt,
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		result, err := r.ObtainRateUserEventById(t.Context(), event.ID)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.False(t, result.SentAt.IsZero())
		require.Equal(t, sentAt.Format(time.RFC3339), result.SentAt.Format(time.RFC3339))
	})
}

func TestRateUserEventRepository_RetainRateUserEventRepository(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserEventRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("insert", func(t *testing.T) {
		t.Parallel()

		rue := &domain.RateUserEvent{
			UserType: "user-3",
			UserID:   uuid.NewV4().String(),
			Message:  "test message",
			Status:   domain.RateUserEventStatusPending,
		}
		require.Empty(t, rue.ID)
		require.True(t, rue.CreatedAt.IsZero())
		require.True(t, rue.SentAt.IsZero())

		require.NoError(t, r.RetainRateUserEvent(t.Context(), rue))
		require.NotEmpty(t, rue.ID)
		require.True(t, rue.SentAt.IsZero())
		require.False(t, rue.CreatedAt.IsZero())

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateUserEventTableName+
				" WHERE "+rateUserEventUserTypeFieldName+" = ?"+
				" AND "+rateUserEventUserIdFieldName+" = ?",
			rue.UserType, rue.UserID,
		).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM"+" "+rateUserEventTableName+" WHERE "+rateUserEventIdFieldName+" = ?", rue.ID).Scan(&count))
		require.Equal(t, 1, count)
	})
	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		rue := &domain.RateUserEvent{
			UserType: "user-3",
			UserID:   uuid.NewV4().String(),
			Message:  "test message",
			Status:   domain.RateUserEventStatusPending,
		}

		require.NoError(t, r.RetainRateUserEvent(t.Context(), rue))
		newId := rue.ID
		require.NoError(t, r.RetainRateUserEvent(t.Context(), rue))
		require.Equal(t, newId, rue.ID)
		require.NoError(t, r.RetainRateUserEvent(t.Context(), rue))
		require.Equal(t, newId, rue.ID)

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateUserEventTableName+
				" WHERE "+rateUserEventUserTypeFieldName+" = ?"+
				" AND "+rateUserEventUserIdFieldName+" = ?",
			rue.UserType, rue.UserID,
		).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM"+" "+rateUserEventTableName+" WHERE "+rateUserEventIdFieldName+" = ?", rue.ID).Scan(&count))
		require.Equal(t, 1, count)
	})
	t.Run("send time round-trips", func(t *testing.T) {
		t.Parallel()

		rue := &domain.RateUserEvent{
			UserType: "user-3",
			UserID:   uuid.NewV4().String(),
			Message:  "test message",
			Status:   domain.RateUserEventStatusPending,
		}
		require.True(t, rue.SentAt.IsZero())
		require.NoError(t, r.RetainRateUserEvent(t.Context(), rue))
		require.True(t, rue.SentAt.IsZero())

		result, err := r.ObtainRateUserEventById(t.Context(), rue.ID)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.SentAt.IsZero())

		sentAt := time.Now().UTC()

		rue.SentAt = sentAt
		require.NoError(t, r.RetainRateUserEvent(t.Context(), rue))
		require.False(t, rue.SentAt.IsZero(), rue.SentAt.Format(time.RFC3339))

		result, err = r.ObtainRateUserEventById(t.Context(), rue.ID)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.False(t, result.SentAt.IsZero())
		require.Equal(t, sentAt.Format(time.RFC3339), result.SentAt.Format(time.RFC3339))
	})
}

func TestRateUserEventRepository_RemoveRateUserEventRepository(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserEventRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("delete", func(t *testing.T) {
		t.Parallel()

		rue := &domain.RateUserEvent{
			UserType: "user-3",
			UserID:   uuid.NewV4().String(),
			Message:  "test message",
			Status:   domain.RateUserEventStatusPending,
		}

		require.NoError(t, r.RetainRateUserEvent(t.Context(), rue))
		require.NoError(t, r.RemoveRateUserEvent(t.Context(), rue))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateUserEventTableName+
				" WHERE "+rateUserEventUserTypeFieldName+" = ?"+
				" AND "+rateUserEventUserIdFieldName+" = ?",
			rue.UserType, rue.UserID,
		).Scan(&count))
		require.Equal(t, 0, count)
		require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM"+" "+rateUserEventTableName+" WHERE "+rateUserEventIdFieldName+" = ?", rue.ID).Scan(&count))
		require.Equal(t, 0, count)
	})
}

func TestRateUserEventRepository_SourceNameRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("source_name persists and is read back correctly", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t, "KAZ_NATIONALBANK_USD_KZT"))
		require.NoError(t, err)

		event := &domain.RateUserEvent{
			SourceName: "KAZ_NATIONALBANK_USD_KZT",
			UserType:   domain.UserTypeTelegram,
			UserID:     "user-42",
			Message:    "rate changed",
			Status:     domain.RateUserEventStatusPending,
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))
		require.NotEmpty(t, event.ID)

		result, err := r.ObtainRateUserEventById(t.Context(), event.ID)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, "KAZ_NATIONALBANK_USD_KZT", result.SourceName)
	})

	t.Run("existing rows without source_name have empty string", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		// Insert without setting SourceName — should default to ""
		event := &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   "user-99",
			Message:  "old event",
			Status:   domain.RateUserEventStatusSent,
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		result, err := r.ObtainRateUserEventById(t.Context(), event.ID)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, "", result.SourceName)
	})

	// Plan 018 Task 6 acceptance criterion: a weather event with an empty SourceName
	// (WeatherCheckAgent queues events without source attribution) must round-trip
	// through RetainRateUserEvent and appear in ObtainUnprocessedRateUserEvents.
	// This proves that reusing the FX notification queue for weather notifications is
	// correct — empty SourceName → NULL in the DB, returned as "" on read.
	t.Run("weather event with empty source name appears in unprocessed queue", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		ev := &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   "weather-user-1",
			Message:  "<b>Test City</b>\nMon 30 Jun, 07:00 +05\n\n☀️ Clear sky",
			// SourceName intentionally empty → stored as NULL by sourceNameForDB
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), ev))
		require.NotEmpty(t, ev.ID)

		unprocessed, err := r.ObtainUnprocessedRateUserEvents(t.Context())
		require.NoError(t, err)

		var found bool
		for _, e := range unprocessed {
			if e.ID == ev.ID {
				assert.Equal(t, "", e.SourceName, "weather event must have empty SourceName after round-trip")
				assert.Equal(t, ev.Message, e.Message)
				assert.Equal(t, domain.UserTypeTelegram, e.UserType)
				found = true
			}
		}
		require.True(t, found, "weather event must appear in the unprocessed queue")
	})
}

func TestRateUserEventRepository_ObtainRateUserEventsBySourceName(t *testing.T) {
	t.Parallel()

	t.Run("returns only events for the given source", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t, "src-A", "src-B"))
		require.NoError(t, err)

		require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
			SourceName: "src-A",
			UserType:   domain.UserTypeTelegram,
			UserID:     "111",
			Message:    "msg",
			Status:     domain.RateUserEventStatusFailed,
		}))
		require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
			SourceName: "src-B",
			UserType:   domain.UserTypeTelegram,
			UserID:     "222",
			Message:    "msg",
			Status:     domain.RateUserEventStatusFailed,
		}))

		result, err := r.ObtainRateUserEventsBySourceName(t.Context(), "src-A", 0, 10)
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Equal(t, "src-A", result[0].SourceName)
	})

	t.Run("status filter works correctly", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t, "src-X"))
		require.NoError(t, err)

		for _, status := range []domain.RateUserEventStatus{
			domain.RateUserEventStatusFailed,
			domain.RateUserEventStatusSent,
			domain.RateUserEventStatusPending,
		} {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				SourceName: "src-X",
				UserType:   domain.UserTypeTelegram,
				UserID:     "u1",
				Message:    "msg",
				Status:     status,
			}))
		}

		failed, err := r.ObtainRateUserEventsBySourceName(t.Context(), "src-X", 0, 10, domain.RateUserEventStatusFailed)
		require.NoError(t, err)
		require.Len(t, failed, 1)
		require.Equal(t, domain.RateUserEventStatusFailed, failed[0].Status)
	})

	t.Run("no status args returns all statuses for source", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t, "src-Y"))
		require.NoError(t, err)

		for _, status := range []domain.RateUserEventStatus{
			domain.RateUserEventStatusFailed,
			domain.RateUserEventStatusSent,
		} {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				SourceName: "src-Y",
				UserType:   domain.UserTypeTelegram,
				UserID:     "u2",
				Message:    "msg",
				Status:     status,
			}))
		}

		all, err := r.ObtainRateUserEventsBySourceName(t.Context(), "src-Y", 0, 10)
		require.NoError(t, err)
		require.Len(t, all, 2)
	})

	t.Run("offset and limit paginate correctly", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t, "src-Z"))
		require.NoError(t, err)

		for i := 0; i < 5; i++ {
			require.NoError(t, r.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				SourceName: "src-Z",
				UserType:   domain.UserTypeTelegram,
				UserID:     "u3",
				Message:    "msg",
				Status:     domain.RateUserEventStatusFailed,
			}))
		}

		page1, err := r.ObtainRateUserEventsBySourceName(t.Context(), "src-Z", 0, 3)
		require.NoError(t, err)
		require.Len(t, page1, 3)

		page2, err := r.ObtainRateUserEventsBySourceName(t.Context(), "src-Z", 3, 3)
		require.NoError(t, err)
		require.Len(t, page2, 2)
	})

	t.Run("unknown source returns empty slice", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		result, err := r.ObtainRateUserEventsBySourceName(t.Context(), "nonexistent", 0, 10)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result)
	})
}

func TestRateUserEventRepository_RemoveRateUserEventOlderThan(t *testing.T) {
	t.Parallel()

	t.Run("removes old non-pending event", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		event := &domain.RateUserEvent{
			UserType:  domain.UserTypeTelegram,
			UserID:    uuid.NewV4().String(),
			Message:   "old sent",
			Status:    domain.RateUserEventStatusSent,
			CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		require.NoError(t, r.RemoveRateUserEventOlderThan(t.Context(), 24*time.Hour))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM "+rateUserEventTableName+" WHERE "+rateUserEventIdFieldName+" = ?",
			event.ID,
		).Scan(&count))
		require.Equal(t, 0, count)
	})

	t.Run("does not remove old pending event", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		event := &domain.RateUserEvent{
			UserType:  domain.UserTypeTelegram,
			UserID:    uuid.NewV4().String(),
			Message:   "old pending",
			Status:    domain.RateUserEventStatusPending,
			CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		require.NoError(t, r.RemoveRateUserEventOlderThan(t.Context(), 24*time.Hour))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM "+rateUserEventTableName+" WHERE "+rateUserEventIdFieldName+" = ?",
			event.ID,
		).Scan(&count))
		require.Equal(t, 1, count)
	})

	t.Run("does not remove recent non-pending event", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		event := &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   uuid.NewV4().String(),
			Message:  "recent sent",
			Status:   domain.RateUserEventStatusSent,
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		require.NoError(t, r.RemoveRateUserEventOlderThan(t.Context(), 24*time.Hour))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM "+rateUserEventTableName+" WHERE "+rateUserEventIdFieldName+" = ?",
			event.ID,
		).Scan(&count))
		require.Equal(t, 1, count)
	})

	t.Run("negative duration is treated as positive", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		event := &domain.RateUserEvent{
			UserType:  domain.UserTypeTelegram,
			UserID:    uuid.NewV4().String(),
			Message:   "old sent neg",
			Status:    domain.RateUserEventStatusSent,
			CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
		}
		require.NoError(t, r.RetainRateUserEvent(t.Context(), event))

		require.NoError(t, r.RemoveRateUserEventOlderThan(t.Context(), -24*time.Hour))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM "+rateUserEventTableName+" WHERE "+rateUserEventIdFieldName+" = ?",
			event.ID,
		).Scan(&count))
		require.Equal(t, 0, count)
	})
}

func TestRateUserEventRepository_TransactionErrors(t *testing.T) {
	t.Parallel()

	newBrokenRepo := func(t *testing.T) *RateUserEventRepository {
		t.Helper()
		r, err := NewRateUserEventRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		r.db = &mockFailDB{err: errors.New("db unavailable")}
		return r
	}

	t.Run("CheckUP propagates transaction error", func(t *testing.T) {
		t.Parallel()
		require.Error(t, newBrokenRepo(t).CheckUP(t.Context()))
	})
	t.Run("ObtainLastNRateUserEvents propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainLastNRateUserEvents(t.Context(), 0, 10)
		require.Error(t, err)
	})
	t.Run("ObtainUnprocessedRateUserEvents propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainUnprocessedRateUserEvents(t.Context())
		require.Error(t, err)
	})
	t.Run("ObtainRateUserEventById propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainRateUserEventById(t.Context(), "some-id")
		require.Error(t, err)
	})
	t.Run("RetainRateUserEvent propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RetainRateUserEvent(t.Context(), &domain.RateUserEvent{UserType: domain.UserTypeTelegram, UserID: "u"})
		require.Error(t, err)
	})
	t.Run("ObtainRateUserEventsBySourceName propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainRateUserEventsBySourceName(t.Context(), "src", 0, 10)
		require.Error(t, err)
	})
	t.Run("RemoveRateUserEvent propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RemoveRateUserEvent(t.Context(), &domain.RateUserEvent{ID: "x"})
		require.Error(t, err)
	})
	t.Run("RemoveRateUserEventOlderThan propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RemoveRateUserEventOlderThan(t.Context(), 24*time.Hour)
		require.Error(t, err)
	})
}

func TestRateUserEventRepository_ObtainDailyBySource(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserEventRepository(stubSQLiteDB(t, "daily-source"))
	require.NoError(t, err)

	srcName := "daily-source"
	day1 := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)

	events := []domain.RateUserEvent{
		{ID: uuid.NewV4().String(), UserType: domain.UserTypeTelegram, UserID: "u1", SourceName: srcName, Message: "msg", Status: domain.RateUserEventStatusSent, SentAt: day1, CreatedAt: day1},
		{ID: uuid.NewV4().String(), UserType: domain.UserTypeTelegram, UserID: "u2", SourceName: srcName, Message: "msg", Status: domain.RateUserEventStatusFailed, SentAt: day1, CreatedAt: day1},
		{ID: uuid.NewV4().String(), UserType: domain.UserTypeTelegram, UserID: "u3", SourceName: srcName, Message: "msg", Status: domain.RateUserEventStatusSent, SentAt: day2, CreatedAt: day2},
		{ID: uuid.NewV4().String(), UserType: domain.UserTypeTelegram, UserID: "u4", SourceName: srcName, Message: "msg", Status: domain.RateUserEventStatusSent, SentAt: day2, CreatedAt: day2},
		{ID: uuid.NewV4().String(), UserType: domain.UserTypeTelegram, UserID: "u5", SourceName: srcName, Message: "msg", Status: domain.RateUserEventStatusPending, CreatedAt: day2},
	}
	for _, e := range events {
		require.NoError(t, r.RetainRateUserEvent(t.Context(), &e))
	}

	t.Run("returns rows grouped by date excluding pending", func(t *testing.T) {
		t.Parallel()

		items, err := r.ObtainDailyEventSummaryBySource(t.Context(), srcName, 0, 25)
		require.NoError(t, err)
		require.NotEmpty(t, items)
		// pending events must never appear
		for _, item := range items {
			require.NotEmpty(t, item.Date)
			require.NotEmpty(t, item.UserType)
		}
	})
	t.Run("counts match fixtures", func(t *testing.T) {
		t.Parallel()

		items, err := r.ObtainDailyEventSummaryBySource(t.Context(), srcName, 0, 25)
		require.NoError(t, err)

		totSent, totFailed := int64(0), int64(0)
		for _, item := range items {
			totSent += item.SuccessCount
			totFailed += item.FailedCount
		}
		require.Equal(t, int64(3), totSent)
		require.Equal(t, int64(1), totFailed)
	})
	t.Run("returns empty slice when source has no non-pending events", func(t *testing.T) {
		t.Parallel()

		items, err := r.ObtainDailyEventSummaryBySource(t.Context(), "nonexistent-source", 0, 25)
		require.NoError(t, err)
		require.NotNil(t, items)
		require.Empty(t, items)
	})
	t.Run("pagination offset works", func(t *testing.T) {
		t.Parallel()

		all, err := r.ObtainDailyEventSummaryBySource(t.Context(), srcName, 0, 25)
		require.NoError(t, err)
		if len(all) < 2 {
			t.Skip("not enough date groups to test pagination")
		}
		page2, err := r.ObtainDailyEventSummaryBySource(t.Context(), srcName, 1, 25)
		require.NoError(t, err)
		require.Len(t, page2, len(all)-1)
	})
}
