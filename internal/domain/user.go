package domain

// UserType identifies the notification channel for a subscriber.
type UserType string

const (
	// UserTypeTelegram identifies a subscriber reached via the Telegram bot.
	UserTypeTelegram UserType = "telegrambot"
)

// User identifies a single subscriber by channel type and platform-specific identifier.
type User struct {
	UserType UserType
	UserID   string
}
