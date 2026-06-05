// Package chart provides the application service that builds sparkline-list
// charts and per-pair rate history from a user's subscriptions and the
// system-wide public chart endpoint. It is consumed by the /api/me/rates/chart,
// /api/me/rates/history, and /api/public/rates/chart handlers and is free of
// HTTP and Telegram concerns.
package chart

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/domain/ratepair"
)

// bucketCount is the fixed number of equal-width time buckets used when
// downsampling a sparkline series, regardless of the selected period.
// Keeping this constant makes the visual density of the sparkline independent
// of the horizon: a 360-day chart has 12 buckets of ≈30 days each, and a
// 7-day chart has 12 buckets of ≈14 hours each. This is intentional — period
// selection is about horizon, not resolution. See Trade-off #3 in the plan.
const bucketCount = 12

// SubscriptionsLoader loads a user's subscriptions.
type SubscriptionsLoader interface {
	ObtainRateUserSubscriptionsByUserID(
		ctx context.Context, userType domain.UserType, userID string,
	) ([]domain.RateUserSubscription, error)
}

// SourcesLoader resolves source metadata (base, quote, kind) for a set of
// source names. Used to build (base, quote, kind) triples from subscription
// source_name references.
type SourcesLoader interface {
	ObtainRateSourcesByNames(ctx context.Context, names []string) (map[string]domain.RateSource, error)
}

// PublicSourcesLoader enumerates all distinct active (name, base, quote, kind)
// triples across the whole system. Satisfied by *repository.RateSourceRepository.
type PublicSourcesLoader interface {
	ObtainDistinctActivePairTriples(ctx context.Context) ([]domain.SourcePairKey, error)
}

// ValuesLoader loads time-series rate values for a bulk set of pairs.
type ValuesLoader interface {
	ObtainValuesForPairsSince(
		ctx context.Context, pairs []domain.SourcePairKey, since time.Time,
	) ([]domain.RateValue, error)
}

// MeChart is the result of ObtainMeChart: one row per canonical currency pair
// (BID and ASK collapsed into one row), sorted by the ratepair.Less comparator.
type MeChart struct {
	// Pairs is the ordered list of sparkline rows. Never nil; empty when the
	// user has no subscriptions.
	Pairs []PairRow
}

// PublicChart is the result of ObtainPublicChart.
type PublicChart struct {
	// Pairs is one page of the system-wide sparkline list. Never nil; empty
	// on an out-of-range page or when no active sources exist.
	Pairs []PairRow
}

// PairRow holds all the data needed to render one sparkline row for a canonical
// currency pair. It may contain one series (BID-only or ASK-only) or two
// (both directions subscribed).
type PairRow struct {
	// Pair is the display label in BID-natural direction (e.g. "USD/KZT").
	// When only ASK is subscribed, the label is inverted from the ASK direction:
	// ASK base="USD" quote="KZT" → label "USD/KZT".
	Pair string
	// Category is the pair's market category ("fiat" or "metal").
	Category ratepair.Category
	// SpreadPct is the relative spread (ASK.Latest - BID.Latest) / BID.Latest * 100.
	// It is non-nil only when both BID and ASK series exist and both have a
	// non-zero Latest value.
	SpreadPct *float64
	// Series contains 1 or 2 entries ordered BID before ASK.
	Series []SeriesRow
}

// SeriesRow holds the sparkline data for one direction within a PairRow.
type SeriesRow struct {
	// Kind is the rate direction; domain.RateSourceKindBID or domain.RateSourceKindASK.
	Kind domain.RateSourceKind
	// Color is the role-based hex color: ratepair.ColorBid for BID, ratepair.ColorAsk for ASK.
	Color string
	// Latest is the last known price for this direction within the window.
	// Zero when there are no data points.
	Latest float64
	// DeltaPct is (last - first) / first * 100 computed over the downsampled
	// points. Zero when fewer than two points are available (Sparse=true).
	DeltaPct float64
	// Sparse is true when fewer than two values were found in the window, so
	// the renderer draws a flat line and shows "+0.0%" delta.
	Sparse bool
	// Points are the downsampled sparkline values. Nil when zero raw values
	// exist in the window (no-data state).
	Points []SparkPoint
}

// SparkPoint is one point in a downsampled sparkline series.
type SparkPoint struct {
	// Timestamp is the right edge of the time bucket this point represents.
	Timestamp time.Time
	// Value is the price of the last sample in the bucket.
	Value float64
}

