package domain

import (
	"testing"
	"time"
	_ "time/tzdata" // embedded IANA tzdata so LoadLocation works without system tzdata

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherUserCity_Validate(t *testing.T) {
	t.Parallel()

	t.Run("morning_summary ignores condition_value", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyMorningSummary, ConditionValue: "ignored"}
		require.NoError(t, c.Validate())
	})

	t.Run("alert_heat with valid numeric value passes", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "35"}
		require.NoError(t, c.Validate())
	})

	t.Run("alert_heat with decimal value passes", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "35.5"}
		require.NoError(t, c.Validate())
	})

	t.Run("alert_heat with non-numeric value returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "hot"}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "valid number")
	})

	t.Run("alert_heat with empty value returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertHeat, ConditionValue: ""}
		err := c.Validate()
		require.Error(t, err)
	})

	t.Run("alert_frost with valid numeric value passes", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertFrost, ConditionValue: "0"}
		require.NoError(t, c.Validate())
	})

	t.Run("alert_frost with negative value passes (frost below zero is valid)", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertFrost, ConditionValue: "-5"}
		require.NoError(t, c.Validate())
	})

	t.Run("alert_frost with non-numeric value returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertFrost, ConditionValue: "cold"}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "valid number")
	})

	t.Run("alert_thunderstorm with empty value passes", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertThunderstorm, ConditionValue: ""}
		require.NoError(t, c.Validate())
	})

	t.Run("alert_thunderstorm with any value passes (value is ignored)", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertThunderstorm, ConditionValue: "whatever"}
		require.NoError(t, c.Validate())
	})

	t.Run("rain_alert with valid integer percent passes", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: "70"}
		require.NoError(t, c.Validate())
	})

	t.Run("rain_alert with decimal percent passes", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: "69.5"}
		require.NoError(t, c.Validate())
	})

	t.Run("rain_alert with 0 returns error (exclusive lower bound)", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: "0"}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probability percent")
	})

	t.Run("rain_alert with 100 passes (highest valid bound)", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: "100"}
		require.NoError(t, c.Validate())
	})

	t.Run("rain_alert with negative value returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: "-1"}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probability percent")
	})

	t.Run("rain_alert with value above 100 returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: "101"}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probability percent")
	})

	t.Run("rain_alert with non-numeric value returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: "heavy"}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "valid number")
	})

	t.Run("rain_alert with empty value returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: WeatherNotifyAlertRain, ConditionValue: ""}
		err := c.Validate()
		require.Error(t, err)
	})

	t.Run("unknown kind returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{NotifyKind: "unknown_kind", ConditionValue: ""}
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown notify_kind")
	})
}

