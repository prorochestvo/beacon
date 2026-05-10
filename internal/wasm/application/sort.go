package application

import (
	"sort"
	"time"

	"github.com/seilbekskindirov/monitor/pkg/api"
)

// sortSourcesByLastRun sorts sources in-place by last_run_at. When desc is
// true the most-recently-run source comes first (matching the JS default).
// Sources with an empty last_run_at are treated as time zero and sort last.
func sortSourcesByLastRun(sources []api.SourceResponse, desc bool) {
	sort.SliceStable(sources, func(i, j int) bool {
		ti := parseTime(sources[i].LastRunAt)
		tj := parseTime(sources[j].LastRunAt)
		if desc {
			return ti.After(tj)
		}
		return ti.Before(tj)
	})
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}
