package repository

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
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
		require.True(t, !result[0].Timestamp.Before(result[1].Timestamp))
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

		err := r.RemoveSourceExecutionHistory(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
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
	defer func() { _ = tx.Rollback() }()

	var count int
	require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM"+" "+executionHistoryTableName+" WHERE "+executionHistoryIdFieldName+" = ?", h.ID).Scan(&count))
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
		require.True(t, !items[0].Timestamp.Before(items[len(items)-1].Timestamp))
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

func TestExecutionHistoryRepository_ObtainExecutionHistoryAfter(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	seed := func(t *testing.T, rows ...domain.ExecutionHistory) *ExecutionHistoryRepository {
		t.Helper()
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r.SourceName)
		}
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, names...))
		require.NoError(t, err)
		for i := range rows {
			rec := rows[i]
			require.NoError(t, repo.RetainExecutionHistory(t.Context(), &rec))
		}
		return repo
	}

	t.Run("a zero cursor walks from the beginning, oldest first", func(t *testing.T) {
		t.Parallel()
		repo := seed(t,
			domain.ExecutionHistory{ID: "eh-b", SourceName: "src-walk", Success: true, Timestamp: base.Add(time.Minute)},
			domain.ExecutionHistory{ID: "eh-a", SourceName: "src-walk", Success: true, Timestamp: base},
			domain.ExecutionHistory{ID: "eh-c", SourceName: "src-walk", Success: false, Timestamp: base.Add(2 * time.Minute)},
		)

		got, err := repo.ObtainExecutionHistoryAfter(t.Context(), time.Time{}, "", 10)
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, []string{"eh-a", "eh-b", "eh-c"}, []string{got[0].ID, got[1].ID, got[2].ID})
	})

	t.Run("the cursor advances through rows sharing one tick", func(t *testing.T) {
		t.Parallel()
		// The whole reason the cursor is a pair: a tick writes one row per source at the
		// same second, and a timestamp-only cursor would loop on it or skip the rest.
		repo := seed(t,
			domain.ExecutionHistory{ID: "eh-a", SourceName: "src-tick", Success: true, Timestamp: base},
			domain.ExecutionHistory{ID: "eh-b", SourceName: "src-tick", Success: true, Timestamp: base},
			domain.ExecutionHistory{ID: "eh-c", SourceName: "src-tick", Success: true, Timestamp: base},
		)

		first, err := repo.ObtainExecutionHistoryAfter(t.Context(), time.Time{}, "", 2)
		require.NoError(t, err)
		require.Len(t, first, 2)

		last := first[len(first)-1]
		second, err := repo.ObtainExecutionHistoryAfter(t.Context(), last.Timestamp, last.ID, 2)
		require.NoError(t, err)
		require.Len(t, second, 1)
		assert.Equal(t, "eh-c", second[0].ID)

		final := second[len(second)-1]
		empty, err := repo.ObtainExecutionHistoryAfter(t.Context(), final.Timestamp, final.ID, 2)
		require.NoError(t, err)
		assert.Empty(t, empty, "the walk terminates rather than repeating the tick")
	})

	t.Run("the timestamp survives the Unix-seconds round trip", func(t *testing.T) {
		t.Parallel()
		repo := seed(t, domain.ExecutionHistory{
			ID: "eh-a", SourceName: "src-ts", Success: true, Timestamp: base,
		})

		got, err := repo.ObtainExecutionHistoryAfter(t.Context(), time.Time{}, "", 10)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.True(t, got[0].Timestamp.Equal(base))
	})
}

func TestExecutionHistoryRepository_ObtainLastNExecutionHistoryBySourceNameBefore(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	seed := func(t *testing.T, n int) *ExecutionHistoryRepository {
		t.Helper()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, "src-cursor"))
		require.NoError(t, err)
		for i := 0; i < n; i++ {
			rec := domain.ExecutionHistory{
				ID:         fmt.Sprintf("eh-%03d", i),
				SourceName: "src-cursor",
				Success:    i%3 != 0,
				Timestamp:  base.Add(time.Duration(i) * time.Minute),
			}
			require.NoError(t, repo.RetainExecutionHistory(t.Context(), &rec))
		}
		return repo
	}

	t.Run("a zero cursor matches the uncursored query", func(t *testing.T) {
		t.Parallel()
		repo := seed(t, 6)

		plain, err := repo.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src-cursor", 4, false)
		require.NoError(t, err)
		cursored, err := repo.ObtainLastNExecutionHistoryBySourceNameBefore(t.Context(), "src-cursor", time.Time{}, "", 4, false)
		require.NoError(t, err)
		assert.Equal(t, plain, cursored)
	})

	t.Run("the two pages partition the history exactly once", func(t *testing.T) {
		t.Parallel()
		repo := seed(t, 6)

		first, err := repo.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src-cursor", 3, false)
		require.NoError(t, err)
		require.Len(t, first, 3)

		last := first[len(first)-1]
		next, err := repo.ObtainLastNExecutionHistoryBySourceNameBefore(t.Context(), "src-cursor", last.Timestamp, last.ID, 3, false)
		require.NoError(t, err)
		require.Len(t, next, 3)

		seen := map[string]int{}
		for _, v := range append(append([]domain.ExecutionHistory{}, first...), next...) {
			seen[v.ID]++
		}
		assert.Len(t, seen, 6)
		for id, n := range seen {
			assert.Equal(t, 1, n, "row %s appeared on both pages", id)
		}
	})

	t.Run("successOnly filters both the page and its continuation", func(t *testing.T) {
		t.Parallel()
		repo := seed(t, 9)

		first, err := repo.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src-cursor", 2, true)
		require.NoError(t, err)
		require.Len(t, first, 2)
		for _, v := range first {
			assert.True(t, v.Success)
		}

		last := first[len(first)-1]
		next, err := repo.ObtainLastNExecutionHistoryBySourceNameBefore(t.Context(), "src-cursor", last.Timestamp, last.ID, 10, true)
		require.NoError(t, err)
		require.NotEmpty(t, next)
		for _, v := range next {
			assert.True(t, v.Success, "the continuation must walk the same filtered sequence")
			assert.Less(t, v.Timestamp.Unix(), last.Timestamp.Unix())
		}
	})

	t.Run("a non-positive limit never reaches the database", func(t *testing.T) {
		t.Parallel()
		repo := seed(t, 3)

		got, err := repo.ObtainLastNExecutionHistoryBySourceNameBefore(t.Context(), "src-cursor", time.Time{}, "", 0, false)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Empty(t, got)
	})
}
