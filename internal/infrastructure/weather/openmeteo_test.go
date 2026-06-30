package weather

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata" // embed IANA tzdata so LoadLocation works without system tzdata

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadFixture reads a JSON fixture file from testdata/.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err, "fixture %s not found", name)
	return data
}

func TestOpenMeteo_Geocode(t *testing.T) {
	t.Parallel()

	t.Run("decodes results correctly", func(t *testing.T) {
		t.Parallel()
		fixture := loadFixture(t, "geocode_almaty.json")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/search", r.URL.Path)
			assert.Equal(t, "Almaty", r.URL.Query().Get("name"))
			assert.Equal(t, "3", r.URL.Query().Get("count"))
			assert.Equal(t, "ru", r.URL.Query().Get("language"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		results, err := om.Geocode(t.Context(), "Almaty", 3)
		require.NoError(t, err)
		require.Len(t, results, 3)

		first := results[0]
		assert.Equal(t, int64(1526384), first.ID)
		assert.Equal(t, "Алматы", first.Name)
		assert.InDelta(t, 43.25249, first.Latitude, 1e-4)
		assert.InDelta(t, 76.9115, first.Longitude, 1e-4)
		assert.Equal(t, "Казахстан", first.Country)
		assert.Equal(t, "KZ", first.CountryCode)
		assert.Equal(t, "Asia/Almaty", first.Timezone)
	})

	t.Run("no results returns empty slice not error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"generationtime_ms":0.1}`))
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		results, err := om.Geocode(t.Context(), "Nonexistent", 5)
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("non-2xx returns error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "server error", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		_, err := om.Geocode(t.Context(), "City", 5)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("malformed JSON returns error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": [INVALID}`))
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		_, err := om.Geocode(t.Context(), "City", 5)
		require.Error(t, err)
	})
}

func TestOpenMeteo_Forecast(t *testing.T) {
	t.Parallel()

	t.Run("decodes daily and current fields from real fixture", func(t *testing.T) {
		t.Parallel()
		fixture := loadFixture(t, "forecast_almaty.json")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/forecast", r.URL.Path)
			assert.Equal(t, "auto", r.URL.Query().Get("timezone"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		obs, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.NoError(t, err)
		require.NotNil(t, obs)

		assert.Equal(t, "open-meteo", obs.Provider)
		assert.Equal(t, "2026-06-30", obs.ForecastDate)

		require.NotNil(t, obs.TempMax)
		assert.InDelta(t, 31.6, *obs.TempMax, 1e-4)

		require.NotNil(t, obs.TempMin)
		assert.InDelta(t, 20.8, *obs.TempMin, 1e-4)

		require.NotNil(t, obs.PrecipSum)
		assert.InDelta(t, 1.1, *obs.PrecipSum, 1e-4)

		require.NotNil(t, obs.PrecipProbMax)
		assert.Equal(t, 69, *obs.PrecipProbMax)

		require.NotNil(t, obs.WeatherCode)
		// daily[0] weather_code=53 overrides current weather_code=0
		assert.Equal(t, 53, *obs.WeatherCode)

		require.NotNil(t, obs.TempCurrent)
		assert.InDelta(t, 21.3, *obs.TempCurrent, 1e-4)

		require.NotNil(t, obs.TempFeels)
		assert.InDelta(t, 22.1, *obs.TempFeels, 1e-4)

		require.NotNil(t, obs.Humidity)
		assert.Equal(t, 61, *obs.Humidity)

		require.NotNil(t, obs.WindSpeed)
		assert.InDelta(t, 1.7, *obs.WindSpeed, 1e-4)

		require.NotNil(t, obs.WindDir)
		assert.Equal(t, 212, *obs.WindDir)

		// After fix #6, sunrise/sunset are stored as correct UTC instants (parsed in
		// the city's timezone). Verify by converting back to Asia/Almaty (UTC+5).
		almatyLoc, almatyErr := time.LoadLocation("Asia/Almaty")
		require.NoError(t, almatyErr)

		require.NotNil(t, obs.Sunrise)
		assert.Equal(t, "04:15", obs.Sunrise.In(almatyLoc).Format("15:04"),
			"sunrise must round-trip to local 04:15 in Asia/Almaty")

		require.NotNil(t, obs.Sunset)
		assert.Equal(t, "19:36", obs.Sunset.In(almatyLoc).Format("15:04"),
			"sunset must round-trip to local 19:36 in Asia/Almaty")

		assert.False(t, obs.CapturedAt.IsZero())
		// ID is not set by the provider; the caller or repository mints it.
		assert.Empty(t, obs.ID)
	})

	t.Run("empty daily array returns error not panic", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"daily":{"time":[],"temperature_2m_max":[],"temperature_2m_min":[]},"current":{"temperature_2m":20}}`))
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		_, err := om.Forecast(t.Context(), 43.0, 77.0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("short daily arrays do not panic", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			// daily.time has one entry but other arrays are missing; must not panic.
			_, _ = w.Write([]byte(`{"daily":{"time":["2026-06-30"],"temperature_2m_max":[31.0]},"current":{"temperature_2m":20,"apparent_temperature":19,"relative_humidity_2m":50,"wind_speed_10m":5,"wind_direction_10m":90,"precipitation":0,"weather_code":0,"cloud_cover":0}}`))
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		obs, err := om.Forecast(t.Context(), 43.0, 77.0)
		require.NoError(t, err)
		require.NotNil(t, obs)
		require.NotNil(t, obs.TempMax)
		assert.InDelta(t, 31.0, *obs.TempMax, 1e-4)
		assert.Nil(t, obs.TempMin, "missing array must yield nil pointer, not panic")
	})

	t.Run("non-2xx returns error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad gateway", http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		_, err := om.Forecast(t.Context(), 43.0, 77.0)
		require.Error(t, err)
	})

	t.Run("malformed JSON returns error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{not json`))
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		_, err := om.Forecast(t.Context(), 43.0, 77.0)
		require.Error(t, err)
	})
}

