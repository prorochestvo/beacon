// Package sourceaudit probes seeded rate sources against the live web to verify
// that their extraction rules still return plausible numeric values.
package sourceaudit

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/tools/rateextractor"
)

// ProbeStatus describes the outcome of probing a single seeded source.
type ProbeStatus string

const (
	// StatusOK indicates the rule returned a plausible value.
	StatusOK ProbeStatus = "OK"
	// StatusFetchError indicates the HTTP request failed.
	StatusFetchError ProbeStatus = "FETCH_ERROR"
	// StatusHTTPNon2xx indicates the server returned a non-2xx status code.
	StatusHTTPNon2xx ProbeStatus = "HTTP_NON_2XX"
	// StatusRegexNoMatch indicates the regex rule found no match in the response body.
	StatusRegexNoMatch ProbeStatus = "REGEX_NO_MATCH"
	// StatusJSONPathError indicates the JSONPath rule could not be evaluated.
	StatusJSONPathError ProbeStatus = "JSON_PATH_ERROR"
	// StatusValueParseError indicates the extracted text could not be parsed as a valid rate.
	StatusValueParseError ProbeStatus = "VALUE_PARSE_ERROR"
	// StatusUnsupportedMethod indicates the rule uses an extraction method not supported by the auditor.
	StatusUnsupportedMethod ProbeStatus = "UNSUPPORTED_METHOD"
	// StatusUnsupportedHeaders indicates the source requires request headers the auditor does not set.
	StatusUnsupportedHeaders ProbeStatus = "UNSUPPORTED_HEADERS"
)

// ProbeResult is the outcome of auditing one seeded source.
type ProbeResult struct {
	Source  SeededSource
	URL     string
	Status  ProbeStatus
	Value   string
	Detail  string
	Body    []byte
	Content string
}

// Auditor orchestrates fetching and extraction for a list of seeded sources.
type Auditor struct {
	Fetcher Fetcher
}

// Run audits all sources sequentially, deduplicating fetches by (URL, headers).
// Two sources sharing a URL with empty/nil headers reuse the same fetch body;
// sources with differing per-source headers each receive their own request.
// The output slice is parallel to sources.
func (a *Auditor) Run(ctx context.Context, sources []SeededSource) ([]ProbeResult, error) {
	cache := make(map[string]*fetchEntry)

	type fetchWork struct {
		url     string
		headers map[string]string
	}
	workByKey := make(map[string]fetchWork, len(sources))
	keyOrder := make([]string, 0, len(sources))

	for _, s := range sources {
		k := fetchCacheKey(s.URL, s.Headers)
		if _, exists := workByKey[k]; !exists {
			workByKey[k] = fetchWork{url: s.URL, headers: s.Headers}
			keyOrder = append(keyOrder, k)
		}
	}

	for _, k := range keyOrder {
		w := workByKey[k]
		res, err := a.Fetcher.Fetch(ctx, w.url, w.headers)
		cache[k] = &fetchEntry{result: res, err: err}
	}

	results := make([]ProbeResult, len(sources))
	for i, src := range sources {
		results[i] = a.probeSource(src, cache[fetchCacheKey(src.URL, src.Headers)])
	}
	return results, nil
}

// fetchCacheKey returns a stable dedup key for (rawURL, headers). Sources sharing
// the same URL and empty/nil headers reuse the same fetch (the existing "shared
// URL fetched once" optimization). Sources that differ in their per-source headers
// each receive an independent fetch.
func fetchCacheKey(rawURL string, headers map[string]string) string {
	if len(headers) == 0 {
		return rawURL
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(rawURL)
	for _, k := range keys {
		b.WriteByte('\x00') // NUL is not valid in URLs; safe separator
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(headers[k])
	}
	return b.String()
}

func (a *Auditor) probeSource(src SeededSource, fetch *fetchEntry) ProbeResult {
	pr := ProbeResult{
		Source: src,
		URL:    src.URL,
	}

	if err := validateSourceURL(src.URL); err != nil {
		pr.Status = StatusFetchError
		pr.Detail = err.Error()
		return pr
	}

	if fetch.err != nil {
		pr.Status = StatusFetchError
		pr.Detail = fetch.err.Error()
		return pr
	}

	pr.Body = fetch.result.Body
	pr.Content = fetch.result.ContentType

	payload := make([]byte, len(fetch.result.Body))
	copy(payload, fetch.result.Body)

	for _, rule := range src.Rules {
		var (
			out []byte
			err error
		)
		switch rule.Method {
		case domain.MethodRegex:
			out, err = rateextractor.ApplyRegex(rule.Pattern, payload)
			if err != nil {
				pr.Status = StatusRegexNoMatch
				pr.Detail = truncate(err.Error(), 80)
				return pr
			}
		case domain.MethodJSONPath:
			out, err = rateextractor.ApplyJSONPath(rule.Pattern, payload)
			if err != nil {
				pr.Status = StatusJSONPathError
				pr.Detail = truncate(err.Error(), 80)
				return pr
			}
		case domain.MethodParseFloat, domain.MethodStoreToRate:
			pr.Status = StatusUnsupportedMethod
			pr.Detail = string(rule.Method)
			return pr
		default:
			pr.Status = StatusUnsupportedMethod
			pr.Detail = string(rule.Method)
			return pr
		}
		payload = bytes.TrimSpace(out)
	}

	payload = bytes.ReplaceAll(payload, []byte(","), []byte("."))
	payload = bytes.ReplaceAll(payload, []byte(" "), []byte(""))

	valueStr := string(payload)

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		pr.Status = StatusValueParseError
		pr.Detail = truncate(valueStr, 80)
		return pr
	}

	if math.IsNaN(value) || math.IsInf(value, 0) {
		pr.Status = StatusValueParseError
		pr.Detail = truncate(valueStr, 80)
		return pr
	}

	if value <= rateextractor.MinPlausibleRateValue || value > rateextractor.MaxPlausibleRateValue {
		pr.Status = StatusValueParseError
		pr.Detail = truncate(valueStr, 80)
		return pr
	}

	pr.Status = StatusOK
	pr.Value = valueStr
	return pr
}

type fetchEntry struct {
	result *FetchResult
	err    error
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// validateSourceURL rejects empty, malformed, or non-http(s) URLs to prevent
// SSRF from seed-file source URLs.
func validateSourceURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("source URL must not be empty")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("URL scheme %q is not allowed (only http/https)", u.Scheme)
	}
}
