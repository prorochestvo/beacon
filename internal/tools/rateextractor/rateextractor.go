// Package rateextractor fetches web pages and applies a pipeline of extraction
// rules (regex, JSONPath, parse_float, store_to_rate) to derive a numeric FX rate,
// then persists the result via rateValueRepository.
package rateextractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/tools/threadsafe"
)

// MinPlausibleRateValue rejects zero and negative extractions.
const MinPlausibleRateValue = 0.0

// MaxPlausibleRateValue rejects values larger than any plausible exchange rate.
const MaxPlausibleRateValue = math.MaxInt32

// NewRateExtractor creates a RateExtractor with HTTP clients configured for the
// given timeout.
//
// It always builds a direct client, and additionally a proxied one when proxyURL is
// non-empty. Which of the two a fetch uses is decided per source by
// RateSourceOptions.UseProxy, so proxyURL alone re-routes nothing — see fetchHtmlPage.
// The Go proxy env triplet (HTTPS_PROXY, HTTP_PROXY, NO_PROXY) is intentionally NOT
// consulted; proxy config is injected explicitly via BEACON_PROXY_URL.
//
// The extractor keeps a per-process negative URL cache (tombstone): once a URL
// fails, later fetches in the same process short-circuit. Built for short-lived
// one-shot processes; do not reuse an instance across cron invocations in a daemon.
func NewRateExtractor(
	rateValueRepository rateValueRepository,
	proxyURL string,
	timeout time.Duration,
	logger io.Writer,
) (*RateExtractor, error) {
	directClient := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{},
	}

	var proxyClient *http.Client
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			// Do not wrap %w — url.Error.Error() includes the raw URL, which may
			// carry userinfo credentials. The operator has the value in the env
			// file; flag only the parse failure.
			return nil, errors.New("parse proxy URL: invalid format (value redacted from log; check the configured proxy URL)")
		}
		proxyClient = &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		}
	}

	extractor, err := NewRateExtractorWithHTTPClients(rateValueRepository, directClient, proxyClient, logger)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	return extractor, nil
}

// NewRateExtractorWithHTTPClient creates a RateExtractor with a caller-supplied HTTP
// client and no proxied client, so every source fetches through it regardless of
// UseProxy. Use this in tests that do not exercise routing.
//
// Like NewRateExtractor, the extractor keeps a per-process negative URL cache
// (tombstone): once a URL fails, later fetches in the same process short-circuit.
// Built for short-lived one-shot processes; do not reuse an instance across cron
// invocations in a daemon.
func NewRateExtractorWithHTTPClient(
	rateValueRepository rateValueRepository,
	httpClient *http.Client,
	logger io.Writer,
) (*RateExtractor, error) {
	return NewRateExtractorWithHTTPClients(rateValueRepository, httpClient, nil, logger)
}

