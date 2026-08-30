package sourceaudit

import (
	"context"
	"errors"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ Fetcher = (*fakeFetcher)(nil)

type fakeFetcher struct {
	responses   map[string]*FetchResult
	errors      map[string]error
	callCounts  map[string]int
	lastHeaders map[string]map[string]string // last headers seen per URL
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{
		responses:   make(map[string]*FetchResult),
		errors:      make(map[string]error),
		callCounts:  make(map[string]int),
		lastHeaders: make(map[string]map[string]string),
	}
}

func (f *fakeFetcher) addResponse(url string, body []byte, contentType string) {
	f.responses[url] = &FetchResult{Body: body, ContentType: contentType, StatusCode: 200}
}

func (f *fakeFetcher) addError(url string, err error) {
	f.errors[url] = err
}

func (f *fakeFetcher) Fetch(_ context.Context, url string, headers map[string]string) (*FetchResult, error) {
	f.callCounts[url]++
	f.lastHeaders[url] = headers
	if err, ok := f.errors[url]; ok {
		return nil, err
	}
	if res, ok := f.responses[url]; ok {
		return res, nil
	}
	return nil, errors.New("no response configured for " + url)
}

func regexSource(name, url, pattern string) SeededSource {
	return SeededSource{
		Name: name,
		URL:  url,
		Side: "BID",
		Rules: []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: pattern},
		},
		Active: true,
	}
}

func TestValidateSourceURL(t *testing.T) {
	t.Parallel()

	t.Run("https URL is accepted", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, validateSourceURL("https://example.com/rates"))
	})

	t.Run("http URL is accepted", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, validateSourceURL("http://example.com/rates"))
	})

	t.Run("file scheme is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("file:///etc/passwd")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("gopher scheme is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("gopher://example.com/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("javascript scheme is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("javascript:alert(1)")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("empty string is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("malformed URL with control character is rejected", func(t *testing.T) {
		t.Parallel()
		// A URL with a null byte is rejected by url.Parse before scheme inspection.
		err := validateSourceURL("http://\x00/path")
		require.Error(t, err)
	})

	t.Run("scheme-only ftp is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("ftp://files.example.com/data")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})
}

