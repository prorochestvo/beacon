package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ archiveSourceRepository        = (*fakeHotRepo)(nil)
	_ archiveTargetRepository        = (*fakeArchiveRepo)(nil)
	_ archiveMetaSourceRepository    = (*fakeHotSourceRepo)(nil)
	_ archiveMetaTargetRepository    = (*fakeArchiveSourceRepo)(nil)
	_ archiveHistorySourceRepository = (*fakeHotHistoryRepo)(nil)
	_ archiveHistoryTargetRepository = (*fakeArchiveHistoryRepo)(nil)
)

// fakeHotRepo is an in-memory stand-in for the hot tier that implements the same
// keyset walk the SQL does, so the agent's cursor handling is exercised for real
// rather than against a mock that hands back whatever it is asked for.
type fakeHotRepo struct {
	rows  []domain.RateValue
	err   error
	calls int
}

func (f *fakeHotRepo) ObtainRateValuesAfter(_ context.Context, after time.Time, afterID string, limit int64) ([]domain.RateValue, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	sorted := append([]domain.RateValue(nil), f.rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Timestamp.Equal(sorted[j].Timestamp) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	out := make([]domain.RateValue, 0, limit)
	for _, r := range sorted {
		if !after.IsZero() || afterID != "" {
			if r.Timestamp.Before(after) {
				continue
			}
			if r.Timestamp.Equal(after) && r.ID <= afterID {
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

// fakeArchiveRepo mimics INSERT OR IGNORE keyed on the id.
type fakeArchiveRepo struct {
	stored    map[string]domain.RateValue
	retainErr error
	wmErr     error
	batches   [][]string
}

func newFakeArchiveRepo() *fakeArchiveRepo {
	return &fakeArchiveRepo{stored: map[string]domain.RateValue{}}
}

func (f *fakeArchiveRepo) RetainRateValues(_ context.Context, records []domain.RateValue) (int, error) {
	if f.retainErr != nil {
		return 0, f.retainErr
	}
	ids := make([]string, 0, len(records))
	inserted := 0
	for _, r := range records {
		ids = append(ids, r.ID)
		if _, ok := f.stored[r.ID]; ok {
			continue
		}
		f.stored[r.ID] = r
		inserted++
	}
	f.batches = append(f.batches, ids)
	return inserted, nil
}

func (f *fakeArchiveRepo) ObtainArchiveWatermark(context.Context) (time.Time, string, bool, error) {
	if f.wmErr != nil {
		return time.Time{}, "", false, f.wmErr
	}
	var (
		bestTS time.Time
		bestID string
		found  bool
	)
	for _, r := range f.stored {
		switch {
		case !found, r.Timestamp.After(bestTS), r.Timestamp.Equal(bestTS) && r.ID > bestID:
			bestTS, bestID, found = r.Timestamp, r.ID, true
		}
	}
	return bestTS, bestID, found, nil
}

// fakeHotSourceRepo stands in for the hot tier's rate_sources table.
type fakeHotSourceRepo struct {
	sources []domain.RateSource
	err     error
	calls   int
}

func newFakeHotSourceRepo(sources ...domain.RateSource) *fakeHotSourceRepo {
	return &fakeHotSourceRepo{sources: sources}
}

func (f *fakeHotSourceRepo) ObtainAllRateSources(context.Context) ([]domain.RateSource, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.RateSource(nil), f.sources...), nil
}

// fakeArchiveSourceRepo mimics the mirror's upsert: rows are inserted or overwritten,
// never removed.
type fakeArchiveSourceRepo struct {
	stored map[string]domain.RateSource
	err    error
	calls  int
}

func newFakeArchiveSourceRepo() *fakeArchiveSourceRepo {
	return &fakeArchiveSourceRepo{stored: map[string]domain.RateSource{}}
}

func (f *fakeArchiveSourceRepo) RetainRateSources(_ context.Context, records []domain.RateSource) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	for _, r := range records {
		f.stored[r.Name] = r
	}
	return len(records), nil
}

// fakeHotHistoryRepo mirrors fakeHotRepo for execution history: it performs the same
// keyset walk the SQL does, so the shared reconcile loop is exercised for real against a
// second row type rather than against a mock that returns whatever it is asked for.
type fakeHotHistoryRepo struct {
	rows  []domain.ExecutionHistory
	err   error
	calls int
}

func (f *fakeHotHistoryRepo) ObtainExecutionHistoryAfter(_ context.Context, after time.Time, afterID string, limit int64) ([]domain.ExecutionHistory, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	sorted := append([]domain.ExecutionHistory(nil), f.rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Timestamp.Equal(sorted[j].Timestamp) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	out := make([]domain.ExecutionHistory, 0, limit)
	for _, r := range sorted {
		if !after.IsZero() || afterID != "" {
			if r.Timestamp.Before(after) {
				continue
			}
			if r.Timestamp.Equal(after) && r.ID <= afterID {
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

// fakeArchiveHistoryRepo mimics INSERT OR IGNORE keyed on the id.
type fakeArchiveHistoryRepo struct {
	stored    map[string]domain.ExecutionHistory
	retainErr error
	wmErr     error
	batches   [][]string
}

func newFakeArchiveHistoryRepo() *fakeArchiveHistoryRepo {
	return &fakeArchiveHistoryRepo{stored: map[string]domain.ExecutionHistory{}}
}

func (f *fakeArchiveHistoryRepo) RetainExecutionHistories(_ context.Context, records []domain.ExecutionHistory) (int, error) {
	if f.retainErr != nil {
		return 0, f.retainErr
	}
	ids := make([]string, 0, len(records))
	inserted := 0
	for _, r := range records {
		ids = append(ids, r.ID)
		if _, ok := f.stored[r.ID]; ok {
			continue
		}
		f.stored[r.ID] = r
		inserted++
	}
	f.batches = append(f.batches, ids)
	return inserted, nil
}

func (f *fakeArchiveHistoryRepo) ObtainArchiveWatermark(context.Context) (time.Time, string, bool, error) {
	if f.wmErr != nil {
		return time.Time{}, "", false, f.wmErr
	}
	var (
		bestTS time.Time
		bestID string
		found  bool
	)
	for _, r := range f.stored {
		switch {
		case !found, r.Timestamp.After(bestTS), r.Timestamp.Equal(bestTS) && r.ID > bestID:
			bestTS, bestID, found = r.Timestamp, r.ID, true
		}
	}
	return bestTS, bestID, found, nil
}

// historyRows builds n outcomes one minute apart starting at base, every third one failed.
func historyRows(n int, base time.Time) []domain.ExecutionHistory {
	out := make([]domain.ExecutionHistory, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.ExecutionHistory{
			ID:         fmt.Sprintf("eh-%03d", i),
			SourceName: "src",
			Success:    i%3 != 0,
			Timestamp:  base.Add(time.Duration(i) * time.Minute),
		})
	}
	return out
}

func hotRows(n int, base time.Time) []domain.RateValue {
	out := make([]domain.RateValue, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.RateValue{
			ID:            "rv-" + string(rune('a'+i/26)) + string(rune('a'+i%26)),
			SourceName:    "src",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Price:         float64(i),
			Timestamp:     base.Add(time.Duration(i) * time.Minute),
		})
	}
	return out
}

func TestNewArchiveAgent(t *testing.T) {
	t.Parallel()

	t.Run("every repository is required", func(t *testing.T) {
		t.Parallel()
		_, err := NewArchiveAgent(nil, newFakeArchiveRepo(), newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 10, io.Discard)
		require.Error(t, err)
		_, err = NewArchiveAgent(&fakeHotRepo{}, nil, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 10, io.Discard)
		require.Error(t, err)
		_, err = NewArchiveAgent(&fakeHotRepo{}, newFakeArchiveRepo(), nil, newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 10, io.Discard)
		require.Error(t, err)
		_, err = NewArchiveAgent(&fakeHotRepo{}, newFakeArchiveRepo(), newFakeHotSourceRepo(), nil, &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 10, io.Discard)
		require.Error(t, err)
	})

	t.Run("a non-positive batch size falls back to the default", func(t *testing.T) {
		t.Parallel()
		a, err := NewArchiveAgent(&fakeHotRepo{}, newFakeArchiveRepo(), newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 0, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, DefaultArchiveBatchSize, a.batchSize)
	})
}

func TestArchiveAgent_Run(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("an empty archive is backfilled in full", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotRepo{rows: hotRows(7, base)}
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(hot, archive, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 3, io.Discard)
		require.NoError(t, err)

		report, err := agent.Run(t.Context())
		copied := report.RateValues
		require.NoError(t, err)
		assert.Equal(t, 7, copied)
		assert.Len(t, archive.stored, 7)
		assert.Len(t, archive.batches, 3, "7 rows at a batch size of 3 is three round trips")
	})

	t.Run("a second run copies nothing and reads one empty batch", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotRepo{rows: hotRows(5, base)}
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(hot, archive, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard)
		require.NoError(t, err)

		_, err = agent.Run(t.Context())
		require.NoError(t, err)
		before := hot.calls

		report, err := agent.Run(t.Context())
		copied := report.RateValues
		require.NoError(t, err)
		assert.Zero(t, copied, "steady state must be free")
		assert.Equal(t, before+1, hot.calls, "the watermark short-circuits after one empty read")
		assert.Len(t, archive.stored, 5)
	})

	t.Run("only rows newer than the watermark are copied", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotRepo{rows: hotRows(4, base)}
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(hot, archive, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard)
		require.NoError(t, err)
		_, err = agent.Run(t.Context())
		require.NoError(t, err)

		hot.rows = append(hot.rows, domain.RateValue{
			ID: "rv-zz", SourceName: "src", Timestamp: base.Add(time.Hour),
		})

		report, err := agent.Run(t.Context())
		copied := report.RateValues
		require.NoError(t, err)
		assert.Equal(t, 1, copied)
		require.NotEmpty(t, archive.batches)
		assert.Equal(t, []string{"rv-zz"}, archive.batches[len(archive.batches)-1],
			"the pass must not re-read rows the archive already holds")
	})

	t.Run("rows sharing one timestamp do not stall the walk", func(t *testing.T) {
		t.Parallel()
		// One collector tick: three rows, one timestamp. A timestamp-only cursor
		// would either loop forever here or drop the tail of the tick.
		hot := &fakeHotRepo{rows: []domain.RateValue{
			{ID: "rv-a", Timestamp: base}, {ID: "rv-b", Timestamp: base}, {ID: "rv-c", Timestamp: base},
		}}
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(hot, archive, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 2, io.Discard)
		require.NoError(t, err)

		report, err := agent.Run(t.Context())
		copied := report.RateValues
		require.NoError(t, err)
		assert.Equal(t, 3, copied)
		assert.Len(t, archive.stored, 3)
	})

	t.Run("a write failure stops the pass and keeps the copied prefix", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotRepo{rows: hotRows(5, base)}
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(hot, archive, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard)
		require.NoError(t, err)

		archive.retainErr = errors.New("disk full")
		report, err := agent.Run(t.Context())
		copied := report.RateValues
		require.Error(t, err)
		assert.Zero(t, copied)
		assert.Empty(t, archive.stored)

		// The next pass, with the fault cleared, picks the gap up on its own: no
		// bookkeeping survives a failed run, and none is needed.
		archive.retainErr = nil
		report, err = agent.Run(t.Context())
		copied = report.RateValues
		require.NoError(t, err)
		assert.Equal(t, 5, copied)
	})

	t.Run("a read failure is reported, not skipped past", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotRepo{err: errors.New("db down")}
		agent, err := NewArchiveAgent(hot, newFakeArchiveRepo(), newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard)
		require.NoError(t, err)

		_, err = agent.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read hot batch")
	})

	t.Run("a watermark failure aborts before any write", func(t *testing.T) {
		t.Parallel()
		archive := newFakeArchiveRepo()
		archive.wmErr = errors.New("archive unreadable")
		agent, err := NewArchiveAgent(&fakeHotRepo{rows: hotRows(3, base)}, archive, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard)
		require.NoError(t, err)

		_, err = agent.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "watermark")
		assert.Empty(t, archive.stored, "an unknown watermark must never lead to a blind copy")
	})

	t.Run("a cancelled context stops cleanly with the prefix committed", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotRepo{rows: hotRows(10, base)}
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(hot, archive, newFakeHotSourceRepo(), newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		report, err := agent.Run(ctx)
		copied := report.RateValues
		require.NoError(t, err, "a cancelled tick is not a failure")
		assert.Zero(t, copied)
	})
}

