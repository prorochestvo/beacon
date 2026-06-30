package weather

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// loadGismeteoFixture reads the named HTML fixture from testdata/.
func loadGismeteoFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err, "gismeteo fixture %s not found", name)
	return data
}

func TestGismeteo_curatedMap(t *testing.T) {
	t.Parallel()

	t.Run("every entry has a non-empty slug and positive id", func(t *testing.T) {
		t.Parallel()

		for locID, city := range gismeteoCities {
			locID, city := locID, city
			t.Run(locID, func(t *testing.T) {
				t.Parallel()
				assert.NotEmpty(t, locID, "map key (location_id) must not be empty")
				assert.NotEmpty(t, city.slug, "slug must not be empty for location_id %q", locID)
				assert.Positive(t, city.id, "id must be positive for location_id %q", locID)
			})
		}
	})

	t.Run("map is not empty", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, gismeteoCities, "curated map must contain at least one entry")
	})

	t.Run("known cities are present with expected slug and id", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			locationID string
			slug       string
			id         int
		}{
			{"1526384", "almaty", 5205},
			{"1526273", "astana", 5164},
			{"1518980", "shymkent", 5324},
			{"524901", "moscow", 4368},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.locationID, func(t *testing.T) {
				t.Parallel()
				city, ok := gismeteoCities[tc.locationID]
				require.True(t, ok, "location_id %q must be in the curated map", tc.locationID)
				assert.Equal(t, tc.slug, city.slug)
				assert.Equal(t, tc.id, city.id)
			})
		}
	})
}

func TestBuildGismeteoURL(t *testing.T) {
	t.Parallel()

	t.Run("builds correct URL from base, slug, and id", func(t *testing.T) {
		t.Parallel()
		got := buildGismeteoURL(gismeteoBaseURL, gismeteoCity{slug: "almaty", id: 5205})
		assert.Equal(t, "https://www.gismeteo.kz/weather-almaty-5205/", got)
	})

	t.Run("builds correct URL for non-KZ city", func(t *testing.T) {
		t.Parallel()
		got := buildGismeteoURL(gismeteoBaseURL, gismeteoCity{slug: "moscow", id: 4368})
		assert.Equal(t, "https://www.gismeteo.kz/weather-moscow-4368/", got)
	})

	t.Run("respects custom base URL (test seam)", func(t *testing.T) {
		t.Parallel()
		got := buildGismeteoURL("http://127.0.0.1:9999", gismeteoCity{slug: "almaty", id: 5205})
		assert.Equal(t, "http://127.0.0.1:9999/weather-almaty-5205/", got)
	})
}

func TestGismeteo_Supports(t *testing.T) {
	t.Parallel()

	// newGismeteoWithClient (unexported) gives a Gismeteo whose Supports method
	// operates purely on the in-memory curated map — no HTTP client is ever called.
	g := newGismeteoWithClient(http.DefaultClient)

	t.Run("returns true for a known location_id", func(t *testing.T) {
		t.Parallel()
		assert.True(t, g.Supports("1526384"))
	})

	t.Run("returns false for an unknown location_id", func(t *testing.T) {
		t.Parallel()
		assert.False(t, g.Supports("9999999"))
	})

	t.Run("returns false for empty string", func(t *testing.T) {
		t.Parallel()
		assert.False(t, g.Supports(""))
	})
}

