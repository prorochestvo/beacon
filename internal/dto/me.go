package dto

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

// MeSubscriptionEditRow is one row in the per-condition subscriptions list
// returned by GET /api/me/subscriptions/raw. Each row maps to exactly one
// domain.RateUserSubscription so the stable ID can drive PATCH and DELETE.
//
// UserID is never returned — the endpoint is scoped to the authenticated caller.
// LatestPrice and LatestAt are omitted: the editor does not need them, and
// including them duplicates the join cost already paid by ListMeSubscriptions.
type MeSubscriptionEditRow struct {
	ID             string `json:"id"`
	SourceName     string `json:"source_name"`
	SourceTitle    string `json:"source_title"`
	BaseCurrency   string `json:"base_currency"`
	QuoteCurrency  string `json:"quote_currency"`
	ConditionType  string `json:"condition_type"`
	ConditionValue string `json:"condition_value"`
	UpdatedAt      string `json:"updated_at"`
}

// MeSubscriptionsRawResponse is the JSON envelope returned by
// GET /api/me/subscriptions/raw. Items is always a non-nil slice.
type MeSubscriptionsRawResponse struct {
	Items []MeSubscriptionEditRow `json:"items"`
}

// MeSubscriptionCreateRequest is the JSON body for POST /api/me/subscriptions.
//
// SourceName must match an existing active rate source. ConditionType must be
// one of the recognised domain.SubscriptionConditionType values. ConditionValue
// is validated by domain.RateUserSubscription.Validate() server-side.
//
// UserID is never accepted in the request body — it is derived from the
// verified X-Telegram-Init-Data chat_id.
type MeSubscriptionCreateRequest struct {
	SourceName     string `json:"source_name"`
	ConditionType  string `json:"condition_type"`
	ConditionValue string `json:"condition_value"`
}

// MeSubscriptionCreateResponse is the JSON envelope for a successful
// POST /api/me/subscriptions (201 Created). Carries only the generated
// subscription ID so the client can PATCH/DELETE without re-fetching the list.
type MeSubscriptionCreateResponse struct {
	ID string `json:"id"`
}

// MeSubscriptionUpdateRequest is the JSON body for
// PATCH /api/me/subscriptions/{id}. Only condition fields may be updated;
// SourceName is intentionally excluded (changing source is a delete+create).
type MeSubscriptionUpdateRequest struct {
	ConditionType  string `json:"condition_type"`
	ConditionValue string `json:"condition_value"`
}

// MeProfileRequest is the JSON body for POST /api/me/profile.
//
// Timezone is an IANA name resolvable by time.LoadLocation
// (e.g. "Asia/Almaty"); the server validates it before persistence and
// returns 400 PublicError on failure.
//
// Locale is a BCP-47 tag (e.g. "ru-RU"). Stored verbatim — the server does
// not validate BCP-47 syntax because the failure mode is cosmetic (garbage
// yields no localisation match later). Empty string is acceptable: the WASM
// client always reads Intl, but a non-browser caller might omit it.
//
// By project policy this DTO never carries username / display-name / phone /
// email — see the no-PII memory.
type MeProfileRequest struct {
	Timezone string `json:"timezone"`
	Locale   string `json:"locale"`
}
