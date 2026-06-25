package chart_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_ObtainMeHistory(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)

	// usdKztBidSrc is a minimal BID source for USD/KZT.
	usdKztBidSrc := domain.RateSource{
		Name:          "src-bid",
		Title:         "USD/KZT BID",
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		Kind:          domain.RateSourceKindBID,
		Active:        true,
	}
	// usdKztAskSrc is a minimal ASK source for USD/KZT.
	usdKztAskSrc := domain.RateSource{
		Name:          "src-ask",
		Title:         "USD/KZT ASK",
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		Kind:          domain.RateSourceKindASK,
		Active:        true,
	}
	// eurKztBidSrc is a BID source for EUR/KZT (different pair).
	eurKztBidSrc := domain.RateSource{
		Name:          "src-eur",
		Title:         "EUR/KZT BID",
		BaseCurrency:  "EUR",
		QuoteCurrency: "KZT",
		Kind:          domain.RateSourceKindBID,
		Active:        true,
	}

	subFor := func(sourceName string) domain.RateUserSubscription {
		return domain.RateUserSubscription{
			SourceName:     sourceName,
			ConditionType:  "delta",
			ConditionValue: "0.5",
		}
	}

	t.Run("no subscriptions returns empty page and total zero", func(t *testing.T) {
		t.Parallel()
		svc := newServiceWithHistory(nil, nil, nil, 0, nil)
		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.EqualValues(t, 0, res.Total)
		assert.Empty(t, res.Items)
		assert.Equal(t, "USD/KZT", res.Pair)
	})

	t.Run("pair not matching any subscription returns empty page", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-eur")}
		sources := map[string]domain.RateSource{"src-eur": eurKztBidSrc}
		svc := newServiceWithHistory(subs, sources, nil, 0, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		assert.EqualValues(t, 0, res.Total)
		assert.Empty(t, res.Items)
	})

	t.Run("single source single direction page 1", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid")}
		sources := map[string]domain.RateSource{"src-bid": usdKztBidSrc}
		histRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 490, Timestamp: base},
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487, Timestamp: base.Add(-time.Minute)},
		}
		svc := newServiceWithHistory(subs, sources, histRows, 2, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		require.EqualValues(t, 2, res.Total)
		require.Len(t, res.Items, 2)
		require.NotNil(t, res.Items[0].Bid)
		assert.Equal(t, 490.0, *res.Items[0].Bid)
		assert.Nil(t, res.Items[0].Ask)
		assert.Equal(t, "USD/KZT BID", res.Items[0].SourceTitle)
	})

	t.Run("two sources two directions are interleaved by timestamp", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid"), subFor("src-ask")}
		sources := map[string]domain.RateSource{
			"src-bid": usdKztBidSrc,
			"src-ask": usdKztAskSrc,
		}
		histRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 490, Timestamp: base},
			{SourceName: "src-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 492, Timestamp: base.Add(-30 * time.Second)},
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 488, Timestamp: base.Add(-time.Minute)},
		}
		svc := newServiceWithHistory(subs, sources, histRows, 3, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		require.Len(t, res.Items, 3)
		// BID at base is newest.
		require.NotNil(t, res.Items[0].Bid)
		assert.Equal(t, 490.0, *res.Items[0].Bid)
		assert.Nil(t, res.Items[0].Ask)
	})

	t.Run("delta is nil for first observation in chain", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid")}
		sources := map[string]domain.RateSource{"src-bid": usdKztBidSrc}
		histRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 490, Timestamp: base},
		}
		svc := newServiceWithHistory(subs, sources, histRows, 1, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		require.Len(t, res.Items, 1)
		// Only one row in the chain: delta must be nil.
		assert.Nil(t, res.Items[0].BidDeltaPct)
		assert.Nil(t, res.Items[0].AskDeltaPct)
	})

	t.Run("delta is computed against previous observation in same chain", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid")}
		sources := map[string]domain.RateSource{"src-bid": usdKztBidSrc}
		// Three rows newest-first: items[0]=490, items[1]=487, items[2]=481.
		// Processed oldest-first (481→487→490):
		//   items[2] (481): nil delta (first in chain).
		//   items[1] (487): (487-481)/481*100 ≈ +1.247%.
		//   items[0] (490): (490-487)/487*100 ≈ +0.616%.
		histRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 490, Timestamp: base},
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487, Timestamp: base.Add(-time.Minute)},
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 481, Timestamp: base.Add(-2 * time.Minute)},
		}
		svc := newServiceWithHistory(subs, sources, histRows, 3, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		require.Len(t, res.Items, 3)

		// items[0] = 490 (newest): delta vs 487.
		require.NotNil(t, res.Items[0].BidDeltaPct)
		assert.InDelta(t, (490.0-487.0)/487.0*100, *res.Items[0].BidDeltaPct, 0.001)

		// items[1] = 487: delta vs 481.
		require.NotNil(t, res.Items[1].BidDeltaPct)
		assert.InDelta(t, (487.0-481.0)/481.0*100, *res.Items[1].BidDeltaPct, 0.001)

		// items[2] = 481 (oldest in page): nil delta (first in chain).
		assert.Nil(t, res.Items[2].BidDeltaPct)
	})

	t.Run("pagination offset and limit apply", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid")}
		sources := map[string]domain.RateSource{"src-bid": usdKztBidSrc}
		// Simulate page 2, limit 2 of 5 total rows.
		histRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 300, Timestamp: base.Add(-2 * time.Minute)},
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 200, Timestamp: base.Add(-3 * time.Minute)},
		}
		svc := newServiceWithHistory(subs, sources, histRows, 5, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 2, 2)
		require.NoError(t, err)
		assert.EqualValues(t, 5, res.Total)
		assert.Len(t, res.Items, 2)
		// The oldest row on a mid-pagination page has no predecessor visible in
		// this page; delta must be nil (cross-page anchoring is a v1 known limitation).
		assert.Nil(t, res.Items[len(res.Items)-1].BidDeltaPct,
			"oldest row on a mid-pagination page has no predecessor visible in this page; delta must be nil")
	})

	t.Run("context cancellation propagates as error", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid")}
		sources := map[string]domain.RateSource{"src-bid": usdKztBidSrc}
		svc := newServiceWithHistory(subs, sources, nil, 0, context.Canceled)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := svc.ObtainMeHistory(ctx, "user1", "USD/KZT", "", 1, 20)
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled))
	})

	t.Run("filters by source_title yields rows from that provider only", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid"), subFor("src-ask")}
		sources := map[string]domain.RateSource{
			"src-bid": usdKztBidSrc,
			"src-ask": usdKztAskSrc,
		}
		// The service pre-filters matchingKeys to src-bid (whose title is "USD/KZT BID")
		// before calling the repo, so the fake only needs to return src-bid rows.
		bidOnlyRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 490, Timestamp: base},
		}
		svc2 := newServiceWithHistory(subs, sources, bidOnlyRows, 1, nil)
		res, err := svc2.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "USD/KZT BID", 1, 20)
		require.NoError(t, err)
		require.EqualValues(t, 1, res.Total)
		require.Len(t, res.Items, 1)
		assert.Equal(t, "USD/KZT BID", res.Items[0].SourceTitle)
		// Ensure src-ask title is absent.
		for _, item := range res.Items {
			assert.NotEqual(t, "USD/KZT ASK", item.SourceTitle)
		}
	})

	t.Run("empty source_title returns all rows (regression)", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid"), subFor("src-ask")}
		sources := map[string]domain.RateSource{
			"src-bid": usdKztBidSrc,
			"src-ask": usdKztAskSrc,
		}
		histRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 490, Timestamp: base},
			{SourceName: "src-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 492, Timestamp: base.Add(-30 * time.Second)},
		}
		svc := newServiceWithHistory(subs, sources, histRows, 2, nil)
		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		assert.EqualValues(t, 2, res.Total)
		assert.Len(t, res.Items, 2)
	})

	t.Run("unknown source_title returns empty result and total zero", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid")}
		sources := map[string]domain.RateSource{"src-bid": usdKztBidSrc}
		svc := newServiceWithHistory(subs, sources, nil, 0, nil)
		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "No Such Provider", 1, 20)
		require.NoError(t, err)
		assert.EqualValues(t, 0, res.Total)
		assert.Empty(t, res.Items)
	})

	t.Run("filter respects pagination when source_title matches multiple rows", func(t *testing.T) {
		t.Parallel()
		subs := []domain.RateUserSubscription{subFor("src-bid")}
		sources := map[string]domain.RateSource{"src-bid": usdKztBidSrc}
		// Simulate page 2, limit 1, total 3 for the filtered provider title.
		histRows := []domain.RateValue{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 488, Timestamp: base.Add(-time.Minute)},
		}
		svc := newServiceWithHistory(subs, sources, histRows, 3, nil)
		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "USD/KZT BID", 2, 1)
		require.NoError(t, err)
		assert.EqualValues(t, 3, res.Total)
		assert.Len(t, res.Items, 1)
	})

	t.Run("two sibling sources at one timestamp collapse to one row", func(t *testing.T) {
		t.Parallel()
		// BID and ASK sources share a Title, one row each at the same timestamp.
		// Service must return one MeHistoryRowResult with both Bid and Ask set and Total=1.
		sharedTitle := "Center Credit Bank (FX)"
		bidSrc := domain.RateSource{
			Name:          "ccb-bid",
			Title:         sharedTitle,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindBID,
			Active:        true,
		}
		askSrc := domain.RateSource{
			Name:          "ccb-ask",
			Title:         sharedTitle,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindASK,
			Active:        true,
		}
		subs := []domain.RateUserSubscription{subFor("ccb-bid"), subFor("ccb-ask")}
		sources := map[string]domain.RateSource{
			"ccb-bid": bidSrc,
			"ccb-ask": askSrc,
		}
		histRows := []domain.RateValue{
			{SourceName: "ccb-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487, Timestamp: base},
			{SourceName: "ccb-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 489, Timestamp: base},
		}
		// groupedTotal=1: two raw rows at the same (title, timestamp) tuple.
		svc := newServiceWithHistoryGrouped(subs, sources, histRows, 2, 1, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		require.EqualValues(t, 1, res.Total)
		require.Len(t, res.Items, 1)
		assert.Equal(t, sharedTitle, res.Items[0].SourceTitle)
		require.NotNil(t, res.Items[0].Bid)
		require.NotNil(t, res.Items[0].Ask)
		assert.Equal(t, 487.0, *res.Items[0].Bid)
		assert.Equal(t, 489.0, *res.Items[0].Ask)
	})

	t.Run("Total counts distinct (title, timestamp) tuples not raw rows", func(t *testing.T) {
		t.Parallel()
		// Two timestamps × (BID + ASK) = 4 raw rate_values rows for the same provider.
		// groupedTotal = 2 (two distinct (title, timestamp) tuples).
		sharedTitle := "Center Credit Bank (FX)"
		bidSrc := domain.RateSource{
			Name:          "ccb2-bid",
			Title:         sharedTitle,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindBID,
			Active:        true,
		}
		askSrc := domain.RateSource{
			Name:          "ccb2-ask",
			Title:         sharedTitle,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindASK,
			Active:        true,
		}
		subs := []domain.RateUserSubscription{subFor("ccb2-bid"), subFor("ccb2-ask")}
		sources := map[string]domain.RateSource{
			"ccb2-bid": bidSrc,
			"ccb2-ask": askSrc,
		}
		histRows := []domain.RateValue{
			{SourceName: "ccb2-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487, Timestamp: base},
			{SourceName: "ccb2-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 489, Timestamp: base},
			{SourceName: "ccb2-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 486, Timestamp: base.Add(-time.Minute)},
			{SourceName: "ccb2-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 488, Timestamp: base.Add(-time.Minute)},
		}
		// rowTotal=4, groupedTotal=2.
		svc := newServiceWithHistoryGrouped(subs, sources, histRows, 4, 2, nil)

		res, err := svc.ObtainMeHistory(t.Context(), "user1", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		assert.EqualValues(t, 2, res.Total)
		assert.Len(t, res.Items, 2)
	})
}
