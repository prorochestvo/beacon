package sourceaudit

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourcesFromRateSources(t *testing.T) {
	t.Parallel()

	rows := []domain.RateSource{
		{Name: "z-active", Title: "Z vendor", BaseCurrency: "USD", QuoteCurrency: "KZT", URL: "https://z.example", Interval: "1h", Kind: domain.RateSourceKindBID, Active: true, FetcherKind: "plain", Options: domain.RateSourceOptions{Headers: map[string]string{"X-Test": "value"}}, Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: "([0-9]+)"}}},
		{Name: "inactive", Active: false, FetcherKind: "plain"},
		{Name: "chromedp", Active: true, FetcherKind: "chromedp"},
		{Name: "a-legacy", Title: "A vendor", Active: true},
	}

	sources, skipped, inactive := SourcesFromRateSources(rows)
	require.Len(t, sources, 2)
	assert.Equal(t, []string{"a-legacy", "z-active"}, []string{sources[0].Name, sources[1].Name})
	assert.Equal(t, SeededSource{
		Name: "z-active", Vendor: "Z vendor", Base: "USD", Quote: "KZT", URL: "https://z.example",
		Interval: "1h", Side: "BID", Active: true, Rules: rows[0].Rules,
		Headers: map[string]string{"X-Test": "value"}, Origin: "rate_sources",
	}, sources[1])
	assert.Equal(t, 1, inactive)
	require.Len(t, skipped, 1)
	assert.Equal(t, "chromedp", skipped[0].Name)
}
