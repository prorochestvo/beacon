package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
