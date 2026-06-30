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
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ meWeatherCityRepository = (*mockWeatherCityRepo)(nil)
var _ weatherGeocoder = (*mockWeatherGeocoder)(nil)
var _ meWeatherObsRepository = (*mockWeatherObsRepo)(nil)

// mockWeatherCityRepo is a test double for meWeatherCityRepository.
type mockWeatherCityRepo struct {
	cities    map[string]*domain.WeatherUserCity // id → city
	byUser    []domain.WeatherUserCity
	retained  []*domain.WeatherUserCity
	removed   []*domain.WeatherUserCity
	retainErr error
	removeErr error
	listErr   error
	getErr    error
}

func (m *mockWeatherCityRepo) RetainWeatherUserCity(_ context.Context, record *domain.WeatherUserCity) error {
	if m.retainErr != nil {
		return m.retainErr
	}
	if record.ID == "" {
		record.ID = "city-generated-id"
	}
	m.retained = append(m.retained, record)
	return nil
}

func (m *mockWeatherCityRepo) ObtainWeatherUserCitiesByUserID(_ context.Context, _ domain.UserType, _ string) ([]domain.WeatherUserCity, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if m.byUser == nil {
		return []domain.WeatherUserCity{}, nil
	}
	return m.byUser, nil
}

func (m *mockWeatherCityRepo) ObtainWeatherUserCityByID(_ context.Context, id string) (*domain.WeatherUserCity, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.cities == nil {
		return nil, internal.ErrNotFound
	}
	c, ok := m.cities[id]
	if !ok {
		return nil, internal.ErrNotFound
	}
	return c, nil
}

func (m *mockWeatherCityRepo) RemoveWeatherUserCity(_ context.Context, record *domain.WeatherUserCity) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	m.removed = append(m.removed, record)
	return nil
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

// mockWeatherObsRepo is a test double for meWeatherObsRepository.
// When obsMap is non-nil and contains the locationID key, the stored obs is
// returned. When the key is absent, internal.ErrNotFound is returned.
// When obsErr is non-nil it is returned for every call regardless of the key.
type mockWeatherObsRepo struct {
	obsMap map[string]*domain.WeatherObservation // locationID → obs
	obsErr error
}

func (m *mockWeatherObsRepo) ObtainLatestObservation(_ context.Context, locationID, _ string) (*domain.WeatherObservation, error) {
	if m.obsErr != nil {
		return nil, m.obsErr
	}
	if obs, ok := m.obsMap[locationID]; ok {
		return obs, nil
	}
	return nil, internal.ErrNotFound
}

// newWeatherHandler builds a Handler wired with the given weather test doubles
// and silenced logger so test output is clean.
func newWeatherHandler(t *testing.T, cityRepo meWeatherCityRepository, geo weatherGeocoder) *Handler {
	t.Helper()
	h, err := NewHandler(
		&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{},
		&mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil, nil, "", time.Time{},
	)
	require.NoError(t, err)
	h.WithWeatherDeps(cityRepo, geo)
	h.logger = log.New(io.Discard, "", 0)
	return h
}

// newWeatherHandlerWithObs builds a Handler wired with city repo, geocoder, and
// obs repo. Extends newWeatherHandler so tests for GetMeWeatherCurrent do not
// have to wire the obs dep manually.
func newWeatherHandlerWithObs(t *testing.T, cityRepo meWeatherCityRepository, geo weatherGeocoder, obsRepo meWeatherObsRepository) *Handler {
	t.Helper()
	h := newWeatherHandler(t, cityRepo, geo)
	h.WithWeatherObsRepo(obsRepo)
	return h
}

func TestHandler_SearchWeatherCities(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(77)

	t.Run("nil geocoder returns 503", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, nil)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search?q=Almaty", nil))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("missing auth header returns 401", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysRejectInitData

		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search?q=Almaty", nil))

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("missing q parameter returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search", nil))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "q is required")
	})

	t.Run("blank q parameter returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		// Raw spaces in URLs are invalid for httptest.NewRequest; encode them.
		req := httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search?q=%20%20%20", nil)
		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("geocoder error returns 500", func(t *testing.T) {
		t.Parallel()
		geo := &mockWeatherGeocoder{err: errors.New("upstream down")}
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, geo)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search?q=Almaty", nil))

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
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, geo)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search?q=Almaty", nil))

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
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search?q=xyzzy", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.WeatherCitySearchResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.NotNil(t, resp.Items)
		require.Empty(t, resp.Items)
	})

	t.Run("initData from query string is ignored (header only)", func(t *testing.T) {
		t.Parallel()
		var seen string
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = func(initData, _ string, _ time.Duration, _ time.Time) (int64, error) {
			seen = initData
			return 0, errors.New("reject")
		}

		req := httptest.NewRequest(http.MethodGet, "/api/me/weather/cities/search?q=Almaty&initData=should_be_ignored", nil)
		rr := httptest.NewRecorder()
		h.SearchWeatherCities(rr, req)

		require.Equal(t, "", seen, "handler must not source initData from query string")
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})
}

