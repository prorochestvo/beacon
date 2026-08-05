package collection

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ tieredRepository = (*fakeTieredRepo)(nil)
	_ metaRepository   = (*fakeMetaRepo)(nil)
	_ vacuumer         = (*fakeVacuumer)(nil)
)

// fakeTieredRepo records the cutoffs it was handed, which is what the ordering and
// window arithmetic are actually asserted on.
type fakeTieredRepo struct {
	name          string
	moved         int64
	pruned        int64
	rolloverErr   error
	pruneErr      error
	rolloverCalls []time.Time
	pruneCalls    []time.Time
	order         *[]string
}

func (f *fakeTieredRepo) Name() string { return f.name }

func (f *fakeTieredRepo) RolloverToArchive(_ context.Context, cutoff time.Time) (int64, error) {
	f.rolloverCalls = append(f.rolloverCalls, cutoff)
	if f.order != nil {
		*f.order = append(*f.order, "rollover:"+f.name)
	}
	if f.rolloverErr != nil {
		return 0, f.rolloverErr
	}
	return f.moved, nil
}

func (f *fakeTieredRepo) PruneArchive(_ context.Context, cutoff time.Time) (int64, error) {
	f.pruneCalls = append(f.pruneCalls, cutoff)
	if f.order != nil {
		*f.order = append(*f.order, "prune:"+f.name)
	}
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}
	if cutoff.IsZero() {
		return 0, nil
	}
	return f.pruned, nil
}

type fakeMetaRepo struct {
	values    map[string]string
	readErr   error
	writeErr  error
	writeCall int
}

func newFakeMetaRepo() *fakeMetaRepo { return &fakeMetaRepo{values: map[string]string{}} }

func (f *fakeMetaRepo) ObtainServiceMeta(_ context.Context, key string) (string, bool, error) {
	if f.readErr != nil {
		return "", false, f.readErr
	}
	v, ok := f.values[key]
	return v, ok, nil
}

func (f *fakeMetaRepo) RetainServiceMeta(_ context.Context, key, value string) error {
	f.writeCall++
	if f.writeErr != nil {
		return f.writeErr
	}
	f.values[key] = value
	return nil
}

type fakeVacuumer struct {
	calls int
	err   error
	order *[]string
}

func (f *fakeVacuumer) Vacuum(context.Context) error {
	f.calls++
	if f.order != nil {
		*f.order = append(*f.order, "vacuum")
	}
	if f.err != nil {
		return f.err
	}
	return nil
}

func TestNewMaintenanceAgent(t *testing.T) {
	t.Parallel()

	table := &fakeTieredRepo{name: "rate_values"}

	t.Run("dependencies are required", func(t *testing.T) {
		t.Parallel()
		_, err := NewMaintenanceAgent(nil, &fakeVacuumer{}, 0, 0, 0, io.Discard, table)
		require.Error(t, err)
		_, err = NewMaintenanceAgent(newFakeMetaRepo(), nil, 0, 0, 0, io.Discard, table)
		require.Error(t, err)
		_, err = NewMaintenanceAgent(newFakeMetaRepo(), &fakeVacuumer{}, 0, 0, 0, io.Discard)
		require.Error(t, err, "a pass with nothing to maintain is a wiring mistake")
		_, err = NewMaintenanceAgent(newFakeMetaRepo(), &fakeVacuumer{}, 0, 0, 0, io.Discard, table, nil)
		require.Error(t, err)
	})

	t.Run("zero durations fall back, except retention", func(t *testing.T) {
		t.Parallel()
		a, err := NewMaintenanceAgent(newFakeMetaRepo(), &fakeVacuumer{}, 0, 0, 0, io.Discard, table)
		require.NoError(t, err)
		assert.Equal(t, DefaultHotWindow, a.hotWindow)
		assert.Equal(t, DefaultVacuumInterval, a.vacuumInterval)
		// Zero retention means keep forever, so it must survive the fallback that rewrites
		// the other two.
		assert.Zero(t, a.retention)
	})
}

