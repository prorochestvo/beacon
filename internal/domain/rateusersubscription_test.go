package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateUserSubscription_DailyTime(t *testing.T) {
	t.Parallel()

	t.Run("valid condition type returns parsed time", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "08:00:00"}
		got, err := rus.DailyTime()
		require.NoError(t, err)
		require.Equal(t, 8, got.Hour())
	})
	t.Run("wrong condition type returns error", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "08:00:00"}
		_, err := rus.DailyTime()
		require.Error(t, err)
	})
}

func TestRateUserSubscription_DeltaThreshold(t *testing.T) {
	t.Parallel()

	t.Run("valid positive threshold returns value", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "5.5"}
		got, err := rus.DeltaThreshold()
		require.NoError(t, err)
		require.InDelta(t, 5.5, got, 0.001)
	})
	t.Run("wrong condition type returns error", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "5"}
		_, err := rus.DeltaThreshold()
		require.Error(t, err)
	})
	t.Run("non-numeric ConditionValue returns error", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "not-a-number"}
		_, err := rus.DeltaThreshold()
		require.Error(t, err)
	})
	t.Run("negative threshold returns error", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "-5"}
		_, err := rus.DeltaThreshold()
		require.Error(t, err)
	})
}

func TestRateUserSubscription_IntervalDuration(t *testing.T) {
	t.Parallel()

	t.Run("valid interval returns duration", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "1h"}
		got, err := rus.IntervalDuration()
		require.NoError(t, err)
		require.Equal(t, time.Hour, got)
	})
	t.Run("wrong condition type returns error", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "1h"}
		_, err := rus.IntervalDuration()
		require.Error(t, err)
	})
}

func TestRateUserSubscription_IsDeltaSatisfied(t *testing.T) {
	t.Parallel()

	t.Run("first run LatestNotifiedRate zero always fires", func(t *testing.T) {
		t.Parallel()
		// Regression: github.com/seilbekskindirov/beacon — notifications sent every tick
		rus := &RateUserSubscription{
			ConditionType:      ConditionTypeDelta,
			ConditionValue:     "5",
			LatestNotifiedRate: 0, // never notified
		}
		ok, err := rus.IsDeltaSatisfied(0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("first run LatestNotifiedRate negative always fires", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:      ConditionTypeDelta,
			ConditionValue:     "5",
			LatestNotifiedRate: -1,
		}
		ok, err := rus.IsDeltaSatisfied(0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("rate unchanged after first notification does not fire", func(t *testing.T) {
		t.Parallel()
		// Regression: github.com/seilbekskindirov/beacon — notifications sent every tick
		// delta == 0 but LatestNotifiedRate > 0 means rate has not changed → must NOT fire
		rus := &RateUserSubscription{
			ConditionType:      ConditionTypeDelta,
			ConditionValue:     "5",
			LatestNotifiedRate: 470.0,
		}
		ok, err := rus.IsDeltaSatisfied(0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("zero threshold unchanged price does not re-fire", func(t *testing.T) {
		t.Parallel()
		// threshold=0 means "any change"; delta=0 (price unchanged) must not fire
		rus := &RateUserSubscription{
			ConditionType:      ConditionTypeDelta,
			ConditionValue:     "0",
			LatestNotifiedRate: 300.0,
		}
		ok, err := rus.IsDeltaSatisfied(0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("zero threshold any nonzero delta fires in either direction", func(t *testing.T) {
		t.Parallel()
		// threshold=0 means "any change"; the branch uses math.Abs(delta), so it must
		// fire on both a rise and a fall, not just a positive delta.
		rus := &RateUserSubscription{
			ConditionType:      ConditionTypeDelta,
			ConditionValue:     "0",
			LatestNotifiedRate: 300.0,
		}
		up, err := rus.IsDeltaSatisfied(0.01)
		require.NoError(t, err)
		require.True(t, up, "positive delta must fire with zero threshold")
		down, err := rus.IsDeltaSatisfied(-0.01)
		require.NoError(t, err)
		require.True(t, down, "negative delta must fire with zero threshold (symmetry via math.Abs)")
	})
	t.Run("delta above threshold", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "5"}
		ok, err := rus.IsDeltaSatisfied(6)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("delta equals threshold", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "5"}
		ok, err := rus.IsDeltaSatisfied(5)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("delta below threshold", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "5", LatestNotifiedRate: 100}
		ok, err := rus.IsDeltaSatisfied(3)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("negative delta above threshold", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "5", LatestNotifiedRate: 100}
		ok, err := rus.IsDeltaSatisfied(-6)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("wrong condition type", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "5"}
		ok, err := rus.IsDeltaSatisfied(10)
		require.Error(t, err)
		require.False(t, ok)
	})
	t.Run("non-parseable ConditionValue propagates DeltaThreshold error", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "not-a-number", LatestNotifiedRate: 100}
		ok, err := rus.IsDeltaSatisfied(10)
		require.Error(t, err)
		require.False(t, ok)
	})
}

func TestRateUserSubscription_IsIntervalDue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)

	t.Run("never notified zero UpdatedAt", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "1h"}
		ok, err := rus.IsIntervalDue(now)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("elapsed greater than interval", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeInterval,
			ConditionValue: "1h",
			UpdatedAt:      now.Add(-2 * time.Hour),
		}
		ok, err := rus.IsIntervalDue(now)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("elapsed equals interval exactly", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeInterval,
			ConditionValue: "1h",
			UpdatedAt:      now.Add(-time.Hour),
		}
		ok, err := rus.IsIntervalDue(now)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("elapsed less than interval", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeInterval,
			ConditionValue: "1h",
			UpdatedAt:      now.Add(-30 * time.Minute),
		}
		ok, err := rus.IsIntervalDue(now)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("invalid ConditionValue", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "bad"}
		ok, err := rus.IsIntervalDue(now)
		require.Error(t, err)
		require.False(t, ok)
	})
	t.Run("wrong condition type", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "1h"}
		ok, err := rus.IsIntervalDue(now)
		require.Error(t, err)
		require.False(t, ok)
	})
}

