package notification

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

const (
	// DefaultSourceStaleFactor multiplies a source's own declared interval to get the
	// silence it is allowed before it counts as broken.
	//
	// Relative rather than absolute because sources declare their own cadence: a source
	// polled every 10 minutes is dead after two hours, one polled every 6 hours is not.
	// Three means two missed cycles plus slack — late enough that ordinary jitter and one
	// skipped run stay quiet, early enough to be told the same day rather than, as the
	// issue found, by grepping the log weeks later.
	DefaultSourceStaleFactor = 3

	// DefaultSourceStaleFloor is the shortest silence that can ever count as broken,
	// whatever the interval says.
	//
	// The collector is cron-driven, so a source cannot actually be attempted more often
	// than the collector runs. Without a floor, a source configured far below that cadence
	// would be permanently "stale" against a threshold it was never able to meet.
	DefaultSourceStaleFloor = 3 * time.Hour
)

// NewSourceHealthAgent constructs a SourceHealthAgent. All repositories are required;
// staleFactor and staleFloor fall back to the defaults when not positive.
func NewSourceHealthAgent(
	sourceRepo sourceHealthSourceRepository,
	historyRepo sourceHealthHistoryRepository,
	healthRepo sourceHealthStateRepository,
	eventRepo rateCheckEventRepository,
	adminChatID int64,
	staleFactor int,
	staleFloor time.Duration,
	logger io.Writer,
) (*SourceHealthAgent, error) {
	if sourceRepo == nil || historyRepo == nil || healthRepo == nil || eventRepo == nil {
		return nil, errors.New("source health agent: sourceRepo, historyRepo, healthRepo and eventRepo are all required")
	}
	if adminChatID == 0 {
		// Without somewhere to send them the agent would compute alerts and drop them,
		// which is the failure mode it exists to remove.
		return nil, errors.New("source health agent: admin chat id is required")
	}
	if staleFactor <= 0 {
		staleFactor = DefaultSourceStaleFactor
	}
	if staleFloor <= 0 {
		staleFloor = DefaultSourceStaleFloor
	}
	if logger == nil {
		logger = io.Discard
	}
	return &SourceHealthAgent{
		sourceRepo:  sourceRepo,
		historyRepo: historyRepo,
		healthRepo:  healthRepo,
		eventRepo:   eventRepo,
		adminChatID: adminChatID,
		staleFactor: staleFactor,
		staleFloor:  staleFloor,
		now:         time.Now,
		logger:      logger,
	}, nil
}

// SourceHealthAgent tells the operator when a rate source has stopped producing data, and
// when it starts again.
//
// A source could previously fail on every run for weeks in complete silence: the per-run
// tombstone that stops one dead source from burning the whole collection run also stopped
// anyone from hearing about it, and the only way to find out was to grep the collector log
// by hand. For a service whose entire purpose is monitoring, silently losing an input is
// the worst available failure mode.
//
// Health is measured as **silence**, not as a failure count. A source that runs and fails
// writes failure rows, and a counter would catch it — but a source that stops being
// attempted at all writes nothing, so its counter stays at zero while it is exactly as
// dead. Time since the last success catches both. The failure count still rides in the
// message, where it is what separates "failing loudly" from "no longer running".
//
// Alerts are edge-triggered through rate_source_health, the same shape the weather
// subscriptions use: one message when a source goes bad, one when it comes back, nothing
// in between. Repeating the alert every run would replace an unnoticed silence with an
// ignored alarm, which is not an improvement.
type SourceHealthAgent struct {
	sourceRepo  sourceHealthSourceRepository
	historyRepo sourceHealthHistoryRepository
	healthRepo  sourceHealthStateRepository
	eventRepo   rateCheckEventRepository
	adminChatID int64
	staleFactor int
	staleFloor  time.Duration
	now         func() time.Time
	logger      io.Writer
}

