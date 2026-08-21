package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherForecastDay_IsRainDay(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		rain *float64
		want bool
	}{
		{name: "nil is not a rain day", rain: nil, want: false},
		{name: "zero is not a rain day", rain: f(0), want: false},
		{name: "just under the bar is drizzle, not rain", rain: f(0.9), want: false},
		{name: "exactly at the bar counts", rain: f(1.0), want: true},
		{name: "well over the bar counts", rain: f(12.4), want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := WeatherForecastDay{RainSum: c.rain}
			assert.Equal(t, c.want, d.IsRainDay())
		})
	}
}

func TestWeatherForecastDay_IsSnowDay(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		snow *float64
		want bool
	}{
		{name: "nil is not a snow day", snow: nil, want: false},
		{name: "zero is not a snow day", snow: f(0), want: false},
		{name: "just under the bar is a dusting", snow: f(0.9), want: false},
		{name: "exactly at the bar counts", snow: f(1.0), want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := WeatherForecastDay{SnowfallSum: c.snow}
			assert.Equal(t, c.want, d.IsSnowDay())
		})
	}
}

func TestWeatherForecastDay_ZeroState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		min     *float64
		max     *float64
		want    WeatherZeroState
		wantLbl string
	}{
		{name: "both nil is unknown", min: nil, max: nil, want: WeatherZeroStateUnknown, wantLbl: ""},
		{name: "min alone is unknown", min: f(-3), max: nil, want: WeatherZeroStateUnknown, wantLbl: ""},
		{name: "max alone is unknown", min: nil, max: f(3), want: WeatherZeroStateUnknown, wantLbl: ""},
		{name: "warm day is above", min: f(11.2), max: f(21.5), want: WeatherZeroStateAbove, wantLbl: "above"},
		{name: "frozen day is below", min: f(-14), max: f(-2), want: WeatherZeroStateBelow, wantLbl: "below"},
		{name: "max exactly zero is still below", min: f(-8), max: f(0), want: WeatherZeroStateBelow, wantLbl: "below"},
		{name: "min exactly zero crosses", min: f(0), max: f(5), want: WeatherZeroStateCrossing, wantLbl: "crossing"},
		{name: "straddling zero crosses", min: f(-4.5), max: f(2.1), want: WeatherZeroStateCrossing, wantLbl: "crossing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := WeatherForecastDay{TempMin: c.min, TempMax: c.max}
			assert.Equal(t, c.want, d.ZeroState())
			assert.Equal(t, c.wantLbl, d.ZeroState().Label())
		})
	}
}

func TestWeatherOutlook_NotableDays(t *testing.T) {
	t.Parallel()

	t.Run("an all-clear window reports nothing", func(t *testing.T) {
		t.Parallel()
		o := NewWeatherOutlook([]WeatherForecastDay{
			warmDay("2026-08-21"), warmDay("2026-08-22"), warmDay("2026-08-23"),
		}, "2026-08-21")
		assert.Empty(t, o.NotableDays())
	})

	t.Run("the baseline day is never reported however wet it is", func(t *testing.T) {
		t.Parallel()
		today := warmDay("2026-08-21")
		today.RainSum = f(9)
		o := NewWeatherOutlook([]WeatherForecastDay{today, warmDay("2026-08-22")}, "2026-08-21")
		assert.Empty(t, o.NotableDays())
	})

	t.Run("rain and snow days are reported", func(t *testing.T) {
		t.Parallel()
		wet := warmDay("2026-08-23")
		wet.RainSum = f(1.3)
		snowy := warmDay("2026-08-25")
		snowy.SnowfallSum = f(4.0)
		o := NewWeatherOutlook([]WeatherForecastDay{
			warmDay("2026-08-21"), warmDay("2026-08-22"), wet, warmDay("2026-08-24"), snowy,
		}, "2026-08-21")
		got := o.NotableDays()
		require.Len(t, got, 2)
		assert.Equal(t, "2026-08-23", got[0].ForecastDate)
		assert.Equal(t, "2026-08-25", got[1].ForecastDate)
	})

	t.Run("only the zero-state transitions are reported, not every cold day", func(t *testing.T) {
		t.Parallel()
		o := NewWeatherOutlook([]WeatherForecastDay{
			tempDay("2026-11-01", 2, 8),    // baseline: above
			tempDay("2026-11-02", 1, 6),    // above, no change
			tempDay("2026-11-03", -2, 4),   // crossing: reported
			tempDay("2026-11-04", -3, 3),   // crossing, no change
			tempDay("2026-11-05", -9, -1),  // below: reported
			tempDay("2026-11-06", -12, -4), // below, no change
			tempDay("2026-11-07", -11, -3), // below, no change
			tempDay("2026-11-08", -2, 5),   // crossing: reported
		}, "2026-11-01")
		got := o.NotableDays()
		require.Len(t, got, 3)
		assert.Equal(t, "2026-11-03", got[0].ForecastDate)
		assert.Equal(t, "2026-11-05", got[1].ForecastDate)
		assert.Equal(t, "2026-11-08", got[2].ForecastDate)
	})

	t.Run("a day with no bounds does not report a transition of its own", func(t *testing.T) {
		t.Parallel()
		gap := WeatherForecastDay{ForecastDate: "2026-11-03"}
		o := NewWeatherOutlook([]WeatherForecastDay{
			tempDay("2026-11-01", -9, -1), // baseline: below
			tempDay("2026-11-02", -8, -2), // below
			gap,                           // unknown
			tempDay("2026-11-04", -7, -3), // below again: still no transition
		}, "2026-11-01")
		assert.Empty(t, o.NotableDays())
	})

	t.Run("a day with no bounds does not reset the comparison either", func(t *testing.T) {
		t.Parallel()
		gap := WeatherForecastDay{ForecastDate: "2026-11-03"}
		o := NewWeatherOutlook([]WeatherForecastDay{
			tempDay("2026-11-01", -9, -1), // baseline: below
			tempDay("2026-11-02", -8, -2), // below
			gap,                           // unknown: the last classified state stays -1 below
			tempDay("2026-11-04", -2, 6),  // crossing, measured against the day before the gap
		}, "2026-11-01")
		got := o.NotableDays()
		require.Len(t, got, 1)
		assert.Equal(t, "2026-11-04", got[0].ForecastDate)
	})

	t.Run("days out of order and before the baseline are windowed away", func(t *testing.T) {
		t.Parallel()
		wet := warmDay("2026-08-23")
		wet.RainSum = f(2)
		stale := warmDay("2026-08-19")
		stale.RainSum = f(5)
		o := NewWeatherOutlook([]WeatherForecastDay{
			wet, stale, warmDay("2026-08-22"), warmDay("2026-08-21"),
		}, "2026-08-21")
		require.Len(t, o.Days(), 3)
		assert.Equal(t, "2026-08-21", o.Days()[0].ForecastDate)
		got := o.NotableDays()
		require.Len(t, got, 1)
		assert.Equal(t, "2026-08-23", got[0].ForecastDate)
	})
}

