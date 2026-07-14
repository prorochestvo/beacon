package inspector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// openMeteoProbeURL is the geocoding endpoint used for the health probe.
	// It is a keyless, idempotent, cheap read that does not mutate any state.
	openMeteoProbeURL = "https://geocoding-api.open-meteo.com/v1/search?name=Berlin&count=1&language=en"

	// openMeteoInspectorUA identifies the health probe in Open-Meteo access logs.
	openMeteoInspectorUA = "Beacon/1.0 health-check (+https://github.com/seilbekskindirov/beacon)"

	// openMeteoProbeTimeout is the per-request timeout for the health probe. It is
	// shorter than the agent sweep budget (3 s) to leave headroom for other inspectors.
	openMeteoProbeTimeout = 2 * time.Second
)

// OpenMeteoInspector is an advisory health inspector for the Open-Meteo API.
// It probes the geocoding endpoint with a known city name and asserts a 2xx
// response with parseable JSON.
//
// This inspector is advisory: its failure is reported in the /health/check component
// map but does not flip the aggregate healthy flag. An Open-Meteo outage must not
// fail the deploy health-gate, which is outside the operator's control.
//
// The probe issues a direct HTTP request (no proxy). cmd/web does not parse
// BEACON_PROXY_URL, so the probe tests host→Open-Meteo reachability on the direct
// path. If the production environment requires a proxy for Open-Meteo egress,
// derive weather health from collection execution history instead of this probe.
type OpenMeteoInspector struct {
	client   *http.Client
	probeURL string
}

// NewOpenMeteoInspector returns an advisory Inspector for the Open-Meteo API.
func NewOpenMeteoInspector() *OpenMeteoInspector {
	return &OpenMeteoInspector{
		client:   &http.Client{Timeout: openMeteoProbeTimeout},
		probeURL: openMeteoProbeURL,
	}
}

// newOpenMeteoInspectorForTest creates an inspector backed by the given HTTP client
// and probe URL. Use in tests to inject an httptest.Server without live network.
func newOpenMeteoInspectorForTest(client *http.Client, probeURL string) *OpenMeteoInspector {
	return &OpenMeteoInspector{client: client, probeURL: probeURL}
}

// Name returns the label used in the /health/check report.
func (o *OpenMeteoInspector) Name() string { return "open-meteo" }

// CheckUP probes the Open-Meteo geocoding endpoint and asserts a 2xx response
// with valid JSON. Returns nil on success or a descriptive error on failure.
func (o *OpenMeteoInspector) CheckUP(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.probeURL, nil)
	if err != nil {
		return fmt.Errorf("open-meteo health: create request: %w", err)
	}
	req.Header.Set("User-Agent", openMeteoInspectorUA)

	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("open-meteo health: request: %w", err)
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("open-meteo health: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("open-meteo health: read body: %w", err)
	}

	// Assert the response is parseable JSON — a malformed body signals API
	// degradation even when the status code is 2xx.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("open-meteo health: parse JSON: %w", err)
	}

	return nil
}
