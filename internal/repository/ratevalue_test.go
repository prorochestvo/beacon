package repository

import (
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestNewRateRepository(t *testing.T) {
	t.Parallel()

	r, err := NewRateValueRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRateRepository_Name(t *testing.T) {
	t.Parallel()

	r, err := NewRateValueRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.Equal(t, rateValueTableName, r.Name())
}

func TestRateRepository_CheckUP(t *testing.T) {
	t.Parallel()

	r, err := NewRateValueRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NoError(t, r.CheckUP(t.Context()))
}

func TestRateRepository_TransactionErrors(t *testing.T) {
	t.Parallel()

	// Create a valid repository (so migrations run), then swap its db for
	// mockFailDB so Transaction errors out — exercising the db.Transaction error
	// branch in every public method.
	newBrokenRepo := func(t *testing.T) *RateValueRepository {
		t.Helper()
		r, err := NewRateValueRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		r.db = &mockFailDB{err: errors.New("db unavailable")}
		return r
	}

	t.Run("CheckUP propagates transaction error", func(t *testing.T) {
		t.Parallel()
		require.Error(t, newBrokenRepo(t).CheckUP(t.Context()))
	})
	t.Run("ObtainAllRateValueBySourceName propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainAllRateValueBySourceName(t.Context(), "src")
		require.Error(t, err)
	})
	t.Run("ObtainLastNRateValuesBySourceName propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainLastNRateValuesBySourceName(t.Context(), "src", 1)
		require.Error(t, err)
	})
	t.Run("ObtainLatestRateValuesBySourceNames propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainLatestRateValuesBySourceNames(t.Context(), []string{"src"})
		require.Error(t, err)
	})
	t.Run("RetainRateValue propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RetainRateValue(t.Context(), &domain.RateValue{Price: 1.0})
		require.Error(t, err)
	})
	t.Run("RemoveRateValue propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RemoveRateValue(t.Context(), &domain.RateValue{ID: "x"})
		require.Error(t, err)
	})
	t.Run("ObtainValuesForPairsSince propagates transaction error", func(t *testing.T) {
		t.Parallel()
		pairs := []domain.SourcePairKey{{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		_, err := newBrokenRepo(t).ObtainValuesForPairsSince(t.Context(), pairs, time.Now())
		require.Error(t, err)
	})
	t.Run("ObtainHistoryForPairsPaged propagates transaction error", func(t *testing.T) {
		t.Parallel()
		pairs := []domain.SourcePairKey{{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		_, _, _, err := newBrokenRepo(t).ObtainHistoryForPairsPaged(t.Context(), pairs, 10, 0)
		require.Error(t, err)
	})
}

func TestRateRepository_RetainRateValue(t *testing.T) {
	t.Parallel()

	r, err := NewRateValueRepository(stubSQLiteDB(t, "test-source"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		err := r.RetainRateValue(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
	})
	t.Run("insert", func(t *testing.T) {
		t.Parallel()

		rate := &domain.RateValue{
			SourceName:    "test-source",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
			Price:         1.23,
		}
		require.Empty(t, rate.ID)

		err = r.RetainRateValue(t.Context(), rate)
		require.NoError(t, err)
		require.NotEmpty(t, rate.ID)
		require.False(t, rate.Timestamp.IsZero())

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		require.NotNil(t, tx)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM"+" "+rateValueTableName+" WHERE "+rateValueIdFieldName+" = ?", rate.ID).Scan(&count))
		require.Equal(t, 1, count)
	})
	t.Run("update", func(t *testing.T) {
		t.Parallel()

		rate := &domain.RateValue{
			SourceName:    "test-source",
			BaseCurrency:  "USD",
			QuoteCurrency: "RUS",
			Price:         1.23,
		}
		require.Empty(t, rate.ID)

		require.NoError(t, r.RetainRateValue(t.Context(), rate))
		require.NotEmpty(t, rate.ID)

		rate.Price = 9.99
		id := rate.ID

		require.NoError(t, r.RetainRateValue(t.Context(), rate))
		require.NotEmpty(t, rate.ID)
		require.Equal(t, id, rate.ID)

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		require.NotNil(t, tx)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM"+" "+rateValueTableName+" WHERE "+rateValueIdFieldName+" = ?", rate.ID).Scan(&count))
		require.NoError(t, err)
		require.Equal(t, 1, count)

		var value float64
		require.NoError(t, tx.QueryRow("SELECT "+rateValuePriceFieldName+" FROM"+" "+rateValueTableName+" WHERE "+rateValueIdFieldName+" = ?", rate.ID).Scan(&value))
		require.NoError(t, err)
		require.Equal(t, 9.99, value)
	})
}

func TestRateRepository_RemoveRateValue(t *testing.T) {
	t.Parallel()

	r, err := NewRateValueRepository(stubSQLiteDB(t, "test-source"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		err := r.RemoveRateValue(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
	})
	t.Run("delete", func(t *testing.T) {
		t.Parallel()

		rate := &domain.RateValue{
			SourceName:    "test-source",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
			Price:         1.23,
		}
		require.Empty(t, rate.ID)

		err = r.RetainRateValue(t.Context(), rate)
		require.NoError(t, err)
		require.NotEmpty(t, rate.ID)
		require.False(t, rate.Timestamp.IsZero())

		err = r.RemoveRateValue(t.Context(), rate)
		require.NoError(t, err)

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		require.NotNil(t, tx)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM"+" "+rateValueTableName+" WHERE "+rateValueIdFieldName+" = ?", rate.ID).Scan(&count))
		require.Equal(t, 0, count)
	})
}

func TestRateRepository_ObtainAllRateValueBySourceName(t *testing.T) {
	t.Parallel()

	r, err := NewRateValueRepository(stubSQLiteDB(t, "src-a", "src-b"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("returns rates for source", func(t *testing.T) {
		t.Parallel()

		rates := []domain.RateValue{
			{SourceName: "src-a", BaseCurrency: "USD", QuoteCurrency: "EUR", Price: 1.1},
			{SourceName: "src-a", BaseCurrency: "USD", QuoteCurrency: "GBP", Price: 1.2},
			{SourceName: "src-b", BaseCurrency: "USD", QuoteCurrency: "JPY", Price: 150},
		}
		for _, rate := range rates {
			require.NoError(t, r.RetainRateValue(t.Context(), &rate))
		}

		result, err := r.ObtainAllRateValueBySourceName(t.Context(), "src-a")
		require.NoError(t, err)
		require.Len(t, result, 2)

		sort.Slice(result, func(i, j int) bool {
			return result[i].QuoteCurrency < result[j].QuoteCurrency
		})

		require.Equal(t, result[0].Price, 1.1)
		require.Equal(t, result[1].Price, 1.2)
	})
	t.Run("empty for unknown source", func(t *testing.T) {
		t.Parallel()

		result, err := r.ObtainAllRateValueBySourceName(t.Context(), "nonexistent")
		require.NoError(t, err)
		require.Empty(t, result)
	})
}

func TestRateRepository_ObtainLastNRateValuesBySourceName(t *testing.T) {
	t.Parallel()

	r, err := NewRateValueRepository(stubSQLiteDB(t, "few-source", "many-source"))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("zero rows", func(t *testing.T) {
		t.Parallel()

		result, err := r.ObtainLastNRateValuesBySourceName(t.Context(), "empty-source", 2)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result)
	})
	t.Run("fewer rows than limit", func(t *testing.T) {
		t.Parallel()

		for _, price := range []float64{1.0, 2.0} {
			rate := &domain.RateValue{SourceName: "few-source", BaseCurrency: "USD", QuoteCurrency: "EUR", Price: price}
			require.NoError(t, r.RetainRateValue(t.Context(), rate))
		}

		result, err := r.ObtainLastNRateValuesBySourceName(t.Context(), "few-source", 5)
		require.NoError(t, err)
		require.Len(t, result, 2)
	})
	t.Run("more rows than limit returns newest first", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()

		for i := 0; i < 5; i++ {
			rate := &domain.RateValue{SourceName: "many-source", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: float64(100 + i), Timestamp: now.Add(time.Duration(i+1) * time.Minute)}
			require.NoError(t, r.RetainRateValue(t.Context(), rate))
		}

		result, err := r.ObtainLastNRateValuesBySourceName(t.Context(), "many-source", 2)
		require.NoError(t, err)
		require.Len(t, result, 2)
		// Newest first: index 0 must have the highest price (inserted last)
		require.GreaterOrEqual(t, result[0].Price, result[1].Price)
	})
}

func TestRateValueRepository_ObtainLatestRateValuesBySourceNames(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns empty map without querying", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		got, err := r.ObtainLatestRateValuesBySourceNames(t.Context(), nil)
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("returns newest row per source, missing sources absent", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t, "bulk-rv-a", "bulk-rv-b"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		for _, rv := range []domain.RateValue{
			{SourceName: "bulk-rv-a", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 100, Timestamp: base.Add(-time.Minute)},
			{SourceName: "bulk-rv-a", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 200, Timestamp: base},
			{SourceName: "bulk-rv-b", BaseCurrency: "EUR", QuoteCurrency: "KZT", Price: 500, Timestamp: base},
		} {
			require.NoError(t, r.RetainRateValue(t.Context(), &rv))
		}

		got, err := r.ObtainLatestRateValuesBySourceNames(t.Context(),
			[]string{"bulk-rv-a", "bulk-rv-b", "missing-rv"})
		require.NoError(t, err)
		require.Len(t, got, 2, "missing-rv must be absent")
		require.Equal(t, 200.0, got["bulk-rv-a"].Price, "must return the newest row")
		require.Equal(t, 500.0, got["bulk-rv-b"].Price)
	})
}

func TestRateValueRepository_ObtainValuesForPairsSince(t *testing.T) {
	t.Parallel()

	// seedWithTimestamp inserts a RateValue then overwrites its timestamp via a
	// direct SQL UPDATE so tests can control it precisely. RetainRateValue always
	// sets Timestamp = now, hence the override.
	seedWithTimestamp := func(t *testing.T, r *RateValueRepository, rv domain.RateValue, ts time.Time) domain.RateValue {
		t.Helper()
		rv.ID = "" // let repo generate the ID
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

	t.Run("empty pairs returns empty slice without querying DB", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		result, err := r.ObtainValuesForPairsSince(t.Context(), nil, time.Now())
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result)
	})

	t.Run("single pair single match returns that row", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t, "src-single"))
		require.NoError(t, err)

		base := time.Now().UTC().Add(-time.Hour)
		seedWithTimestamp(t, r, domain.RateValue{
			SourceName:    "src-single",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Price:         100,
		}, base)

		pairs := []domain.SourcePairKey{
			{SourceName: "src-single", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
		}
		result, err := r.ObtainValuesForPairsSince(t.Context(), pairs, base.Add(-time.Minute))
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Equal(t, 100.0, result[0].Price)
	})

	t.Run("single pair multiple matches returned in ascending timestamp order", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t, "src-multi"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		for i, price := range []float64{10, 20, 30} {
			seedWithTimestamp(t, r, domain.RateValue{
				SourceName:    "src-multi",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Price:         price,
			}, base.Add(time.Duration(i)*time.Minute))
		}

		pairs := []domain.SourcePairKey{{SourceName: "src-multi", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		result, err := r.ObtainValuesForPairsSince(t.Context(), pairs, base.Add(-time.Second))
		require.NoError(t, err)
		require.Len(t, result, 3)
		require.Equal(t, 10.0, result[0].Price)
		require.Equal(t, 20.0, result[1].Price)
		require.Equal(t, 30.0, result[2].Price)
	})

	t.Run("multiple pairs interleaved correctly by timestamp", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t, "src-pair-a", "src-pair-b"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "src-pair-a", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 1}, base)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "src-pair-b", BaseCurrency: "EUR", QuoteCurrency: "KZT", Price: 2}, base.Add(time.Minute))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "src-pair-a", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 3}, base.Add(2*time.Minute))

		pairs := []domain.SourcePairKey{
			{SourceName: "src-pair-a", BaseCurrency: "USD", QuoteCurrency: "KZT"},
			{SourceName: "src-pair-b", BaseCurrency: "EUR", QuoteCurrency: "KZT"},
		}
		result, err := r.ObtainValuesForPairsSince(t.Context(), pairs, base.Add(-time.Second))
		require.NoError(t, err)
		require.Len(t, result, 3)
		// ascending timestamp order
		require.Equal(t, 1.0, result[0].Price)
		require.Equal(t, 2.0, result[1].Price)
		require.Equal(t, 3.0, result[2].Price)
	})

	t.Run("since filter excludes older rows", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t, "src-since"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		cutoff := base.Add(-time.Hour)
		// One row before cutoff (excluded), one after (included).
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "src-since", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 1}, base.Add(-2*time.Hour))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "src-since", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 2}, base)

		pairs := []domain.SourcePairKey{{SourceName: "src-since", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		result, err := r.ObtainValuesForPairsSince(t.Context(), pairs, cutoff)
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Equal(t, 2.0, result[0].Price)
	})

	t.Run("rows for unrelated source are not returned", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateValueRepository(stubSQLiteDB(t, "src-wanted", "src-other"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "src-wanted", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 1}, base)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "src-other", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 99}, base)

		pairs := []domain.SourcePairKey{{SourceName: "src-wanted", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		result, err := r.ObtainValuesForPairsSince(t.Context(), pairs, base.Add(-time.Second))
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Equal(t, 1.0, result[0].Price)
	})

	t.Run("rows for deleted source are excluded via FK cascade", func(t *testing.T) {
		t.Parallel()

		db := stubSQLiteDB(t, "src-cascade")
		r, err := NewRateValueRepository(db)
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{
			SourceName:    "src-cascade",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Price:         42,
		}, base)

		// Confirm the row is present before deletion.
		pairs := []domain.SourcePairKey{{SourceName: "src-cascade", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		before, err := r.ObtainValuesForPairsSince(t.Context(), pairs, base.Add(-time.Second))
		require.NoError(t, err)
		require.Len(t, before, 1)

		// Delete the source; ON DELETE CASCADE must remove the rate_value row.
		srcRepo, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		require.NoError(t, srcRepo.RemoveRateSource(t.Context(), &domain.RateSource{Name: "src-cascade"}))

		// The rate_value row must be gone.
		after, err := r.ObtainValuesForPairsSince(t.Context(), pairs, base.Add(-time.Second))
		require.NoError(t, err)
		require.Empty(t, after, "ON DELETE CASCADE must remove rate_value rows when their source is deleted")
	})
}

