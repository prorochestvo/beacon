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

var _ executionHistoryTierReader = (*fakeHistoryTierReader)(nil)

// historyLastNCall records one newest-first request, cursor and filter included.
type historyLastNCall struct {
	before      time.Time
	beforeID    string
	limit       int64
	successOnly bool
}

// fakeHistoryTierReader answers from an in-memory row set using the same cursor and
// filter semantics the SQL implements, so the routing is exercised against real
// filtering rather than a mock that returns whatever it is handed.
type fakeHistoryTierReader struct {
	rows []domain.ExecutionHistory
	err  error

	lastNCalls []historyLastNCall
	latest     map[string]domain.ExecutionHistory
	latestCall bool
	errorCount int64
	countCall  bool
	pageArgs   []int64
}

func (f *fakeHistoryTierReader) ObtainLastNExecutionHistoryBySourceName(
	ctx context.Context, sourceName string, limit int64, successOnly bool,
) ([]domain.ExecutionHistory, error) {
	return f.ObtainLastNExecutionHistoryBySourceNameBefore(ctx, sourceName, time.Time{}, "", limit, successOnly)
}

func (f *fakeHistoryTierReader) ObtainLastNExecutionHistoryBySourceNameBefore(
	_ context.Context, _ string, before time.Time, beforeID string, limit int64, successOnly bool,
) ([]domain.ExecutionHistory, error) {
	f.lastNCalls = append(f.lastNCalls, historyLastNCall{
		before: before, beforeID: beforeID, limit: limit, successOnly: successOnly,
	})
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.ExecutionHistory, 0, limit)
	for _, r := range f.rows {
		if successOnly && !r.Success {
			continue
		}
		if !before.IsZero() {
			if r.Timestamp.After(before) {
				continue
			}
			if r.Timestamp.Equal(before) && r.ID >= beforeID {
				continue
			}
		}
		out = append(out, r)
		if int64(len(out)) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeHistoryTierReader) ObtainLatestExecutionHistoryBySources(
	_ context.Context, _ []string,
) (map[string]domain.ExecutionHistory, error) {
	f.latestCall = true
	if f.err != nil {
		return nil, f.err
	}
	return f.latest, nil
}

func (f *fakeHistoryTierReader) ObtainExecutionHistoryErrorCount(context.Context) (int64, error) {
	f.countCall = true
	if f.err != nil {
		return 0, f.err
	}
	return f.errorCount, nil
}

func (f *fakeHistoryTierReader) ObtainLastNExecutionHistoryErrors(
	_ context.Context, offset, limit int64,
) ([]domain.ExecutionHistory, error) {
	f.pageArgs = append(f.pageArgs, limit, offset)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// tierHistoryRows builds n outcomes one day apart ending at end, newest first — the order
// the last-N reads return.
func tierHistoryRows(prefix string, end time.Time, n int) []domain.ExecutionHistory {
	out := make([]domain.ExecutionHistory, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.ExecutionHistory{
			ID:         fmt.Sprintf("%s%03d", prefix, i),
			SourceName: "src",
			Success:    i%3 != 0,
			Timestamp:  end.Add(-time.Duration(i) * 24 * time.Hour),
		})
	}
	return out
}

func TestNewTieredExecutionHistoryRepository(t *testing.T) {
	t.Parallel()

	_, err := NewTieredExecutionHistoryRepository(nil, &fakeHistoryTierReader{})
	require.Error(t, err)
	_, err = NewTieredExecutionHistoryRepository(&fakeHistoryTierReader{}, nil)
	require.Error(t, err)
}

