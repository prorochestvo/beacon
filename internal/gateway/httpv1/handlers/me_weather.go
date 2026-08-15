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

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4 KiB
	var body dto.WeatherCityCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	id, err := h.meWeatherSvc.CreateMeCity(r.Context(), strconv.FormatInt(userID, 10), appweather.NewCity{
		LocationID:     body.LocationID,
		DisplayName:    body.DisplayName,
		Latitude:       body.Latitude,
		Longitude:      body.Longitude,
		Timezone:       body.Timezone,
		Country:        body.Country,
		Admin1:         body.Admin1,
		NotifyKind:     domain.WeatherNotifyKind(body.NotifyKind),
		NotifyHour:     body.NotifyHour,
		ConditionValue: body.ConditionValue,
	})
	if err != nil {
		h.meWeatherWriteError(w, err, "CreateMeWeatherCity")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(dto.WeatherCityCreateResponse{ID: id}); err != nil {
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
// 409 when the row is one of the forced, system-managed kinds; the ownership check
// runs first, so cross-user stays 404 and never reveals that a forced row is there.
// 500 on persistence failure.
func (h *Handler) DeleteMeWeatherCity(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.callerID(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing city id"}`, http.StatusBadRequest)
		return
	}

	if err := h.meWeatherSvc.DeleteMeCity(r.Context(), strconv.FormatInt(userID, 10), id); err != nil {
		h.meWeatherWriteError(w, err, "DeleteMeWeatherCity")
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

	locationID := r.PathValue("location_id")
	if strings.TrimSpace(locationID) == "" {
		http.Error(w, `{"error":"missing location id"}`, http.StatusBadRequest)
		return
	}

	if err := h.meWeatherSvc.DeleteMeLocation(r.Context(), strconv.FormatInt(userID, 10), locationID); err != nil {
		h.meWeatherWriteError(w, err, "DeleteMeWeatherLocation")
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

const (
	// weatherGeoTimeout is the per-request deadline for outbound geocoding calls.
	// A slow Open-Meteo response must not stall the HTTP worker.
	weatherGeoTimeout = 5 * time.Second
	// weatherSearchMaxResults is the number of geocoding matches requested.
	weatherSearchMaxResults = 5
)

// meWeatherCityNotFound is the answer for a city subscription that does not
// exist and for one owned by somebody else. One message under one status:
// telling the two apart would confirm another user's city exists.
const meWeatherCityNotFound = "city not found"

// meWeatherForced is the fallback explanation for a forced, system-managed row
// that cannot be deleted on its own. The service normally supplies the message
// naming the kind; this stands in if it ever does not.
const meWeatherForced = "This alert stays on for every tracked city; remove the city to turn it off."

// meWeatherService is the application service behind the whole
// /api/v1/me/weather family bar the geocoding search, satisfied by
// *appweather.Service. Location dedup, "not collected yet" as a state rather
// than an error, field validation, ownership and the forced alert rows all live
// there; this package parses the request and renders the answer.
type meWeatherService interface {
	ObtainMeCities(ctx context.Context, userID string) ([]domain.WeatherUserCity, error)
	ObtainMeCurrent(ctx context.Context, userID string) ([]appweather.CurrentCity, error)
	CreateMeCity(ctx context.Context, userID string, req appweather.NewCity) (string, error)
	DeleteMeCity(ctx context.Context, userID, id string) error
	DeleteMeLocation(ctx context.Context, userID, locationID string) error
}

// weatherGeocoder is the geocoding contract used by SearchWeatherCities. It
// returns display-ready search items with resolved location_id, coordinates,
// and IANA timezone. The implementation calls an external geocoding API; callers
// must supply a bounded context to avoid long-held worker goroutines.
type weatherGeocoder interface {
	Geocode(ctx context.Context, name string, count int) ([]dto.WeatherCitySearchItem, error)
}

// meWeatherWriteError renders a failure returned by the weather application
// service.
//
// The three answers are ordered by what they disclose. ErrForcedSubscription is
// only ever reached after ownership has been settled, so a 409 says nothing
// about a row the caller does not own. internal.ErrNotFound covers a missing row
// and somebody else's alike. A *internal.PublicError is the caller's own
// mistake, and anything left is the store's.
//
// The body is encoded rather than concatenated: these messages can carry a value
// the caller sent, and a quote in one would otherwise break the document.
func (h *Handler) meWeatherWriteError(w http.ResponseWriter, err error, logContext string) {
	switch {
	case errors.Is(err, appweather.ErrForcedSubscription):
		h.publicErrorJSON(w, publicErrorMessage(err, meWeatherForced), http.StatusConflict, logContext)
	case errors.Is(err, internal.ErrNotFound):
		h.publicErrorJSON(w, meWeatherCityNotFound, http.StatusNotFound, logContext)
	default:
		var pub *internal.PublicError
		if errors.As(err, &pub) {
			h.publicErrorJSON(w, pub.Details(), http.StatusBadRequest, logContext)
			return
		}
		h.internalError(w, fmt.Errorf("%s: %w", logContext, err))
	}
}

// publicErrorMessage returns the user-facing text err carries, or fallback when
// it carries none.
func publicErrorMessage(err error, fallback string) string {
	var pub *internal.PublicError
	if errors.As(err, &pub) {
		return pub.Details()
	}
	return fallback
}
