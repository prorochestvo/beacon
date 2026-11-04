package rateextractor

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Keys allow a hyphen because JSON objects are keyed by whatever the upstream chose,
	// and Yahoo keys its batched quote response by ticker — "BTC-USD" is a key, not a
	// subtraction. The charset stays otherwise narrow so a typo in a rule is still
	// rejected by name rather than silently treated as a missing key.
	//
	// The index accepts a leading minus: negative indices count back from the end over
	// non-null elements (a null is not a point), which is the only way to address "the
	// newest point" in a series whose length changes between requests.
	reArraySegment = regexp.MustCompile(`^([\w-]+)\[(-?\d+)\]$`)
	rePlainSegment = regexp.MustCompile(`^[\w-]+$`)
)

type pathSegment struct {
	Key      string
	HasIndex bool
	// Index may be negative, counting back from the end over non-null array elements:
	// -1 is the last non-null value. Negative indices are not positionally aligned across
	// sibling arrays. Resolution happens in ApplyJSONPath after parsing the payload.
	Index int
}

func parseJSONPath(pattern string) ([]pathSegment, error) {
	if pattern == "" {
		return nil, errors.New("json_path: path pattern must not be empty")
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
