package rateextractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/seilbekskindirov/beacon/internal/tools/threadsafe"
	"github.com/stretchr/testify/require"
)

// Response bodies captured from live validation of the two seeded sources. They are
// fed to an httptest server rather than read from disk: nothing here exercises file
// I/O, and at this size a separate testdata file only hides what the test asserts.
const (
	// kaseLastDealHTML keeps the Angular SSR _ngcontent attributes, which prove the
	// regex tolerates attribute prefixes, and the "4 630,00" comma decimal with a
	// space thousands-separator, which the normaliser has to reduce to 4630.00.
	kaseLastDealHTML = `<div _ngcontent-ng-c179376686 class="price"><div _ngcontent-ng-c179376686 class="last-deal">` +
		`<div _ngcontent-ng-c179376686 class="value"> 4 630,00 </div>` +
		`<div _ngcontent-ng-c179376686 class="label"> price of the last deal </div></div></div>`

	// yahooV8AAPLJSON is the shape the seed rule's json_path walks.
	yahooV8AAPLJSON = `{"chart":{"result":[{"meta":{"regularMarketPrice":282.0}}]}}`
)

// compile-time interface checks
var _ rateValueRepository = &repository.RateValueRepository{}
var _ rateValueRepository = &mockRateValueRepository{}

func TestNewRateExtractorWithHTTPClient(t *testing.T) {
	t.Parallel()

	logger := threadsafe.NewBuffer(nil)

	t.Run("Valid", func(t *testing.T) {
		t.Parallel()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{},
			logger,
		)
		require.NoError(t, err)
		require.NotNil(t, ext)
	})
	t.Run("NilClient", func(t *testing.T) {
		t.Parallel()

		_, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			nil,
			logger,
		)
		require.Error(t, err)
	})

	t.Run("a client with no timeout fetches instead of expiring instantly", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `<span class="rate">450.75</span>`)
		}))
		defer srv.Close()

		// srv.Client() leaves Timeout at zero, and it is the client these tests must
		// pass — a bare &http.Client{} shares http.DefaultTransport across parallel
		// tests. Forwarding that zero to context.WithTimeout produced a deadline
		// already in the past, so every fetch died in under a millisecond looking
		// like a network fault.
		client := srv.Client()
		require.Zero(t, client.Timeout, "the case under test is a client with no timeout")

		rateRepo := &mockRateValueRepository{}
		ownLogger := threadsafe.NewBuffer(nil)
		ext, err := NewRateExtractorWithHTTPClient(rateRepo, client, ownLogger)
		require.NoError(t, err)

		source := &domain.RateSource{
			Name:          "no_timeout_src",
			URL:           srv.URL,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `class="rate">([\d.]+)`},
			},
		}
		require.NoError(t, ext.Run(t.Context(), source))
		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 450.75, rateRepo.retained[0].Price, 0.001)

		// Defaulting silently is the standing objection to defaulting at all, so the
		// substitution has to be visible somewhere.
		require.Contains(t, ownLogger.String(), "direct http client has no timeout",
			"the substituted bound must be announced, not applied in silence")
	})
}

