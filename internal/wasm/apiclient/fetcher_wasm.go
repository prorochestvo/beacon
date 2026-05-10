//go:build js && wasm

package apiclient

import (
	"context"
	"encoding/json"

	"github.com/seilbekskindirov/monitor/internal/wasm/dom"
)

// domFetcher is the production Fetcher backed by dom.FetchJSON and
// dom.FetchNoContent. It is only compiled under js+wasm.
type domFetcher struct{}

// NewDOMFetcher returns a Fetcher backed by the browser's window.fetch API.
// Construct one per WASM lifetime and pass it to New.
func NewDOMFetcher() Fetcher { return domFetcher{} }

func (domFetcher) FetchJSON(ctx context.Context, method, url string, body any, headers map[string]string) ([]byte, error) {
	raw, err := dom.FetchJSON[json.RawMessage](ctx, method, url, body, headers)
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

func (domFetcher) FetchNoContent(ctx context.Context, method, url string, body any, headers map[string]string) error {
	return dom.FetchNoContent(ctx, method, url, body, headers)
}
