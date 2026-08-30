package notification

import (
	"context"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAdminChatID int64 = 424242

var (
	_ sourceHealthSourceRepository  = (*fakeHealthSourceRepo)(nil)
	_ sourceHealthHistoryRepository = (*fakeHealthHistoryRepo)(nil)
	_ sourceHealthStateRepository   = (*fakeHealthStateRepo)(nil)
	_ rateCheckEventRepository      = (*fakeHealthEventRepo)(nil)
)

type fakeHealthSourceRepo struct {
	sources []domain.RateSource
	err     error
}

func (f *fakeHealthSourceRepo) ObtainAllRateSources(context.Context) ([]domain.RateSource, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sources, nil
}

type fakeHealthHistoryRepo struct {
	health []domain.SourceCollectionHealth
	err    error
}

func (f *fakeHealthHistoryRepo) ObtainSourceCollectionHealth(context.Context) ([]domain.SourceCollectionHealth, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.health, nil
}

// fakeHealthStateRepo mimics the latch table, keeping the same map the agent reads so a
// test can assert the persisted state rather than only the report.
type fakeHealthStateRepo struct {
	latched   map[string]time.Time
	readErr   error
	writeErr  error
	deleteErr error
}

func newFakeHealthStateRepo(names ...string) *fakeHealthStateRepo {
	m := map[string]time.Time{}
	for _, n := range names {
		m[n] = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	}
	return &fakeHealthStateRepo{latched: m}
}

func (f *fakeHealthStateRepo) ObtainAlertedSources(context.Context) (map[string]time.Time, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	out := make(map[string]time.Time, len(f.latched))
	for k, v := range f.latched {
		out[k] = v
	}
	return out, nil
}

func (f *fakeHealthStateRepo) RetainAlertedSource(_ context.Context, name string, at time.Time) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.latched[name] = at
	return nil
}

func (f *fakeHealthStateRepo) RemoveAlertedSource(_ context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.latched, name)
	return nil
}

type fakeHealthEventRepo struct {
	events []domain.RateUserEvent
	err    error
}

func (f *fakeHealthEventRepo) RetainRateUserEvent(_ context.Context, e *domain.RateUserEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, *e)
	return nil
}

func healthSource(name, interval string, active bool) domain.RateSource {
	return domain.RateSource{
		Name:          name,
		Title:         name + " Bank",
		Interval:      interval,
		Active:        active,
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		Kind:          domain.RateSourceKindBID,
	}
}

// TestSourceHealthAgent_AbortIsMarked covers the worst case #74 names. This agent is the
// watchdog that reports a source gone silent, so when its own pre-flight read fails, the
// outage it exists to announce disappears along with it.
func TestSourceHealthAgent_AbortIsMarked(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func(error) (*SourceHealthAgent, error){
		"source read": func(readErr error) (*SourceHealthAgent, error) {
			return NewSourceHealthAgent(&fakeHealthSourceRepo{err: readErr}, &fakeHealthHistoryRepo{},
				newFakeHealthStateRepo(), &fakeHealthEventRepo{}, testAdminChatID, 0, 0, io.Discard)
		},
		"collection history read": func(readErr error) (*SourceHealthAgent, error) {
			return NewSourceHealthAgent(&fakeHealthSourceRepo{}, &fakeHealthHistoryRepo{err: readErr},
				newFakeHealthStateRepo(), &fakeHealthEventRepo{}, testAdminChatID, 0, 0, io.Discard)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			readErr := errors.New("read interrupted")
			agent, buildErr := build(readErr)
			require.NoError(t, buildErr)

			_, err := agent.Run(t.Context())
			require.Error(t, err)
			require.ErrorIs(t, err, internal.ErrAgentAborted,
				"the watchdog going quiet is the failure that must reach a human")
			require.ErrorIs(t, err, readErr, "the cause must survive the marking")
		})
	}
}

