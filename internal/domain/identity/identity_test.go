package identity_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Parallel()

	allKinds := []identity.Kind{
		identity.KindRateSource,
		identity.KindRateValue,
		identity.KindRateUserEvent,
		identity.KindRateUserSubscription,
		identity.KindExecutionHistory,
		identity.KindWeatherUserCity,
		identity.KindWeatherObservation,
	}

	t.Run("prefix matches kind", func(t *testing.T) {
		t.Parallel()
		for _, k := range allKinds {
			id := identity.New(k)
			prefix := string(k)
			assert.True(t, strings.HasPrefix(id, prefix),
				"ID %q must start with prefix %q", id, prefix)
		}
	})

	t.Run("output is unique across calls", func(t *testing.T) {
		t.Parallel()
		seen := make(map[string]struct{}, 100)
		for i := 0; i < 100; i++ {
			id := identity.New(identity.KindRateValue)
			_, exists := seen[id]
			require.False(t, exists, "duplicate ID produced on call %d: %q", i, id)
			seen[id] = struct{}{}
			tIdx := strings.LastIndex(id, "T")
			require.Greater(t, tIdx, 0, "ID %q must contain the T separator", id)
			uuidHex := id[tIdx+1:]
			require.NotEmpty(t, uuidHex, "UUID segment must not be empty in %q", id)
			require.NotEqual(t, strings.Repeat("0", len(uuidHex)), uuidHex,
				"UUID segment is all zeros in %q", id)
		}
	})

	t.Run("format matches contract", func(t *testing.T) {
		t.Parallel()
		for _, k := range allKinds {
			prefix := regexp.QuoteMeta(string(k))
			// UUIDv4 is 16 bytes -> exactly 32 uppercase hex chars when formatted with %X.
			// \d+ for the nanosecond field allows "0" (legal at exact-second boundaries);
			// uniqueness is guaranteed by the UUID segment, not the timestamp.
			pattern := regexp.MustCompile(`^` + prefix + `\d{14}Z\d+T[0-9A-F]{32}$`)
			id := identity.New(k)
			assert.True(t, pattern.MatchString(id),
				"ID %q for kind %q does not match expected format", id, k)
		}
	})
}
