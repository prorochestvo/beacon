package migrations_test

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/tools/rateextractor"
	"github.com/seilbekskindirov/beacon/migrations"
)

// halykMigrationName is the migration whose rules this file guards. The rules are data, not
// code, so nothing else compiles them against the payload they are meant for.
const halykMigrationName = "202611.037.rate_sources.halyk_tier_bound.sql"

// halykRuleStatement pulls the rules and the source name out of each of the migration's UPDATEs.
var halykRuleStatement = regexp.MustCompile(`SET rules = '(.*?)'\s+WHERE name = '(KZ_HALYK_(?:ASK|BID)_USD_KZT)'`)

func TestHalykRules_ExtractFirstTierRates(t *testing.T) {
	t.Parallel()

	content, err := migrations.MigrationsFS.ReadFile(halykMigrationName)
	require.NoError(t, err)
	statements := halykRuleStatement.FindAllStringSubmatch(string(content), -1)
	require.Len(t, statements, 2)

	patterns := make(map[string]string, len(statements))
	for _, statement := range statements {
		var rules []domain.RateSourceRule
		err = json.Unmarshal([]byte(statement[1]), &rules)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		require.Equal(t, domain.MethodRegex, rules[0].Method)
		patterns[statement[2]] = rules[0].Pattern
	}
	require.Contains(t, patterns, "KZ_HALYK_ASK_USD_KZT")
	require.Contains(t, patterns, "KZ_HALYK_BID_USD_KZT")

	// The live shape as of 2026-11-09: the first tier ends at 30000, and the later tiers carry
	// different sell rates, so a match past the first tier would show up as a wrong value.
	current := []byte(`{"result":true,"data":{"currencyHistory":[{"date":"2026-11-09","branches":{"030102":{` +
		`"USD\/KZT":{"value_from_1":0,"value_to_1":30000,"sell_rate_1":451.6,"buy_rate_1":444.6,` +
		`"value_from_2":30000,"value_to_2":100000,"sell_rate_2":450.1,"buy_rate_2":444.7,` +
		`"value_from_3":100000,"value_to_3":null,"sell_rate_3":449.8,"buy_rate_3":444.8},` +
		`"GBP\/KZT":{"value_from_1":0,"value_to_1":1000,"sell_rate_1":610.5,"buy_rate_1":590.5}}}}]}}`)
	// The shape the seeded rules were written against.
	seeded := []byte(`{"USD\/KZT":{"value_from_1":0,"value_to_1":10000,"sell_rate_1":470.1,"buy_rate_1":465.2}}`)
	// A single-tier grade with no upper bound.
	unbounded := []byte(`{"USD\/KZT":{"value_from_1":0,"value_to_1":null,"sell_rate_1":452.0,"buy_rate_1":445.0}}`)

	tests := []struct {
		name    string
		source  string
		payload []byte
		want    float64
	}{
		{name: "ask from the current shape", source: "KZ_HALYK_ASK_USD_KZT", payload: current, want: 451.6},
		{name: "bid from the current shape", source: "KZ_HALYK_BID_USD_KZT", payload: current, want: 444.6},
		{name: "ask from the seeded shape", source: "KZ_HALYK_ASK_USD_KZT", payload: seeded, want: 470.1},
		{name: "bid from the seeded shape", source: "KZ_HALYK_BID_USD_KZT", payload: seeded, want: 465.2},
		{name: "ask with a null bound", source: "KZ_HALYK_ASK_USD_KZT", payload: unbounded, want: 452.0},
		{name: "bid with a null bound", source: "KZ_HALYK_BID_USD_KZT", payload: unbounded, want: 445.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := rateextractor.ApplyRegex(patterns[tt.source], tt.payload)
			require.NoError(t, err)
			rate, err := strconv.ParseFloat(string(got), 64)
			require.NoError(t, err)
			assert.InDelta(
				t,
				tt.want,
				rate,
				1e-9,
			)
		})
	}

	t.Run("a tier that does not start at zero misses", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"USD\/KZT":{"value_from_1":1000,"value_to_1":30000,"sell_rate_1":451.6,"buy_rate_1":444.6}}`)
		for source, pattern := range patterns {
			_, err := rateextractor.ApplyRegex(pattern, payload)
			require.Error(t, err, source)
		}
	})
}
