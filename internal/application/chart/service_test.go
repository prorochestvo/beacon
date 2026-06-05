package chart_test

import (
	"context"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal/application/chart"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/domain/ratepair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ chart.SubscriptionsLoader = (*fakeSubs)(nil)
var _ chart.SourcesLoader = (*fakeSources)(nil)
var _ chart.ValuesLoader = (*fakeValues)(nil)
var _ chart.HistoryValuesLoader = (*fakeHistoryValues)(nil)
var _ chart.PublicSourcesLoader = (*fakePublicSources)(nil)

type fakeSubs struct {
	subs []domain.RateUserSubscription
	err  error
}

func (f *fakeSubs) ObtainRateUserSubscriptionsByUserID(_ context.Context, _ domain.UserType, _ string) ([]domain.RateUserSubscription, error) {
	return f.subs, f.err
}

type fakeSources struct {
	sources map[string]domain.RateSource
	err     error
}

func (f *fakeSources) ObtainRateSourcesByNames(_ context.Context, names []string) (map[string]domain.RateSource, error) {
	if f.err != nil {
		return nil, f.err
	}
	result := make(map[string]domain.RateSource, len(names))
	for _, n := range names {
		if s, ok := f.sources[n]; ok {
			result[n] = s
		}
	}
	return result, nil
}

type fakeValues struct {
	values []domain.RateValue
	err    error
}

func (f *fakeValues) ObtainValuesForPairsSince(_ context.Context, _ []domain.SourcePairKey, _ time.Time) ([]domain.RateValue, error) {
	return f.values, f.err
}

type fakeHistoryValues struct {
	rows         []domain.RateValue
	total        int64
	groupedTotal int64
	err          error
}

func (f *fakeHistoryValues) ObtainHistoryForPairsPaged(_ context.Context, _ []domain.SourcePairKey, _, _ int64) ([]domain.RateValue, int64, int64, error) {
	return f.rows, f.total, f.groupedTotal, f.err
}

type fakePublicSources struct {
	keys []domain.SourcePairKey
	err  error
}

func (f *fakePublicSources) ObtainDistinctActivePairTriples(_ context.Context) ([]domain.SourcePairKey, error) {
	return f.keys, f.err
}

// fixedNow pins the clock to a known time so bucket arithmetic is deterministic.
var fixedNow = time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)

func newService(subs []domain.RateUserSubscription, sources map[string]domain.RateSource, values []domain.RateValue) *chart.Service {
	return chart.NewService(
		&fakeSubs{subs: subs},
		&fakeSources{sources: sources},
		&fakeValues{values: values},
		&fakeHistoryValues{},
		&fakePublicSources{},
		func() time.Time { return fixedNow },
	)
}

func newServiceWithHistory(
	subs []domain.RateUserSubscription,
	sources map[string]domain.RateSource,
	histRows []domain.RateValue,
	histTotal int64,
	histErr error,
) *chart.Service {
	// groupedTotal mirrors histTotal for existing subtests: single-source,
	// single-direction data produces equal row-level and grouped-title counts.
	return chart.NewService(
		&fakeSubs{subs: subs},
		&fakeSources{sources: sources},
		&fakeValues{},
		&fakeHistoryValues{rows: histRows, total: histTotal, groupedTotal: histTotal, err: histErr},
		&fakePublicSources{},
		func() time.Time { return fixedNow },
	)
}

// newServiceWithHistoryGrouped is like newServiceWithHistory but lets the caller
// specify rowTotal and groupedTotal independently. Use this for subtests where
// BID/ASK sibling sources share a title so rowTotal != groupedTotal.
func newServiceWithHistoryGrouped(
	subs []domain.RateUserSubscription,
	sources map[string]domain.RateSource,
	histRows []domain.RateValue,
	rowTotal, groupedTotal int64,
	histErr error,
) *chart.Service {
	return chart.NewService(
		&fakeSubs{subs: subs},
		&fakeSources{sources: sources},
		&fakeValues{},
		&fakeHistoryValues{rows: histRows, total: rowTotal, groupedTotal: groupedTotal, err: histErr},
		&fakePublicSources{},
		func() time.Time { return fixedNow },
	)
}

