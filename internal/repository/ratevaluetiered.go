package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// DefaultHotHorizon is how far back the hot database is expected to hold rate values.
// Reads that stay inside it are answered by the hot tier alone; reads that cross it are
// answered by the archive.
//
// It is also the boundary the hot tier's pruning will eventually use, and the two must
// stay the same number: a query routed to the hot tier for a window the hot tier no
// longer keeps returns a short answer with no error to signal it.
const DefaultHotHorizon = 180 * 24 * time.Hour

// tierReader is the read surface the tiered repository needs from each tier. Both are
// satisfied by *RateValueRepository — the archive's rate_values and rate_sources columns
// match the hot schema, so the same queries run against either database.
type tierReader interface {
	ObtainValuesForPairsBetween(
		ctx context.Context, pairs []domain.SourcePairKey, since, until time.Time,
	) ([]domain.RateValue, error)
	ObtainHistoryForPairsPaged(
		ctx context.Context, pairs []domain.SourcePairKey, limit, offset int64,
	) (rows []domain.RateValue, rowTotal int64, groupedTotal int64, err error)
}

// NewTieredRateValueRepository routes rate-value reads across the hot and archive
// databases at the given horizon. now defaults to time.Now and exists for tests;
// horizon falls back to DefaultHotHorizon when not positive.
//
// Both tiers are required. There is deliberately no archive-less mode: a deployment
// without an archive passes its *RateValueRepository straight to the services, which
// take the same interfaces. Folding that case in here would mean accepting a nil
// archive, and a typed-nil pointer stored in an interface is not nil — the check would
// pass and the first deep read would panic.
func NewTieredRateValueRepository(
	hot, archive tierReader, horizon time.Duration, now func() time.Time,
) (*TieredRateValueRepository, error) {
	if hot == nil || archive == nil {
		return nil, errors.New("tiered rate values: hot and archive readers are both required")
	}
	if horizon <= 0 {
		horizon = DefaultHotHorizon
	}
	if now == nil {
		now = time.Now
	}
	return &TieredRateValueRepository{hot: hot, archive: archive, horizon: horizon, now: now}, nil
}

// TieredRateValueRepository answers rate-value reads from whichever tier holds the
// window being asked about.
//
// The archive is a superset of the hot database — the reconciliation pass only ever adds
// to it — so it can answer any query on its own. The hot tier is preferred wherever it
// suffices anyway, for two reasons: it is the tier the reconciliation pass can lag
// behind, and it is the one whose indexes stay small enough to matter as history grows.
type TieredRateValueRepository struct {
	hot     tierReader
	archive tierReader
	horizon time.Duration
	now     func() time.Time
}

// ObtainValuesForPairsSince returns the chart series for the given pairs, reading each
// half of the window from the tier that owns it.
//
// A window that starts inside the horizon — every chart period up to 180 days — is a
// single hot read, unchanged from the untiered path. A window that starts before it is
// two reads over half-open, non-overlapping ranges: [since, cut) from the archive and
// [cut, now] from the hot tier. The halves cannot duplicate or drop a row at the seam,
// so they concatenate without deduplication, and because each half is already ordered by
// (timestamp, id) and every archive row precedes every hot row, the concatenation is
// ordered too.
//
// Taking the recent half from the hot tier rather than from the archive is what keeps
// the right edge of a long chart current: the archive trails the hot database by however
// long it takes the next reconciliation pass to run.
func (r *TieredRateValueRepository) ObtainValuesForPairsSince(
	ctx context.Context, pairs []domain.SourcePairKey, since time.Time,
) ([]domain.RateValue, error) {
	cut := r.now().Add(-r.horizon)

	if !since.Before(cut) {
		return r.hot.ObtainValuesForPairsBetween(ctx, pairs, since, time.Time{})
	}

	archived, err := r.archive.ObtainValuesForPairsBetween(ctx, pairs, since, cut)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("archive tier: %w", err), loginjector.NewTraceError())
	}

	recent, err := r.hot.ObtainValuesForPairsBetween(ctx, pairs, cut, time.Time{})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("hot tier: %w", err), loginjector.NewTraceError())
	}

	return append(archived, recent...), nil
}

// ObtainHistoryForPairsPaged reads the paged history view entirely from the archive.
//
// Splitting this one at the horizon the way the chart is split would mean translating a
// page offset across two tiers, and it would not help anyway: the total this returns
// drives the pagination control, and once the hot tier is pruned its total stops
// describing how much history exists. The archive's total always does, and since the
// archive holds every row the hot tier holds, the rows that go with that total come from
// the same place.
//
// The cost is that a row is visible here only once the reconciliation pass has copied it.
// Collection and reconciliation run in that order inside a single collector process, so
// the gap is the seconds between them; a pass that fails widens it until the next tick,
// and the next tick closes it.
func (r *TieredRateValueRepository) ObtainHistoryForPairsPaged(
	ctx context.Context, pairs []domain.SourcePairKey, limit, offset int64,
) (rows []domain.RateValue, rowTotal int64, groupedTotal int64, err error) {
	return r.archive.ObtainHistoryForPairsPaged(ctx, pairs, limit, offset)
}