// Run evaluates every active source and queues a message for each transition.
//
// It continues past a per-source failure rather than aborting: one source whose alert
// could not be queued must not silence the rest, which is the same reasoning that put the
// tombstone in the collector in the first place. Errors are joined and returned once the
// sweep is done.
func (a *SourceHealthAgent) Run(ctx context.Context) (SourceHealthReport, error) {
	var report SourceHealthReport

	sources, err := a.sourceRepo.ObtainAllRateSources(ctx)
	if err != nil {
		return report, errors.Join(internal.ErrAgentAborted,
			fmt.Errorf("source health: read sources: %w", err), loginjector.NewTraceError())
	}

	health, err := a.historyRepo.ObtainSourceCollectionHealth(ctx)
	if err != nil {
		return report, errors.Join(internal.ErrAgentAborted,
			fmt.Errorf("source health: read collection history: %w", err), loginjector.NewTraceError())
	}
	byName := make(map[string]domain.SourceCollectionHealth, len(health))
	for _, h := range health {
		byName[h.SourceName] = h
	}

	alerted, err := a.healthRepo.ObtainAlertedSources(ctx)
	if err != nil {
		return report, errors.Join(fmt.Errorf("source health: read alert state: %w", err), loginjector.NewTraceError())
	}

	now := a.now().UTC()
	var errs []error

	for _, source := range sources {
		if !source.Active {
			// An inactive source is not collected on purpose. Alerting that it stopped
			// producing data would report the configuration back to the operator who set it.
			continue
		}

		state, known := byName[source.Name]
		if !known || !state.HasRun() {
			// Never attempted. It has not failed — it has not started. A newly added
			// source must not announce itself as broken before its first cycle.
			continue
		}

		threshold, thresholdErr := a.threshold(source)
		if thresholdErr != nil {
			errs = append(errs, thresholdErr)
			continue
		}

		// A source that has never succeeded is judged from its first attempt: it is
		// broken, not unknown, and treating a zero timestamp as "recent" would hide the
		// worst case behind the best-looking value.
		since := state.LastSuccessAt
		if since.IsZero() {
			since = state.LastRunAt
		}
		unhealthy := now.Sub(since) > threshold

		_, wasAlerted := alerted[source.Name]
		switch {
		case unhealthy && !wasAlerted:
			name, text := renderSourceDown(source, state, now, threshold)
			if err = a.announce(ctx, name, text); err != nil {
				errs = append(errs, err)
				continue
			}
			if err = a.healthRepo.RetainAlertedSource(ctx, source.Name, now); err != nil {
				errs = append(errs, errors.Join(fmt.Errorf("source health: latch %s: %w", source.Name, err), loginjector.NewTraceError()))
				continue
			}
			report.Alerted = append(report.Alerted, source.Name)

		case !unhealthy && wasAlerted:
			name, text := renderSourceRecovered(source, state, now)
			if err = a.announce(ctx, name, text); err != nil {
				errs = append(errs, err)
				continue
			}
			if err = a.healthRepo.RemoveAlertedSource(ctx, source.Name); err != nil {
				errs = append(errs, errors.Join(fmt.Errorf("source health: clear latch %s: %w", source.Name, err), loginjector.NewTraceError()))
				continue
			}
			report.Recovered = append(report.Recovered, source.Name)
		}
	}

	// Stable order so the log line and the tests read the same way every run.
	sort.Strings(report.Alerted)
	sort.Strings(report.Recovered)

	if len(report.Alerted) > 0 {
		fmt.Fprintf(a.logger, "source health: %d source(s) went silent: %v\n", len(report.Alerted), report.Alerted)
	}
	if len(report.Recovered) > 0 {
		fmt.Fprintf(a.logger, "source health: %d source(s) recovered: %v\n", len(report.Recovered), report.Recovered)
	}

	return report, errors.Join(errs...)
}

// threshold is how long this source may stay silent before it counts as broken.
func (a *SourceHealthAgent) threshold(source domain.RateSource) (time.Duration, error) {
	interval, err := time.ParseDuration(source.Interval)
	if err != nil {
		return 0, errors.Join(
			fmt.Errorf("source health: source %s has an invalid interval %q: %w", source.Name, source.Interval, err),
			loginjector.NewTraceError(),
		)
	}
	scaled := interval * time.Duration(a.staleFactor)
	if scaled < a.staleFloor {
		return a.staleFloor, nil
	}
	return scaled, nil
}

// announce queues one message for the admin chat.
//
// It goes through the ordinary notification pool rather than calling Telegram directly, so
// an operator alert gets the same persistence, retry and failure audit as a user's — and
// so this agent needs no Telegram client of its own.
func (a *SourceHealthAgent) announce(ctx context.Context, sourceName, message string) error {
	event := &domain.RateUserEvent{
		SourceName: sourceName,
		UserID:     strconv.FormatInt(a.adminChatID, 10),
		Message:    message,
		Status:     domain.RateUserEventStatusPending,
	}
	if err := a.eventRepo.RetainRateUserEvent(ctx, event); err != nil {
		return errors.Join(fmt.Errorf("source health: queue alert for %s: %w", sourceName, err), loginjector.NewTraceError())
	}
	return nil
}

// SourceHealthReport is what one evaluation decided.
type SourceHealthReport struct {
	// Alerted names the sources that just went bad.
	Alerted []string
	// Recovered names the sources that just came back.
	Recovered []string
}

// sourceHealthSourceRepository supplies the sources to judge and their declared cadence.
type sourceHealthSourceRepository interface {
	ObtainAllRateSources(ctx context.Context) ([]domain.RateSource, error)
}

// sourceHealthHistoryRepository supplies the collection outcomes health is derived from.
type sourceHealthHistoryRepository interface {
	ObtainSourceCollectionHealth(ctx context.Context) ([]domain.SourceCollectionHealth, error)
}

// sourceHealthStateRepository holds which sources have already been alerted about.
type sourceHealthStateRepository interface {
	ObtainAlertedSources(ctx context.Context) (map[string]time.Time, error)
	RetainAlertedSource(ctx context.Context, sourceName string, at time.Time) error
	RemoveAlertedSource(ctx context.Context, sourceName string) error
}