// NewRateExtractorWithHTTPClients creates a RateExtractor with caller-supplied direct
// and proxied HTTP clients. Use this in tests that exercise per-source routing.
//
// proxyClient may be nil, which models an unconfigured BEACON_PROXY_URL: a source
// asking for the proxy then falls back to direct rather than failing, and the fetch is
// logged as such — see fetchHtmlPage. httpClient is required.
func NewRateExtractorWithHTTPClients(
	rateValueRepository rateValueRepository,
	httpClient *http.Client,
	proxyClient *http.Client,
	logger io.Writer,
) (*RateExtractor, error) {
	if httpClient == nil {
		err := errors.New("http client cannot be nil")
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	p := &RateExtractor{
		RateValueRepository: rateValueRepository,
		cache:               threadsafe.NewCache(30 * time.Minute),
		httpClient:          httpClient,
		proxyClient:         proxyClient,
		logger:              logger,
		failedURLs:          make(map[string]error),
	}

	return p, nil
}

// RateExtractor fetches a URL, applies the source's rule pipeline, and persists
// the extracted rate value. Responses are cached in memory for 30 minutes to avoid
// redundant fetches when multiple sources share the same URL.
//
// httpClient is the direct route and is always set. proxyClient is set only when a
// proxy URL was configured; sources choose between them via RateSourceOptions.UseProxy.
type RateExtractor struct {
	RateValueRepository rateValueRepository
	cache               *threadsafe.Cache
	httpClient          *http.Client
	proxyClient         *http.Client
	logger              io.Writer
	failedURLs          map[string]error
	failedURLsMu        sync.Mutex
}

// Name returns the identifier used in scheduler and log output.
func (extractor *RateExtractor) Name() string {
	return "rate_extractor"
}

// Run fetches source.URL, applies all extraction rules in sequence, and persists
// the resulting rate value. Returns an error if any rule fails or the parsed value
// is outside [MinPlausibleRateValue, MaxPlausibleRateValue].
// Per-source headers from source.Options.Headers override the default User-Agent
// when provided, and source.Options.UseProxy selects the route; see fetchHtmlPage
// for the cache-key limitation.
func (extractor *RateExtractor) Run(ctx context.Context, source *domain.RateSource) error {
	payload, err := extractor.fetchHtmlPage(ctx, source.URL, source.Options.Headers, source.Options.UseProxy)
	if err != nil || payload == nil {
		if err == nil {
			err = errors.New("page is nil")
		}
		err = fmt.Errorf("could not read html page %v: %w", source.URL, err)
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	return applyRulesAndStore(ctx, source, payload, extractor.RateValueRepository)
}

// loadFailedURL returns the cached error for key and true if key was previously
// recorded as failed during the current process lifetime. key is the fetch key, not
// the bare URL — see fetchKey.
func (extractor *RateExtractor) loadFailedURL(key string) (error, bool) {
	extractor.failedURLsMu.Lock()
	defer extractor.failedURLsMu.Unlock()
	e, ok := extractor.failedURLs[key]
	return e, ok
}

// recordFailedURL stores err as the tombstone for key. Subsequent fetches matching key
// inside the same process short-circuit and return a wrapped form of err.
// See constructor godoc for lifetime constraint.
func (extractor *RateExtractor) recordFailedURL(key string, err error) {
	extractor.failedURLsMu.Lock()
	defer extractor.failedURLsMu.Unlock()
	extractor.failedURLs[key] = err
}

// fetchHtmlPage fetches rawURL and returns its body. The response is cached in memory
// for 30 minutes and a failure is tombstoned for the process lifetime, both under the
// fetch key rather than the bare URL.
//
// useProxy selects the proxied client. It only takes effect when one was configured:
// a source asking for a proxy that does not exist falls back to direct and says so in
// the log, because a missing environment variable should not stop collection of a
// source that reaches its host either way.
//
// headers are applied after the default User-Agent, so a non-nil entry overrides it.
// Headers are deliberately NOT part of the fetch key: sources sharing a URL are how
// batching works — the 20 Yahoo sources share one request by design — and they share
// identical headers, so the collision is benign. Two sources on one URL wanting
// different headers would still get whichever fetched first. Add headers to the key
// before creating such a pair.
func (extractor *RateExtractor) fetchHtmlPage(
	ctx context.Context, rawURL string, headers map[string]string, useProxy bool,
) ([]byte, error) {
	httpClient := extractor.httpClient
	route := "direct"
	switch {
	case useProxy && extractor.proxyClient != nil:
		httpClient = extractor.proxyClient
		route = "proxy"
	case useProxy:
		_, _ = fmt.Fprintf(extractor.logger,
			"rate_extractor: source requests the proxy but none is configured; fetching direct url=%s\n", rawURL)
		useProxy = false
	}

	key := fetchKey(rawURL, useProxy)

	if cached, ok := extractor.loadFailedURL(key); ok {
		_, _ = fmt.Fprintf(extractor.logger,
			"rate_extractor: short-circuit url=%s via=%s prior_error=%v\n", rawURL, route, cached)
		err := fmt.Errorf("short-circuit (tombstoned this run): %w", cached)
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, httpClient.Timeout)
	defer cancel()

	if page, err := extractor.cache.Fetch(key); err == nil {
		if b, ok := page.([]byte); ok && len(b) > 0 {
			return b, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		err = fmt.Errorf("create request: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(key, err)
		return nil, err
	}

	req.Header.Set("User-Agent", "Beacon/1.0 (+https://github.com/seilbekskindirov/beacon)")
	// Per-source headers override defaults; applied after so source wins.
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	_, _ = fmt.Fprintf(extractor.logger, "rate_extractor: fetching url %s via=%s\n", rawURL, route)

	resp, err := httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("do request: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(key, err)
		return nil, err
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("fetch %s: unexpected status %d (%s)", rawURL, resp.StatusCode, resp.Status)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(key, err)
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		err = fmt.Errorf("read response body: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(key, err)
		return nil, err
	}

	if err = extractor.cache.Push(key, body); err != nil {
		_, _ = extractor.cache.Pull(key) // ensure cache is clean if push failed
		err = errors.Join(err, loginjector.NewTraceError())
		_, _ = fmt.Fprintf(extractor.logger, "rate_extractor: could not push response payload to cache: %v", err)
	}

	return body, nil
}

// maxResponseBytes caps the body read from any rate-source URL to guard against
// OOM from unexpectedly large responses; rate-source pages are KBs (KASE ~540 KB).
const maxResponseBytes = 10 << 20 // 10 MB

// rateValueRepository is the narrow persistence interface required by RateExtractor.
type rateValueRepository interface {
	RetainRateValue(ctx context.Context, rate *domain.RateValue) error
}

// fetchKey identifies a fetch for both the response cache and the failure tombstone.
//
// The route is part of the key because the same URL fetched direct and through the
// proxy are different requests that can legitimately give different answers — or one
// answer and one failure. Keying on the URL alone would let whichever ran first decide
// for both, and would let a direct failure tombstone a proxied source that would have
// succeeded. Headers are still not in the key: see fetchHtmlPage.
func fetchKey(rawURL string, useProxy bool) string {
	route := "direct"
	if useProxy {
		route = "proxy"
	}
	return fmt.Sprintf("GET:%s:%s", route, rawURL)
}

// applyRulesAndStore executes the extraction rule pipeline on payload and
// persists the resulting rate value via repo. It is the shared rule-application
// core used by both the plain HTTP extractor and the chromedp extractor.
func applyRulesAndStore(ctx context.Context, source *domain.RateSource, payload []byte, repo rateValueRepository) error {
	var err error

	for i, rule := range source.Rules {
		switch rule.Method {
		case domain.MethodParseFloat:
			var f float64
			p := bytes.ReplaceAll(payload, []byte(" "), []byte(""))
			p = bytes.ReplaceAll(p, []byte(","), []byte("."))
			f, err = strconv.ParseFloat(string(p), 10)
			if err != nil {
				err = fmt.Errorf("could not parse rate value %s: %s", string(payload), err.Error())
				err = errors.Join(err, loginjector.NewTraceError())
				return err
			}
			payload = []byte(fmt.Sprintf("%.3f", f))
		case domain.MethodRegex:
			payload, err = ApplyRegex(rule.Pattern, payload)
			if err != nil {
				err = errors.Join(err, fmt.Errorf("rule %d: apply regex pattern %q: %w", i, rule.Pattern, err))
				err = errors.Join(err, loginjector.NewTraceError())
				return err
			}
		case domain.MethodJSONPath:
			payload, err = ApplyJSONPath(rule.Pattern, payload)
			if err != nil {
				err = errors.Join(err, fmt.Errorf("rule %d: apply json_path %q: %w", i, rule.Pattern, err))
				err = errors.Join(err, loginjector.NewTraceError())
				return err
			}
		case domain.MethodStoreToRate:
		default:
			err = fmt.Errorf("unsupported extraction method: %s", rule.Method)
			err = errors.Join(err, loginjector.NewTraceError())
			return err
		}
		payload = bytes.TrimSpace(payload)
	}

	payload = bytes.ReplaceAll(payload, []byte(","), []byte("."))
	payload = bytes.ReplaceAll(payload, []byte(" "), []byte(""))

	value, err := strconv.ParseFloat(string(payload), 64)
	if err != nil {
		err = fmt.Errorf("parse extracted value %q: %s", payload, err.Error())
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	if math.IsNaN(value) || math.IsInf(value, 0) {
		err = fmt.Errorf("extracted value is NaN or Inf for source %s", source.Name)
		return errors.Join(err, loginjector.NewTraceError())
	}

	if value <= MinPlausibleRateValue || value > MaxPlausibleRateValue {
		err = fmt.Errorf("invalid rate value: %s", string(payload))
		err = fmt.Errorf("parse extracted value %q: %s", payload, err.Error())
		return errors.Join(err, loginjector.NewTraceError())
	}

	rateValue := &domain.RateValue{
		SourceName:    source.Name,
		BaseCurrency:  source.BaseCurrency,
		QuoteCurrency: source.QuoteCurrency,
		Price:         value,
		Timestamp:     time.Now().UTC(),
	}

	err = repo.RetainRateValue(ctx, rateValue)
	if err != nil {
		err = errors.Join(fmt.Errorf("could not keep the %f rate value of %s", value, source.Name), err)
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	return nil
}
