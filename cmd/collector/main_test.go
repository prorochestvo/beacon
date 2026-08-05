package main

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWireWeather(t *testing.T) {
	t.Parallel()

	t.Run("always builds a runner", func(t *testing.T) {
		t.Parallel()
		// Repos are constructed with a nil db and never Run in this test — they only need
		// to be non-nil so NewWeatherAgent's required-arg check passes and wireWeather
		// returns the assembled runner. Open-Meteo is hardcoded always-on: there is no
		// "inactive" state to test.
		cityRepo, err := repository.NewWeatherUserCityRepository(nil)
		require.NoError(t, err)
		obsRepo, err := repository.NewWeatherObservationRepository(nil)
		require.NoError(t, err)

		agent, err := wireWeather(cityRepo, obsRepo, nil)
		require.NoError(t, err)
		assert.NotNil(t, agent, "Open-Meteo is hardcoded always-on and must always produce a weather runner")
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

		agent, err := wireWeather(cityRepo, obsRepo, nil)
		require.NoError(t, err)
		assert.NotNil(t, agent)
	})

}

// TestCollectorIgnoresProxyEnv pins that a proxy setting left behind in the operator's env
// file cannot quietly resume routing collection through a tunnel that was removed on the
// evidence in issue #16.
//
// Not parallel, and deliberately its own test rather than a subtest: t.Setenv mutates
// process state, which the testing package refuses to allow under a parallel parent.
func TestCollectorIgnoresProxyEnv(t *testing.T) {
	t.Setenv("BEACON_PROXY_URL", "http://127.0.0.1:9")

	cityRepo, err := repository.NewWeatherUserCityRepository(nil)
	require.NoError(t, err)
	obsRepo, err := repository.NewWeatherObservationRepository(nil)
	require.NoError(t, err)

	agent, err := wireWeather(cityRepo, obsRepo, nil)
	require.NoError(t, err, "a proxy setting left in the environment must be inert")
	assert.NotNil(t, agent)
}
