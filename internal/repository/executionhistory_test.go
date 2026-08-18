package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestNewExecutionHistoryRepository(t *testing.T) {
	t.Parallel()

	r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestExecutionHistoryRepository_Name(t *testing.T) {
	t.Parallel()

	r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.Equal(t, executionHistoryTableName, r.Name())
}

func TestExecutionHistoryRepository_CheckUP(t *testing.T) {
	t.Parallel()

	r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NoError(t, r.CheckUP(t.Context()))
}

func TestExecutionHistoryRepository_RetainAndObtain(t *testing.T) {
	t.Parallel()

	r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
	require.NoError(t, err)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		err := r.RetainExecutionHistory(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
	})
	t.Run("insert success record", func(t *testing.T) {
		t.Parallel()

		h := &domain.ExecutionHistory{
			SourceName: "halyk_bank",
			Success:    true,
			Timestamp:  time.Now().UTC().Truncate(time.Second),
		}
		require.NoError(t, r.RetainExecutionHistory(t.Context(), h))
		require.NotEmpty(t, h.ID)
	})
	t.Run("insert failure record", func(t *testing.T) {
		t.Parallel()

		h := &domain.ExecutionHistory{
			SourceName: "kaspi_bank",
			Success:    false,
			Error:      "connection refused",
			Timestamp:  time.Now().UTC().Truncate(time.Second),
		}
		require.NoError(t, r.RetainExecutionHistory(t.Context(), h))
		require.NotEmpty(t, h.ID)
	})
}

func TestExecutionHistoryRepository_ObtainLastN(t *testing.T) {
	t.Parallel()

	r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
	require.NoError(t, err)

	t.Run("zero rows returns empty non-nil slice", func(t *testing.T) {
		t.Parallel()

		records, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "nonexistent", 5, false)
		require.NoError(t, err)
		require.NotNil(t, records)
		require.Empty(t, records)
	})
	t.Run("successOnly filters failures", func(t *testing.T) {
		t.Parallel()

		src := "filtered-source"
		now := time.Now().UTC()

		rows := []domain.ExecutionHistory{
			{SourceName: src, Success: true, Timestamp: now.Add(-2 * time.Minute)},
			{SourceName: src, Success: false, Error: "oops", Timestamp: now.Add(-time.Minute)},
			{SourceName: src, Success: true, Timestamp: now},
		}
		for _, row := range rows {
			require.NoError(t, r.RetainExecutionHistory(t.Context(), &row))
		}

		result, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), src, 10, true)
		require.NoError(t, err)
		require.Len(t, result, 2, "only successful rows")
		for _, rec := range result {
			require.True(t, rec.Success)
		}
	})
	t.Run("successOnly=false returns all rows", func(t *testing.T) {
		t.Parallel()

		src := "all-rows-source"
		now := time.Now().UTC()

		rows := []domain.ExecutionHistory{
			{SourceName: src, Success: true, Timestamp: now.Add(-2 * time.Minute)},
			{SourceName: src, Success: false, Error: "err", Timestamp: now.Add(-time.Minute)},
			{SourceName: src, Success: true, Timestamp: now},
		}
		for _, row := range rows {
			require.NoError(t, r.RetainExecutionHistory(t.Context(), &row))
		}

		result, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), src, 10, false)
		require.NoError(t, err)
		require.Len(t, result, 3)
	})
	t.Run("limit is respected newest-first", func(t *testing.T) {
		t.Parallel()

		src := "limit-source"
		now := time.Now().UTC()

		for i := 0; i < 5; i++ {
			h := &domain.ExecutionHistory{
				SourceName: src,
				Success:    true,
				Timestamp:  now.Add(time.Duration(i) * time.Minute),
			}
			require.NoError(t, r.RetainExecutionHistory(t.Context(), h))
		}

		result, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), src, 2, false)
		require.NoError(t, err)
		require.Len(t, result, 2)
		// newest-first: result[0].Timestamp >= result[1].Timestamp
		require.False(t, result[0].Timestamp.Before(result[1].Timestamp))
	})
}

