// Package collection implements the rate-collection loop that fetches live
// exchange rates from configured sources and persists them to the database.
package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/tools/rateextractor"
)

// NewRateAgent constructs a RateAgent. proxyURL may be empty to disable proxying.
// chromiumPath is the absolute path to the Chromium/Chrome binary used for
// chromedp sources; pass an empty string to let chromedp search PATH.
// The chromedp extractor is constructed eagerly but Chromium is only launched on
// the first Run call for a chromedp source, so a plain-only deployment pays no
// startup cost from Chromium being absent.
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

	chromedpExt := rateextractor.NewChromedpRateExtractor(chromiumPath, logger, rRateValue)

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
type RateAgent struct {
	rateValueRepository        rateValueRepository
	rateSourceRepository       rateSourceRepository
	executionHistoryRepository executionHistoryRepository
	plainExtractor             rateExtractor
	chromedpExtractor          rateExtractor
	logger                     io.Writer
}

// Run fetches all active, due rate sources and stores the results.
// Returns a joined error containing all per-source failures; nil if all succeeded.
func (a *RateAgent) Run(ctx context.Context) (err error) {
	// isDue returns true if the source should run in this invocation.
	// The grace period is interval/4, clamped to [30s, 1h], to absorb scheduling
	// jitter between sources that share the same declared interval. For example, a
	// 4h source uses a 1h grace (fires when elapsed ≥ 3h) so that two sources whose
	// last runs drifted by several minutes still land in the same invocation.
	// Short intervals (≤ 2m) keep the original 30s grace unchanged.
	// If no successful execution history exists, the source is always considered due.
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
		errSource = errors.Join(errSource, internal.NewTraceError())
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
				errInterval = errors.Join(errInterval, internal.NewTraceError())
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

	for _, source := range sources {
		h := &domain.ExecutionHistory{
			SourceName: source.Name,
			Success:    true,
			Timestamp:  now,
		}

		var err error
		switch source.FetcherKind {
		case "", "plain":
			err = a.plainExtractor.Run(ctx, &source)
		case "chromedp":
			err = a.chromedpExtractor.Run(ctx, &source)
		default:
			err = fmt.Errorf("source %q: unsupported fetcher_kind %q", source.Name, source.FetcherKind)
			err = errors.Join(err, internal.NewTraceError())
		}

		if err != nil {
			h.Success = false
			h.Error = errors.Join(err, internal.NewTraceError()).Error()
		}

		err = errors.Join(err, a.executionHistoryRepository.RetainExecutionHistory(ctx, h))
		if err != nil {
			err = errors.Join(err, internal.NewTraceError())
			errs[source.Name] = errors.Join(errs[source.Name], err)
		}
	}

	return errs
}

type rateExtractor interface {
	Run(context.Context, *domain.RateSource) error
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
