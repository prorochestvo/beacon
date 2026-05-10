package api

// SubscriptionSummaryResponse is the JSON shape of one source subscription summary row.
// UserID is never included so subscriber identifiers are not leaked via the API.
type SubscriptionSummaryResponse struct {
	SourceName        string `json:"source_name"`
	UserType          string `json:"user_type"`
	SubscriptionCount int64  `json:"subscription_count"`
	LastSentAt        string `json:"last_sent_at,omitempty"`
	SuccessCount      int64  `json:"success_count"`
	FailedCount       int64  `json:"failed_count"`
}

// SubscriptionDetailResponse is the JSON shape of one subscription detail row.
// UserID is never included to prevent leaking subscriber identifiers.
type SubscriptionDetailResponse struct {
	ID               string `json:"id"`
	UserType         string `json:"user_type"`
	SourceName       string `json:"source_name"`
	Condition        string `json:"condition"`
	LatestNotifiedAt string `json:"latest_notified_at,omitempty"`
}