func TestNewRateExtractor(t *testing.T) {
	t.Parallel()

	logger := threadsafe.NewBuffer(nil)

	t.Run("no proxy passes empty string and succeeds", func(t *testing.T) {
		t.Parallel()

		ext, err := NewRateExtractor(
			&mockRateValueRepository{},
			"",
			5*time.Second,
			logger,
		)
		require.NoError(t, err)
		require.NotNil(t, ext)
	})

	t.Run("invalid proxy URL is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := NewRateExtractor(
			&mockRateValueRepository{},
			"://bad url",
			5*time.Second,
			logger,
		)
		require.Error(t, err)
	})

	t.Run("invalid proxy URL is rejected with redacted error", func(t *testing.T) {
		t.Parallel()

		const badProxy = "http://%xx-invalid-percent-escape"
		_, err := NewRateExtractor(
			&mockRateValueRepository{},
			badProxy,
			5*time.Second,
			logger,
		)
		require.Error(t, err)
		// The error must not leak any substring from the raw URL into the log.
		require.NotContains(t, err.Error(), "%xx")
		require.NotContains(t, err.Error(), "invalid-percent-escape")
		require.Contains(t, err.Error(), "parse proxy URL")
	})

	// A configured proxy URL builds the proxied client but routes nothing by itself.
	// This pair is the whole two-level contract: BEACON_PROXY_URL says a proxy exists,
	// options.use_proxy says a source wants it, and only their conjunction proxies.
	// The first subtest is what keeps issue #16's measured default in place when an
	// operator sets the variable — it used to be the opposite assertion.
	newProxiedExtractor := func(t *testing.T) (ext *RateExtractor, targetURL string, proxyHits *atomic.Int32) {
		t.Helper()
		proxyHits = &atomic.Int32{}

		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			proxyHits.Add(1)
			_, _ = w.Write([]byte("correct-proxy"))
		}))
		t.Cleanup(proxy.Close)

		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("target"))
		}))
		t.Cleanup(target.Close)

		ext, err := NewRateExtractor(&mockRateValueRepository{}, proxy.URL, 5*time.Second, logger)
		require.NoError(t, err)
		return ext, target.URL, proxyHits
	}

	proxySource := func(name, targetURL string, useProxy bool) *domain.RateSource {
		return &domain.RateSource{
			Name:    name,
			URL:     targetURL,
			Options: domain.RateSourceOptions{UseProxy: useProxy},
			Rules:   []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `(correct-proxy|target)`}},
		}
	}

	t.Run("a configured proxyURL alone routes nothing", func(t *testing.T) {
		t.Parallel()

		ext, targetURL, proxyHits := newProxiedExtractor(t)
		_ = ext.Run(t.Context(), proxySource("direct_src", targetURL, false))
		require.Zero(t, proxyHits.Load(),
			"a source that has not opted in must stay direct even with a proxy configured")
	})

	t.Run("a source opted in routes through the proxy", func(t *testing.T) {
		t.Parallel()

		ext, targetURL, proxyHits := newProxiedExtractor(t)
		_ = ext.Run(t.Context(), proxySource("proxy_src", targetURL, true))
		require.Positive(t, proxyHits.Load(), "options.use_proxy must route traffic through the proxy")
	})
}

func TestRateExtractor_Name(t *testing.T) {
	t.Parallel()

	logger := threadsafe.NewBuffer(nil)

	ext, err := NewRateExtractorWithHTTPClient(
		&mockRateValueRepository{},
		&http.Client{},
		logger,
	)
	require.NoError(t, err)
	require.Equal(t, "rate_extractor", ext.Name())
}

