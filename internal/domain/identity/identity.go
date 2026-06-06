// Package identity mints unique, human-readable identifiers for domain entities.
// Every identifier has the format:
//
//	<prefix>YYYYMMDDhhmmssZ<nanos>T<UUIDv4-hex-uppercase>
//
// where the prefix is determined by the Kind constant passed to New.
// The on-disk contract (prefix strings, field order, case) is frozen:
// no migration is required when adopting this package in place of the
// per-repository helpers it replaces.
package identity

import (
	"fmt"
	"time"

	"github.com/twinj/uuid"
)

// Kind is a closed enum that identifies the entity type an ID was minted for.
// The string value of each Kind constant doubles as the on-disk prefix and must
// not be changed without a data migration.
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
)

// New returns a new unique string identifier for the given Kind.
// The format is:
//
//	<prefix>YYYYMMDDhhmmssZ<nanos>T<UUIDv4-hex-uppercase>
//
// Time is taken from time.Now().UTC() at the moment of the call.
// The UUID component uses UUIDv4 from github.com/twinj/uuid, formatted as
// uppercase hex with no separators, matching the existing on-disk convention.
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
