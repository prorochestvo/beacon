package rulegen

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitize(t *testing.T) {
	t.Parallel()

	t.Run("removes script blocks case-insensitively", func(t *testing.T) {
		t.Parallel()
		body := []byte(`<html><SCRIPT type="text/javascript">var x=1;</SCRIPT><p>rate</p></html>`)
		out, original, err := Sanitize(body, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, len(body), original)
		assert.NotContains(t, string(out), "var x=1")
		assert.Contains(t, string(out), "rate")
	})

	t.Run("removes multiple non-adjacent script blocks", func(t *testing.T) {
		t.Parallel()
		body := []byte(`<html><script>first()</script><p>mid</p><script>second()</script></html>`)
		out, _, err := Sanitize(body, nil, nil)
		require.NoError(t, err)
		assert.NotContains(t, string(out), "first()")
		assert.NotContains(t, string(out), "second()")
		assert.Contains(t, string(out), "mid")
	})

	t.Run("removes style blocks", func(t *testing.T) {
		t.Parallel()
		body := []byte(`<html><style>.rate{color:red}</style><p>123.45</p></html>`)
		out, _, err := Sanitize(body, nil, nil)
		require.NoError(t, err)
		assert.NotContains(t, string(out), ".rate{color:red}")
		assert.Contains(t, string(out), "123.45")
	})

	t.Run("truncates oversized body to the configured cap when no anchor matches", func(t *testing.T) {
		t.Parallel()
		body := []byte(strings.Repeat("a", maxBodyBytesForLLM+1024))
		out, original, err := Sanitize(body, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, maxBodyBytesForLLM+1024, original)
		assert.Len(t, out, maxBodyBytesForLLM)
	})

	t.Run("original size is reported pre-strip", func(t *testing.T) {
		t.Parallel()
		scriptBlock := `<script>lots of js code here</script>`
		body := []byte(scriptBlock + "<p>rate: 100</p>")
		_, original, err := Sanitize(body, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, len(body), original)
	})

	t.Run("body below the cap is not truncated", func(t *testing.T) {
		t.Parallel()
		body := []byte("<p>rate: 456.78</p>")
		out, _, err := Sanitize(body, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, body, out)
	})

	t.Run("body exceeding 5 MB returns error", func(t *testing.T) {
		t.Parallel()
		body := make([]byte, maxRawBodyBytes+1)
		_, original, err := Sanitize(body, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "5 MB")
		assert.Equal(t, len(body), original)
	})

	t.Run("anchor found in oversized body returns located window not full body", func(t *testing.T) {
		t.Parallel()
		// Build a body larger than maxBodyBytesForLLM with "USD" near the end.
		prefix := strings.Repeat("x", maxBodyBytesForLLM+10)
		body := []byte(prefix + "USD")
		out, _, err := Sanitize(body, nil, []string{"USD"})
		require.NoError(t, err)
		assert.Contains(t, string(out), "USD")
		assert.LessOrEqual(t, len(out), maxBodyBytesForLLM)
	})

	t.Run("removes head section so locate skips meta tags", func(t *testing.T) {
		t.Parallel()
		// USD appears inside <meta> in <head> — it must not anchor the window.
		// The structural anchor <div class="rate is only present in the body section.
		padding := strings.Repeat("x", 5000)
		body := []byte(`<html><head><meta content="USD here"/></head><body>` + padding + `<div class="rate">USD rate row</div></body></html>`)
		out, _, err := Sanitize(body, []string{`<div class="rate`}, []string{"USD"})
		require.NoError(t, err)
		// The window must contain the body-side rate row marker, not just the meta text.
		assert.Contains(t, string(out), `<div class="rate`)
		assert.Contains(t, string(out), "USD rate row")
	})

	t.Run("no head tag is a safe no-op", func(t *testing.T) {
		t.Parallel()
		// A JSON body has no <head>; the strip should be a no-op.
		body := []byte(`{"rate":{"USD":450.5},"meta":"no html here"}`)
		out, _, err := Sanitize(body, nil, []string{"USD"})
		require.NoError(t, err)
		assert.Contains(t, string(out), "USD")
		assert.Contains(t, string(out), "450.5")
	})

	t.Run("header element is preserved (head strip does not match header)", func(t *testing.T) {
		t.Parallel()
		body := []byte(`<html><header class="nav">site nav USD</header><main>USD rate row</main></html>`)
		out, _, err := Sanitize(body, nil, []string{"USD"})
		require.NoError(t, err)
		// <header> must survive; only <head>...</head> is stripped.
		assert.Contains(t, string(out), "<header")
		assert.Contains(t, string(out), "site nav USD")
	})

	t.Run("structural anchor wins over earlier currency anchor end-to-end", func(t *testing.T) {
		t.Parallel()
		// "USD" at offset ~100; "<table" at offset ~5000.
		// The window should be centred near 5000, not 100.
		prefix := strings.Repeat("y", 100)
		mid := strings.Repeat("z", 4900)
		body := []byte(prefix + "USD" + mid + "<table><tr><td>USD 450</td></tr></table>")
		out, _, err := Sanitize(body, []string{"<table"}, []string{"USD"})
		require.NoError(t, err)
		// The window must contain the table marker, not the leading "USD" sentinel.
		assert.Contains(t, string(out), "<table")
	})

	t.Run("bcc fixture structural anchor takes priority over currency tier", func(t *testing.T) {
		t.Parallel()
		data, err := os.ReadFile("../../../tmp/testdata/sources/bcc.html")
		if err != nil {
			t.Skip("bcc.html fixture not present; skipping fixture-based test")
		}
		structural := []string{
			"<table", "<tbody", "<tr ",
			`<div class="text-lg`,
			`<div class="rate`,
			`class="currency"`,
			`class="exchange"`,
			`data-currency=`,
		}
		out, _, err := Sanitize(data, structural, []string{"USD", "KZT"})
		require.NoError(t, err)
		// Output must fit within the locate window regardless of body size.
		assert.LessOrEqual(t, len(out), locateWindowBytes, "output must fit within the locate window")
		// A structural Tier 1 anchor (<div class="text-lg) was found; the window
		// is centred on it, not on the first bare "USD" which sits ~495 KB later
		// in this fixture.  The tier-priority mechanism is verified: if currency
		// tier had won, the window would be centred ~536 KB into the body and
		// would not contain the structural marker.
		assert.Contains(t, string(out), `<div class="text-lg`)
	})

	t.Run("text-lg anchor appears twice only co-located occurrence wins", func(t *testing.T) {
		t.Parallel()
		// First <div class="text-lg"> at offset ~100 — no USD/KZT nearby (marketing heading).
		// Second <div class="text-lg"> at offset ~50100 — "USD" within 300 bytes (rate row).
		lonely := strings.Repeat("a", 100) + `<div class="text-lg">Special Offer!</div>` + strings.Repeat("b", 49960)
		rateRow := `<div class="text-lg">` + strings.Repeat("c", 300) + "USD rate 469.00"
		body := []byte(lonely + rateRow)
		structural := []string{`<div class="text-lg`}
		out, _, err := Sanitize(body, structural, []string{"USD", "KZT"})
		require.NoError(t, err)
		// The window must contain the USD-adjacent occurrence, not the lonely marketing one.
		assert.Contains(t, string(out), "USD rate 469.00")
		assert.NotContains(t, string(out), "Special Offer!")
	})
}