func TestService_ObtainMeChart(t *testing.T) {
	t.Parallel()

	t.Run("empty subscriptions returns empty pairs", func(t *testing.T) {
		t.Parallel()
		svc := newService(nil, nil, nil)
		ch, err := svc.ObtainMeChart(t.Context(), "user1")
		require.NoError(t, err)
		require.NotNil(t, ch)
		assert.Empty(t, ch.Pairs)
	})

	t.Run("subscriptions loader error propagates", func(t *testing.T) {
		t.Parallel()
		svc := chart.NewService(
			&fakeSubs{err: errFake},
			&fakeSources{},
			&fakeValues{},
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		_, err := svc.ObtainMeChart(t.Context(), "user1")
		require.Error(t, err)
	})

	t.Run("sources loader error propagates", func(t *testing.T) {
		t.Parallel()
		svc := chart.NewService(
			&fakeSubs{subs: []domain.RateUserSubscription{{SourceName: "src"}}},
			&fakeSources{err: errFake},
			&fakeValues{},
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		_, err := svc.ObtainMeChart(t.Context(), "user1")
		require.Error(t, err)
	})

	t.Run("values loader error propagates", func(t *testing.T) {
		t.Parallel()
		svc := chart.NewService(
			&fakeSubs{subs: []domain.RateUserSubscription{{SourceName: "src"}}},
			&fakeSources{sources: map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			}},
			&fakeValues{err: errFake},
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		_, err := svc.ObtainMeChart(t.Context(), "user1")
		require.Error(t, err)
	})

	t.Run("single BID pair with dense data produces correct delta and role color", func(t *testing.T) {
		t.Parallel()
		since := fixedNow.Add(-ratepair.ChartWindow)
		step := ratepair.ChartWindow / 12
		ts0 := since.Add(step / 2)
		ts11 := since.Add(11*step + step/2)

		svc := chart.NewService(
			&fakeSubs{subs: []domain.RateUserSubscription{{SourceName: "src"}}},
			&fakeSources{sources: map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			}},
			&fakeValues{values: []domain.RateValue{
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 400, Timestamp: ts0},
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 500, Timestamp: ts11},
			}},
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)

		row := result.Pairs[0]
		assert.Equal(t, "USD/KZT", row.Pair)
		require.Len(t, row.Series, 1)

		sr := row.Series[0]
		assert.Equal(t, domain.RateSourceKind("BID"), sr.Kind)
		assert.Equal(t, ratepair.ColorBid, sr.Color)
		assert.False(t, sr.Sparse)
		assert.InDelta(t, 25.0, sr.DeltaPct, 0.001, "delta should be (500-400)/400*100=25%%")
		assert.Equal(t, 500.0, sr.Latest)
		assert.NotEmpty(t, sr.Points)
	})

	t.Run("single ASK pair: label is inverted to quote/base form", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "src"}},
			map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "ASK"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		// ASK-only label is BID-natural: base becomes quote's counterpart.
		// USD ASK stored as base=USD quote=KZT → display label USD/KZT.
		assert.Equal(t, "USD/KZT", result.Pairs[0].Pair,
			"ASK-only pair must use BID-natural label (quote/base inverted from ASK direction)")
		require.Len(t, result.Pairs[0].Series, 1)
		assert.Equal(t, ratepair.ColorAsk, result.Pairs[0].Series[0].Color)
	})

	t.Run("BID and ASK for same pair collapse into one row with two series", func(t *testing.T) {
		t.Parallel()
		since := fixedNow.Add(-ratepair.ChartWindow)
		step := ratepair.ChartWindow / 12
		ts0 := since.Add(step / 2)
		ts11 := since.Add(11*step + step/2)

		svc := newService(
			[]domain.RateUserSubscription{
				{SourceName: "src-bid"},
				{SourceName: "src-ask"},
			},
			map[string]domain.RateSource{
				"src-bid": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
				"src-ask": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "ASK"},
			},
			[]domain.RateValue{
				{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487.0, Timestamp: ts0},
				{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487.55, Timestamp: ts11},
				{SourceName: "src-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 488.0, Timestamp: ts0},
				{SourceName: "src-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 488.95, Timestamp: ts11},
			},
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1, "BID and ASK must collapse to one pair row")

		row := result.Pairs[0]
		assert.Equal(t, "USD/KZT", row.Pair)
		require.Len(t, row.Series, 2)
		assert.Equal(t, domain.RateSourceKind("BID"), row.Series[0].Kind, "BID must be first in series")
		assert.Equal(t, domain.RateSourceKind("ASK"), row.Series[1].Kind, "ASK must be second in series")
		assert.Equal(t, ratepair.ColorBid, row.Series[0].Color)
		assert.Equal(t, ratepair.ColorAsk, row.Series[1].Color)

		require.NotNil(t, row.SpreadPct, "SpreadPct must be set when both directions are present")
		assert.InDelta(t, (488.95-487.55)/487.55*100, *row.SpreadPct, 0.001)
	})

	t.Run("spread is nil when only BID is present", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "src"}},
			map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		assert.Nil(t, result.Pairs[0].SpreadPct)
	})

	t.Run("spread is nil when BID latest is zero", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{
				{SourceName: "src-bid"},
				{SourceName: "src-ask"},
			},
			map[string]domain.RateSource{
				"src-bid": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
				"src-ask": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "ASK"},
			},
			// No values for BID → BID.Latest=0; spread must be nil.
			[]domain.RateValue{
				{SourceName: "src-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 488.95, Timestamp: fixedNow.Add(-time.Hour)},
			},
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		assert.Nil(t, result.Pairs[0].SpreadPct, "SpreadPct must be nil when BID latest is zero")
	})

	t.Run("single pair with sparse data marks series as sparse", func(t *testing.T) {
		t.Parallel()
		since := fixedNow.Add(-ratepair.ChartWindow)
		step := ratepair.ChartWindow / 12
		ts := since.Add(step / 2)

		svc := chart.NewService(
			&fakeSubs{subs: []domain.RateUserSubscription{{SourceName: "src"}}},
			&fakeSources{sources: map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			}},
			&fakeValues{values: []domain.RateValue{
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 450, Timestamp: ts},
			}},
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		require.Len(t, result.Pairs[0].Series, 1)
		sr := result.Pairs[0].Series[0]
		assert.True(t, sr.Sparse)
		assert.Equal(t, 0.0, sr.DeltaPct)
		assert.Equal(t, 450.0, sr.Latest)
	})

	t.Run("single pair with zero data in window is no-data series", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "src"}},
			map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		require.Len(t, result.Pairs[0].Series, 1)
		sr := result.Pairs[0].Series[0]
		assert.True(t, sr.Sparse)
		assert.Equal(t, 0.0, sr.Latest)
		assert.Nil(t, sr.Points)
	})

	t.Run("dedupe collapses same base/quote/kind from multiple subscriptions", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{
				{SourceName: "src", UserID: "u1"},
				{SourceName: "src", UserID: "u1"},
			},
			map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u1")
		require.NoError(t, err)
		assert.Len(t, result.Pairs, 1)
	})

	t.Run("delta sign math: declining series produces negative delta", func(t *testing.T) {
		t.Parallel()
		since := fixedNow.Add(-ratepair.ChartWindow)
		step := ratepair.ChartWindow / 12
		ts0 := since.Add(step / 2)
		ts11 := since.Add(11*step + step/2)

		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "src"}},
			map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			[]domain.RateValue{
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 500, Timestamp: ts0},
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 400, Timestamp: ts11},
			},
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		require.Len(t, result.Pairs[0].Series, 1)
		assert.Less(t, result.Pairs[0].Series[0].DeltaPct, 0.0, "declining series must have negative delta")
	})

	t.Run("metal base row has CategoryMetal", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "gold-src"}},
			map[string]domain.RateSource{
				"gold-src": {BaseCurrency: "GOLD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		assert.Equal(t, ratepair.CategoryMetal, result.Pairs[0].Category)
	})

	t.Run("fiat base row has CategoryFiat", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "usd-src"}},
			map[string]domain.RateSource{
				"usd-src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		assert.Equal(t, ratepair.CategoryFiat, result.Pairs[0].Category)
	})

	t.Run("bucket downsampling: last value in each bucket wins", func(t *testing.T) {
		t.Parallel()
		since := fixedNow.Add(-ratepair.ChartWindow)
		step := ratepair.ChartWindow / 12
		ts0a := since.Add(step / 4)
		ts0b := since.Add(step * 3 / 4)

		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "src"}},
			map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			[]domain.RateValue{
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 100, Timestamp: ts0a},
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 200, Timestamp: ts0b},
			},
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		require.Len(t, result.Pairs[0].Series, 1)
		sr := result.Pairs[0].Series[0]
		assert.False(t, sr.Sparse)
		assert.Len(t, sr.Points, 12)
		require.NotEmpty(t, sr.Points)
		assert.Equal(t, 200.0, sr.Points[0].Value)
		for i, pt := range sr.Points {
			assert.Equal(t, 200.0, pt.Value, "bucket %d must be forward-filled to 200", i)
		}
	})

	t.Run("leading empty buckets before first sample are omitted", func(t *testing.T) {
		t.Parallel()
		since := fixedNow.Add(-ratepair.ChartWindow)
		step := ratepair.ChartWindow / 12
		ts3 := since.Add(3*step + step/2)
		ts4 := since.Add(4*step + step/2)

		svc := newService(
			[]domain.RateUserSubscription{{SourceName: "src"}},
			map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			[]domain.RateValue{
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 300, Timestamp: ts3},
				{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 350, Timestamp: ts4},
			},
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		require.Len(t, result.Pairs[0].Series, 1)
		sr := result.Pairs[0].Series[0]
		assert.False(t, sr.Sparse)
		assert.Len(t, sr.Points, 9)
		assert.Equal(t, 300.0, sr.Points[0].Value)
		assert.Equal(t, 350.0, sr.Points[1].Value)
		for i := 2; i < len(sr.Points); i++ {
			assert.Equal(t, 350.0, sr.Points[i].Value, "bucket %d must be forward-filled to 350", i+3)
		}
	})

	t.Run("multiple pairs are sorted fiat before metal", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{
				{SourceName: "gold-src"},
				{SourceName: "usd-src"},
			},
			map[string]domain.RateSource{
				"gold-src": {BaseCurrency: "GOLD", QuoteCurrency: "KZT", Kind: "BID"},
				"usd-src":  {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 2)
		assert.Contains(t, result.Pairs[0].Pair, "USD", "fiat (USD) must come before metal (GOLD)")
		assert.Contains(t, result.Pairs[1].Pair, "GOLD")
	})

	t.Run("two different fiat pairs produce two rows", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{
				{SourceName: "usd-src"},
				{SourceName: "eur-src"},
			},
			map[string]domain.RateSource{
				"usd-src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
				"eur-src": {BaseCurrency: "EUR", QuoteCurrency: "KZT", Kind: "BID"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		assert.Len(t, result.Pairs, 2)
	})

	t.Run("USD/KZT BID and KZT/USD ASK stay in separate rows", func(t *testing.T) {
		t.Parallel()
		// Two subscriptions with opposite storage directions: USD/KZT BID
		// (price ≈487) and KZT/USD ASK (price ≈0.00205). They share the same
		// underlying pair economically but the scales are incommensurable, so
		// they must NOT be merged into a single row.
		svc := newService(
			[]domain.RateUserSubscription{
				{SourceName: "usd-kzt-bid"},
				{SourceName: "kzt-usd-ask"},
			},
			map[string]domain.RateSource{
				"usd-kzt-bid": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
				"kzt-usd-ask": {BaseCurrency: "KZT", QuoteCurrency: "USD", Kind: "ASK"},
			},
			nil,
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 2, "opposite storage directions must produce separate rows")
		// Each row has exactly one series.
		assert.Len(t, result.Pairs[0].Series, 1)
		assert.Len(t, result.Pairs[1].Series, 1)
		// Neither row has a spread: each has only a single series.
		assert.Nil(t, result.Pairs[0].SpreadPct)
		assert.Nil(t, result.Pairs[1].SpreadPct)
	})

	t.Run("spread is nil when ASK latest is zero", func(t *testing.T) {
		t.Parallel()
		svc := newService(
			[]domain.RateUserSubscription{
				{SourceName: "src-bid"},
				{SourceName: "src-ask"},
			},
			map[string]domain.RateSource{
				"src-bid": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
				"src-ask": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "ASK"},
			},
			// Only BID has values → ASK.Latest=0; spread must be nil.
			[]domain.RateValue{
				{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Price: 487.55, Timestamp: fixedNow.Add(-time.Hour)},
			},
		)
		result, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		require.Len(t, result.Pairs, 1)
		assert.Nil(t, result.Pairs[0].SpreadPct, "SpreadPct must be nil when ASK latest is zero")
	})
}

