package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	appweather "github.com/seilbekskindirov/beacon/internal/application/weather"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ meWeatherService = (*mockMeWeatherSvc)(nil)
var _ weatherGeocoder = (*mockWeatherGeocoder)(nil)

// mockMeWeatherSvc is a test double for meWeatherService. It records what the
// handler derived from the request and replays whatever the test staged.
type mockMeWeatherSvc struct {
	cities    []domain.WeatherUserCity
	citiesErr error

	current    []appweather.CurrentCity
	currentErr error

	createID  string
	createErr error
	deleteErr error
	locErr    error

	gotUserID     string
	gotID         string
	gotLocationID string
	gotCreate     *appweather.NewCity
}

func (m *mockMeWeatherSvc) ObtainMeCities(_ context.Context, userID string) ([]domain.WeatherUserCity, error) {
	m.gotUserID = userID
	if m.citiesErr != nil {
		return nil, m.citiesErr
	}
	return m.cities, nil
}

func (m *mockMeWeatherSvc) ObtainMeCurrent(_ context.Context, userID string) ([]appweather.CurrentCity, error) {
	m.gotUserID = userID
	if m.currentErr != nil {
		return nil, m.currentErr
	}
	return m.current, nil
}

func (m *mockMeWeatherSvc) CreateMeCity(_ context.Context, userID string, req appweather.NewCity) (string, error) {
	m.gotUserID, m.gotCreate = userID, &req
	if m.createErr != nil {
		return "", m.createErr
	}
	if m.createID == "" {
		return "generated-id", nil
	}
	return m.createID, nil
}

func (m *mockMeWeatherSvc) DeleteMeCity(_ context.Context, userID, id string) error {
	m.gotUserID, m.gotID = userID, id
	return m.deleteErr
}

func (m *mockMeWeatherSvc) DeleteMeLocation(_ context.Context, userID, locationID string) error {
	m.gotUserID, m.gotLocationID = userID, locationID
	return m.locErr
}

// mockWeatherGeocoder is a test double for weatherGeocoder.
type mockWeatherGeocoder struct {
	items []dto.WeatherCitySearchItem
	err   error
}

func (m *mockWeatherGeocoder) Geocode(_ context.Context, _ string, _ int) ([]dto.WeatherCitySearchItem, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.items == nil {
		return []dto.WeatherCitySearchItem{}, nil
	}
	return m.items, nil
}

// newWeatherHandler builds a Handler wired with the given weather test doubles
// and a silenced logger so test output stays clean.
func newWeatherHandler(t *testing.T, svc meWeatherService, geo weatherGeocoder) *Handler {
	t.Helper()
	return newTestHandler(t, Config{
		MeWeatherSvc:    svc,
		WeatherGeocoder: geo,
		Logger:          log.New(io.Discard, "", 0),
	})
}

func TestHandler_SearchWeatherCities(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(77)

	t.Run("missing q parameter returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockMeWeatherSvc{}, &mockWeatherGeocoder{})
		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities/search", http.NoBody), callerUserID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "q is required")
	})

	t.Run("blank q parameter returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockMeWeatherSvc{}, &mockWeatherGeocoder{})
		// Raw spaces in URLs are invalid for httptest.NewRequest; encode them.
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities/search?q=%20%20%20", http.NoBody)
		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, withCaller(req, callerUserID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("geocoder error returns 500", func(t *testing.T) {
		t.Parallel()
		geo := &mockWeatherGeocoder{err: errors.New("upstream down")}
		h := newWeatherHandler(t, &mockMeWeatherSvc{}, geo)
		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities/search?q=Almaty", http.NoBody), callerUserID))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("happy path returns geocoding items", func(t *testing.T) {
		t.Parallel()
		geo := &mockWeatherGeocoder{items: []dto.WeatherCitySearchItem{
			{LocationID: "1234", DisplayName: "Almaty", Latitude: 43.25, Longitude: 76.94, Timezone: "Asia/Almaty", Country: "Kazakhstan", Admin1: "Almaty"},
			{LocationID: "5678", DisplayName: "Almatinka", Latitude: 43.10, Longitude: 76.80, Timezone: "Asia/Almaty", Country: "Kazakhstan", Admin1: "Almaty Region"},
		}}
		h := newWeatherHandler(t, &mockMeWeatherSvc{}, geo)
		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities/search?q=Almaty", http.NoBody), callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.WeatherCitySearchResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 2)
		assert.Equal(t, "1234", resp.Items[0].LocationID)
		assert.Equal(t, "Almaty", resp.Items[0].DisplayName)
		assert.Equal(t, "Asia/Almaty", resp.Items[0].Timezone)
	})

	t.Run("empty geocoder result returns empty items array (not null)", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockMeWeatherSvc{}, &mockWeatherGeocoder{})
		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities/search?q=xyzzy", http.NoBody), callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.WeatherCitySearchResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.NotNil(t, resp.Items)
		require.Empty(t, resp.Items)
	})

}