// Service builds sparkline charts and per-pair history from a user's
// subscriptions, and the system-wide public sparkline list. Construct with
// NewService; it has no mutable state and is safe for concurrent use.
type Service struct {
	subs          SubscriptionsLoader
	sources       SourcesLoader
	values        ValuesLoader
	history       HistoryValuesLoader
	publicSources PublicSourcesLoader
	now           func() time.Time
}

// NewService constructs a Service. now is injected for deterministic tests;
// pass time.Now in production. history is the loader used by ObtainMeHistory;
// the same *repository.RateValueRepository instance satisfies both ValuesLoader
// and HistoryValuesLoader. publicSources is the loader used by ObtainPublicChart;
// pass the same *repository.RateSourceRepository used for sources.
func NewService(subs SubscriptionsLoader, sources SourcesLoader, values ValuesLoader, history HistoryValuesLoader, publicSources PublicSourcesLoader, now func() time.Time) *Service {
	return &Service{subs: subs, sources: sources, values: values, history: history, publicSources: publicSources, now: now}
}

// ObtainMeChart loads the calling user's subscriptions and builds a sparkline
// chart covering the last 7 days. It is a convenience wrapper around
// ObtainMeChartForPeriod with periodDays=7.
func (s *Service) ObtainMeChart(ctx context.Context, userID string) (*MeChart, error) {
	return s.ObtainMeChartForPeriod(ctx, userID, 7)
}

// ObtainMeChartForPeriod loads the calling user's subscriptions, fetches
// periodDays of rate data for every subscribed pair, downsamples each direction
// into bucketCount points, groups BID and ASK for the same canonical pair into
// one PairRow, and returns the sorted rows. periodDays should be one of the
// whitelisted values {7, 30, 90, 180, 360}; validation is the caller's
// responsibility (the handler layer enforces it).
//
// Pipeline:
//  1. Load user subscriptions via subs.ObtainRateUserSubscriptionsByUserID.
//  2. Load source metadata (base, quote, kind) for each unique source_name.
//  3. Dedupe (base, quote, kind) triples; one series per triple regardless
//     of how many sources serve it.
//  4. Load rate values for all (source_name, base, quote, kind) keys since
//     now - periodDays*24h via values.ObtainValuesForPairsSince.
//  5. Group values by (base, quote, kind); when multiple sources contribute a
//     value at the same timestamp, the last row (highest ID) wins.
//  6. Downsample each group into bucketCount equal-width time buckets
//     (left-closed, right-open). Each bucket takes the last sample's value.
//     Empty buckets carry the previous bucket's value forward. Leading empty
//     buckets before the first sample are omitted.
//  7. Compute delta_pct over downsampled points.
//  8. Group BID and ASK SeriesRows for the same canonical pair into one PairRow.
//  9. Determine the display label as BID-natural direction.
//  10. Compute SpreadPct when both directions are present and non-zero.
//  11. Sort rows fiat-before-metal, then canonical-pair-ascending.
func (s *Service) ObtainMeChartForPeriod(ctx context.Context, userID string, periodDays int64) (*MeChart, error) {
	subs, err := s.subs.ObtainRateUserSubscriptionsByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return &MeChart{Pairs: []PairRow{}}, nil
	}

	sourceNames := uniqueSourceNames(subs)

	sourceMeta, err := s.sources.ObtainRateSourcesByNames(ctx, sourceNames)
	if err != nil {
		return nil, err
	}

	// Build SourcePairKey list (one per unique source_name that resolves to a source).
	seen := make(map[string]struct{}, len(subs))
	var allKeys []domain.SourcePairKey
	for _, sub := range subs {
		if _, ok := seen[sub.SourceName]; ok {
			continue
		}
		seen[sub.SourceName] = struct{}{}
		src, ok := sourceMeta[sub.SourceName]
		if !ok {
			continue
		}
		allKeys = append(allKeys, domain.SourcePairKey{
			SourceName:    sub.SourceName,
			BaseCurrency:  src.BaseCurrency,
			QuoteCurrency: src.QuoteCurrency,
			Kind:          src.Kind,
		})
	}

	if len(allKeys) == 0 {
		return &MeChart{Pairs: []PairRow{}}, nil
	}

	// Dedupe to unique (base, quote, kind) triples.
	uniquePairs := dedupePairTriples(allKeys)

	since := s.now().Add(-time.Duration(periodDays) * 24 * time.Hour)
	rows, err := s.buildPairRows(ctx, allKeys, uniquePairs, since)
	if err != nil {
		return nil, err
	}

	return &MeChart{Pairs: rows}, nil
}