var errFake = assert.AnError

// captureValues is a ValuesLoader that records the since argument passed to it
// so tests can assert the correct time window is forwarded from the service.
type captureValues struct {
	capturedSince time.Time
	values        []domain.RateValue
	err           error
}

var _ chart.ValuesLoader = (*captureValues)(nil)

func (c *captureValues) ObtainValuesForPairsSince(_ context.Context, _ []domain.SourcePairKey, since time.Time) ([]domain.RateValue, error) {
	c.capturedSince = since
	return c.values, c.err
}

func newPublicService(keys []domain.SourcePairKey, values []domain.RateValue) *chart.Service {
	return chart.NewService(
		&fakeSubs{},
		&fakeSources{},
		&fakeValues{values: values},
		&fakeHistoryValues{},
		&fakePublicSources{keys: keys},
		func() time.Time { return fixedNow },
	)
}

func TestService_ObtainMeChartForPeriod(t *testing.T) {
	t.Parallel()

	t.Run("since equals now minus period*24h for period=7", func(t *testing.T) {
		t.Parallel()
		cap := &captureValues{}
		svc := chart.NewService(
			&fakeSubs{subs: []domain.RateUserSubscription{{SourceName: "src"}}},
			&fakeSources{sources: map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			}},
			cap,
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		_, err := svc.ObtainMeChartForPeriod(t.Context(), "u", 7)
		require.NoError(t, err)
		expected := fixedNow.Add(-7 * 24 * time.Hour)
		assert.Equal(t, expected, cap.capturedSince, "since must be now - 7*24h")
	})

	t.Run("since equals now minus period*24h for period=90", func(t *testing.T) {
		t.Parallel()
		cap := &captureValues{}
		svc := chart.NewService(
			&fakeSubs{subs: []domain.RateUserSubscription{{SourceName: "src"}}},
			&fakeSources{sources: map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			}},
			cap,
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		_, err := svc.ObtainMeChartForPeriod(t.Context(), "u", 90)
		require.NoError(t, err)
		expected := fixedNow.Add(-90 * 24 * time.Hour)
		assert.Equal(t, expected, cap.capturedSince, "since must be now - 90*24h")
	})

	t.Run("ObtainMeChart wrapper delegates with period=7", func(t *testing.T) {
		t.Parallel()
		cap := &captureValues{}
		svc := chart.NewService(
			&fakeSubs{subs: []domain.RateUserSubscription{{SourceName: "src"}}},
			&fakeSources{sources: map[string]domain.RateSource{
				"src": {BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: "BID"},
			}},
			cap,
			&fakeHistoryValues{},
			&fakePublicSources{},
			func() time.Time { return fixedNow },
		)
		_, err := svc.ObtainMeChart(t.Context(), "u")
		require.NoError(t, err)
		expected := fixedNow.Add(-7 * 24 * time.Hour)
		assert.Equal(t, expected, cap.capturedSince, "ObtainMeChart must pass since = now - 7*24h")
	})
}