func TestHandler_ListMeWeatherCities(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(99)
	const callerIDStr = "99"

	t.Run("nil repo returns 503", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, nil, nil)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities", nil))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("missing auth returns 401", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysRejectInitData

		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities", nil))

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("repo error returns 500", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{listErr: errors.New("db down")}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities", nil))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("empty list returns empty items array", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.WeatherCitiesResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.NotNil(t, resp.Items)
		require.Empty(t, resp.Items)
	})

	t.Run("happy path returns caller cities", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{byUser: []domain.WeatherUserCity{
			{
				ID: "c1", UserType: domain.UserTypeTelegram, UserID: callerIDStr,
				LocationID: "1234", DisplayName: "Almaty", Latitude: 43.25, Longitude: 76.94,
				Timezone: "Asia/Almaty", Country: "Kazakhstan", Admin1: "Almaty",
				NotifyKind: domain.WeatherNotifyMorningSummary, NotifyHour: 7,
			},
		}}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.ListMeWeatherCities(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/cities", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.WeatherCitiesResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Equal(t, "c1", resp.Items[0].ID)
		assert.Equal(t, "1234", resp.Items[0].LocationID)
		assert.Equal(t, "Almaty", resp.Items[0].DisplayName)
		assert.Equal(t, 7, resp.Items[0].NotifyHour)
	})
}

func TestHandler_CreateMeWeatherCity(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(55)
	const callerIDStr = "55"

	validBody := dto.WeatherCityCreateRequest{
		LocationID:  "9999",
		DisplayName: "Almaty",
		Latitude:    43.25,
		Longitude:   76.94,
		Timezone:    "Asia/Almaty",
		Country:     "Kazakhstan",
		Admin1:      "Almaty",
	}

	bodyJSON := func(r dto.WeatherCityCreateRequest) io.Reader {
		b, _ := json.Marshal(r)
		return strings.NewReader(string(b))
	}

	t.Run("nil repo returns 503", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, nil, nil)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(validBody))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("missing auth returns 401", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(validBody))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("malformed JSON body returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", strings.NewReader("{invalid json"))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("empty location_id returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		b := validBody
		b.LocationID = ""
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "location_id is required")
	})

	t.Run("empty display_name returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		b := validBody
		b.DisplayName = ""
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "display_name is required")
	})

	t.Run("invalid timezone returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		b := validBody
		b.Timezone = "Not/A/Timezone"
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid timezone")
	})

	t.Run("latitude out of range returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		b := validBody
		b.Latitude = 91.0
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "latitude")
	})

	t.Run("longitude out of range returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		b := validBody
		b.Longitude = -181.0
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "longitude")
	})

	t.Run("notify_hour out of range returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		hour := 24
		b := validBody
		b.NotifyHour = &hour
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "notify_hour")
	})

	t.Run("retain repo error returns 500", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{retainErr: errors.New("db down")}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(validBody))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("happy path returns 201 with generated id", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(validBody))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusCreated, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.WeatherCityCreateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.NotEmpty(t, resp.ID)
	})

	t.Run("happy path stores correct user fields", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(validBody))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusCreated, rr.Code)
		require.Len(t, cityRepo.retained, 1)
		stored := cityRepo.retained[0]
		assert.Equal(t, callerIDStr, stored.UserID)
		assert.Equal(t, domain.UserTypeTelegram, stored.UserType)
		assert.Equal(t, "9999", stored.LocationID)
		assert.Equal(t, "Almaty", stored.DisplayName)
		assert.Equal(t, "Asia/Almaty", stored.Timezone)
		assert.Nil(t, stored.GismeteoCityID, "GismeteoCityID must be nil in MVP")
	})

	t.Run("default notify_hour is applied when omitted", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		b := validBody
		b.NotifyHour = nil // explicitly omit
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusCreated, rr.Code)
		require.Len(t, cityRepo.retained, 1)
		assert.Equal(t, weatherDefaultNotifyHour, cityRepo.retained[0].NotifyHour)
	})

	t.Run("explicit notify_hour 0 is accepted", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		hour := 0
		b := validBody
		b.NotifyHour = &hour
		req := httptest.NewRequest(http.MethodPost, "/api/me/weather/cities", bodyJSON(b))
		rr := httptest.NewRecorder()
		h.CreateMeWeatherCity(rr, req)

		require.Equal(t, http.StatusCreated, rr.Code)
		require.Len(t, cityRepo.retained, 1)
		assert.Equal(t, 0, cityRepo.retained[0].NotifyHour)
	})
}

