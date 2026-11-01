package notification

import (
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSubscriptionDigest(t *testing.T) {
	t.Parallel()

	// fixedDigestNow is a deterministic timestamp for digest header assertions.
	fixedDigestNow := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

	bidSource := func(name string) domain.RateSource {
		return domain.RateSource{
			Name:          name,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindBID,
		}
	}

	t.Run("nil snapshots returns nil result", func(t *testing.T) {
		t.Parallel()

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, nil)
		require.NoError(t, err)
		require.Nil(t, parts)
	})

	t.Run("empty snapshots returns nil result", func(t *testing.T) {
		t.Parallel()

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{})
		require.NoError(t, err)
		require.Nil(t, parts)
	})

	t.Run("single BID snapshot renders pair price and delta", func(t *testing.T) {
		t.Parallel()

		snap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{
				LatestNotifiedRate: 489.30,
			},
			Source:       bidSource("src1"),
			CurrentPrice: 487.55,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{snap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		assert.Contains(t, parts[0], "USD/KZT")
		assert.Contains(t, parts[0], "487.55")
		assert.Contains(t, parts[0], minusSign) // U+2212 minus sign (negative delta)
		assert.Contains(t, parts[0], "1.75")
		assert.Contains(t, parts[0], arrowDown)
	})

	t.Run("BID cross-source dedup keeps MAX price", func(t *testing.T) {
		t.Parallel()

		highSnap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 0},
			Source:       bidSource("S_HIGH"),
			CurrentPrice: 490,
		}
		lowSnap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 0},
			Source:       bidSource("S_LOW"),
			CurrentPrice: 488,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{highSnap, lowSnap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		// Both snapshots share the same (base, quote, kind) → exactly one row.
		assert.Equal(t, 1, strings.Count(parts[0], "USD/KZT"))
		// BID-MAX: 490 wins over 488.
		assert.Contains(t, parts[0], "490")
		assert.NotContains(t, parts[0], "488")
	})

	t.Run("LAST cross-source dedup keeps MAX price with coherent delta and source name", func(t *testing.T) {
		t.Parallel()

		lastSource := func(name string) domain.RateSource {
			return domain.RateSource{
				Name:          name,
				BaseCurrency:  "AAPL",
				QuoteCurrency: "USD",
				Kind:          domain.RateSourceKindLAST,
			}
		}

		highSnap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 280},
			Source:       lastSource("S_HIGH"),
			CurrentPrice: 290,
		}
		lowSnap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 280},
			Source:       lastSource("S_LOW"),
			CurrentPrice: 285,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{highSnap, lowSnap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		// LAST-MAX: 290 wins over 285.
		assert.Contains(t, parts[0], "290")
		assert.NotContains(t, parts[0], "285")
		// Pair label must be AAPL/USD (base/quote), not USD/AAPL.
		assert.Contains(t, parts[0], "AAPL/USD")
		assert.NotContains(t, parts[0], "USD/AAPL")
		// Delta corresponds to the winner (S_HIGH): 290 - 280 = +10.00; loser delta would be +5.00.
		assert.Contains(t, parts[0], "+10.00",
			"delta must come from the winning (MAX) source S_HIGH, not the loser")
		assert.NotContains(t, parts[0], "+5.00",
			"loser S_LOW delta must not appear in the rendered message")
	})

	t.Run("LAST subscription renders correct base/quote label", func(t *testing.T) {
		t.Parallel()

		snap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 270},
			Source: domain.RateSource{
				Name:          "YAHOO_AAPL",
				BaseCurrency:  "AAPL",
				QuoteCurrency: "USD",
				Kind:          domain.RateSourceKindLAST,
			},
			CurrentPrice: 282.34,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{snap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		assert.Contains(t, parts[0], "AAPL/USD")
		assert.Contains(t, parts[0], "282.34")
	})

	t.Run("ASK cross-source dedup keeps MIN price", func(t *testing.T) {
		t.Parallel()

		askSource := func(name string) domain.RateSource {
			return domain.RateSource{
				Name:          name,
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindASK,
			}
		}

		highSnap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 0},
			Source:       askSource("S_HIGH"),
			CurrentPrice: 495,
		}
		lowSnap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 0},
			Source:       askSource("S_LOW"),
			CurrentPrice: 491,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{highSnap, lowSnap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		// ASK-MIN: 491 wins over 495.
		assert.Contains(t, parts[0], "491")
		assert.NotContains(t, parts[0], "495")
	})

	t.Run("first-fire guard: LatestNotifiedRate zero produces blank delta cell", func(t *testing.T) {
		t.Parallel()

		snap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{
				LatestNotifiedRate: 0,
			},
			Source:       bidSource("src1"),
			CurrentPrice: 487.55,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{snap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		// Price present; arrows absent (delta == CurrentPrice → first-fire guard).
		assert.Contains(t, parts[0], "487.55")
		assert.NotContains(t, parts[0], arrowUp)
		assert.NotContains(t, parts[0], arrowDown)
	})

	t.Run("header is plain FX rates with no hashtag", func(t *testing.T) {
		t.Parallel()

		snap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 480},
			Source:       bidSource("src1"),
			CurrentPrice: 487.55,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{snap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		assert.Contains(t, parts[0], "FX rates")
		assert.NotContains(t, parts[0], hashtagDelta)
		assert.NotContains(t, parts[0], hashtagInterval)
		assert.NotContains(t, parts[0], hashtagDaily)
		assert.NotContains(t, parts[0], hashtagCron)
	})

	t.Run("output contains pre block", func(t *testing.T) {
		t.Parallel()

		snap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 480},
			Source:       bidSource("src1"),
			CurrentPrice: 487.55,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, nil, []SubscriptionSnapshot{snap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		assert.Contains(t, parts[0], "<pre>")
		assert.Contains(t, parts[0], "</pre>")
	})

	t.Run("timezone location applied to timestamp", func(t *testing.T) {
		t.Parallel()

		loc, err := time.LoadLocation("Asia/Almaty")
		require.NoError(t, err)

		snap := SubscriptionSnapshot{
			Subscription: domain.RateUserSubscription{LatestNotifiedRate: 480},
			Source:       bidSource("src1"),
			CurrentPrice: 487.55,
		}

		parts, err := BuildSubscriptionDigest(fixedDigestNow, loc, []SubscriptionSnapshot{snap})
		require.NoError(t, err)
		require.Len(t, parts, 1)

		// Asia/Almaty is UTC+5; timestamp should contain "+05".
		assert.Contains(t, parts[0], "+05")
	})
}