func TestService_ObtainPublicChartForPeriod(t *testing.T) {
	t.Parallel()

	t.Run("since equals now minus period*24h for period=30", func(t *testing.T) {
		t.Parallel()
		cap := &captureValues{}
		keys := []domain.SourcePairKey{
			{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
		}
		svc := chart.NewService(
			&fakeSubs{},
			&fakeSources{},
			cap,
			&fakeHistoryValues{},
			&fakePublicSources{keys: keys},
			func() time.Time { return fixedNow },
		)
		_, _, err := svc.ObtainPublicChartForPeriod(t.Context(), 1, 20, 30)
		require.NoError(t, err)
		expected := fixedNow.Add(-30 * 24 * time.Hour)
		assert.Equal(t, expected, cap.capturedSince, "since must be now - 30*24h")
	})

	t.Run("since equals now minus period*24h for period=7", func(t *testing.T) {
		t.Parallel()
		cap := &captureValues{}
		keys := []domain.SourcePairKey{
			{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
		}
		svc := chart.NewService(
			&fakeSubs{},
			&fakeSources{},
			cap,
			&fakeHistoryValues{},
			&fakePublicSources{keys: keys},
			func() time.Time { return fixedNow },
		)
		_, _, err := svc.ObtainPublicChartForPeriod(t.Context(), 1, 20, 7)
		require.NoError(t, err)
		expected := fixedNow.Add(-7 * 24 * time.Hour)
		assert.Equal(t, expected, cap.capturedSince, "since must be now - 7*24h")
	})

	t.Run("ObtainPublicChart wrapper delegates with period=7", func(t *testing.T) {
		t.Parallel()
		cap := &captureValues{}
		keys := []domain.SourcePairKey{
			{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
		}
		svc := chart.NewService(
			&fakeSubs{},
			&fakeSources{},
			cap,
			&fakeHistoryValues{},
			&fakePublicSources{keys: keys},
			func() time.Time { return fixedNow },
		)
		_, _, err := svc.ObtainPublicChart(t.Context(), 1, 20)
		require.NoError(t, err)
		expected := fixedNow.Add(-7 * 24 * time.Hour)
		assert.Equal(t, expected, cap.capturedSince, "ObtainPublicChart must pass since = now - 7*24h")
	})
}

func TestService_ObtainPublicChart(t *testing.T) {
	t.Parallel()

	t.Run("empty system returns zero pairs", func(t *testing.T) {
		t.Parallel()
		svc := newPublicService(nil, nil)
		ch, total, err := svc.ObtainPublicChart(t.Context(), 1, 20)
		require.NoError(t, err)
		require.NotNil(t, ch)
		assert.Empty(t, ch.Pairs)
		assert.EqualValues(t, 0, total)
	})

	t.Run("single pair returns one row", func(t *testing.T) {
		t.Parallel()
		keys := []domain.SourcePairKey{
			{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
		}
		svc := newPublicService(keys, nil)
		ch, total, err := svc.ObtainPublicChart(t.Context(), 1, 20)
		require.NoError(t, err)
		require.NotNil(t, ch)
		require.Len(t, ch.Pairs, 1)
		assert.EqualValues(t, 1, total)
		assert.Equal(t, "USD/KZT", ch.Pairs[0].Pair)
	})

	t.Run("pagination slices correctly", func(t *testing.T) {
		t.Parallel()
		// 3 distinct pairs, page size 1, page 2 → returns second pair.
		keys := []domain.SourcePairKey{
			{SourceName: "src-eur", BaseCurrency: "EUR", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
			{SourceName: "src-usd", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
			{SourceName: "src-rub", BaseCurrency: "RUB", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
		}
		svc := newPublicService(keys, nil)
		ch, total, err := svc.ObtainPublicChart(t.Context(), 2, 1)
		require.NoError(t, err)
		require.NotNil(t, ch)
		require.Len(t, ch.Pairs, 1, "page 2 with limit 1 must return exactly one row")
		assert.EqualValues(t, 3, total, "total must be the unpaginated count")
	})

	t.Run("ordering is deterministic across pages", func(t *testing.T) {
		t.Parallel()
		keys := []domain.SourcePairKey{
			{SourceName: "src-usd", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
			{SourceName: "src-eur", BaseCurrency: "EUR", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
		}
		svc := newPublicService(keys, nil)

		// Page 1 + page 2 (limit 1) must produce non-overlapping, consistently ordered results.
		ch1, _, err := svc.ObtainPublicChart(t.Context(), 1, 1)
		require.NoError(t, err)
		ch2, _, err := svc.ObtainPublicChart(t.Context(), 2, 1)
		require.NoError(t, err)
		require.Len(t, ch1.Pairs, 1)
		require.Len(t, ch2.Pairs, 1)
		assert.NotEqual(t, ch1.Pairs[0].Pair, ch2.Pairs[0].Pair, "page 1 and page 2 must not return the same pair")
	})

	t.Run("total is unpaginated count", func(t *testing.T) {
		t.Parallel()
		// BID + ASK for the same pair collapse to ONE row, so total = 1 not 2.
		keys := []domain.SourcePairKey{
			{SourceName: "src-bid", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
			{SourceName: "src-ask", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindASK},
		}
		svc := newPublicService(keys, nil)
		_, total, err := svc.ObtainPublicChart(t.Context(), 1, 20)
		require.NoError(t, err)
		assert.EqualValues(t, 1, total, "BID+ASK for the same pair must collapse to one row in the total")
	})

	t.Run("public sources loader error propagates", func(t *testing.T) {
		t.Parallel()
		svc := chart.NewService(
			&fakeSubs{},
			&fakeSources{},
			&fakeValues{},
			&fakeHistoryValues{},
			&fakePublicSources{err: errFake},
			func() time.Time { return fixedNow },
		)
		_, _, err := svc.ObtainPublicChart(t.Context(), 1, 20)
		require.Error(t, err)
	})

	t.Run("out-of-range page returns empty pairs but correct total", func(t *testing.T) {
		t.Parallel()
		keys := []domain.SourcePairKey{
			{SourceName: "src", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID},
		}
		svc := newPublicService(keys, nil)
		ch, total, err := svc.ObtainPublicChart(t.Context(), 999, 20)
		require.NoError(t, err)
		require.NotNil(t, ch)
		assert.Empty(t, ch.Pairs)
		assert.EqualValues(t, 1, total)
	})
}