func TestOpenMeteo_ProxyRouting(t *testing.T) {
	t.Parallel()

	t.Run("non-empty proxyURL routes through proxy", func(t *testing.T) {
		t.Parallel()

		proxyCalled := false
		// A proxy stub that sets a flag and responds with a 200.
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			proxyCalled = true
			http.Error(w, "proxy intercepted", http.StatusBadGateway)
		}))
		t.Cleanup(proxy.Close)

		om, err := NewOpenMeteo(proxy.URL)
		require.NoError(t, err)

		// The upstream server is irrelevant; the proxy intercepts and returns 502.
		_, err = om.Geocode(t.Context(), "Test", 1)
		require.Error(t, err)

		assert.True(t, proxyCalled, "proxy must have been called when proxyURL is set")
	})

	t.Run("invalid proxyURL returns constructor error", func(t *testing.T) {
		t.Parallel()
		_, err := NewOpenMeteo("://bad-url")
		require.Error(t, err)
	})
}

func TestLocationKey(t *testing.T) {
	t.Parallel()

	t.Run("uses ID when non-zero", func(t *testing.T) {
		t.Parallel()
		key := LocationKey(GeoResult{ID: 1526384, Latitude: 43.25, Longitude: 76.91})
		assert.Equal(t, "1526384", key)
	})

	t.Run("falls back to rounded coordinates when ID is zero", func(t *testing.T) {
		t.Parallel()
		key := LocationKey(GeoResult{ID: 0, Latitude: 43.2525, Longitude: 76.9115})
		assert.Equal(t, "43.2525,76.9115", key)
	})
}

// newTestOpenMeteo returns an OpenMeteo configured to route all requests to
// baseURL (which typically points at an httptest server). The transport rewrites
// all host:port parts of the outgoing URL to baseURL so the test server receives them.
func newTestOpenMeteo(t *testing.T, geocodeBase, forecastBase string) *OpenMeteo {
	t.Helper()

	geoURL, err := url.Parse(geocodeBase)
	require.NoError(t, err)
	foreURL, err := url.Parse(forecastBase)
	require.NoError(t, err)

	transport := &redirectTransport{
		geoHost:      geoURL.Host,
		forecastHost: foreURL.Host,
	}
	return NewOpenMeteoWithClient(&http.Client{Transport: transport})
}

// redirectTransport rewrites the Host of each request so all calls go to the
// test server regardless of the original URL constructed by OpenMeteo.
type redirectTransport struct {
	geoHost      string
	forecastHost string
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	// Both geocoding and forecast paths are served by the same test server here.
	if cloned.URL.Host != "" {
		cloned.URL.Host = rt.geoHost
		cloned.URL.Scheme = "http"
	}
	return http.DefaultTransport.RoundTrip(cloned)
}
