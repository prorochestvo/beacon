// Package ratepair provides classification, coloring, deduplication, and
// sorting utilities for currency-pair data in the Mini App sparkline chart,
// shared by the chart application service and the HTTP handler layer.
package ratepair

import (
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// Category identifies the broad market type of a currency pair's base.
type Category string

const (
	// CategoryFiat identifies a fiat-currency base (the default category).
	CategoryFiat Category = "fiat"
	// CategoryMetal identifies a precious-metal base (GOLD, SILVER, PLATINUM, PALLADIUM).
	CategoryMetal Category = "metal"
	// CategoryEquity identifies an equity/stock base (e.g. AAPL, CCBN), priced
	// as a single last-traded value (RateSourceKindLAST).
	CategoryEquity Category = "equity"
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

// equitySymbols is the set of uppercase base codes treated as equities.
//
// DUPLICATION NOTE: these tickers are also seeded as LAST sources in
// migrations/202606.009.rate_sources.seed_stocks.sql and
// migrations/202607.018.rate_sources.seed_yahoo_watchlist.sql. Adding a new
// equity ticker requires editing BOTH this set and the migration; a ticker
// present only in the migration silently classifies as fiat. See
// TestCategoryOf. Crypto bases (BTC, ETH, SOL, XRP, DOGE) share this category
// too — there is no separate CategoryCrypto; see the Trade-offs section of
// plans/completed/*.expand-yahoo-equity-watchlist.md.
var equitySymbols = map[string]struct{}{
	"AAPL":  {},
	"CCBN":  {},
	"MSFT":  {},
	"GOOGL": {},
	"AMZN":  {},
	"NVDA":  {},
	"META":  {},
	"TSLA":  {},
	"NFLX":  {},
	"AMD":   {},
	"PLTR":  {},
	"COIN":  {},
	"AVGO":  {},
	"SPY":   {},
	"QQQ":   {},
	"BTC":   {},
	"ETH":   {},
	"SOL":   {},
	"XRP":   {},
	"DOGE":  {},
}

// IsEquitySymbol reports whether base (case-insensitive) is a known equity ticker.
func IsEquitySymbol(base string) bool {
	_, ok := equitySymbols[strings.ToUpper(base)]
	return ok
}

// CategoryOf returns the market category for the given base code. Equity
// bases (AAPL, CCBN) take priority, then metals, then fiat as the default.
// Empty string is treated as fiat.
func CategoryOf(base string) Category {
	if IsEquitySymbol(base) {
		return CategoryEquity
	}
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
	// Kind is the rate direction; one of domain.RateSourceKindBID,
	// domain.RateSourceKindASK, or domain.RateSourceKindLAST.
	Kind domain.RateSourceKind
}

// categoryOrder assigns a numeric rank to each Category so Less sorts
// fiat → metal → equity regardless of the strings' lexicographic order.
// Every Category constant must have an entry here; a missing key silently
// returns rank 0 (same as fiat), which would sort that category first instead
// of at its intended position. Add a new constant to this map and to
// TestLess/"categoryOrder is complete" in ratepair_test.go whenever a new
// Category is introduced.
var categoryOrder = map[Category]int{
	CategoryFiat:   0,
	CategoryMetal:  1,
	CategoryEquity: 2,
}

// Less is the sort comparator for a slice of Pair values. Sort key:
//  1. Category ascending: fiat (0) < metal (1) < equity (2).
//  2. Canonical pair ascending: min(base,quote)+"/"+max(base,quote), both
//     uppercased. This keeps BID and ASK rows for the same underlying pair
//     adjacent regardless of label direction.
//  3. Kind ascending: BID before ASK before LAST.
func Less(a, b Pair) bool {
	catA := CategoryOf(a.Base)
	catB := CategoryOf(b.Base)
	if catA != catB {
		return categoryOrder[catA] < categoryOrder[catB]
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
	// ColorLast is the semantic line color for LAST (equity) series. Distinct
	// from ColorBid/ColorAsk so a stock line reads as its own asset class.
	ColorLast = "#D98E04"
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
// BID-before-ASK-before-LAST sorting. Lower values sort first.
//
// Note: the row sort in the chart service (service.go buildPairRows) constructs
// a kind-less Pair, so kindOrder is not exercised with LAST today. The LAST=2
// value is forward-looking consistency only.
func kindOrder(k domain.RateSourceKind) int {
	switch k {
	case domain.RateSourceKindASK:
		return 1
	case domain.RateSourceKindLAST:
		return 2
	default:
		return 0
	}
}
