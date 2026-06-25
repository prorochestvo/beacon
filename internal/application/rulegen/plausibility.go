package rulegen

// currencyPair is the map key. A struct rather than a "USD/KZT" string avoids
// format ambiguity and gives allocation-free lookups.
type currencyPair struct {
	Base  string
	Quote string
}

// valueRange holds an inclusive [Lo, Hi] plausibility window. A rate value v
// is accepted when Lo <= v <= Hi.
type valueRange struct {
	Lo float64
	Hi float64
}

// plausibleRanges maps every (base, quote) pair seeded in
// migrations/202605.007.rate_sources.seed_initial.sql to an inclusive [Lo, Hi]
// float64 window (see migrations/ for the current seed filename). Pairs absent
// from the table fall through to the universal
// (MinPlausibleRateValue, MaxPlausibleRateValue] check in the executor, so
// unknown pairs are never hard-rejected here.
//
// Scope boundary: internal/tools/rateextractor and
// internal/application/sourceaudit deliberately do NOT use this table — both
// have their own history-based checks. Adding a rate source means adding (or
// intentionally omitting) its pair here and documenting the decision.
var plausibleRanges = map[currencyPair]valueRange{
	// KZT-quoted majors
	{Base: "USD", Quote: "KZT"}: {Lo: 100, Hi: 1000},
	{Base: "EUR", Quote: "KZT"}: {Lo: 100, Hi: 1000},
	{Base: "GBP", Quote: "KZT"}: {Lo: 100, Hi: 1000},
	{Base: "CHF", Quote: "KZT"}: {Lo: 100, Hi: 1000},
	{Base: "CAD", Quote: "KZT"}: {Lo: 100, Hi: 1000},
	{Base: "AED", Quote: "KZT"}: {Lo: 50, Hi: 500}, // AED ≈ USD / 3.67

	// CIS / emerging
	{Base: "RUB", Quote: "KZT"}: {Lo: 1, Hi: 30},
	{Base: "RUR", Quote: "KZT"}: {Lo: 1, Hi: 30}, // legacy alias used by qazpost JSON
	{Base: "TRY", Quote: "KZT"}: {Lo: 1, Hi: 50},

	// Small per-unit quotes
	{Base: "JPY", Quote: "KZT"}: {Lo: 0.5, Hi: 10},
	{Base: "UZS", Quote: "KZT"}: {Lo: 0.01, Hi: 1},

	// Metals (per troy ounce in KZT); ranges are intentionally wide to
	// survive multi-decade price swings.
	{Base: "GOLD", Quote: "KZT"}:   {Lo: 100_000, Hi: 5_000_000},
	{Base: "SILVER", Quote: "KZT"}: {Lo: 1_000, Hi: 100_000},
}

// plausibleRangeFor returns the inclusive plausibility window [lo, hi] for the
// given base/quote pair. When ok is false the pair is not in the table and the
// caller must fall back to the universal range check.
//
// Lookup is case-sensitive: ISO codes are always uppercase here, so a lowercase
// code ("usd") indicates an upstream bug and will not match — surfacing it
// loudly rather than silently accepting it.
//
// Bounds are inclusive: a value v is acceptable when lo <= v <= hi. A boundary
// value of exactly lo must not be rejected — real rates land on round numbers.
func plausibleRangeFor(base, quote string) (lo, hi float64, ok bool) {
	r, ok := plausibleRanges[currencyPair{Base: base, Quote: quote}]
	if !ok {
		return 0, 0, false
	}
	return r.Lo, r.Hi, true
}
