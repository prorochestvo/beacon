package dto

// HealthCheckResponse is the JSON body returned by GET /health/check.
// Status is true iff every dependency reported healthy. Services maps each
// component name to "ok" or the verbatim error message. HTTP status code mirrors
// Status: 200 when true, 503 when false.
type HealthCheckResponse struct {
	Status   bool              `json:"status"`
	Server   HealthServer      `json:"server"`
	Services map[string]string `json:"services"`
}

// HealthServer carries static metadata about the running service included in
// every /health/check response. Version is the build version string injected at
// link time. Uptime is the human-readable duration since the service started.
type HealthServer struct {
	Version string `json:"version"`
	Uptime  string `json:"uptime"`
}
