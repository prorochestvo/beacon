package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ tierReader = (*fakeTierReader)(nil)

// windowCall records the bounds one tier was asked for, so a test can assert the split
// itself rather than only the rows that came back from it.
type windowCall struct {
	since time.Time
	until time.Time
}

// fakeTierReader answers from an in-memory row set using the same half-open window
// semantics the SQL implements, so the routing is exercised against real filtering
// rather than a mock that returns whatever it is handed.
type fakeTierReader struct {
	rows     []domain.RateValue
	err      error
	windows  []windowCall
	pageErr  error
	pageArgs []int64
	rowTotal int64
	grouped  int64
}

func (f *fakeTierReader) ObtainValuesForPairsBetween(
	_ context.Context, pairs []domain.SourcePairKey, since, until time.Time,
) ([]domain.RateValue, error) {
	f.windows = append(f.windows, windowCall{since: since, until: until})
	if f.err != nil {
		return nil, f.err
	}
	if len(pairs) == 0 {
		return []domain.RateValue{}, nil
	}
	out := make([]domain.RateValue, 0, len(f.rows))
	for _, r := range f.rows {
		if r.Timestamp.Before(since) {
			continue
		}
		if !until.IsZero() && !r.Timestamp.Before(until) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeTierReader) ObtainHistoryForPairsPaged(
	_ context.Context, _ []domain.SourcePairKey, limit, offset int64,
) ([]domain.RateValue, int64, int64, error) {
	f.pageArgs = append(f.pageArgs, limit, offset)
	if f.pageErr != nil {
		return nil, 0, 0, f.pageErr
	}
	return f.rows, f.rowTotal, f.grouped, nil
}

// tierRows builds n rows one day apart ending at end, oldest first — the order both
// tiers return and the order the concatenation has to preserve.
func tierRows(prefix string, end time.Time, n int) []domain.RateValue {
	out := make([]domain.RateValue, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, domain.RateValue{
			ID:            fmt.Sprintf("%s%03d", prefix, i),
			SourceName:    "src",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Price:         float64(i),
			Timestamp:     end.Add(-time.Duration(i) * 24 * time.Hour),
		})
	}
	return out
}