func TestRateUserSubscription_IsDailyDue(t *testing.T) {
	t.Parallel()

	// now = 2026-04-12 09:00:00 UTC; todayFire for "08:00:00" = 2026-04-12 08:00:00 UTC
	now := time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)

	t.Run("daily time passed, never notified", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "08:00:00"}
		ok, err := rus.IsDailyDue(now)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("daily time passed, not notified today", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeDaily,
			ConditionValue: "08:00:00",
			UpdatedAt:      time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC), // yesterday
		}
		ok, err := rus.IsDailyDue(now)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("daily time passed, already notified today", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeDaily,
			ConditionValue: "08:00:00",
			UpdatedAt:      time.Date(2026, 4, 12, 8, 30, 0, 0, time.UTC), // 08:30 today — after todayFire
		}
		ok, err := rus.IsDailyDue(now)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("daily time not yet reached", func(t *testing.T) {
		t.Parallel()
		// now = 07:00, daily fire = 08:00 → not yet reached
		early := time.Date(2026, 4, 12, 7, 0, 0, 0, time.UTC)
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "08:00:00"}
		ok, err := rus.IsDailyDue(early)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("invalid ConditionValue", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "bad"}
		ok, err := rus.IsDailyDue(now)
		require.Error(t, err)
		require.False(t, ok)
	})
	t.Run("wrong condition type", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "08:00:00"}
		ok, err := rus.IsDailyDue(now)
		require.Error(t, err)
		require.False(t, ok)
	})
}

func TestRateUserSubscription_IsCronDue(t *testing.T) {
	t.Parallel()

	// now = 2026-04-12 09:35:00 UTC
	// cron "0 * * * *" fires on the hour: last fire was 09:00, next is 10:00
	now := time.Date(2026, 4, 12, 9, 35, 0, 0, time.UTC)

	t.Run("never notified cron trivially fired", func(t *testing.T) {
		t.Parallel()
		// zero UpdatedAt → schedule.Next(time.Time{}) is in year 0001 → always ≤ now
		rus := &RateUserSubscription{ConditionType: ConditionTypeCron, ConditionValue: "* * * * *"}
		ok, err := rus.IsCronDue(now)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("cron fired since last notify", func(t *testing.T) {
		t.Parallel()
		// UpdatedAt = 08:34 → schedule.Next(08:34) = 09:00 → 09:00 ≤ 09:35 → true
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeCron,
			ConditionValue: "0 * * * *",
			UpdatedAt:      now.Add(-61 * time.Minute), // 08:34
		}
		ok, err := rus.IsCronDue(now)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("cron not yet fired", func(t *testing.T) {
		t.Parallel()
		// UpdatedAt = 09:05 → schedule.Next(09:05) = 10:00 → 10:00 > 09:35 → false
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeCron,
			ConditionValue: "0 * * * *",
			UpdatedAt:      now.Add(-30 * time.Minute), // 09:05
		}
		ok, err := rus.IsCronDue(now)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("invalid cron expression", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeCron, ConditionValue: "bad cron"}
		ok, err := rus.IsCronDue(now)
		require.Error(t, err)
		require.False(t, ok)
	})
	t.Run("wrong condition type", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "* * * * *"}
		ok, err := rus.IsCronDue(now)
		require.Error(t, err)
		require.False(t, ok)
	})
}

