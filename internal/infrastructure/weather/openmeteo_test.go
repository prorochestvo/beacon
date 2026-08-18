package weather

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
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
			assert.Equal(t, "2", r.URL.Query().Get("forecast_days"), "must request 2 forecast days so the 6h look-ahead window reaches past midnight")
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

		// The 1-day fixture has 24 hourly entries; the decoder must populate them.
		assert.Len(t, obs.Hourly, 24, "24 hourly points expected from 1-day fixture")
	})

	t.Run("2-day fixture: daily[0] is the first day and ForecastDate reflects it", func(t *testing.T) {
		t.Parallel()
		fixture := loadFixture(t, "forecast_almaty_2day.json")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		obs, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.NoError(t, err)

		// daily[0] in the fixture is "2026-06-30"; with forecast_days=2 the decoder must
		// still use daily[0] (today) as ForecastDate — not daily[1] or any computed date.
		assert.Equal(t, "2026-06-30", obs.ForecastDate, "ForecastDate must be daily[0], not daily[1]")

		// 2 daily entries → 48 hourly points (24 per day).
		assert.Len(t, obs.Hourly, 48, "48 hourly points expected from 2-day fixture")
	})

	t.Run("2-day fixture: first hourly point maps to correct UTC instant", func(t *testing.T) {
		t.Parallel()
		fixture := loadFixture(t, "forecast_almaty_2day.json")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteo(t, srv.URL, srv.URL)
		obs, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.NoError(t, err)
		require.NotEmpty(t, obs.Hourly)

		// Fixture first entry: "2026-06-30T00:00" in Asia/Almaty (UTC+5) = 2026-06-29T19:00 UTC.
		almatyLoc, almatyErr := time.LoadLocation("Asia/Almaty")
		require.NoError(t, almatyErr)
		assert.Equal(t, "2026-06-30T00:00", obs.Hourly[0].Time.In(almatyLoc).Format("2006-01-02T15:04"),
			"first hourly point must round-trip to 2026-06-30T00:00 in Asia/Almaty")
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

		om, err := NewOpenMeteo(proxy.URL, io.Discard)
		require.NoError(t, err)

		// The upstream server is irrelevant; the proxy intercepts and returns 502.
		_, err = om.Geocode(t.Context(), "Test", 1)
		require.Error(t, err)

		assert.True(t, proxyCalled, "proxy must have been called when proxyURL is set")
	})

	t.Run("invalid proxyURL returns constructor error", func(t *testing.T) {
		t.Parallel()
		_, err := NewOpenMeteo("://bad-url", io.Discard)
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
// newTestOpenMeteoWithLogger is newTestOpenMeteo with the retry log captured.
func newTestOpenMeteoWithLogger(t *testing.T, geocodeURL, forecastURL string, logger io.Writer) *OpenMeteo {
	t.Helper()
	om := newTestOpenMeteo(t, geocodeURL, forecastURL)
	om.logger = logger
	return om
}

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
	return NewOpenMeteoWithClient(&http.Client{Transport: transport}, io.Discard)
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

// TestOpenMeteo_Retry covers the transient-failure handling added for issue #15.
//
// Open-Meteo answers 5xx intermittently — 167 of them across one production log — and
// without a retry each one dropped that location for the whole collection run. Attempts
// are counted by how many times the test server was hit, so the assertions describe
// observable behaviour rather than the client's internals.
func TestOpenMeteo_Retry(t *testing.T) {
	t.Parallel()

	fixture := loadFixture(t, "forecast_almaty.json")

	// countingServer answers with the given statuses in order, repeating the last one
	// once the list runs out, and reports how many requests it received.
	countingServer := func(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int64) {
		t.Helper()
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := int(hits.Add(1))
			status := statuses[len(statuses)-1]
			if n <= len(statuses) {
				status = statuses[n-1]
			}
			if status == http.StatusOK {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(fixture)
				return
			}
			http.Error(w, "upstream says no", status)
		}))
		t.Cleanup(srv.Close)
		return srv, &hits
	}

	t.Run("a 503 followed by success is absorbed", func(t *testing.T) {
		t.Parallel()
		srv, hits := countingServer(t, http.StatusServiceUnavailable, http.StatusOK)
		var log strings.Builder

		om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, &log)
		obs, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.NoError(t, err, "the caller must never see a transient 503")
		require.NotNil(t, obs)
		assert.Equal(t, int64(2), hits.Load())

		// The run recovered, but the upstream was flaky and the log has to say so —
		// otherwise a degrading provider looks exactly like a healthy one.
		assert.Contains(t, log.String(), fmt.Sprintf("attempt 1 of %d failed", openMeteoMaxAttempts))
		assert.Contains(t, log.String(), "recovered on attempt 2")

		// The recovery time is the point of the line: it bounds how long the fault
		// lasted, which is the only evidence that says whether the retry budget is
		// the right size. Parsed rather than pattern-matched, because a stuck timer
		// printing "0s" would satisfy any regexp for "a number followed by a unit"
		// while reporting nothing. One backoff has elapsed by here, jittered down to
		// 200ms at worst.
		elapsed := loggedDuration(t, log.String(), `recovered on attempt 2 of \d+ after (\S+)`)
		assert.Greater(t, elapsed, 150*time.Millisecond,
			"the recovery time must be real; a zero means the clock is not being read")
	})

	t.Run("a persistent 503 fails after exactly the attempt budget", func(t *testing.T) {
		t.Parallel()
		srv, hits := countingServer(t, http.StatusServiceUnavailable)
		var log strings.Builder

		om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, &log)
		_, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.Error(t, err)
		assert.Equal(t, int64(openMeteoMaxAttempts), hits.Load(),
			"the whole budget is spent on a persistent fault, and not one attempt more")
		assert.Contains(t, err.Error(), "503")
		assert.Contains(t, err.Error(), fmt.Sprintf("giving up after %d attempt(s)", openMeteoMaxAttempts),
			"an outright failure must say how hard it tried, or it reads like one unlucky request")
		// Every backoff bar the last has elapsed by the time this gives up. Compared
		// against the minimum the schedule can produce after jitter, so the assertion
		// fails on a clock that is not being read rather than on an unlucky draw.
		spent := loggedDuration(t, err.Error(), `giving up after \d+ attempt\(s\) in (\S+):`)
		assert.Greater(t, spent, minRetrySchedule(),
			"the budget actually spent must be real, or the log cannot say how narrow it was")
		assert.NotContains(t, log.String(), "recovered")
	})

	t.Run("every 5xx is treated as transient", func(t *testing.T) {
		t.Parallel()
		for _, status := range []int{
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout,
		} {
			srv, hits := countingServer(t, status, http.StatusOK)
			om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, io.Discard)
			_, err := om.Forecast(t.Context(), 43.25249, 76.9115)
			require.NoError(t, err, "status %d", status)
			assert.Equal(t, int64(2), hits.Load(), "status %d must be retried", status)
		}
	})

	t.Run("4xx is answered once and never retried", func(t *testing.T) {
		t.Parallel()
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
			srv, hits := countingServer(t, status)
			om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, io.Discard)
			_, err := om.Forecast(t.Context(), 43.25249, 76.9115)
			require.Error(t, err, "status %d", status)
			assert.Equal(t, int64(1), hits.Load(),
				"status %d describes a request that will not become valid by being re-sent", status)
			assert.Contains(t, err.Error(), "giving up after 1 attempt(s)")
		}
	})

	t.Run("429 is not retried", func(t *testing.T) {
		t.Parallel()
		srv, hits := countingServer(t, http.StatusTooManyRequests)
		om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, io.Discard)

		// Open-Meteo is keyless and limits by IP. Re-sending immediately asks for the
		// same refusal and pushes the caller further into the limit.
		_, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.Error(t, err)
		assert.Equal(t, int64(1), hits.Load())
	})

	t.Run("a connection-level failure is retried", func(t *testing.T) {
		t.Parallel()
		// A server that closes the connection without answering is indistinguishable
		// from a 5xx from the caller's side, and just as transient.
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				hijacker, ok := w.(http.Hijacker)
				assert.True(t, ok)
				conn, _, hijackErr := hijacker.Hijack()
				assert.NoError(t, hijackErr)
				_ = conn.Close()
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, io.Discard)
		obs, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.NoError(t, err)
		require.NotNil(t, obs)
		assert.Equal(t, int64(2), hits.Load())
	})

	t.Run("a clean first attempt logs nothing", func(t *testing.T) {
		t.Parallel()
		srv, hits := countingServer(t, http.StatusOK)
		var log strings.Builder

		om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, &log)
		_, err := om.Forecast(t.Context(), 43.25249, 76.9115)
		require.NoError(t, err)
		assert.Equal(t, int64(1), hits.Load())
		assert.Empty(t, log.String(), "the healthy path must stay silent or the log becomes noise")
	})

	t.Run("a cancelled context stops instead of sleeping out the backoff", func(t *testing.T) {
		t.Parallel()
		srv, hits := countingServer(t, http.StatusServiceUnavailable)
		om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, io.Discard)

		ctx, cancel := context.WithCancel(t.Context())
		// Cancel while the client is between attempts. The backoff must observe it
		// rather than run its timer out, or a tick cut short by SIGTERM would hang.
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		_, err := om.Forecast(ctx, 43.25249, 76.9115)
		elapsed := time.Since(start)

		require.Error(t, err)
		assert.Less(t, elapsed, openMeteoRetryBackoff*4,
			"cancellation must cut the wait short, not merely be noticed after it")
		assert.LessOrEqual(t, hits.Load(), int64(openMeteoMaxAttempts))
	})

	t.Run("geocode gets the same treatment as forecast", func(t *testing.T) {
		t.Parallel()
		// Both paths share one get(), so this pins that the retry sits at the seam
		// rather than being bolted onto the forecast call.
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				http.Error(w, "upstream says no", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(loadFixture(t, "geocode_almaty.json"))
		}))
		t.Cleanup(srv.Close)

		om := newTestOpenMeteoWithLogger(t, srv.URL, srv.URL, io.Discard)
		results, err := om.Geocode(t.Context(), "Almaty", 3)
		require.NoError(t, err)
		assert.NotEmpty(t, results)
		assert.Equal(t, int64(2), hits.Load())
	})
}

