package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ weatherSourceLoader = (*fakeWeatherSourceLoader)(nil)
var _ gismeteoCoverageLoader = (*fakeGismeteoCoverageLoader)(nil)

// fakeWeatherSourceLoader is a test double for weatherSourceLoader.
type fakeWeatherSourceLoader struct {
	sources []domain.WeatherSource
	err     error
}

func (f *fakeWeatherSourceLoader) ObtainAllWeatherSources(context.Context) ([]domain.WeatherSource, error) {
	return f.sources, f.err
}

// fakeGismeteoCoverageLoader is a test double for gismeteoCoverageLoader.
type fakeGismeteoCoverageLoader struct {
	coverage map[string]domain.WeatherGismeteoCity
	err      error
}

func (f *fakeGismeteoCoverageLoader) ObtainGismeteoCoverage(context.Context) (map[string]domain.WeatherGismeteoCity, error) {
	return f.coverage, f.err
}

// coverageFixture returns a minimal non-empty gismeteo coverage map for tests.
func coverageFixture() map[string]domain.WeatherGismeteoCity {
	return map[string]domain.WeatherGismeteoCity{
		"1526384": {LocationID: "1526384", Slug: "almaty", GismeteoID: 5205, Label: "Almaty"},
	}
}

func TestGismeteoOption(t *testing.T) {
	t.Parallel()

	gismeteoActive := map[string]domain.WeatherSource{
		domain.ProviderGismeteo: {
			Provider:         domain.ProviderGismeteo,
			Active:           true,
			BaseURL:          "https://www.gismeteo.kz",
			ThrottleInterval: "3h",
		},
	}

	t.Run("active provider with coverage returns an option", func(t *testing.T) {
		t.Parallel()
		opt, err := gismeteoOption(context.Background(), gismeteoActive,
			&fakeGismeteoCoverageLoader{coverage: coverageFixture()}, "", io.Discard)
		require.NoError(t, err)
		assert.NotNil(t, opt, "active provider + non-empty coverage must yield a gismeteo option")
	})

	t.Run("inactive provider returns no option", func(t *testing.T) {
		t.Parallel()
		byProvider := map[string]domain.WeatherSource{
			domain.ProviderGismeteo: {Provider: domain.ProviderGismeteo, Active: false},
		}
		opt, err := gismeteoOption(context.Background(), byProvider,
			&fakeGismeteoCoverageLoader{coverage: coverageFixture()}, "", io.Discard)
		require.NoError(t, err)
		assert.Nil(t, opt, "an operator-disabled provider must skip the gismeteo phase")
	})

	t.Run("absent provider row defaults to active", func(t *testing.T) {
		t.Parallel()
		opt, err := gismeteoOption(context.Background(), map[string]domain.WeatherSource{},
			&fakeGismeteoCoverageLoader{coverage: coverageFixture()}, "", io.Discard)
		require.NoError(t, err)
		assert.NotNil(t, opt, "a missing gismeteo row defaults to active")
	})

	t.Run("empty coverage returns no option", func(t *testing.T) {
		t.Parallel()
		opt, err := gismeteoOption(context.Background(), gismeteoActive,
			&fakeGismeteoCoverageLoader{coverage: map[string]domain.WeatherGismeteoCity{}}, "", io.Discard)
		require.NoError(t, err)
		assert.Nil(t, opt, "empty coverage disables the gismeteo phase")
	})

	t.Run("coverage load error returns no option and no error", func(t *testing.T) {
		t.Parallel()
		opt, err := gismeteoOption(context.Background(), gismeteoActive,
			&fakeGismeteoCoverageLoader{err: errors.New("db down")}, "", io.Discard)
		require.NoError(t, err, "a coverage read failure is non-fatal — it skips gismeteo, never aborts collection")
		assert.Nil(t, opt)
	})

	t.Run("malformed throttle interval falls back without error", func(t *testing.T) {
		t.Parallel()
		byProvider := map[string]domain.WeatherSource{
			domain.ProviderGismeteo: {Provider: domain.ProviderGismeteo, Active: true, ThrottleInterval: "banana"},
		}
		opt, err := gismeteoOption(context.Background(), byProvider,
			&fakeGismeteoCoverageLoader{coverage: coverageFixture()}, "", io.Discard)
		require.NoError(t, err, "a bad throttle_interval must fall back to the default, never abort")
		assert.NotNil(t, opt)
	})
}

func TestWireWeather(t *testing.T) {
	t.Parallel()

	t.Run("open-meteo inactive disables weather collection", func(t *testing.T) {
		t.Parallel()
		src := &fakeWeatherSourceLoader{sources: []domain.WeatherSource{
			{Provider: domain.ProviderOpenMeteo, Active: false},
		}}
		// weatherCity/weatherObs are nil: the inactive branch returns before NewWeatherAgent
		// would touch them.
		agent, err := wireWeather(nil, nil, src, &fakeGismeteoCoverageLoader{}, "", io.Discard)
		require.NoError(t, err)
		assert.Nil(t, agent, "an inactive open-meteo row means no weather collection at all")
	})

	t.Run("open-meteo active builds a runner", func(t *testing.T) {
		t.Parallel()
		// Repos are constructed with a nil db and never Run in this test — they only need
		// to be non-nil so NewWeatherAgent's required-arg check passes and wireWeather
		// returns the assembled runner.
		cityRepo, err := repository.NewWeatherUserCityRepository(nil)
		require.NoError(t, err)
		obsRepo, err := repository.NewWeatherObservationRepository(nil)
		require.NoError(t, err)

		src := &fakeWeatherSourceLoader{sources: []domain.WeatherSource{
			{Provider: domain.ProviderOpenMeteo, Active: true},
		}}
		agent, err := wireWeather(cityRepo, obsRepo, src,
			&fakeGismeteoCoverageLoader{coverage: coverageFixture()}, "", io.Discard)
		require.NoError(t, err)
		assert.NotNil(t, agent, "an active open-meteo row must produce a weather runner")
	})
}