func TestHandler_ListMeWeatherCities(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(99)
	const callerIDStr = "99"

	t.Run("service error returns 500", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeWeatherSvc{citiesErr: errors.New("db down")}
		h := newWeatherHandler(t, svc, &mockWeatherGeocoder{})
		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities", http.NoBody), callerUserID))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("empty list returns empty items array", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockMeWeatherSvc{}, &mockWeatherGeocoder{})
		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities", http.NoBody), callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.WeatherCitiesResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.NotNil(t, resp.Items)
		require.Empty(t, resp.Items)
	})

	t.Run("happy path renders the caller's cities", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeWeatherSvc{cities: []domain.WeatherUserCity{
			{
				ID: "c1", UserType: domain.UserTypeTelegram, UserID: callerIDStr,
				LocationID: "1234", DisplayName: "Almaty", Latitude: 43.25, Longitude: 76.94,
				Timezone: "Asia/Almaty", Country: "Kazakhstan", Admin1: "Almaty",
				NotifyKind: domain.WeatherNotifyMorningSummary, NotifyHour: 7,
			},
		}}
		h := newWeatherHandler(t, svc, &mockWeatherGeocoder{})
		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/cities", http.NoBody), callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.WeatherCitiesResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Equal(t, "c1", resp.Items[0].ID)
		assert.Equal(t, "1234", resp.Items[0].LocationID)
		assert.Equal(t, "Almaty", resp.Items[0].DisplayName)
		assert.Equal(t, "Asia/Almaty", resp.Items[0].Timezone)
		assert.Equal(t, "morning_summary", resp.Items[0].NotifyKind)
		assert.Equal(t, 7, resp.Items[0].NotifyHour)
		assert.Equal(t, callerIDStr, svc.gotUserID, "the list is scoped to the authenticated caller")
	})
}

