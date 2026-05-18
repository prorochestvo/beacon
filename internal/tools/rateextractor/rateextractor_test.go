package rateextractor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/repository"
	"github.com/seilbekskindirov/monitor/internal/tools/threadsafe"
	"github.com/stretchr/testify/require"
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
}

func TestNewRateExtractor(t *testing.T) {
	t.Parallel()

	logger := threadsafe.NewBuffer(nil)

	t.Run("NoProxy", func(t *testing.T) {
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
	t.Run("InvalidProxyURL", func(t *testing.T) {
		t.Parallel()

		_, err := NewRateExtractor(
			&mockRateValueRepository{},
			"://bad url",
			5*time.Second,
			logger,
		)
		require.Error(t, err)
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
		// The guard `if err == nil { err = errors.New("page is nil") }` in Run
		// defends against a hypothetical fetchHtmlPage returning (nil, nil).
		// In practice, fetchHtmlPage returns nil only on error, and io.ReadAll on
		// an empty body yields []byte{} (non-nil), so payload == nil is unreachable
		// through the HTTP path. We exercise the defensive branch by injecting a
		// custom RoundTripper whose body returns (0, io.EOF) immediately; io.ReadAll
		// of that yields []byte{} rather than nil, so the nil guard still does not
		// fire and we instead get a parse error on the empty payload. This subtest
		// documents that the nil-synthesis branch is unreachable from outside the
		// package via the transport API, and asserts the adjacent empty-body behaviour.
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

		// io.ReadAll returns []byte{} for an empty body — not nil — so the
		// payload-nil guard does not fire. The extractor fails later (no regex
		// match on empty payload). The error must NOT contain "page is nil".
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

		body, err := ext.fetchHtmlPage(t.Context(), srv.URL)
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

		body1, err := ext.fetchHtmlPage(t.Context(), srv.URL)
		require.NoError(t, err)
		require.Equal(t, []byte(responseBody), body1)

		body2, err := ext.fetchHtmlPage(t.Context(), srv.URL)
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

		_, err = ext.fetchHtmlPage(t.Context(), "://bad")
		require.Error(t, err)
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

		// Pre-populate the cache with the same key but an empty value so that:
		//   1. Fetch() finds the key but the empty-byte guard skips the cache hit.
		//   2. After the real HTTP fetch, Push() fails (key already exists in go-cache.Add).
		//   3. fetchHtmlPage logs the error and still returns the fetched body with nil error.
		cacheKey := fmt.Sprintf("GET:%s", srv.URL)
		require.NoError(t, ext.cache.Push(cacheKey, []byte{}))

		body, err := ext.fetchHtmlPage(t.Context(), srv.URL)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, []byte(responseBody), body)
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

// emptyBodyTransport is an http.RoundTripper that returns a 200 response with
// an empty body (Read immediately returns (0, io.EOF)). Used in the
// "empty body yields non-nil slice so page-is-nil guard is unreachable via transport"
// subtest to verify that io.ReadAll of an empty body yields []byte{} (not nil),
// meaning the payload-nil guard in Run is unreachable from this transport path.
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
