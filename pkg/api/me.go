package api

// MeSubscriptionRow is one row in the Mini App subscriptions response.
// Groups all conditions for the same source into a single row.
// UserID is never returned — the endpoint is scoped to the authenticated caller.
type MeSubscriptionRow struct {
	SourceName    string   `json:"source_name"`
	SourceTitle   string   `json:"source_title"`
	BaseCurrency  string   `json:"base_currency"`
	QuoteCurrency string   `json:"quote_currency"`
	Conditions    []string `json:"conditions"`
	LatestPrice   float64  `json:"latest_price,omitempty"`
	LatestAt      string   `json:"latest_at,omitempty"`
}

// MeSubscriptionsResponse is the JSON envelope returned by GET /api/me/subscriptions.
type MeSubscriptionsResponse struct {
	Items    []MeSubscriptionRow `json:"items"`
	Page     int64               `json:"page"`
	PageSize int64               `json:"page_size"`
	Total    int64               `json:"total"`
}
