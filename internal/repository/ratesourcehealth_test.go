package repository

import (
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateSourceHealthRepository(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	t.Run("an empty table reports nothing latched", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateSourceHealthRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		got, err := repo.ObtainAlertedSources(t.Context())
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a latch round-trips", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateSourceHealthRepository(stubSQLiteDB(t, "src-a"))
		require.NoError(t, err)

		require.NoError(t, repo.RetainAlertedSource(t.Context(), "src-a", at))
		got, err := repo.ObtainAlertedSources(t.Context())
		require.NoError(t, err)
		require.Contains(t, got, "src-a")
		assert.True(t, got["src-a"].Equal(at))
	})

	t.Run("re-latching overwrites rather than failing", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-a")
		repo, err := NewRateSourceHealthRepository(db)
		require.NoError(t, err)

		// A caller that lost track of the state must not be able to deadlock on a
		// primary-key collision.
		require.NoError(t, repo.RetainAlertedSource(t.Context(), "src-a", at))
		require.NoError(t, repo.RetainAlertedSource(t.Context(), "src-a", at.Add(time.Hour)))

		got, err := repo.ObtainAlertedSources(t.Context())
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.True(t, got["src-a"].Equal(at.Add(time.Hour)))
	})

	t.Run("clearing a latch that is not there is not an error", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateSourceHealthRepository(stubSQLiteDB(t, "src-a"))
		require.NoError(t, err)

		// Recovery clears unconditionally; a source that was never alerted about has
		// nothing to recover from and must not produce an error for it.
		require.NoError(t, repo.RemoveAlertedSource(t.Context(), "src-a"))
	})

	t.Run("an empty source name is rejected", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateSourceHealthRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		require.Error(t, repo.RetainAlertedSource(t.Context(), "", at))
	})

	t.Run("deleting the source takes its latch with it", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-a")
		repo, err := NewRateSourceHealthRepository(db)
		require.NoError(t, err)
		require.NoError(t, repo.RetainAlertedSource(t.Context(), "src-a", at))

		// A latch outliving its source could only ever produce a recovery notice for a
		// source nobody has.
		sources, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		require.NoError(t, sources.RemoveRateSource(t.Context(), &domain.RateSource{Name: "src-a"}))

		got, err := repo.ObtainAlertedSources(t.Context())
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("CheckUP reads the table", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateSourceHealthRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		require.NoError(t, repo.CheckUP(t.Context()))
		assert.Equal(t, "rate_source_health", repo.Name())
	})
}

func TestExecutionHistoryRepository_ObtainSourceCollectionHealth(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	seed := func(t *testing.T, repo *ExecutionHistoryRepository, source string, ts time.Time, success bool, message string) {
		t.Helper()
		rec := domain.ExecutionHistory{SourceName: source, Success: success, Error: message, Timestamp: ts}
		require.NoError(t, repo.RetainExecutionHistory(t.Context(), &rec))
	}

	byName := func(rows []domain.SourceCollectionHealth) map[string]domain.SourceCollectionHealth {
		out := make(map[string]domain.SourceCollectionHealth, len(rows))
		for _, r := range rows {
			out[r.SourceName] = r
		}
		return out
	}

	t.Run("a healthy source reports its success and no failures", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, "src-a"))
		require.NoError(t, err)
		seed(t, repo, "src-a", base.Add(-2*time.Hour), true, "")
		seed(t, repo, "src-a", base.Add(-time.Hour), true, "")

		got, err := repo.ObtainSourceCollectionHealth(t.Context())
		require.NoError(t, err)
		h := byName(got)["src-a"]
		assert.True(t, h.LastSuccessAt.Equal(base.Add(-time.Hour)))
		assert.True(t, h.LastRunAt.Equal(base.Add(-time.Hour)))
		assert.Zero(t, h.ConsecutiveFailures)
		assert.Empty(t, h.LastError)
	})

	t.Run("failures after the last success are counted, earlier ones are not", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, "src-a"))
		require.NoError(t, err)
		// An old outage that already recovered must not inflate the current count.
		seed(t, repo, "src-a", base.Add(-9*time.Hour), false, "ancient history")
		seed(t, repo, "src-a", base.Add(-8*time.Hour), false, "ancient history")
		seed(t, repo, "src-a", base.Add(-5*time.Hour), true, "")
		seed(t, repo, "src-a", base.Add(-2*time.Hour), false, "older failure")
		seed(t, repo, "src-a", base.Add(-time.Hour), false, "newest failure")

		got, err := repo.ObtainSourceCollectionHealth(t.Context())
		require.NoError(t, err)
		h := byName(got)["src-a"]
		assert.True(t, h.LastSuccessAt.Equal(base.Add(-5*time.Hour)))
		assert.True(t, h.LastRunAt.Equal(base.Add(-time.Hour)))
		assert.Equal(t, int64(2), h.ConsecutiveFailures, "only the failures since the last success")
		assert.Equal(t, "newest failure", h.LastError)
	})

	t.Run("a source that never succeeded counts every failure", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, "src-b"))
		require.NoError(t, err)
		seed(t, repo, "src-b", base.Add(-3*time.Hour), false, "first")
		seed(t, repo, "src-b", base.Add(-time.Hour), false, "latest")

		got, err := repo.ObtainSourceCollectionHealth(t.Context())
		require.NoError(t, err)
		h := byName(got)["src-b"]
		assert.True(t, h.LastSuccessAt.IsZero(), "never succeeded is zero, not the epoch")
		assert.True(t, h.LastRunAt.Equal(base.Add(-time.Hour)))
		assert.Equal(t, int64(2), h.ConsecutiveFailures)
		assert.Equal(t, "latest", h.LastError)
	})

	t.Run("a source with no history is absent, not zero-valued", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, "src-a", "src-quiet"))
		require.NoError(t, err)
		seed(t, repo, "src-a", base, true, "")

		// "Never attempted" and "attempted and broken" want different answers, so the
		// caller has to be able to tell them apart.
		got, err := repo.ObtainSourceCollectionHealth(t.Context())
		require.NoError(t, err)
		assert.NotContains(t, byName(got), "src-quiet")
		assert.Contains(t, byName(got), "src-a")
	})

	t.Run("sources are reported independently", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, "src-a", "src-b"))
		require.NoError(t, err)
		seed(t, repo, "src-a", base.Add(-time.Hour), true, "")
		seed(t, repo, "src-b", base.Add(-time.Hour), false, "b is broken")

		got, err := repo.ObtainSourceCollectionHealth(t.Context())
		require.NoError(t, err)
		m := byName(got)
		assert.Zero(t, m["src-a"].ConsecutiveFailures)
		assert.Equal(t, int64(1), m["src-b"].ConsecutiveFailures)
		assert.Equal(t, "b is broken", m["src-b"].LastError)
	})

	t.Run("failures within one tick are all counted", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t, "src-a"))
		require.NoError(t, err)
		// One collector tick writes several rows at the same second; a comparison that
		// only looked at the newest timestamp would count one of them.
		for i := 0; i < 3; i++ {
			seed(t, repo, "src-a", base.Add(-time.Hour), false, "same second")
		}

		got, err := repo.ObtainSourceCollectionHealth(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(3), byName(got)["src-a"].ConsecutiveFailures)
	})

	t.Run("an empty table returns an empty result", func(t *testing.T) {
		t.Parallel()
		repo, err := NewExecutionHistoryRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		got, err := repo.ObtainSourceCollectionHealth(t.Context())
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Empty(t, got)
	})
}
