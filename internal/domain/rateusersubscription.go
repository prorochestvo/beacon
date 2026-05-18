package domain

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"
)

// RateUserSubscription represents a user's subscription to a monitored rate source.
type RateUserSubscription struct {
	ID                 string                    `json:"id"`
	UserType           UserType                  `json:"user_type"`
	UserID             string                    `json:"user_id"`
	SourceName         string                    `json:"source_name"`
	ConditionType      SubscriptionConditionType `json:"condition_type"`
	ConditionValue     string                    `json:"condition_value"`
	LatestNotifiedRate float64                   `json:"latest_notified_rate"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	CreatedAt          time.Time                 `json:"created_at"`
}

// Validate returns a non-nil error if the subscription is misconfigured.
func (rus *RateUserSubscription) Validate() error {
	switch rus.ConditionType {
	case ConditionTypeDaily:
		_, err := rus.DailyTime()
		return err
	case ConditionTypeDelta:
		_, err := rus.DeltaThreshold()
		return err
	case ConditionTypeInterval:
		_, err := rus.IntervalDuration()
		return err
	case ConditionTypeCron:
		_, err := parseCronSchedule(rus.ConditionValue)
		return err
	default:
		return fmt.Errorf("unknown condition type: %q", rus.ConditionType)
	}
}

// DailyTime parses ConditionValue as a time-of-day (HH:MM:SS).
// Returns an error if ConditionType is not ConditionTypeDaily.
func (rus *RateUserSubscription) DailyTime() (time.Time, error) {
	if rus.ConditionType != ConditionTypeDaily {
		return time.Time{}, fmt.Errorf("invalid condition type: %s", rus.ConditionType)
	}
	return time.Parse(time.TimeOnly, rus.ConditionValue)
}

// DeltaThreshold parses ConditionValue as a non-negative float64 delta threshold.
// Returns an error if ConditionType is not ConditionTypeDelta or the value is negative.
func (rus *RateUserSubscription) DeltaThreshold() (float64, error) {
	if rus.ConditionType != ConditionTypeDelta {
		return 0, fmt.Errorf("invalid condition type: %s", rus.ConditionType)
	}
	d, err := strconv.ParseFloat(rus.ConditionValue, 10)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid interval: %s", rus.ConditionValue)
	}
	return d, nil
}

// IntervalDuration parses ConditionValue as a Go duration string (minimum 1 minute).
// Returns an error if ConditionType is not ConditionTypeInterval or the duration is below the minimum.
func (rus *RateUserSubscription) IntervalDuration() (time.Duration, error) {
	if rus.ConditionType != ConditionTypeInterval {
		return 0, fmt.Errorf("invalid condition type: %s", rus.ConditionType)
	}
	d, err := time.ParseDuration(rus.ConditionValue)
	if err != nil {
		return 0, err
	}
	if d < time.Minute {
		return 0, fmt.Errorf("invalid interval: %s", rus.ConditionValue)
	}
	return d, nil
}

// parseCronSchedule is an unexported helper shared by IsCronDue and Validate.
func parseCronSchedule(expr string) (cron.Schedule, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	return parser.Parse(expr)
}

// IsCronDue reports whether the cron schedule has fired at least once since the
// last notification (rus.UpdatedAt). now must be UTC.
func (rus *RateUserSubscription) IsCronDue(now time.Time) (bool, error) {
	if rus.ConditionType != ConditionTypeCron {
		return false, fmt.Errorf("invalid condition type: %s", rus.ConditionType)
	}
	schedule, err := parseCronSchedule(rus.ConditionValue)
	if err != nil {
		return false, err
	}
	return !schedule.Next(rus.UpdatedAt).After(now), nil
}

// IsIntervalDue reports whether enough time has elapsed since the last notification.
// now must be UTC.
func (rus *RateUserSubscription) IsIntervalDue(now time.Time) (bool, error) {
	if rus.ConditionType != ConditionTypeInterval {
		return false, fmt.Errorf("invalid condition type: %s", rus.ConditionType)
	}
	interval, err := rus.IntervalDuration()
	if err != nil {
		return false, err
	}
	if rus.UpdatedAt.IsZero() {
		return true, nil
	}
	return now.Sub(rus.UpdatedAt) >= interval, nil
}

// IsDailyDue reports whether the daily notification time has passed today and the
// subscription has not yet been notified today. now must be UTC.
func (rus *RateUserSubscription) IsDailyDue(now time.Time) (bool, error) {
	if rus.ConditionType != ConditionTypeDaily {
		return false, fmt.Errorf("invalid condition type: %s", rus.ConditionType)
	}
	t, err := rus.DailyTime()
	if err != nil {
		return false, err
	}
	year, month, day := now.Date()
	todayFire := time.Date(year, month, day, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)

	if now.Before(todayFire) {
		return false, nil
	}
	if rus.UpdatedAt.IsZero() {
		return true, nil
	}
	return rus.UpdatedAt.Before(todayFire), nil
}

// IsDue reports whether the subscription condition is satisfied and a notification
// should be sent. now must be UTC. delta is the signed price change since the last
// notification; it is only used for ConditionTypeDelta.
func (rus *RateUserSubscription) IsDue(now time.Time, delta float64) (bool, error) {
	if rus == nil {
		return false, fmt.Errorf("subscription is nil")
	}
	switch rus.ConditionType {
	case ConditionTypeDelta:
		return rus.IsDeltaSatisfied(delta)
	case ConditionTypeInterval:
		return rus.IsIntervalDue(now)
	case ConditionTypeDaily:
		return rus.IsDailyDue(now)
	case ConditionTypeCron:
		return rus.IsCronDue(now)
	default:
		return false, fmt.Errorf("unknown condition type: %q", rus.ConditionType)
	}
}

// IsDeltaSatisfied reports whether the absolute rate change meets the threshold.
// On the first run (LatestNotifiedRate <= 0) the method fires unconditionally so the
// user receives an initial baseline reading. On subsequent runs the absolute delta
// must reach or exceed the configured threshold.
func (rus *RateUserSubscription) IsDeltaSatisfied(delta float64) (bool, error) {
	if rus.ConditionType != ConditionTypeDelta {
		return false, fmt.Errorf("invalid condition type: %s", rus.ConditionType)
	}
	// First run: no prior notification has ever been sent for this subscription.
	// Fire unconditionally so the user receives an initial baseline reading.
	if rus.LatestNotifiedRate <= 0 {
		return true, nil
	}
	threshold, err := rus.DeltaThreshold()
	if err != nil {
		return false, err
	}
	return math.Abs(delta) >= threshold, nil
}

// RateUserSubscriptionDetail holds the detail of a single subscription for the UI list.
type RateUserSubscriptionDetail struct {
	ID               string
	UserType         UserType
	SourceName       string
	ConditionType    string
	ConditionValue   string
	LatestNotifiedAt time.Time // zero if never notified
}

// RateUserSubscriptionSummary holds aggregated per-(source, user_type) notification statistics.
type RateUserSubscriptionSummary struct {
	SourceName        string
	UserType          UserType
	SubscriptionCount int64
	LastSentAt        time.Time // zero if no events have been sent
	SuccessCount      int64
	FailedCount       int64
}

// SubscriptionConditionType identifies the trigger rule for a user subscription.
type SubscriptionConditionType string

const (
	// ConditionTypeDelta triggers when the absolute rate change meets a threshold.
	ConditionTypeDelta SubscriptionConditionType = "delta"
	// ConditionTypeInterval triggers when a fixed duration has elapsed since the last notification.
	ConditionTypeInterval SubscriptionConditionType = "interval"
	// ConditionTypeDaily triggers once per day at a configured time-of-day.
	ConditionTypeDaily SubscriptionConditionType = "daily"
	// ConditionTypeCron triggers according to a standard cron schedule expression.
	ConditionTypeCron SubscriptionConditionType = "cron"
)
