package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherObservation_MarshalHourlyJSON(t *testing.T) {
	t.Parallel()

	t.Run("nil Hourly returns nil bytes without error", func(t *testing.T) {
		t.Parallel()
		obs := &WeatherObservation{Hourly: nil}
		data, err := obs.MarshalHourlyJSON()
		require.NoError(t, err)
		assert.Nil(t, data)
	})

	t.Run("empty Hourly returns nil bytes without error", func(t *testing.T) {
		t.Parallel()
		obs := &WeatherObservation{Hourly: []WeatherHourlyPoint{}}
		data, err := obs.MarshalHourlyJSON()
		require.NoError(t, err)
		assert.Nil(t, data)
	})

	t.Run("single point encodes time as RFC3339 UTC", func(t *testing.T) {
		t.Parallel()
		ts := time.Date(2026, 6, 30, 7, 0, 0, 0, time.UTC)
		p := 69
		obs := &WeatherObservation{Hourly: []WeatherHourlyPoint{{Time: ts, PrecipProb: &p}}}
		data, err := obs.MarshalHourlyJSON()
		require.NoError(t, err)
		require.NotNil(t, data)
		assert.Contains(t, string(data), "2026-06-30T07:00:00Z")
	})

	t.Run("nil PrecipProb and Temp fields are omitted from JSON", func(t *testing.T) {
		t.Parallel()
		ts := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)
		obs := &WeatherObservation{Hourly: []WeatherHourlyPoint{{Time: ts}}}
		data, err := obs.MarshalHourlyJSON()
		require.NoError(t, err)
		s := string(data)
		assert.NotContains(t, s, `"p"`, "nil PrecipProb must be omitted via omitempty")
		assert.NotContains(t, s, `"c"`, "nil Temp must be omitted via omitempty")
		assert.Contains(t, s, `"t"`)
	})

	t.Run("multiple points round-trip count via unmarshal", func(t *testing.T) {
		t.Parallel()
		p1, p2 := 30, 80
		obs := &WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: time.Date(2026, 6, 30, 7, 0, 0, 0, time.UTC), PrecipProb: &p1},
				{Time: time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC), PrecipProb: &p2},
			},
		}
		data, err := obs.MarshalHourlyJSON()
		require.NoError(t, err)
		var got WeatherObservation
		require.NoError(t, got.UnmarshalHourlyJSON(data))
		require.Len(t, got.Hourly, 2)
	})
}

func TestWeatherObservation_UnmarshalHourlyJSON(t *testing.T) {
	t.Parallel()

	t.Run("nil input yields empty non-nil slice", func(t *testing.T) {
		t.Parallel()
		obs := &WeatherObservation{}
		require.NoError(t, obs.UnmarshalHourlyJSON(nil))
		assert.NotNil(t, obs.Hourly)
		assert.Empty(t, obs.Hourly)
	})

	t.Run("empty bytes yield empty non-nil slice", func(t *testing.T) {
		t.Parallel()
		obs := &WeatherObservation{}
		require.NoError(t, obs.UnmarshalHourlyJSON([]byte{}))
		assert.NotNil(t, obs.Hourly)
		assert.Empty(t, obs.Hourly)
	})

	t.Run("malformed JSON returns error", func(t *testing.T) {
		t.Parallel()
		obs := &WeatherObservation{}
		err := obs.UnmarshalHourlyJSON([]byte(`{not json`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal hourly_json")
	})

	t.Run("invalid time string in point returns error", func(t *testing.T) {
		t.Parallel()
		obs := &WeatherObservation{}
		err := obs.UnmarshalHourlyJSON([]byte(`[{"t":"NOT-A-TIME"}]`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse hourly time")
	})

	t.Run("round-trip preserves all fields", func(t *testing.T) {
		t.Parallel()
		ts := time.Date(2026, 6, 30, 14, 0, 0, 0, time.UTC)
		prob := 75
		temp := 28.5
		original := &WeatherObservation{
			Hourly: []WeatherHourlyPoint{{Time: ts, PrecipProb: &prob, Temp: &temp}},
		}
		data, err := original.MarshalHourlyJSON()
		require.NoError(t, err)

		got := &WeatherObservation{}
		require.NoError(t, got.UnmarshalHourlyJSON(data))
		require.Len(t, got.Hourly, 1)
		assert.Equal(t, ts.UTC(), got.Hourly[0].Time.UTC())
		require.NotNil(t, got.Hourly[0].PrecipProb)
		assert.Equal(t, prob, *got.Hourly[0].PrecipProb)
		require.NotNil(t, got.Hourly[0].Temp)
		assert.InDelta(t, temp, *got.Hourly[0].Temp, 1e-6)
	})
}

func TestWMOWeatherCode(t *testing.T) {
	t.Parallel()

	t.Run("clear sky", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(0)
		assert.Equal(t, "Clear sky", text)
		assert.Equal(t, "☀️", emoji)
	})

	t.Run("partly cloudy", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(2)
		assert.Equal(t, "Partly cloudy", text)
		assert.Equal(t, "⛅", emoji)
	})

	t.Run("fog", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(45)
		assert.Equal(t, "Foggy", text)
		assert.NotEmpty(t, emoji)
	})

	t.Run("moderate rain", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(63)
		assert.Equal(t, "Moderate rain", text)
		assert.Equal(t, "🌧️", emoji)
	})

	t.Run("thunderstorm", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(95)
		assert.Equal(t, "Thunderstorm", text)
		assert.Equal(t, "⛈️", emoji)
	})

	t.Run("snowfall", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(71)
		assert.Equal(t, "Slight snowfall", text)
		assert.Equal(t, "❄️", emoji)
	})

	t.Run("unknown code returns safe default not empty", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(999)
		assert.Equal(t, "Unknown", text)
		assert.Equal(t, "❓", emoji)
	})

	t.Run("unknown negative code returns safe default", func(t *testing.T) {
		t.Parallel()
		text, emoji := WMOWeatherCode(-1)
		assert.Equal(t, "Unknown", text)
		assert.NotEmpty(t, emoji)
	})
}
