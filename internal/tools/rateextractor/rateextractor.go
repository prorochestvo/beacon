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
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/tools/threadsafe"
)

// MinPlausibleRateValue rejects zero and negative extractions.
const MinPlausibleRateValue = 0.0

// MaxPlausibleRateValue rejects values larger than any plausible exchange rate.
const MaxPlausibleRateValue = math.MaxInt32

// NewRateExtractor creates a RateExtractor with an HTTP client configured for the
// given timeout. When proxyURL is non-empty the client routes requests through that proxy.
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
			return nil, fmt.Errorf("parse proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	extractor, err := NewRateExtractorWithHTTPClient(rateValueRepository, httpClient, logger)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	return extractor, nil
}

// NewRateExtractorWithHTTPClient creates a RateExtractor with a caller-supplied HTTP
// client. Use this in tests to inject a custom transport or timeout.
func NewRateExtractorWithHTTPClient(
	rateValueRepository rateValueRepository,
	httpClient *http.Client,
	logger io.Writer,
) (*RateExtractor, error) {
	if httpClient == nil {
		err := errors.New("http client cannot be nil")
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	p := &RateExtractor{
		RateValueRepository: rateValueRepository,
		cache:               threadsafe.NewCache(30 * time.Minute),
		httpClient:          httpClient,
		logger:              logger,
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
}

// Name returns the identifier used in scheduler and log output.
func (extractor *RateExtractor) Name() string {
	return "rate_extractor"
}

// Run fetches source.URL, applies all extraction rules in sequence, and persists
// the resulting rate value. Returns an error if any rule fails or the parsed value
// is outside [MinPlausibleRateValue, MaxPlausibleRateValue].
func (extractor *RateExtractor) Run(ctx context.Context, source *domain.RateSource) error {
	payload, err := extractor.fetchHtmlPage(ctx, source.URL)
	if err != nil || payload == nil {
		if err != nil {
			err = errors.New("page is nil")
		}
		err = fmt.Errorf("could not read html page %v: %w", source.URL, err)
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	for i, rule := range source.Rules {
		switch rule.Method {
		case domain.MethodParseFloat:
			var f float64
			p := bytes.ReplaceAll(payload, []byte(" "), []byte(""))
			p = bytes.ReplaceAll(p, []byte(","), []byte("."))
			f, err = strconv.ParseFloat(string(p), 10)
			if err != nil {
				err = fmt.Errorf("could not parse rate value %s: %s", string(payload), err.Error())
				err = errors.Join(err, internal.NewTraceError())
				return err
			}
			payload = []byte(fmt.Sprintf("%.3f", f))
		case domain.MethodRegex:
			payload, err = extractor.fetchRegexPage(ctx, rule.Pattern, payload)
			if err != nil {
				err = errors.Join(err, fmt.Errorf("rule %d: apply regex pattern %q: %w", i, rule.Pattern, err))
				err = errors.Join(err, internal.NewTraceError())
				return err
			}
		case domain.MethodJSONPath:
			payload, err = extractor.extractJSONPath(rule.Pattern, payload)
			if err != nil {
				err = errors.Join(err, fmt.Errorf("rule %d: apply json_path %q: %w", i, rule.Pattern, err))
				err = errors.Join(err, internal.NewTraceError())
				return err
			}
		case domain.MethodStoreToRate:
		default:
			err = fmt.Errorf("unsupported extraction method: %s", rule.Method)
			err = errors.Join(err, internal.NewTraceError())
			return err
		}
		payload = bytes.TrimSpace(payload)
	}

	payload = bytes.ReplaceAll(payload, []byte(","), []byte("."))
	payload = bytes.ReplaceAll(payload, []byte(" "), []byte(""))

	value, err := strconv.ParseFloat(string(payload), 64)
	if err != nil {
		err = fmt.Errorf("parse extracted value %q: %s", payload, err.Error())
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	if math.IsNaN(value) || math.IsInf(value, 0) {
		err = fmt.Errorf("extracted value is NaN or Inf for source %s", source.Name)
		return errors.Join(err, internal.NewTraceError())
	}

	if value <= MinPlausibleRateValue || value > MaxPlausibleRateValue {
		err = fmt.Errorf("invalid rate value: %s", string(payload))
		err = fmt.Errorf("parse extracted value %q: %s", payload, err.Error())
		return errors.Join(err, internal.NewTraceError())
	}

	rateValue := &domain.RateValue{
		SourceName:    source.Name,
		BaseCurrency:  source.BaseCurrency,
		QuoteCurrency: source.QuoteCurrency,
		Price:         value,
		Timestamp:     time.Now().UTC(),
	}

	err = extractor.RateValueRepository.RetainRateValue(ctx, rateValue)
	if err != nil {
		err = errors.Join(fmt.Errorf("could not keep the %f rate value of %s", value, source.Name), err)
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	return nil
}

func (extractor *RateExtractor) fetchHtmlPage(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, extractor.httpClient.Timeout)
	defer cancel()

	cacheKey := fmt.Sprintf("GET:%s", rawURL)
	if page, err := extractor.cache.Fetch(cacheKey); err == nil {
		if b, ok := page.([]byte); ok && len(b) > 0 {
			return b, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		err = fmt.Errorf("create request: %w", err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	req.Header.Set("User-Agent", "FXRateMonitor/1.0 (+https://github.com/seilbekskindirov/monitor)")

	_, _ = fmt.Fprintf(extractor.logger, "rate_extractor: fetching url %s\n", rawURL)

	resp, err := extractor.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("do request: %w", err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("fetch %s: unexpected status %d (%s)", rawURL, resp.StatusCode, resp.Status)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("read response body: %w", err)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	if err = extractor.cache.Push(cacheKey, body); err != nil {
		_, _ = extractor.cache.Pull(cacheKey) // ensure cache is clean if push failed
		err = errors.Join(err, internal.NewTraceError())
		_, _ = fmt.Fprintf(extractor.logger, "rate_extractor: could not push response payload to cache: %v", err)
	}

	return body, nil
}

func (extractor *RateExtractor) fetchRegexPage(_ context.Context, pattern string, payload []byte) ([]byte, error) {
	return ApplyRegex(pattern, payload)
}

// rateValueRepository is the narrow persistence interface required by RateExtractor.
type rateValueRepository interface {
	RetainRateValue(ctx context.Context, rate *domain.RateValue) error
}
