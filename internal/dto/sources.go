// Package dto defines the JSON DTOs exchanged between the HTTP server and the WASM client.
package dto

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

// SourceActiveRequest is the body of PATCH /api/v1/sources/{name}/active.
type SourceActiveRequest struct {
	Active bool `json:"active"`
}
