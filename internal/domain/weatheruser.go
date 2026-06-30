package domain

import (
	"fmt"
	"time"
)

// WeatherNotifyKind is a string enum identifying the delivery trigger for a weather subscription.
// Values are persisted to the database; do not rename existing constants without a data migration.
type WeatherNotifyKind string

const (
	// WeatherNotifyMorningSummary delivers a daily morning forecast summary
	// for the subscribed city, evaluated in the city's local timezone.
	WeatherNotifyMorningSummary WeatherNotifyKind = "morning_summary"
)

// WeatherUserCity records a user's per-city weather subscription.
// NotifyHour is the local-time hour (0–23) at which the daily summary fires, in Timezone.
// LastNotifiedAt is zero when no notification has ever been sent for this city.
// GismeteoCityID is nil until the curated gismeteo city map is consulted (second increment).
type WeatherUserCity struct {
	ID             string
	UserType       UserType
	UserID         string
	LocationID     string
	DisplayName    string
	Latitude       float64
	Longitude      float64
	Timezone       string // IANA timezone name, e.g. "Asia/Almaty"
	Country        string
	Admin1         string
	GismeteoCityID *int
	NotifyKind     WeatherNotifyKind
	NotifyHour     int // local 0–23
	LastNotifiedAt time.Time
	UpdatedAt      time.Time
	CreatedAt      time.Time
}

// IsMorningDue reports whether the daily morning summary should fire now,
// evaluated in the city's local timezone. now must be UTC. It fires once per
// local calendar day at NotifyHour. Returns an error if the stored timezone
// is not loadable.
func (c *WeatherUserCity) IsMorningDue(now time.Time) (bool, error) {
	if c.Timezone == "" {
		return false, fmt.Errorf("weather city %s: timezone is empty", c.ID)
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return false, fmt.Errorf("weather city %s: load timezone %q: %w", c.ID, c.Timezone, err)
	}
	local := now.In(loc)
	fire := time.Date(local.Year(), local.Month(), local.Day(), c.NotifyHour, 0, 0, 0, loc)
	if local.Before(fire) {
		return false, nil
	}
	if c.LastNotifiedAt.IsZero() {
		return true, nil
	}
	return c.LastNotifiedAt.In(loc).Before(fire), nil
}