func TestNewSourceHealthAgent(t *testing.T) {
	t.Parallel()

	src := &fakeHealthSourceRepo{}
	hist := &fakeHealthHistoryRepo{}
	state := newFakeHealthStateRepo()
	events := &fakeHealthEventRepo{}

	t.Run("every repository is required", func(t *testing.T) {
		t.Parallel()
		_, err := NewSourceHealthAgent(nil, hist, state, events, testAdminChatID, 0, 0, io.Discard)
		require.Error(t, err)
		_, err = NewSourceHealthAgent(src, nil, state, events, testAdminChatID, 0, 0, io.Discard)
		require.Error(t, err)
		_, err = NewSourceHealthAgent(src, hist, nil, events, testAdminChatID, 0, 0, io.Discard)
		require.Error(t, err)
		_, err = NewSourceHealthAgent(src, hist, state, nil, testAdminChatID, 0, 0, io.Discard)
		require.Error(t, err)
	})

	t.Run("an absent admin chat is refused rather than dropped", func(t *testing.T) {
		t.Parallel()
		// Constructing without a destination would produce an agent that computes alerts
		// and throws them away — the exact silence this exists to end.
		_, err := NewSourceHealthAgent(src, hist, state, events, 0, 0, 0, io.Discard)
		require.Error(t, err)
	})

	t.Run("zero tuning falls back to the defaults", func(t *testing.T) {
		t.Parallel()
		a, err := NewSourceHealthAgent(src, hist, state, events, testAdminChatID, 0, 0, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, DefaultSourceStaleFactor, a.staleFactor)
		assert.Equal(t, DefaultSourceStaleFloor, a.staleFloor)
	})
}

