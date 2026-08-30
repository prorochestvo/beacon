package labelfmt

import (
	"math"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestGroupThousands(t *testing.T) {
	t.Parallel()

	t.Run("sub-thousand — no separator", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "471.95", GroupThousands(471.95))
	})
	t.Run("exactly one separator", func(t *testing.T) {
		t.Parallel()
		// Verify the separator is ASCII 0x20, not a non-breaking or thin space.
		require.Equal(t, "68 382.56", GroupThousands(68382.56))
	})
	t.Run("multiple separators", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "1 234 567.89", GroupThousands(1234567.89))
	})
	t.Run("zero", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "0.00", GroupThousands(0))
	})
	t.Run("negative with separator", func(t *testing.T) {
		t.Parallel()
		// Sign must be U+2212, not ASCII hyphen-minus.
		got := GroupThousands(-68382.56)
		require.Equal(t, "−68 382.56", got)
	})
	t.Run("rounding bumps integer part across thousands boundary", func(t *testing.T) {
		t.Parallel()
		// 999.999 rounds to 1000.00 before grouping; must become "1 000.00".
		require.Equal(t, "1 000.00", GroupThousands(999.999))
	})
	t.Run("exactly one thousand", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "1 138.09", GroupThousands(1138.09))
	})
	t.Run("NaN returns sentinel", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "?.??", GroupThousands(math.NaN()))
	})
	t.Run("+Inf returns sentinel", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "?.??", GroupThousands(math.Inf(1)))
	})
	t.Run("-Inf returns sentinel", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "?.??", GroupThousands(math.Inf(-1)))
	})
}

func TestSubscriptionConditionLabel(t *testing.T) {
	t.Parallel()

	t.Run("delta condition", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeDelta, ConditionValue: "10"}
		require.Equal(t, "Δ ≥ 10%", SubscriptionConditionLabel(s))
	})
	t.Run("delta condition with fractional value", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeDelta, ConditionValue: "0.5"}
		require.Equal(t, "Δ ≥ 0.5%", SubscriptionConditionLabel(s))
	})
	t.Run("interval condition 24h maps to 1d", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeInterval, ConditionValue: "24h"}
		require.Equal(t, "every 1d", SubscriptionConditionLabel(s))
	})
	t.Run("interval condition 168h maps to 1w", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeInterval, ConditionValue: "168h"}
		require.Equal(t, "every 1w", SubscriptionConditionLabel(s))
	})
	t.Run("interval condition passthrough", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeInterval, ConditionValue: "4h"}
		require.Equal(t, "every 4h", SubscriptionConditionLabel(s))
	})
	t.Run("daily condition with full HH:MM:SS", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeDaily, ConditionValue: "06:00:00"}
		require.Equal(t, "daily at 06:00 UTC", SubscriptionConditionLabel(s))
	})
	t.Run("daily condition short value", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeDaily, ConditionValue: "06"}
		require.Equal(t, "daily at 06 UTC", SubscriptionConditionLabel(s))
	})
	t.Run("cron condition valid expression", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: domain.ConditionTypeCron, ConditionValue: "0 9 * * 1"}
		require.Equal(t, "weekly on Monday (UTC 09:00)", SubscriptionConditionLabel(s))
	})
	t.Run("unknown condition type returns raw type", func(t *testing.T) {
		t.Parallel()
		s := domain.RateUserSubscription{ConditionType: "unknown"}
		require.Equal(t, "unknown", SubscriptionConditionLabel(s))
	})
}

func TestIntervalLabel(t *testing.T) {
	t.Parallel()

	t.Run("24h maps to 1d", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "1d", IntervalLabel("24h"))
	})
	t.Run("168h maps to 1w", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "1w", IntervalLabel("168h"))
	})
	t.Run("passthrough for other values", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "4h", IntervalLabel("4h"))
	})
}

func TestCronWeekdayLabel(t *testing.T) {
	t.Parallel()

	t.Run("valid cron expression returns weekday name", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "Monday", CronWeekdayLabel("0 9 * * 1"))
		require.Equal(t, "Sunday", CronWeekdayLabel("0 9 * * 0"))
		require.Equal(t, "Saturday", CronWeekdayLabel("0 9 * * 6"))
	})
	t.Run("malformed expression returns raw expression", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "not-a-cron", CronWeekdayLabel("not-a-cron"))
	})
	t.Run("valid syntax but unknown weekday digit returns raw", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "0 9 * * 7", CronWeekdayLabel("0 9 * * 7"))
	})
}
