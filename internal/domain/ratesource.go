// Package domain defines value types shared across the application.
package domain

// RateSource defines a monitored source and its extraction rules.
type RateSource struct {
	Name          string                 `json:"name"`
	Title         string                 `json:"title"`
	URL           string                 `json:"url"`
	Interval      string                 `json:"interval"`
	BaseCurrency  string                 `json:"base_currency"`
	QuoteCurrency string                 `json:"quote_currency"`
	Kind          RateSourceKind         `json:"kind"`
	Active        bool                   `json:"active"`
	FetcherKind   string                 `json:"fetcher_kind"`
	Options       RateSourceOptions      `json:"options"`
	Rules         []RateSourceRule       `json:"rules"`
	RuleMetadata  RateSourceRuleMetadata `json:"rule_metadata"`
}

// RateSourceRuleMetadata records how the extraction rule was produced.
// Fields are empty for hand-seeded rules; populated by cmd/doctor rulegen for
// LLM-generated rules.
type RateSourceRuleMetadata struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	AttemptsUsed int    `json:"attempts_used,omitempty"`
	GeneratedAt  string `json:"generated_at,omitempty"` // RFC3339 UTC; empty for hand-seeded rules
}

// RateSourceRule defines an extraction rule for a source.
type RateSourceRule struct {
	Method  Method `json:"method"`
	Pattern string `json:"pattern"`
	Options string `json:"options,omitempty"`
}

// RateSourceOptions holds optional fetcher-level parameters for a source.
type RateSourceOptions struct {
	// Reserve is an alternative URL used as a fallback when the primary URL fails.
	Reserve string `json:"reserve,omitempty"`
	// WaitSelector is a CSS selector the chromedp fetcher waits for before extracting content.
	WaitSelector string `json:"wait_selector,omitempty"`
	// Headers are extra HTTP request headers applied by the plain fetcher, overriding
	// defaults (e.g. a browser User-Agent for sources that reject the default). Ignored
	// by the chromedp fetcher. Empty for most sources.
	// WARNING: values are stored in plaintext in the database and in git-tracked migration
	// files. Do not store secrets (API keys, bearer tokens) here. For auth-gated sources
	// inject credentials at runtime via BEACON_* env vars / dsninjector and substitute
	// them in the collection layer.
	Headers map[string]string `json:"headers,omitempty"`
}

// Method identifies the extraction algorithm applied to raw page content.
type Method string

const (
	// MethodParseFloat parses the matched text as a floating-point number.
	MethodParseFloat Method = "parse_float"
	// MethodRegex applies a regular expression to extract a substring.
	MethodRegex Method = "regex"
	// MethodJSONPath evaluates a JSONPath expression against the response body.
	MethodJSONPath Method = "json"
	// MethodStoreToRate stores the processed value directly as the rate.
	MethodStoreToRate Method = "store_as_rate"
)

// RateSourceKind distinguishes bid, ask, and last-traded prices for a rate source.
type RateSourceKind string

const (
	// RateSourceKindBID indicates a bid (buy) price source.
	RateSourceKindBID RateSourceKind = "BID"
	// RateSourceKindASK indicates an ask (sell) price source.
	RateSourceKindASK RateSourceKind = "ASK"
	// RateSourceKindLAST indicates a single last-traded price (e.g. an equity
	// close/last-deal price) with no bid/ask direction.
	RateSourceKindLAST RateSourceKind = "LAST"
)
