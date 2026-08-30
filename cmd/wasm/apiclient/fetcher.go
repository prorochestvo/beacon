package apiclient

import "context"

// Fetcher abstracts the HTTP transport layer for the API client. The production
// implementation (domFetcher in fetcher_wasm.go) wraps dom.FetchJSON and
// dom.FetchNoContent under js+wasm; tests provide an in-memory fake.
//
// FetchJSON returns the raw response bytes so Client methods can decode them into
// the appropriate internal/dto type without the interface needing to be generic.
// FetchNoContent is for endpoints that return no body (e.g. 204).
//
// Non-2xx HTTP status codes are reported as errors — the caller need not inspect
// status independently.
type Fetcher interface {
	FetchJSON(ctx context.Context, method, url string, body any, headers map[string]string) ([]byte, error)
	FetchNoContent(ctx context.Context, method, url string, body any, headers map[string]string) error
}
