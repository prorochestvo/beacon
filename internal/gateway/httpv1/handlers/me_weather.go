package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	appweather "github.com/seilbekskindirov/beacon/internal/application/weather"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// SearchWeatherCities calls the geocoding provider and returns the top matches
// for the q query parameter. Auth is required so the endpoint cannot be used
// as an open geocoding proxy.
//
// GET /api/v1/me/weather/cities/search?q=<city>
// Auth: X-Telegram-Init-Data header only.
//
// 200 with WeatherCitySearchResponse on success.
// 400 when q is absent or empty.
// 401 on auth failure.
func (h *Handler) SearchWeatherCities(w http.ResponseWriter, r *http.Request) {
	// This is the one /api/v1/me handler with no use for the caller's id, and it asks
	// for it anyway. Every other handler is protected twice — by the mount, and by
	// failing closed when the id is absent — and skipping the second here would make
	// this the only route that serves anyone if it were ever registered outside the
	// authenticated mux. Verified: removing the middleware from the mount fails this
	// route's guard cases and no others.
	if _, ok := h.callerID(w, r); !ok {
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		pub := internal.NewPublicError("q is required")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}

	// Bound the external geocoding call so a slow provider cannot hold the worker.
	geoCtx, cancel := context.WithTimeout(r.Context(), weatherGeoTimeout)
	defer cancel()

	items, err := h.weatherGeocoder.Geocode(geoCtx, q, weatherSearchMaxResults)
	if err != nil {
		h.internalError(w, fmt.Errorf("SearchWeatherCities geocode: %w", err))
		return
	}

	if items == nil {
		items = []dto.WeatherCitySearchItem{}
	}
	writeJSON(w, dto.WeatherCitySearchResponse{Items: items})
}

// ListMeWeatherCities returns the authenticated caller's saved city subscriptions.
//
// GET /api/v1/me/weather/cities
// Auth: X-Telegram-Init-Data header only.
func (h *Handler) ListMeWeatherCities(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.callerID(w, r)
	if !ok {
		return
	}
	tgUserID := strconv.FormatInt(userID, 10)

	cities, err := h.meWeatherSvc.ObtainMeCities(r.Context(), tgUserID)
	if err != nil {
		h.internalError(w, fmt.Errorf("ListMeWeatherCities: %w", err))
		return
	}

	rows := make([]dto.WeatherCityRow, 0, len(cities))
	for _, c := range cities {
		rows = append(rows, dto.WeatherCityRow{
			ID:             c.ID,
			LocationID:     c.LocationID,
			DisplayName:    c.DisplayName,
			Latitude:       c.Latitude,
			Longitude:      c.Longitude,
			Timezone:       c.Timezone,
			Country:        c.Country,
			Admin1:         c.Admin1,
			NotifyHour:     c.NotifyHour,
			NotifyKind:     string(c.NotifyKind),
			ConditionValue: c.ConditionValue,
		})
	}
	writeJSON(w, dto.WeatherCitiesResponse{Items: rows})
}