func TestWeatherOutlook_Signature(t *testing.T) {
	t.Parallel()

	t.Run("an empty outlook is the version prefix alone, never the empty string", func(t *testing.T) {
		t.Parallel()
		o := NewWeatherOutlook([]WeatherForecastDay{warmDay("2026-08-21")}, "2026-08-21")
		assert.Equal(t, "o1:", o.Signature())
		assert.NotEmpty(t, o.Signature())
	})

	t.Run("equal outlooks produce equal signatures", func(t *testing.T) {
		t.Parallel()
		build := func() WeatherOutlook {
			wet := tempDay("2026-08-23", 14.6, 22.0)
			wet.RainSum = f(1.3)
			return NewWeatherOutlook([]WeatherForecastDay{tempDay("2026-08-21", 11.2, 21.5), wet}, "2026-08-21")
		}
		first, second := build().Signature(), build().Signature()
		assert.Equal(t, first, second)
		assert.Equal(t, "o1:2026-08-23:R+", first)
	})

	t.Run("a changed flag changes the signature", func(t *testing.T) {
		t.Parallel()
		wet := tempDay("2026-08-23", 14.6, 22.0)
		wet.RainSum = f(1.3)
		before := NewWeatherOutlook([]WeatherForecastDay{tempDay("2026-08-21", 11.2, 21.5), wet}, "2026-08-21")

		wet.SnowfallSum = f(2)
		after := NewWeatherOutlook([]WeatherForecastDay{tempDay("2026-08-21", 11.2, 21.5), wet}, "2026-08-21")

		assert.NotEqual(t, before.Signature(), after.Signature())
		assert.Equal(t, "o1:2026-08-23:RS+", after.Signature())
	})
}

func TestCompareWeatherOutlookSignatures(t *testing.T) {
	t.Parallel()

	t.Run("no previous state marks nothing", func(t *testing.T) {
		t.Parallel()
		got := CompareWeatherOutlookSignatures("", "o1:2026-08-23:R+;2026-08-25:S-")
		assert.Empty(t, got.Changed)
		assert.Empty(t, got.Cleared)
	})

	t.Run("a different signature version marks nothing", func(t *testing.T) {
		t.Parallel()
		got := CompareWeatherOutlookSignatures("o0:2026-08-23:R+", "o1:2026-08-23:R+")
		assert.Empty(t, got.Changed)
		assert.Empty(t, got.Cleared)
	})

	t.Run("new and changed days are marked, unchanged ones are not", func(t *testing.T) {
		t.Parallel()
		got := CompareWeatherOutlookSignatures(
			"o1:2026-08-23:R+;2026-08-25:R+",
			"o1:2026-08-23:R+;2026-08-25:RS+;2026-08-27:S-",
		)
		assert.False(t, got.Changed["2026-08-23"])
		assert.True(t, got.Changed["2026-08-25"])
		assert.True(t, got.Changed["2026-08-27"])
		assert.Empty(t, got.Cleared)
	})

	t.Run("days that stopped being notable are reported cleared, ascending", func(t *testing.T) {
		t.Parallel()
		got := CompareWeatherOutlookSignatures(
			"o1:2026-08-23:R+;2026-08-25:R+;2026-08-27:S-",
			"o1:2026-08-25:R+",
		)
		assert.Empty(t, got.Changed)
		assert.Equal(t, []string{"2026-08-23", "2026-08-27"}, got.Cleared)
	})

	t.Run("an outlook emptying out clears everything it held", func(t *testing.T) {
		t.Parallel()
		got := CompareWeatherOutlookSignatures("o1:2026-08-23:R+", "o1:")
		assert.Empty(t, got.Changed)
		assert.Equal(t, []string{"2026-08-23"}, got.Cleared)
	})
}

// f returns a pointer to v, so table cases can express "absent" as nil.
func f(v float64) *float64 { return &v }

// tempDay returns a day carrying only a temperature range.
func tempDay(date string, minTemp, maxTemp float64) WeatherForecastDay {
	return WeatherForecastDay{ForecastDate: date, TempMin: f(minTemp), TempMax: f(maxTemp)}
}

// warmDay returns a dry day comfortably above freezing.
func warmDay(date string) WeatherForecastDay {
	return tempDay(date, 11.2, 21.5)
}
