// Package service implements the application-layer business logic consumed by the
// gateway layer (HTTP handlers and Telegram bot). All methods return plain errors;
// the gateway maps them to the generic fallback message per the project's
// error-handling contract in CLAUDE.md.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
)

// NewRateRestAPI returns a RateRestApi wired to the given repository implementations.
func NewRateRestAPI(
	rExecutionHistory executionHistoryRepository,
	rRateSource rateSourceRepository,
	rRateValue rateValueRepository,
	rRateUserSubscription rateUserSubscriptionRepository,
	rRateUserEvent rateUserEventRepository,
) (*RateRestApi, error) {

	h := &RateRestApi{
		executionHistoryRepository:     rExecutionHistory,
		rateSourceRepository:           rRateSource,
		rateValueRepository:            rRateValue,
		rateUserSubscriptionRepository: rRateUserSubscription,
		rateUserEventRepository:        rRateUserEvent,
	}

	return h, nil
}

// RateRestApi groups all v1 HTTP handlers and their repository dependencies.
type RateRestApi struct {
	executionHistoryRepository     executionHistoryRepository
	rateSourceRepository           rateSourceRepository
	rateValueRepository            rateValueRepository
	rateUserSubscriptionRepository rateUserSubscriptionRepository
	rateUserEventRepository        rateUserEventRepository
}

type executionHistoryRepository interface {
	ObtainLastNExecutionHistoryBySourceName(context.Context, string, int64, bool) ([]domain.ExecutionHistory, error)
	ObtainLatestExecutionHistoryBySources(context.Context, []string) (map[string]domain.ExecutionHistory, error)
	ObtainExecutionHistoryErrorCount(context.Context) (int64, error)
	ObtainLastNExecutionHistoryErrors(context.Context, int64, int64) ([]domain.ExecutionHistory, error)
}

type rateSourceRepository interface {
	CheckUP(context.Context) error
	ObtainAllRateSources(context.Context) ([]domain.RateSource, error)
	ObtainRateSourceByName(context.Context, string) (*domain.RateSource, error)
	RetainRateSource(context.Context, *domain.RateSource) error
}

type rateValueRepository interface {
	ObtainLastNRateValuesBySourceName(context.Context, string, int64) ([]domain.RateValue, error)
}

type rateUserSubscriptionRepository interface {
	ObtainRateUserSubscriptionsBySource(context.Context, string) ([]domain.RateUserSubscription, error)
	ObtainSubscriptionSummaryBySource(context.Context, string) ([]domain.RateUserSubscriptionSummary, error)
	ObtainRateUserSubscriptionsBySourcePaged(context.Context, string, int64, int64) ([]domain.RateUserSubscriptionDetail, error)
}

type rateUserEventRepository interface {
	ObtainLastNRateUserEvents(context.Context, int64, int64, ...domain.RateUserEventStatus) ([]domain.RateUserEvent, error)
	ObtainRateUserEventsBySourceName(context.Context, string, int64, int64, ...domain.RateUserEventStatus) ([]domain.RateUserEvent, error)
	ObtainDailyEventSummaryBySource(context.Context, string, int64, int64) ([]domain.RateUserEventDailySummary, error)
}

// ObtainLastNExecutionHistoryBySourceName returns the most recent limit execution history records
// for the given source name, ordered newest-first.
func (h *RateRestApi) ObtainLastNExecutionHistoryBySourceName(ctx context.Context, name string, limit int64) ([]domain.ExecutionHistory, error) {
	items, err := h.executionHistoryRepository.ObtainLastNExecutionHistoryBySourceName(ctx, name, limit, false)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return items, nil
}

