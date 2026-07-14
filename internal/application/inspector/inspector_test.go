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

func TestGismeteoInspector_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("Name returns gismeteo", func(t *testing.T) {
		t.Parallel()
		insp := NewGismeteoInspector()
		assert.Equal(t, "gismeteo", insp.Name())
	})

	t.Run("CheckUP returns nil on 200", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		insp := newGismeteoInspectorForTest(srv.Client(), srv.URL)
		require.NoError(t, insp.CheckUP(context.Background()))
	})

	t.Run("CheckUP returns nil on 301 redirect (reachable)", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Serve 301 with no Location header. Without a custom CheckRedirect the
			// default client would try to follow the redirect and fail because there is
			// no Location target to redirect to. ErrUseLastResponse stops the client
			// at the 301 so CheckUP receives the status directly and evaluates it.
			w.WriteHeader(http.StatusMovedPermanently)
		}))
		defer srv.Close()
		client := &http.Client{
			Timeout: gismeteoProbeTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		insp := newGismeteoInspectorForTest(client, srv.URL)
		require.NoError(t, insp.CheckUP(context.Background()))
	})

	t.Run("CheckUP returns nil on 403 (reachable, bot-fence)", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		insp := newGismeteoInspectorForTest(srv.Client(), srv.URL)
		require.NoError(t, insp.CheckUP(context.Background()))
	})

	t.Run("CheckUP returns error on 500", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}))
		defer srv.Close()
		insp := newGismeteoInspectorForTest(srv.Client(), srv.URL)
		err := insp.CheckUP(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("CheckUP returns error on 503", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		insp := newGismeteoInspectorForTest(srv.Client(), srv.URL)
		err := insp.CheckUP(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "503")
	})

	t.Run("CheckUP returns error on transport failure", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		srv.Close() // close before the request so the transport fails
		insp := newGismeteoInspectorForTest(srv.Client(), srv.URL)
		err := insp.CheckUP(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gismeteo health")
	})

	t.Run("request carries the configured User-Agent", func(t *testing.T) {
		t.Parallel()
		var gotUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUA = r.Header.Get("User-Agent")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		insp := newGismeteoInspectorForTest(srv.Client(), srv.URL)
		require.NoError(t, insp.CheckUP(context.Background()))
		assert.Equal(t, gismeteoInspectorUA, gotUA, "User-Agent must match gismeteoInspectorUA")
	})

	t.Run("advisory gismeteo failure does not flip aggregate healthy", func(t *testing.T) {
		t.Parallel()
		agent := NewAgentWithAdvisory(inspectorTimeout,
			[]Inspector{&stubInspector{name: "sqlite", err: nil}},
			[]Inspector{&stubInspector{name: "gismeteo", err: errors.New("gismeteo down")}},
		)
		healthy, report := agent.CheckUp(context.Background())

		assert.True(t, healthy, "advisory gismeteo failure must not set healthy=false")
		assert.Equal(t, "gismeteo down", report["gismeteo"])
		assert.Equal(t, "ok", report["sqlite"])
	})

	t.Run("NewGismeteoInspectorWithURL probes the configured base URL", func(t *testing.T) {
		t.Parallel()
		var hit bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hit = true
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		insp := NewGismeteoInspectorWithURL(srv.URL)
		require.NoError(t, insp.CheckUP(context.Background()))
		assert.True(t, hit, "probe must hit the configured base URL")
	})

	t.Run("NewGismeteoInspectorWithURL empty base URL falls back to default probe URL", func(t *testing.T) {
		t.Parallel()
		insp := NewGismeteoInspectorWithURL("")
		assert.Equal(t, gismeteoProbeURL, insp.probeURL,
			"an empty base_url must keep the compiled-in default probe URL")
	})
}