func TestWeatherUserCity_EvaluateAlert(t *testing.T) {
	t.Parallel()

	ptr64 := func(v float64) *float64 { return &v }
	ptrint := func(v int) *int { return &v }

	t.Run("heat fires when TempMax equals threshold", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "35"}
		obs := WeatherObservation{TempMax: ptr64(35.0)}
		fired, reason, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "≥")
		assert.Contains(t, reason, "+35.0°C")
	})

	t.Run("heat fires when TempMax exceeds threshold", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "35"}
		obs := WeatherObservation{TempMax: ptr64(36.5)}
		fired, reason, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "+36.5°C")
	})

	t.Run("heat does not fire when TempMax below threshold", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "35"}
		obs := WeatherObservation{TempMax: ptr64(34.9)}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("heat does not fire when TempMax is nil", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "35"}
		obs := WeatherObservation{TempMax: nil}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("heat returns error on bad condition_value", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertHeat, ConditionValue: "notanumber"}
		obs := WeatherObservation{TempMax: ptr64(40.0)}
		_, _, err := c.EvaluateAlert(obs, time.Time{})
		require.Error(t, err)
	})

	t.Run("frost fires when TempMin equals threshold", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertFrost, ConditionValue: "0"}
		obs := WeatherObservation{TempMin: ptr64(0.0)}
		fired, reason, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "≤")
		assert.Contains(t, reason, "+0.0°C")
	})

	t.Run("frost fires when TempMin below threshold", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertFrost, ConditionValue: "0"}
		obs := WeatherObservation{TempMin: ptr64(-7.2)}
		fired, reason, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.True(t, fired)
		// Must use U+2212 minus sign for negative temperature
		assert.Contains(t, reason, "−7.2°C")
		assert.NotContains(t, reason, "-7.2°C")
	})

	t.Run("frost does not fire when TempMin above threshold", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertFrost, ConditionValue: "0"}
		obs := WeatherObservation{TempMin: ptr64(0.1)}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("frost does not fire when TempMin is nil", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertFrost, ConditionValue: "0"}
		obs := WeatherObservation{TempMin: nil}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("thunderstorm fires for code 95", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertThunderstorm}
		obs := WeatherObservation{WeatherCode: ptrint(95)}
		fired, reason, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "Thunderstorm")
	})

	t.Run("thunderstorm fires for code 96", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertThunderstorm}
		obs := WeatherObservation{WeatherCode: ptrint(96)}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.True(t, fired)
	})

	t.Run("thunderstorm fires for code 99", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertThunderstorm}
		obs := WeatherObservation{WeatherCode: ptrint(99)}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.True(t, fired)
	})

	t.Run("thunderstorm does not fire for code 3 (overcast)", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertThunderstorm}
		obs := WeatherObservation{WeatherCode: ptrint(3)}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("thunderstorm does not fire when WeatherCode is nil", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyAlertThunderstorm}
		obs := WeatherObservation{WeatherCode: nil}
		fired, _, err := c.EvaluateAlert(obs, time.Time{})
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("rain_alert dispatches to EvaluateRain and fires within window", func(t *testing.T) {
		t.Parallel()
		now := time.Date(2026, 6, 30, 7, 0, 0, 0, time.UTC)
		prob := 85
		c := &WeatherUserCity{ID: "rain-c", NotifyKind: WeatherNotifyAlertRain, ConditionValue: "70"}
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(time.Hour), PrecipProb: &prob},
			},
		}
		fired, reason, err := c.EvaluateAlert(obs, now)
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "Rain likely")
		assert.Contains(t, reason, "85%")
	})

	t.Run("morning_summary kind returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: WeatherNotifyMorningSummary}
		_, _, err := c.EvaluateAlert(WeatherObservation{}, time.Time{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not an alert kind")
	})

	t.Run("unknown kind returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{ID: "c1", NotifyKind: "completely_unknown"}
		_, _, err := c.EvaluateAlert(WeatherObservation{}, time.Time{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not an alert kind")
	})
}

func TestWeatherUserCity_IsMorningDue(t *testing.T) {
	t.Parallel()

	// Almaty is UTC+5, no DST. A 07:00 local fire is 02:00 UTC.
	const tz = "Asia/Almaty"
	loc, err := time.LoadLocation(tz)
	require.NoError(t, err)

	// local 2026-06-15 07:00:00 = UTC 2026-06-15 02:00:00
	fireLocal := time.Date(2026, 6, 15, 7, 0, 0, 0, loc)
	fireUTC := fireLocal.UTC()

	city := func(lastNotified time.Time) *WeatherUserCity {
		return &WeatherUserCity{
			ID:             "wuc-test",
			Timezone:       tz,
			NotifyHour:     7,
			LastNotifiedAt: lastNotified,
		}
	}

	t.Run("before local fire time is not due", func(t *testing.T) {
		t.Parallel()
		c := city(time.Time{})
		// 06:59 Almaty = 01:59 UTC
		before := fireUTC.Add(-1 * time.Minute)
		due, err := c.IsMorningDue(before)
		require.NoError(t, err)
		assert.False(t, due)
	})

	t.Run("after fire time, never notified, is due", func(t *testing.T) {
		t.Parallel()
		c := city(time.Time{})
		after := fireUTC.Add(1 * time.Minute)
		due, err := c.IsMorningDue(after)
		require.NoError(t, err)
		assert.True(t, due)
	})

	t.Run("after fire time, already notified today, is not due", func(t *testing.T) {
		t.Parallel()
		// notified at 07:30 local same day
		notifiedToday := time.Date(2026, 6, 15, 7, 30, 0, 0, loc)
		c := city(notifiedToday.UTC())
		// now is 08:00 local same day
		now := time.Date(2026, 6, 15, 8, 0, 0, 0, loc)
		due, err := c.IsMorningDue(now.UTC())
		require.NoError(t, err)
		assert.False(t, due)
	})

	t.Run("notified yesterday is due today", func(t *testing.T) {
		t.Parallel()
		// notified yesterday at 07:30 local
		yesterday := time.Date(2026, 6, 14, 7, 30, 0, 0, loc)
		c := city(yesterday.UTC())
		// now is today 07:01 local
		now := time.Date(2026, 6, 15, 7, 1, 0, 0, loc)
		due, err := c.IsMorningDue(now.UTC())
		require.NoError(t, err)
		assert.True(t, due)
	})

	t.Run("unknown timezone returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{
			ID:         "wuc-bad-tz",
			Timezone:   "Galaxy/Nowhere",
			NotifyHour: 7,
		}
		_, err := c.IsMorningDue(time.Now().UTC())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Galaxy/Nowhere")
	})

	t.Run("empty timezone returns error", func(t *testing.T) {
		t.Parallel()
		c := &WeatherUserCity{
			ID:         "wuc-empty-tz",
			Timezone:   "",
			NotifyHour: 7,
		}
		_, err := c.IsMorningDue(time.Now().UTC())
		require.Error(t, err)
	})

	t.Run("fire exactly at fire time with no prior notification is due", func(t *testing.T) {
		t.Parallel()
		c := city(time.Time{})
		due, err := c.IsMorningDue(fireUTC)
		require.NoError(t, err)
		assert.True(t, due)
	})
}