func TestArchiveAgent_SyncSources(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	// A function, not a shared slice: newFakeHotSourceRepo(sources()...) forwards the
	// caller's backing array rather than copying it, so a subtest that edits a title
	// would be writing into every other parallel subtest's fixture.
	sources := func() []domain.RateSource {
		return []domain.RateSource{
			{Name: "src", Title: "Provider One", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
			{Name: "src2", Title: "Provider Two", BaseCurrency: "EUR", QuoteCurrency: "KZT", Kind: domain.RateSourceKindASK},
		}
	}

	t.Run("the source mirror is refreshed on every pass", func(t *testing.T) {
		t.Parallel()
		hotMeta := newFakeHotSourceRepo(sources()...)
		archiveMeta := newFakeArchiveSourceRepo()
		agent, err := NewArchiveAgent(
			&fakeHotRepo{rows: hotRows(3, base)}, newFakeArchiveRepo(),
			hotMeta, archiveMeta, &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard,
		)
		require.NoError(t, err)

		_, err = agent.Run(t.Context())
		require.NoError(t, err)
		assert.Len(t, archiveMeta.stored, 2)
		assert.Equal(t, "Provider One", archiveMeta.stored["src"].Title)

		// A second pass copies no new values but still refreshes the mirror, which is
		// what repairs a title edited in the hot tier between ticks.
		hotMeta.sources[0].Title = "Provider One Renamed"
		_, err = agent.Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 2, archiveMeta.calls)
		assert.Equal(t, "Provider One Renamed", archiveMeta.stored["src"].Title)
	})

	t.Run("sources are mirrored before any value is copied", func(t *testing.T) {
		t.Parallel()
		archiveMeta := newFakeArchiveSourceRepo()
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(
			&fakeHotRepo{rows: hotRows(3, base)}, archive,
			newFakeHotSourceRepo(sources()...), archiveMeta, &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard,
		)
		require.NoError(t, err)

		// The mirror write must already have happened by the time the first value
		// batch lands, or the history view's grouping join has nothing to resolve.
		archive.retainErr = errors.New("value write refused")
		_, err = agent.Run(t.Context())
		require.Error(t, err)
		assert.NotEmpty(t, archiveMeta.stored, "the mirror is written first, so it survives a failed value batch")
	})

	t.Run("a mirror failure aborts the pass before any value is copied", func(t *testing.T) {
		t.Parallel()
		archiveMeta := newFakeArchiveSourceRepo()
		archiveMeta.err = errors.New("mirror unwritable")
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(
			&fakeHotRepo{rows: hotRows(3, base)}, archive,
			newFakeHotSourceRepo(sources()...), archiveMeta, &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard,
		)
		require.NoError(t, err)

		report, err := agent.Run(t.Context())
		copied := report.RateValues
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mirror sources")
		assert.Zero(t, copied)
		assert.Empty(t, archive.stored, "values must not outrun the sources that describe them")
	})

	t.Run("an unreadable hot source table aborts the pass", func(t *testing.T) {
		t.Parallel()
		hotMeta := newFakeHotSourceRepo(sources()...)
		hotMeta.err = errors.New("db down")
		archive := newFakeArchiveRepo()
		agent, err := NewArchiveAgent(
			&fakeHotRepo{rows: hotRows(3, base)}, archive,
			hotMeta, newFakeArchiveSourceRepo(), &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard,
		)
		require.NoError(t, err)

		_, err = agent.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read hot sources")
		assert.Empty(t, archive.stored)
	})

	t.Run("an empty source table is not an error", func(t *testing.T) {
		t.Parallel()
		archiveMeta := newFakeArchiveSourceRepo()
		agent, err := NewArchiveAgent(
			&fakeHotRepo{rows: hotRows(3, base)}, newFakeArchiveRepo(),
			newFakeHotSourceRepo(), archiveMeta, &fakeHotHistoryRepo{}, newFakeArchiveHistoryRepo(), 100, io.Discard,
		)
		require.NoError(t, err)

		report, err := agent.Run(t.Context())
		copied := report.RateValues
		require.NoError(t, err)
		assert.Equal(t, 3, copied, "values still archive when there is no source metadata to mirror")
		assert.Zero(t, archiveMeta.calls, "an empty set is not written")
	})
}

