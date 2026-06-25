package apiclient_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

func TestClient_PublicRatesChart(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes paginated chart response", func(t *testing.T) {
		t.Parallel()
		body := `{"window":"7 days","page":1,"limit":20,"total":2,"pairs":[{"pair":"USD/KZT","category":"fiat","series":[{"kind":"BID","color":"#1D9E75","latest":449.5,"delta_pct":0.1,"sparse":false}]},{"pair":"EUR/KZT","category":"fiat","series":[{"kind":"BID","color":"#1D9E75","latest":530.0,"delta_pct":-0.2,"sparse":false}]}]}`
		f := &fakeFetcher{jsonResponse: []byte(body)}
		c := apiclient.New(f)
		got, err := c.PublicRatesChart(t.Context(), 1, 20, 7)
		require.NoError(t, err)
		assert.Equal(t, "7 days", got.Window)
		assert.Equal(t, 1, got.Page)
		assert.Equal(t, 20, got.Limit)
		assert.Equal(t, int64(2), got.Total)
		require.Len(t, got.Pairs, 2)
		assert.Equal(t, "USD/KZT", got.Pairs[0].Pair)
		assert.Equal(t, "EUR/KZT", got.Pairs[1].Pair)
	})

	t.Run("correct URL is constructed with page, limit, and period", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`{"window":"7 days","page":2,"limit":5,"total":0,"pairs":[]}`)}
		c := apiclient.New(f)
		_, err := c.PublicRatesChart(t.Context(), 2, 5, 7)
		require.NoError(t, err)
		assert.Equal(t, "GET", f.lastMethod)
		assert.Contains(t, f.lastURL, "/api/public/rates/chart")
		assert.Contains(t, f.lastURL, "page=2")
		assert.Contains(t, f.lastURL, "limit=5")
		assert.Contains(t, f.lastURL, "period=7")
	})

	t.Run("period=30 is forwarded in URL query string", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`{"window":"30 days","page":1,"limit":20,"total":0,"pairs":[]}`)}
		c := apiclient.New(f)
		got, err := c.PublicRatesChart(t.Context(), 1, 20, 30)
		require.NoError(t, err)
		assert.Equal(t, "30 days", got.Window)
		assert.Contains(t, f.lastURL, "period=30", "period must appear in the request URL")
	})

	t.Run("no auth header is sent", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`{"window":"7 days","page":1,"limit":20,"total":0,"pairs":[]}`)}
		c := apiclient.New(f)
		_, err := c.PublicRatesChart(t.Context(), 1, 20, 7)
		require.NoError(t, err)
		assert.Nil(t, f.lastHeaders, "public endpoint must send no auth headers")
	})

	t.Run("empty pairs returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`{"window":"7 days","page":1,"limit":20,"total":0,"pairs":[]}`)}
		c := apiclient.New(f)
		got, err := c.PublicRatesChart(t.Context(), 1, 20, 7)
		require.NoError(t, err)
		assert.NotNil(t, got.Pairs)
		assert.Empty(t, got.Pairs)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 503")}
		c := apiclient.New(f)
		_, err := c.PublicRatesChart(t.Context(), 1, 20, 7)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 503")
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.PublicRatesChart(t.Context(), 1, 20, 7)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode public rates chart")
	})

	t.Run("pairs field contains spread_pct when present", func(t *testing.T) {
		t.Parallel()
		body := `{"window":"7 days","page":1,"limit":20,"total":1,"pairs":[{"pair":"USD/KZT","category":"fiat","spread_pct":0.29,"series":[{"kind":"BID","color":"#1D9E75","latest":449.5,"delta_pct":0.1,"sparse":false},{"kind":"ASK","color":"#378ADD","latest":450.0,"delta_pct":0.0,"sparse":false}]}]}`
		f := &fakeFetcher{jsonResponse: []byte(body)}
		c := apiclient.New(f)
		got, err := c.PublicRatesChart(t.Context(), 1, 20, 7)
		require.NoError(t, err)
		require.Len(t, got.Pairs, 1)
		require.NotNil(t, got.Pairs[0].SpreadPct)
		assert.InDelta(t, 0.29, *got.Pairs[0].SpreadPct, 0.001)
	})

	t.Run("returned type is dto.PublicChartResponse not MeChartResponse", func(t *testing.T) {
		t.Parallel()
		// The returned struct must have the pagination fields Page/Limit/Total that
		// dto.MeChartResponse lacks; returning MeChartResponse would fail to compile.
		f := &fakeFetcher{jsonResponse: []byte(`{"window":"7 days","page":3,"limit":10,"total":30,"pairs":[]}`)}
		c := apiclient.New(f)
		got, err := c.PublicRatesChart(t.Context(), 3, 10, 7)
		require.NoError(t, err)
		var _ dto.PublicChartResponse = got // compile-time check on the return type
		assert.Equal(t, 3, got.Page)
		assert.Equal(t, 10, got.Limit)
		assert.Equal(t, int64(30), got.Total)
	})
}