func TestHandler_DeleteMeWeatherCity(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(33)
	const callerIDStr = "33"
	const otherIDStr = "44"

	callerCity := &domain.WeatherUserCity{
		ID: "city-1", UserType: domain.UserTypeTelegram, UserID: callerIDStr,
		LocationID: "1234", DisplayName: "Almaty",
	}
	otherCity := &domain.WeatherUserCity{
		ID: "city-2", UserType: domain.UserTypeTelegram, UserID: otherIDStr,
		LocationID: "5678", DisplayName: "Moscow",
	}

	t.Run("nil repo returns 503", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, nil, nil)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/city-1", nil)
		req.SetPathValue("id", "city-1")
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("missing auth returns 401", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/city-1", nil)
		req.SetPathValue("id", "city-1")
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("missing id path param returns 400", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/", nil)
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("city not found returns 404", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandler(t, &mockWeatherCityRepo{cities: map[string]*domain.WeatherUserCity{}}, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/nonexistent", nil)
		req.SetPathValue("id", "nonexistent")
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusNotFound, rr.Code)
		require.Contains(t, rr.Body.String(), "city not found")
	})

	t.Run("cross-user access returns 404 not 403", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{cities: map[string]*domain.WeatherUserCity{"city-2": otherCity}}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID) // caller is 33, city belongs to 44

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/city-2", nil)
		req.SetPathValue("id", "city-2")
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusNotFound, rr.Code, "cross-user access must return 404, not 403")
		require.Contains(t, rr.Body.String(), "city not found")
	})

	t.Run("repo lookup error returns 500", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{getErr: errors.New("db down")}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/city-1", nil)
		req.SetPathValue("id", "city-1")
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("remove repo error returns 500", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{
			cities:    map[string]*domain.WeatherUserCity{"city-1": callerCity},
			removeErr: errors.New("db down"),
		}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/city-1", nil)
		req.SetPathValue("id", "city-1")
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("happy path returns 204 and removes the city", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{cities: map[string]*domain.WeatherUserCity{"city-1": callerCity}}
		h := newWeatherHandler(t, cityRepo, &mockWeatherGeocoder{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodDelete, "/api/me/weather/cities/city-1", nil)
		req.SetPathValue("id", "city-1")
		rr := httptest.NewRecorder()
		h.DeleteMeWeatherCity(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code)
		require.Len(t, cityRepo.removed, 1)
		assert.Equal(t, "city-1", cityRepo.removed[0].ID)
	})
}

func TestHandler_GetMeWeatherCurrent(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(42)

	newCity := func(locationID, displayName string) domain.WeatherUserCity {
		return domain.WeatherUserCity{
			ID:          "city-" + locationID,
			UserType:    domain.UserTypeTelegram,
			UserID:      "42",
			LocationID:  locationID,
			DisplayName: displayName,
			Timezone:    "Asia/Almaty",
			NotifyKind:  domain.WeatherNotifyMorningSummary,
			NotifyHour:  7,
		}
	}

	newObs := func(locationID string) *domain.WeatherObservation {
		temp := 25.5
		return &domain.WeatherObservation{
			ID:          "obs-" + locationID,
			LocationID:  locationID,
			Provider:    domain.ProviderOpenMeteo,
			TempCurrent: &temp,
			WeatherCode: func() *int { c := 0; return &c }(),
			CapturedAt:  time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		}
	}

	t.Run("nil city repo returns 503", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandlerWithObs(t, nil, nil, &mockWeatherObsRepo{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("nil obs repo returns 503", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandlerWithObs(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{}, nil)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("missing auth returns 401", func(t *testing.T) {
		t.Parallel()
		h := newWeatherHandlerWithObs(t, &mockWeatherCityRepo{}, &mockWeatherGeocoder{}, &mockWeatherObsRepo{})
		h.validateInitData = alwaysRejectInitData

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("city repo error returns 500 with fallback message", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{listErr: errors.New("db down")}
		h := newWeatherHandlerWithObs(t, cityRepo, &mockWeatherGeocoder{}, &mockWeatherObsRepo{})
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("obs repo error returns 500 with fallback message", func(t *testing.T) {
		t.Parallel()
		city := newCity("1234", "Almaty")
		cityRepo := &mockWeatherCityRepo{byUser: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherObsRepo{obsErr: errors.New("obs db down")}
		h := newWeatherHandlerWithObs(t, cityRepo, &mockWeatherGeocoder{}, obsRepo)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}`
		assert.Contains(t, rr.Body.String(), errFallbackMessage)
	})

	t.Run("city with no observation returns has_data false", func(t *testing.T) {
		t.Parallel()
		city := newCity("1234", "Almaty")
		cityRepo := &mockWeatherCityRepo{byUser: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherObsRepo{obsMap: map[string]*domain.WeatherObservation{}} // empty map → ErrNotFound
		h := newWeatherHandlerWithObs(t, cityRepo, &mockWeatherGeocoder{}, obsRepo)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp struct {
			Items []struct {
				LocationID string `json:"location_id"`
				HasData    bool   `json:"has_data"`
			} `json:"items"`
		}
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		assert.Equal(t, "1234", resp.Items[0].LocationID)
		assert.False(t, resp.Items[0].HasData)
	})

	t.Run("happy path returns items with data", func(t *testing.T) {
		t.Parallel()
		city := newCity("1234", "Almaty")
		obs := newObs("1234")
		cityRepo := &mockWeatherCityRepo{byUser: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherObsRepo{obsMap: map[string]*domain.WeatherObservation{"1234": obs}}
		h := newWeatherHandlerWithObs(t, cityRepo, &mockWeatherGeocoder{}, obsRepo)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp struct {
			Items []struct {
				LocationID     string  `json:"location_id"`
				DisplayName    string  `json:"display_name"`
				HasData        bool    `json:"has_data"`
				TempCurrent    float64 `json:"temp_current"`
				ConditionText  string  `json:"condition_text"`
				ConditionEmoji string  `json:"condition_emoji"`
				CapturedAt     string  `json:"captured_at"`
			} `json:"items"`
		}
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1)
		item := resp.Items[0]
		assert.Equal(t, "1234", item.LocationID)
		assert.Equal(t, "Almaty", item.DisplayName)
		assert.True(t, item.HasData)
		assert.InDelta(t, 25.5, item.TempCurrent, 0.001)
		assert.Equal(t, "Clear sky", item.ConditionText)
		assert.NotEmpty(t, item.CapturedAt)
	})

	t.Run("dedup: two rows with same location_id produce one item", func(t *testing.T) {
		t.Parallel()
		// Simulate two notify-kind rows for the same physical city.
		city1 := newCity("1234", "Almaty")
		city2 := city1
		city2.ID = "city-1234-b"
		obs := newObs("1234")
		cityRepo := &mockWeatherCityRepo{byUser: []domain.WeatherUserCity{city1, city2}}
		obsRepo := &mockWeatherObsRepo{obsMap: map[string]*domain.WeatherObservation{"1234": obs}}
		h := newWeatherHandlerWithObs(t, cityRepo, &mockWeatherGeocoder{}, obsRepo)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp struct {
			Items []struct {
				LocationID string `json:"location_id"`
			} `json:"items"`
		}
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.Len(t, resp.Items, 1, "two notify-kind rows for the same location_id must produce exactly one item")
		assert.Equal(t, "1234", resp.Items[0].LocationID)
	})

	t.Run("empty city list returns empty items array", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCityRepo{} // ObtainWeatherUserCitiesByUserID returns []
		obsRepo := &mockWeatherObsRepo{}
		h := newWeatherHandlerWithObs(t, cityRepo, &mockWeatherGeocoder{}, obsRepo)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		rr := httptest.NewRecorder()
		h.GetMeWeatherCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/me/weather/current", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp struct {
			Items []json.RawMessage `json:"items"`
		}
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		require.NotNil(t, resp.Items)
		require.Empty(t, resp.Items)
	})
}