func TestRateExtractor_Run(t *testing.T) {
	t.Parallel()

	logger := threadsafe.NewBuffer(nil)

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `<span class="rate">450.75</span>`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name:          "test_src",
			URL:           srv.URL,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `class="rate">([\d.]+)`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.NoError(t, ext.Run(t.Context(), source))

		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 450.75, rateRepo.retained[0].Price, 0.001)
		require.Equal(t, "USD", rateRepo.retained[0].BaseCurrency)
		require.Equal(t, "KZT", rateRepo.retained[0].QuoteCurrency)
		require.Equal(t, "test_src", rateRepo.retained[0].SourceName)
	})
	t.Run("comma and space replacement", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `rate=1 234,56end`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name: "comma_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `rate=([\d ,]+)end`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.NoError(t, ext.Run(t.Context(), source))
		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 1234.56, rateRepo.retained[0].Price, 0.001)
	})
	t.Run("method store to rate", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `42.0`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name:  "store_src",
			URL:   srv.URL,
			Rules: []domain.RateSourceRule{{Method: domain.MethodStoreToRate}},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.NoError(t, ext.Run(t.Context(), source))
		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 42.0, rateRepo.retained[0].Price, 0.001)
	})
	t.Run("multiple regex rules", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `<div>outer: <span>inner: 99.9</span></div>`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name: "multi_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `outer: (.+)</div>`},
				{Method: domain.MethodRegex, Pattern: `inner: ([\d.]+)`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.NoError(t, ext.Run(t.Context(), source))
		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 99.9, rateRepo.retained[0].Price, 0.001)
	})
	t.Run("http non-2xx", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		source := &domain.RateSource{Name: "fail_src", URL: srv.URL}

		ext, err := NewRateExtractorWithHTTPClient(&mockRateValueRepository{}, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("http request fails", func(t *testing.T) {
		t.Parallel()

		source := &domain.RateSource{Name: "bad_url_src", URL: "http://127.0.0.1:1"} // nothing listening

		ext, err := NewRateExtractorWithHTTPClient(&mockRateValueRepository{}, &http.Client{Timeout: 500 * time.Millisecond}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("unsupported method", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `100`)
		}))
		defer srv.Close()

		source := &domain.RateSource{
			Name: "unsupported_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: "unknown_method"},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(&mockRateValueRepository{}, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("invalid regex pattern", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `100`)
		}))
		defer srv.Close()

		source := &domain.RateSource{
			Name: "bad_regex_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `[invalid`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(&mockRateValueRepository{}, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("regex no match", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `no numbers here`)
		}))
		defer srv.Close()

		source := &domain.RateSource{
			Name: "nomatch_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `price=(\d+)`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(&mockRateValueRepository{}, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("non-numeric payload", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `<span>N/A</span>`)
		}))
		defer srv.Close()

		source := &domain.RateSource{
			Name: "nan_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `<span>(.+)</span>`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(&mockRateValueRepository{}, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("zero value", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `value=0.0end`)
		}))
		defer srv.Close()

		source := &domain.RateSource{
			Name: "zero_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `value=([\d.]+)end`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(&mockRateValueRepository{}, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("json_path happy path", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"data":{"rate":450.75}}`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name:          "json_src",
			URL:           srv.URL,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodJSONPath, Pattern: "data.rate"},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.NoError(t, ext.Run(t.Context(), source))
		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 450.75, rateRepo.retained[0].Price, 0.001)
	})
	t.Run("json_path array result", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"rates":[{"value":1.23}]}`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name: "json_arr_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodJSONPath, Pattern: "rates[0].value"},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.NoError(t, ext.Run(t.Context(), source))
		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 1.23, rateRepo.retained[0].Price, 0.001)
	})
	t.Run("json_path key not found", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"other":1}`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name: "json_notfound_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodJSONPath, Pattern: "rate"},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
		require.Empty(t, rateRepo.retained)
	})
	t.Run("json_path invalid JSON response", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `not-json`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name: "json_badjson_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodJSONPath, Pattern: "rate"},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
		require.Empty(t, rateRepo.retained)
	})
	t.Run("json_path combined with parse_float", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"r":"1 234,56"}`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}

		source := &domain.RateSource{
			Name: "json_combined_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodJSONPath, Pattern: "r"},
				{Method: domain.MethodParseFloat},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.NoError(t, ext.Run(t.Context(), source))
		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 1234.56, rateRepo.retained[0].Price, 0.001)
	})
	t.Run("options headers override default User-Agent", func(t *testing.T) {
		t.Parallel()

		var receivedUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedUA = r.Header.Get("User-Agent")
			_, _ = fmt.Fprint(w, `42.00`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}
		source := &domain.RateSource{
			Name: "ua_override_src",
			URL:  srv.URL,
			Options: domain.RateSourceOptions{
				Headers: map[string]string{
					"User-Agent": "CustomBot/2.0",
				},
			},
			Rules: []domain.RateSourceRule{{Method: domain.MethodStoreToRate}},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)
		require.NoError(t, ext.Run(t.Context(), source))

		require.Equal(t, "CustomBot/2.0", receivedUA, "per-source User-Agent must override the Beacon/1.0 default")
	})

	t.Run("default User-Agent sent when Options.Headers is nil", func(t *testing.T) {
		t.Parallel()

		var receivedUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedUA = r.Header.Get("User-Agent")
			_, _ = fmt.Fprint(w, `42.00`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}
		source := &domain.RateSource{
			Name:  "default_ua_src",
			URL:   srv.URL,
			Rules: []domain.RateSourceRule{{Method: domain.MethodStoreToRate}},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)
		require.NoError(t, ext.Run(t.Context(), source))

		require.Contains(t, receivedUA, "Beacon/1.0", "default UA must start with Beacon/1.0 when no override")
	})

	t.Run("KASE last-deal comma-decimal format parses correctly", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(kaseLastDealHTML))
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}
		source := &domain.RateSource{
			Name:          "KZ_KASE_LAST_CCBN_KZT",
			URL:           srv.URL,
			BaseCurrency:  "CCBN",
			QuoteCurrency: "KZT",
			Rules: []domain.RateSourceRule{
				{
					Method:  domain.MethodRegex,
					Pattern: `class="last-deal"[^>]*><div[^>]*class="value"[^>]*>\s*([0-9][0-9 ,.]*)`,
				},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)
		require.NoError(t, ext.Run(t.Context(), source))

		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 4630.00, rateRepo.retained[0].Price, 0.001,
			"comma decimal and space thousands-separator must normalize to 4630.00")
		require.Equal(t, "CCBN", rateRepo.retained[0].BaseCurrency)
		require.Equal(t, "KZT", rateRepo.retained[0].QuoteCurrency)
	})

	t.Run("Yahoo v8 JSON regularMarketPrice extracted via json path", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(yahooV8AAPLJSON))
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{}
		source := &domain.RateSource{
			Name:          "US_YAHOO_LAST_AAPL_USD",
			URL:           srv.URL,
			BaseCurrency:  "AAPL",
			QuoteCurrency: "USD",
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodJSONPath, Pattern: "chart.result[0].meta.regularMarketPrice"},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)
		require.NoError(t, ext.Run(t.Context(), source))

		require.Len(t, rateRepo.retained, 1)
		require.InDelta(t, 282.0, rateRepo.retained[0].Price, 0.001)
		require.Equal(t, "AAPL", rateRepo.retained[0].BaseCurrency)
		require.Equal(t, "USD", rateRepo.retained[0].QuoteCurrency)
	})

	t.Run("retain rate value fails", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `rate=123.45end`)
		}))
		defer srv.Close()

		rateRepo := &mockRateValueRepository{err: errors.New("db error")}

		source := &domain.RateSource{
			Name: "db_fail_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `rate=([\d.]+)end`},
			},
		}

		ext, err := NewRateExtractorWithHTTPClient(rateRepo, &http.Client{Timeout: 5 * time.Second}, logger)
		require.NoError(t, err)

		require.Error(t, ext.Run(t.Context(), source))
	})
	t.Run("fetch error wraps underlying response", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, "upstream down")
		}))
		defer srv.Close()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		err = ext.Run(t.Context(), &domain.RateSource{
			Name: "err_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `.+`},
			},
		})

		require.Error(t, err)
		require.ErrorContains(t, err, srv.URL)
		require.ErrorContains(t, err, "could not read html page")
		require.ErrorContains(t, err, "unexpected status 503")
		require.NotContains(t, err.Error(), "page is nil")
	})
	t.Run("empty body yields non-nil slice so page-is-nil guard is unreachable via transport", func(t *testing.T) {
		// Run's guard `if err == nil { err = errors.New("page is nil") }` defends
		// against a hypothetical fetchHtmlPage returning (nil, nil). In practice
		// fetchHtmlPage returns nil only on error, and io.ReadAll of an empty body
		// yields []byte{} (non-nil), so payload == nil is unreachable via the HTTP
		// path. A RoundTripper whose body returns (0, io.EOF) still yields []byte{},
		// so the guard does not fire and we get a parse error on the empty payload.
		// This documents the branch as unreachable through the transport API and
		// asserts the adjacent empty-body behaviour.
		t.Parallel()

		transport := &emptyBodyTransport{}
		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second, Transport: transport},
			logger,
		)
		require.NoError(t, err)

		err = ext.Run(t.Context(), &domain.RateSource{
			Name: "nilbody_src",
			URL:  "http://example.com/rate",
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `rate=([\d.]+)`},
			},
		})

		// io.ReadAll returns []byte{}, not nil, so the payload-nil guard does not
		// fire; the extractor fails later (no regex match). The error must NOT
		// contain "page is nil".
		require.Error(t, err)
		require.NotContains(t, err.Error(), "page is nil")
	})
	t.Run("fetch timeout error preserves cause", func(t *testing.T) {
		t.Parallel()

		const clientTimeout = 50 * time.Millisecond

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Block well past the client timeout so the deadline fires first.
			select {
			case <-time.After(200 * time.Millisecond):
				_, _ = fmt.Fprint(w, "too late")
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: clientTimeout},
			logger,
		)
		require.NoError(t, err)

		err = ext.Run(t.Context(), &domain.RateSource{
			Name: "timeout_src",
			URL:  srv.URL,
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `.+`},
			},
		})

		require.Error(t, err)
		require.NotContains(t, err.Error(), "page is nil")
		// net/http wraps deadline errors inside *url.Error; the deadline string
		// is always present in the chain regardless of Go version.
		require.True(t,
			errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded"),
			"expected timeout cause in error, got: %v", err,
		)
	})
}

func TestRateExtractor_fetchHtmlPage(t *testing.T) {
	t.Parallel()

	logger := threadsafe.NewBuffer(nil)

	t.Run("bring page", func(t *testing.T) {
		t.Parallel()

		const responseBody = `<html>rate: 123.45</html>`

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, responseBody)
		}))
		defer srv.Close()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		body, err := ext.fetchHtmlPage(t.Context(), srv.URL, nil, false)
		require.NoError(t, err)
		require.Equal(t, []byte(responseBody), body)
	})
	t.Run("cache page", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		const responseBody = `<html>cached: 99.9</html>`

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount++
			_, _ = fmt.Fprint(w, responseBody)
		}))
		defer srv.Close()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		body1, err := ext.fetchHtmlPage(t.Context(), srv.URL, nil, false)
		require.NoError(t, err)
		require.Equal(t, []byte(responseBody), body1)

		body2, err := ext.fetchHtmlPage(t.Context(), srv.URL, nil, false)
		require.NoError(t, err)
		require.Equal(t, []byte(responseBody), body2)

		require.Equal(t, 1, callCount, "expected exactly one HTTP request; second call must be served from cache")
	})
	t.Run("invalid url", func(t *testing.T) {
		t.Parallel()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		_, err = ext.fetchHtmlPage(t.Context(), "://bad", nil, false)
		require.Error(t, err)
	})
	t.Run("non-nil headers forwarded to server", func(t *testing.T) {
		t.Parallel()

		var receivedUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedUA = r.Header.Get("User-Agent")
			_, _ = fmt.Fprint(w, "<html>ok</html>")
		}))
		defer srv.Close()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		body, err := ext.fetchHtmlPage(t.Context(), srv.URL, map[string]string{"User-Agent": "TestAgent/3.0"}, false)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "TestAgent/3.0", receivedUA,
			"non-nil headers must override the default User-Agent on the outgoing request")
	})

	t.Run("cache is failed but process is not interrupted", func(t *testing.T) {
		t.Parallel()

		const responseBody = `<html>rate: 77.7</html>`

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, responseBody)
		}))
		defer srv.Close()

		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		// Pre-populate the cache key with an empty value so:
		//   1. Fetch() finds the key but the empty-byte guard skips the cache hit.
		//   2. The post-fetch Push() fails (key already exists in go-cache.Add).
		//   3. fetchHtmlPage logs the error and still returns the body with nil error.
		cacheKey := fmt.Sprintf("GET:%s", srv.URL)
		require.NoError(t, ext.cache.Push(cacheKey, []byte{}))

		body, err := ext.fetchHtmlPage(t.Context(), srv.URL, nil, false)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, []byte(responseBody), body)
	})
}

func TestRateExtractor_failFast(t *testing.T) {
	t.Parallel()

	t.Run("shared URL 500 hits server once", func(t *testing.T) {
		t.Parallel()

		var callCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		logger := threadsafe.NewBuffer(nil)
		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		srcA := &domain.RateSource{Name: "a", URL: srv.URL, Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `.+`}}}
		srcB := &domain.RateSource{Name: "b", URL: srv.URL, Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `.+`}}}

		err1 := ext.Run(t.Context(), srcA)
		require.Error(t, err1)

		err2 := ext.Run(t.Context(), srcB)
		require.Error(t, err2)
		require.ErrorContains(t, err2, "short-circuit")
		require.ErrorContains(t, err2, "tombstoned this run")

		require.Equal(t, int32(1), callCount.Load(), "the failing URL must be hit only once across both runs")
		require.Contains(t, logger.String(), "short-circuit")
	})

	t.Run("shared URL 200 still hits server once", func(t *testing.T) {
		t.Parallel()

		var callCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount.Add(1)
			_, _ = fmt.Fprint(w, `<span>450.75</span>`)
		}))
		defer srv.Close()

		logger := threadsafe.NewBuffer(nil)
		rateRepo := &mockRateValueRepository{}
		ext, err := NewRateExtractorWithHTTPClient(
			rateRepo,
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		srcA := &domain.RateSource{Name: "a", URL: srv.URL, Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `<span>([\d.]+)</span>`}}}
		srcB := &domain.RateSource{Name: "b", URL: srv.URL, Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `<span>([\d.]+)</span>`}}}

		require.NoError(t, ext.Run(t.Context(), srcA))
		require.NoError(t, ext.Run(t.Context(), srcB))

		require.Equal(t, int32(1), callCount.Load(), "positive cache must serve second source without a new HTTP request")
		require.Len(t, rateRepo.retained, 2, "both sources must persist their own rate value")

		_, poisoned := ext.loadFailedURL(srv.URL)
		require.False(t, poisoned, "a successful fetch must not be recorded as failed")
	})

	t.Run("unrelated URLs failure does not poison other URL", func(t *testing.T) {
		t.Parallel()

		var callCount500 atomic.Int32
		srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount500.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv500.Close()

		var callCount200 atomic.Int32
		srv200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount200.Add(1)
			_, _ = fmt.Fprint(w, `<span>100.0</span>`)
		}))
		defer srv200.Close()

		logger := threadsafe.NewBuffer(nil)
		rateRepo := &mockRateValueRepository{}
		ext, err := NewRateExtractorWithHTTPClient(
			rateRepo,
			&http.Client{Timeout: 5 * time.Second},
			logger,
		)
		require.NoError(t, err)

		src500 := &domain.RateSource{Name: "fail_src", URL: srv500.URL, Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `.+`}}}
		src200 := &domain.RateSource{Name: "ok_src", URL: srv200.URL, Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `<span>([\d.]+)</span>`}}}

		require.Error(t, ext.Run(t.Context(), src500))
		require.NoError(t, ext.Run(t.Context(), src200))

		require.Equal(t, int32(1), callCount500.Load(), "failing URL must be hit exactly once")
		require.Equal(t, int32(1), callCount200.Load(), "unrelated URL must be hit exactly once")
		require.Len(t, rateRepo.retained, 1, "only the successful source must persist a rate value")
	})

	t.Run("unreachable host short-circuits second attempt", func(t *testing.T) {
		t.Parallel()

		logger := threadsafe.NewBuffer(nil)
		ext, err := NewRateExtractorWithHTTPClient(
			&mockRateValueRepository{},
			&http.Client{Timeout: 500 * time.Millisecond},
			logger,
		)
		require.NoError(t, err)

		srcA := &domain.RateSource{Name: "a", URL: "http://127.0.0.1:1", Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `.+`}}}
		srcB := &domain.RateSource{Name: "b", URL: "http://127.0.0.1:1", Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: `.+`}}}

		err1 := ext.Run(t.Context(), srcA)
		require.Error(t, err1)

		start := time.Now()
		err2 := ext.Run(t.Context(), srcB)
		elapsed := time.Since(start)

		require.Error(t, err2)
		require.ErrorContains(t, err2, "short-circuit")
		require.ErrorContains(t, err2, "tombstoned this run")
		require.Less(t, elapsed, 400*time.Millisecond, "short-circuit must not wait for transport timeout; elapsed=%v", elapsed)
		require.Contains(t, logger.String(), "short-circuit")
	})
}

type mockRateValueRepository struct {
	err      error
	retained []*domain.RateValue
}

func (m *mockRateValueRepository) RetainRateValue(_ context.Context, rate *domain.RateValue) error {
	if m.err != nil {
		return m.err
	}
	m.retained = append(m.retained, rate)
	return nil
}

// emptyBodyTransport is an http.RoundTripper returning a 200 with an empty body
// (Read returns (0, io.EOF)). It lets the page-is-nil subtest verify io.ReadAll
// yields []byte{} (not nil), so Run's payload-nil guard is unreachable here.
type emptyBodyTransport struct{}

var _ http.RoundTripper = &emptyBodyTransport{}

func (emptyBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// TestRateExtractorPerSourceProxy covers routing: which of the two clients a source
// fetches through, and that the two routes do not share cache or tombstone slots.
//
// The proxied client is a real http.Transport with Proxy set, pointed at an httptest
// server standing in for the proxy. A plain-HTTP proxy receives the request itself
// (absolute-URI form) rather than a CONNECT, so the stand-in can answer directly and
// the assertion covers actual routing rather than which struct field was read.
func TestRateExtractorPerSourceProxy(t *testing.T) {
	t.Parallel()

	newRouted := func(t *testing.T, directBody, proxyBody string) (
		ext *RateExtractor, originURL string, log *threadsafe.Buffer,
		directHits, proxyHits *atomic.Int32,
	) {
		t.Helper()
		directHits, proxyHits = &atomic.Int32{}, &atomic.Int32{}

		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			directHits.Add(1)
			_, _ = fmt.Fprint(w, directBody)
		}))
		t.Cleanup(origin.Close)

		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			proxyHits.Add(1)
			_, _ = fmt.Fprint(w, proxyBody)
		}))
		t.Cleanup(proxy.Close)

		proxyTarget, err := url.Parse(proxy.URL)
		require.NoError(t, err)

		log = threadsafe.NewBuffer(nil)
		ext, err = NewRateExtractorWithHTTPClients(
			&mockRateValueRepository{},
			// srv.Client() rather than a bare &http.Client{}: a nil Transport shares
			// http.DefaultTransport across parallel tests. The explicit timeout keeps
			// a hung routing test failing in seconds — see timedClient.
			timedClient(origin.Client()),
			&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxyTarget)}},
			log,
		)
		require.NoError(t, err)
		return ext, origin.URL, log, directHits, proxyHits
	}

	t.Run("a source without the flag fetches direct", func(t *testing.T) {
		t.Parallel()
		ext, origin, _, directHits, proxyHits := newRouted(t, "DIRECT", "PROXIED")

		body, err := ext.fetchHtmlPage(t.Context(), origin, nil, false)
		require.NoError(t, err)
		require.Equal(t, []byte("DIRECT"), body)
		require.Equal(t, int32(1), directHits.Load())
		require.Equal(t, int32(0), proxyHits.Load(), "an unflagged source must never reach the proxy")
	})

	t.Run("a flagged source fetches through the proxy", func(t *testing.T) {
		t.Parallel()
		ext, origin, log, directHits, proxyHits := newRouted(t, "DIRECT", "PROXIED")

		body, err := ext.fetchHtmlPage(t.Context(), origin, nil, true)
		require.NoError(t, err)
		require.Equal(t, []byte("PROXIED"), body)
		require.Equal(t, int32(1), proxyHits.Load())
		require.Equal(t, int32(0), directHits.Load())
		require.Contains(t, log.String(), "via=proxy")
	})

	t.Run("two sources on one URL do not share a cache slot across routes", func(t *testing.T) {
		t.Parallel()
		// This is the collision the batched Yahoo sources made reachable: 20 of them
		// share one URL by design, so a URL-only key would hand whichever fetched
		// first to the other route as well.
		ext, origin, _, directHits, proxyHits := newRouted(t, "DIRECT", "PROXIED")

		direct, err := ext.fetchHtmlPage(t.Context(), origin, nil, false)
		require.NoError(t, err)
		proxied, err := ext.fetchHtmlPage(t.Context(), origin, nil, true)
		require.NoError(t, err)

		require.Equal(t, []byte("DIRECT"), direct)
		require.Equal(t, []byte("PROXIED"), proxied, "the proxied fetch must not be served the direct response")
		require.Equal(t, int32(1), directHits.Load())
		require.Equal(t, int32(1), proxyHits.Load())
	})

	t.Run("a failure on one route does not tombstone the other", func(t *testing.T) {
		t.Parallel()
		// The tombstone stops one dead source burning a whole run. Keyed on the URL
		// alone it would also stop a source that reaches the same host by the other
		// route and would have succeeded.
		var proxyHits atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		t.Cleanup(origin.Close)
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			proxyHits.Add(1)
			_, _ = fmt.Fprint(w, "PROXIED")
		}))
		t.Cleanup(proxy.Close)
		proxyTarget, err := url.Parse(proxy.URL)
		require.NoError(t, err)

		ext, err := NewRateExtractorWithHTTPClients(
			&mockRateValueRepository{},
			timedClient(origin.Client()),
			&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxyTarget)}},
			threadsafe.NewBuffer(nil),
		)
		require.NoError(t, err)

		_, err = ext.fetchHtmlPage(t.Context(), origin.URL, nil, false)
		require.Error(t, err, "the direct fetch is expected to fail and tombstone its own route")

		body, err := ext.fetchHtmlPage(t.Context(), origin.URL, nil, true)
		require.NoError(t, err, "the proxied route must not inherit the direct route's tombstone")
		require.Equal(t, []byte("PROXIED"), body)
		require.Equal(t, int32(1), proxyHits.Load())
	})

	t.Run("a flagged source falls back to direct when no proxy is configured", func(t *testing.T) {
		t.Parallel()
		var directHits atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			directHits.Add(1)
			_, _ = fmt.Fprint(w, "DIRECT")
		}))
		t.Cleanup(origin.Close)

		log := threadsafe.NewBuffer(nil)
		ext, err := NewRateExtractorWithHTTPClients(
			&mockRateValueRepository{}, timedClient(origin.Client()), nil, log,
		)
		require.NoError(t, err)

		body, err := ext.fetchHtmlPage(t.Context(), origin.URL, nil, true)
		require.NoError(t, err, "a missing BEACON_PROXY_URL must not stop a source that reaches its host direct")
		require.Equal(t, []byte("DIRECT"), body)
		require.Equal(t, int32(1), directHits.Load())
		require.Contains(t, log.String(), "requests the proxy but none is configured")
	})

	t.Run("Run passes the source flag through", func(t *testing.T) {
		t.Parallel()
		ext, origin, _, directHits, proxyHits := newRouted(t, "1.5", "2.5")

		source := &domain.RateSource{
			Name:          "TEST_PROXIED",
			URL:           origin,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Options:       domain.RateSourceOptions{UseProxy: true},
			Rules:         []domain.RateSourceRule{{Method: domain.MethodParseFloat}},
		}
		require.NoError(t, ext.Run(t.Context(), source))
		require.Equal(t, int32(1), proxyHits.Load())
		require.Equal(t, int32(0), directHits.Load())
	})
}

// TestRateSourceOptionsUseProxyDefault pins that the flag is absent-means-direct, so a
// source row written before the option existed, or one that simply omits it, is never
// routed through the proxy.
func TestRateSourceOptionsUseProxyDefault(t *testing.T) {
	t.Parallel()

	var opts domain.RateSourceOptions
	require.NoError(t, json.Unmarshal([]byte(`{"headers":{"User-Agent":"x"}}`), &opts))
	require.False(t, opts.UseProxy)

	encoded, err := json.Marshal(domain.RateSourceOptions{})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "use_proxy",
		"the default must not be written back into stored options")
}

// timedClient gives an httptest client a short request timeout. A zero Timeout is no
// longer a trap — fetchHtmlPage substitutes defaultFetchTimeout for it — but that
// default is a minute, and a routing test that somehow hangs should fail in seconds
// rather than sit there. The zero-timeout path has its own test on the constructor.
func timedClient(c *http.Client) *http.Client {
	c.Timeout = 5 * time.Second
	return c
}
