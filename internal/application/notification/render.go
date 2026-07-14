package notification

import (
	"html"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// SubscriptionSnapshot pairs one subscription with its source metadata and the
// current rate value for a digest row. The caller must omit sources with no
// current rate — one cannot be fabricated.
type SubscriptionSnapshot struct {
	Subscription domain.RateUserSubscription
	Source       domain.RateSource
	CurrentPrice float64
}

// BuildSubscriptionDigest renders caller subscriptions into one or more Telegram
// HTML message parts using the scheduled notifier's aligned-table format.
// Cross-source dedup on (BaseCurrency, QuoteCurrency, Kind) matches
// RateCheckAgent.Run: BID keeps MAX price, ASK keeps MIN, LAST keeps MAX
// (deterministic tiebreak; revisit to "most recent" when a second feed for one
// ticker is added), ties go to first-seen.
// The header is the plain "FX rates" title — no hashtag — since the digest is
// on-demand, not a trigger fire.
//
// Returns an empty slice when snapshots is empty (the caller owns the
// "no subscriptions yet" empty-state UX).
func BuildSubscriptionDigest(now time.Time, loc *time.Location, snapshots []SubscriptionSnapshot) ([]string, error) {
	if len(snapshots) == 0 {
		return nil, nil
	}

	buckets := make(map[string]*dedupBucket, len(snapshots))
	for _, snap := range snapshots {
		foldIntoBuckets(buckets, snap)
	}

	alerts := make([]alert, 0, len(buckets))
	for _, b := range buckets {
		// Digest is on-demand — Triggers is nil, so reasonHashtags returns ""
		// and headerLines produces the bare header.
		alerts = append(alerts, b.a)
	}

	return buildAlertMessage(now, loc, alerts...)
}

// foldIntoBuckets inserts or updates a dedupBucket for the snapshot's
// (base, quote, kind) key under the BID-MAX / ASK-MIN extremum rule. Price and
// delta are always replaced together (coherent, from the same source). Strict
// > (BID) / < (ASK) means first-seen wins on equal prices, matching RateCheckAgent.Run.
//
// No trigger accumulation — only the agent path collects triggers.
func foldIntoBuckets(buckets map[string]*dedupBucket, snap SubscriptionSnapshot) {
	key := snap.Source.BaseCurrency + "|" + snap.Source.QuoteCurrency + "|" + string(snap.Source.Kind)
	delta := snap.CurrentPrice - snap.Subscription.LatestNotifiedRate

	b := buckets[key]
	if b == nil {
		b = &dedupBucket{
			a: alert{
				SourceName:    html.EscapeString(snap.Source.Name),
				BaseCurrency:  snap.Source.BaseCurrency,
				QuoteCurrency: snap.Source.QuoteCurrency,
				CurrencyKind:  snap.Source.Kind,
				CurrentPrice:  snap.CurrentPrice,
				Delta:         delta,
			},
			triggers: make(map[domain.SubscriptionConditionType]string),
		}
		buckets[key] = b
		return
	}

	// Extremum selection: BID keeps MAX, ASK keeps MIN, LAST keeps MAX.
	// Price, delta, and SourceName are replaced together (coherence invariant).
	switch snap.Source.Kind {
	case domain.RateSourceKindBID:
		if snap.CurrentPrice > b.a.CurrentPrice {
			b.a.CurrentPrice = snap.CurrentPrice
			b.a.Delta = delta
			b.a.SourceName = html.EscapeString(snap.Source.Name)
		}
	case domain.RateSourceKindASK:
		if snap.CurrentPrice < b.a.CurrentPrice {
			b.a.CurrentPrice = snap.CurrentPrice
			b.a.Delta = delta
			b.a.SourceName = html.EscapeString(snap.Source.Name)
		}
	case domain.RateSourceKindLAST:
		// Multiple feeds of the same last price converge; keep MAX as a
		// deterministic tiebreak. MVP has no same-ticker collision (AAPL only
		// from Yahoo, CCBN only from KASE → distinct keys); revisit to
		// "most recent observation" when a second feed for one ticker is added.
		if snap.CurrentPrice > b.a.CurrentPrice {
			b.a.CurrentPrice = snap.CurrentPrice
			b.a.Delta = delta
			b.a.SourceName = html.EscapeString(snap.Source.Name)
		}
	}
}
