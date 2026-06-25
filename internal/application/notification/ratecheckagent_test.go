package notification

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/repository"
	"github.com/stretchr/testify/require"
)

var _ rateSourceRepository = &repository.RateSourceRepository{}
var _ rateValueRepository = &repository.RateValueRepository{}
var _ rateUserSubscriptionRepository = &repository.RateUserSubscriptionRepository{}
var _ rateCheckEventRepository = &repository.RateUserEventRepository{}

var _ rateSourceRepository = (*mockCheckSourceRepository)(nil)
var _ rateValueRepository = (*mockCheckValueRepository)(nil)
var _ rateValueRepository = (*mockCheckValueRepositoryMulti)(nil)
var _ rateUserSubscriptionRepository = (*mockCheckSubscriptionRepository)(nil)
var _ rateCheckEventRepository = (*mockCheckEventRepository)(nil)

func TestNewRateCheckAgent(t *testing.T) {
	t.Parallel()

	t.Run("valid construction", func(t *testing.T) {
		t.Parallel()

		agent, err := NewRateCheckAgent(
			&mockCheckSourceRepository{},
			&mockCheckValueRepository{},
			&mockCheckSubscriptionRepository{},
			&mockCheckEventRepository{},
			nil, // profile repo is optional → nil falls back to UTC.
			io.Discard,
		)
		require.NoError(t, err)
		require.NotNil(t, agent)
	})
	t.Run("nil rateSourceRepository returns error", func(t *testing.T) {
		t.Parallel()

		_, err := NewRateCheckAgent(nil, &mockCheckValueRepository{}, &mockCheckSubscriptionRepository{}, &mockCheckEventRepository{}, nil, io.Discard)
		require.Error(t, err)
	})
	t.Run("nil rateValueRepository returns error", func(t *testing.T) {
		t.Parallel()

		_, err := NewRateCheckAgent(&mockCheckSourceRepository{}, nil, &mockCheckSubscriptionRepository{}, &mockCheckEventRepository{}, nil, io.Discard)
		require.Error(t, err)
	})
	t.Run("nil rateUserSubscriptionRepository returns error", func(t *testing.T) {
		t.Parallel()

		_, err := NewRateCheckAgent(&mockCheckSourceRepository{}, &mockCheckValueRepository{}, nil, &mockCheckEventRepository{}, nil, io.Discard)
		require.Error(t, err)
	})
	t.Run("nil rateUserEventRepository returns error", func(t *testing.T) {
		t.Parallel()

		_, err := NewRateCheckAgent(&mockCheckSourceRepository{}, &mockCheckValueRepository{}, &mockCheckSubscriptionRepository{}, nil, nil, io.Discard)
		require.Error(t, err)
	})
}

