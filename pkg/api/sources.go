// Package api defines the JSON DTOs exchanged between the HTTP server and the WASM client.
package api

import "github.com/seilbekskindirov/monitor/internal/domain"

// SourceResponse is the JSON representation of a configured rate source,
// decorated with its most recent execution status.
type SourceResponse struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	BaseCurrency  string `json:"base_currency"`
	QuoteCurrency string `json:"quote_currency"`
	Interval      string `json:"interval"`
	Active        bool   `json:"active"`
	LastSuccess   bool   `json:"last_success"`
	LastError     string `json:"last_error,omitempty"`
	LastRunAt     string `json:"last_run_at,omitempty"`
}

// SourceActiveRequest is the body of PATCH /api/sources/{name}/active.
type SourceActiveRequest struct {
	Active bool `json:"active"`
}

// RulegenRequest is the body of POST /api/sources/{name}/rules/generate.
// All fields are optional; absent fields use CLI defaults
// (max_primary_attempts=3, max_fallback_attempts=2, force_fallback=false).
// Values outside [1, 10] for attempt-count fields are rejected with 400.
type RulegenRequest struct {
	ForceFallback       bool `json:"force_fallback,omitempty"`
	MaxPrimaryAttempts  int  `json:"max_primary_attempts,omitempty"`
	MaxFallbackAttempts int  `json:"max_fallback_attempts,omitempty"`
}

// RulegenResponse is the body returned on HTTP 200 from
// POST /api/sources/{name}/rules/generate.
type RulegenResponse struct {
	Source string `json:"source"`
	// Value is the rate value extracted by the newly generated rules, used to verify correctness.
	Value float64                 `json:"value"`
	Rules []domain.RateSourceRule `json:"rules"`
	// AttemptsUsed is the number of LLM generation attempts consumed before a passing rule set was found.
	AttemptsUsed int `json:"attempts_used"`
	// Escalated is true when the primary model failed and the fallback model was used.
	Escalated bool   `json:"escalated"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	// GeneratedAt is the RFC3339 UTC timestamp of generation.
	GeneratedAt string `json:"generated_at"`
}
