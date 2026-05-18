// Package routes centralises all HTTP route path constants for the v1 API.
// Using constants prevents typos and makes the full API surface auditable at a glance.
package routes

const (
	// Sources lists all configured rate sources.
	Sources = "/api/sources"

	// SourceRatesChart returns aggregated chart data for a named source.
	// Must be registered BEFORE SourceRates so the more-specific pattern wins.
	SourceRatesChart = "/api/sources/{name}/rates/chart"

	// SourceRates returns recent rate values for a named source.
	// The {name} segment maps to r.PathValue("name") in Go 1.22+.
	SourceRates = "/api/sources/{name}/rates"

	// SourceHistory returns execution history for a named source.
	SourceHistory = "/api/sources/{name}/history"

	// SourceEventsFailed returns paginated failed events for a named source.
	SourceEventsFailed = "/api/sources/{name}/events/failed"

	// SourceSubscriptions returns grouped subscription statistics for a named source.
	SourceSubscriptions = "/api/sources/{name}/subscriptions"

	// EventsPending returns all currently pending notification events.
	EventsPending = "/api/events/pending"

	// Notifications returns the last N notification pool records.
	// Registered before NotificationsFailed to avoid prefix shadowing.
	Notifications = "/api/notifications"

	// NotificationsFailed returns all failed notification pool records.
	NotificationsFailed = "/api/notifications/failed"

	// SourceToggleActive enables or disables a named source.
	SourceToggleActive = "/api/sources/{name}/active"

	// SourceSubscriptionsList returns paginated subscription details for a named source.
	SourceSubscriptionsList = "/api/sources/{name}/subscriptions/list"

	// SourceEventsDaily returns daily aggregated event counts for a named source.
	SourceEventsDaily = "/api/sources/{name}/events/daily"

	// Stats returns global application statistics.
	Stats = "/api/stats"

	// ErrorsExecution returns the most recent failed execution history records.
	ErrorsExecution = "/api/errors/execution"

	// MeSubscriptions returns the calling user's own subscriptions enriched with the
	// latest rate value per source. Authentication is via Telegram WebApp initData HMAC.
	MeSubscriptions = "/api/me/subscriptions"

	// SourceRulesGenerate triggers the rule-generator audit loop for a named source.
	// Admin-only: authenticated via Telegram WebApp initData with chat-id check.
	// Synchronous; up to 120s wall-clock. More specific than any existing GET on
	// /api/sources/{name}/... so Go's ServeMux longest-prefix rule routes it correctly.
	SourceRulesGenerate = "/api/sources/{name}/rules/generate"
)