func TestAuditor_Run(t *testing.T) {
	t.Parallel()

	t.Run("happy path single source regex match", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://example.com/", []byte(`<span>468.95</span>`), "text/html")

		a := &Auditor{Fetcher: f}
		results, err := a.Run(t.Context(), []SeededSource{
			regexSource("SRC1", "https://example.com/", `<span>([\d.]+)</span>`),
		})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, StatusOK, results[0].Status)
		assert.Equal(t, "468.95", results[0].Value)
	})

	t.Run("two sources sharing one URL fetcher invoked once", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://shared.com/", []byte(`bid=460.00 ask=470.00`), "text/html")

		a := &Auditor{Fetcher: f}
		sources := []SeededSource{
			regexSource("SRC_BID", "https://shared.com/", `bid=([\d.]+)`),
			regexSource("SRC_ASK", "https://shared.com/", `ask=([\d.]+)`),
		}
		results, err := a.Run(t.Context(), sources)
		require.NoError(t, err)
		require.Len(t, results, 2)
		assert.Equal(t, 1, f.callCounts["https://shared.com/"], "fetcher must be called exactly once")
		assert.Equal(t, StatusOK, results[0].Status)
		assert.Equal(t, "460.00", results[0].Value)
		assert.Equal(t, StatusOK, results[1].Status)
		assert.Equal(t, "470.00", results[1].Value)
		assert.Equal(t, results[0].Body, results[1].Body, "both results share the same body slice")
	})

	t.Run("regex with no match", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://example.com/", []byte(`no numbers here`), "text/html")

		a := &Auditor{Fetcher: f}
		results, err := a.Run(t.Context(), []SeededSource{
			regexSource("SRC1", "https://example.com/", `price=([\d.]+)`),
		})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, StatusRegexNoMatch, results[0].Status)
	})

	t.Run("fetch error propagates to all sources of that URL", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addError("https://bad.com/", errors.New("connection refused"))
		f.addResponse("https://good.com/", []byte(`val=123.45`), "text/html")

		a := &Auditor{Fetcher: f}
		sources := []SeededSource{
			regexSource("SRC_BAD1", "https://bad.com/", `([\d.]+)`),
			regexSource("SRC_BAD2", "https://bad.com/", `([\d.]+)`),
			regexSource("SRC_GOOD", "https://good.com/", `val=([\d.]+)`),
		}
		results, err := a.Run(t.Context(), sources)
		require.NoError(t, err)
		require.Len(t, results, 3)
		assert.Equal(t, StatusFetchError, results[0].Status)
		assert.Contains(t, results[0].Detail, "connection refused")
		assert.Equal(t, StatusFetchError, results[1].Status)
		assert.Equal(t, StatusOK, results[2].Status)
		assert.Equal(t, "123.45", results[2].Value)
	})

	t.Run("JSON-path rule on a JSON body", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://api.example.com/", []byte(`{"usdSell":"465.5"}`), "application/json")

		a := &Auditor{Fetcher: f}
		sources := []SeededSource{
			{
				Name: "SRC_JSON",
				URL:  "https://api.example.com/",
				Side: "ASK",
				Rules: []domain.RateSourceRule{
					{Method: domain.MethodJSONPath, Pattern: "usdSell"},
				},
				Active: true,
			},
		}
		results, err := a.Run(t.Context(), sources)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, StatusOK, results[0].Status)
		assert.Equal(t, "465.5", results[0].Value)
	})

	t.Run("unsupported method parse_float", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://example.com/", []byte(`450.00`), "text/html")

		a := &Auditor{Fetcher: f}
		sources := []SeededSource{
			{
				Name: "SRC_PARSEFLOAT",
				URL:  "https://example.com/",
				Side: "BID",
				Rules: []domain.RateSourceRule{
					{Method: domain.MethodParseFloat},
				},
				Active: true,
			},
		}
		results, err := a.Run(t.Context(), sources)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, StatusUnsupportedMethod, results[0].Status)
	})

	t.Run("extracted value that fails sanity check", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://example.com/zero", []byte(`<v>0.0</v>`), "text/html")
		f.addResponse("https://example.com/neg", []byte(`<v>-5.0</v>`), "text/html")
		f.addResponse("https://example.com/nan", []byte(`<v>NaN</v>`), "text/html")

		a := &Auditor{Fetcher: f}
		sources := []SeededSource{
			regexSource("ZERO", "https://example.com/zero", `<v>([\d.]+)</v>`),
			regexSource("NEG", "https://example.com/neg", `<v>(-[\d.]+)</v>`),
			regexSource("NAN", "https://example.com/nan", `<v>([A-Za-z]+)</v>`),
		}
		results, err := a.Run(t.Context(), sources)
		require.NoError(t, err)
		require.Len(t, results, 3)
		assert.Equal(t, StatusValueParseError, results[0].Status, "zero value")
		assert.Equal(t, StatusValueParseError, results[1].Status, "negative value")
		assert.Equal(t, StatusValueParseError, results[2].Status, "non-numeric NaN string")
	})

	t.Run("thousand separator value with regular space", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://example.com/", []byte(`rate: 70 534.67 end`), "text/html")

		a := &Auditor{Fetcher: f}
		results, err := a.Run(t.Context(), []SeededSource{
			regexSource("SRC_SPACE", "https://example.com/", `rate: ([\d .]+) end`),
		})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, StatusOK, results[0].Status)
		assert.Equal(t, "70534.67", results[0].Value)
	})

	t.Run("per-source headers are forwarded to the fetcher", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://api.example.com/", []byte(`<v>123.45</v>`), "text/html")

		src := SeededSource{
			Name:    "SRC_HEADERS",
			URL:     "https://api.example.com/",
			Side:    "BID",
			Headers: map[string]string{"User-Agent": "CustomBot/2.0"},
			Rules:   []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `<v>([\d.]+)</v>`}},
			Active:  true,
		}

		a := &Auditor{Fetcher: f}
		results, err := a.Run(t.Context(), []SeededSource{src})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, StatusOK, results[0].Status)
		// The fetcher must have received the per-source header override.
		assert.Equal(t, "CustomBot/2.0", f.lastHeaders["https://api.example.com/"]["User-Agent"])
	})

	t.Run("two sources sharing URL but different headers are fetched separately", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://shared.example.com/", []byte(`<v>200.00</v>`), "text/html")

		src1 := SeededSource{
			Name:    "SRC_A",
			URL:     "https://shared.example.com/",
			Side:    "BID",
			Headers: map[string]string{"User-Agent": "BotA"},
			Rules:   []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `<v>([\d.]+)</v>`}},
			Active:  true,
		}
		src2 := SeededSource{
			Name:    "SRC_B",
			URL:     "https://shared.example.com/",
			Side:    "BID",
			Headers: map[string]string{"User-Agent": "BotB"},
			Rules:   []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `<v>([\d.]+)</v>`}},
			Active:  true,
		}

		a := &Auditor{Fetcher: f}
		results, err := a.Run(t.Context(), []SeededSource{src1, src2})
		require.NoError(t, err)
		require.Len(t, results, 2)
		// Different headers → different cache keys → two independent fetches.
		assert.Equal(t, 2, f.callCounts["https://shared.example.com/"])
	})

	t.Run("two sources sharing URL with nil headers still dedup to one fetch", func(t *testing.T) {
		t.Parallel()

		f := newFakeFetcher()
		f.addResponse("https://shared2.example.com/", []byte(`bid=460.00 ask=470.00`), "text/html")

		a := &Auditor{Fetcher: f}
		sources := []SeededSource{
			regexSource("SRC_BID2", "https://shared2.example.com/", `bid=([\d.]+)`),
			regexSource("SRC_ASK2", "https://shared2.example.com/", `ask=([\d.]+)`),
		}
		results, err := a.Run(t.Context(), sources)
		require.NoError(t, err)
		require.Len(t, results, 2)
		// Both have nil headers → same cache key → exactly one fetch.
		assert.Equal(t, 1, f.callCounts["https://shared2.example.com/"], "nil-header sources sharing a URL must dedup to one fetch")
	})
}
