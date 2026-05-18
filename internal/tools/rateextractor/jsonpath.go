package rateextractor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	reArraySegment = regexp.MustCompile(`^(\w+)\[(\d+)\]$`)
	rePlainSegment = regexp.MustCompile(`^\w+$`)
)

type pathSegment struct {
	Key      string
	HasIndex bool
	Index    int
}

func parseJSONPath(pattern string) ([]pathSegment, error) {
	if pattern == "" {
		return nil, fmt.Errorf("json_path: path pattern must not be empty")
	}

	rawSegments := strings.Split(pattern, ".")
	segments := make([]pathSegment, 0, len(rawSegments))

	for _, raw := range rawSegments {
		if m := reArraySegment.FindStringSubmatch(raw); m != nil {
			idx, err := strconv.Atoi(m[2])
			if err != nil {
				// Should not happen given \d+ regex, but guard anyway.
				return nil, fmt.Errorf("json_path: invalid array index in segment %q: %w", raw, err)
			}
			segments = append(segments, pathSegment{Key: m[1], HasIndex: true, Index: idx})
			continue
		}

		if rePlainSegment.MatchString(raw) {
			segments = append(segments, pathSegment{Key: raw})
			continue
		}

		return nil, fmt.Errorf("json_path: invalid path segment %q", raw)
	}

	return segments, nil
}
