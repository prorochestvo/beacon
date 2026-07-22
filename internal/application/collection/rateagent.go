// Package collection implements the rate-collection loop that fetches live
// exchange rates from configured sources and persists them to the database.
package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/tools/rateextractor"
)

// NewRateAgent constructs a RateAgent. proxyURL may be empty to disable proxying.
// chromiumPath is the absolute path to the Chromium/Chrome binary for chromedp
// sources; pass empty to let chromedp search PATH. The chromedp extractor is
// constructed eagerly, but Chromium launches only on the first Run for a chromedp
// source, so plain-only deployments pay no startup cost.
func NewRateAgent(
	proxyURL string,
	chromiumPath string,
	rRateSource rateSourceRepository,
	rExecutionHistory executionHistoryRepository,
	rRateValue rateValueRepository,
	logger io.Writer,
) (*RateAgent, error) {
	plain, err := rateextractor.NewRateExtractor(rRateValue, proxyURL, time.Minute, logger)
	if err != nil {
		return nil, err
	}

	chromedpExt := rateextractor.NewChromedpRateExtractor(chromiumPath, proxyURL, logger, rRateValue)

	a := &RateAgent{
		rateValueRepository:        rRateValue,
		rateSourceRepository:       rRateSource,
		executionHistoryRepository: rExecutionHistory,
		plainExtractor:             plain,
		chromedpExtractor:          chromedpExt,
		logger:                     logger,
	}

	return a, nil
}

// RateAgent fetches rates for all active sources that are due, persisting results
// and execution history. It is designed to run to completion on each invocation.
//
// The chromedp slot uses a separate interface so a tick with multiple
// chromedp-kind sources can share one Chromium subprocess (RunBatch), while
// plain HTTP sources keep their per-source Run path.
type RateAgent struct {
	rateValueRepository        rateValueRepository
	rateSourceRepository       rateSourceRepository
	executionHistoryRepository executionHistoryRepository
	plainExtractor             rateExtractor
	chromedpExtractor          chromedpBatchExtractor
	logger                     io.Writer
}

type rateExtractor interface {
	Run(context.Context, *domain.RateSource) error
}

// chromedpBatchExtractor processes a slice of chromedp-kind sources under one
// shared Chromium subprocess. Implementations must return one entry per source
// keyed by source.Name; a nil or absent entry signals success. An empty batch
// must be a fast no-op so plain-only ticks do not pay a Chromium cold-start.
type chromedpBatchExtractor interface {
	RunBatch(context.Context, []*domain.RateSource) map[string]error
}

type executionHistoryRepository interface {
	RetainExecutionHistory(context.Context, *domain.ExecutionHistory) error
	ObtainLastNExecutionHistoryBySourceName(context.Context, string, int64, bool) ([]domain.ExecutionHistory, error)
}

type rateSourceRepository interface {
	ObtainRateSourceByName(context.Context, string) (*domain.RateSource, error)
	ObtainAllRateSources(context.Context) ([]domain.RateSource, error)
}

type rateValueRepository interface {
	ObtainLastNRateValuesBySourceName(context.Context, string, int64) ([]domain.RateValue, error)
	RetainRateValue(context.Context, *domain.RateValue) error
}

// Run fetches all active, due rate sources and stores the results.
// Returns a joined error containing all per-source failures; nil if all succeeded.
func (a *RateAgent) Run(ctx context.Context) (err error) {
	// isDue reports whether the source should run this invocation. The grace
	// period is interval/4 clamped to [30s, 1h], absorbing scheduling jitter
	// between sources sharing a declared interval (e.g. a 4h source fires at
	// elapsed ≥ 3h). With no successful execution history, the source is due.
	isDue := func(
		ctx context.Context,
		repository executionHistoryRepository,
		sourceName string,
		interval time.Duration,
		now time.Time,
	) bool {
		records, err := repository.ObtainLastNExecutionHistoryBySourceName(ctx, sourceName, 1, true)
		if err != nil || len(records) == 0 {
			return true
		}
		grace := interval >> 2
		grace = max(grace, 30*time.Second)
		grace = min(grace, time.Hour)
		return now.Sub(records[0].Timestamp) >= interval-grace
	}

	var sources []domain.RateSource
	if s, errSource := a.rateSourceRepository.ObtainAllRateSources(ctx); errSource != nil {
		errSource = errors.Join(errSource, loginjector.NewTraceError())
		return errSource
	} else if len(s) > 0 {
		now := time.Now().UTC()
		sources = make([]domain.RateSource, 0, len(s))
		for _, source := range s {
			if !source.Active {
				continue
			}
			interval, errInterval := time.ParseDuration(source.Interval)
			if errInterval != nil {
				errInterval = fmt.Errorf("invalid interval %q, %s", source.Interval, errInterval.Error())
				errInterval = errors.Join(errInterval, loginjector.NewTraceError())
				err = errors.Join(err, errInterval)
				continue
			}
			if !isDue(ctx, a.executionHistoryRepository, source.Name, interval, now) {
				continue
			}
			sources = append(sources, source)
		}
	}

	if sources == nil || len(sources) == 0 {
		return
	}

	errs := a.execution(ctx, sources)

	combined := make([]error, 0, len(errs))
	for k, e := range errs {
		if e != nil {
			combined = append(combined, fmt.Errorf("source %s: %w", k, e))
		}
	}
	return errors.Join(err, errors.Join(combined...))
}

func (a *RateAgent) execution(ctx context.Context, sources []domain.RateSource) map[string]error {
	now := time.Now().UTC()
	errs := make(map[string]error, len(sources))

	// Partition by fetcher_kind so chromedp sources share one Chromium
	// subprocess via RunBatch. The persistence loop below preserves the original
	// source order regardless of partitioning.
	sourceErrs := make(map[string]error, len(sources))
	var chromedpBatch []*domain.RateSource
	for i := range sources {
		s := &sources[i]
		switch s.FetcherKind {
		case "", "plain":
			sourceErrs[s.Name] = a.plainExtractor.Run(ctx, s)
		case "chromedp":
			chromedpBatch = append(chromedpBatch, s)
		default:
			err := fmt.Errorf("source %q: unsupported fetcher_kind %q", s.Name, s.FetcherKind)
			sourceErrs[s.Name] = errors.Join(err, loginjector.NewTraceError())
		}
	}

	// chromedp sources run as one batch under a shared allocator.
	if len(chromedpBatch) > 0 {
		batchResults := a.chromedpExtractor.RunBatch(ctx, chromedpBatch)
		for _, s := range chromedpBatch {
			sourceErrs[s.Name] = batchResults[s.Name]
		}
	}

	// Persistence runs under context.Background() so SIGTERM (cancelling the
	// agent's ctx) does not drop execution_history rows for sources that already
	// finished fetching. Dropping them would force the next tick to re-fetch
	// everything that completed under the dying process, losing cron's
	// observability into what actually ran.
	persistCtx := context.Background()
	for _, source := range sources {
		h := &domain.ExecutionHistory{
			SourceName: source.Name,
			Success:    true,
			Timestamp:  now,
		}
		runErr := sourceErrs[source.Name]
		if runErr != nil {
			h.Success = false
			h.Error = errors.Join(runErr, loginjector.NewTraceError()).Error()
		}

		retainErr := a.executionHistoryRepository.RetainExecutionHistory(persistCtx, h)
		combined := errors.Join(runErr, retainErr)
		if combined != nil {
			errs[source.Name] = errors.Join(errs[source.Name], errors.Join(combined, loginjector.NewTraceError()))
		}
	}

	return errs
}
