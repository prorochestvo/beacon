package api

import "time"

// NotificationResponse is the JSON representation of a notification pool record.
// The message body is intentionally omitted to avoid leaking rate content through the API.
// UserID is omitted when empty to prevent leaking subscriber identifiers via endpoints that
// do not require it (e.g. failed-events per source).
type NotificationResponse struct {
	ID        string    `json:"id"`
	UserType  string    `json:"user_type"`
	UserID    string    `json:"user_id,omitempty"`
	Status    string    `json:"status"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	SentAt    time.Time `json:"sent_at"`
}
