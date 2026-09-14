package migrations_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/tools/rateextractor"
	"github.com/seilbekskindirov/beacon/migrations"
)

// bccRulesMigrationName is the migration whose rules this file guards. The rules are data,
// not code, so nothing else compiles them: without this test a pattern that matches
// nothing reaches production and shows up as sources that quietly stop producing values,
// which is exactly how the rules it replaced were lost for three weeks.
const bccRulesMigrationName = "202609.035.rate_sources.bcc_data_fx_rules.sql"

// bccRuleStatement pulls the (rules, source name) pair out of each UPDATE in that file.
var bccRuleStatement = regexp.MustCompile(`SET rules = '(.*?)' WHERE name = '([A-Z_]+)';`)

// bccJSONCode maps the currency token in a source name to the code the page's JSON uses for
// it. The metals are the only two that differ: the strings GOLD and SILVER live in
// currencyMainName, which sits after the prices in each object and so cannot anchor a
// forward-only match.
var bccJSONCode = map[string]string{
	"USD": "USD", "EUR": "EUR", "RUB": "RUB", "GBP": "GBP", "AED": "AED", "CAD": "CAD",
	"CHF": "CHF", "TRY": "TRY", "UZS": "UZS", "JPY": "JPY", "GOLD": "XAU", "SILVER": "XAG",
}

// bccQuotes are the fixture's prices, keyed by JSON code. UZS carries its real five
// significant digits: it is the pair that says whether a rounding step crept back into the
// pipeline.
var bccQuotes = map[string][2]string{
	"USD": {"449.35", "450.95"},
	"EUR": {"519.75", "521.89"},
	"RUB": {"5.3089", "5.4026"},
	"GBP": {"606.74", "609.25"},
	"XAU": {"62412.66", "63097.82"},
	"AED": {"122.17", "123.03"},
	"XAG": {"908.83", "941.75"},
	"CAD": {"323.48", "325.48"},
	"CHF": {"548.82", "551.5"},
	"TRY": {"9.1756", "9.3413"},
	"UZS": {"0.03367", "0.0419"},
	"JPY": {"2.869", "2.9794"},
	"CNY": {"66.7558", "67.4622"},
}

func TestBCCFXRules_ExtractFromDataFXAttribute(t *testing.T) {
	t.Parallel()

	rules := readBCCRules(t)
	require.Len(t, rules, 24, "the migration must cover all 24 KZ_BCC_FX_* sources")

	page := bccPage(bccOrder(), "")

	for name, raw := range rules {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var parsed []domain.RateSourceRule
			require.NoError(t, json.Unmarshal([]byte(raw), &parsed))

			// One regex rule and nothing after it. A parse_float step would reformat the
			// capture with %.3f, which turns the UZS quote 0.03367 into 0.034; JSON numbers
			// need none of the separator normalisation that step exists for.
			require.Len(t, parsed, 1)
			require.Equal(t, domain.MethodRegex, parsed[0].Method)

			got, err := rateextractor.ApplyRegex(parsed[0].Pattern, page)
			require.NoError(t, err)
			assert.Equal(t, bccExpected(t, name), string(got))
		})
	}
}

func TestBCCFXRules_DoNotReadTheNeighbouringPair(t *testing.T) {
	t.Parallel()

	rules := readBCCRules(t)

	// XAU quotes without a sell key. The bound on the pattern is 600 characters and the
	// closest two pair objects in the live document are 1232 apart, so the BID rule must
	// fail rather than report the next currency's price as the price of gold.
	page := bccPage(bccOrder(), "XAU")

	_, err := applyRuleOf(t, rules, "KZ_BCC_FX_BID_GOLD_KZT", page)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "produced no match")

	// The ASK side of the same pair is untouched by the omission and still reads its own buy.
	got, err := applyRuleOf(t, rules, "KZ_BCC_FX_ASK_GOLD_KZT", page)
	require.NoError(t, err)
	assert.Equal(t, bccQuotes["XAU"][1], string(got))
}

// applyRuleOf runs the named source's single regex rule against page.
func applyRuleOf(t *testing.T, rules map[string]string, name string, page []byte) ([]byte, error) {
	t.Helper()

	raw, ok := rules[name]
	require.True(t, ok, "migration carries no rule for %s", name)

	var parsed []domain.RateSourceRule
	require.NoError(t, json.Unmarshal([]byte(raw), &parsed))
	require.Len(t, parsed, 1)

	return rateextractor.ApplyRegex(parsed[0].Pattern, page)
}

