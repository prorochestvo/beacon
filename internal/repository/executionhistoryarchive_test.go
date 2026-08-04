package repository

import (
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveHistory builds an outcome with an explicit id and timestamp, since the archive
// is keyed on the id the hot tier assigned rather than minting its own.
func archiveHistory(id, source string, ts time.Time, success bool, message string) domain.ExecutionHistory {
	return domain.ExecutionHistory{
		ID:         id,
		SourceName: source,
		Success:    success,
		Error:      message,
		Timestamp:  ts,
	}
}

func TestExecutionHistoryArchiveRepository_RetainExecutionHistories(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("inserts new rows and reports the count", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		inserted, err := repo.RetainExecutionHistories(t.Context(), []domain.ExecutionHistory{
			archiveHistory("eh1", "src-a", base, true, ""),
			archiveHistory("eh2", "src-a", base.Add(time.Minute), false, "boom"),
		})
		require.NoError(t, err)
		assert.Equal(t, 2, inserted)

		count, err := repo.CountExecutionHistory(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})

	t.Run("re-copying a window already held inserts nothing", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		batch := []domain.ExecutionHistory{
			archiveHistory("eh1", "src-a", base, true, ""),
			archiveHistory("eh2", "src-a", base.Add(time.Minute), false, "boom"),
		}
		_, err = repo.RetainExecutionHistories(t.Context(), batch)
		require.NoError(t, err)

		// Idempotence is what lets a failed pass simply run again.
		inserted, err := repo.RetainExecutionHistories(t.Context(), batch)
		require.NoError(t, err)
		assert.Zero(t, inserted)

		count, err := repo.CountExecutionHistory(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})

	t.Run("an existing row is never rewritten", func(t *testing.T) {
		t.Parallel()
		db := stubArchiveDB(t)
		repo, err := NewExecutionHistoryArchiveRepository(db)
		require.NoError(t, err)

		_, err = repo.RetainExecutionHistories(t.Context(), []domain.ExecutionHistory{
			archiveHistory("eh1", "src-a", base, false, "original failure"),
		})
		require.NoError(t, err)
		_, err = repo.RetainExecutionHistories(t.Context(), []domain.ExecutionHistory{
			archiveHistory("eh1", "src-a", base, true, "rewritten"),
		})
		require.NoError(t, err)

		reader, err := NewExecutionHistoryRepository(db)
		require.NoError(t, err)
		rows, err := reader.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src-a", 10, false)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "original failure", rows[0].Error, "the archive records history, it does not revise it")
	})

	t.Run("a record without an id is rejected", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		_, err = repo.RetainExecutionHistories(t.Context(), []domain.ExecutionHistory{
			archiveHistory("", "src-a", base, true, ""),
		})
		require.Error(t, err)

		count, err := repo.CountExecutionHistory(t.Context())
		require.NoError(t, err)
		assert.Zero(t, count, "the batch is one transaction, so nothing lands")
	})

	t.Run("an empty batch is a no-op", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		inserted, err := repo.RetainExecutionHistories(t.Context(), nil)
		require.NoError(t, err)
		assert.Zero(t, inserted)
	})
}