func TestRateCheckAgent_Run(t *testing.T) {
	t.Parallel()

	t.Run("no sources — returns nil, no events queued", func(t *testing.T) {
		t.Parallel()

		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository:           &mockCheckSourceRepository{sources: nil},
			rateValueRepository:            &mockCheckValueRepository{},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{},
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained)
	})

	t.Run("sources with no rate values — returns nil, no events queued", func(t *testing.T) {
		t.Parallel()

		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository:           &mockCheckSourceRepository{sources: []domain.RateSource{{Name: "src1"}}},
			rateValueRepository:            &mockCheckValueRepository{values: nil},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{},
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained)
	})

	t.Run("sources with rate values but no subscriptions — returns nil", func(t *testing.T) {
		t.Parallel()

		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository:           &mockCheckSourceRepository{sources: []domain.RateSource{{Name: "src1"}}},
			rateValueRepository:            &mockCheckValueRepository{values: []domain.RateValue{{Price: 100}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{subs: nil},
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained)
	})

	t.Run("subscription condition satisfied — event retained", func(t *testing.T) {
		t.Parallel()

		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{{Name: "src1", Title: "Test Bank", BaseCurrency: "USD", QuoteCurrency: "KZT"}},
			},
			rateValueRepository: &mockCheckValueRepository{values: []domain.RateValue{{Price: 100}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{
				subs: []domain.RateUserSubscription{{
					UserType:       domain.UserTypeTelegram,
					UserID:         "111",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "0",
				}},
			},
			rateUserEventRepository: eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		require.Equal(t, domain.UserTypeTelegram, eventRepo.retained[0].UserType)
		require.Equal(t, "111", eventRepo.retained[0].UserID)
	})

	t.Run("event retain failure propagated to caller", func(t *testing.T) {
		t.Parallel()

		eventRepo := &mockCheckEventRepository{err: errors.New("event repo down")}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{{Name: "src1", Title: "Test Bank", BaseCurrency: "USD", QuoteCurrency: "KZT"}},
			},
			rateValueRepository: &mockCheckValueRepository{values: []domain.RateValue{{Price: 100}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{
				subs: []domain.RateUserSubscription{{
					UserType:       domain.UserTypeTelegram,
					UserID:         "111",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "0",
				}},
			},
			rateUserEventRepository: eventRepo,
		}

		err := a.Run(t.Context())
		require.Error(t, err, "retain failure must surface to caller, not be swallowed into failCount")
		require.Contains(t, err.Error(), "retain event chat_id=111")
		require.Contains(t, err.Error(), "event repo down")
	})

	t.Run("subscription condition not satisfied — no event retained", func(t *testing.T) {
		t.Parallel()

		// delta=0.01 (1.0-0.99) is below the 1.0 threshold → IsDeltaSatisfied
		// false, no event retained.
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{sources: []domain.RateSource{{Name: "src1"}}},
			rateValueRepository:  &mockCheckValueRepository{values: []domain.RateValue{{Price: 1.0}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{
				subs: []domain.RateUserSubscription{{
					UserType:           domain.UserTypeTelegram,
					UserID:             "222",
					ConditionType:      domain.ConditionTypeDelta,
					ConditionValue:     "1.0",
					LatestNotifiedRate: 0.99,
				}},
			},
			rateUserEventRepository: eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained)
	})

	t.Run("source repo error — error returned", func(t *testing.T) {
		t.Parallel()

		a := &RateCheckAgent{
			rateSourceRepository:           &mockCheckSourceRepository{err: errors.New("source db fail")},
			rateValueRepository:            &mockCheckValueRepository{},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{},
			rateUserEventRepository:        &mockCheckEventRepository{},
		}

		require.Error(t, a.Run(t.Context()))
	})

	t.Run("rate value repo error — error returned for that source", func(t *testing.T) {
		t.Parallel()

		a := &RateCheckAgent{
			rateSourceRepository:           &mockCheckSourceRepository{sources: []domain.RateSource{{Name: "src1"}}},
			rateValueRepository:            &mockCheckValueRepository{err: errors.New("value db fail")},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{},
			rateUserEventRepository:        &mockCheckEventRepository{},
		}

		err := a.Run(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "src1")
	})

	t.Run("subscription repo error — error returned for that source", func(t *testing.T) {
		t.Parallel()

		a := &RateCheckAgent{
			rateSourceRepository:           &mockCheckSourceRepository{sources: []domain.RateSource{{Name: "src1"}}},
			rateValueRepository:            &mockCheckValueRepository{values: []domain.RateValue{{Price: 100}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{err: errors.New("sub db fail")},
			rateUserEventRepository:        &mockCheckEventRepository{},
		}

		err := a.Run(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "src1")
	})

	t.Run("unsupported UserType — error entry in result", func(t *testing.T) {
		t.Parallel()

		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{sources: []domain.RateSource{{Name: "src1"}}},
			rateValueRepository:  &mockCheckValueRepository{values: []domain.RateValue{{Price: 100}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{
				subs: []domain.RateUserSubscription{{
					UserType:       "bogus",
					UserID:         "999",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "0",
				}},
			},
			rateUserEventRepository: &mockCheckEventRepository{},
		}

		err := a.Run(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported user type")
	})

	t.Run("subscription retain error appears in error map", func(t *testing.T) {
		t.Parallel()

		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{sources: []domain.RateSource{{Name: "src1"}}},
			rateValueRepository:  &mockCheckValueRepository{values: []domain.RateValue{{Price: 100}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{
				subs: []domain.RateUserSubscription{{
					UserType:       domain.UserTypeTelegram,
					UserID:         "555",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "0",
				}},
				retainErr: errors.New("retain fail"),
			},
			rateUserEventRepository: &mockCheckEventRepository{},
		}

		err := a.Run(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "src1")
	})

	t.Run("delta type unchanged rate does not fire after first notification", func(t *testing.T) {
		t.Parallel()
		// Regression: notifications sent every 10 minutes when rate was stable.
		// LatestNotifiedRate == currentValue → delta == 0 with LatestNotifiedRate > 0 → must not fire.
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{{
					Name: "KZ_QAZPOST_BID_USD_KZT", Title: "QazPost",
					BaseCurrency: "USD", QuoteCurrency: "KZT",
				}},
			},
			rateValueRepository: &mockCheckValueRepository{
				values: []domain.RateValue{{Price: 470.0}},
			},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{
				subs: []domain.RateUserSubscription{{
					UserType:           domain.UserTypeTelegram,
					UserID:             "115818690",
					ConditionType:      domain.ConditionTypeDelta,
					ConditionValue:     "1.0",
					LatestNotifiedRate: 470.0, // already notified at this price
				}},
			},
			rateUserEventRepository: eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no notification expected when rate is unchanged")
	})

	// Table format does not show source names; assert the two distinct pair
	// strings (USD/KZT, EUR/KZT) appear as two rows in one consolidated message.
	t.Run("two sources same user — consolidated into one event with two pair rows", func(t *testing.T) {
		t.Parallel()

		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{
					{Name: "SRC_A", Title: "Alpha Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
					{Name: "SRC_B", Title: "Beta Bank", BaseCurrency: "EUR", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
				},
			},
			rateValueRepository: &mockCheckValueRepository{values: []domain.RateValue{{Price: 475}}},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepository{
				subs: []domain.RateUserSubscription{{
					UserType:       domain.UserTypeTelegram,
					UserID:         "115818690",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "0",
				}},
			},
			rateUserEventRepository: eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		msg := eventRepo.retained[0].Message
		// The two distinct pairs must appear as rows (BID → base/quote); source names are not rendered.
		require.Contains(t, msg, "USD/KZT")
		require.Contains(t, msg, "EUR/KZT")
		require.NotContains(t, msg, "Alpha Bank")
		require.NotContains(t, msg, "Beta Bank")
	})

	t.Run("multiple subs same dedup key — single bullet, all retained", func(t *testing.T) {
		t.Parallel()

		const currentPrice = 475.0
		subRepo := &mockCheckSubscriptionRepository{
			subs: []domain.RateUserSubscription{
				{
					UserType:       domain.UserTypeTelegram,
					UserID:         "42",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "0",
				},
				{
					UserType:       domain.UserTypeTelegram,
					UserID:         "42",
					ConditionType:  domain.ConditionTypeInterval,
					ConditionValue: "4h",
				},
				{
					UserType:       domain.UserTypeTelegram,
					UserID:         "42",
					ConditionType:  domain.ConditionTypeDaily,
					ConditionValue: "00:00:00",
				},
			},
		}
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{{
					Name: "SRC_X", Title: "X Bank",
					BaseCurrency: "USD", QuoteCurrency: "KZT",
				}},
			},
			rateValueRepository:            &mockCheckValueRepository{values: []domain.RateValue{{Price: currentPrice}}},
			rateUserSubscriptionRepository: subRepo,
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))

		require.Len(t, eventRepo.retained, 1, "all three triggers must collapse to one bullet")

		msg := eventRepo.retained[0].Message
		// Hashtags must cover both delta and at least one schedule type.
		require.Contains(t, msg, hashtagDelta, "#DELTA tag must appear")
		require.True(t,
			strings.Contains(msg, hashtagInterval) ||
				strings.Contains(msg, hashtagDaily) ||
				strings.Contains(msg, hashtagCron),
			"at least one schedule hashtag must appear")

		require.Len(t, subRepo.retained, 3, "all three subscriptions must be retained")
		for _, s := range subRepo.retained {
			require.Equal(t, currentPrice, s.LatestNotifiedRate, "LatestNotifiedRate must advance for every retained sub")
		}
	})

	t.Run("same condition type fires twice — collapsed to one badge bit", func(t *testing.T) {
		t.Parallel()

		const currentPrice = 10.0
		subRepo := &mockCheckSubscriptionRepository{
			subs: []domain.RateUserSubscription{
				{
					UserType:       domain.UserTypeTelegram,
					UserID:         "77",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "1.0",
				},
				{
					UserType:       domain.UserTypeTelegram,
					UserID:         "77",
					ConditionType:  domain.ConditionTypeDelta,
					ConditionValue: "0.5",
				},
			},
		}
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{{
					Name: "SRC_Y", Title: "Y Bank",
					BaseCurrency: "USD", QuoteCurrency: "KZT",
				}},
			},
			rateValueRepository:            &mockCheckValueRepository{values: []domain.RateValue{{Price: currentPrice}}},
			rateUserSubscriptionRepository: subRepo,
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))

		require.Len(t, eventRepo.retained, 1)
		msg := eventRepo.retained[0].Message

		// Hashtag shows #DELTA exactly once even though delta fires twice.
		require.Contains(t, msg, hashtagDelta)
		require.Equal(t, 1, strings.Count(msg, hashtagDelta))

		require.Len(t, subRepo.retained, 2, "both subscriptions must be retained")
	})

	t.Run("cross-source dedup same pair+kind — one row per user", func(t *testing.T) {
		t.Parallel()

		// Two BID sources for USD/KZT, user subscribed at both → exactly ONE
		// row, both subscriptions retained.
		const (
			priceA = 471.0
			priceB = 473.0 // BID-MAX: priceB wins
		)
		subRepo := &mockCheckSubscriptionRepository{
			subs: []domain.RateUserSubscription{{
				UserType:       domain.UserTypeTelegram,
				UserID:         "99",
				ConditionType:  domain.ConditionTypeDelta,
				ConditionValue: "0",
			}},
		}
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{
					{Name: "SRC_A", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
					{Name: "SRC_B", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
				},
			},
			rateValueRepository: &mockCheckValueRepositoryMulti{
				values: map[string]float64{
					"SRC_A": priceA,
					"SRC_B": priceB,
				},
			},
			rateUserSubscriptionRepository: subRepo,
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))

		// One event message; the message contains USD/KZT exactly once as a row.
		require.Len(t, eventRepo.retained, 1)
		msg := eventRepo.retained[0].Message
		// Count occurrences of the pair label — should appear exactly once.
		require.Equal(t, 1, countLines(msg, "USD/KZT"), "pair must appear in exactly one row")

		// Both subs (one per source) retained, each with its own per-source
		// LatestNotifiedRate. Catches the bug where hoisting the assignment out
		// of the per-source loop gives the losing source's sub the winning price.
		require.Len(t, subRepo.retained, 2, "retain-count canary: both subs must be retained")
		retainedPrices := make(map[float64]bool, 2)
		for _, s := range subRepo.retained {
			retainedPrices[s.LatestNotifiedRate] = true
		}
		require.True(t, retainedPrices[priceA], "SRC_A LatestNotifiedRate (%.2f) must be retained", priceA)
		require.True(t, retainedPrices[priceB], "SRC_B LatestNotifiedRate (%.2f) must be retained", priceB)
	})

	t.Run("BID-MAX selection — higher price wins, delta follows winner", func(t *testing.T) {
		t.Parallel()

		// SRC_B (473) wins over SRC_A (471) under BID-MAX; assert the winning
		// price in the rendered message.
		const (
			priceA = 471.0
			priceB = 473.0
		)
		subRepo := &mockCheckSubscriptionRepository{
			subs: []domain.RateUserSubscription{{
				UserType:       domain.UserTypeTelegram,
				UserID:         "99",
				ConditionType:  domain.ConditionTypeDelta,
				ConditionValue: "0",
			}},
		}
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{
					{Name: "SRC_A", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
					{Name: "SRC_B", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
				},
			},
			rateValueRepository: &mockCheckValueRepositoryMulti{
				values: map[string]float64{
					"SRC_A": priceA,
					"SRC_B": priceB,
				},
			},
			rateUserSubscriptionRepository: subRepo,
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		msg := eventRepo.retained[0].Message
		require.Contains(t, msg, "473.00", "BID-MAX: higher price must appear in message")
		require.NotContains(t, msg, "471.00", "lower BID price must not appear")

		// Both subs retained, each with its per-source price.
		require.Len(t, subRepo.retained, 2)
		retainedPrices := make(map[float64]bool, 2)
		for _, s := range subRepo.retained {
			retainedPrices[s.LatestNotifiedRate] = true
		}
		require.True(t, retainedPrices[priceA], "SRC_A LatestNotifiedRate must be retained")
		require.True(t, retainedPrices[priceB], "SRC_B LatestNotifiedRate must be retained")
	})

	t.Run("ASK-MIN selection — lower price wins, delta follows winner", func(t *testing.T) {
		t.Parallel()

		// SRC_A (471) wins over SRC_B (473) under ASK-MIN.
		const (
			priceA = 471.0
			priceB = 473.0
		)
		subRepo := &mockCheckSubscriptionRepository{
			subs: []domain.RateUserSubscription{{
				UserType:       domain.UserTypeTelegram,
				UserID:         "88",
				ConditionType:  domain.ConditionTypeDelta,
				ConditionValue: "0",
			}},
		}
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{
					{Name: "SRC_A", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindASK},
					{Name: "SRC_B", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindASK},
				},
			},
			rateValueRepository: &mockCheckValueRepositoryMulti{
				values: map[string]float64{
					"SRC_A": priceA,
					"SRC_B": priceB,
				},
			},
			rateUserSubscriptionRepository: subRepo,
			rateUserEventRepository:        eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		msg := eventRepo.retained[0].Message
		require.Contains(t, msg, "471.00", "ASK-MIN: lower price must appear in message")
		require.NotContains(t, msg, "473.00", "higher ASK price must not appear")

		// Both subs retained, each with its per-source price.
		require.Len(t, subRepo.retained, 2)
		retainedPrices := make(map[float64]bool, 2)
		for _, s := range subRepo.retained {
			retainedPrices[s.LatestNotifiedRate] = true
		}
		require.True(t, retainedPrices[priceA], "SRC_A LatestNotifiedRate must be retained")
		require.True(t, retainedPrices[priceB], "SRC_B LatestNotifiedRate must be retained")
	})

	t.Run("delta follows winning source", func(t *testing.T) {
		t.Parallel()

		// SRC_A: price=480, LatestNotifiedRate=470 → delta=+10 (BID loser; priceA < priceB)
		// SRC_B: price=490, LatestNotifiedRate=475 → delta=+15 (BID winner; higher price)
		// BID-MAX → SRC_B wins; rendered delta must be +15, not +10.
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{
					{Name: "SRC_A", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
					{Name: "SRC_B", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
				},
			},
			rateValueRepository: &mockCheckValueRepositoryMulti{
				values: map[string]float64{
					"SRC_A": 480.0,
					"SRC_B": 490.0,
				},
			},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepositoryMulti{
				subsBySource: map[string][]domain.RateUserSubscription{
					"SRC_A": {{
						UserType:           domain.UserTypeTelegram,
						UserID:             "44",
						ConditionType:      domain.ConditionTypeDelta,
						ConditionValue:     "0",
						LatestNotifiedRate: 470.0,
					}},
					"SRC_B": {{
						UserType:           domain.UserTypeTelegram,
						UserID:             "44",
						ConditionType:      domain.ConditionTypeDelta,
						ConditionValue:     "0",
						LatestNotifiedRate: 475.0,
					}},
				},
			},
			rateUserEventRepository: eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		msg := eventRepo.retained[0].Message
		// Winner delta = 490 - 475 = 15.00; loser delta = 480 - 470 = 10.00.
		require.Contains(t, msg, "+15.00", "delta must come from the winning source")
		require.NotContains(t, msg, "+10.00", "loser's delta must not appear")
	})

	t.Run("BID-MAX tie — first-seen wins", func(t *testing.T) {
		t.Parallel()

		// Both BID sources have the same price. The strict > comparison in the
		// extremum-selection branch means the first-seen source retains the bucket
		// on equal prices. SRC_A is listed first and has LatestNotifiedRate=400,
		// so its delta = 480 - 400 = 80. SRC_B has LatestNotifiedRate=460, so its
		// delta = 480 - 460 = 20. The first-seen (SRC_A) delta must win.
		const tiedPrice = 480.0
		eventRepo := &mockCheckEventRepository{}
		a := &RateCheckAgent{
			rateSourceRepository: &mockCheckSourceRepository{
				sources: []domain.RateSource{
					{Name: "SRC_A", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
					{Name: "SRC_B", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
				},
			},
			rateValueRepository: &mockCheckValueRepositoryMulti{
				values: map[string]float64{
					"SRC_A": tiedPrice,
					"SRC_B": tiedPrice,
				},
			},
			rateUserSubscriptionRepository: &mockCheckSubscriptionRepositoryMulti{
				subsBySource: map[string][]domain.RateUserSubscription{
					"SRC_A": {{
						UserType:           domain.UserTypeTelegram,
						UserID:             "55",
						ConditionType:      domain.ConditionTypeDelta,
						ConditionValue:     "0",
						LatestNotifiedRate: 400.0, // delta = 80
					}},
					"SRC_B": {{
						UserType:           domain.UserTypeTelegram,
						UserID:             "55",
						ConditionType:      domain.ConditionTypeDelta,
						ConditionValue:     "0",
						LatestNotifiedRate: 460.0, // delta = 20
					}},
				},
			},
			rateUserEventRepository: eventRepo,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		msg := eventRepo.retained[0].Message
		// First-seen source (SRC_A) wins the tie; its delta (+80.00) must appear.
		require.Contains(t, msg, "+80.00", "first-seen source delta must win on BID tie")
		require.NotContains(t, msg, "+20.00", "second-seen source delta must not appear on BID tie")
	})
}

// countLines counts how many lines in s contain the substring sub.
func countLines(s, sub string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

type mockCheckSourceRepository struct {
	sources []domain.RateSource
	err     error
}

func (m *mockCheckSourceRepository) ObtainAllRateSources(_ context.Context) ([]domain.RateSource, error) {
	return m.sources, m.err
}

type mockCheckValueRepository struct {
	values []domain.RateValue
	err    error
}

func (m *mockCheckValueRepository) ObtainLastNRateValuesBySourceName(_ context.Context, _ string, _ int64) ([]domain.RateValue, error) {
	return m.values, m.err
}

// mockCheckValueRepositoryMulti returns different prices per source name.
type mockCheckValueRepositoryMulti struct {
	values map[string]float64
}

func (m *mockCheckValueRepositoryMulti) ObtainLastNRateValuesBySourceName(_ context.Context, name string, _ int64) ([]domain.RateValue, error) {
	if p, ok := m.values[name]; ok {
		return []domain.RateValue{{Price: p}}, nil
	}
	return nil, nil
}

type mockCheckSubscriptionRepository struct {
	subs      []domain.RateUserSubscription
	err       error
	retainErr error
	retained  []*domain.RateUserSubscription
}

func (m *mockCheckSubscriptionRepository) ObtainRateUserSubscriptionsBySource(_ context.Context, _ string) ([]domain.RateUserSubscription, error) {
	return m.subs, m.err
}

func (m *mockCheckSubscriptionRepository) RetainRateUserSubscription(_ context.Context, s *domain.RateUserSubscription) error {
	if m.retainErr != nil {
		return m.retainErr
	}
	cp := *s
	m.retained = append(m.retained, &cp)
	return m.err
}

// mockCheckSubscriptionRepositoryMulti returns different subscriptions per source name.
type mockCheckSubscriptionRepositoryMulti struct {
	subsBySource map[string][]domain.RateUserSubscription
	retained     []*domain.RateUserSubscription
}

var _ rateUserSubscriptionRepository = (*mockCheckSubscriptionRepositoryMulti)(nil)

func (m *mockCheckSubscriptionRepositoryMulti) ObtainRateUserSubscriptionsBySource(_ context.Context, name string) ([]domain.RateUserSubscription, error) {
	return m.subsBySource[name], nil
}

func (m *mockCheckSubscriptionRepositoryMulti) RetainRateUserSubscription(_ context.Context, s *domain.RateUserSubscription) error {
	cp := *s
	m.retained = append(m.retained, &cp)
	return nil
}

type mockCheckEventRepository struct {
	retained []*domain.RateUserEvent
	err      error
}

func (m *mockCheckEventRepository) RetainRateUserEvent(_ context.Context, e *domain.RateUserEvent) error {
	cp := *e
	m.retained = append(m.retained, &cp)
	return m.err
}

func TestCollapseConditionValue(t *testing.T) {
	t.Parallel()

	t.Run("delta — lower incoming wins", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "0.5", collapseConditionValue("1.0", "0.5", domain.ConditionTypeDelta))
	})
	t.Run("delta — higher incoming keeps existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "0.5", collapseConditionValue("0.5", "1.0", domain.ConditionTypeDelta))
	})
	t.Run("delta — malformed incoming preserves existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "1.0", collapseConditionValue("1.0", "garbage", domain.ConditionTypeDelta))
	})
	t.Run("delta — malformed existing preserves existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "garbage", collapseConditionValue("garbage", "0.5", domain.ConditionTypeDelta))
	})

	t.Run("interval — shorter incoming wins", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "10m", collapseConditionValue("1h", "10m", domain.ConditionTypeInterval))
	})
	t.Run("interval — longer incoming keeps existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "10m", collapseConditionValue("10m", "1h", domain.ConditionTypeInterval))
	})
	t.Run("interval — malformed incoming preserves existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "1h", collapseConditionValue("1h", "garbage", domain.ConditionTypeInterval))
	})
	t.Run("interval — malformed existing preserves existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "garbage", collapseConditionValue("garbage", "10m", domain.ConditionTypeInterval))
	})

	t.Run("daily — lex-earlier incoming wins", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "08:00", collapseConditionValue("09:00", "08:00", domain.ConditionTypeDaily))
	})
	t.Run("daily — lex-later incoming keeps existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "08:00", collapseConditionValue("08:00", "09:00", domain.ConditionTypeDaily))
	})

	t.Run("cron — earlier weekday incoming wins", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "0 9 * * 1", collapseConditionValue("0 9 * * 5", "0 9 * * 1", domain.ConditionTypeCron))
	})
	t.Run("cron — later weekday incoming keeps existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "0 9 * * 1", collapseConditionValue("0 9 * * 1", "0 9 * * 5", domain.ConditionTypeCron))
	})
	t.Run("cron — malformed incoming preserves existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "0 9 * * 1", collapseConditionValue("0 9 * * 1", "garbage", domain.ConditionTypeCron))
	})
	t.Run("cron — malformed existing keeps existing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "garbage", collapseConditionValue("garbage", "0 9 * * 1", domain.ConditionTypeCron))
	})
}