func TestRateUserSubscription_IsDue(t *testing.T) {
	t.Parallel()

	// Fixed anchor: Sunday 2026-04-12 09:35:00 UTC
	now := time.Date(2026, 4, 12, 9, 35, 0, 0, time.UTC)

	t.Run("nil receiver returns error", func(t *testing.T) {
		t.Parallel()
		var rus *RateUserSubscription
		ok, err := rus.IsDue(now, 0)
		require.Error(t, err)
		require.False(t, ok)
	})
	t.Run("delta above threshold", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "5", LatestNotifiedRate: 100}
		ok, err := rus.IsDue(now, 10)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("delta below threshold", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "5", LatestNotifiedRate: 100}
		ok, err := rus.IsDue(now, 3)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("delta zero first run fires", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:      ConditionTypeDelta,
			ConditionValue:     "5",
			LatestNotifiedRate: 0,
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("delta zero rate unchanged after first notification does not fire", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{
			ConditionType:      ConditionTypeDelta,
			ConditionValue:     "5",
			LatestNotifiedRate: 470.0,
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("interval never notified", func(t *testing.T) {
		t.Parallel()
		// zero UpdatedAt → IsIntervalDue returns true immediately
		rus := &RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "1h"}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("interval elapsed greater than interval", func(t *testing.T) {
		t.Parallel()
		// UpdatedAt = 07:35 → elapsed = 2h > 1h → true
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeInterval,
			ConditionValue: "1h",
			UpdatedAt:      now.Add(-2 * time.Hour),
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("interval elapsed equals interval exactly", func(t *testing.T) {
		t.Parallel()
		// UpdatedAt = 08:35 → elapsed = 1h == 1h → true
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeInterval,
			ConditionValue: "1h",
			UpdatedAt:      now.Add(-time.Hour),
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("interval elapsed less than interval", func(t *testing.T) {
		t.Parallel()
		// UpdatedAt = 09:05 → elapsed = 30m < 1h → false
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeInterval,
			ConditionValue: "1h",
			UpdatedAt:      now.Add(-30 * time.Minute),
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("daily never notified, time passed", func(t *testing.T) {
		t.Parallel()
		// fire = 08:00, now = 09:35, UpdatedAt = zero → never notified → true
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "08:00:00"}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("daily already notified today", func(t *testing.T) {
		t.Parallel()
		// fire = 08:00, UpdatedAt = 08:30 (after todayFire) → already notified today → false
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeDaily,
			ConditionValue: "08:00:00",
			UpdatedAt:      time.Date(2026, 4, 12, 8, 30, 0, 0, time.UTC),
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("daily not yet reached today", func(t *testing.T) {
		t.Parallel()
		// fire = 10:00, now = 09:35 → not yet reached → false
		rus := &RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "10:00:00"}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("daily notified yesterday", func(t *testing.T) {
		t.Parallel()
		// fire = 08:00, UpdatedAt = yesterday at 08:30 → before todayFire → true
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeDaily,
			ConditionValue: "08:00:00",
			UpdatedAt:      time.Date(2026, 4, 11, 8, 30, 0, 0, time.UTC),
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("cron never notified (zero UpdatedAt)", func(t *testing.T) {
		t.Parallel()
		// zero UpdatedAt → schedule.Next(time.Time{}) is in year 0001 → always ≤ now → true
		rus := &RateUserSubscription{ConditionType: ConditionTypeCron, ConditionValue: "* * * * *"}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("cron fired since last notify", func(t *testing.T) {
		t.Parallel()
		// "0 * * * *" fires on the hour.
		// UpdatedAt = 08:34 (now - 61m) → schedule.Next(08:34) = 09:00 → 09:00 ≤ 09:35 → true
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeCron,
			ConditionValue: "0 * * * *",
			UpdatedAt:      now.Add(-61 * time.Minute), // 08:34
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("cron not yet fired", func(t *testing.T) {
		t.Parallel()
		// "0 * * * *" fires on the hour.
		// UpdatedAt = 09:05 (now - 30m) → schedule.Next(09:05) = 10:00 → 10:00 > 09:35 → false
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeCron,
			ConditionValue: "0 * * * *",
			UpdatedAt:      now.Add(-30 * time.Minute), // 09:05
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("cron fires exactly on the minute boundary", func(t *testing.T) {
		t.Parallel()
		// "35 9 * * *" fires at 09:35 daily.
		// UpdatedAt = 09:34 (now - 1m) → schedule.Next(09:34) = 09:35 == now → !After(now) → true
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeCron,
			ConditionValue: "35 9 * * *",
			UpdatedAt:      now.Add(-time.Minute), // 09:34
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("cron weekly, fired last week", func(t *testing.T) {
		t.Parallel()
		// "0 9 * * 1" fires every Monday at 09:00.
		// now = Sunday 2026-04-12 09:35; last Monday = 2026-04-06.
		// UpdatedAt = 2026-03-30 09:00 (two Mondays ago)
		// → schedule.Next(2026-03-30 09:00) = 2026-04-06 09:00 → 2026-04-06 09:00 ≤ now → true
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeCron,
			ConditionValue: "0 9 * * 1",
			UpdatedAt:      time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC),
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("cron weekly, not yet fired this week", func(t *testing.T) {
		t.Parallel()
		// "0 9 * * 1" fires every Monday at 09:00.
		// now = Sunday 2026-04-12 09:35; UpdatedAt = today 08:00 (Sunday, before 09:00)
		// → schedule.Next(2026-04-12 08:00) = 2026-04-13 09:00 (next Monday) > now → false
		rus := &RateUserSubscription{
			ConditionType:  ConditionTypeCron,
			ConditionValue: "0 9 * * 1",
			UpdatedAt:      time.Date(2026, 4, 12, 8, 0, 0, 0, time.UTC),
		}
		ok, err := rus.IsDue(now, 0)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("cron invalid expression", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: ConditionTypeCron, ConditionValue: "bad cron"}
		ok, err := rus.IsDue(now, 0)
		require.Error(t, err)
		require.False(t, ok)
	})
	t.Run("unknown condition type returns error", func(t *testing.T) {
		t.Parallel()
		rus := &RateUserSubscription{ConditionType: "bogus", ConditionValue: "x"}
		ok, err := rus.IsDue(now, 0)
		require.Error(t, err)
		require.False(t, ok)
	})
}

func TestRateUserSubscriptionValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		sub       RateUserSubscription
		wantError bool
	}{
		{
			name:      "delta valid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeDelta, ConditionValue: "10"},
			wantError: false,
		},
		{
			name:      "daily_8am valid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "08:00:00"},
			wantError: false,
		},
		{
			name:      "daily_6pm valid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeDaily, ConditionValue: "18:00:00"},
			wantError: false,
		},
		{
			name:      "interval 1m valid (minimum boundary)",
			sub:       RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: time.Minute.String()},
			wantError: false,
		},
		{
			name:      "interval 6h valid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: (6 * time.Hour).String()},
			wantError: false,
		},
		{
			name:      "interval 24h valid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: (24 * time.Hour).String()},
			wantError: false,
		},
		{
			name:      "interval zero invalid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: "0"},
			wantError: true,
		},
		{
			name:      "interval 30s invalid (below minimum)",
			sub:       RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: (30 * time.Second).String()},
			wantError: true,
		},
		{
			name:      "interval negative invalid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeInterval, ConditionValue: (-5 * time.Minute).String()},
			wantError: true,
		},
		{
			name:      "unknown condition type invalid",
			sub:       RateUserSubscription{ConditionType: "bogus"},
			wantError: true,
		},
		{
			name:      "empty condition type invalid",
			sub:       RateUserSubscription{ConditionType: ""},
			wantError: true,
		},
		{
			name:      "cron condition every 10 minutes invalid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeCron, ConditionValue: "*/10 * * * *"},
			wantError: false,
		},
		{
			name:      "cron condition every day at midnight invalid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeCron, ConditionValue: "0 0 * * *"},
			wantError: false,
		},
		{
			name:      "cron condition every Monday at 9am invalid",
			sub:       RateUserSubscription{ConditionType: ConditionTypeCron, ConditionValue: "0 9 * * 1"},
			wantError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.sub.Validate()
			if tc.wantError {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
