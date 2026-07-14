package chart

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// HistoryValuesLoader loads paginated rate_values for a bulk set of
// (source, base, quote) keys, sorted newest first. Returning both counts from
// one snapshot prevents a concurrent collector write from making them disagree
// (see plans/015-history-group-by-provider.md, Risk 2).
type HistoryValuesLoader interface {
	// ObtainHistoryForPairsPaged returns paginated rate_values rows, the
	// row-level total, and the grouped total of distinct (provider title,
	// timestamp) tuples, all from one read-only transaction for a consistent
	// snapshot. Use groupedTotal as the user-facing pagination total; rowTotal
	// is for diagnostics.
	ObtainHistoryForPairsPaged(
		ctx context.Context, pairs []domain.SourcePairKey, limit, offset int64,
	) (rows []domain.RateValue, rowTotal int64, groupedTotal int64, err error)
}

// MeHistoryResult is the result of ObtainMeHistory: one page of rate-collection
// events for a canonical pair, sorted newest first.
type MeHistoryResult struct {
	// Pair is the canonical pair label the results are scoped to (e.g. "USD/KZT").
	Pair string
	// Total is the unpaginated count of distinct (title, timestamp) tuples.
	Total int64
	// Items contains the page rows, newest first.
	Items []MeHistoryRowResult
}

// MeHistoryRowResult is one grouped rate-collection event for a single
// (title, timestamp) tuple. BID and ASK from sibling sources sharing the
// same provider title at the same scrape moment are collapsed into one row.
// LAST (equity) sources surface in their own Last slot, never in Bid.
type MeHistoryRowResult struct {
	// SourceTitle is the human-readable provider title that acts as the grouping key.
	SourceTitle string
	// Timestamp is when the collector scraped this value.
	Timestamp time.Time
	// Bid is the BID price; nil when the provider only scraped ASK/LAST at this moment.
	Bid *float64
	// Ask is the ASK price; nil when the provider only scraped BID/LAST at this moment.
	Ask *float64
	// Last is the last-traded price for equity (LAST-kind) sources; nil for BID/ASK-only providers.
	Last *float64
	// BidDeltaPct is the percent change from the previous BID in this
	// (title, direction) chain within the page. Nil for the first row.
	BidDeltaPct *float64
	// AskDeltaPct is the percent change from the previous ASK in this
	// (title, direction) chain within the page. Nil for the first row.
	AskDeltaPct *float64
	// LastDeltaPct is the percent change from the previous LAST observation in
	// this (title, direction) chain within the page. Nil for the first row.
	LastDeltaPct *float64
}