func TestDecodeGismeteoForecast(t *testing.T) {
	t.Parallel()

	const almaty = "1526384"

	t.Run("parses fixture into expected fields", func(t *testing.T) {
		t.Parallel()
		fixture := loadGismeteoFixture(t, "gismeteo_almaty.html")

		obs, err := decodeGismeteoForecast(fixture, almaty)
		require.NoError(t, err)
		require.NotNil(t, obs)

		assert.Equal(t, domain.ProviderGismeteo, obs.Provider,
			"Provider token must match domain.ProviderGismeteo (literal data token)")
		assert.Equal(t, almaty, obs.LocationID)
		assert.False(t, obs.CapturedAt.IsZero())
		assert.NotEmpty(t, obs.ForecastDate)

		// Temperature: today's high = 26°C, low = 19°C.
		require.NotNil(t, obs.TempMax, "TempMax must be non-nil")
		assert.InDelta(t, 26.0, *obs.TempMax, 1e-9)

		require.NotNil(t, obs.TempMin, "TempMin must be non-nil")
		assert.InDelta(t, 19.0, *obs.TempMin, 1e-9)

		// Precipitation: 0.3 + 0.05 + 0.05 + 0.1 = 0.50 mm.
		require.NotNil(t, obs.PrecipSum, "PrecipSum must be non-nil")
		assert.InDelta(t, 0.50, *obs.PrecipSum, 1e-9)

		// Condition: "облачно, небольшой дождь" → WMO 61 (slight rain).
		require.NotNil(t, obs.WeatherCode, "WeatherCode must be non-nil when condition is parseable")
		assert.Equal(t, 61, *obs.WeatherCode)

		// Fields gismeteo does not expose — must be nil, not zero.
		assert.Nil(t, obs.TempCurrent, "TempCurrent must be nil: gismeteo does not expose current temperature")
		assert.Nil(t, obs.TempFeels, "TempFeels must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.Humidity, "Humidity must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.WindSpeed, "WindSpeed must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.WindDir, "WindDir must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.Precip, "Precip (current) must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.CloudCover, "CloudCover must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.PrecipProbMax, "PrecipProbMax must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.Sunrise, "Sunrise must be nil: not exposed by gismeteo")
		assert.Nil(t, obs.Sunset, "Sunset must be nil: not exposed by gismeteo")
	})

	t.Run("empty HTML returns DOM-drift error when all fields are nil", func(t *testing.T) {
		t.Parallel()
		// After M2: when no regex matches (all four fields nil), decodeGismeteoForecast
		// returns a descriptive error instead of a silent all-nil observation.
		_, err := decodeGismeteoForecast([]byte(`<html></html>`), almaty)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no usable fields parsed")
	})

	t.Run("TempMax parse error on non-numeric value returns wrapped error", func(t *testing.T) {
		t.Parallel()
		// The is-active anchor and top:0px structure must match so the regex captures
		// the value field; the non-numeric string then fails strconv.ParseFloat.
		html := []byte(`<div class="weathertab is-active" data-tooltip="ясно">` +
			`<div class='value' style='top: 0px;width: 50%;'>` +
			`<temperature-value value="notanumber" from-unit="c" reactive></temperature-value>` +
			`</div></div>`)
		_, err := decodeGismeteoForecast(html, almaty)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse TempMax")
	})

	t.Run("TempMin parse error on non-numeric value returns wrapped error", func(t *testing.T) {
		t.Parallel()
		// No top:0px in this fragment → TempMax regex does not match; TempMin regex
		// matches top:5px and captures the non-numeric value.
		html := []byte(`<div class="weathertab is-active" data-tooltip="ясно">` +
			`<div class='value' style='top: 5px;width: 50%;'>` +
			`<temperature-value value="notanumber" from-unit="c" reactive></temperature-value>` +
			`</div></div>`)
		_, err := decodeGismeteoForecast(html, almaty)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse TempMin")
	})

	t.Run("PrecipSum parse error on non-numeric value returns wrapped error", func(t *testing.T) {
		t.Parallel()
		// No weathertab markup needed — precipitation regex is not anchored to is-active.
		// The non-numeric value string fails strconv.ParseFloat.
		html := []byte(`<precipitation-value class="item-unit unit-blue" value="x.y.z" from-unit="mm" reactive></precipitation-value>`)
		_, err := decodeGismeteoForecast(html, almaty)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse PrecipSum")
	})

	t.Run("provider token is always domain.ProviderGismeteo (not translated)", func(t *testing.T) {
		t.Parallel()
		fixture := loadGismeteoFixture(t, "gismeteo_almaty.html")
		obs, err := decodeGismeteoForecast(fixture, almaty)
		require.NoError(t, err)
		assert.Equal(t, "gismeteo", obs.Provider,
			"ProviderGismeteo is a data token and must never be translated")
	})
}

func TestMapGismeteoCondition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tooltip string
		wantWMO *int
	}{
		{"ясно", intPtrTest(0)},
		{"малооблачно", intPtrTest(1)},
		{"переменная облачность", intPtrTest(2)},
		{"облачно", intPtrTest(3)},
		{"пасмурно", intPtrTest(3)},
		{"туман", intPtrTest(45)},
		{"небольшой дождь", intPtrTest(61)},
		{"облачно, небольшой дождь", intPtrTest(61)}, // compound tooltip: more specific rule wins
		{"дождь", intPtrTest(61)},
		{"умеренный дождь", intPtrTest(63)},
		{"сильный дождь", intPtrTest(65)},
		{"небольшой снег", intPtrTest(71)},
		{"снег", intPtrTest(71)},
		{"умеренный снег", intPtrTest(73)},
		{"сильный снег", intPtrTest(75)},
		{"снег с дождём", intPtrTest(67)},
		{"гроза", intPtrTest(95)},
		{"гроза с градом", intPtrTest(99)},
		{"", nil},
		{"неизвестная погода xyz", nil},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.tooltip, func(t *testing.T) {
			t.Parallel()
			got := mapGismeteoCondition(tc.tooltip)
			if tc.wantWMO == nil {
				assert.Nil(t, got, "unknown tooltip must yield nil WeatherCode, not zero")
			} else {
				require.NotNil(t, got)
				assert.Equal(t, *tc.wantWMO, *got)
			}
		})
	}
}

