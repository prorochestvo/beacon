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

// FetchResult holds the response from a single HTTP GET.
type FetchResult struct {
	Body        []byte
	ContentType string
	StatusCode  int
}

// Fetcher performs HTTP GETs and returns the body with metadata.
// headers are per-source overrides applied after the User-Agent the fetcher was
// constructed with; nil is safe and results in only that header being sent.
type Fetcher interface {
	Fetch(ctx context.Context, url string, headers map[string]string) (*FetchResult, error)
}

// maxResponseBytes caps the body read from any audit fetch to guard against
// OOM from unexpectedly large responses; rate-source pages are KBs (KASE ~540 KB).
const maxResponseBytes = 10 << 20 // 10 MB

// httpFetcher is the production Fetcher implementation. Not tested directly
// against the network; coverage comes from cmd/doctor audit integration.
type httpFetcher struct {
	client    *http.Client
	timeout   time.Duration
	userAgent string
}

func (f *httpFetcher) Fetch(ctx context.Context, rawURL string, headers map[string]string) (*FetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)
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
		return nil, fmt.Errorf("fetch %s: unexpected status %d (%s)", rawURL, resp.StatusCode, resp.Status)
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

// NewHTTPFetcher constructs an httpFetcher with the given per-request timeout.
// proxyURL is an optional HTTP proxy URL (e.g. "http://127.0.0.1:7788"); pass ""
// for no proxy.
//
// userAgent is supplied by the caller rather than held here. This package audits
// arbitrary rate sources and has no business knowing whose project it serves; the
// composition root passes internal.UserAgent (#65).
//
// When proxyURL is empty an explicit &http.Transport{} with no Proxy field is
// used: a nil Transport would fall back to http.DefaultTransport, whose Proxy
// reads HTTPS_PROXY/HTTP_PROXY from the environment and would silently route
// traffic the caller never configured.
func NewHTTPFetcher(timeout time.Duration, proxyURL, userAgent string) (Fetcher, error) {
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
		client:    client,
		timeout:   timeout,
		userAgent: userAgent,
	}, nil
}