func TestSourceHealthAgent_Run(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	// A 6h source — production's actual cadence — is allowed 18h of silence.
	newAgent := func(
		t *testing.T,
		sources []domain.RateSource,
		health []domain.SourceCollectionHealth,
		state *fakeHealthStateRepo,
		events *fakeHealthEventRepo,
	) *SourceHealthAgent {
		t.Helper()
		a, err := NewSourceHealthAgent(
			&fakeHealthSourceRepo{sources: sources},
			&fakeHealthHistoryRepo{health: health},
			state, events, testAdminChatID, 3, time.Hour, io.Discard,
		)
		require.NoError(t, err)
		a.now = func() time.Time { return now }
		return a
	}

	t.Run("a source inside its window says nothing", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("src", "6h", true)},
			[]domain.SourceCollectionHealth{{
				SourceName: "src", LastSuccessAt: now.Add(-7 * time.Hour), LastRunAt: now.Add(-7 * time.Hour),
			}},
			state, events)

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Empty(t, report.Alerted)
		assert.Empty(t, events.events)
		assert.Empty(t, state.latched)
	})

	t.Run("a source past its window alerts once and latches", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("src", "6h", true)},
			[]domain.SourceCollectionHealth{{
				SourceName:          "src",
				LastSuccessAt:       now.Add(-30 * time.Hour),
				LastRunAt:           now.Add(-time.Hour),
				ConsecutiveFailures: 5,
				LastError:           "do request: connection refused",
			}},
			state, events)

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"src"}, report.Alerted)
		require.Len(t, events.events, 1)
		assert.Contains(t, state.latched, "src")

		e := events.events[0]
		assert.Equal(t, strconv.FormatInt(testAdminChatID, 10), e.UserID, "operator alerts go to the admin chat")
		assert.Equal(t, "src", e.SourceName)
		assert.Equal(t, domain.RateUserEventStatusPending, e.Status)
		assert.Contains(t, e.Message, "Source silent")
		assert.Contains(t, e.Message, "5", "the failure count is what separates failing loudly from not running")
		assert.Contains(t, e.Message, "connection refused")
	})

	t.Run("staying broken stays silent", func(t *testing.T) {
		t.Parallel()
		// The whole point of the latch. The obvious fix for an unnoticed outage is to
		// alert every run, which trades silence for an alarm nobody reads.
		state, events := newFakeHealthStateRepo("src"), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("src", "6h", true)},
			[]domain.SourceCollectionHealth{{
				SourceName: "src", LastSuccessAt: now.Add(-30 * time.Hour), LastRunAt: now.Add(-time.Hour),
			}},
			state, events)

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Empty(t, report.Alerted)
		assert.Empty(t, events.events)
		assert.Contains(t, state.latched, "src", "the latch stays on while the source is still broken")
	})

	t.Run("recovery notifies once and clears the latch", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo("src"), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("src", "6h", true)},
			[]domain.SourceCollectionHealth{{
				SourceName: "src", LastSuccessAt: now.Add(-30 * time.Minute), LastRunAt: now.Add(-30 * time.Minute),
			}},
			state, events)

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"src"}, report.Recovered)
		require.Len(t, events.events, 1)
		assert.Contains(t, events.events[0].Message, "Source recovered")
		assert.NotContains(t, state.latched, "src")

		// And a second pass after recovery is quiet again.
		events.events = nil
		second, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Empty(t, second.Recovered)
		assert.Empty(t, events.events)
	})

	t.Run("a source that has never succeeded is judged from its first attempt", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("src", "6h", true)},
			[]domain.SourceCollectionHealth{{
				SourceName:          "src",
				LastRunAt:           now.Add(-40 * time.Hour),
				ConsecutiveFailures: 7,
				LastError:           "selector matched nothing",
			}},
			state, events)

		// A zero LastSuccessAt must not read as "recent" — that would hide the worst case
		// behind the best-looking value.
		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"src"}, report.Alerted)
		require.Len(t, events.events, 1)
		assert.Contains(t, events.events[0].Message, "Never collected successfully")
	})

	t.Run("a source that has never run at all is left alone", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("fresh", "6h", true)},
			nil, // no history row at all
			state, events)

		// A source added a minute ago has not failed; it has not started.
		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Empty(t, report.Alerted)
		assert.Empty(t, events.events)
	})

	t.Run("a source that stopped being attempted is reported as such", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("src", "6h", true)},
			[]domain.SourceCollectionHealth{{
				SourceName:    "src",
				LastSuccessAt: now.Add(-40 * time.Hour),
				LastRunAt:     now.Add(-40 * time.Hour),
				// No failures since: it is not failing, it simply stopped running. A
				// consecutive-failure counter would read zero here and say nothing.
				ConsecutiveFailures: 0,
			}},
			state, events)

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"src"}, report.Alerted)
		require.Len(t, events.events, 1)
		assert.Contains(t, events.events[0].Message, "no longer being collected")
	})

	t.Run("the window scales with each source's own interval", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		// Same silence, different cadence: 10h is dead for a 1h source and fine for a 6h one.
		agent := newAgent(t,
			[]domain.RateSource{
				healthSource("fast", "1h", true),
				healthSource("slow", "6h", true),
			},
			[]domain.SourceCollectionHealth{
				{SourceName: "fast", LastSuccessAt: now.Add(-10 * time.Hour), LastRunAt: now.Add(-10 * time.Hour)},
				{SourceName: "slow", LastSuccessAt: now.Add(-10 * time.Hour), LastRunAt: now.Add(-10 * time.Hour)},
			},
			state, events)

		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"fast"}, report.Alerted,
			"a fixed run count or a fixed duration would have judged these two identically")
	})

	t.Run("the floor protects sources configured below the collector cadence", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		a, err := NewSourceHealthAgent(
			&fakeHealthSourceRepo{sources: []domain.RateSource{healthSource("tiny", "1m", true)}},
			&fakeHealthHistoryRepo{health: []domain.SourceCollectionHealth{{
				SourceName: "tiny", LastSuccessAt: now.Add(-30 * time.Minute), LastRunAt: now.Add(-30 * time.Minute),
			}}},
			state, events, testAdminChatID, 3, time.Hour, io.Discard,
		)
		require.NoError(t, err)
		a.now = func() time.Time { return now }

		// 3×1m is three minutes, but the collector is cron-driven and cannot attempt
		// anything that often; without the floor this source would alert forever.
		report, err := a.Run(t.Context())
		require.NoError(t, err)
		assert.Empty(t, report.Alerted)
	})

	t.Run("inactive sources are ignored", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("off", "6h", false)},
			[]domain.SourceCollectionHealth{{
				SourceName: "off", LastSuccessAt: now.Add(-500 * time.Hour), LastRunAt: now.Add(-500 * time.Hour),
			}},
			state, events)

		// Not collecting a disabled source is the configuration working, not a fault.
		report, err := agent.Run(t.Context())
		require.NoError(t, err)
		assert.Empty(t, report.Alerted)
		assert.Empty(t, events.events)
	})

	t.Run("one broken source does not silence the rest", func(t *testing.T) {
		t.Parallel()
		state, events := newFakeHealthStateRepo(), &fakeHealthEventRepo{}
		agent := newAgent(t,
			[]domain.RateSource{
				healthSource("bad-interval", "not-a-duration", true),
				healthSource("healthy-report", "6h", true),
			},
			[]domain.SourceCollectionHealth{
				{SourceName: "bad-interval", LastSuccessAt: now.Add(-99 * time.Hour), LastRunAt: now.Add(-99 * time.Hour)},
				{SourceName: "healthy-report", LastSuccessAt: now.Add(-30 * time.Hour), LastRunAt: now.Add(-time.Hour)},
			},
			state, events)

		report, err := agent.Run(t.Context())
		require.Error(t, err, "the malformed interval is still reported")
		assert.Contains(t, err.Error(), "invalid interval")
		assert.Equal(t, []string{"healthy-report"}, report.Alerted,
			"a source the agent could not judge must not suppress the ones it could")
	})

	t.Run("a failed enqueue leaves the latch off so the next run retries", func(t *testing.T) {
		t.Parallel()
		state := newFakeHealthStateRepo()
		events := &fakeHealthEventRepo{err: errors.New("pool unavailable")}
		agent := newAgent(t,
			[]domain.RateSource{healthSource("src", "6h", true)},
			[]domain.SourceCollectionHealth{{
				SourceName: "src", LastSuccessAt: now.Add(-30 * time.Hour), LastRunAt: now.Add(-time.Hour),
			}},
			state, events)

		// Latching before the alert is queued would swallow the outage permanently.
		report, err := agent.Run(t.Context())
		require.Error(t, err)
		assert.Empty(t, report.Alerted)
		assert.NotContains(t, state.latched, "src")
	})

	t.Run("an unreadable dependency aborts before deciding anything", func(t *testing.T) {
		t.Parallel()
		for name, agent := range map[string]*SourceHealthAgent{
			"sources": mustAgent(t, &fakeHealthSourceRepo{err: errors.New("db down")}, &fakeHealthHistoryRepo{}, newFakeHealthStateRepo(), &fakeHealthEventRepo{}),
			"history": mustAgent(t, &fakeHealthSourceRepo{}, &fakeHealthHistoryRepo{err: errors.New("db down")}, newFakeHealthStateRepo(), &fakeHealthEventRepo{}),
			"latches": mustAgent(t, &fakeHealthSourceRepo{}, &fakeHealthHistoryRepo{}, &fakeHealthStateRepo{readErr: errors.New("db down")}, &fakeHealthEventRepo{}),
		} {
			_, err := agent.Run(t.Context())
			require.Error(t, err, "dependency %s", name)
		}
	})
}

