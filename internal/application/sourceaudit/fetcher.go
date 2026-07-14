package sourceaudit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var _ Fetcher = (*httpFetcher)(nil)

// maxResponseBytes caps the body read from any audit fetch to guard against
// OOM from unexpectedly large responses; rate-source pages are KBs (KASE ~540 KB).
const maxResponseBytes = 10 << 20 // 10 MB

// DefaultUserAgent is the audit tool's User-Agent header, matching the
// production extractor value.
const DefaultUserAgent = "Beacon/1.0 (+https://github.com/seilbekskindirov/beacon)"

// FetchResult holds the response from a single HTTP GET.
type FetchResult struct {
	Body        []byte
	ContentType string
	StatusCode  int
}

// Fetcher performs HTTP GETs and returns the body with metadata.
// headers are per-source overrides applied after the default User-Agent; nil
// is safe and results in only the default header being sent.
type Fetcher interface {
	Fetch(ctx context.Context, url string, headers map[string]string) (*FetchResult, error)
}

// NewHTTPFetcher constructs an httpFetcher with the given per-request timeout.
// proxyURL is an optional HTTP proxy URL (e.g. "http://127.0.0.1:7788"); pass ""
// for no proxy.
//
// When proxyURL is empty an explicit &http.Transport{} with no Proxy field is
// used: a nil Transport would fall back to http.DefaultTransport, whose Proxy
// reads HTTPS_PROXY/HTTP_PROXY from the environment and would silently route
// traffic the caller never configured.
func NewHTTPFetcher(timeout time.Duration, proxyURL string) (Fetcher, error) {
	var client *http.Client
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, errors.New("parse proxy URL: invalid format (value redacted from log; check the configured proxy URL)")
		}
		client = &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		}
	} else {
		client = &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{}, // empty transport — no Proxy, no env auto-pickup
		}
	}
	return &httpFetcher{
		client:  client,
		timeout: timeout,
	}, nil
}

// httpFetcher is the production Fetcher implementation. Not tested directly
// against the network; coverage comes from cmd/doctor audit integration.
type httpFetcher struct {
	client  *http.Client
	timeout time.Duration
}

func (f *httpFetcher) Fetch(ctx context.Context, url string, headers map[string]string) (*FetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	// Per-source headers override defaults; applied after so source wins.
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: unexpected status %d (%s)", url, resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	return &FetchResult{
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		StatusCode:  resp.StatusCode,
	}, nil
}
