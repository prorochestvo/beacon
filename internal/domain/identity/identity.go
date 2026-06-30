// Package identity mints unique, human-readable identifiers for domain entities.
// Every identifier has the format:
//
//	<prefix>YYYYMMDDhhmmssZ<nanos>T<UUIDv4-hex-uppercase>
//
// where the prefix is determined by the Kind constant passed to New.
// The on-disk contract (prefix strings, field order, case) is frozen.
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
)

// New returns a new unique string identifier for the given Kind, in the format:
//
//	<prefix>YYYYMMDDhhmmssZ<nanos>T<UUIDv4-hex-uppercase>
//
// Time is time.Now().UTC() at the moment of the call. The UUID component is
// UUIDv4 from github.com/twinj/uuid, uppercase hex with no separators.
// Callers that need the prefix alone can cast the Kind to string.
func New(k Kind) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s%04d%02d%02d%02d%02d%02dZ%dT%X",
		k,
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
		now.Nanosecond(),
		uuid.NewV4().Bytes(),
	)
}