// TestHandler_CreateMeWeatherCity covers what the handler kept: reading the
// body, mapping it onto the service request, and turning the answer into a
// status. Which values are acceptable, and the forced alert rows every tracked
// city carries, are exercised in internal/application/weather.
func TestHandler_CreateMeWeatherCity(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(99)

	notifyHour := 9
	validBody := dto.WeatherCityCreateRequest{
		LocationID:     "1234",
		DisplayName:    "Almaty",
		Latitude:       43.25,
		Longitude:      76.94,
		Timezone:       "Asia/Almaty",
		Country:        "Kazakhstan",
		Admin1:         "Almaty",
		NotifyKind:     "alert_heat",
		NotifyHour:     &notifyHour,
		ConditionValue: "35",
	}

	bodyJSON := func(v any) *strings.Reader {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		return strings.NewReader(string(raw))
	}

	post := func(t *testing.T, svc meWeatherService, body io.Reader) *httptest.ResponseRecorder {
		t.Helper()
		h := newWeatherHandler(t, svc, &mockWeatherGeocoder{})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/me/weather/cities", body)
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, withCaller(req, callerUserID))
		return rr
	}

	t.Run("201 with the generated id, and every field forwarded", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeWeatherSvc{createID: "city-new"}
		rr := post(t, svc, bodyJSON(validBody))

		require.Equal(t, http.StatusCreated, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.WeatherCityCreateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, "city-new", resp.ID)

		assert.Equal(t, "99", svc.gotUserID, "ownership comes from the authenticated caller, never from the body")
		require.NotNil(t, svc.gotCreate)
		assert.Equal(t, "1234", svc.gotCreate.LocationID)
		assert.Equal(t, "Almaty", svc.gotCreate.DisplayName)
		assert.InDelta(t, 43.25, svc.gotCreate.Latitude, 0.0001)
		assert.InDelta(t, 76.94, svc.gotCreate.Longitude, 0.0001)
		assert.Equal(t, "Asia/Almaty", svc.gotCreate.Timezone)
		assert.Equal(t, "Kazakhstan", svc.gotCreate.Country)
		assert.Equal(t, "Almaty", svc.gotCreate.Admin1)
		assert.Equal(t, domain.WeatherNotifyAlertHeat, svc.gotCreate.NotifyKind)
		assert.Equal(t, "35", svc.gotCreate.ConditionValue)
		require.NotNil(t, svc.gotCreate.NotifyHour)
		assert.Equal(t, 9, *svc.gotCreate.NotifyHour)
	})

	t.Run("an omitted notify_hour stays absent rather than becoming zero", func(t *testing.T) {
		t.Parallel()

		body := validBody
		body.NotifyHour = nil
		svc := &mockMeWeatherSvc{}
		require.Equal(t, http.StatusCreated, post(t, svc, bodyJSON(body)).Code)

		require.NotNil(t, svc.gotCreate)
		assert.Nil(t, svc.gotCreate.NotifyHour,
			"flattening nil to 0 here would silently pick midnight instead of the default")
	})

	t.Run("400 on a malformed body, before the service is reached", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeWeatherSvc{}
		rr := post(t, svc, strings.NewReader("not-json"))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
		assert.Nil(t, svc.gotCreate)
	})

	t.Run("400 on a body exceeding 4 KiB", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeWeatherSvc{}
		rr := post(t, svc, strings.NewReader(strings.Repeat("x", 5<<10)))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Nil(t, svc.gotCreate)
	})

	t.Run("a PublicError from the service becomes a 400 carrying its message", func(t *testing.T) {
		t.Parallel()

		rr := post(t, &mockMeWeatherSvc{createErr: internal.NewPublicError("latitude must be between -90 and 90")}, bodyJSON(validBody))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		require.Contains(t, rr.Body.String(), "latitude must be between -90 and 90")
	})

	t.Run("500 on any other service failure, with the detail kept out of the body", func(t *testing.T) {
		t.Parallel()

		rr := post(t, &mockMeWeatherSvc{createErr: errors.New("db down")}, bodyJSON(validBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
		assert.NotContains(t, rr.Body.String(), "db down")
	})
}

func TestHandler_DeleteMeWeatherCity(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(33)

	del := func(t *testing.T, svc meWeatherService, id string) *httptest.ResponseRecorder {
		t.Helper()
		h := newWeatherHandler(t, svc, &mockWeatherGeocoder{})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/me/weather/cities/"+id, http.NoBody)
		req.SetPathValue("id", id)
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, withCaller(req, callerUserID))
		return rr
	}

	t.Run("204 on success, forwarding the caller and the id", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeWeatherSvc{}
		require.Equal(t, http.StatusNoContent, del(t, svc, "city-1").Code)
		assert.Equal(t, "33", svc.gotUserID)
		assert.Equal(t, "city-1", svc.gotID)
	})

	t.Run("400 on a missing id, before the service is reached", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeWeatherSvc{}
		rr := del(t, svc, "")

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "missing city id")
		assert.Empty(t, svc.gotID)
	})

	t.Run("404 on a missing city and on another user's", func(t *testing.T) {
		t.Parallel()

		rr := del(t, &mockMeWeatherSvc{deleteErr: internal.ErrNotFound}, "city-other")

		require.Equal(t, http.StatusNotFound, rr.Code,
			"another user's city is 404, never 403 — a 403 confirms it exists")
		require.Contains(t, rr.Body.String(), "city not found")
	})

	t.Run("409 with the explanation when the row is forced", func(t *testing.T) {
		t.Parallel()

		notice := "Thaw alerts stay on for every tracked city; remove the city to turn it off."
		svc := &mockMeWeatherSvc{deleteErr: errors.Join(appweather.ErrForcedSubscription, internal.NewPublicError(notice))}
		rr := del(t, svc, "city-thaw")

		require.Equal(t, http.StatusConflict, rr.Code,
			"a forced row is a conflict with how the resource works, not a malformed request")
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		assert.Equal(t, notice, body["error"])
	})

	t.Run("500 on any other service failure", func(t *testing.T) {
		t.Parallel()

		rr := del(t, &mockMeWeatherSvc{deleteErr: errors.New("db down")}, "city-1")

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}

func TestHandler_DeleteMeWeatherLocation(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(55)

	del := func(t *testing.T, svc meWeatherService, locationID string) *httptest.ResponseRecorder {
		t.Helper()
		h := newWeatherHandler(t, svc, &mockWeatherGeocoder{})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/me/weather/locations/x", http.NoBody)
		req.SetPathValue("location_id", locationID)
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherLocation(rr, withCaller(req, callerUserID))
		return rr
	}

	t.Run("204 on success, forwarding the caller and the location", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeWeatherSvc{}
		require.Equal(t, http.StatusNoContent, del(t, svc, "1234").Code)
		assert.Equal(t, "55", svc.gotUserID)
		assert.Equal(t, "1234", svc.gotLocationID)
	})

	t.Run("400 on a missing or blank location id, before the service is reached", func(t *testing.T) {
		t.Parallel()

		blank := map[string]string{"empty": "", "whitespace only": "   "}
		for name, locationID := range blank {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				svc := &mockMeWeatherSvc{}
				rr := del(t, svc, locationID)

				require.Equal(t, http.StatusBadRequest, rr.Code)
				require.Contains(t, rr.Body.String(), "missing location id")
				assert.Empty(t, svc.gotLocationID)
			})
		}
	})

	t.Run("404 when the caller holds nothing there", func(t *testing.T) {
		t.Parallel()

		rr := del(t, &mockMeWeatherSvc{locErr: internal.ErrNotFound}, "1234")

		require.Equal(t, http.StatusNotFound, rr.Code)
		require.Contains(t, rr.Body.String(), "city not found")
	})

	t.Run("500 on any other service failure", func(t *testing.T) {
		t.Parallel()

		rr := del(t, &mockMeWeatherSvc{locErr: errors.New("db down")}, "1234")

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}

// TestHandler_GetMeWeatherCurrent covers the rendering the handler kept: the
// wire shape of a city with and without a reading, and the city-local sunrise
// and sunset strings that exist so the WASM client needs no tzdata.
// Deduplicating locations and treating "not collected yet" as a state rather
// than an error belong to internal/application/weather and are tested there.
func TestHandler_GetMeWeatherCurrent(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(42)

	newCity := func(locationID, displayName, timezone string) domain.WeatherUserCity {
		return domain.WeatherUserCity{
			ID:          "city-" + locationID,
			UserType:    domain.UserTypeTelegram,
			UserID:      "42",
			LocationID:  locationID,
			DisplayName: displayName,
			Timezone:    timezone,
			NotifyKind:  domain.WeatherNotifyMorningSummary,
			NotifyHour:  7,
		}
	}

	newObs := func(locationID string) *domain.WeatherObservation {
		temp := 25.5
		code := 0
		sunrise := time.Date(2026, 6, 30, 0, 30, 0, 0, time.UTC)
		sunset := time.Date(2026, 6, 30, 15, 45, 0, 0, time.UTC)
		return &domain.WeatherObservation{
			ID:          "obs-" + locationID,
			LocationID:  locationID,
			Provider:    domain.ProviderOpenMeteo,
			TempCurrent: &temp,
			WeatherCode: &code,
			Sunrise:     &sunrise,
			Sunset:      &sunset,
			CapturedAt:  time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		}
	}

	get := func(t *testing.T, svc meWeatherService) *httptest.ResponseRecorder {
		t.Helper()
		h := newWeatherHandler(t, svc, &mockWeatherGeocoder{})
		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, withCaller(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/weather/current", http.NoBody), callerUserID))
		return rr
	}

	t.Run("service error returns 500 with fallback message", func(t *testing.T) {
		t.Parallel()
		rr := get(t, &mockMeWeatherSvc{currentErr: errors.New("db down")})

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("a city with no observation renders has_data false and no readings", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeWeatherSvc{current: []appweather.CurrentCity{
			{City: newCity("1234", "Almaty", "Asia/Almaty")},
		}}
		rr := get(t, svc)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.WeatherCurrentResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Equal(t, "1234", resp.Items[0].LocationID)
		assert.False(t, resp.Items[0].HasData)
		assert.NotContains(t, rr.Body.String(), "temp_current",
			"absent readings must be omitted, never rendered as a zero the client would show")
		assert.Equal(t, "42", svc.gotUserID)
	})

	t.Run("a city with an observation renders every reading", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeWeatherSvc{current: []appweather.CurrentCity{
			{City: newCity("1234", "Almaty", "Asia/Almaty"), Observation: newObs("1234")},
		}}
		rr := get(t, svc)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.WeatherCurrentResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)

		item := resp.Items[0]
		assert.Equal(t, "1234", item.LocationID)
		assert.Equal(t, "Almaty", item.DisplayName)
		assert.True(t, item.HasData)
		require.NotNil(t, item.TempCurrent)
		assert.InDelta(t, 25.5, *item.TempCurrent, 0.001)
		assert.Equal(t, "Clear sky", item.ConditionText)
		assert.NotEmpty(t, item.ConditionEmoji)
		assert.Equal(t, "2026-06-30T12:00:00Z", item.CapturedAt)
	})

	t.Run("sunrise and sunset render in the city's timezone", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeWeatherSvc{current: []appweather.CurrentCity{
			// Asia/Almaty is UTC+5, so 00:30Z is 05:30 and 15:45Z is 20:45 locally.
			{City: newCity("1234", "Almaty", "Asia/Almaty"), Observation: newObs("1234")},
		}}
		rr := get(t, svc)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.WeatherCurrentResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Equal(t, "05:30", resp.Items[0].SunriseLocal)
		assert.Equal(t, "20:45", resp.Items[0].SunsetLocal)
	})

	t.Run("an unloadable timezone drops only the sun times", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeWeatherSvc{current: []appweather.CurrentCity{
			{City: newCity("1234", "Almaty", "Mars/Olympus"), Observation: newObs("1234")},
		}}
		rr := get(t, svc)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.WeatherCurrentResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Empty(t, resp.Items[0].SunriseLocal)
		assert.Empty(t, resp.Items[0].SunsetLocal)
		require.NotNil(t, resp.Items[0].TempCurrent, "a bad timezone must not cost the numeric readings")
	})

	t.Run("no cities returns an empty items array", func(t *testing.T) {
		t.Parallel()
		rr := get(t, &mockMeWeatherSvc{})

		require.Equal(t, http.StatusOK, rr.Code)
		var resp struct {
			Items []json.RawMessage `json:"items"`
		}
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.NotNil(t, resp.Items)
		require.Empty(t, resp.Items)
	})
}
