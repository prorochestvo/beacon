package api

// StatsResponse is the JSON shape of the global stats endpoint.
type StatsResponse struct {
	SourcesTotal  int64 `json:"sources_total"`
	SourcesActive int64 `json:"sources_active"`
	ErrorsTotal   int64 `json:"errors_total"`
}