// CreateMeWeatherCity persists a city weather subscription for the authenticated
// caller. Server-side validation covers timezone (time.LoadLocation), notify_hour
// in [0,23], and coordinate range checks. The client must copy fields verbatim
// from the search result; lat/lng/timezone are not re-geocoded here.
//
// POST /api/v1/me/weather/cities
// Body: WeatherCityCreateRequest
// Auth: X-Telegram-Init-Data header only.
//
// 201 Created with WeatherCityCreateResponse on success.
// 400 with a PublicError body on validation failure.
// 401 on auth failure.
// 500 on persistence failure.
func (h *Handler) CreateMeWeatherCity(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.callerID(w, r)
	if !ok {
		return
	}
	tgUserID := strconv.FormatInt(userID, 10)

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4 KiB
	var body dto.WeatherCityCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Server-side validation — never trust client-supplied geocoding fields.
	if strings.TrimSpace(body.LocationID) == "" {
		pub := internal.NewPublicError("location_id is required")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.DisplayName) == "" {
		pub := internal.NewPublicError("display_name is required")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}
	if _, err := time.LoadLocation(body.Timezone); err != nil {
		pub := internal.NewPublicError("invalid timezone: must be a valid IANA timezone name")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}
	if body.Latitude < -90 || body.Latitude > 90 {
		pub := internal.NewPublicError("latitude must be between -90 and 90")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}
	if body.Longitude < -180 || body.Longitude > 180 {
		pub := internal.NewPublicError("longitude must be between -180 and 180")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}

	notifyHour := weatherDefaultNotifyHour
	if body.NotifyHour != nil {
		notifyHour = *body.NotifyHour
	}
	if notifyHour < 0 || notifyHour > 23 {
		pub := internal.NewPublicError("notify_hour must be between 0 and 23")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}

	// Determine notify_kind: default to morning_summary when omitted.
	notifyKind := domain.WeatherNotifyMorningSummary
	if body.NotifyKind != "" {
		notifyKind = domain.WeatherNotifyKind(body.NotifyKind)
	}

	// morning_summary and alert_thaw ignore condition_value; normalize to empty
	// so arbitrary free text is never stored for a kind that does not use it.
	// (alert_thunderstorm is not blanked here — pre-existing asymmetry, out of
	// scope for this change.)
	conditionValue := body.ConditionValue
	if notifyKind == domain.WeatherNotifyMorningSummary || notifyKind == domain.WeatherNotifyAlertThaw {
		conditionValue = ""
	}

	record := &domain.WeatherUserCity{
		UserType:       domain.UserTypeTelegram,
		UserID:         tgUserID,
		LocationID:     body.LocationID,
		DisplayName:    body.DisplayName,
		Latitude:       body.Latitude,
		Longitude:      body.Longitude,
		Timezone:       body.Timezone,
		Country:        body.Country,
		Admin1:         body.Admin1,
		NotifyKind:     notifyKind,
		NotifyHour:     notifyHour,
		ConditionValue: conditionValue,
	}

	// Validate the (kind, condition_value) pair. Validate() returns a plain error
	// whose message is safe to surface directly to the user. json.NewEncoder is used
	// here instead of string concatenation so a condition_value containing a quote
	// cannot corrupt the JSON.
	if valErr := record.Validate(); valErr != nil {
		pub := internal.NewPublicError(valErr.Error())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if encErr := json.NewEncoder(w).Encode(map[string]string{"error": pub.Details()}); encErr != nil {
			h.logger.Print(errors.Join(fmt.Errorf("encode validation error response: %w", encErr), loginjector.NewTraceError()))
		}
		return
	}

	if err := h.meWeatherCityRepo.RetainWeatherUserCity(r.Context(), record); err != nil {
		h.internalError(w, fmt.Errorf("CreateMeWeatherCity retain: %w", err))
		return
	}

	// alert_thaw and rain_alert are forced and system-managed: every tracked city carries
	// exactly one of each, so any add ensures the ones this request did not itself create.
	// Built from the already-validated body.* fields, not from record —
	// RetainWeatherUserCity mutates record's ID/timestamps in place.
	//
	// The AlertLatched seed differs per kind, and the asymmetry is deliberate:
	//
	//   - thaw starts pre-latched (true), matching migration 202607.021: it fires on
	//     TempMax > 0 alone, the default warm-season state, so an armed row would risk
	//     firing on the very first check tick for a city already past freezing. A
	//     genuinely still-frozen city re-arms to false on the next tick with no
	//     notification either way.
	//   - rain starts armed (false), matching migration 202608.026: it notifies on BOTH
	//     latch edges, so a pre-latched row would emit a spurious "rain cleared" on the
	//     first tick, "no rain" being the normal state. Armed, the only first-tick message
	//     it can produce is a truthful "rain expected".
	//
	// keepExisting rows are ensured only when absent, so re-adding any other alert kind for
	// the same city cannot stomp a rain threshold the user retuned (the upsert does rewrite
	// condition_value).
	forced := []struct {
		kind           domain.WeatherNotifyKind
		conditionValue string
		alertLatched   bool
		keepExisting   bool
	}{
		{domain.WeatherNotifyAlertThaw, "", true, false},
		{domain.WeatherNotifyAlertRain, weatherDefaultRainThreshold, false, true},
	}
	for _, f := range forced {
		if notifyKind == f.kind {
			continue // the request itself created this row
		}
		if f.keepExisting {
			exists, existsErr := h.weatherKindExists(r.Context(), tgUserID, body.LocationID, f.kind)
			if existsErr != nil {
				h.internalError(w, fmt.Errorf("CreateMeWeatherCity ensure %s: %w", f.kind, existsErr))
				return
			}
			if exists {
				continue
			}
		}
		row := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         tgUserID,
			LocationID:     body.LocationID,
			DisplayName:    body.DisplayName,
			Latitude:       body.Latitude,
			Longitude:      body.Longitude,
			Timezone:       body.Timezone,
			Country:        body.Country,
			Admin1:         body.Admin1,
			NotifyKind:     f.kind,
			NotifyHour:     weatherDefaultNotifyHour,
			ConditionValue: f.conditionValue,
			AlertLatched:   f.alertLatched,
		}
		if err := h.meWeatherCityRepo.RetainWeatherUserCity(r.Context(), row); err != nil {
			h.internalError(w, fmt.Errorf("CreateMeWeatherCity ensure %s: %w", f.kind, err))
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(dto.WeatherCityCreateResponse{ID: record.ID}); err != nil {
		h.logger.Print(errors.Join(
			fmt.Errorf("encode CreateMeWeatherCity response: %w", err),
			loginjector.NewTraceError(),
		))
	}
}

// DeleteMeWeatherCity removes a city subscription owned by the authenticated caller.
//
// DELETE /api/v1/me/weather/cities/{id}
// Auth: X-Telegram-Init-Data header only.
//
// 204 No Content on success.
// 401 on auth failure.
// 404 on missing city or cross-user access (same response — no existence disclosure).
// 500 on persistence failure.
func (h *Handler) DeleteMeWeatherCity(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.callerID(w, r)
	if !ok {
		return
	}
	tgUserID := strconv.FormatInt(userID, 10)

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing city id"}`, http.StatusBadRequest)
		return
	}

	city := h.meWeatherCityOwnershipCheck(w, r, id, tgUserID)
	if city == nil {
		return
	}

	if notice, forced := forcedWeatherKindNotice(city.NotifyKind); forced {
		// Forced, system-managed row: it can only be removed by removing the whole
		// city (DELETE /api/v1/me/weather/locations/{location_id}). Reached only via
		// the API — the UI renders no delete control for these kinds.
		pub := internal.NewPublicError(notice)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		if encErr := json.NewEncoder(w).Encode(map[string]string{"error": pub.Details()}); encErr != nil {
			h.logger.Print(errors.Join(fmt.Errorf("encode DeleteMeWeatherCity forced-kind-conflict response: %w", encErr), loginjector.NewTraceError()))
		}
		return
	}

	if err := h.meWeatherCityRepo.RemoveWeatherUserCity(r.Context(), city); err != nil {
		h.internalError(w, fmt.Errorf("DeleteMeWeatherCity remove: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteMeWeatherLocation removes every city subscription (all notify kinds, including the
// forced alert_thaw row) owned by the authenticated caller at the given location.
//
// DELETE /api/v1/me/weather/locations/{location_id}
// Auth: X-Telegram-Init-Data header only.
//
// 204 No Content on success.
// 400 when location_id is missing.
// 401 on auth failure.
// 404 when the caller has no rows at this location (missing or cross-user — same response,
// no existence disclosure): the repository's atomic delete reports zero rows affected.
// 500 on persistence failure.
func (h *Handler) DeleteMeWeatherLocation(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.callerID(w, r)
	if !ok {
		return
	}
	tgUserID := strconv.FormatInt(userID, 10)

	locationID := r.PathValue("location_id")
	if strings.TrimSpace(locationID) == "" {
		http.Error(w, `{"error":"missing location id"}`, http.StatusBadRequest)
		return
	}

	// The repository's atomic check-and-delete is the sole ownership check: 404 (not
	// 403) on internal.ErrNotFound covers both "no such location for anyone" and "owned
	// by another user" — no existence disclosure, consistent with
	// meWeatherCityOwnershipCheck. No pre-scan needed.
	if err := h.meWeatherCityRepo.RemoveWeatherUserCitiesByLocation(r.Context(), domain.UserTypeTelegram, tgUserID, locationID); err != nil {
		if errors.Is(err, internal.ErrNotFound) {
			pub := internal.NewPublicError("city not found")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			if encErr := json.NewEncoder(w).Encode(map[string]string{"error": pub.Details()}); encErr != nil {
				h.logger.Print(errors.Join(fmt.Errorf("encode DeleteMeWeatherLocation not-found response: %w", encErr), loginjector.NewTraceError()))
			}
			return
		}
		h.internalError(w, fmt.Errorf("DeleteMeWeatherLocation remove: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMeWeatherCurrent returns the latest stored Open-Meteo observation for each
// distinct city the authenticated caller subscribes to. A city with multiple
// notify-kind rows (e.g. morning_summary + an alert) appears exactly once,
// deduplicated by location_id. A city whose first collection has not yet
// completed returns an item with has_data:false so the client can render a
// "no data yet" placeholder without treating the absence as an error.
//
// Sunrise and sunset times are pre-formatted as "15:04" in the city's IANA
// timezone so the WASM client requires no tzdata. A timezone that fails to load
// is skipped (the numeric fields are still returned).
//
// GET /api/v1/me/weather/current
// Auth: X-Telegram-Init-Data header only.
//
// 200 with WeatherCurrentResponse on success.
// 401 on auth failure.
// 500 on unexpected repo errors.
func (h *Handler) GetMeWeatherCurrent(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.callerID(w, r)
	if !ok {
		return
	}
	tgUserID := strconv.FormatInt(userID, 10)

	current, err := h.meWeatherSvc.ObtainMeCurrent(r.Context(), tgUserID)
	if err != nil {
		h.internalError(w, fmt.Errorf("GetMeWeatherCurrent: %w", err))
		return
	}

	items := make([]dto.WeatherCurrentItem, 0, len(current))
	for _, c := range current {
		items = append(items, weatherCurrentItem(c))
	}

	writeJSON(w, dto.WeatherCurrentResponse{Items: items})
}

// weatherCurrentItem renders one city and its latest observation for the wire.
// A nil observation is the "collector has not run for this city yet" case and
// renders as has_data:false with every reading absent, so a client never has to
// read an omitted number as a zero.
func weatherCurrentItem(c appweather.CurrentCity) dto.WeatherCurrentItem {
	item := dto.WeatherCurrentItem{
		LocationID:  c.City.LocationID,
		DisplayName: c.City.DisplayName,
		Timezone:    c.City.Timezone,
	}
	if c.Observation == nil {
		return item
	}
	obs := c.Observation

	item.HasData = true
	item.TempCurrent = obs.TempCurrent
	item.TempFeels = obs.TempFeels
	item.Humidity = obs.Humidity
	item.WindSpeed = obs.WindSpeed
	item.WindDir = obs.WindDir
	item.Precip = obs.Precip
	item.CloudCover = obs.CloudCover
	item.TempMax = obs.TempMax
	item.TempMin = obs.TempMin
	item.WeatherCode = obs.WeatherCode
	if obs.WeatherCode != nil {
		text, emoji := domain.WMOWeatherCode(*obs.WeatherCode)
		item.ConditionText = text
		item.ConditionEmoji = emoji
	}
	item.CapturedAt = obs.CapturedAt.UTC().Format(time.RFC3339)

	// Convert sunrise/sunset to city-local "15:04" strings server-side so the
	// WASM bundle needs no tzdata. A bad timezone skips only the sun times —
	// numeric fields are still returned.
	if c.City.Timezone != "" {
		if loc, locErr := time.LoadLocation(c.City.Timezone); locErr == nil {
			if obs.Sunrise != nil {
				item.SunriseLocal = obs.Sunrise.In(loc).Format("15:04")
			}
			if obs.Sunset != nil {
				item.SunsetLocal = obs.Sunset.In(loc).Format("15:04")
			}
		}
	}

	return item
}

// weatherKindExists reports whether the caller already owns a subscription of the given
// kind at the given location. Ensuring a forced kind that carries a user-tunable threshold
// has to be skipped when the row is already there, because RetainWeatherUserCity's upsert
// rewrites condition_value — without this check, adding any second alert to a city would
// silently reset a rain threshold the user had retuned.
func (h *Handler) weatherKindExists(ctx context.Context, userID, locationID string, kind domain.WeatherNotifyKind) (bool, error) {
	rows, err := h.meWeatherCityRepo.ObtainWeatherUserCitiesByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return false, err
	}
	for i := range rows {
		if rows[i].LocationID == locationID && rows[i].NotifyKind == kind {
			return true, nil
		}
	}
	return false, nil
}

// meWeatherCityOwnershipCheck loads the city by id, verifies the caller owns it,
// and returns it. On not-found or ownership mismatch it writes 404 and returns nil.
// On repo error it writes 500 and returns nil. Callers must return when nil is returned.
//
// The 404 response for a cross-user access is intentionally indistinguishable
// from a genuine miss to avoid existence disclosure.
func (h *Handler) meWeatherCityOwnershipCheck(w http.ResponseWriter, r *http.Request, id, tgUserID string) *domain.WeatherUserCity {
	city, err := h.meWeatherCityRepo.ObtainWeatherUserCityByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, internal.ErrNotFound) {
			pub := internal.NewPublicError("city not found")
			http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusNotFound)
			return nil
		}
		h.internalError(w, fmt.Errorf("weather city lookup: %w", err))
		return nil
	}
	if city.UserID != tgUserID {
		// 404 not 403 to avoid disclosing another user's city.
		pub := internal.NewPublicError("city not found")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusNotFound)
		return nil
	}
	return city
}

const (
	// weatherGeoTimeout is the per-request deadline for outbound geocoding calls.
	// A slow Open-Meteo response must not stall the HTTP worker.
	weatherGeoTimeout = 5 * time.Second
	// weatherSearchMaxResults is the number of geocoding matches requested.
	weatherSearchMaxResults = 5
	// weatherDefaultNotifyHour is the local hour used when the client omits notify_hour.
	weatherDefaultNotifyHour = 7
	// weatherDefaultRainThreshold is the precipitation probability percent seeded into the
	// forced rain_alert row of a newly tracked city. It matches the value the backfill
	// migration (202608.026) writes for pre-existing cities. Users retune it by re-adding
	// a rain alert with a different threshold, which upserts condition_value in place.
	weatherDefaultRainThreshold = "60"
)

// meWeatherCityRepository is the storage contract for the caller's city subscriptions.
type meWeatherCityRepository interface {
	RetainWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	ObtainWeatherUserCitiesByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.WeatherUserCity, error)
	ObtainWeatherUserCityByID(ctx context.Context, id string) (*domain.WeatherUserCity, error)
	RemoveWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	// RemoveWeatherUserCitiesByLocation deletes every subscription row (all notify
	// kinds, including the forced alert_thaw row) for one (userType, userID, locationID).
	// Returns internal.ErrNotFound when no row matches.
	RemoveWeatherUserCitiesByLocation(ctx context.Context, userType domain.UserType, userID, locationID string) error
}

// meWeatherService is the application service behind the caller's own weather
// reads, satisfied by *appweather.Service. Deduplicating a city tracked under
// several notify kinds, and treating "not collected yet" as a state rather than
// an error, both live there; this package renders what it returns.
type meWeatherService interface {
	ObtainMeCities(ctx context.Context, userID string) ([]domain.WeatherUserCity, error)
	ObtainMeCurrent(ctx context.Context, userID string) ([]appweather.CurrentCity, error)
}

// weatherGeocoder is the geocoding contract used by SearchWeatherCities. It
// returns display-ready search items with resolved location_id, coordinates,
// and IANA timezone. The implementation calls an external geocoding API; callers
// must supply a bounded context to avoid long-held worker goroutines.
type weatherGeocoder interface {
	Geocode(ctx context.Context, name string, count int) ([]dto.WeatherCitySearchItem, error)
}

// forcedWeatherKindNotice returns the user-facing explanation for a forced,
// system-managed subscription kind that cannot be deleted on its own, and ok=false for
// every kind the user may delete freely.
func forcedWeatherKindNotice(kind domain.WeatherNotifyKind) (notice string, ok bool) {
	switch kind {
	case domain.WeatherNotifyAlertThaw:
		return "Thaw alerts stay on for every tracked city; remove the city to turn it off.", true
	case domain.WeatherNotifyAlertRain:
		return "Rain alerts stay on for every tracked city; remove the city to turn it off.", true
	default:
		return "", false
	}
}
