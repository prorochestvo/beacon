package internal_test

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/stretchr/testify/assert"
)

// TestUserAgentsAreByteIdenticalToWhatShipped pins the composed constants against the
// literals that were in the tree before consolidation.
//
// The point of #65 was to consolidate where the identity is written, not to change what
// upstreams receive. These strings go out over the wire to rate sources and Open-Meteo,
// so a stray space or a reordered comment is a behaviour change wearing a refactor's
// clothes. Composing from parts is what makes the two constants unable to drift; this is
// what stops the composition from drifting from history.
//
// Changing either value is a deliberate act: update this test in the same commit, and say
// in the message which upstream you checked.
func TestUserAgentsAreByteIdenticalToWhatShipped(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"Beacon/1.0 (+https://github.com/seilbekskindirov/beacon)",
		internal.UserAgent)

	assert.Equal(t,
		"Beacon/1.0 health-check (+https://github.com/seilbekskindirov/beacon)",
		internal.HealthCheckUserAgent)
}