// ObtainMeHistory returns paginated rate-collection events for the calling
// user's subscribed sources matching the given canonical pair label (e.g.
// "USD/KZT"), sorted newest first.
//
// page is 1-based. limit is bounded by the caller; the service does no
// re-bounding. When no subscriptions match pair, the result has zero items and
// Total=0 (not an error).
//
// sourceTitle is an optional filter. When non-empty, only rows from providers
// whose title matches it byte-for-byte (case-sensitive) are included; the
// filter pre-filters matchingKeys (which retains all source names for sibling
// sources of the same provider) before the repository call, so Total reflects
// the filtered grouped count and pagination stays correct. An unknown
// sourceTitle returns 200 with Total=0 and empty Items — never 400.
//
// Load-bearing invariants (see plans/015-history-group-by-provider.md):
//
//   - Invariant 1: each provider has at most one BID source and one ASK source
//     per (base, quote) pair. If two BID sources share a title for one pair,
//     last value in the page wins within the (title, timestamp) group.
//
//   - Invariant 2: SourceTitle is unique per provider across a user's sources.
//     Two unrelated providers sharing a title string would merge into one chip
//     and row. The seeds enforce this; do not add a provider whose title
//     duplicates an existing one.
//
// The result groups BID and ASK observations for the same (title, timestamp)
// into one MeHistoryRowResult. Delta percent is computed against the previous
// sample in the same (title, direction) chain within the page — the first row
// in any chain has nil delta (v1: cross-page anchoring is deferred).
func (s *Service) ObtainMeHistory(ctx context.Context, userID, pair, sourceTitle string, page, limit int64) (*MeHistoryResult, error) {
	subs, err := s.subs.ObtainRateUserSubscriptionsByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return nil, err
	}

	empty := &MeHistoryResult{Pair: pair, Total: 0, Items: []MeHistoryRowResult{}}
	if len(subs) == 0 {
		return empty, nil
	}

	sourceNames := uniqueSourceNames(subs)
	sourceMeta, err := s.sources.ObtainRateSourcesByNames(ctx, sourceNames)
	if err != nil {
		return nil, err
	}

	// Build SourcePairKey list filtered to sources whose canonical pair label
	// matches pair, via the same pairGroupKey helper as ObtainMeChart.
	seenSources := make(map[string]struct{}, len(subs))
	var matchingKeys []domain.SourcePairKey
	for _, sub := range subs {
		src, ok := sourceMeta[sub.SourceName]
		if !ok {
			continue
		}
		label := pairGroupKey(src.BaseCurrency, src.QuoteCurrency)
		if label != strings.ToUpper(pair) {
			continue
		}
		// Dedup by source_name so each appears once in the tuple-IN clause. Do NOT
		// dedup by title — sibling sources (same title, different source_name) must
		// both appear in matchingKeys so the SQL WHERE covers both.
		if _, already := seenSources[sub.SourceName]; already {
			continue
		}
		seenSources[sub.SourceName] = struct{}{}
		matchingKeys = append(matchingKeys, domain.SourcePairKey{
			SourceName:    src.Name,
			BaseCurrency:  src.BaseCurrency,
			QuoteCurrency: src.QuoteCurrency,
			Kind:          src.Kind,
		})
	}

	// Optional source-title filter. Byte-for-byte because titles are stored
	// case-sensitively in rate_sources.title.
	if sourceTitle != "" {
		// Filter in-place; matchingKeys is local so reusing the backing array is safe.
		filtered := matchingKeys[:0]
		for _, k := range matchingKeys {
			if src, ok := sourceMeta[k.SourceName]; ok && src.Title == sourceTitle {
				filtered = append(filtered, k)
			}
		}
		matchingKeys = filtered
	}

	if len(matchingKeys) == 0 {
		return empty, nil
	}

	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}

	rawRows, rowTotal, groupedTotal, err := s.history.ObtainHistoryForPairsPaged(ctx, matchingKeys, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("ObtainMeHistory user=%s pair=%s: %w", userID, pair, err)
	}
	// rowTotal is the row-level count; groupedTotal is the user-facing total
	// after BID/ASK sibling collapse.
	_ = rowTotal

	if len(rawRows) == 0 && groupedTotal == 0 {
		return empty, nil
	}

	// Build source-metadata maps for fast lookup during grouping.
	titleBySource := make(map[string]string, len(matchingKeys))
	kindBySource := make(map[string]domain.RateSourceKind, len(matchingKeys))
	for _, k := range matchingKeys {
		if src, ok := sourceMeta[k.SourceName]; ok {
			titleBySource[k.SourceName] = src.Title
		}
		kindBySource[k.SourceName] = k.Kind
	}

	// Group rows by (source_title, timestamp). The repository returns rows
	// newest-first; we preserve that order so the result stays newest-first
	// without an extra sort.
	type groupKey struct {
		sourceTitle string
		timestamp   time.Time
	}
	groupOrder := make([]groupKey, 0, len(rawRows))
	groupMap := make(map[groupKey]*MeHistoryRowResult, len(rawRows))

	for i := range rawRows {
		rv := &rawRows[i]
		title := titleBySource[rv.SourceName]
		gk := groupKey{sourceTitle: title, timestamp: rv.Timestamp}
		g, exists := groupMap[gk]
		if !exists {
			newRow := &MeHistoryRowResult{
				SourceTitle: title,
				Timestamp:   rv.Timestamp,
			}
			groupMap[gk] = newRow
			groupOrder = append(groupOrder, gk)
			g = newRow
		}
		// Assign price to the correct direction slot.
		kind := kindBySource[rv.SourceName]
		price := rv.Price
		switch kind {
		case domain.RateSourceKindBID:
			// Invariant 1: one BID source per (title, base, quote); if violated,
			// last-write-in-page-order wins.
			if g.Bid != nil {
				log.Printf("warn: history title collision user=%s pair=%s title=%q ts=%s kind=BID: silent overwrite (Invariant 1 violated)",
					userID, pair, gk.sourceTitle, gk.timestamp.Format(time.RFC3339))
			}
			g.Bid = &price
		case domain.RateSourceKindLAST:
			// Equity last-traded price gets its own slot, distinct from Bid.
			// Invariant 1: one LAST source per (title, base, quote); if violated,
			// last-write-in-page-order wins.
			if g.Last != nil {
				log.Printf("warn: history title collision user=%s pair=%s title=%q ts=%s kind=LAST: silent overwrite (Invariant 1 violated)",
					userID, pair, gk.sourceTitle, gk.timestamp.Format(time.RFC3339))
			}
			g.Last = &price
		default: // handles ASK and, defensively, any unrecognised kind → Ask slot
			if kind != domain.RateSourceKindASK {
				// An unrecognised kind reaching this branch means a new
				// RateSourceKind was added to the domain without a
				// corresponding case here — route to Ask as a safe fallback
				// but surface the skew so it is visible in logs.
				log.Printf("warn: history unrecognised kind user=%s pair=%s title=%q ts=%s kind=%q: routing to Ask slot (data-model skew)",
					userID, pair, gk.sourceTitle, gk.timestamp.Format(time.RFC3339), kind)
			}
			// Invariant 1: one ASK source per (title, base, quote); if violated,
			// last-write-in-page-order wins.
			if g.Ask != nil {
				log.Printf("warn: history title collision user=%s pair=%s title=%q ts=%s kind=ASK: silent overwrite (Invariant 1 violated)",
					userID, pair, gk.sourceTitle, gk.timestamp.Format(time.RFC3339))
			}
			g.Ask = &price
		}
	}

	items := make([]MeHistoryRowResult, 0, len(groupOrder))
	for _, gk := range groupOrder {
		items = append(items, *groupMap[gk])
	}

	// Compute per-direction deltas within the page. items is newest-first, but
	// deltas need oldest-first per provider, so we walk each title's indices in
	// reverse, compute deltas, and write them back.
	//
	// v1 limitation: the first row in any (title, direction) chain within the
	// page has nil delta even if the full history has a predecessor. Cross-page
	// anchoring is deferred.
	type directionState struct {
		lastBid  *float64
		lastAsk  *float64
		lastLast *float64
	}

	// Build per-title ordered indices (largest index = oldest in newest-first list).
	titleIndices := make(map[string][]int, len(matchingKeys))
	for i := range items {
		t := items[i].SourceTitle
		titleIndices[t] = append(titleIndices[t], i)
	}

	for _, idxList := range titleIndices {
		// Sort descending by index to process oldest-first for forward deltas.
		sort.Slice(idxList, func(a, b int) bool { return idxList[a] > idxList[b] })

		st := &directionState{}
		for _, idx := range idxList {
			row := &items[idx]
			if row.Bid != nil {
				if st.lastBid != nil && *st.lastBid != 0 {
					d := (*row.Bid - *st.lastBid) / *st.lastBid * 100
					row.BidDeltaPct = &d
				}
				v := *row.Bid
				st.lastBid = &v
			}
			if row.Ask != nil {
				if st.lastAsk != nil && *st.lastAsk != 0 {
					d := (*row.Ask - *st.lastAsk) / *st.lastAsk * 100
					row.AskDeltaPct = &d
				}
				v := *row.Ask
				st.lastAsk = &v
			}
			if row.Last != nil {
				if st.lastLast != nil && *st.lastLast != 0 {
					d := (*row.Last - *st.lastLast) / *st.lastLast * 100
					row.LastDeltaPct = &d
				}
				v := *row.Last
				st.lastLast = &v
			}
		}
	}

	return &MeHistoryResult{
		Pair:  pair,
		Total: groupedTotal,
		Items: items,
	}, nil
}
