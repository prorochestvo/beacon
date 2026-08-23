package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/seilbekskindirov/beacon/internal/tools/rateextractor"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWireWeather(t *testing.T) {
	t.Parallel()

	t.Run("always builds both runners", func(t *testing.T) {
		t.Parallel()
		// Repos are constructed with a nil db and never Run in this test — they only need
		// to be non-nil so the agents' required-arg checks pass and wireWeather returns the
		// assembled runners. Open-Meteo is hardcoded always-on: there is no "inactive"
		// state to test.
		cityRepo, err := repository.NewWeatherUserCityRepository(nil)
		require.NoError(t, err)
		obsRepo, err := repository.NewWeatherObservationRepository(nil)
		require.NoError(t, err)
		forecastRepo, err := repository.NewWeatherForecastDayRepository(nil)
		require.NoError(t, err)

		agents, err := wireWeather(cityRepo, obsRepo, forecastRepo, nil)
		require.NoError(t, err)
		require.Len(t, agents, 2, "current conditions and the long-range forecast are separate runners")
		for i, agent := range agents {
			assert.NotNil(t, agent, "Open-Meteo is hardcoded always-on and must always produce runner %d", i)
		}
	})

	t.Run("weather collection is direct", func(t *testing.T) {
		t.Parallel()
		// This replaces a test that fed wireWeather a malformed proxy URL and asserted it
		// errored. There is no proxy URL to malform any more: the collector reaches every
		// upstream directly, which also makes it agree with cmd/web, whose Open-Meteo
		// health probe has always been direct.
		cityRepo, err := repository.NewWeatherUserCityRepository(nil)
		require.NoError(t, err)
		obsRepo, err := repository.NewWeatherObservationRepository(nil)
		require.NoError(t, err)
		forecastRepo, err := repository.NewWeatherForecastDayRepository(nil)
		require.NoError(t, err)

		agents, err := wireWeather(cityRepo, obsRepo, forecastRepo, nil)
		require.NoError(t, err)
		assert.NotEmpty(t, agents)
	})

}

// TestWeatherIgnoresProxyEnv pins that weather collection stays direct whatever
// BEACON_PROXY_URL says. Rate sources gained a per-source opt-in (issue #28); weather did
// not, because Open-Meteo is one global keyless provider and cmd/web's health inspector
// probes it directly — making the two disagree again would restore a wart that was
// deliberately removed.
//
// Not parallel, and deliberately its own test rather than a subtest: t.Setenv mutates
// process state, which the testing package refuses to allow under a parallel parent.
func TestWeatherIgnoresProxyEnv(t *testing.T) {
	t.Setenv(internal.EnvProxyURL, "http://127.0.0.1:9")

	cityRepo, err := repository.NewWeatherUserCityRepository(nil)
	require.NoError(t, err)
	obsRepo, err := repository.NewWeatherObservationRepository(nil)
	require.NoError(t, err)
	forecastRepo, err := repository.NewWeatherForecastDayRepository(nil)
	require.NoError(t, err)

	agents, err := wireWeather(cityRepo, obsRepo, forecastRepo, nil)
	require.NoError(t, err, "a proxy setting in the environment must be inert for weather")
	assert.NotEmpty(t, agents)
}

// TestProxyEnvAloneDoesNotRouteCollection pins the deploy-time half of the two-level
// contract: BEACON_PROXY_URL reaches the rate agent, but nothing moves until a source row
// sets options.use_proxy. An operator can therefore configure the variable without
// re-routing all 56 sources — which is what preserves the measured conclusion of issue #16
// that direct is faster, more available and less suspicious for these hosts.
//
// The configured proxy points at a closed port, so the assertion needs no counter: a
// fetch that succeeds cannot have gone through it. Reading the value with os.Getenv
// rather than the package-level ProxyURL var, which is initialised at process start,
// long before t.Setenv runs.
func TestProxyEnvAloneDoesNotRouteCollection(t *testing.T) {
	const deadProxy = "http://127.0.0.1:9" // discard port: nothing listens
	t.Setenv(internal.EnvProxyURL, deadProxy)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("451.0"))
	}))
	t.Cleanup(target.Close)

	extractor, err := rateextractor.NewRateExtractor(
		&discardingRateValueRepository{}, os.Getenv(internal.EnvProxyURL), 5*time.Second, io.Discard,
		internal.UserAgent,
	)
	require.NoError(t, err)

	// No Options.UseProxy: the default a source row carries when nobody opted it in.
	err = extractor.Run(t.Context(), &domain.RateSource{
		Name:  "DEFAULT_DIRECT",
		URL:   target.URL,
		Rules: []domain.RateSourceRule{{Method: domain.MethodStoreToRate}},
	})
	assert.NoError(t, err,
		"an unflagged source must fetch direct; routing it through the configured proxy would fail here")
}

// discardingRateValueRepository accepts every write and keeps nothing.
type discardingRateValueRepository struct{}

func (*discardingRateValueRepository) RetainRateValue(_ context.Context, _ *domain.RateValue) error {
	return nil
}
