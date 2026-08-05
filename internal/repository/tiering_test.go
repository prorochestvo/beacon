package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedRateValueAt inserts a rate value and rewrites its timestamp, since RetainRateValue
// always stamps now.
func seedRateValueAt(t *testing.T, r *RateValueRepository, source string, ts time.Time, price float64) domain.RateValue {
	t.Helper()
	rv := domain.RateValue{SourceName: source, BaseCurrency: "USD", QuoteCurrency: "KZT", Price: price}
	require.NoError(t, r.RetainRateValue(t.Context(), &rv))

	tx, err := r.db.Transaction(t.Context())
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(),
		"UPDATE "+rateValueTableName+" SET "+rateValueTimestampFieldName+" = ? WHERE "+rateValueIdFieldName+" = ?",
		ts.UTC().Format(time.RFC3339), rv.ID,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	rv.Timestamp = ts.UTC().Truncate(time.Second)
	return rv
}

// countIn reports how many rows one physical table holds, bypassing the union so a test
// can assert where a row actually lives rather than only that it is readable.
func countIn(t *testing.T, db interface {
	ReadOnlyTransaction(context.Context) (*sql.Tx, error)
}, table string) int64 {
	t.Helper()
	tx, err := db.ReadOnlyTransaction(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	var n int64
	require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

func TestRateValueRepository_RolloverToArchive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	t.Run("rows older than the cutoff move, newer ones stay", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-roll")
		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)

		seedRateValueAt(t, repo, "src-roll", now.Add(-200*24*time.Hour), 1)
		seedRateValueAt(t, repo, "src-roll", now.Add(-190*24*time.Hour), 2)
		seedRateValueAt(t, repo, "src-roll", now.Add(-10*24*time.Hour), 3)

		moved, err := repo.RolloverToArchive(t.Context(), now.Add(-180*24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(2), moved)
		assert.Equal(t, int64(1), countIn(t, db, rateValueTableName))
		assert.Equal(t, int64(2), countIn(t, db, rateValueArchiveTableName))
	})

	t.Run("the reads see exactly the same rows before and after", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-same")
		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)

		for i := 0; i < 12; i++ {
			seedRateValueAt(t, repo, "src-same", now.Add(-time.Duration(i*30)*24*time.Hour), float64(i))
		}

		// This is the property the whole design turns on: moving a row between tiers is
		// invisible above the repository. Anything else here is an outage.
		before, err := repo.ObtainLastNRateValuesBySourceName(t.Context(), "src-same", 100)
		require.NoError(t, err)
		require.Len(t, before, 12)

		pairs := []domain.SourcePairKey{{SourceName: "src-same", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		chartBefore, err := repo.ObtainValuesForPairsSince(t.Context(), pairs, now.Add(-400*24*time.Hour))
		require.NoError(t, err)
		latestBefore, err := repo.ObtainLatestRateValuesBySourceNames(t.Context(), []string{"src-same"})
		require.NoError(t, err)
		pageBefore, rowsBefore, groupedBefore, err := repo.ObtainHistoryForPairsPaged(t.Context(), pairs, 5, 0)
		require.NoError(t, err)

		moved, err := repo.RolloverToArchive(t.Context(), now.Add(-180*24*time.Hour))
		require.NoError(t, err)
		require.Positive(t, moved, "the fixture must actually straddle the cutoff")
		require.Positive(t, countIn(t, db, rateValueTableName), "and must leave something hot")

		after, err := repo.ObtainLastNRateValuesBySourceName(t.Context(), "src-same", 100)
		require.NoError(t, err)
		assert.Equal(t, before, after)

		chartAfter, err := repo.ObtainValuesForPairsSince(t.Context(), pairs, now.Add(-400*24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, chartBefore, chartAfter)

		latestAfter, err := repo.ObtainLatestRateValuesBySourceNames(t.Context(), []string{"src-same"})
		require.NoError(t, err)
		assert.Equal(t, latestBefore, latestAfter)

		pageAfter, rowsAfter, groupedAfter, err := repo.ObtainHistoryForPairsPaged(t.Context(), pairs, 5, 0)
		require.NoError(t, err)
		assert.Equal(t, pageBefore, pageAfter)
		assert.Equal(t, rowsBefore, rowsAfter)
		assert.Equal(t, groupedBefore, groupedAfter, "the grouped count still joins the live rate_sources")
	})

	t.Run("a second pass moves nothing", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-twice")
		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)
		seedRateValueAt(t, repo, "src-twice", now.Add(-200*24*time.Hour), 1)

		cutoff := now.Add(-180 * 24 * time.Hour)
		first, err := repo.RolloverToArchive(t.Context(), cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), first)

		second, err := repo.RolloverToArchive(t.Context(), cutoff)
		require.NoError(t, err)
		assert.Zero(t, second, "the move is atomic, so a repeat finds nothing left to move")
		assert.Equal(t, int64(1), countIn(t, db, rateValueArchiveTableName), "and cannot duplicate")
	})

	t.Run("a zero cutoff archives nothing", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-zero")
		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)
		seedRateValueAt(t, repo, "src-zero", now.Add(-500*24*time.Hour), 1)

		// The guard exists so a caller that computed its window from a zero clock or a
		// missing setting cannot archive the entire hot tier.
		moved, err := repo.RolloverToArchive(t.Context(), time.Time{})
		require.NoError(t, err)
		assert.Zero(t, moved)
		assert.Equal(t, int64(1), countIn(t, db, rateValueTableName))
		assert.Zero(t, countIn(t, db, rateValueArchiveTableName))
	})

	t.Run("archived rows survive deleting their source", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-gone")
		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)
		seedRateValueAt(t, repo, "src-gone", now.Add(-200*24*time.Hour), 1)
		seedRateValueAt(t, repo, "src-gone", now.Add(-1*24*time.Hour), 2)

		_, err = repo.RolloverToArchive(t.Context(), now.Add(-180*24*time.Hour))
		require.NoError(t, err)

		// rate_values cascades on source deletion; the archive deliberately carries no
		// foreign key, so a removed source takes the hot row and leaves the history.
		sources, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		require.NoError(t, sources.RemoveRateSource(t.Context(), &domain.RateSource{Name: "src-gone"}))

		assert.Zero(t, countIn(t, db, rateValueTableName), "the hot row cascaded away")
		assert.Equal(t, int64(1), countIn(t, db, rateValueArchiveTableName), "the archived row did not")
	})
}

