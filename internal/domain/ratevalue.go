package domain

import "time"

// SourcePairKey identifies a unique (source, base, quote, kind) tuple used to
// bulk-load time-series data for a user's subscriptions. Kind is the source's
// rate direction; it is not stored in rate_values directly (the column lives in
// rate_sources).
type SourcePairKey struct {
	SourceName    string
	BaseCurrency  string
	QuoteCurrency string
	Kind          RateSourceKind
}

// RateValue represents a single exchange rate data point.
type RateValue struct {
	ID            string
	SourceName    string
	BaseCurrency  string
	QuoteCurrency string
	Price         float64

	// Timestamp is when this project stored the value, not when the source quoted it.
	// The repository sets it on write and ignores whatever a caller passes, because
	// no source publishes a quote time this project reads — the extraction rules
	// produce a number and nothing else.
	//
	// Consequences worth knowing before relying on it: a gap in collection shows up
	// here as a gap in the series, and the latest-rate query orders by this column
	// with the id as tie-break, so the ordering depends on our clock alone. If market
	// time is ever needed, it belongs in a separate nullable column rather than in
	// this one.
	Timestamp time.Time
}
