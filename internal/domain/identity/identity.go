// Package identity mints unique, human-readable identifiers for domain entities.
// Every identifier has the format:
//
//	<prefix>YYYYMMDDhhmmssZ<nanos-9-digits>T<UUIDv4-hex-uppercase>
//
// where the prefix is determined by the Kind constant passed to New.
// The on-disk contract (prefix strings, field order, field widths, case) is frozen.
package identity

import (
	"fmt"
	"time"

	"github.com/twinj/uuid"
)

// Kind is a closed enum identifying the entity type an ID was minted for.
// Each Kind's string value doubles as the on-disk prefix and must not change
// without a data migration.
type Kind string

// The string value of each constant below is the on-disk ID prefix and is frozen.
// Changing it requires a data migration on every table that stores these IDs.
const (
	// KindRateSource is the Kind for rate_sources entities.
	KindRateSource Kind = "RS"
	// KindRateValue is the Kind for rate_values entities.
	KindRateValue Kind = "RV"
	// KindRateUserEvent is the Kind for rate_user_events entities.
	KindRateUserEvent Kind = "RUE"
	// KindRateUserSubscription is the Kind for rate_user_subscriptions entities.
	KindRateUserSubscription Kind = "RUS"
	// KindExecutionHistory is the Kind for execution_history entities.
	KindExecutionHistory Kind = "H"
	// KindWeatherUserCity is the Kind for weather_user_cities entities.
	KindWeatherUserCity Kind = "WUC"
	// KindWeatherObservation is the Kind for weather_observations entities.
	KindWeatherObservation Kind = "WOB"
	// KindWeatherForecastDay is the Kind for weather_forecast_days entities.
	KindWeatherForecastDay Kind = "WFD"
)

// New returns a new unique string identifier for the given Kind, in the format:
//
//	<prefix>YYYYMMDDhhmmssZ<nanos-9-digits>T<UUIDv4-hex-uppercase>
//
// Time is time.Now().UTC() at the moment of the call. The UUID component is
// UUIDv4 from github.com/twinj/uuid, uppercase hex with no separators.
// Callers that need the prefix alone can cast the Kind to string.
func New(k Kind) string {
	return format(k, time.Now().UTC())
}

// format renders the identifier for kind k minted at instant at.
//
// Every field is fixed-width, the nanosecond one included, because identifiers
// are stored and compared as text: rows sharing a second-resolution timestamp
// are tie-broken with ORDER BY id DESC, and only constant widths make that byte
// order agree with chronological order. A ragged nanosecond field sorts an
// identifier minted at .000000009 above one minted at .123456789 — "Z9" beats
// "Z1" — so a latest-row query returns the older row (issue #56).
func format(k Kind, at time.Time) string {
	return fmt.Sprintf("%s%04d%02d%02d%02d%02d%02dZ%09dT%X",
		k,
		at.Year(), at.Month(), at.Day(),
		at.Hour(), at.Minute(), at.Second(),
		at.Nanosecond(),
		uuid.NewV4().Bytes(),
	)
}