func TestRateValueRepository_PruneArchive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	setup := func(t *testing.T) (*RateValueRepository, interface {
		ReadOnlyTransaction(context.Context) (*sql.Tx, error)
	}) {
		t.Helper()
		db := stubSQLiteDB(t, "src-prune")
		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)
		seedRateValueAt(t, repo, "src-prune", now.Add(-800*24*time.Hour), 1)
		seedRateValueAt(t, repo, "src-prune", now.Add(-300*24*time.Hour), 2)
		_, err = repo.RolloverToArchive(t.Context(), now.Add(-180*24*time.Hour))
		require.NoError(t, err)
		return repo, db
	}

	t.Run("a zero cutoff keeps everything", func(t *testing.T) {
		t.Parallel()
		repo, db := setup(t)

		// This is the configured behaviour: the archive exists so history is never lost.
		pruned, err := repo.PruneArchive(t.Context(), time.Time{})
		require.NoError(t, err)
		assert.Zero(t, pruned)
		assert.Equal(t, int64(2), countIn(t, db, rateValueArchiveTableName))
	})

	t.Run("a cutoff deletes only what predates it", func(t *testing.T) {
		t.Parallel()
		repo, db := setup(t)

		pruned, err := repo.PruneArchive(t.Context(), now.Add(-500*24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), pruned)
		assert.Equal(t, int64(1), countIn(t, db, rateValueArchiveTableName))
	})

	t.Run("pruning never touches the hot tier", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-hot")
		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)
		seedRateValueAt(t, repo, "src-hot", now.Add(-1*time.Hour), 1)

		// A retention window shorter than the hot window must not reach through and delete
		// live data — retention applies to the archive alone, the hot tier is bounded by
		// the roll-over.
		pruned, err := repo.PruneArchive(t.Context(), now)
		require.NoError(t, err)
		assert.Zero(t, pruned)
		assert.Equal(t, int64(1), countIn(t, db, rateValueTableName))
	})
}

