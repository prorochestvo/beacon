package inspector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// gismeteoProbeURL is the gismeteo.kz host URL used for the reachability probe.
	// A HEAD or GET to the root is cheap and does not trigger any scraping logic.
	gismeteoProbeURL = "https://www.gismeteo.kz/"

	// gismeteoInspectorUA is a browser-like User-Agent. gismeteo.kz may return 403
	// or redirect for requests with an empty UA — both count as "reachable" (non-5xx),
	// but some CDN configurations may time out on empty UA strings.
	gismeteoInspectorUA = "Beacon/1.0 health-check (+https://github.com/seilbekskindirov/beacon)"

	// gismeteoProbeTimeout is the per-request timeout for the health probe. It is
	// shorter than the agent sweep budget (3 s) to leave headroom for other inspectors.
	gismeteoProbeTimeout = 2 * time.Second
)

// GismeteoInspector is an advisory health inspector for the gismeteo.kz host.
// It issues a plain HTTP GET and asserts a non-5xx response. A 200, 301, 302, or 403
// all count as "reachable" — gismeteo's CDN may legitimately redirect or block a
// health probe UA while still being up. Only a 5xx or a transport error is a failure.
//
// This inspector is advisory: its failure is reported in the /health/check component
// map but does not flip the aggregate healthy flag. A gismeteo outage must not fail
// the deploy health-gate, which is outside the operator's control.
//
// The probe issues a direct HTTP request (no proxy). cmd/web does not parse
// BEACON_PROXY_URL, so the probe tests host→gismeteo reachability on the direct
// path. If the production environment requires a proxy for gismeteo egress,
// derive gismeteo health from collection execution history instead of this probe.
// A full chromedp render's cold-start can exceed the 3 s health budget, so this
// probe deliberately does not use Chromium.
type GismeteoInspector struct {
	client   *http.Client
	probeURL string
}

// NewGismeteoInspector returns an advisory Inspector for the gismeteo.kz host.
func NewGismeteoInspector() *GismeteoInspector {
	return &GismeteoInspector{
		client:   &http.Client{Timeout: gismeteoProbeTimeout},
		probeURL: gismeteoProbeURL,
	}
}

// newGismeteoInspectorForTest creates an inspector backed by the given HTTP client
// and probe URL. Use in tests to inject an httptest.Server without live network.
func newGismeteoInspectorForTest(client *http.Client, probeURL string) *GismeteoInspector {
	return &GismeteoInspector{client: client, probeURL: probeURL}
}

// Name returns the label used in the /health/check report.
func (g *GismeteoInspector) Name() string { return "gismeteo" }

// CheckUP probes the gismeteo.kz host and asserts a non-5xx response.
// Returns nil on success (any status below 500) or a descriptive error on failure.
func (g *GismeteoInspector) CheckUP(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.probeURL, nil)
	if err != nil {
		return fmt.Errorf("gismeteo health: create request: %w", err)
	}
	req.Header.Set("User-Agent", gismeteoInspectorUA)

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gismeteo health: request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// Accept any response below 500. gismeteo's CDN may return a redirect (301/302)
	// or a bot-fence 403 while the host itself is fully operational.
	if resp.StatusCode >= 500 {
		return fmt.Errorf("gismeteo health: unexpected status %d", resp.StatusCode)
	}

	return nil
}
