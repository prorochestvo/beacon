package identity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFormat(t *testing.T) {
	t.Parallel()

	// One fixed second: only the nanosecond field varies below, which is the
	// field that decides the order of rows sharing a second-resolution timestamp.
	second := time.Date(2026, time.August, 12, 10, 30, 0, 0, time.UTC)

	t.Run("later instant sorts above earlier one across a digit-width boundary", func(t *testing.T) {
		t.Parallel()

		earlier := format(KindRateValue, second.Add(9*time.Nanosecond))
		later := format(KindRateValue, second.Add(123456789*time.Nanosecond))

		require.Less(t, earlier, later,
			"identifier minted at .000000009 (%q) must sort below one minted at .123456789 (%q): "+
				"text order is what ORDER BY id DESC compares", earlier, later)
	})

	t.Run("text order matches time order at every nanosecond width", func(t *testing.T) {
		t.Parallel()

		// Every decimal width a nanosecond component can occupy, ascending in time.
		nanoseconds := []int{0, 9, 99, 999, 9999, 99999, 999999, 9999999, 99999999, 123456789, 999999999}

		previous := format(KindRateValue, second.Add(time.Duration(nanoseconds[0])*time.Nanosecond))
		for _, n := range nanoseconds[1:] {
			current := format(KindRateValue, second.Add(time.Duration(n)*time.Nanosecond))
			require.Less(t, previous, current,
				"identifier minted at %dns (%q) must sort above its predecessor (%q)", n, current, previous)
			previous = current
		}
	})

	t.Run("a later second sorts above an earlier one", func(t *testing.T) {
		t.Parallel()

		// The nanosecond field must never outweigh the seconds above it.
		earlier := format(KindRateValue, second.Add(999999999*time.Nanosecond))
		later := format(KindRateValue, second.Add(time.Second))

		require.Less(t, earlier, later,
			"identifier minted at .999999999 (%q) must sort below one minted a second later (%q)", earlier, later)
	})
}
