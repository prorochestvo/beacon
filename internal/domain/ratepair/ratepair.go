// Package ratepair provides classification, coloring, deduplication, and
// sorting utilities for currency-pair data in the Mini App sparkline chart,
// shared by the chart application service and the HTTP handler layer.
package ratepair

import (
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
)

// Category identifies the broad market type of a currency pair's base.
type Category string

const (
	// CategoryFiat identifies a fiat-currency base (the default category).
	CategoryFiat Category = "fiat"
	// CategoryMetal identifies a precious-metal base (GOLD, SILVER, PLATINUM, PALLADIUM).
	CategoryMetal Category = "metal"
)

// metalSymbols is the set of uppercase base-currency codes treated as metals.
// Exported access is via IsMetalSymbol.
var metalSymbols = map[string]struct{}{
	"GOLD":      {},
	"SILVER":    {},
	"PLATINUM":  {},
	"PALLADIUM": {},
}

// IsMetalSymbol reports whether base (case-insensitive) is in the metal set.
func IsMetalSymbol(base string) bool {
	_, ok := metalSymbols[strings.ToUpper(base)]
	return ok
}

// CategoryOf returns CategoryMetal when the uppercased base is a member of
// metalSymbols, otherwise CategoryFiat. Empty string is treated as fiat.
func CategoryOf(base string) Category {
	if IsMetalSymbol(base) {
		return CategoryMetal
	}
	return CategoryFiat
}

// Pair is a value object identifying a unique rate stream.
type Pair struct {
	// Base is the base currency code (e.g. "USD", "GOLD").
	Base string
	// Quote is the quote currency code (e.g. "KZT").
	Quote string
	// Kind is the rate direction; must be domain.RateSourceKindBID or
	// domain.RateSourceKindASK.
	Kind domain.RateSourceKind
}

// Less is the sort comparator for a slice of Pair values. Sort key:
//  1. Category ascending (fiat before metal).
//  2. Canonical pair ascending: min(base,quote)+"/"+max(base,quote), both
//     uppercased. This keeps BID and ASK rows for the same underlying pair
//     adjacent regardless of label direction.
//  3. Kind ascending: BID before ASK.
func Less(a, b Pair) bool {
	catA := CategoryOf(a.Base)
	catB := CategoryOf(b.Base)
	if catA != catB {
		// CategoryFiat < CategoryMetal lexicographically ("fiat" < "metal").
		return catA < catB
	}
	canonA := canonicalPair(a.Base, a.Quote)
	canonB := canonicalPair(b.Base, b.Quote)
	if canonA != canonB {
		return canonA < canonB
	}
	// BID < ASK lexicographically.
	return kindOrder(a.Kind) < kindOrder(b.Kind)
}

// Dedupe collapses duplicate (Base, Quote, Kind) triples, preserving the
// first occurrence in input order. Callers should sort the result using Less
// after deduplication.
func Dedupe(in []Pair) []Pair {
	type key struct {
		base, quote string
		kind        domain.RateSourceKind
	}
	seen := make(map[key]struct{}, len(in))
	out := make([]Pair, 0, len(in))
	for _, p := range in {
		k := key{
			base:  strings.ToUpper(p.Base),
			quote: strings.ToUpper(p.Quote),
			kind:  p.Kind,
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, p)
	}
	return out
}

const (
	// ColorBid is the semantic line color for BID series across all pairs.
	ColorBid = "#1D9E75"
	// ColorAsk is the semantic line color for ASK series across all pairs.
	ColorAsk = "#378ADD"
	// ColorDeltaUp is the hex color used for positive delta indicators.
	ColorDeltaUp = "#3B6D11"
	// ColorDeltaDown is the hex color used for negative delta indicators.
	ColorDeltaDown = "#A32D2D"
)

// ChartWindow is the default rolling time window used for the sparkline chart.
//
// Deprecated: pass a periodDays parameter to the service methods that accept it
// (ObtainMeChartForPeriod, ObtainPublicChartForPeriod). Kept as a semantic
// default so existing call-sites and test fixtures still compile during the
// migration.
const ChartWindow = 7 * 24 * time.Hour

// canonicalPair returns "MIN/MAX" of the two codes (uppercased) so that
// USD/KZT and KZT/USD share the same canonical key.
func canonicalPair(a, b string) string {
	ua := strings.ToUpper(a)
	ub := strings.ToUpper(b)
	if ua <= ub {
		return ua + "/" + ub
	}
	return ub + "/" + ua
}

// kindOrder maps a RateSourceKind to a numeric order used for stable
// BID-before-ASK sorting. Lower values sort first.
func kindOrder(k domain.RateSourceKind) int {
	if k == domain.RateSourceKindASK {
		return 1
	}
	return 0
}
