package main

import (
	"context"
	"errors"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/application/inspector"
	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
)

var _ gismeteoSourceLoader = (*fakeGismeteoSourceLoader)(nil)

// fakeGismeteoSourceLoader is a test double for gismeteoSourceLoader.
type fakeGismeteoSourceLoader struct {
	row *domain.WeatherSource
	err error
}

func (f *fakeGismeteoSourceLoader) ObtainWeatherSourceByProvider(_ context.Context, _ string) (*domain.WeatherSource, error) {
	return f.row, f.err
}

// inspectorNames extracts the Name() of each inspector for assertion.
func inspectorNames(insps []inspector.Inspector) []string {
	names := make([]string, 0, len(insps))
	for _, i := range insps {
		names = append(names, i.Name())
	}
	return names
}

func TestBuildWeatherAdvisoryInspectors(t *testing.T) {
	t.Parallel()

	t.Run("active gismeteo row registers both open-meteo and gismeteo", func(t *testing.T) {
		t.Parallel()
		loader := &fakeGismeteoSourceLoader{row: &domain.WeatherSource{
			Provider: domain.ProviderGismeteo,
			Active:   true,
			BaseURL:  "https://www.gismeteo.kz",
		}}
		got := buildWeatherAdvisoryInspectors(context.Background(), loader)
		assert.ElementsMatch(t, []string{"open-meteo", "gismeteo"}, inspectorNames(got))
	})

	t.Run("inactive gismeteo row omits the gismeteo inspector", func(t *testing.T) {
		t.Parallel()
		loader := &fakeGismeteoSourceLoader{row: &domain.WeatherSource{
			Provider: domain.ProviderGismeteo,
			Active:   false,
		}}
		got := buildWeatherAdvisoryInspectors(context.Background(), loader)
		names := inspectorNames(got)
		assert.Equal(t, []string{"open-meteo"}, names)
		assert.NotContains(t, names, "gismeteo",
			"an operator-disabled provider must not be registered as a health component")
	})

	t.Run("absent row defaults to active and registers gismeteo", func(t *testing.T) {
		t.Parallel()
		loader := &fakeGismeteoSourceLoader{row: nil, err: nil}
		got := buildWeatherAdvisoryInspectors(context.Background(), loader)
		assert.ElementsMatch(t, []string{"open-meteo", "gismeteo"}, inspectorNames(got))
	})

	t.Run("config load error falls back to registering gismeteo with the default URL", func(t *testing.T) {
		t.Parallel()
		loader := &fakeGismeteoSourceLoader{err: errors.New("db down")}
		got := buildWeatherAdvisoryInspectors(context.Background(), loader)
		assert.ElementsMatch(t, []string{"open-meteo", "gismeteo"}, inspectorNames(got),
			"a config-read failure must not hide gismeteo health; register with the default URL")
	})
}
