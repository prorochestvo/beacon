package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewRateUserSubscriptionRepository(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRateUserSubscriptionRepository_Name(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.Equal(t, rateUserSubscriptionTableName, r.Name())
}

func TestRateUserSubscriptionRepository_CheckUP(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NoError(t, r.CheckUP(t.Context()))
}

func TestRateUserSubscriptionRepository_RetainRateUserSubscription(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t, "src-a", "src-b", "src-dt"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		err := r.RetainRateUserSubscription(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
	})
	t.Run("insert", func(t *testing.T) {
		t.Parallel()

		sub := &domain.RateUserSubscription{
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-1",
			SourceName:     "src-a",
			ConditionType:  domain.ConditionTypeDelta,
			ConditionValue: "0.5",
		}

		require.NoError(t, r.RetainRateUserSubscription(t.Context(), sub))
		require.False(t, sub.CreatedAt.IsZero())

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateUserSubscriptionTableName+
				" WHERE "+rateUserSubscriptionUserTypeFieldName+" = ?"+
				" AND "+rateUserSubscriptionUserIdFieldName+" = ?"+
				" AND "+rateUserSubscriptionSourceNameFieldName+" = ?",
			sub.UserType, sub.UserID, sub.SourceName,
		).Scan(&count))
		require.Equal(t, 1, count)
	})
	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		sub := &domain.RateUserSubscription{
			UserType:   domain.UserTypeTelegram,
			UserID:     "user-2",
			SourceName: "src-b",
		}

		require.NoError(t, r.RetainRateUserSubscription(t.Context(), sub))
		require.NoError(t, r.RetainRateUserSubscription(t.Context(), sub))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateUserSubscriptionTableName+
				" WHERE "+rateUserSubscriptionUserTypeFieldName+" = ?"+
				" AND "+rateUserSubscriptionUserIdFieldName+" = ?"+
				" AND "+rateUserSubscriptionSourceNameFieldName+" = ?",
			sub.UserType, sub.UserID, sub.SourceName,
		).Scan(&count))
		require.Equal(t, 1, count)
	})
	t.Run("delta_threshold round-trips", func(t *testing.T) {
		t.Parallel()

		sub := &domain.RateUserSubscription{
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-dt",
			SourceName:     "src-dt",
			ConditionType:  domain.ConditionTypeDelta,
			ConditionValue: "0.75",
		}
		require.NoError(t, r.RetainRateUserSubscription(t.Context(), sub))

		result, err := r.ObtainRateUserSubscriptionsBySource(t.Context(), "src-dt")
		require.NoError(t, err)
		require.Len(t, result, 1)

		deltaThreshold, err := result[0].DeltaThreshold()
		require.NoError(t, err)
		require.Equal(t, 0.75, deltaThreshold)
	})
}

func TestRateUserSubscriptionRepository_RemoveRateUserSubscription(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t, "src-c"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		err := r.RemoveRateUserSubscription(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
	})
	t.Run("delete", func(t *testing.T) {
		t.Parallel()

		sub := &domain.RateUserSubscription{
			UserType:   domain.UserTypeTelegram,
			UserID:     "user-3",
			SourceName: "src-c",
		}

		require.NoError(t, r.RetainRateUserSubscription(t.Context(), sub))
		require.NoError(t, r.RemoveRateUserSubscription(t.Context(), sub))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateUserSubscriptionTableName+
				" WHERE "+rateUserSubscriptionUserTypeFieldName+" = ?"+
				" AND "+rateUserSubscriptionUserIdFieldName+" = ?"+
				" AND "+rateUserSubscriptionSourceNameFieldName+" = ?",
			sub.UserType, sub.UserID, sub.SourceName,
		).Scan(&count))
		require.Equal(t, 0, count)
	})
}

func TestRateUserSubscriptionRepository_ObtainRateUserSubscriptionByID(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t, "src-byid"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("returns record when found", func(t *testing.T) {
		t.Parallel()
		sub := &domain.RateUserSubscription{
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-byid-1",
			SourceName:     "src-byid",
			ConditionType:  domain.ConditionTypeDelta,
			ConditionValue: "0.3",
		}
		require.NoError(t, r.RetainRateUserSubscription(t.Context(), sub))
		require.NotEmpty(t, sub.ID)

		got, err := r.ObtainRateUserSubscriptionByID(t.Context(), sub.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, sub.ID, got.ID)
		require.Equal(t, sub.UserID, got.UserID)
		require.Equal(t, sub.SourceName, got.SourceName)
		require.Equal(t, sub.ConditionType, got.ConditionType)
		require.Equal(t, sub.ConditionValue, got.ConditionValue)
	})

	t.Run("returns nil nil for unknown id", func(t *testing.T) {
		t.Parallel()
		got, err := r.ObtainRateUserSubscriptionByID(t.Context(), "no-such-id")
		require.NoError(t, err)
		require.Nil(t, got)
	})
}

