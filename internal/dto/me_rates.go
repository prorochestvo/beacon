package dto

import "time"

// MeHistoryResponse is the JSON envelope returned by GET /api/me/rates/history.
// Items is sorted newest first.
type MeHistoryResponse struct {
	// Pair is the canonical pair label (e.g. "USD/KZT") the results are scoped to.
	Pair string `json:"pair"`
	// Page is the 1-based page index returned.
	Page int `json:"page"`
	// Limit is the page size used for this response.
	Limit int `json:"limit"`
	// Total is the unpaginated count of matching rows.
	Total int64 `json:"total"`
	// Items contains the history rows for this page, newest first.
	Items []MeHistoryRow `json:"items"`
}

// MeHistoryRow is one rate-collection event for the requested pair.
// Bid, Ask, and Last are pointers so a one-direction source emits exactly
// the direction it owns. Delta fields are nil for the first observation in
// their (title, direction) chain within the window.
type MeHistoryRow struct {
	// SourceTitle is the human-readable provider title (e.g. "Center Credit Bank (FX)").
	// This is the grouping key: BID and ASK rows from sibling sources sharing the
	// same title and timestamp are collapsed into one MeHistoryRow.
	SourceTitle string `json:"source_title"`
	// Timestamp is when the collector scraped this value.
	Timestamp time.Time `json:"timestamp"`
	// Bid is the BID price; nil when the source only tracks ASK or LAST.
	Bid *float64 `json:"bid,omitempty"`
	// Ask is the ASK price; nil when the source only tracks BID or LAST.
	Ask *float64 `json:"ask,omitempty"`
	// Last is the last-traded (equity) price; nil for BID/ASK-only sources.
	Last *float64 `json:"last,omitempty"`
	// BidDeltaPct is the percent change from the previous BID observation in
	// this (title, direction) chain within the page; nil for the first row.
	BidDeltaPct *float64 `json:"bid_delta_pct,omitempty"`
	// AskDeltaPct is the percent change from the previous ASK observation in
	// this (title, direction) chain within the page; nil for the first row.
	AskDeltaPct *float64 `json:"ask_delta_pct,omitempty"`
	// LastDeltaPct is the percent change from the previous LAST observation in
	// this (title, direction) chain within the page; nil for the first row.
	LastDeltaPct *float64 `json:"last_delta_pct,omitempty"`
}

// MeChartResponse is the JSON envelope returned by GET /api/me/rates/chart.
// Window is a human-readable label for the chart's time range (e.g. "7 days").
type MeChartResponse struct {
	Window string           `json:"window"`
	Pairs  []MeChartPairRow `json:"pairs"`
}

// MeChartPairRow holds the sparkline data for one canonical currency pair.
// Pair is the display label derived from the BID-natural direction (e.g.
// "USD/KZT"). Series contains 1 or 2 entries: BID and/or ASK. SpreadPct is
// the relative spread (ASK-BID)/BID*100, present only when both directions
// exist and both have a non-zero Latest.
type MeChartPairRow struct {
	Pair      string          `json:"pair"`
	Category  string          `json:"category"`
	SpreadPct *float64        `json:"spread_pct,omitempty"`
	Series    []MeChartSeries `json:"series"`
}

// MeChartSeries holds the sparkline data for one direction (BID or ASK) within
// a pair row. Color is the role-based hex: ratepair.ColorBid for BID,
// ratepair.ColorAsk for ASK. Sparse is true when fewer than two data points
// were found in the requested window.
//
// EffectiveDays is the number of days covered by this series. Always >= 1 when
// Sparse==false and len(Points)>0; zero is reserved for the sparse/no-data case.
// A value below the requested period means the X-axis was capped to actual data
// coverage; the renderer should show "(max available)". Do NOT add omitempty: the
// WASM client relies on the zero value being on the wire to distinguish
// "sparse/no-data" from "field missing entirely".
type MeChartSeries struct {
	Kind          string         `json:"kind"`
	Color         string         `json:"color"`
	Latest        float64        `json:"latest"`
	DeltaPct      float64        `json:"delta_pct"`
	Sparse        bool           `json:"sparse"`
	EffectiveDays int            `json:"effective_days"`
	Points        []MeChartPoint `json:"points,omitempty"`
}

// MeChartPoint is one downsampled point in a sparkline series.
type MeChartPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// PublicChartResponse is the JSON envelope returned by GET /api/public/rates/chart.
// Pairs reuses MeChartPairRow so both endpoints share the same chart-row wire
// shape. The pagination fields (Page, Limit, Total) are absent from
// MeChartResponse to keep the two contracts independent.
type PublicChartResponse struct {
	// Window is a human-readable label for the chart's time range (e.g. "7 days").
	Window string `json:"window"`
	// Page is the 1-based page index returned.
	Page int `json:"page"`
	// Limit is the page size used for this response.
	Limit int `json:"limit"`
	// Total is the unpaginated count of PairRows (after BID+ASK grouping).
	Total int64 `json:"total"`
	// Pairs is the sparkline-list for this page. Never JSON null; empty array
	// when no active sources exist or the page is out of range.
	Pairs []MeChartPairRow `json:"pairs"`
}
