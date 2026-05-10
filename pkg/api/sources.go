// Package api defines the HTTP request and response types shared by the server
// and the WASM frontend client.
package api

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
