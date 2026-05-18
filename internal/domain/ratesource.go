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
// Fields are empty for hand-seeded rules; populated by cmd/rulegen for
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

// RateSourceKind distinguishes bid and ask prices for a rate source.
type RateSourceKind string

const (
	// RateSourceKindBID indicates a bid (buy) price source.
	RateSourceKindBID = "BID"
	// RateSourceKindASK indicates an ask (sell) price source.
	RateSourceKindASK = "ASK"
)