// readBCCRules returns the migration's rules JSON keyed by source name.
func readBCCRules(t *testing.T) map[string]string {
	t.Helper()

	content, err := migrations.MigrationsFS.ReadFile(bccRulesMigrationName)
	require.NoError(t, err)

	rules := make(map[string]string)
	for _, m := range bccRuleStatement.FindAllStringSubmatch(string(content), -1) {
		rules[m[2]] = m[1]
	}

	return rules
}

// bccExpected returns the quote a source name must extract: sell for BID, buy for ASK. The
// field names are the customer's rather than the bank's, so sell is the lower of the two.
func bccExpected(t *testing.T, name string) string {
	t.Helper()

	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, "KZ_BCC_FX_"), "_KZT"), "_")
	require.Len(t, parts, 2, "unexpected source name %q", name)

	code, ok := bccJSONCode[parts[1]]
	require.True(t, ok, "no JSON code for currency %q", parts[1])

	if parts[0] == "BID" {
		return bccQuotes[code][0]
	}

	return bccQuotes[code][1]
}

// bccOrder is the order the codes appear in on the live page. Adjacency is what the bound on
// each pattern is measured against, so the fixture keeps it.
func bccOrder() []string {
	return []string{"USD", "EUR", "RUB", "GBP", "XAU", "CNY", "AED", "XAG", "CAD", "CHF", "TRY", "UZS", "JPY"}
}

// bccPage renders a page shaped like bcc.kz/en/personal/currency-rates/: the quotes as an
// HTML-entity-escaped JSON document inside an Alpine.js attribute, with the key order and
// the per-object length of the real payload. The pair named by omitSell is rendered without
// its sell key.
//
// CNY is in the fixture and has no source of ours; it is what makes the objects around the
// metals neighbours rather than the first or last thing in the document.
func bccPage(codes []string, omitSell string) []byte {
	objects := make([]string, 0, len(codes))
	for i, code := range codes {
		objects = append(objects, fmt.Sprintf("&quot;%d&quot;:{%s}", i, bccPairObject(code, omitSell == code)))
	}

	return []byte(`<main><div class="text-lg">Currency rates</div><div x-ref="fx" data-fx='{` +
		strings.Join(objects, ",") + `}'></div></main>`)
}

// bccPairObject renders one pair the way the page escapes it. Three neighbours of the
// captured value are deliberately present: currencyBuy ahead of the anchor, and sellNet /
// buyNet behind the prices — all three would be captured by a looser pattern.
func bccPairObject(code string, omitSell bool) string {
	quotes := bccQuotes[code]
	prices := `&quot;sell&quot;:` + quotes[0] + `,&quot;buy&quot;:` + quotes[1]
	if omitSell {
		prices = `&quot;buy&quot;:` + quotes[1]
	}

	return fmt.Sprintf(
		`&quot;currencyPairId&quot;:14,&quot;currencySell&quot;:&quot;%[1]s&quot;,`+
			`&quot;currencyBuy&quot;:&quot;KZT&quot;,&quot;currencyMain&quot;:&quot;%[1]s&quot;,`+
			`&quot;currencyMinor&quot;:&quot;KZT&quot;,&quot;currencyName&quot;:&quot;АҚШ доллары&quot;,`+
			`&quot;pictureUrlMain&quot;:&quot;api\/v1\/fx\/images\/flags\/%[2]s.png&quot;,`+
			`&quot;pictureUrlMinor&quot;:&quot;api\/v1\/fx\/images\/flags\/kzt.png&quot;,`+
			`%[3]s,&quot;lastUpdateDateTime&quot;:&quot;14.09.2026 09:05:14&quot;,`+
			`&quot;currencyMainName&quot;:&quot;%[1]s&quot;,&quot;currencyMinorName&quot;:&quot;KZT&quot;,`+
			`&quot;stopTomLossSell&quot;:null,&quot;stopTomLossBuy&quot;:null,`+
			`&quot;stopSpotLossSell&quot;:null,&quot;stopSpotLossBuy&quot;:null,`+
			`&quot;maxAmount&quot;:14000,&quot;minAmount&quot;:1,&quot;ricRound&quot;:2,`+
			`&quot;amountRound&quot;:2,&quot;functionalType&quot;:&quot;ON_THR&quot;,`+
			`&quot;currencyMainSymbol&quot;:&quot;гр&quot;,&quot;currencyMinorSymbol&quot;:&quot;₸&quot;,`+
			`&quot;sellNet&quot;:1.11,&quot;buyNet&quot;:2.22,&quot;orderQtyRound&quot;:3,`+
			`&quot;quoteOriginalNetRound&quot;:2,&quot;isAvailable24x5&quot;:false,`+
			`&quot;isAvailableUntil&quot;:false,&quot;availableUntilTime&quot;:null`,
		code, strings.ToLower(code), prices,
	)
}