func TestExecutionHistoryRepository_ObtainLatestExecutionHistoryBySources(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns empty map without querying", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		got, err := r.ObtainLatestExecutionHistoryBySources(t.Context(), nil)
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("returns newest row per source, missing sources absent from map", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		now := time.Now().UTC()
		for _, row := range []domain.ExecutionHistory{
			{SourceName: "bulk-a", Success: false, Error: "old", Timestamp: now.Add(-time.Hour)},
			{SourceName: "bulk-a", Success: true, Timestamp: now},
			{SourceName: "bulk-b", Success: true, Timestamp: now.Add(-30 * time.Minute)},
		} {
			require.NoError(t, r.RetainExecutionHistory(t.Context(), &row))
		}

		got, err := r.ObtainLatestExecutionHistoryBySources(t.Context(),
			[]string{"bulk-a", "bulk-b", "missing-source"})
		require.NoError(t, err)
		require.Len(t, got, 2, "missing-source has no rows so must be absent from the map")
		require.True(t, got["bulk-a"].Success, "must return the newest row for bulk-a")
		require.Equal(t, "bulk-b", got["bulk-b"].SourceName)
	})
	t.Run("single-name list works (placeholder edge case)", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		require.NoError(t, r.RetainExecutionHistory(t.Context(),
			&domain.ExecutionHistory{SourceName: "solo", Success: true, Timestamp: time.Now().UTC()}))

		got, err := r.ObtainLatestExecutionHistoryBySources(t.Context(), []string{"solo"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Contains(t, got, "solo")
	})
}

func TestExecutionHistoryRepository_RemoveSourceExecutionHistory(t *testing.T) {
	t.Parallel()

	r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
	require.NoError(t, err)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		removeErr := r.RemoveSourceExecutionHistory(t.Context(), nil)
		require.Error(t, removeErr)
		require.ErrorContains(t, removeErr, "nil")
	})

	h := &domain.ExecutionHistory{
		SourceName: "to-remove",
		Success:    true,
		Timestamp:  time.Now().UTC(),
	}
	require.NoError(t, r.RetainExecutionHistory(t.Context(), h))
	require.NotEmpty(t, h.ID)

	require.NoError(t, r.RemoveSourceExecutionHistory(t.Context(), h))

	tx, err := r.db.Transaction(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	var count int
	require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM"+" "+executionHistoryTableName+" WHERE "+executionHistoryIdFieldName+" = ?", h.ID).Scan(&count))
	require.Equal(t, 0, count)
}

func TestExecutionHistoryRepository_TransactionErrors(t *testing.T) {
	t.Parallel()

	newBrokenRepo := func(t *testing.T) *ExecutionHistoryRepository {
		t.Helper()
		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		r.db = &mockFailDB{err: errors.New("db unavailable")}
		return r
	}

	t.Run("CheckUP propagates transaction error", func(t *testing.T) {
		t.Parallel()
		require.Error(t, newBrokenRepo(t).CheckUP(t.Context()))
	})
	t.Run("ObtainLastNExecutionHistoryBySourceName propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 1, false)
		require.Error(t, err)
	})
	t.Run("ObtainLatestExecutionHistoryBySources propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainLatestExecutionHistoryBySources(t.Context(), []string{"src"})
		require.Error(t, err)
	})
	t.Run("RetainExecutionHistory propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RetainExecutionHistory(t.Context(), &domain.ExecutionHistory{SourceName: "src"})
		require.Error(t, err)
	})
	t.Run("RemoveSourceExecutionHistory propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RemoveSourceExecutionHistory(t.Context(), &domain.ExecutionHistory{ID: "x"})
		require.Error(t, err)
	})
}

