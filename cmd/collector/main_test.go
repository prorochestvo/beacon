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

		agent, err := wireWeather(cityRepo, obsRepo, "", nil)
		require.NoError(t, err)
		assert.NotNil(t, agent, "Open-Meteo is hardcoded always-on and must always produce a weather runner")
	})

	t.Run("invalid proxy URL returns an error", func(t *testing.T) {
		t.Parallel()
		cityRepo, err := repository.NewWeatherUserCityRepository(nil)
		require.NoError(t, err)
		obsRepo, err := repository.NewWeatherObservationRepository(nil)
		require.NoError(t, err)

		agent, err := wireWeather(cityRepo, obsRepo, "://bad-url", nil)
		require.Error(t, err)
		assert.Nil(t, agent)
	})
}