func TestRetryBackoff(t *testing.T) {
	t.Parallel()

	t.Run("grows with each attempt and stays inside the jitter band", func(t *testing.T) {
		t.Parallel()
		for attempt := 1; attempt <= openMeteoMaxAttempts; attempt++ {
			base := min(openMeteoRetryBackoff<<(attempt-1), openMeteoRetryBackoffCap)
			lo := time.Duration(float64(base) * (1 - openMeteoRetryJitter))
			hi := time.Duration(float64(base) * (1 + openMeteoRetryJitter))
			for range 50 {
				got := retryBackoff(attempt)
				assert.GreaterOrEqual(t, got, lo, "attempt %d", attempt)
				assert.LessOrEqual(t, got, hi, "attempt %d", attempt)
			}
		}
	})

	t.Run("stops doubling at the cap", func(t *testing.T) {
		t.Parallel()
		// Uncapped, the wait doubles out of the budget: the 250ms base reaches 4s by
		// attempt 5. The production window says a longer wait recovers no more
		// requests than a short one, so the cap is what keeps extra attempts cheap.
		ceiling := time.Duration(float64(openMeteoRetryBackoffCap) * (1 + openMeteoRetryJitter))
		for attempt := 1; attempt <= 64; attempt++ {
			got := retryBackoff(attempt)
			assert.Positive(t, got, "attempt %d: a shift past the width of a Duration must not fire the timer immediately", attempt)
			assert.LessOrEqual(t, got, ceiling, "attempt %d", attempt)
		}
	})

	t.Run("is not constant, so concurrent callers do not re-send in lockstep", func(t *testing.T) {
		t.Parallel()
		seen := map[time.Duration]struct{}{}
		for range 50 {
			seen[retryBackoff(1)] = struct{}{}
		}
		assert.Greater(t, len(seen), 1, "jitter must actually vary the wait")
	})
}

