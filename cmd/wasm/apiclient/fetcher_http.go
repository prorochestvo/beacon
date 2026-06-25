//go:build !js

package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpFetcher is a net/http-backed Fetcher for non-WASM consumers such as
// integration tests and future CLI tools. The production WASM build uses
// domFetcher in fetcher_wasm.go instead.
type httpFetcher struct {
	baseURL string
	client  *http.Client
}

var _ Fetcher = (*httpFetcher)(nil)

// NewHTTPFetcher returns a Fetcher backed by net/http. baseURL is prepended to
// every relative path passed to FetchJSON or FetchNoContent; a trailing slash
// is trimmed so paths like "/api/sources" join cleanly. httpClient is optional —
// when nil, a default with a 5 s timeout is used.
func NewHTTPFetcher(baseURL string, httpClient *http.Client) Fetcher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &httpFetcher{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  httpClient,
	}
}

func (f *httpFetcher) FetchJSON(ctx context.Context, method, url string, body any, headers map[string]string) ([]byte, error) {
	req, err := f.newRequest(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return raw, nil
}

func (f *httpFetcher) FetchNoContent(ctx context.Context, method, url string, body any, headers map[string]string) error {
	req, err := f.newRequest(ctx, method, url, body)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

// newRequest builds an *http.Request from the given parameters, prepending
// baseURL to the relative path and setting Content-Type when body is non-nil.
func (f *httpFetcher) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}
