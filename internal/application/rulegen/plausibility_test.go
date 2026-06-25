package rulegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlausibleRangeFor(t *testing.T) {
	t.Parallel()

	t.Run("every seeded pair has an entry", func(t *testing.T) {
		t.Parallel()
		// All 13 base currencies seeded in
		// migrations/202605.007.rate_sources.seed_initial.sql paired with KZT
		// (see migrations/ for the current seed filename).
		seededPairs := []struct {
			base  string
			quote string
		}{
			{"USD", "KZT"},
			{"EUR", "KZT"},
			{"AED", "KZT"},
			{"CHF", "KZT"},
			{"GBP", "KZT"},
			{"CAD", "KZT"},
			{"RUB", "KZT"},
			{"RUR", "KZT"},
			{"TRY", "KZT"},
			{"JPY", "KZT"},
			{"UZS", "KZT"},
			{"GOLD", "KZT"},
			{"SILVER", "KZT"},
		}
		for _, p := range seededPairs {
			lo, hi, ok := plausibleRangeFor(p.base, p.quote)
			require.True(t, ok, "expected entry for %s/%s", p.base, p.quote)
			assert.Greater(t, hi, lo, "%s/%s: hi must be greater than lo", p.base, p.quote)
		}
	})

	t.Run("unknown pair returns ok=false", func(t *testing.T) {
		t.Parallel()
		_, _, ok := plausibleRangeFor("XAU", "KZT")
		assert.False(t, ok)

		_, _, ok2 := plausibleRangeFor("USD", "EUR")
		assert.False(t, ok2)
	})

	t.Run("case-sensitive lookup", func(t *testing.T) {
		t.Parallel()
		// ISO codes are always uppercase; lowercase is a bug we want to surface.
		_, _, ok := plausibleRangeFor("usd", "kzt")
		assert.False(t, ok, "lowercase lookup must not match")

		_, _, ok2 := plausibleRangeFor("Usd", "KZT")
		assert.False(t, ok2, "mixed-case lookup must not match")
	})

	t.Run("USD/KZT range rejects 19.1671 and accepts 469.0", func(t *testing.T) {
		t.Parallel()
		lo, hi, ok := plausibleRangeFor("USD", "KZT")
		require.True(t, ok)
		// Smoke-test incident: 19.1671 was silently accepted before this table.
		assert.Less(t, 19.1671, lo, "19.1671 must be below the lo bound")
		// A real-world BCC USD/KZT rate.
		assert.GreaterOrEqual(t, 469.0, lo)
		assert.LessOrEqual(t, 469.0, hi)
	})
}
