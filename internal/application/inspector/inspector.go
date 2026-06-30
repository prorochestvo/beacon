// Package inspector implements the health-check inspector pattern for the web binary.
// Each external dependency registers itself as an Inspector; the Agent aggregates their
// results under a single bounded context timeout so one slow or dead dependency cannot
// hang the endpoint or hide the status of the others.
package inspector

import (
	"context"
	"time"
)

// inspectorTimeout is the whole-sweep budget for a single Agent.CheckUp call.
// Any dependency that does not respond within this window is reported as failing.
const inspectorTimeout = 3 * time.Second

// Inspector is the contract every health-checked dependency must satisfy.
// Name returns a stable, unique label shown in the /health/check report.
// CheckUP performs a real, cheap, read-only probe and returns nil on success
// or the failure reason as an error.
type Inspector interface {
	Name() string
	CheckUP(ctx context.Context) error
}

// inspectorEntry pairs an Inspector with an advisory flag. An advisory inspector's
// failure is reported in the /health/check component map but does not flip the
// aggregate healthy flag, so a third-party outage cannot fail the deploy health-gate.
type inspectorEntry struct {
	inspector Inspector
	advisory  bool
}

// Agent runs all registered inspectors under a single bounded context timeout.
// Inspectors registered as critical (the default) flip the aggregate healthy flag
// on failure; advisory inspectors are reported but do not affect the aggregate.
type Agent struct {
	entries []inspectorEntry
	timeout time.Duration
}

// NewAgent constructs an Agent where all inspectors are critical.
// When timeout is zero or negative, inspectorTimeout (3 s) is used.
func NewAgent(timeout time.Duration, inspectors ...Inspector) *Agent {
	if timeout <= 0 {
		timeout = inspectorTimeout
	}
	entries := make([]inspectorEntry, len(inspectors))
	for i, insp := range inspectors {
		entries[i] = inspectorEntry{inspector: insp, advisory: false}
	}
	return &Agent{entries: entries, timeout: timeout}
}

// NewAgentWithAdvisory constructs an Agent with separate critical and advisory
// inspector slices. Critical inspector failures set healthy=false; advisory
// inspector failures appear in the report but leave healthy unaffected.
// When timeout is zero or negative, inspectorTimeout (3 s) is used.
func NewAgentWithAdvisory(timeout time.Duration, critical []Inspector, advisory []Inspector) *Agent {
	if timeout <= 0 {
		timeout = inspectorTimeout
	}
	entries := make([]inspectorEntry, 0, len(critical)+len(advisory))
	for _, insp := range critical {
		entries = append(entries, inspectorEntry{inspector: insp, advisory: false})
	}
	for _, insp := range advisory {
		entries = append(entries, inspectorEntry{inspector: insp, advisory: true})
	}
	return &Agent{entries: entries, timeout: timeout}
}

// CheckUp probes every registered inspector under a single deadline and returns a
// per-component report. One slow or failing check never prevents the others from
// running. Returns healthy=true iff every critical inspector returned nil; advisory
// failures appear in the report but do not set healthy=false. The report maps each
// inspector's Name() to "ok" or the verbatim error message.
func (a *Agent) CheckUp(ctx context.Context) (healthy bool, report map[string]string) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	healthy = true
	report = make(map[string]string, len(a.entries))
	for _, entry := range a.entries {
		name := entry.inspector.Name()
		if name == "" {
			continue
		}
		if err := entry.inspector.CheckUP(ctx); err != nil {
			report[name] = err.Error()
			if !entry.advisory {
				healthy = false
			}
			continue
		}
		report[name] = "ok"
	}
	return healthy, report
}

// dbPinger is the subset of *sqlitedb.SQLiteClient used by DBInspector.
// Defined here so tests can substitute a fake without importing the concrete type.
type dbPinger interface {
	Ping(ctx context.Context) error
}

// DBInspector wraps a SQLite client and adapts it to the Inspector interface.
// It delegates to the client's Ping method, which performs a PingContext followed
// by a SELECT 1 inside a rolled-back transaction.
type DBInspector struct {
	client dbPinger
}

// NewDBInspector returns an Inspector backed by the given SQLite client.
func NewDBInspector(client dbPinger) *DBInspector {
	return &DBInspector{client: client}
}

// Name returns the label used in the /health/check report.
func (d *DBInspector) Name() string { return "sqlite" }

// CheckUP delegates to the underlying Ping.
func (d *DBInspector) CheckUP(ctx context.Context) error {
	return d.client.Ping(ctx)
}

// botPinger is the subset of *telegrambot.TelegramBotClient used by TelegramInspector.
// Defined here so tests can substitute a fake without importing the concrete type.
type botPinger interface {
	Ping(ctx context.Context) error
}

// TelegramInspector wraps a Telegram bot client and adapts it to the Inspector interface.
// It delegates to the client's Ping method, which calls GetMe and asserts a non-zero
// bot ID. Note: the underlying tgbotapi.BotAPI call does not honour the context, so
// the probe is bounded at the Agent sweep level rather than the individual HTTP call.
type TelegramInspector struct {
	client botPinger
}

// NewTelegramInspector returns an Inspector backed by the given Telegram bot client.
func NewTelegramInspector(client botPinger) *TelegramInspector {
	return &TelegramInspector{client: client}
}

// Name returns the label used in the /health/check report.
func (t *TelegramInspector) Name() string { return "telegram" }

// CheckUP delegates to the underlying Ping.
func (t *TelegramInspector) CheckUP(ctx context.Context) error {
	return t.client.Ping(ctx)
}
