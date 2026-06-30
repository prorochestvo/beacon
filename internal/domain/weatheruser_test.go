package domain

import (
	"testing"
	"time"
	_ "time/tzdata" // embedded IANA tzdata so LoadLocation works without system tzdata

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
