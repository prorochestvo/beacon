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

// kaseCCBNMigrationName is the migration whose rule this file guards. The rule is data, not
// code, so nothing else compiles it against the payload it is meant for.
const kaseCCBNMigrationName = "202611.036.rate_sources.kase_ccbn_deals_api.sql"

// kaseCCBNRuleStatement pulls the new url, the rules and the guarding old url out of the
// migration's single UPDATE.
var kaseCCBNRuleStatement = regexp.MustCompile(`SET url = '([^']*)', rules = '(.*?)', rule_metadata = '\{\}'\s+WHERE name = 'KZ_KASE_LAST_CCBN_KZT' AND url = '([^']*)';`)

func TestKASECCBNRule_ExtractsNewestDealPrice(t *testing.T) {
	t.Parallel()

	content, err := migrations.MigrationsFS.ReadFile(kaseCCBNMigrationName)
	require.NoError(t, err)
	statement := kaseCCBNRuleStatement.FindStringSubmatch(string(content))
	require.Len(t, statement, 4)
	assert.Equal(t, "https://kase.kz/api/instruments/last-deals/CCBN", statement[1])
	assert.Equal(t, "https://kase.kz/en/investors/shares/CCBN", statement[3])

	var rules []domain.RateSourceRule
	err = json.Unmarshal([]byte(statement[2]), &rules)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, domain.MethodRegex, rules[0].Method)
	pattern := rules[0].Pattern

	t.Run("the newest deal's price", func(t *testing.T) {
		t.Parallel()
		// dirty_price and the older deal differ from the newest price, so a match on either
		// would show up as a wrong value.
		fixture := []byte(`[{
			"deal_date":"2026-11-03T17:29:48","volume":10,"volume_kzt":54610.0,"volume_usd":100.0,
			"price":5461.0,"price_trend":0.0,"dirty_price":5512.0,"dirty_price_trend":0.0,
			"income":0.0,"income_diff":null
		},{
			"deal_date":"2026-11-03T17:28:48","volume":5,"volume_kzt":27000.0,"volume_usd":50.0,
			"price":5400.0,"price_trend":-1.0,"dirty_price":5444.0,"dirty_price_trend":-1.0,
			"income":0.0,"income_diff":null
		}]`)

		got, err := rateextractor.ApplyRegex(pattern, fixture)
		require.NoError(t, err)
		price, err := strconv.ParseFloat(string(got), 64)
		require.NoError(t, err)
		assert.InDelta(
			t,
			5461.0,
			price,
			1e-9,
		)
	})

	t.Run("an empty deal list misses", func(t *testing.T) {
		t.Parallel()
		_, err := rateextractor.ApplyRegex(pattern, []byte(`[]`))
		require.Error(t, err)
	})

	t.Run("a null newest price misses rather than taking an older deal", func(t *testing.T) {
		t.Parallel()
		_, err := rateextractor.ApplyRegex(pattern, []byte(`[{"price":null,"dirty_price":5512.0},{"price":5400.0}]`))
		require.Error(t, err)
	})

	t.Run("exponent notation misses rather than truncating", func(t *testing.T) {
		t.Parallel()
		_, err := rateextractor.ApplyRegex(pattern, []byte(`[{"price":5.461e+03}]`))
		require.Error(t, err)
	})
}