func TestGismeteo_Forecast(t *testing.T) {
	t.Parallel()

	const almaty = "1526384"
	fixture := loadGismeteoFixture(t, "gismeteo_almaty.html")

	t.Run("returns observation for supported location", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		obs, err := g.Forecast(t.Context(), almaty)
		require.NoError(t, err)
		require.NotNil(t, obs)
		assert.Equal(t, domain.ProviderGismeteo, obs.Provider)
		assert.Equal(t, almaty, obs.LocationID)
	})

	t.Run("unknown location returns ErrGismeteoLocationUnavailable without HTTP call", func(t *testing.T) {
		t.Parallel()
		// The server must not be called for an unsupported location_id.
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		_, err := g.Forecast(t.Context(), "9999999")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGismeteoLocationUnavailable)
		assert.False(t, called, "HTTP server must not be called for an unsupported location")
	})

	t.Run("server error is surfaced", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		_, err := g.Forecast(t.Context(), almaty)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("request carries browser User-Agent", func(t *testing.T) {
		t.Parallel()
		var gotUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUA = r.Header.Get("User-Agent")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		_, _ = g.Forecast(t.Context(), almaty)
		assert.Equal(t, gismeteoUserAgent, gotUA, "User-Agent must equal the configured gismeteoUserAgent constant")
	})

	t.Run("transport failure returns wrapped error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		srv.Close() // close before the request so the transport fails immediately
		g := newGismeteoForTest(srv.Client(), srv.URL)

		_, err := g.Forecast(t.Context(), almaty)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gismeteo")
	})
}

func TestGismeteo_ForecastBatch(t *testing.T) {
	t.Parallel()

	const almaty = "1526384"
	const astana = "1526273"
	const unknown = "9999999"

	fixture := loadGismeteoFixture(t, "gismeteo_almaty.html")

	t.Run("fetches all supported ids and returns observations", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		obsByLoc, errByLoc := g.ForecastBatch(t.Context(), []string{almaty, astana})
		assert.Empty(t, errByLoc)
		require.Len(t, obsByLoc, 2)

		assert.Equal(t, domain.ProviderGismeteo, obsByLoc[almaty].Provider)
		assert.Equal(t, domain.ProviderGismeteo, obsByLoc[astana].Provider)
	})

	t.Run("unsupported location appears in errByLoc without aborting supported ones", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		obsByLoc, errByLoc := g.ForecastBatch(t.Context(), []string{almaty, unknown})
		require.Len(t, obsByLoc, 1, "only supported locations appear in obsByLoc")
		require.Len(t, errByLoc, 1, "unsupported location must appear in errByLoc")

		require.ErrorIs(t, errByLoc[unknown], ErrGismeteoLocationUnavailable)
		assert.Equal(t, domain.ProviderGismeteo, obsByLoc[almaty].Provider)
	})

	t.Run("per-location HTTP error appears in errByLoc without aborting others", func(t *testing.T) {
		t.Parallel()
		// Server returns 500 for almaty's path, 200 + fixture for everything else.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "almaty") {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixture)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		obsByLoc, errByLoc := g.ForecastBatch(t.Context(), []string{almaty, astana})
		require.Len(t, obsByLoc, 1)
		require.Len(t, errByLoc, 1)
		assert.Contains(t, errByLoc[almaty].Error(), "500")
		assert.Equal(t, domain.ProviderGismeteo, obsByLoc[astana].Provider)
	})

	t.Run("empty input returns empty maps without any HTTP calls", func(t *testing.T) {
		t.Parallel()
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		obsByLoc, errByLoc := g.ForecastBatch(t.Context(), nil)
		assert.Empty(t, obsByLoc)
		assert.Empty(t, errByLoc)
		assert.False(t, called, "HTTP server must not be called for empty input")
	})

	t.Run("all-unsupported input returns only errByLoc entries without any HTTP calls", func(t *testing.T) {
		t.Parallel()
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		g := newGismeteoForTest(srv.Client(), srv.URL)

		obsByLoc, errByLoc := g.ForecastBatch(t.Context(), []string{"9999999", "8888888"})
		assert.Empty(t, obsByLoc)
		assert.Len(t, errByLoc, 2)
		assert.False(t, called, "HTTP server must not be called when no specs are built")
	})
}

// intPtrTest is a local helper to create *int literals in test tables.
func intPtrTest(v int) *int { return &v }
