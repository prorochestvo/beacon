package repository

// StatsResult holds the global application statistics returned by the stats endpoint.
type StatsResult struct {
	SourcesTotal  int64
	SourcesActive int64
	ErrorsTotal   int64
}
