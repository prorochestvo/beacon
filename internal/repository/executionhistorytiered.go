package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// executionHistoryTierReader is the read surface the tiered repository needs from each
// tier. Both are satisfied by *ExecutionHistoryRepository — the archive's
// execution_history carries the hot schema's columns, so the same queries run against
// either database.
type executionHistoryTierReader interface {
	ObtainLastNExecutionHistoryBySourceName(
		ctx context.Context, sourceName string, limit int64, successOnly bool,
	) ([]domain.ExecutionHistory, error)
	ObtainLastNExecutionHistoryBySourceNameBefore(
		ctx context.Context, sourceName string, before time.Time, beforeID string, limit int64, successOnly bool,
	) ([]domain.ExecutionHistory, error)
	ObtainLatestExecutionHistoryBySources(
		ctx context.Context, sourceNames []string,
	) (map[string]domain.ExecutionHistory, error)
	ObtainExecutionHistoryErrorCount(ctx context.Context) (int64, error)
	ObtainLastNExecutionHistoryErrors(ctx context.Context, offset, limit int64) ([]domain.ExecutionHistory, error)
}

// NewTieredExecutionHistoryRepository routes execution-history reads across the hot and
// archive databases.
//
// Both tiers are required, for the same reason the rate-value tiered reader requires
// both: a deployment without an archive passes its *ExecutionHistoryRepository straight
// to the services, and folding that case in here would mean accepting a nil archive —
// a typed-nil pointer in an interface is not nil, so the check would pass and the first
// archive read would panic.
func NewTieredExecutionHistoryRepository(
	hot, archive executionHistoryTierReader,
) (*TieredExecutionHistoryRepository, error) {
	if hot == nil || archive == nil {
		return nil, errors.New("tiered execution history: hot and archive readers are both required")
	}
	return &TieredExecutionHistoryRepository{hot: hot, archive: archive}, nil
}

// TieredExecutionHistoryRepository answers execution-history reads from whichever tier
// holds what is being asked for.
//
// Unlike the rate-value chart there is no time window to split on here — the callers ask
// for a row count, the latest row per source, or every failure ever recorded. So the
// split is by question rather than by horizon: anything about recent state stays on the
// hot tier, anything unbounded goes to the archive, and the one row-counted read tops up
// across the seam.
type TieredExecutionHistoryRepository struct {
	hot     executionHistoryTierReader
	archive executionHistoryTierReader
}

// ObtainLatestExecutionHistoryBySources reads the hot tier alone.
//
// It returns at most one row per source and every one of them is by definition the most
// recent, so the hot tier always has it — and reading it there rather than from the
// archive keeps the source list free of reconciliation lag, which is exactly the column
// an operator refreshes to see whether the last tick worked.
func (r *TieredExecutionHistoryRepository) ObtainLatestExecutionHistoryBySources(
	ctx context.Context, sourceNames []string,
) (map[string]domain.ExecutionHistory, error) {
	return r.hot.ObtainLatestExecutionHistoryBySources(ctx, sourceNames)
}

// ObtainLastNExecutionHistoryBySourceName asks the hot tier first and tops up from the
// archive only when the hot tier came up short.
//
// Production records about 4.4 outcomes per source per day, so the API's maximum limit of
// 1000 spans roughly 227 days — past a 180-day horizon. Once the hot tier is pruned it can
// therefore answer a large limit short, and short is indistinguishable from "that is all
// there is" to the caller.
//
// The top-up asks for rows strictly older than the last one the hot tier returned, under
// the same successOnly filter, so the two halves walk one sequence and cannot overlap.
// When the hot tier fills the limit — every ordinary request — the archive is not touched.
func (r *TieredExecutionHistoryRepository) ObtainLastNExecutionHistoryBySourceName(
	ctx context.Context, sourceName string, limit int64, successOnly bool,
) ([]domain.ExecutionHistory, error) {
	recent, err := r.hot.ObtainLastNExecutionHistoryBySourceName(ctx, sourceName, limit, successOnly)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("hot tier: %w", err), loginjector.NewTraceError())
	}
	if int64(len(recent)) >= limit {
		return recent, nil
	}

	// A zero cursor when the hot tier returned nothing means the archive answers the
	// whole request, which is the correct reading of "the newest N outcomes this source
	// has".
	var (
		before   time.Time
		beforeID string
	)
	if n := len(recent); n > 0 {
		before, beforeID = recent[n-1].Timestamp, recent[n-1].ID
	}

	older, err := r.archive.ObtainLastNExecutionHistoryBySourceNameBefore(
		ctx, sourceName, before, beforeID, limit-int64(len(recent)), successOnly,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("archive tier: %w", err), loginjector.NewTraceError())
	}

	return append(recent, older...), nil
}

// ObtainExecutionHistoryErrorCount reads the archive.
//
// This is a count of every failure since the beginning, with no window at all — the one
// number a pruned hot tier stops being able to produce. It also has to agree with the
// pages ObtainLastNExecutionHistoryErrors returns, since it is what sizes their
// pagination, and those come from the archive too.
func (r *TieredExecutionHistoryRepository) ObtainExecutionHistoryErrorCount(ctx context.Context) (int64, error) {
	return r.archive.ObtainExecutionHistoryErrorCount(ctx)
}

// ObtainLastNExecutionHistoryErrors reads the archive.
//
// Offset pagination over failures is unbounded by design: the view exists to page back
// through everything that ever went wrong. Splitting it at a horizon would mean
// translating the offset across two tiers whose totals disagree; one tier answering both
// the pages and the count keeps them consistent.
//
// The cost is that a failure appears here only once reconciliation has copied it —
// seconds, since collection and reconciliation run in that order in one collector
// process. The source list, which is what an operator watches for a live failure, reads
// the hot tier and is unaffected.
func (r *TieredExecutionHistoryRepository) ObtainLastNExecutionHistoryErrors(
	ctx context.Context, offset, limit int64,
) ([]domain.ExecutionHistory, error) {
	return r.archive.ObtainLastNExecutionHistoryErrors(ctx, offset, limit)
}
