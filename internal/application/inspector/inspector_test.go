package inspector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ Inspector = (*stubInspector)(nil)
var _ dbPinger = (*stubPinger)(nil)
var _ botPinger = (*stubPinger)(nil)

// stubInspector is a test double for Inspector. When block is true, CheckUP blocks
// until ctx is cancelled and returns its error, simulating a hung dependency.
type stubInspector struct {
	name  string
	err   error
	block bool
}

func (s *stubInspector) Name() string { return s.name }

func (s *stubInspector) CheckUP(ctx context.Context) error {
	if s.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.err
}

// stubPinger satisfies both dbPinger and botPinger.
type stubPinger struct {
	err error
}

func (s *stubPinger) Ping(_ context.Context) error { return s.err }

func TestAgent_CheckUp(t *testing.T) {
	t.Parallel()

	t.Run("all healthy returns true and ok for each component", func(t *testing.T) {
		t.Parallel()
		agent := NewAgent(inspectorTimeout,
			&stubInspector{name: "db", err: nil},
			&stubInspector{name: "telegram", err: nil},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.True(t, healthy)
		assert.Equal(t, map[string]string{"db": "ok", "telegram": "ok"}, report)
	})

	t.Run("one failing returns false and error for that component, ok for others", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("db is dead")
		agent := NewAgent(inspectorTimeout,
			&stubInspector{name: "db", err: boom},
			&stubInspector{name: "telegram", err: nil},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.False(t, healthy)
		assert.Equal(t, "db is dead", report["db"])
		assert.Equal(t, "ok", report["telegram"])
	})

	t.Run("all failing returns false with all error messages", func(t *testing.T) {
		t.Parallel()
		agent := NewAgent(inspectorTimeout,
			&stubInspector{name: "db", err: errors.New("db gone")},
			&stubInspector{name: "telegram", err: errors.New("tg gone")},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.False(t, healthy)
		assert.Equal(t, "db gone", report["db"])
		assert.Equal(t, "tg gone", report["telegram"])
	})

	t.Run("no inspectors returns healthy with empty report", func(t *testing.T) {
		t.Parallel()
		agent := NewAgent(inspectorTimeout)
		healthy, report := agent.CheckUp(context.Background())

		assert.True(t, healthy)
		assert.Empty(t, report)
	})

	t.Run("inspector with empty name is skipped", func(t *testing.T) {
		t.Parallel()
		agent := NewAgent(inspectorTimeout,
			&stubInspector{name: "", err: errors.New("ignored")},
			&stubInspector{name: "db", err: nil},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.True(t, healthy)
		assert.Equal(t, map[string]string{"db": "ok"}, report)
		assert.NotContains(t, report, "", "empty-named inspector must be skipped")
	})

	t.Run("zero timeout falls back to default", func(t *testing.T) {
		t.Parallel()
		agent := NewAgent(0, &stubInspector{name: "db", err: nil})
		assert.Equal(t, inspectorTimeout, agent.timeout)
	})

	t.Run("negative timeout falls back to default", func(t *testing.T) {
		t.Parallel()
		agent := NewAgent(-time.Second, &stubInspector{name: "db", err: nil})
		assert.Equal(t, inspectorTimeout, agent.timeout)
	})

	t.Run("timeout bounds the sweep and blocking inspector returns context error", func(t *testing.T) {
		t.Parallel()
		const budget = 100 * time.Millisecond
		agent := NewAgent(budget, &stubInspector{name: "slow", block: true})

		start := time.Now()
		healthy, report := agent.CheckUp(context.Background())
		elapsed := time.Since(start)

		assert.False(t, healthy)
		assert.Less(t, elapsed, 5*budget, "sweep must complete within 5× budget")
		assert.Contains(t, report["slow"], "context", "blocking inspector must report a context error")
	})
}

func TestDBInspector(t *testing.T) {
	t.Parallel()

	t.Run("Name returns sqlite", func(t *testing.T) {
		t.Parallel()
		insp := NewDBInspector(&stubPinger{})
		assert.Equal(t, "sqlite", insp.Name())
	})

	t.Run("CheckUP delegates to Ping and returns nil on success", func(t *testing.T) {
		t.Parallel()
		insp := NewDBInspector(&stubPinger{err: nil})
		require.NoError(t, insp.CheckUP(context.Background()))
	})

	t.Run("CheckUP propagates error from Ping", func(t *testing.T) {
		t.Parallel()
		want := errors.New("db unreachable")
		insp := NewDBInspector(&stubPinger{err: want})
		err := insp.CheckUP(context.Background())
		require.ErrorIs(t, err, want)
	})
}

func TestTelegramInspector(t *testing.T) {
	t.Parallel()

	t.Run("Name returns telegram", func(t *testing.T) {
		t.Parallel()
		insp := NewTelegramInspector(&stubPinger{})
		assert.Equal(t, "telegram", insp.Name())
	})

	t.Run("CheckUP delegates to Ping and returns nil on success", func(t *testing.T) {
		t.Parallel()
		insp := NewTelegramInspector(&stubPinger{err: nil})
		require.NoError(t, insp.CheckUP(context.Background()))
	})

	t.Run("CheckUP propagates error from Ping", func(t *testing.T) {
		t.Parallel()
		want := errors.New("telegram unreachable")
		insp := NewTelegramInspector(&stubPinger{err: want})
		err := insp.CheckUP(context.Background())
		require.ErrorIs(t, err, want)
	})
}

func TestAgent_CheckUp_Advisory(t *testing.T) {
	t.Parallel()

	t.Run("advisory failure reported but healthy stays true when critical pass", func(t *testing.T) {
		t.Parallel()
		agent := NewAgentWithAdvisory(inspectorTimeout,
			[]Inspector{&stubInspector{name: "sqlite", err: nil}},
			[]Inspector{&stubInspector{name: "open-meteo", err: errors.New("open-meteo down")}},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.True(t, healthy, "advisory failure must not flip healthy")
		assert.Equal(t, "ok", report["sqlite"])
		assert.Equal(t, "open-meteo down", report["open-meteo"], "advisory error must appear in report")
	})

	t.Run("critical failure still sets healthy false even when advisory passes", func(t *testing.T) {
		t.Parallel()
		agent := NewAgentWithAdvisory(inspectorTimeout,
			[]Inspector{&stubInspector{name: "sqlite", err: errors.New("db dead")}},
			[]Inspector{&stubInspector{name: "open-meteo", err: nil}},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.False(t, healthy)
		assert.Equal(t, "db dead", report["sqlite"])
		assert.Equal(t, "ok", report["open-meteo"])
	})

	t.Run("both critical and advisory failing sets healthy false", func(t *testing.T) {
		t.Parallel()
		agent := NewAgentWithAdvisory(inspectorTimeout,
			[]Inspector{&stubInspector{name: "sqlite", err: errors.New("db dead")}},
			[]Inspector{&stubInspector{name: "open-meteo", err: errors.New("weather down")}},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.False(t, healthy)
		assert.Equal(t, "db dead", report["sqlite"])
		assert.Equal(t, "weather down", report["open-meteo"])
	})

	t.Run("all advisory passing returns healthy", func(t *testing.T) {
		t.Parallel()
		agent := NewAgentWithAdvisory(inspectorTimeout,
			[]Inspector{},
			[]Inspector{&stubInspector{name: "open-meteo", err: nil}},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.True(t, healthy)
		assert.Equal(t, "ok", report["open-meteo"])
	})

	t.Run("zero timeout falls back to default for NewAgentWithAdvisory", func(t *testing.T) {
		t.Parallel()
		agent := NewAgentWithAdvisory(0, nil, nil)
		assert.Equal(t, inspectorTimeout, agent.timeout)
	})
}

func TestOpenMeteoInspector(t *testing.T) {
	t.Parallel()

	t.Run("Name returns open-meteo", func(t *testing.T) {
		t.Parallel()
		insp := NewOpenMeteoInspector()
		assert.Equal(t, "open-meteo", insp.Name())
	})

	t.Run("CheckUP returns nil on 2xx with valid JSON", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"id":2950159,"name":"Berlin"}]}`))
		}))
		defer srv.Close()
		insp := newOpenMeteoInspectorForTest(srv.Client(), srv.URL)
		require.NoError(t, insp.CheckUP(context.Background()))
	})

	t.Run("CheckUP returns error on non-2xx", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":true}`, http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		insp := newOpenMeteoInspectorForTest(srv.Client(), srv.URL)
		err := insp.CheckUP(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "503")
	})

	t.Run("CheckUP returns error on malformed JSON", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`not json at all`))
		}))
		defer srv.Close()
		insp := newOpenMeteoInspectorForTest(srv.Client(), srv.URL)
		err := insp.CheckUP(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse JSON")
	})

	t.Run("advisory failure is non-gating in agent", func(t *testing.T) {
		t.Parallel()
		// Simulate Open-Meteo being down: the advisory inspector fails.
		agent := NewAgentWithAdvisory(inspectorTimeout,
			[]Inspector{&stubInspector{name: "sqlite", err: nil}},
			[]Inspector{&stubInspector{name: "open-meteo", err: errors.New("simulated outage")}},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.True(t, healthy, "Open-Meteo outage must not set healthy=false")
		assert.Equal(t, "simulated outage", report["open-meteo"])
		assert.Equal(t, "ok", report["sqlite"])
	})
}
