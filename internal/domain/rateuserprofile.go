package domain

import "time"

// RateUserProfile carries user-scoped preferences not specific to any single
// subscription.
//
// Timezone is an IANA name resolvable by time.LoadLocation (e.g. "Asia/Almaty").
// Validated in the repository on write; readers should still fall back
// gracefully if a later Go version drops a previously-known zone.
//
// Locale is a BCP-47 tag (e.g. "ru-RU"), stored as-is from the client with no
// server-side validation; empty when the client didn't provide one. By policy
// this is the only identity-adjacent field we keep besides chat_id —
// username/display-name fields are off-limits; see the no-PII feedback memory.
type RateUserProfile struct {
	UserType  UserType
	UserID    string
	Timezone  string
	Locale    string
	UpdatedAt time.Time
	CreatedAt time.Time
}