func TestExecutionHistoryRepository_ObtainErrorCount(t *testing.T) {
	t.Parallel()

	t.Run("returns zero when no records", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		count, err := r.ObtainExecutionHistoryErrorCount(t.Context())
		require.NoError(t, err)
		require.Equal(t, int64(0), count)
	})
	t.Run("counts only failed records", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		src := "count-errs-source"
		now := time.Now().UTC()
		for i, ok := range []bool{true, false, false, true, false} {
			h := &domain.ExecutionHistory{
				SourceName: src,
				Success:    ok,
				Timestamp:  now.Add(time.Duration(i) * time.Second),
			}
			require.NoError(t, r.RetainExecutionHistory(t.Context(), h))
		}

		count, err := r.ObtainExecutionHistoryErrorCount(t.Context())
		require.NoError(t, err)
		require.GreaterOrEqual(t, count, int64(3))
	})
}

func TestExecutionHistoryRepository_ObtainErrors(t *testing.T) {
	t.Parallel()

	t.Run("returns empty slice when no failures", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		items, err := r.ObtainLastNExecutionHistoryErrors(t.Context(), 0, 50)
		require.NoError(t, err)
		require.NotNil(t, items)
		require.Empty(t, items)
	})
	t.Run("returns only failed records newest-first", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		src := "err-order-source"
		now := time.Now().UTC()
		records := []domain.ExecutionHistory{
			{SourceName: src, Success: true, Timestamp: now.Add(-3 * time.Minute)},
			{SourceName: src, Success: false, Error: "err-a", Timestamp: now.Add(-2 * time.Minute)},
			{SourceName: src, Success: false, Error: "err-b", Timestamp: now.Add(-time.Minute)},
			{SourceName: src, Success: true, Timestamp: now},
		}
		for _, rec := range records {
			require.NoError(t, r.RetainExecutionHistory(t.Context(), &rec))
		}

		items, err := r.ObtainLastNExecutionHistoryErrors(t.Context(), 0, 50)
		require.NoError(t, err)
		require.Len(t, items, 2) // exactly 2 failures in this isolated DB
		for _, item := range items {
			require.False(t, item.Success)
		}
		// newest-first ordering
		require.False(t, items[0].Timestamp.Before(items[len(items)-1].Timestamp))
	})
	t.Run("pagination with offset respects limit", func(t *testing.T) {
		t.Parallel()

		r, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		src := "err-page-source"
		now := time.Now().UTC()
		for i := 0; i < 5; i++ {
			h := &domain.ExecutionHistory{
				SourceName: src,
				Success:    false,
				Error:      "oops",
				Timestamp:  now.Add(time.Duration(i) * time.Second),
			}
			require.NoError(t, r.RetainExecutionHistory(t.Context(), h))
		}

		page1, err := r.ObtainLastNExecutionHistoryErrors(t.Context(), 0, 2)
		require.NoError(t, err)
		require.Len(t, page1, 2)

		page2, err := r.ObtainLastNExecutionHistoryErrors(t.Context(), 2, 2)
		require.NoError(t, err)
		require.Len(t, page2, 2)

		require.NotEqual(t, page1[0].ID, page2[0].ID)
	})
}

func BenchmarkExecutionHistoryRepository_ObtainLastN(b *testing.B) {
	r, err := NewExecutionHistoryRepository(stubSQLiteDB(b))
	if err != nil {
		b.Fatal(err)
	}

	ctx := b.Context()
	src := "bench-source"
	now := time.Now().UTC()

	for i := 0; i < 200; i++ {
		h := &domain.ExecutionHistory{
			SourceName: src,
			Success:    i%2 == 0,
			Timestamp:  now.Add(time.Duration(i) * time.Second),
		}
		if err := r.RetainExecutionHistory(ctx, h); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for b.Loop() {
		_, _ = r.ObtainLastNExecutionHistoryBySourceName(ctx, src, 10, true)
	}
}
