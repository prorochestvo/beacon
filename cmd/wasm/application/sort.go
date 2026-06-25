package application

import (
	"sort"
	"time"

	"github.com/seilbekskindirov/monitor/internal/dto"
)

// sortSourcesByLastRun sorts sources in-place by last_run_at. desc=true puts the
// most-recently-run first (matching the JS default). Empty last_run_at is treated
// as time zero and sorts last.
func sortSourcesByLastRun(sources []dto.SourceResponse, desc bool) {
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