// ObtainPublicChart enumerates every distinct (base, quote, kind) triple across
// active sources and builds a 7-day sparkline list. It is a convenience wrapper
// around ObtainPublicChartForPeriod with periodDays=7.
//
// page and limit are both normalised internally: page < 1 defaults to 1, limit
// < 1 defaults to 20, limit > 100 is clamped to 100.
func (s *Service) ObtainPublicChart(ctx context.Context, page, limit int64) (*PublicChart, int64, error) {
	return s.ObtainPublicChartForPeriod(ctx, page, limit, 7)
}

// ObtainPublicChartForPeriod enumerates every distinct (base, quote, kind) triple
// across active sources, downsamples each into a sparkline covering periodDays,
// groups BID/ASK into one PairRow per canonical pair, sorts via ratepair.Less,
// then slices to the requested page. Returns the page and the unpaginated total
// of PairRows (the post-grouping count; BID+ASK for the same canonical pair
// collapse to one row, so total ≤ len(triples)). Returns a non-nil *PublicChart
// even when the result is empty or the page is out of range.
//
// periodDays should be one of the whitelisted values {7, 30, 90, 180, 360};
// validation is the caller's responsibility (the handler layer enforces it).
// page and limit are both normalised internally: page < 1 defaults to 1, limit
// < 1 defaults to 20, limit > 100 is clamped to 100.
func (s *Service) ObtainPublicChartForPeriod(ctx context.Context, page, limit, periodDays int64) (*PublicChart, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	triples, err := s.publicSources.ObtainDistinctActivePairTriples(ctx)
	if err != nil {
		return nil, 0, err
	}
	if len(triples) == 0 {
		return &PublicChart{Pairs: []PairRow{}}, 0, nil
	}

	uniquePairs := dedupePairTriples(triples)

	since := s.now().Add(-time.Duration(periodDays) * 24 * time.Hour)
	rows, err := s.buildPairRows(ctx, triples, uniquePairs, since)
	if err != nil {
		return nil, 0, err
	}

	total := int64(len(rows))
	offset := (page - 1) * limit
	if offset >= total {
		return &PublicChart{Pairs: []PairRow{}}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return &PublicChart{Pairs: rows[offset:end]}, total, nil
}

// buildPairRows runs the shared downsampling, BID/ASK grouping, and
// sort pipeline used by ObtainMeChartForPeriod and ObtainPublicChartForPeriod.
// allKeys is the list of (source, base, quote, kind) query targets; uniquePairs
// is the deduplicated set of (base, quote, kind) triples for which series should
// be built; since is the lower bound of the time window (now - period). Returns
// a non-nil slice (possibly empty) sorted by ratepair.Less.
//
// The sort uses sort.SliceStable so that two PairRows with equal ratepair.Less
// ordering remain in the order they were produced by the grouping loop. In
// practice this is moot — ratepair.Less uses canonical pair as a tiebreaker —
// but it makes page boundaries deterministic in the presence of any future
// ties, which matters for ObtainPublicChartForPeriod where the sorted slice is
// then split into pages.
func (s *Service) buildPairRows(ctx context.Context, allKeys []domain.SourcePairKey, uniquePairs []ratepair.Pair, since time.Time) ([]PairRow, error) {
	if len(allKeys) == 0 {
		return []PairRow{}, nil
	}

	now := s.now()

	rawValues, err := s.values.ObtainValuesForPairsSince(ctx, allKeys, since)
	if err != nil {
		return nil, err
	}

	// Build a source-name → kind map so the value-grouping loop below is O(1)
	// per row instead of O(len(allKeys)) per row.
	kindBySource := make(map[string]domain.RateSourceKind, len(allKeys))
	for _, k := range allKeys {
		kindBySource[k.SourceName] = k.Kind
	}

	// Group values by (base, quote, kind), using the kind inferred from the
	// source key list (rate_values has no kind column).
	type groupKey struct {
		base, quote string
		kind        domain.RateSourceKind
	}
	grouped := make(map[groupKey][]domain.RateValue, len(uniquePairs))
	for _, rv := range rawValues {
		gk := groupKey{
			base:  strings.ToUpper(rv.BaseCurrency),
			quote: strings.ToUpper(rv.QuoteCurrency),
			kind:  kindBySource[rv.SourceName],
		}
		grouped[gk] = append(grouped[gk], rv)
	}

	// Build one SeriesRow per unique (base, quote, kind) triple.
	groupMap := make(map[string]*pairGroup, len(uniquePairs))
	groupOrder := make([]string, 0, len(uniquePairs))

	for _, p := range uniquePairs {
		gk := groupKey{
			base:  strings.ToUpper(p.Base),
			quote: strings.ToUpper(p.Quote),
			kind:  p.Kind,
		}
		sr := buildSeriesRow(p, grouped[gk], since, now)

		canon := pairGroupKey(p.Base, p.Quote)
		g, exists := groupMap[canon]
		if !exists {
			g = &pairGroup{
				labelBase:  strings.ToUpper(p.Base),
				labelQuote: strings.ToUpper(p.Quote),
			}
			groupMap[canon] = g
			groupOrder = append(groupOrder, canon)
		}
		switch p.Kind {
		case domain.RateSourceKindBID:
			g.bid = &sr
			// BID-natural direction: base/quote from BID subscription.
			g.labelBase = strings.ToUpper(p.Base)
			g.labelQuote = strings.ToUpper(p.Quote)
		case domain.RateSourceKindASK:
			g.ask = &sr
			// Only set label from ASK if BID not yet seen.
			// BID-natural label uses the stored base/quote of the ASK subscription
			// unchanged. Both BID and ASK subscriptions for the same rate are stored
			// with identical base and quote (the direction is encoded in Kind, not
			// in which currency occupies base vs quote). So base/quote from ASK
			// gives the same BID-natural label as it would from BID.
			if g.bid == nil {
				g.labelBase = strings.ToUpper(p.Base)
				g.labelQuote = strings.ToUpper(p.Quote)
			}
		}
	}

	rows := make([]PairRow, 0, len(groupOrder))
	for _, canon := range groupOrder {
		g := groupMap[canon]
		row := buildPairRow(g)
		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		// Use the BID-natural label base/quote for canonical sort.
		pairI := ratepair.Pair{Base: labelBase(rows[i].Pair), Quote: labelQuote(rows[i].Pair)}
		pairJ := ratepair.Pair{Base: labelBase(rows[j].Pair), Quote: labelQuote(rows[j].Pair)}
		return ratepair.Less(pairI, pairJ)
	})

	return rows, nil
}

