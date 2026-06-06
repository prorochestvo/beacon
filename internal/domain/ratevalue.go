package domain

import "time"

// SourcePairKey identifies a unique (source, base, quote, kind) tuple used to
// bulk-load time-series data for a user's subscriptions. Kind is the rate
// direction emitted by the source, derived from domain.RateSourceKind; it is
// not stored in rate_values directly (the column lives in rate_sources).
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
	Timestamp     time.Time
}