func TestRateValueRepository_ObtainHistoryForPairsPaged(t *testing.T) {
	t.Parallel()

	// seedWithTimestamp inserts a RateValue and overwrites its timestamp via a
	// direct SQL UPDATE so tests can control the timestamp precisely.
	seedWithTimestamp := func(t *testing.T, r *RateValueRepository, rv domain.RateValue, ts time.Time) domain.RateValue {
		t.Helper()
		rv.ID = ""
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

	t.Run("empty pairs returns empty slice and zero total", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateValueRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		rows, total, groupedTotal, err := r.ObtainHistoryForPairsPaged(t.Context(), nil, 20, 0)
		require.NoError(t, err)
		require.Empty(t, rows)
		require.EqualValues(t, 0, total)
		require.EqualValues(t, 0, groupedTotal)
	})

	t.Run("single source single direction returns rows newest first", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateValueRepository(stubSQLiteDB(t, "hist-single"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-single", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 100}, base.Add(-2*time.Minute))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-single", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 200}, base.Add(-time.Minute))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-single", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 300}, base)

		pairs := []domain.SourcePairKey{{SourceName: "hist-single", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		rows, total, groupedTotal, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.NoError(t, err)
		require.EqualValues(t, 3, total)
		require.EqualValues(t, 3, groupedTotal)
		require.Len(t, rows, 3)
		// Newest first.
		require.Equal(t, 300.0, rows[0].Price)
		require.Equal(t, 200.0, rows[1].Price)
		require.Equal(t, 100.0, rows[2].Price)
	})

	t.Run("two sources two directions returns interleaved rows ordered by timestamp", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateValueRepository(stubSQLiteDB(t, "hist-src-a", "hist-src-b"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-src-a", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 1}, base.Add(-2*time.Minute))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-src-b", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 2}, base.Add(-time.Minute))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-src-a", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 3}, base)

		pairs := []domain.SourcePairKey{
			{SourceName: "hist-src-a", BaseCurrency: "USD", QuoteCurrency: "KZT"},
			{SourceName: "hist-src-b", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		rows, total, _, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.NoError(t, err)
		require.EqualValues(t, 3, total)
		require.Len(t, rows, 3)
		// Newest first.
		require.Equal(t, 3.0, rows[0].Price)
		require.Equal(t, 2.0, rows[1].Price)
		require.Equal(t, 1.0, rows[2].Price)
	})

	t.Run("limit caps returned rows", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateValueRepository(stubSQLiteDB(t, "hist-limit"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		for i := range 5 {
			seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-limit", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: float64(i + 1)}, base.Add(time.Duration(i)*time.Second))
		}

		pairs := []domain.SourcePairKey{{SourceName: "hist-limit", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		rows, total, _, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 2, 0)
		require.NoError(t, err)
		require.EqualValues(t, 5, total)
		require.Len(t, rows, 2)
	})

	t.Run("offset skips earlier rows", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateValueRepository(stubSQLiteDB(t, "hist-offset"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		for i := range 5 {
			seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-offset", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: float64(i + 1)}, base.Add(time.Duration(i)*time.Second))
		}

		pairs := []domain.SourcePairKey{{SourceName: "hist-offset", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		// Newest row is price=5; offset=2 skips the two newest, so first returned is price=3.
		rows, total, _, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 10, 2)
		require.NoError(t, err)
		require.EqualValues(t, 5, total)
		require.Len(t, rows, 3)
		require.Equal(t, 3.0, rows[0].Price)
	})

	t.Run("total reflects un-paginated row count", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateValueRepository(stubSQLiteDB(t, "hist-total"))
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		for i := range 10 {
			seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-total", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: float64(i)}, base.Add(time.Duration(i)*time.Second))
		}

		pairs := []domain.SourcePairKey{{SourceName: "hist-total", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		_, total, _, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 3, 0)
		require.NoError(t, err)
		require.EqualValues(t, 10, total)
	})

	t.Run("ids tie-break when timestamps are equal", func(t *testing.T) {
		t.Parallel()
		r, err := NewRateValueRepository(stubSQLiteDB(t, "hist-tie"))
		require.NoError(t, err)

		// Both rows share the same timestamp (second resolution); ID DESC breaks the tie.
		sameTS := time.Now().UTC().Truncate(time.Second)
		rv1 := seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-tie", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 10}, sameTS)
		rv2 := seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-tie", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 20}, sameTS)

		pairs := []domain.SourcePairKey{{SourceName: "hist-tie", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		rows, total, _, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Len(t, rows, 2)
		// ID DESC: rv2 was inserted after rv1, so its ID is lexicographically larger;
		// it must come first.
		require.True(t, rv2.ID > rv1.ID, "pre-condition: rv2.ID must be greater for this test to be meaningful")
		require.Equal(t, rv2.ID, rows[0].ID)
		require.Equal(t, rv1.ID, rows[1].ID)
	})

	// seedSourceWithTitle creates a rate_source with a specific title instead of
	// the default "test fixture <name>" produced by stubSQLiteDB.
	seedSourceWithTitle := func(t *testing.T, sqlDB db, name, title, kind string) {
		t.Helper()
		sr, srErr := NewRateSourceRepository(sqlDB)
		require.NoError(t, srErr)
		require.NoError(t, sr.RetainRateSource(t.Context(), &domain.RateSource{
			Name:          name,
			Title:         title,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			URL:           "https://example.invalid/" + name,
			Interval:      "10m",
			Kind:          domain.RateSourceKind(kind),
			Active:        true,
		}))
	}

	t.Run("single source two timestamps counted as grouped total 2", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDB(t)
		seedSourceWithTitle(t, sqlDB, "hist-gt-single", "Center Credit Bank (FX)", "BID")
		r, err := NewRateValueRepository(sqlDB)
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-gt-single", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 100}, base.Add(-time.Minute))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-gt-single", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 200}, base)

		pairs := []domain.SourcePairKey{{SourceName: "hist-gt-single", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		_, rowTotal, groupedTotal, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.NoError(t, err)
		require.EqualValues(t, 2, rowTotal)
		require.EqualValues(t, 2, groupedTotal)
	})

	t.Run("sibling BID+ASK sources at same timestamp count as grouped total 1", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDB(t)
		// Two sources share the same title; their rows at the same timestamp are
		// one distinct (title, timestamp) tuple in the grouped count.
		seedSourceWithTitle(t, sqlDB, "hist-gt-bid", "Center Credit Bank (FX)", "BID")
		seedSourceWithTitle(t, sqlDB, "hist-gt-ask", "Center Credit Bank (FX)", "ASK")
		r, err := NewRateValueRepository(sqlDB)
		require.NoError(t, err)

		sameTS := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-gt-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487}, sameTS)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-gt-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 489}, sameTS)

		pairs := []domain.SourcePairKey{
			{SourceName: "hist-gt-bid", BaseCurrency: "USD", QuoteCurrency: "KZT"},
			{SourceName: "hist-gt-ask", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		_, rowTotal, groupedTotal, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.NoError(t, err)
		require.EqualValues(t, 2, rowTotal, "two raw rate_values rows")
		require.EqualValues(t, 1, groupedTotal, "BID+ASK at same timestamp under same title count as 1")
	})

	t.Run("title containing literal pipe is counted correctly", func(t *testing.T) {
		t.Parallel()
		// Locks in the pipe-delimiter behaviour: a title that itself contains '|'
		// must not corrupt the COUNT(DISTINCT ...) result for two distinct timestamps.
		sqlDB := stubSQLiteDB(t)
		seedSourceWithTitle(t, sqlDB, "hist-gt-pipe", "AT|T Bank", "BID")
		r, err := NewRateValueRepository(sqlDB)
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-gt-pipe", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 100}, base.Add(-time.Minute))
		seedWithTimestamp(t, r, domain.RateValue{SourceName: "hist-gt-pipe", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 200}, base)

		pairs := []domain.SourcePairKey{{SourceName: "hist-gt-pipe", BaseCurrency: "USD", QuoteCurrency: "KZT"}}
		_, rowTotal, groupedTotal, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.NoError(t, err)
		require.EqualValues(t, 2, rowTotal)
		require.EqualValues(t, 2, groupedTotal, "two distinct timestamps must count as 2 even when title contains '|'")
	})
}
