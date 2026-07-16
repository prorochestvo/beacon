// Package routes centralises all HTTP route path constants for the v1 API,
// preventing typos and making the full API surface auditable at a glance.
package routes

const (
	// Sources lists all configured rate sources.
	Sources = "/api/sources"

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

	// PublicRatesChart returns the paginated sparkline-list for all currency pairs
	// across active sources. No auth. Query params: page (default 1), limit
	// (default 20, max 100), period (one of 7, 30, 90, 180, 360 days, default 7).
	PublicRatesChart = "/api/public/rates/chart"

	// MeSubscriptions returns the calling user's own subscriptions enriched with the
	// latest rate value per source. Authentication is via Telegram WebApp initData HMAC.
	MeSubscriptions = "/api/me/subscriptions"

	// MeRatesChart returns sparkline-list chart data for the calling user's
	// subscribed currency pairs over the requested period (one of 7, 30, 90, 180,
	// 360 days, default 7). Auth via Telegram WebApp initData HMAC
	// (X-Telegram-Init-Data header only; no query parameter, to keep initData out
	// of access logs).
	MeRatesChart = "/api/me/rates/chart"

	// MeRatesHistory returns paginated rate-collection events for the calling
	// user's subscribed sources matching a canonical pair label. Auth via Telegram
	// WebApp initData HMAC (X-Telegram-Init-Data header only; no query parameter,
	// to keep initData out of access logs).
	MeRatesHistory = "/api/me/rates/history"

	// MeSubscriptionsRaw returns the calling user's subscriptions as one row per
	// condition (not grouped by source), with stable subscription IDs suitable for
	// PATCH and DELETE. Authentication is via Telegram WebApp initData HMAC.
	MeSubscriptionsRaw = "/api/me/subscriptions/raw"

	// MeSubscriptionByID is the pattern for single-subscription endpoints:
	// PATCH to update condition fields, DELETE to remove. Cross-user access
	// returns 404 (same body as a genuine miss) to avoid existence disclosure.
	MeSubscriptionByID = "/api/me/subscriptions/{id}"

	// MeProfile upserts the calling user's profile preferences (currently only
	// IANA timezone). Auth via Telegram WebApp initData HMAC, same as MeSubscriptions.
	MeProfile = "/api/me/profile"

	// MeWeatherCurrent returns the latest stored Open-Meteo observation for each
	// of the caller's subscribed cities (one item per distinct location_id).
	// HasData is false for a city whose first collection has not yet completed.
	// Auth via Telegram WebApp initData HMAC (X-Telegram-Init-Data header only).
	MeWeatherCurrent = "/api/me/weather/current"

	// MeWeatherCitiesSearch returns geocoding matches for the ?q= query string.
	// Must be registered before MeWeatherCities to win longest-path matching.
	// Auth via Telegram WebApp initData HMAC (X-Telegram-Init-Data header only).
	MeWeatherCitiesSearch = "/api/me/weather/cities/search"

	// MeWeatherCities is the collection endpoint for the caller's saved city
	// weather subscriptions (GET list, POST create). Auth via Telegram WebApp
	// initData HMAC (X-Telegram-Init-Data header only).
	MeWeatherCities = "/api/me/weather/cities"

	// MeWeatherCityByID is the single-city endpoint (DELETE). Cross-user access
	// returns 404 (same body as a genuine miss) to avoid existence disclosure.
	// Deleting an alert_thaw row returns 409 (forced, system-managed subscription);
	// remove the whole city via MeWeatherLocationByID instead.
	// Auth via Telegram WebApp initData HMAC (X-Telegram-Init-Data header only).
	MeWeatherCityByID = "/api/me/weather/cities/{id}"

	// MeWeatherLocationByID is the location-scoped delete endpoint (DELETE). Removes every
	// subscription row for the caller at this location, including the forced alert_thaw row.
	// Cross-user / no-rows access returns 404 (no existence disclosure).
	MeWeatherLocationByID = "/api/me/weather/locations/{location_id}"

	// Ping is the liveness probe. Touches no dependency; always returns 200. Registered
	// at /ping. /healthz is kept as a backward-compatible alias.
	Ping = "/ping"

	// Healthz is a backward-compatible alias for Ping; kept so existing monitoring
	// scripts and deploy gates that target /healthz continue to work unchanged.
	Healthz = "/healthz"

	// HealthCheck is the readiness probe. Runs all dependency inspectors under a
	// bounded timeout and returns a per-component JSON report. 200 when healthy,
	// 503 when any dependency is down. No auth; for deploy gates and uptime monitors.
	HealthCheck = "/health/check"
)