// pairGroup accumulates BID and ASK series for one canonical currency pair
// while iterating over the unique-pair list.
type pairGroup struct {
	// labelBase and labelQuote form the BID-natural display label (BASE/QUOTE).
	// Derived from the BID subscription when present; otherwise from the ASK
	// subscription with base and quote swapped.
	labelBase  string
	labelQuote string
	bid        *SeriesRow
	ask        *SeriesRow
}

// buildPairRow assembles a PairRow from a pairGroup. BID series is placed
// first when present; ASK second. SpreadPct is computed when both exist and
// both have a non-zero Latest.
func buildPairRow(g *pairGroup) PairRow {
	label := g.labelBase + "/" + g.labelQuote
	category := ratepair.CategoryOf(g.labelBase)

	row := PairRow{
		Pair:     label,
		Category: category,
	}

	if g.bid != nil {
		row.Series = append(row.Series, *g.bid)
	}
	if g.ask != nil {
		row.Series = append(row.Series, *g.ask)
	}

	if g.bid != nil && g.ask != nil && g.bid.Latest != 0 && g.ask.Latest != 0 {
		spread := (g.ask.Latest - g.bid.Latest) / g.bid.Latest * 100
		row.SpreadPct = &spread
	}

	return row
}

// buildSeriesRow constructs a SeriesRow from raw rate values for a single pair direction.
//
// Bucket boundaries are left-closed, right-open:
// bucket i covers [since + i*step, since + (i+1)*step).
// Each bucket picks the last value in it (since vals are ordered by timestamp ASC,
// later overwrites win). Empty buckets carry the previous bucket's value forward
// (continuous line). Leading empty buckets before the first sample are omitted,
// so the returned Points slice length may be less than bucketCount.
func buildSeriesRow(p ratepair.Pair, vals []domain.RateValue, since, now time.Time) SeriesRow {
	color := ratepair.ColorBid
	if p.Kind == domain.RateSourceKindASK {
		color = ratepair.ColorAsk
	}

	sr := SeriesRow{
		Kind:  p.Kind,
		Color: color,
	}

	if len(vals) == 0 {
		sr.Sparse = true
		return sr
	}

	deduped := dedupeByTimestamp(vals)

	// Sparse when fewer than 2 distinct raw samples in the window.
	// We check deduped (timestamp-deduplicated) raw samples, not filled buckets.
	sparse := len(deduped) < 2

	window := now.Sub(since)
	step := window / time.Duration(bucketCount)

	bucketVal := make([]float64, bucketCount)
	bucketFilled := make([]bool, bucketCount)

	for _, rv := range deduped {
		offset := rv.Timestamp.Sub(since)
		idx := int(offset / step)
		if idx < 0 {
			idx = 0
		}
		if idx >= bucketCount {
			idx = bucketCount - 1
		}
		bucketVal[idx] = rv.Price
		bucketFilled[idx] = true
	}

	var prev float64
	hasPrev := false
	var points []SparkPoint
	for i := 0; i < bucketCount; i++ {
		bucketTime := since.Add(time.Duration(i+1) * step)
		if bucketFilled[i] {
			prev = bucketVal[i]
			hasPrev = true
			points = append(points, SparkPoint{Timestamp: bucketTime, Value: prev})
		} else if hasPrev {
			points = append(points, SparkPoint{Timestamp: bucketTime, Value: prev})
		}
	}

	if len(points) == 0 {
		sr.Sparse = true
		return sr
	}

	sr.Latest = points[len(points)-1].Value
	sr.Points = points
	sr.Sparse = sparse

	if sparse {
		sr.DeltaPct = 0
		return sr
	}

	first := points[0].Value
	last := points[len(points)-1].Value
	if first != 0 {
		sr.DeltaPct = (last - first) / first * 100
	}

	return sr
}