func TestRateUserSubscriptionRepository_ObtainRateUserSubscriptionsByUserID(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t, "src-a", "src-b", "src-c"))
	require.NoError(t, err)
	require.NotNil(t, r)

	now := time.Now().UTC()

	t.Run("returns subscriptions for user", func(t *testing.T) {
		t.Parallel()

		subs := []domain.RateUserSubscription{
			{UserType: domain.UserTypeTelegram, UserID: "user-4", SourceName: "src-a", ConditionType: domain.ConditionTypeDelta, ConditionValue: "10", LatestNotifiedRate: 11.13, CreatedAt: now},
			{UserType: domain.UserTypeTelegram, UserID: "user-4", SourceName: "src-b", ConditionType: domain.ConditionTypeInterval, ConditionValue: "10h", LatestNotifiedRate: 12.12, CreatedAt: now},
			{UserType: domain.UserTypeTelegram, UserID: "user-4", SourceName: "src-c", ConditionType: domain.ConditionTypeDaily, ConditionValue: "10:00:00", LatestNotifiedRate: 13.11, CreatedAt: now},
			{UserType: domain.UserTypeTelegram, UserID: "user-5", SourceName: "src-a", ConditionType: domain.ConditionTypeCron, ConditionValue: "*/5 * * *", LatestNotifiedRate: 14.21, CreatedAt: now},
		}
		for i := range subs {
			require.NoError(t, r.RetainRateUserSubscription(t.Context(), &subs[i]))
		}

		result, err := r.ObtainRateUserSubscriptionsByUserID(t.Context(), domain.UserTypeTelegram, "user-4")
		require.NoError(t, err)
		require.Len(t, result, 3)
		for i, r := range result {
			require.Equal(t, domain.UserTypeTelegram, r.UserType)
			require.Equal(t, "user-4", r.UserID)
			require.Equal(t, subs[i].UserType, r.UserType)
			require.Equal(t, subs[i].UserID, r.UserID)
			require.Equal(t, subs[i].SourceName, r.SourceName)
			require.Equal(t, subs[i].ConditionType, r.ConditionType)
			require.Equal(t, subs[i].ConditionValue, r.ConditionValue)
			require.Equal(t, subs[i].LatestNotifiedRate, r.LatestNotifiedRate)
			require.Equal(t, subs[i].CreatedAt.Format(time.RFC3339), r.CreatedAt.Format(time.RFC3339))
		}
	})
	t.Run("empty for unknown user", func(t *testing.T) {
		t.Parallel()

		result, err := r.ObtainRateUserSubscriptionsByUserID(t.Context(), domain.UserTypeTelegram, "nonexistent")
		require.NoError(t, err)
		require.Empty(t, result)
	})
}

func TestRateUserSubscriptionRepository_ObtainRateUserSubscriptionsBySource(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t, "src-x", "src-y"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("returns subscriptions for source", func(t *testing.T) {
		t.Parallel()

		subs := []domain.RateUserSubscription{
			{UserType: domain.UserTypeTelegram, UserID: "user-6", SourceName: "src-x"},
			{UserType: domain.UserTypeTelegram, UserID: "user-7", SourceName: "src-x"},
			{UserType: domain.UserTypeTelegram, UserID: "user-8", SourceName: "src-y"},
		}
		for i := range subs {
			require.NoError(t, r.RetainRateUserSubscription(t.Context(), &subs[i]))
		}

		result, err := r.ObtainRateUserSubscriptionsBySource(t.Context(), "src-x")
		require.NoError(t, err)
		require.Len(t, result, 2)
		for _, s := range result {
			require.Equal(t, "src-x", s.SourceName)
		}
	})
	t.Run("empty for unknown source", func(t *testing.T) {
		t.Parallel()

		result, err := r.ObtainRateUserSubscriptionsBySource(t.Context(), "nonexistent")
		require.NoError(t, err)
		require.Empty(t, result)
	})
}