func TestArchiveAgent_ExecutionHistory(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	newAgent := func(t *testing.T, hotHistory *fakeHotHistoryRepo, archiveHistory *fakeArchiveHistoryRepo, batch int) *ArchiveAgent {
		t.Helper()
		a, err := NewArchiveAgent(
			&fakeHotRepo{}, newFakeArchiveRepo(),
			newFakeHotSourceRepo(), newFakeArchiveSourceRepo(),
			hotHistory, archiveHistory,
			batch, io.Discard,
		)
		require.NoError(t, err)
		return a
	}

	t.Run("an empty archive is backfilled in full", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotHistoryRepo{rows: historyRows(7, base)}
		archive := newFakeArchiveHistoryRepo()

		report, err := newAgent(t, hot, archive, 3).Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 7, report.ExecutionHistory)
		assert.Len(t, archive.stored, 7)
		assert.Len(t, archive.batches, 3, "7 rows at a batch size of 3 is three round trips")
	})

	t.Run("a second run copies nothing and reads one empty batch", func(t *testing.T) {
		t.Parallel()
		hot := &fakeHotHistoryRepo{rows: historyRows(5, base)}
		archive := newFakeArchiveHistoryRepo()
		agent := newAgent(t, hot, archive, 100)

		_, err := agent.Run(t.Context())
		require.NoError(t, err)
		before := hot.calls

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Zero(t, report.ExecutionHistory, "steady state must be free")
		assert.Equal(t, before+1, hot.calls, "the watermark short-circuits after one empty read")
	})

	t.Run("rows sharing one tick's timestamp are walked, not re-read", func(t *testing.T) {
		t.Parallel()
		// One collector tick writes an outcome per source at the same second. A cursor
		// on the timestamp alone would either loop on that tick or skip the rest of it.
		rows := make([]domain.ExecutionHistory, 0, 6)
		for i := 0; i < 6; i++ {
			rows = append(rows, domain.ExecutionHistory{
				ID:         fmt.Sprintf("eh-%03d", i),
				SourceName: fmt.Sprintf("src-%d", i),
				Success:    true,
				Timestamp:  base,
			})
		}
		hot := &fakeHotHistoryRepo{rows: rows}
		archive := newFakeArchiveHistoryRepo()

		report, err := newAgent(t, hot, archive, 2).Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 6, report.ExecutionHistory)
		assert.Len(t, archive.stored, 6)
	})

	t.Run("both tables are copied in one pass", func(t *testing.T) {
		t.Parallel()
		archiveValues := newFakeArchiveRepo()
		archiveHistory := newFakeArchiveHistoryRepo()
		agent, err := NewArchiveAgent(
			&fakeHotRepo{rows: hotRows(4, base)}, archiveValues,
			newFakeHotSourceRepo(), newFakeArchiveSourceRepo(),
			&fakeHotHistoryRepo{rows: historyRows(6, base)}, archiveHistory,
			100, io.Discard,
		)
		require.NoError(t, err)

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 4, report.RateValues)
		assert.Equal(t, 6, report.ExecutionHistory)
		assert.Equal(t, 10, report.Total())
	})

	t.Run("a rate-value failure leaves execution history for the next tick", func(t *testing.T) {
		t.Parallel()
		archiveValues := newFakeArchiveRepo()
		archiveValues.retainErr = errors.New("value write refused")
		archiveHistory := newFakeArchiveHistoryRepo()
		agent, err := NewArchiveAgent(
			&fakeHotRepo{rows: hotRows(4, base)}, archiveValues,
			newFakeHotSourceRepo(), newFakeArchiveSourceRepo(),
			&fakeHotHistoryRepo{rows: historyRows(6, base)}, archiveHistory,
			100, io.Discard,
		)
		require.NoError(t, err)

		report, err := agent.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate values")
		assert.Zero(t, report.ExecutionHistory)
		assert.Empty(t, archiveHistory.stored,
			"the pass stops at the first error; neither table's completeness depends on the other's")
	})

	t.Run("an execution-history failure is named as such", func(t *testing.T) {
		t.Parallel()
		archive := newFakeArchiveHistoryRepo()
		archive.retainErr = errors.New("history write refused")

		_, err := newAgent(t, &fakeHotHistoryRepo{rows: historyRows(3, base)}, archive, 100).Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "execution history")
		assert.Contains(t, err.Error(), "write batch")
	})

	t.Run("a watermark failure aborts before any write", func(t *testing.T) {
		t.Parallel()
		archive := newFakeArchiveHistoryRepo()
		archive.wmErr = errors.New("archive unreadable")

		_, err := newAgent(t, &fakeHotHistoryRepo{rows: historyRows(3, base)}, archive, 100).Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "watermark")
		assert.Empty(t, archive.stored, "an unknown watermark must never lead to a blind copy")
	})

	t.Run("a cancelled context stops cleanly", func(t *testing.T) {
		t.Parallel()
		archive := newFakeArchiveHistoryRepo()
		agent := newAgent(t, &fakeHotHistoryRepo{rows: historyRows(10, base)}, archive, 100)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		report, err := agent.Run(ctx)
		require.NoError(t, err, "a cancelled tick is not a failure")
		assert.Zero(t, report.ExecutionHistory)
	})
}