// dedupeByTimestamp keeps, for each unique timestamp, the last row in the
// group (highest ID since IDs encode timestamp + UUID bytes). Input must be
// sorted ASC by timestamp then ASC by ID, matching the repository query order.
func dedupeByTimestamp(vals []domain.RateValue) []domain.RateValue {
	if len(vals) == 0 {
		return nil
	}
	out := make([]domain.RateValue, 0, len(vals))
	i := 0
	for i < len(vals) {
		j := i + 1
		for j < len(vals) && vals[j].Timestamp.Equal(vals[i].Timestamp) {
			j++
		}
		out = append(out, vals[j-1])
		i = j
	}
	return out
}

// dedupePairTriples returns a deduplicated slice of ratepair.Pair built from
// the given SourcePairKey list. Two keys with the same (base, quote, kind)
// collapse to one pair regardless of source_name.
func dedupePairTriples(keys []domain.SourcePairKey) []ratepair.Pair {
	pairs := make([]ratepair.Pair, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, ratepair.Pair{
			Base:  k.BaseCurrency,
			Quote: k.QuoteCurrency,
			Kind:  k.Kind,
		})
	}
	return ratepair.Dedupe(pairs)
}

// uniqueSourceNames returns the set of unique source names from subscriptions.
func uniqueSourceNames(subs []domain.RateUserSubscription) []string {
	seen := make(map[string]struct{}, len(subs))
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		if _, ok := seen[s.SourceName]; ok {
			continue
		}
		seen[s.SourceName] = struct{}{}
		out = append(out, s.SourceName)
	}
	return out
}

// pairGroupKey returns the group identifier used to collapse BID and ASK with
// the same storage direction into one row. base/quote are taken as-stored,
// so subscriptions with inverted storage direction stay in separate rows.
// This differs intentionally from ratepair.canonicalPair (which uses min/max
// sorting for adjacent ordering): USD/KZT BID and KZT/USD ASK must NOT merge,
// because their scales are incommensurable (≈487 vs ≈0.002).
func pairGroupKey(base, quote string) string {
	return strings.ToUpper(base) + "/" + strings.ToUpper(quote)
}

// labelBase returns the base portion of a "BASE/QUOTE" pair label.
// Returns the whole string if no slash is found (should not happen in practice).
func labelBase(label string) string {
	if i := strings.IndexByte(label, '/'); i >= 0 {
		return label[:i]
	}
	return label
}

// labelQuote returns the quote portion of a "BASE/QUOTE" pair label.
// Returns empty string if no slash is found.
func labelQuote(label string) string {
	if i := strings.IndexByte(label, '/'); i >= 0 {
		return label[i+1:]
	}
	return ""
}
