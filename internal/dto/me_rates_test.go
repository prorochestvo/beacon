package dto_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/internal/dto"
)

func TestMeChartPairRow_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("SpreadPct nil is omitted from JSON output", func(t *testing.T) {
		t.Parallel()
		row := dto.MeChartPairRow{
			Pair:      "USD/KZT",
			Category:  "fiat",
			SpreadPct: nil,
			Series:    []dto.MeChartSeries{},
		}
		b, err := json.Marshal(row)
		require.NoError(t, err)
		assert.NotContains(t, string(b), "spread_pct",
			"nil SpreadPct must be omitted from JSON output (omitempty)")
	})

	t.Run("SpreadPct non-nil round-trips correctly", func(t *testing.T) {
		t.Parallel()
		spread := 0.2937
		ts := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
		original := dto.MeChartPairRow{
			Pair:      "EUR/KZT",
			Category:  "fiat",
			SpreadPct: &spread,
			Series: []dto.MeChartSeries{
				{
					Kind:     "BID",
					Color:    "#1D9E75",
					Latest:   480.5,
					DeltaPct: 1.25,
					Sparse:   false,
					Points: []dto.MeChartPoint{
						{Timestamp: ts, Value: 478.0},
						{Timestamp: ts.Add(time.Hour), Value: 480.5},
					},
				},
			},
		}

		b, err := json.Marshal(original)
		require.NoError(t, err)

		var decoded dto.MeChartPairRow
		require.NoError(t, json.Unmarshal(b, &decoded))

		require.NotNil(t, decoded.SpreadPct, "SpreadPct must survive the round-trip")
		assert.InDelta(t, spread, *decoded.SpreadPct, 1e-9)
		assert.Equal(t, original.Pair, decoded.Pair)
		assert.Equal(t, original.Category, decoded.Category)
		require.Len(t, decoded.Series, 1)
		sr := decoded.Series[0]
		assert.Equal(t, "BID", sr.Kind)
		assert.InDelta(t, 480.5, sr.Latest, 1e-9)
		require.Len(t, sr.Points, 2)
		assert.Equal(t, ts.UTC(), sr.Points[0].Timestamp.UTC())
		assert.InDelta(t, 478.0, sr.Points[0].Value, 1e-9)
	})
}
