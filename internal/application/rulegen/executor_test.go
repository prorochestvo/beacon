package rulegen

import (
	"fmt"
	"math"
	"testing"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRuleExecutor_Execute(t *testing.T) {
	t.Parallel()

	ex := NewRuleExecutor()

	t.Run("regex single rule extracts value", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(\d+\.\d+)`},
		}
		v, err := ex.Execute(rules, []byte("foo 12.34 bar"), "", "")
		require.NoError(t, err)
		assert.InDelta(t, 12.34, v, 0.0001)
	})

	t.Run("chained regex rules in sequence", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `rate:\s*(\S+)`},
			{Method: domain.MethodRegex, Pattern: `(\d+\.\d+)`},
		}
		v, err := ex.Execute(rules, []byte("rate: 456.78 some extra"), "", "")
		require.NoError(t, err)
		assert.InDelta(t, 456.78, v, 0.0001)
	})

	t.Run("json path extracts numeric value", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodJSONPath, Pattern: "usd.sell"},
		}
		v, err := ex.Execute(rules, []byte(`{"usd":{"sell":"123.45"}}`), "", "")
		require.NoError(t, err)
		assert.InDelta(t, 123.45, v, 0.0001)
	})

	t.Run("comma-decimal locale is normalized", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `([0-9 ,]+)`},
		}
		v, err := ex.Execute(rules, []byte("1 234,56"), "", "")
		require.NoError(t, err)
		assert.InDelta(t, 1234.56, v, 0.0001)
	})

	t.Run("empty rules returns error", func(t *testing.T) {
		t.Parallel()
		_, err := ex.Execute(nil, []byte("123"), "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no rules")
	})

	t.Run("unsupported method parse_float returns error", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodParseFloat, Pattern: ""},
		}
		_, err := ex.Execute(rules, []byte("123.45"), "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported method")
	})

	t.Run("unsupported method store_as_rate returns error", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodStoreToRate, Pattern: ""},
		}
		_, err := ex.Execute(rules, []byte("123.45"), "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported method")
	})

	t.Run("value below floor returns error", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(-[\d.]+)`},
		}
		_, err := ex.Execute(rules, []byte("-1.5"), "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside plausible range")
	})

	t.Run("zero value rejected", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(0\.0)`},
		}
		_, err := ex.Execute(rules, []byte("0.0"), "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside plausible range")
	})

	t.Run("value above ceiling returns error", func(t *testing.T) {
		t.Parallel()
		body := []byte(fmt.Sprintf("%d", math.MaxInt32+1))
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(\d+)`},
		}
		_, err := ex.Execute(rules, body, "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside plausible range")
	})

	t.Run("NaN string returns error", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(NaN)`},
		}
		_, err := ex.Execute(rules, []byte("NaN"), "", "")
		require.Error(t, err)
	})

	t.Run("body slice is not mutated by caller", func(t *testing.T) {
		t.Parallel()
		body := []byte("price: 100.00 USD")
		original := make([]byte, len(body))
		copy(original, body)
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(\d+\.\d+)`},
		}
		_, err := ex.Execute(rules, body, "", "")
		require.NoError(t, err)
		assert.Equal(t, original, body, "Execute must not mutate the caller's body slice")
	})

	t.Run("USD/KZT value 19.1671 is rejected with per-pair error", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(19\.1671)`},
		}
		_, err := ex.Execute(rules, []byte("rate 19.1671 end"), "USD", "KZT")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside plausible range")
		assert.Contains(t, err.Error(), "100")
		assert.Contains(t, err.Error(), "1000")
		assert.Contains(t, err.Error(), "USD/KZT")
	})

	t.Run("USD/KZT value 470.0 is accepted", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(470\.0)`},
		}
		v, err := ex.Execute(rules, []byte("rate 470.0 end"), "USD", "KZT")
		require.NoError(t, err)
		assert.InDelta(t, 470.0, v, 0.0001)
	})

	t.Run("unknown pair USD/XYZ falls back to universal range and accepts 470", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(470\.0)`},
		}
		v, err := ex.Execute(rules, []byte("rate 470.0 end"), "USD", "XYZ")
		require.NoError(t, err)
		assert.InDelta(t, 470.0, v, 0.0001)
	})

	t.Run("empty base/quote falls back to universal range", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(470\.0)`},
		}
		v, err := ex.Execute(rules, []byte("rate 470.0 end"), "", "")
		require.NoError(t, err)
		assert.InDelta(t, 470.0, v, 0.0001)

		// Universal range only excludes <=0 and >MaxInt32; 19.1671 passes.
		rules2 := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(19\.1671)`},
		}
		v2, err2 := ex.Execute(rules2, []byte("rate 19.1671 end"), "", "")
		require.NoError(t, err2)
		assert.InDelta(t, 19.1671, v2, 0.0001)
	})
}