func TestLocate(t *testing.T) {
	t.Parallel()

	t.Run("anchor at start clamps window to beginning of body", func(t *testing.T) {
		t.Parallel()
		body := []byte("USD is here" + strings.Repeat("x", 1000))
		out, found := Locate(body, nil, []string{"USD"}, 100, 0)
		assert.True(t, found)
		// hit is at offset 0; start clamps to 0, end = 0+50 = 50
		assert.Equal(t, body[0:50], out)
	})

	t.Run("anchor in middle of body returns centred window", func(t *testing.T) {
		t.Parallel()
		// 200 bytes of padding, then "USD", then 200 bytes more.
		prefix := strings.Repeat("a", 200)
		suffix := strings.Repeat("b", 200)
		body := []byte(prefix + "USD" + suffix)
		out, found := Locate(body, nil, []string{"USD"}, 100, 0)
		assert.True(t, found)
		// hit at 200; half=50; start=150, end=250
		assert.Equal(t, body[150:250], out)
		assert.Contains(t, string(out), "USD")
	})

	t.Run("no anchor returns body unchanged and found false", func(t *testing.T) {
		t.Parallel()
		body := []byte("no matching content here")
		out, found := Locate(body, []string{"<table"}, []string{"USD", "EUR"}, 100, 0)
		assert.False(t, found)
		assert.Equal(t, body, out)
	})

	t.Run("tier-1 structural hit wins over earlier tier-2 currency hit", func(t *testing.T) {
		t.Parallel()
		// "USD" at offset 10 (tier-2), "<table" at offset 500 (tier-1) with "USD" nearby.
		// The window should be centred near 500, not 10.
		prefix := strings.Repeat("z", 10)
		mid := strings.Repeat("z", 490)
		body := []byte(prefix + "USD" + mid + "<table>USD data</table>")
		out, found := Locate(body, []string{"<table"}, []string{"USD"}, 20, defaultCoLocationBytes)
		assert.True(t, found)
		assert.Contains(t, string(out), "<table")
	})

	t.Run("tier-2 currency hit used when tier-1 has no match", func(t *testing.T) {
		t.Parallel()
		prefix := strings.Repeat("a", 200)
		body := []byte(prefix + "USD" + strings.Repeat("b", 200))
		out, found := Locate(body, []string{"<table", "<tbody"}, []string{"USD"}, 100, defaultCoLocationBytes)
		assert.True(t, found)
		assert.Contains(t, string(out), "USD")
	})

	t.Run("smallest offset wins within tier-1", func(t *testing.T) {
		t.Parallel()
		// "<table" at offset 10 with "KZT" co-located, "<tbody" at offset 500 with "KZT" co-located;
		// tier-1 should pick the smallest qualifying offset (10).
		prefix := strings.Repeat("z", 10)
		mid := strings.Repeat("z", 490)
		body := []byte(prefix + "<table>KZT</table>" + mid + "<tbody>KZT</tbody>")
		out, found := Locate(body, []string{"<tbody", "<table"}, []string{"KZT"}, 20, defaultCoLocationBytes)
		assert.True(t, found)
		// best=10; half=10; window contains "<table" not "<tbody"
		assert.Contains(t, string(out), "<table")
		assert.NotContains(t, string(out), "<tbody")
	})

	t.Run("empty tier-1 slice falls through to tier-2", func(t *testing.T) {
		t.Parallel()
		prefix := strings.Repeat("a", 100)
		body := []byte(prefix + "KZT" + strings.Repeat("b", 100))
		out, found := Locate(body, []string{}, []string{"KZT"}, 20, defaultCoLocationBytes)
		assert.True(t, found)
		assert.Contains(t, string(out), "KZT")
	})

	t.Run("empty tier-2 slice with no tier-1 hits returns body false", func(t *testing.T) {
		t.Parallel()
		body := []byte("plain text no anchors here")
		out, found := Locate(body, nil, []string{}, 100, defaultCoLocationBytes)
		assert.False(t, found)
		assert.Equal(t, body, out)
	})

	t.Run("duplicate anchors within a tier are deduplicated", func(t *testing.T) {
		t.Parallel()
		body := []byte(strings.Repeat("x", 50) + "USD" + strings.Repeat("x", 50))
		out, found := Locate(body, nil, []string{"USD", "USD", "USD"}, 20, 0)
		assert.True(t, found)
		assert.Contains(t, string(out), "USD")
	})

	t.Run("empty anchor strings are skipped", func(t *testing.T) {
		t.Parallel()
		body := []byte("hello world")
		out, found := Locate(body, []string{"", ""}, []string{"", ""}, 100, defaultCoLocationBytes)
		assert.False(t, found)
		assert.Equal(t, body, out)
	})

	t.Run("anchor near end clamps window to end of body", func(t *testing.T) {
		t.Parallel()
		// "USD" is in the last 3 bytes.
		body := []byte(strings.Repeat("x", 100) + "USD")
		out, found := Locate(body, nil, []string{"USD"}, 100, 0)
		assert.True(t, found)
		// hit=100; half=50; start=50, end clamped to 103
		assert.Equal(t, body[50:], out)
		assert.Contains(t, string(out), "USD")
	})

	t.Run("window clamps correctly at body start", func(t *testing.T) {
		t.Parallel()
		// Anchor at the very first byte; start must not go negative.
		// Pass currency alongside so the co-location guard is satisfied.
		body := []byte("<table>KZT" + strings.Repeat("x", 200))
		out, found := Locate(body, []string{"<table"}, []string{"KZT"}, 100, defaultCoLocationBytes)
		assert.True(t, found)
		assert.Equal(t, body[0:50], out)
	})

	t.Run("tier-1 hit with co-located currency wins", func(t *testing.T) {
		t.Parallel()
		// Tier-1 anchor at offset 5000, "USD" at offset 5300 (within 5 KB).
		prefix := strings.Repeat("x", 5000)
		body := []byte(prefix + `<div class="text-lg">` + strings.Repeat("y", 300) + "USD value here")
		out, found := Locate(body, []string{`<div class="text-lg"`}, []string{"USD"}, 200, defaultCoLocationBytes)
		assert.True(t, found)
		assert.Contains(t, string(out), `<div class="text-lg"`)
	})

	t.Run("tier-1 hit without co-located currency is skipped", func(t *testing.T) {
		t.Parallel()
		// Tier-1 at offset 1000 with no currency nearby (lonely marketing heading).
		// Second tier-1 hit at offset ~100048 with "USD" at 100048+50 (within 5 KB).
		prefix1 := strings.Repeat("a", 1000)
		gap := strings.Repeat("b", 99000)
		// <div class="text-lg"> is 21 bytes; "USD rate" is 50 bytes into the rate section.
		rateSection := `<div class="text-lg">` + strings.Repeat("c", 50) + "USD rate here" + strings.Repeat("c", 100)
		body := []byte(prefix1 + `<div class="text-lg">no currency far away</div>` + gap + rateSection)
		// Use a window large enough to encompass USD from the second anchor.
		out, found := Locate(body, []string{`<div class="text-lg"`}, []string{"USD"}, 1000, defaultCoLocationBytes)
		assert.True(t, found)
		// The far hit (~100048) should win because it has USD co-located.
		assert.Contains(t, string(out), "USD rate here")
	})

	t.Run("tier-1 candidates smallest offset among co-located wins", func(t *testing.T) {
		t.Parallel()
		// Three tier-1 hits: offset 200 (co-located), offset 500 (co-located), offset 10000 (no currency).
		hit1 := strings.Repeat("a", 200) + "<table>USD</table>"
		hit2 := strings.Repeat("b", 282) + "<table>USD</table>" // 200+18+282=500
		noHit := strings.Repeat("c", 9482) + "<table>nothing nearby</table>"
		body := []byte(hit1 + hit2 + noHit)
		out, found := Locate(body, []string{"<table"}, []string{"USD"}, 40, defaultCoLocationBytes)
		assert.True(t, found)
		// smallest qualifying offset is 200; window centred there
		assert.Contains(t, string(out), "<table>USD</table>")
	})

	t.Run("tier-1 falls through to tier-2 when zero qualify", func(t *testing.T) {
		t.Parallel()
		// Tier-1 anchor exists but no currency is within ±5 KB of it.
		// Currency anchor "USD" exists far away — tier-2 should find it.
		prefix := strings.Repeat("x", 100)
		gap := strings.Repeat("y", 20000)
		body := []byte(prefix + "<table>no currency anywhere nearby</table>" + gap + "USD rate here")
		out, found := Locate(body, []string{"<table"}, []string{"USD"}, 200, defaultCoLocationBytes)
		assert.True(t, found)
		assert.Contains(t, string(out), "USD rate here")
		assert.NotContains(t, string(out), "<table>")
	})

	t.Run("coLocationBytes=0 disables the guard", func(t *testing.T) {
		t.Parallel()
		// Without the co-location guard, the tier-1 hit at offset 1000 wins even
		// though there is no currency code nearby.
		prefix := strings.Repeat("x", 1000)
		body := []byte(prefix + "<table>no currency here</table>" + strings.Repeat("z", 30000) + "USD far away")
		out, found := Locate(body, []string{"<table"}, []string{"USD"}, 200, 0)
		assert.True(t, found)
		assert.Contains(t, string(out), "<table>")
	})

	t.Run("empty currency slice with co-location enabled falls through to tier-2", func(t *testing.T) {
		t.Parallel()
		// Co-location requires a currency match; with no currencies, no tier-1
		// hit can qualify. Tier-2 is also empty, so the result is false.
		body := []byte(strings.Repeat("x", 100) + "<table>some data</table>")
		out, found := Locate(body, []string{"<table"}, []string{}, 100, defaultCoLocationBytes)
		assert.False(t, found)
		assert.Equal(t, body, out)
	})

	t.Run("duplicate tier-1 anchors are still deduplicated", func(t *testing.T) {
		t.Parallel()
		body := []byte(strings.Repeat("z", 50) + "<table>USD data</table>" + strings.Repeat("z", 50))
		out, found := Locate(body, []string{"<table", "<table", "<table"}, []string{"USD"}, 40, defaultCoLocationBytes)
		assert.True(t, found)
		assert.Contains(t, string(out), "<table")
	})
}