func TestMaintenanceAgent_Run(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	newAgent := func(t *testing.T, meta *fakeMetaRepo, vac *fakeVacuumer, retention time.Duration, tables ...tieredRepository) *MaintenanceAgent {
		t.Helper()
		a, err := NewMaintenanceAgent(meta, vac, 180*24*time.Hour, retention, 7*24*time.Hour, io.Discard, tables...)
		require.NoError(t, err)
		a.now = func() time.Time { return now }
		return a
	}

	t.Run("every table is rolled over at the hot-window cutoff", func(t *testing.T) {
		t.Parallel()
		values := &fakeTieredRepo{name: "rate_values", moved: 7}
		history := &fakeTieredRepo{name: "execution_history", moved: 3}

		report, err := newAgent(t, newFakeMetaRepo(), &fakeVacuumer{}, 0, values, history).Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(10), report.Total())
		assert.Equal(t, int64(7), report.Moved["rate_values"])
		assert.Equal(t, int64(3), report.Moved["execution_history"])

		require.Len(t, values.rolloverCalls, 1)
		assert.Equal(t, now.Add(-180*24*time.Hour), values.rolloverCalls[0])
		require.Len(t, history.rolloverCalls, 1)
		assert.Equal(t, now.Add(-180*24*time.Hour), history.rolloverCalls[0])
	})

	t.Run("retention off passes a zero cutoff, so nothing is deleted", func(t *testing.T) {
		t.Parallel()
		values := &fakeTieredRepo{name: "rate_values", pruned: 99}

		report, err := newAgent(t, newFakeMetaRepo(), &fakeVacuumer{}, 0, values).Run(t.Context())
		require.NoError(t, err)
		assert.Empty(t, report.Pruned)
		require.Len(t, values.pruneCalls, 1)
		assert.True(t, values.pruneCalls[0].IsZero(),
			"the configured state is keep-forever, and it must be expressed as a cutoff that deletes nothing")
	})

	t.Run("retention on passes its own cutoff", func(t *testing.T) {
		t.Parallel()
		values := &fakeTieredRepo{name: "rate_values", pruned: 4}

		report, err := newAgent(t, newFakeMetaRepo(), &fakeVacuumer{}, 1000*24*time.Hour, values).Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(4), report.Pruned["rate_values"])
		require.Len(t, values.pruneCalls, 1)
		assert.Equal(t, now.Add(-1000*24*time.Hour), values.pruneCalls[0])
	})

	t.Run("vacuum runs last, after everything it has to reclaim", func(t *testing.T) {
		t.Parallel()
		var order []string
		values := &fakeTieredRepo{name: "rate_values", order: &order}
		history := &fakeTieredRepo{name: "execution_history", order: &order}
		vac := &fakeVacuumer{order: &order}

		_, err := newAgent(t, newFakeMetaRepo(), vac, 0, values, history).Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{
			"rollover:rate_values", "prune:rate_values",
			"rollover:execution_history", "prune:execution_history",
			"vacuum",
		}, order, "vacuuming before the deletes would compact a file about to be perforated again")
	})
}

