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

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// meWeatherCityRepository is the storage contract for the caller's city subscriptions.
type meWeatherCityRepository interface {
	RetainWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	ObtainWeatherUserCitiesByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.WeatherUserCity, error)
	ObtainWeatherUserCityByID(ctx context.Context, id string) (*domain.WeatherUserCity, error)
	RemoveWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
}

// weatherGeocoder is the geocoding contract used by SearchWeatherCities. It
// returns display-ready search items with resolved location_id, coordinates,
// and IANA timezone. The implementation calls an external geocoding API; callers
// must supply a bounded context to avoid long-held worker goroutines.
type weatherGeocoder interface {
	Geocode(ctx context.Context, name string, count int) ([]dto.WeatherCitySearchItem, error)
}

// WithWeatherDeps injects the weather city repository and geocoder into the
// handler. Returns h to allow chaining after NewHandler. Both deps are
// nil-safe: if either is nil the weather endpoints return 503.
func (h *Handler) WithWeatherDeps(cityRepo meWeatherCityRepository, geocoder weatherGeocoder) *Handler {
	h.meWeatherCityRepo = cityRepo
	h.weatherGeocoder = geocoder
	return h
}

// SearchWeatherCities calls the geocoding provider and returns the top matches
// for the q query parameter. Auth is required so the endpoint cannot be used
// as an open geocoding proxy.
//
// GET /api/me/weather/cities/search?q=<city>
// Auth: X-Telegram-Init-Data header only.
//
// 200 with WeatherCitySearchResponse on success.
// 400 when q is absent or empty.
// 401 on auth failure.
// 503 when the weather service is not wired.
func (h *Handler) SearchWeatherCities(w http.ResponseWriter, r *http.Request) {
	if h.weatherGeocoder == nil {
		http.Error(w, `{"error":"weather service unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	initData := r.Header.Get("X-Telegram-Init-Data")
	if _, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn()); err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
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
// GET /api/me/weather/cities
// Auth: X-Telegram-Init-Data header only.
func (h *Handler) ListMeWeatherCities(w http.ResponseWriter, r *http.Request) {
	if h.meWeatherCityRepo == nil {
		http.Error(w, `{"error":"weather service unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	tgUserID := strconv.FormatInt(userID, 10)

	cities, err := h.meWeatherCityRepo.ObtainWeatherUserCitiesByUserID(r.Context(), domain.UserTypeTelegram, tgUserID)
	if err != nil {
		h.internalError(w, fmt.Errorf("ListMeWeatherCities: %w", err))
		return
	}

	rows := make([]dto.WeatherCityRow, 0, len(cities))
	for _, c := range cities {
		rows = append(rows, dto.WeatherCityRow{
			ID:          c.ID,
			LocationID:  c.LocationID,
			DisplayName: c.DisplayName,
			Latitude:    c.Latitude,
			Longitude:   c.Longitude,
			Timezone:    c.Timezone,
			Country:     c.Country,
			Admin1:      c.Admin1,
			NotifyHour:  c.NotifyHour,
		})
	}
	writeJSON(w, dto.WeatherCitiesResponse{Items: rows})
}

// CreateMeWeatherCity persists a city weather subscription for the authenticated
// caller. Server-side validation covers timezone (time.LoadLocation), notify_hour
// in [0,23], and coordinate range checks. The client must copy fields verbatim
// from the search result; lat/lng/timezone are not re-geocoded here.
//
// POST /api/me/weather/cities
// Body: WeatherCityCreateRequest
// Auth: X-Telegram-Init-Data header only.
//
// 201 Created with WeatherCityCreateResponse on success.
// 400 with a PublicError body on validation failure.
// 401 on auth failure.
// 500 on persistence failure.
func (h *Handler) CreateMeWeatherCity(w http.ResponseWriter, r *http.Request) {
	if h.meWeatherCityRepo == nil {
		http.Error(w, `{"error":"weather service unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
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

	record := &domain.WeatherUserCity{
		UserType:    domain.UserTypeTelegram,
		UserID:      tgUserID,
		LocationID:  body.LocationID,
		DisplayName: body.DisplayName,
		Latitude:    body.Latitude,
		Longitude:   body.Longitude,
		Timezone:    body.Timezone,
		Country:     body.Country,
		Admin1:      body.Admin1,
		NotifyKind:  domain.WeatherNotifyMorningSummary,
		NotifyHour:  notifyHour,
		// GismeteoCityID stays nil — populated only by the curated map in the second increment.
	}

	if err := h.meWeatherCityRepo.RetainWeatherUserCity(r.Context(), record); err != nil {
		h.internalError(w, fmt.Errorf("CreateMeWeatherCity retain: %w", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(dto.WeatherCityCreateResponse{ID: record.ID}); err != nil {
		h.logger.Print(errors.Join(
			fmt.Errorf("encode CreateMeWeatherCity response: %w", err),
			internal.NewTraceError(),
		))
	}
}

// DeleteMeWeatherCity removes a city subscription owned by the authenticated caller.
//
// DELETE /api/me/weather/cities/{id}
// Auth: X-Telegram-Init-Data header only.
//
// 204 No Content on success.
// 401 on auth failure.
// 404 on missing city or cross-user access (same response — no existence disclosure).
// 500 on persistence failure.
func (h *Handler) DeleteMeWeatherCity(w http.ResponseWriter, r *http.Request) {
	if h.meWeatherCityRepo == nil {
		http.Error(w, `{"error":"weather service unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
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

	if err := h.meWeatherCityRepo.RemoveWeatherUserCity(r.Context(), city); err != nil {
		h.internalError(w, fmt.Errorf("DeleteMeWeatherCity remove: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
)