func TestWeatherUserCity_EvaluateRain(t *testing.T) {
	t.Parallel()

	ptr := func(v int) *int { return &v }

	baseCity := &WeatherUserCity{
		ID:             "city-rain",
		NotifyKind:     WeatherNotifyAlertRain,
		ConditionValue: "70",
	}
	now := time.Date(2026, 6, 30, 7, 0, 0, 0, time.UTC)

	t.Run("fires when maxProb equals threshold within window", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(time.Hour), PrecipProb: ptr(70)},
			},
		}
		fired, reason, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "70%")
		assert.Contains(t, reason, "within 6h")
	})

	t.Run("fires when maxProb exceeds threshold", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(2 * time.Hour), PrecipProb: ptr(85)},
			},
		}
		fired, reason, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "85%")
	})

	t.Run("reports the highest probability in the window", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(time.Hour), PrecipProb: ptr(50)},
				{Time: now.Add(2 * time.Hour), PrecipProb: ptr(80)},
				{Time: now.Add(3 * time.Hour), PrecipProb: ptr(75)},
			},
		}
		fired, reason, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.True(t, fired)
		assert.Contains(t, reason, "80%")
	})

	t.Run("does not fire when maxProb below threshold", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(time.Hour), PrecipProb: ptr(65)},
			},
		}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("does not fire when all points are in the past", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(-time.Hour), PrecipProb: ptr(90)},
				{Time: now.Add(-2 * time.Hour), PrecipProb: ptr(90)},
			},
		}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("point at exactly now is included (inclusive lower bound)", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now, PrecipProb: ptr(90)},
			},
		}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.True(t, fired)
	})

	t.Run("point at exactly windowEnd is excluded (exclusive upper bound)", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(weatherRainWindow), PrecipProb: ptr(90)},
			},
		}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("points beyond window are excluded", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(7 * time.Hour), PrecipProb: ptr(90)},
			},
		}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("empty Hourly returns false without error", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{Hourly: []WeatherHourlyPoint{}}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("nil Hourly returns false without error", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{Hourly: nil}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("nil PrecipProb points are skipped", func(t *testing.T) {
		t.Parallel()
		obs := WeatherObservation{
			Hourly: []WeatherHourlyPoint{
				{Time: now.Add(time.Hour), PrecipProb: nil},
				{Time: now.Add(2 * time.Hour), PrecipProb: nil},
			},
		}
		fired, _, err := baseCity.EvaluateRain(obs, now)
		require.NoError(t, err)
		assert.False(t, fired)
	})

	t.Run("bad condition_value returns error", func(t *testing.T) {
		t.Parallel()
		city := &WeatherUserCity{
			NotifyKind:     WeatherNotifyAlertRain,
			ConditionValue: "not-a-number",
		}
		_, _, err := city.EvaluateRain(WeatherObservation{}, now)
		require.Error(t, err)
	})
}