func TestMaintenanceAgent_Vacuum(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	table := func() *fakeTieredRepo { return &fakeTieredRepo{name: "rate_values"} }

	newAgent := func(t *testing.T, meta *fakeMetaRepo, vac *fakeVacuumer) *MaintenanceAgent {
		t.Helper()
		a, err := NewMaintenanceAgent(meta, vac, 180*24*time.Hour, 0, 7*24*time.Hour, io.Discard, table())
		require.NoError(t, err)
		a.now = func() time.Time { return now }
		return a
	}

	t.Run("a never-vacuumed database is vacuumed immediately", func(t *testing.T) {
		t.Parallel()
		meta, vac := newFakeMetaRepo(), &fakeVacuumer{}

		report, err := newAgent(t, meta, vac).Run(t.Context())
		require.NoError(t, err)
		assert.True(t, report.Vacuumed)
		assert.Equal(t, 1, vac.calls, "a fresh deployment should not wait a week for its first reclaim")
		assert.Equal(t, now.Format(time.RFC3339), meta.values[repository.ServiceMetaKeyLastVacuum])
	})

	t.Run("a recent stamp skips the run", func(t *testing.T) {
		t.Parallel()
		meta, vac := newFakeMetaRepo(), &fakeVacuumer{}
		meta.values[repository.ServiceMetaKeyLastVacuum] = now.Add(-24 * time.Hour).Format(time.RFC3339)

		report, err := newAgent(t, meta, vac).Run(t.Context())
		require.NoError(t, err)
		assert.False(t, report.Vacuumed)
		assert.Zero(t, vac.calls)
	})

	t.Run("an expired stamp runs again", func(t *testing.T) {
		t.Parallel()
		meta, vac := newFakeMetaRepo(), &fakeVacuumer{}
		meta.values[repository.ServiceMetaKeyLastVacuum] = now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)

		report, err := newAgent(t, meta, vac).Run(t.Context())
		require.NoError(t, err)
		assert.True(t, report.Vacuumed)
		assert.Equal(t, 1, vac.calls)
	})

	t.Run("a failed vacuum leaves the stamp alone so the next tick retries", func(t *testing.T) {
		t.Parallel()
		meta := newFakeMetaRepo()
		vac := &fakeVacuumer{err: errors.New("database is locked")}

		report, err := newAgent(t, meta, vac).Run(t.Context())
		require.Error(t, err)
		assert.False(t, report.Vacuumed)
		assert.Empty(t, meta.values, "stamping a failed run would skip a whole interval over a transient lock")
		assert.Zero(t, meta.writeCall)
	})

	t.Run("an unparseable stamp is surfaced, not silently re-vacuumed", func(t *testing.T) {
		t.Parallel()
		meta, vac := newFakeMetaRepo(), &fakeVacuumer{}
		meta.values[repository.ServiceMetaKeyLastVacuum] = "last tuesday"

		_, err := newAgent(t, meta, vac).Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not RFC3339")
		assert.Zero(t, vac.calls, "vacuuming on every tick because a value got corrupted is worse than an error")
	})

	t.Run("the roll-over still happened when vacuum fails", func(t *testing.T) {
		t.Parallel()
		values := &fakeTieredRepo{name: "rate_values", moved: 5}
		a, err := NewMaintenanceAgent(newFakeMetaRepo(), &fakeVacuumer{err: errors.New("locked")},
			180*24*time.Hour, 0, 7*24*time.Hour, io.Discard, values)
		require.NoError(t, err)
		a.now = func() time.Time { return now }

		report, err := a.Run(t.Context())
		require.Error(t, err)
		assert.Equal(t, int64(5), report.Total(), "the report must still describe the committed work")
	})
}

func TestMaintenanceAgent_Failures(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	newAgent := func(t *testing.T, tables ...tieredRepository) (*MaintenanceAgent, *fakeVacuumer) {
		t.Helper()
		vac := &fakeVacuumer{}
		a, err := NewMaintenanceAgent(newFakeMetaRepo(), vac, 180*24*time.Hour, 0, 7*24*time.Hour, io.Discard, tables...)
		require.NoError(t, err)
		a.now = func() time.Time { return now }
		return a, vac
	}

	t.Run("a rollover failure names the table and stops the pass", func(t *testing.T) {
		t.Parallel()
		failing := &fakeTieredRepo{name: "rate_values", rolloverErr: errors.New("disk full")}
		second := &fakeTieredRepo{name: "execution_history"}
		a, vac := newAgent(t, failing, second)

		_, err := a.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rollover rate_values")
		assert.Empty(t, second.rolloverCalls, "the pass stops rather than pressing on")
		assert.Zero(t, vac.calls, "and never vacuums a file whose roll-over half-completed")
	})

	t.Run("a prune failure names the table", func(t *testing.T) {
		t.Parallel()
		failing := &fakeTieredRepo{name: "rate_values", pruneErr: errors.New("disk full")}
		a, _ := newAgent(t, failing)

		_, err := a.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prune rate_values")
	})

	t.Run("a stamp read failure aborts before vacuuming", func(t *testing.T) {
		t.Parallel()
		meta := newFakeMetaRepo()
		meta.readErr = errors.New("db down")
		vac := &fakeVacuumer{}
		a, err := NewMaintenanceAgent(meta, vac, 180*24*time.Hour, 0, 7*24*time.Hour, io.Discard,
			&fakeTieredRepo{name: "rate_values"})
		require.NoError(t, err)
		a.now = func() time.Time { return now }

		_, err = a.Run(t.Context())
		require.Error(t, err)
		assert.Zero(t, vac.calls, "an unknown cadence must not licence an unbounded vacuum")
	})
}