func TestExecutionHistoryRepository_Tiering(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	seed := func(t *testing.T, repo *ExecutionHistoryRepository, ts time.Time, success bool) {
		t.Helper()
		rec := domain.ExecutionHistory{SourceName: "src-eh", Success: success, Timestamp: ts}
		if !success {
			rec.Error = "boom"
		}
		require.NoError(t, repo.RetainExecutionHistory(t.Context(), &rec))
	}

	t.Run("the unbounded error count still spans both tiers", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-eh")
		repo, err := NewExecutionHistoryRepository(db)
		require.NoError(t, err)
		seed(t, repo, now.Add(-400*24*time.Hour), false)
		seed(t, repo, now.Add(-200*24*time.Hour), false)
		seed(t, repo, now.Add(-1*time.Hour), false)

		before, err := repo.ObtainExecutionHistoryErrorCount(t.Context())
		require.NoError(t, err)
		require.Equal(t, int64(3), before)

		moved, err := repo.RolloverToArchive(t.Context(), now.Add(-180*24*time.Hour))
		require.NoError(t, err)
		require.Equal(t, int64(2), moved)

		// The whole point: a count of every failure since the beginning must not shrink
		// when the hot tier is bounded, or a quieter number reads as things improving.
		after, err := repo.ObtainExecutionHistoryErrorCount(t.Context())
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})

	t.Run("paged failures and per-source reads span both tiers", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-eh")
		repo, err := NewExecutionHistoryRepository(db)
		require.NoError(t, err)
		for i := 0; i < 10; i++ {
			seed(t, repo, now.Add(-time.Duration(i*40)*24*time.Hour), i%2 == 0)
		}

		pageBefore, err := repo.ObtainLastNExecutionHistoryErrors(t.Context(), 0, 50)
		require.NoError(t, err)
		lastNBefore, err := repo.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src-eh", 50, false)
		require.NoError(t, err)
		latestBefore, err := repo.ObtainLatestExecutionHistoryBySources(t.Context(), []string{"src-eh"})
		require.NoError(t, err)

		moved, err := repo.RolloverToArchive(t.Context(), now.Add(-180*24*time.Hour))
		require.NoError(t, err)
		require.Positive(t, moved)
		require.Positive(t, countIn(t, db, executionHistoryTableName))

		pageAfter, err := repo.ObtainLastNExecutionHistoryErrors(t.Context(), 0, 50)
		require.NoError(t, err)
		assert.Equal(t, pageBefore, pageAfter)

		lastNAfter, err := repo.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src-eh", 50, false)
		require.NoError(t, err)
		assert.Equal(t, lastNBefore, lastNAfter)

		latestAfter, err := repo.ObtainLatestExecutionHistoryBySources(t.Context(), []string{"src-eh"})
		require.NoError(t, err)
		assert.Equal(t, latestBefore, latestAfter)
	})

	t.Run("the Unix-seconds cutoff selects the same rows the reads report", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t, "src-eh")
		repo, err := NewExecutionHistoryRepository(db)
		require.NoError(t, err)
		cutoff := now.Add(-180 * 24 * time.Hour)
		seed(t, repo, cutoff.Add(-time.Second), true)
		seed(t, repo, cutoff, true)
		seed(t, repo, cutoff.Add(time.Second), true)

		// execution_history stores INT seconds while rate_values stores RFC3339 text; the
		// cutoff has to be converted the same way the column is written or the boundary
		// lands somewhere else entirely.
		moved, err := repo.RolloverToArchive(t.Context(), cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(1), moved, "strictly older than the cutoff, so the cutoff row stays hot")
		assert.Equal(t, int64(2), countIn(t, db, executionHistoryTableName))
	})
}

// TestTieredReadsUseIndexesOnBothBranches pins the plan, not just the result.
//
// A union that stopped riding the archive's index would still return correct rows, so no
// behavioural test would catch it — it would surface years later as the dashboard slowing
// down in proportion to accumulated history, which is the exact failure this whole design
// exists to prevent.
func TestTieredReadsUseIndexesOnBothBranches(t *testing.T) {
	t.Parallel()

	db := stubSQLiteDB(t, "src-plan")

	explain := func(t *testing.T, query string, args ...any) string {
		t.Helper()
		tx, err := db.ReadOnlyTransaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		rows, err := tx.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()

		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var detail string
			require.NoError(t, rows.Scan(&a, &b, &c, &detail))
			plan.WriteString(detail)
			plan.WriteString("\n")
		}
		require.NoError(t, rows.Err())
		return plan.String()
	}

	t.Run("the per-source read searches both tiers by index", func(t *testing.T) {
		plan := explain(t,
			rateValueSqlSelect+" WHERE "+rateValueSourceNameFieldName+" = ?"+
				" ORDER BY "+rateValueTimestampFieldName+" DESC LIMIT ?;",
			"src-plan", 10,
		)
		assert.Contains(t, plan, "SEARCH "+rateValueTableName+" USING INDEX")
		assert.Contains(t, plan, "SEARCH "+rateValueArchiveTableName+" USING INDEX")
		assert.NotContains(t, plan, "SCAN "+rateValueArchiveTableName)
	})

	t.Run("the failed-run read searches both tiers by index", func(t *testing.T) {
		plan := explain(t,
			executionHistorySqlSelect+" WHERE "+executionHistorySourceNameFieldName+" = ?"+
				" AND "+executionHistorySuccessFieldName+" = 0 LIMIT ?;",
			"src-plan", 10,
		)
		assert.Contains(t, plan, "SEARCH "+executionHistoryTableName+" USING INDEX")
		assert.Contains(t, plan, "SEARCH "+executionHistoryArchiveTableName+" USING INDEX")
		assert.NotContains(t, plan, "SCAN "+executionHistoryArchiveTableName)
	})
}
