// Package labelfmt provides human-readable label formatting for subscription
// conditions and notification triggers used across the Telegram bot UI.
package labelfmt

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/monitor/internal/domain"
)

// SubscriptionConditionLabel returns a human-readable description of a subscription's
// condition, formatted for the Telegram inline-keyboard UI.
func SubscriptionConditionLabel(s domain.RateUserSubscription) string {
	switch s.ConditionType {
	case domain.ConditionTypeDelta:
		return fmt.Sprintf("Δ ≥ %s%%", s.ConditionValue)
	case domain.ConditionTypeInterval:
		return fmt.Sprintf("every %s", IntervalLabel(s.ConditionValue))
	case domain.ConditionTypeDaily:
		if len(s.ConditionValue) >= 5 {
			return fmt.Sprintf("daily at %s UTC", s.ConditionValue[:5])
		}
		return fmt.Sprintf("daily at %s UTC", s.ConditionValue)
	case domain.ConditionTypeCron:
		return fmt.Sprintf("weekly on %s (UTC 09:00)", CronWeekdayLabel(s.ConditionValue))
	default:
		return string(s.ConditionType)
	}
}

// IntervalLabel maps raw duration strings to short human-readable labels.
func IntervalLabel(v string) string {
	switch v {
	case "24h":
		return "1d"
	case "168h":
		return "1w"
	default:
		return v
	}
}

// CronWeekdayLabel extracts the weekday name from a "0 9 * * N" cron expression.
// Returns the raw expression if it cannot be parsed.
func CronWeekdayLabel(expr string) string {
	days := map[string]string{
		"0": "Sunday", "1": "Monday", "2": "Tuesday",
		"3": "Wednesday", "4": "Thursday", "5": "Friday", "6": "Saturday",
	}
	parts := strings.Fields(expr)
	if len(parts) == 5 {
		if name, ok := days[parts[4]]; ok {
			return name
		}
	}
	return expr
}