// prunedTo drops rows older than cut, the way the hot tier's pruning will. Deriving the
// fixture from the same boundary the reader uses is deliberate: hand-counting how many
// rows survive a 180-day horizon is exactly the off-by-one this test is meant to catch
// in the code rather than reproduce in itself.
func prunedTo(rows []domain.RateValue, cut time.Time) []domain.RateValue {
	out := make([]domain.RateValue, 0, len(rows))
	for _, r := range rows {
		if r.Timestamp.Before(cut) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func TestNewTieredRateValueRepository(t *testing.T) {
	t.Parallel()

	t.Run("both tiers are required", func(t *testing.T) {
		t.Parallel()
		_, err := NewTieredRateValueRepository(nil, &fakeTierReader{}, 0, nil)
		require.Error(t, err)
		_, err = NewTieredRateValueRepository(&fakeTierReader{}, nil, 0, nil)
		require.Error(t, err)
	})

	t.Run("a non-positive horizon falls back to the default", func(t *testing.T) {
		t.Parallel()
		r, err := NewTieredRateValueRepository(&fakeTierReader{}, &fakeTierReader{}, 0, nil)
		require.NoError(t, err)
		assert.Equal(t, DefaultHotHorizon, r.horizon)
		assert.NotNil(t, r.now, "now must default so the horizon can be evaluated")
	})
}

func TestTieredRateValueRepository_ObtainValuesForPairsSince(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	horizon := 180 * 24 * time.Hour
	cut := now.Add(-horizon)
	pairs := []domain.SourcePairKey{{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT"}}

	newTiered := func(t *testing.T, hot, archive *fakeTierReader) *TieredRateValueRepository {
		t.Helper()
		r, err := NewTieredRateValueRepository(hot, archive, horizon, clock)
		require.NoError(t, err)
		return r
	}

	t.Run("a window inside the horizon never touches the archive", func(t *testing.T) {
		t.Parallel()
		hot := &fakeTierReader{rows: tierRows("hot-", now, 5)}
		archive := &fakeTierReader{rows: tierRows("arc-", now, 5)}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainValuesForPairsSince(t.Context(), pairs, now.Add(-7*24*time.Hour))
		require.NoError(t, err)
		assert.Len(t, got, 5)
		assert.Empty(t, archive.windows, "the hot tier answers every period up to the horizon")
		require.Len(t, hot.windows, 1)
		assert.True(t, hot.windows[0].until.IsZero(), "a hot-only read has no upper bound")
	})

	t.Run("a window starting exactly at the horizon stays hot", func(t *testing.T) {
		t.Parallel()
		hot := &fakeTierReader{}
		archive := &fakeTierReader{}
		r := newTiered(t, hot, archive)

		_, err := r.ObtainValuesForPairsSince(t.Context(), pairs, cut)
		require.NoError(t, err)
		assert.Empty(t, archive.windows, "the boundary belongs to the hot tier")
		assert.Len(t, hot.windows, 1)
	})

	t.Run("a window crossing the horizon is split into disjoint halves", func(t *testing.T) {
		t.Parallel()
		hot := &fakeTierReader{}
		archive := &fakeTierReader{}
		r := newTiered(t, hot, archive)

		since := now.Add(-360 * 24 * time.Hour)
		_, err := r.ObtainValuesForPairsSince(t.Context(), pairs, since)
		require.NoError(t, err)

		require.Len(t, archive.windows, 1)
		require.Len(t, hot.windows, 1)
		assert.Equal(t, since, archive.windows[0].since)
		assert.Equal(t, cut, archive.windows[0].until, "the archive half stops where the hot half begins")
		assert.Equal(t, cut, hot.windows[0].since)
		assert.True(t, hot.windows[0].until.IsZero(), "the recent half runs to now")
	})

	t.Run("the split returns every row once, in order", func(t *testing.T) {
		t.Parallel()
		// 360 days of daily rows in each tier. The archive is a superset, so the same
		// rows exist on both sides of the cut — which is exactly the case a wrong
		// split would duplicate.
		all := tierRows("rv-", now, 360)
		hot := &fakeTierReader{rows: all}
		archive := &fakeTierReader{rows: all}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainValuesForPairsSince(t.Context(), pairs, now.Add(-360*24*time.Hour))
		require.NoError(t, err)

		seen := make(map[string]int, len(got))
		duplicated := make([]string, 0)
		for _, v := range got {
			seen[v.ID]++
			if seen[v.ID] == 2 {
				duplicated = append(duplicated, v.ID)
			}
		}
		assert.Empty(t, duplicated, "the two halves must not overlap")
		assert.Equal(t, 360, len(got), "the two halves must cover the window exactly once")

		for i := 1; i < len(got); i++ {
			assert.False(t, got[i].Timestamp.Before(got[i-1].Timestamp),
				"concatenation must preserve the ascending order each half returns")
		}
	})

	t.Run("a hot tier pruned to the horizon still answers a deep window in full", func(t *testing.T) {
		t.Parallel()
		// The state this whole seam exists for: the hot tier holds only what pruning
		// left it, the archive holds everything.
		all := tierRows("rv-", now, 360)
		hot := &fakeTierReader{rows: prunedTo(all, cut)}
		archive := &fakeTierReader{rows: all}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainValuesForPairsSince(t.Context(), pairs, now.Add(-360*24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, 360, len(got), "no row may fall through the seam between the tiers")

		// And the hot tier alone would have answered short — which is what makes the
		// routing load-bearing rather than decorative.
		assert.Less(t, len(hot.rows), 360)
	})

	t.Run("an archive failure is reported against the archive", func(t *testing.T) {
		t.Parallel()
		archive := &fakeTierReader{err: errors.New("archive unreadable")}
		r := newTiered(t, &fakeTierReader{}, archive)

		_, err := r.ObtainValuesForPairsSince(t.Context(), pairs, now.Add(-360*24*time.Hour))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "archive tier")
	})

	t.Run("a hot failure on the recent half is reported against the hot tier", func(t *testing.T) {
		t.Parallel()
		hot := &fakeTierReader{err: errors.New("db down")}
		r := newTiered(t, hot, &fakeTierReader{})

		_, err := r.ObtainValuesForPairsSince(t.Context(), pairs, now.Add(-360*24*time.Hour))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hot tier")
	})

	t.Run("no pairs returns an empty result from both halves", func(t *testing.T) {
		t.Parallel()
		hot := &fakeTierReader{rows: tierRows("hot-", now, 5)}
		archive := &fakeTierReader{rows: tierRows("arc-", now, 5)}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainValuesForPairsSince(t.Context(), nil, now.Add(-360*24*time.Hour))
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestTieredRateValueRepository_ObtainHistoryForPairsPaged(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	pairs := []domain.SourcePairKey{{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT"}}

	t.Run("every page comes from the archive", func(t *testing.T) {
		t.Parallel()
		// The hot tier reports a smaller total, as a pruned one would. Reading it here
		// would silently truncate the pagination control.
		hot := &fakeTierReader{rows: tierRows("hot-", now, 3), rowTotal: 3, grouped: 3}
		archive := &fakeTierReader{rows: tierRows("arc-", now, 7), rowTotal: 7, grouped: 5}
		r, err := NewTieredRateValueRepository(hot, archive, DefaultHotHorizon, func() time.Time { return now })
		require.NoError(t, err)

		rows, rowTotal, grouped, err := r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.NoError(t, err)
		assert.Len(t, rows, 7)
		assert.Equal(t, int64(7), rowTotal)
		assert.Equal(t, int64(5), grouped)
		assert.Empty(t, hot.pageArgs, "the hot tier's total does not describe how much history exists")
		assert.Equal(t, []int64{20, 0}, archive.pageArgs, "limit and offset pass through untouched")
	})

	t.Run("a deep offset passes through unchanged", func(t *testing.T) {
		t.Parallel()
		archive := &fakeTierReader{rowTotal: 5000, grouped: 2500}
		r, err := NewTieredRateValueRepository(&fakeTierReader{}, archive, DefaultHotHorizon, func() time.Time { return now })
		require.NoError(t, err)

		_, _, _, err = r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 4000)
		require.NoError(t, err)
		assert.Equal(t, []int64{20, 4000}, archive.pageArgs,
			"one tier answers the whole view, so there is no offset to translate")
	})

	t.Run("an archive failure surfaces", func(t *testing.T) {
		t.Parallel()
		archive := &fakeTierReader{pageErr: errors.New("archive unreadable")}
		r, err := NewTieredRateValueRepository(&fakeTierReader{}, archive, DefaultHotHorizon, func() time.Time { return now })
		require.NoError(t, err)

		_, _, _, err = r.ObtainHistoryForPairsPaged(t.Context(), pairs, 20, 0)
		require.Error(t, err)
	})
}