func TestTieredExecutionHistoryRepository(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	newTiered := func(t *testing.T, hot, archive *fakeHistoryTierReader) *TieredExecutionHistoryRepository {
		t.Helper()
		r, err := NewTieredExecutionHistoryRepository(hot, archive)
		require.NoError(t, err)
		return r
	}

	t.Run("the latest row per source comes from the hot tier", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHistoryTierReader{latest: map[string]domain.ExecutionHistory{"src": {ID: "eh-hot"}}}
		archive := &fakeHistoryTierReader{latest: map[string]domain.ExecutionHistory{"src": {ID: "eh-arc"}}}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainLatestExecutionHistoryBySources(t.Context(), []string{"src"})
		require.NoError(t, err)
		assert.Equal(t, "eh-hot", got["src"].ID, "the operator's live view must not lag behind reconciliation")
		assert.False(t, archive.latestCall)
	})

	t.Run("the unbounded error count comes from the archive", func(t *testing.T) {
		t.Parallel()
		// A pruned hot tier reports a smaller number, which is precisely the value that
		// stops meaning "every failure ever".
		hot := &fakeHistoryTierReader{errorCount: 12}
		archive := &fakeHistoryTierReader{errorCount: 420}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainExecutionHistoryErrorCount(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(420), got)
		assert.False(t, hot.countCall)
	})

	t.Run("paged failures come from the archive with the arguments untouched", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHistoryTierReader{}
		archive := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 3)}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainLastNExecutionHistoryErrors(t.Context(), 4000, 20)
		require.NoError(t, err)
		assert.Len(t, got, 3)
		assert.Empty(t, hot.pageArgs, "the count and the pages must come from one tier or they disagree")
		assert.Equal(t, []int64{20, 4000}, archive.pageArgs)
	})

	t.Run("a limit the hot tier can fill never touches the archive", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 300)}
		archive := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 400)}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 100, false)
		require.NoError(t, err)
		assert.Len(t, got, 100)
		assert.Empty(t, archive.lastNCalls, "the ordinary request costs exactly one query")
	})

	t.Run("a short hot answer is topped up from the archive", func(t *testing.T) {
		t.Parallel()
		// ~4.4 outcomes per source per day means the API's maximum limit of 1000 reaches
		// further back than a 180-day horizon holds.
		hot := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 180)}
		archive := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 400)}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 300, false)
		require.NoError(t, err)
		require.Len(t, got, 300, "the caller asked in rows and must get that many rows")

		seen := make(map[string]int, len(got))
		for _, v := range got {
			seen[v.ID]++
		}
		assert.Len(t, seen, 300, "the top-up must not repeat a row the hot tier returned")

		for i := 1; i < len(got); i++ {
			assert.False(t, got[i].Timestamp.After(got[i-1].Timestamp), "the result stays newest-first")
		}
	})

	t.Run("the successOnly filter carries into the top-up", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 6)}
		archive := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 30)}
		r := newTiered(t, hot, archive)

		got, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 8, true)
		require.NoError(t, err)
		for _, v := range got {
			assert.True(t, v.Success, "a successes-only request must not pick up failures across the seam")
		}

		require.Len(t, archive.lastNCalls, 1)
		assert.True(t, archive.lastNCalls[0].successOnly, "resuming under a different filter would skip rows")
	})

	t.Run("the top-up resumes strictly after the last hot row", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 5)}
		archive := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 20)}
		r := newTiered(t, hot, archive)

		_, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 12, false)
		require.NoError(t, err)

		require.Len(t, archive.lastNCalls, 1)
		call := archive.lastNCalls[0]
		oldestHot := hot.rows[len(hot.rows)-1]
		assert.Equal(t, oldestHot.Timestamp, call.before)
		assert.Equal(t, oldestHot.ID, call.beforeID)
		assert.Equal(t, int64(7), call.limit, "only the shortfall is requested")
	})

	t.Run("an empty hot tier lets the archive answer the whole request", func(t *testing.T) {
		t.Parallel()
		archive := &fakeHistoryTierReader{rows: tierHistoryRows("eh-", now, 20)}
		r := newTiered(t, &fakeHistoryTierReader{}, archive)

		got, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 5, false)
		require.NoError(t, err)
		assert.Len(t, got, 5)
		require.Len(t, archive.lastNCalls, 1)
		assert.True(t, archive.lastNCalls[0].before.IsZero(), "no hot row means no cursor to resume from")
	})

	t.Run("failures are attributed to the tier that produced them", func(t *testing.T) {
		t.Parallel()
		r := newTiered(t, &fakeHistoryTierReader{err: errors.New("db down")}, &fakeHistoryTierReader{})
		_, err := r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 10, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hot tier")

		r = newTiered(t, &fakeHistoryTierReader{}, &fakeHistoryTierReader{err: errors.New("archive unreadable")})
		_, err = r.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src", 10, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "archive tier")
	})
}