func mustAgent(
	t *testing.T,
	src *fakeHealthSourceRepo,
	hist *fakeHealthHistoryRepo,
	state sourceHealthStateRepository,
	events *fakeHealthEventRepo,
) *SourceHealthAgent {
	t.Helper()
	a, err := NewSourceHealthAgent(src, hist, state, events, testAdminChatID, 3, time.Hour, io.Discard)
	require.NoError(t, err)
	return a
}

func TestHumaniseDuration(t *testing.T) {
	t.Parallel()

	// The alert is read on a phone during an incident; "3h 20m" is what an operator
	// parses, "3h20m0s" is what a Go program prints.
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "less than a minute"},
		{45 * time.Minute, "45m"},
		{3 * time.Hour, "3h"},
		{3*time.Hour + 20*time.Minute, "3h 20m"},
		{49 * time.Hour, "2d 1h"},
		{48 * time.Hour, "2d"},
		{-time.Hour, "less than a minute"},
	} {
		assert.Equal(t, tc.want, humaniseDuration(tc.in), "input %s", tc.in)
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "short", truncateRunes("short", 10))
	assert.Equal(t, "abc…", truncateRunes("abcdef", 3))
	// Multi-byte input must be cut on a rune boundary, not mid-character — upstream
	// errors carry non-ASCII text.
	assert.Equal(t, "привет…", truncateRunes("приветствие", 6))
}