// TestRetryScheduleFitsTheTightestCaller guards a coupling nothing else reports.
//
// The Mini App city search bounds its geocode at 5s (weatherGeoTimeout in the handlers
// package), and geocode goes through the same get() as the collector. sleepWithContext
// honours that deadline, so a budget whose waiting alone approaches it stops being a
// retry for that caller and becomes a slower way to fail a search — with no failing test
// and no log line to say why. Widening the budget is fine; widening it past this means
// moving weatherGeoTimeout in the same change.
func TestRetryScheduleFitsTheTightestCaller(t *testing.T) {
	t.Parallel()

	// The waits the schedule can produce at their most unlucky, across the whole
	// budget: one fewer than the attempts, since nothing waits after the last.
	var scheduled time.Duration
	for attempt := 1; attempt < openMeteoMaxAttempts; attempt++ {
		base := min(openMeteoRetryBackoff<<(attempt-1), openMeteoRetryBackoffCap)
		scheduled += time.Duration(float64(base) * (1 + openMeteoRetryJitter))
	}

	assert.Less(t, scheduled, openMeteoTightestCallerDeadline/2,
		"the waits alone claim half the search deadline, leaving nothing for the requests themselves")
}

// minRetrySchedule is the least time a request that spends the whole budget can
// have been waiting: every backoff bar the one after the final attempt, each
// jittered to the bottom of its band. Derived rather than written down so a
// change to the budget moves the assertion with it.
func minRetrySchedule() time.Duration {
	var total time.Duration
	for attempt := 1; attempt < openMeteoMaxAttempts; attempt++ {
		base := min(openMeteoRetryBackoff<<(attempt-1), openMeteoRetryBackoffCap)
		total += time.Duration(float64(base) * (1 - openMeteoRetryJitter))
	}
	return total
}

// loggedDuration pulls the duration captured by pattern out of text and parses it,
// failing the test when the line is absent or the value is not a duration at all.
func loggedDuration(t *testing.T, text, pattern string) time.Duration {
	t.Helper()

	m := regexp.MustCompile(pattern).FindStringSubmatch(text)
	require.Len(t, m, 2, "no timed line matching %q in:\n%s", pattern, text)

	d, err := time.ParseDuration(m[1])
	require.NoError(t, err, "%q is not a duration", m[1])
	return d
}
