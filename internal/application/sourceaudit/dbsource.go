package sourceaudit

import (
	"sort"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// SourcesFromRateSources maps active database rows to sources supported by the
// plain HTTP auditor. Active rows using other fetchers are returned separately.
func SourcesFromRateSources(rows []domain.RateSource) (sources []SeededSource, skipped []domain.RateSource, inactive int) {
	for _, row := range rows {
		if !row.Active {
			inactive++
			continue
		}
		if row.FetcherKind != "" && row.FetcherKind != "plain" {
			skipped = append(skipped, row)
			continue
		}
		sources = append(sources, SeededSource{
			Name: row.Name, Vendor: row.Title, Base: row.BaseCurrency,
			Quote: row.QuoteCurrency, URL: row.URL, Interval: row.Interval,
			Side: string(row.Kind), Active: true, Rules: row.Rules,
			Headers: row.Options.Headers, Origin: "rate_sources",
		})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, skipped, inactive
}