func TestExecutionHistoryArchiveRepository_ObtainArchiveWatermark(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("an empty archive reports no watermark", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		_, _, ok, err := repo.ObtainArchiveWatermark(t.Context())
		require.NoError(t, err)
		assert.False(t, ok, "no watermark means the pass starts from the beginning of the hot tier")
	})

	t.Run("the watermark is the newest (timestamp, id) pair", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		// One tick writes a row per source at the same second; the id is what makes the
		// resume point unambiguous within it.
		_, err = repo.RetainExecutionHistories(t.Context(), []domain.ExecutionHistory{
			archiveHistory("eh1", "src-a", base, true, ""),
			archiveHistory("eh3", "src-c", base, true, ""),
			archiveHistory("eh2", "src-b", base, true, ""),
			archiveHistory("eh0", "src-d", base.Add(-time.Hour), true, ""),
		})
		require.NoError(t, err)

		ts, id, ok, err := repo.ObtainArchiveWatermark(t.Context())
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, base, ts)
		assert.Equal(t, "eh3", id)
	})

	t.Run("the timestamp round-trips through Unix seconds", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		_, err = repo.RetainExecutionHistories(t.Context(), []domain.ExecutionHistory{
			archiveHistory("eh1", "src-a", base, true, ""),
		})
		require.NoError(t, err)

		ts, _, _, err := repo.ObtainArchiveWatermark(t.Context())
		require.NoError(t, err)
		assert.True(t, ts.Equal(base), "the hot tier stores INT seconds; the archive must match or the cursor drifts")
		assert.Equal(t, time.UTC, ts.Location())
	})

	t.Run("CheckUP reads the archive table", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)
		require.NoError(t, repo.CheckUP(t.Context()))
		assert.Equal(t, "execution_history_archive", repo.Name())
	})
}

// TestArchiveExecutionHistorySchemaServesTheHotRepository pins the property the tiering
// rests on for this table: the archive's columns match the hot schema, so the ordinary
// repository reads it with no second implementation of the queries.
func TestArchiveExecutionHistorySchemaServesTheHotRepository(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	seed := func(t *testing.T) *ExecutionHistoryRepository {
		t.Helper()
		db := stubArchiveDB(t)
		writer, err := NewExecutionHistoryArchiveRepository(db)
		require.NoError(t, err)
		_, err = writer.RetainExecutionHistories(t.Context(), []domain.ExecutionHistory{
			archiveHistory("eh1", "src-a", base, true, ""),
			archiveHistory("eh2", "src-a", base.Add(time.Minute), false, "boom"),
			archiveHistory("eh3", "src-b", base.Add(2*time.Minute), false, "kaboom"),
		})
		require.NoError(t, err)

		reader, err := NewExecutionHistoryRepository(db)
		require.NoError(t, err)
		return reader
	}

	t.Run("the unbounded error count reads the archive", func(t *testing.T) {
		t.Parallel()
		count, err := seed(t).ObtainExecutionHistoryErrorCount(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})

	t.Run("paged failures read the archive", func(t *testing.T) {
		t.Parallel()
		reader := seed(t)

		page, err := reader.ObtainLastNExecutionHistoryErrors(t.Context(), 0, 10)
		require.NoError(t, err)
		require.Len(t, page, 2)
		assert.Equal(t, "eh3", page[0].ID, "newest first")

		// Offset pagination has to be stable across pages, which is what the id
		// tiebreaker on the ORDER BY buys.
		second, err := reader.ObtainLastNExecutionHistoryErrors(t.Context(), 1, 10)
		require.NoError(t, err)
		require.Len(t, second, 1)
		assert.Equal(t, "eh2", second[0].ID)
	})

	t.Run("the cursored last-N query reads the archive", func(t *testing.T) {
		t.Parallel()
		reader := seed(t)

		first, err := reader.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src-a", 1, false)
		require.NoError(t, err)
		require.Len(t, first, 1)

		next, err := reader.ObtainLastNExecutionHistoryBySourceNameBefore(
			t.Context(), "src-a", first[0].Timestamp, first[0].ID, 10, false,
		)
		require.NoError(t, err)
		require.Len(t, next, 1)
		assert.NotEqual(t, first[0].ID, next[0].ID)
	})

	t.Run("the reconciliation walk reads the archive", func(t *testing.T) {
		t.Parallel()
		// The archive is also walked forward when a future backfill has to resume
		// through it, so the same keyset query must work on this schema.
		rows, err := seed(t).ObtainExecutionHistoryAfter(t.Context(), time.Time{}, "", 10)
		require.NoError(t, err)
		require.Len(t, rows, 3)
		assert.Equal(t, "eh1", rows[0].ID, "oldest first")
	})
}