// CheckUP runs a cheap rate_sources read to confirm the database is reachable.
// Used by the /healthz endpoint.
func (h *RateRestApi) CheckUP(ctx context.Context) error {
	if err := h.rateSourceRepository.CheckUP(ctx); err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// ObtainLatestExecutionHistoryBySources returns the most recent execution_history
// row per source, keyed by source name; sources with no rows are absent from the
// map. Used by ListSources to replace the per-source N+1 with one bulk read.
func (h *RateRestApi) ObtainLatestExecutionHistoryBySources(ctx context.Context, names []string) (map[string]domain.ExecutionHistory, error) {
	items, err := h.executionHistoryRepository.ObtainLatestExecutionHistoryBySources(ctx, names)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}

// ObtainLastSuccessNExecutionHistoryBySourceName returns the most recent limit successful
// execution history records for the given source name, ordered newest-first.
func (h *RateRestApi) ObtainLastSuccessNExecutionHistoryBySourceName(ctx context.Context, name string, limit int64) ([]domain.ExecutionHistory, error) {
	items, err := h.executionHistoryRepository.ObtainLastNExecutionHistoryBySourceName(ctx, name, limit, true)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return items, nil
}

// UpdateRateSourceActive enables or disables the named rate source. Wraps
// internal.ErrNotFound when the source does not exist, so callers can distinguish
// 404 from 500 via errors.Is.
func (h *RateRestApi) UpdateRateSourceActive(ctx context.Context, name string, active bool) error {
	src, err := h.rateSourceRepository.ObtainRateSourceByName(ctx, name)
	if err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	if src == nil {
		return errors.Join(
			fmt.Errorf("rate source %q: %w", name, internal.ErrNotFound),
			internal.NewTraceError(),
		)
	}

	src.Active = active

	err = h.rateSourceRepository.RetainRateSource(ctx, src)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	return nil
}

// ObtainAllRateSources returns all configured rate sources.
func (h *RateRestApi) ObtainAllRateSources(ctx context.Context) ([]domain.RateSource, error) {
	items, err := h.rateSourceRepository.ObtainAllRateSources(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return items, nil
}

// ObtainLastNRateValuesBySourceName returns the most recent limit rate values for the given source.
func (h *RateRestApi) ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error) {
	items, err := h.rateValueRepository.ObtainLastNRateValuesBySourceName(
		ctx,
		name,
		limit,
	)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return items, nil
}

// ObtainListOfLastRateUserEvent returns the most recent limit notification events across all statuses.
func (h *RateRestApi) ObtainListOfLastRateUserEvent(ctx context.Context, limit int64) ([]domain.RateUserEvent, error) {
	items, err := h.rateUserEventRepository.ObtainLastNRateUserEvents(
		ctx,
		0,
		limit,
		domain.RateUserEventStatusPending,
		domain.RateUserEventStatusSent,
		domain.RateUserEventStatusFailed,
		domain.RateUserEventStatusCanceled,
	)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return items, nil
}

// ObtainFailedListOfRateUserEvent returns paginated failed notification events.
func (h *RateRestApi) ObtainFailedListOfRateUserEvent(ctx context.Context, offset, limit int64) ([]domain.RateUserEvent, error) {
	items, err := h.rateUserEventRepository.ObtainLastNRateUserEvents(
		ctx,
		offset,
		limit,
		domain.RateUserEventStatusFailed,
	)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return items, nil
}

// ObtainPendingRateUserEvents returns up to 1000 pending events (default-page widget).
func (h *RateRestApi) ObtainPendingRateUserEvents(ctx context.Context) ([]domain.RateUserEvent, error) {
	items, err := h.rateUserEventRepository.ObtainLastNRateUserEvents(
		ctx, 0, 1000, domain.RateUserEventStatusPending,
	)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}

// ObtainFailedRateUserEventsBySourceName returns a single page of failed events for a source.
func (h *RateRestApi) ObtainFailedRateUserEventsBySourceName(ctx context.Context, sourceName string, page, pageSize int64) ([]domain.RateUserEvent, error) {
	offset := (page - 1) * pageSize
	items, err := h.rateUserEventRepository.ObtainRateUserEventsBySourceName(
		ctx, sourceName, offset, pageSize, domain.RateUserEventStatusFailed,
	)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}

// ObtainStats returns global source and error counts.
func (h *RateRestApi) ObtainStats(ctx context.Context) (domain.StatsResult, error) {
	sources, err := h.rateSourceRepository.ObtainAllRateSources(ctx)
	if err != nil {
		return domain.StatsResult{}, errors.Join(err, internal.NewTraceError())
	}
	var active int64
	for _, s := range sources {
		if s.Active {
			active++
		}
	}
	errCount, err := h.executionHistoryRepository.ObtainExecutionHistoryErrorCount(ctx)
	if err != nil {
		return domain.StatsResult{}, errors.Join(err, internal.NewTraceError())
	}
	return domain.StatsResult{
		SourcesTotal:  int64(len(sources)),
		SourcesActive: active,
		ErrorsTotal:   errCount,
	}, nil
}

// ObtainRateUserSubscriptionsBySourcePaged returns a page of subscription details for a source.
func (h *RateRestApi) ObtainRateUserSubscriptionsBySourcePaged(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserSubscriptionDetail, error) {
	items, err := h.rateUserSubscriptionRepository.ObtainRateUserSubscriptionsBySourcePaged(ctx, sourceName, offset, limit)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}

// ObtainDailyEventSummaryBySource returns per-day aggregated event counts for a source.
func (h *RateRestApi) ObtainDailyEventSummaryBySource(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserEventDailySummary, error) {
	items, err := h.rateUserEventRepository.ObtainDailyEventSummaryBySource(ctx, sourceName, offset, limit)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}

// ObtainLastNExecutionHistoryErrors returns the most recent failed execution history records.
func (h *RateRestApi) ObtainLastNExecutionHistoryErrors(ctx context.Context, offset, limit int64) ([]domain.ExecutionHistory, error) {
	items, err := h.executionHistoryRepository.ObtainLastNExecutionHistoryErrors(ctx, offset, limit)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}

// ObtainSubscriptionSummaryBySource returns grouped subscription + event statistics for a source.
func (h *RateRestApi) ObtainSubscriptionSummaryBySource(ctx context.Context, sourceName string) ([]domain.RateUserSubscriptionSummary, error) {
	items, err := h.rateUserSubscriptionRepository.ObtainSubscriptionSummaryBySource(ctx, sourceName)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return items, nil
}