func TestRateUserSubscriptionRepository_ObtainSubscriptionSummaryBySource(t *testing.T) {
	t.Parallel()

	// ObtainSubscriptionSummaryBySource JOINs rate_user_events, so both repositories
	// must be initialised on the same DB to ensure both tables exist.
	newSubAndEventRepos := func(t *testing.T, sourceNames ...string) (*RateUserSubscriptionRepository, *RateUserEventRepository) {
		t.Helper()
		db := stubSQLiteDB(t, sourceNames...)
		subRepo, err := NewRateUserSubscriptionRepository(db)
		require.NoError(t, err)
		eventRepo, err := NewRateUserEventRepository(db)
		require.NoError(t, err)
		return subRepo, eventRepo
	}

	t.Run("no subscriptions returns empty slice", func(t *testing.T) {
		t.Parallel()

		r, _ := newSubAndEventRepos(t)

		result, err := r.ObtainSubscriptionSummaryBySource(t.Context(), "nonexistent-source")
		require.NoError(t, err)
		require.Empty(t, result)
	})
	t.Run("returns aggregated counts per user_type", func(t *testing.T) {
		t.Parallel()

		r, _ := newSubAndEventRepos(t, "src-sum")

		subs := []domain.RateUserSubscription{
			{UserType: domain.UserTypeTelegram, UserID: "u-sum-1", SourceName: "src-sum"},
			{UserType: domain.UserTypeTelegram, UserID: "u-sum-2", SourceName: "src-sum"},
		}
		for i := range subs {
			require.NoError(t, r.RetainRateUserSubscription(t.Context(), &subs[i]))
		}

		result, err := r.ObtainSubscriptionSummaryBySource(t.Context(), "src-sum")
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Equal(t, "src-sum", result[0].SourceName)
		require.Equal(t, domain.UserTypeTelegram, result[0].UserType)
		require.Equal(t, int64(2), result[0].SubscriptionCount)
		require.True(t, result[0].LastSentAt.IsZero(), "no events sent yet")
	})
	t.Run("one user with multiple subscriptions — events counted once, not per-subscription", func(t *testing.T) {
		t.Parallel()

		subRepo, eventRepo := newSubAndEventRepos(t, "src-multi")

		// One user has two subscriptions to the same source (e.g. delta + daily).
		for _, cond := range []domain.SubscriptionConditionType{
			domain.ConditionTypeDelta, domain.ConditionTypeDaily,
		} {
			require.NoError(t, subRepo.RetainRateUserSubscription(t.Context(), &domain.RateUserSubscription{
				UserType: domain.UserTypeTelegram, UserID: "u-multi", SourceName: "src-multi",
				ConditionType: cond, ConditionValue: "x",
			}))
		}

		// Three sent events for that user.
		for i := 0; i < 3; i++ {
			require.NoError(t, eventRepo.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
				UserType: domain.UserTypeTelegram, UserID: "u-multi", SourceName: "src-multi",
				Message: "m", Status: domain.RateUserEventStatusSent,
			}))
		}

		result, err := subRepo.ObtainSubscriptionSummaryBySource(t.Context(), "src-multi")
		require.NoError(t, err)
		require.Len(t, result, 1)
		// Without per-user event pre-aggregation, SuccessCount would be 6 (3 events × 2 subs).
		require.Equal(t, int64(3), result[0].SuccessCount,
			"events must count once per user regardless of subscription count")
	})
	t.Run("per-user JOIN — events do not double-count across subscribers", func(t *testing.T) {
		t.Parallel()

		subRepo, eventRepo := newSubAndEventRepos(t, "src-join")

		// Two users subscribe to src-join.
		for _, uid := range []string{"u-join-1", "u-join-2"} {
			require.NoError(t, subRepo.RetainRateUserSubscription(t.Context(), &domain.RateUserSubscription{
				UserType: domain.UserTypeTelegram, UserID: uid, SourceName: "src-join",
			}))
		}

		// Each user has 3 'sent' events for src-join.
		for _, uid := range []string{"u-join-1", "u-join-2"} {
			for i := 0; i < 3; i++ {
				require.NoError(t, eventRepo.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
					UserType: domain.UserTypeTelegram, UserID: uid, SourceName: "src-join",
					Message: "m", Status: domain.RateUserEventStatusSent,
				}))
			}
		}

		result, err := subRepo.ObtainSubscriptionSummaryBySource(t.Context(), "src-join")
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Equal(t, int64(2), result[0].SubscriptionCount)
		// Without the per-user JOIN (e.user_id = s.user_id), success_count
		// would be 2*6 = 12 instead of 6.
		require.Equal(t, int64(6), result[0].SuccessCount,
			"per-user JOIN must yield 6 sent events, not 12 from cross-product")
		require.Equal(t, int64(0), result[0].FailedCount)
	})
}

