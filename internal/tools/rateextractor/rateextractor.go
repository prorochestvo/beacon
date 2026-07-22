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

// maxResponseBytes caps the body read from any rate-source URL to guard against
// OOM from unexpectedly large responses; rate-source pages are KBs (KASE ~540 KB).
const maxResponseBytes = 10 << 20 // 10 MB

// MinPlausibleRateValue rejects zero and negative extractions.
const MinPlausibleRateValue = 0.0

// MaxPlausibleRateValue rejects values larger than any plausible exchange rate.
const MaxPlausibleRateValue = math.MaxInt32

// NewRateExtractor creates a RateExtractor with an HTTP client configured for the
// given timeout.
//
// A non-empty proxyURL is parsed and used as the explicit proxy for all requests.
// An empty proxyURL uses no proxy — the Go proxy env triplet (HTTPS_PROXY,
// HTTP_PROXY, NO_PROXY) is intentionally NOT consulted; proxy config is injected
// explicitly via BEACON_PROXY_URL.
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
	transport := &http.Transport{}

	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			// Do not wrap %w — url.Error.Error() includes the raw URL, which may
			// carry userinfo credentials. The operator has the value in the env
			// file; flag only the parse failure.
			return nil, errors.New("parse proxy URL: invalid format (value redacted from log; check the configured proxy URL)")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	extractor, err := NewRateExtractorWithHTTPClient(rateValueRepository, httpClient, logger)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	return extractor, nil
}

// NewRateExtractorWithHTTPClient creates a RateExtractor with a caller-supplied HTTP
// client. Use this in tests to inject a custom transport or timeout.
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
	if httpClient == nil {
		err := errors.New("http client cannot be nil")
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	p := &RateExtractor{
		RateValueRepository: rateValueRepository,
		cache:               threadsafe.NewCache(30 * time.Minute),
		httpClient:          httpClient,
		logger:              logger,
		failedURLs:          make(map[string]error),
	}

	return p, nil
}

// RateExtractor fetches a URL, applies the source's rule pipeline, and persists
// the extracted rate value. Responses are cached in memory for 30 minutes to avoid
// redundant fetches when multiple sources share the same URL.
type RateExtractor struct {
	RateValueRepository rateValueRepository
	cache               *threadsafe.Cache
	httpClient          *http.Client
	logger              io.Writer
	failedURLs          map[string]error
	failedURLsMu        sync.Mutex
}

// rateValueRepository is the narrow persistence interface required by RateExtractor.
type rateValueRepository interface {
	RetainRateValue(ctx context.Context, rate *domain.RateValue) error
}

// Name returns the identifier used in scheduler and log output.
func (extractor *RateExtractor) Name() string {
	return "rate_extractor"
}

// Run fetches source.URL, applies all extraction rules in sequence, and persists
// the resulting rate value. Returns an error if any rule fails or the parsed value
// is outside [MinPlausibleRateValue, MaxPlausibleRateValue].
// Per-source headers from source.Options.Headers override the default User-Agent
// when provided; see fetchHtmlPage for the cache-key limitation.
func (extractor *RateExtractor) Run(ctx context.Context, source *domain.RateSource) error {
	payload, err := extractor.fetchHtmlPage(ctx, source.URL, source.Options.Headers)
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

// loadFailedURL returns the cached error for url and true if url was previously
// recorded as failed during the current process lifetime.
func (extractor *RateExtractor) loadFailedURL(url string) (error, bool) {
	extractor.failedURLsMu.Lock()
	defer extractor.failedURLsMu.Unlock()
	e, ok := extractor.failedURLs[url]
	return e, ok
}

// recordFailedURL stores err as the tombstone for url. Subsequent fetches of url
// inside the same process short-circuit and return a wrapped form of err.
// See constructor godoc for lifetime constraint.
func (extractor *RateExtractor) recordFailedURL(url string, err error) {
	extractor.failedURLsMu.Lock()
	defer extractor.failedURLsMu.Unlock()
	extractor.failedURLs[url] = err
}

// fetchHtmlPage fetches rawURL and returns its body. The response is cached in
// memory by URL for 30 minutes; a failed URL is tombstoned for the process lifetime.
//
// headers are applied after the default User-Agent, so a non-nil entry overrides
// it. Two sources sharing the same URL but needing different headers would share
// the same cache slot and return the first-fetched response — that is a known
// limitation; all current sources have unique URLs, so the collision is not
// reachable in production.
func (extractor *RateExtractor) fetchHtmlPage(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	if cached, ok := extractor.loadFailedURL(rawURL); ok {
		_, _ = fmt.Fprintf(extractor.logger,
			"rate_extractor: short-circuit url=%s prior_error=%v\n", rawURL, cached)
		err := fmt.Errorf("short-circuit (tombstoned this run): %w", cached)
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, extractor.httpClient.Timeout)
	defer cancel()

	// Cache key is URL-only; per-source headers are not part of the key, the same limitation
	// as failedURLs. Safe today because every source has a unique URL.
	cacheKey := fmt.Sprintf("GET:%s", rawURL)
	if page, err := extractor.cache.Fetch(cacheKey); err == nil {
		if b, ok := page.([]byte); ok && len(b) > 0 {
			return b, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		err = fmt.Errorf("create request: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(rawURL, err)
		return nil, err
	}

	req.Header.Set("User-Agent", "Beacon/1.0 (+https://github.com/seilbekskindirov/beacon)")
	// Per-source headers override defaults; applied after so source wins.
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	_, _ = fmt.Fprintf(extractor.logger, "rate_extractor: fetching url %s\n", rawURL)

	resp, err := extractor.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("do request: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(rawURL, err)
		return nil, err
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("fetch %s: unexpected status %d (%s)", rawURL, resp.StatusCode, resp.Status)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(rawURL, err)
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		err = fmt.Errorf("read response body: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		extractor.recordFailedURL(rawURL, err)
		return nil, err
	}

	if err = extractor.cache.Push(cacheKey, body); err != nil {
		_, _ = extractor.cache.Pull(cacheKey) // ensure cache is clean if push failed
		err = errors.Join(err, loginjector.NewTraceError())
		_, _ = fmt.Fprintf(extractor.logger, "rate_extractor: could not push response payload to cache: %v", err)
	}

	return body, nil
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
