package domain

import "time"

// RateUserEvent represents a single outbound notification stored in the pool.
// Persisted before delivery; retains LastError and Status for audit and retry.
type RateUserEvent struct {
	ID         string
	SourceName string // name of the rate source that triggered the event
	UserType   UserType
	UserID     string
	Message    string
	Status     RateUserEventStatus
	LastError  string // empty when no error
	SentAt     time.Time
	CreatedAt  time.Time
}

// RateUserEventDailySummary holds per-(user_type, date) aggregated event counts for a source.
type RateUserEventDailySummary struct {
	UserType     string
	Date         string // YYYY-MM-DD
	SuccessCount int64
	FailedCount  int64
}

// RateUserEventStatus represents the delivery state of a notification in the pool.
type RateUserEventStatus string

const (
	// RateUserEventStatusPending indicates the notification has not yet been attempted.
	RateUserEventStatusPending RateUserEventStatus = "pending"
	// RateUserEventStatusSent indicates the notification was delivered successfully.
	RateUserEventStatusSent RateUserEventStatus = "sent"
	// RateUserEventStatusFailed indicates the last delivery attempt returned an error.
	RateUserEventStatusFailed RateUserEventStatus = "failed"
	// RateUserEventStatusCanceled indicates the notification was abandoned without delivery.
	RateUserEventStatusCanceled RateUserEventStatus = "canceled"
)