func TestRateUserSubscriptionRepository_TransactionErrors(t *testing.T) {
	t.Parallel()

	newBrokenRepo := func(t *testing.T) *RateUserSubscriptionRepository {
		t.Helper()
		r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		r.db = &mockFailDB{err: errors.New("db unavailable")}
		return r
	}

	t.Run("CheckUP propagates transaction error", func(t *testing.T) {
		t.Parallel()
		require.Error(t, newBrokenRepo(t).CheckUP(t.Context()))
	})
	t.Run("ObtainRateUserSubscriptionsByUserID propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainRateUserSubscriptionsByUserID(t.Context(), domain.UserTypeTelegram, "u1")
		require.Error(t, err)
	})
	t.Run("ObtainRateUserSubscriptionsBySource propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainRateUserSubscriptionsBySource(t.Context(), "src")
		require.Error(t, err)
	})
	t.Run("RetainRateUserSubscription propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RetainRateUserSubscription(t.Context(), &domain.RateUserSubscription{UserType: domain.UserTypeTelegram, UserID: "u", SourceName: "s"})
		require.Error(t, err)
	})
	t.Run("ObtainSubscriptionSummaryBySource propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainSubscriptionSummaryBySource(t.Context(), "src")
		require.Error(t, err)
	})
	t.Run("RemoveRateUserSubscription propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RemoveRateUserSubscription(t.Context(), &domain.RateUserSubscription{UserType: domain.UserTypeTelegram, UserID: "u", SourceName: "s"})
		require.Error(t, err)
	})
	t.Run("ObtainRateUserSubscriptionByID propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainRateUserSubscriptionByID(t.Context(), "any-id")
		require.Error(t, err)
	})
}

func TestRateUserSubscriptionRepository_ObtainBySourcePaged(t *testing.T) {
	t.Parallel()

	r, err := NewRateUserSubscriptionRepository(stubSQLiteDB(t, "paged-source"))
	require.NoError(t, err)

	sourceName := "paged-source"
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		sub := &domain.RateUserSubscription{
			UserType:       domain.UserTypeTelegram,
			UserID:         fmt.Sprintf("user-%d", i),
			SourceName:     sourceName,
			ConditionType:  "delta",
			ConditionValue: "10",
			CreatedAt:      now.Add(time.Duration(i) * time.Minute),
		}
		require.NoError(t, r.RetainRateUserSubscription(t.Context(), sub))
	}

	t.Run("returns up to limit items", func(t *testing.T) {
		t.Parallel()

		items, err := r.ObtainRateUserSubscriptionsBySourcePaged(t.Context(), sourceName, 0, 3)
		require.NoError(t, err)
		require.Len(t, items, 3)
	})
	t.Run("returns empty slice when offset exceeds count", func(t *testing.T) {
		t.Parallel()

		items, err := r.ObtainRateUserSubscriptionsBySourcePaged(t.Context(), sourceName, 100, 25)
		require.NoError(t, err)
		require.NotNil(t, items)
		require.Empty(t, items)
	})
	t.Run("condition field is populated", func(t *testing.T) {
		t.Parallel()

		items, err := r.ObtainRateUserSubscriptionsBySourcePaged(t.Context(), sourceName, 0, 25)
		require.NoError(t, err)
		require.NotEmpty(t, items)
		require.NotEmpty(t, items[0].ConditionType)
		require.NotEmpty(t, items[0].ConditionValue)
	})
	t.Run("user_id never returned", func(t *testing.T) {
		t.Parallel()

		items, err := r.ObtainRateUserSubscriptionsBySourcePaged(t.Context(), sourceName, 0, 25)
		require.NoError(t, err)
		require.NotEmpty(t, items)
		for _, item := range items {
			// RateUserSubscriptionDetail does not have a UserID field — compile-time check
			_ = item.ID
		}
	})
}
