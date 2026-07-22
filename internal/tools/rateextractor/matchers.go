package rateextractor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/prorochestvo/loginjector"
)

// ApplyRegex compiles pattern and extracts the first capture group from payload.
// The pattern must contain at least one capture group.
func ApplyRegex(pattern string, payload []byte) ([]byte, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		err = fmt.Errorf("compile pattern %q: %w", pattern, err)
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	matches := re.FindSubmatch(payload)
	if len(matches) < 2 {
		err = fmt.Errorf("pattern %q produced no match in body (len=%d)", pattern, len(payload))
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	return matches[1], nil
}

// ApplyJSONPath traverses payload using a dot-separated path expression and
// returns the terminal value as bytes.
func ApplyJSONPath(pattern string, payload []byte) ([]byte, error) {
	segments, err := parseJSONPath(pattern)
	if err != nil {
		return nil, fmt.Errorf("json_path: parse path %q: %w", pattern, err)
	}

	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()

	var root interface{}
	if err = dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("json_path: unmarshal JSON: %w", err)
	}

	current := root
	for _, seg := range segments {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("json_path: expected object at %q, got %T", seg.Key, current)
		}

		val, exists := m[seg.Key]
		if !exists {
			return nil, fmt.Errorf("json_path: key %q not found", seg.Key)
		}

		if seg.HasIndex {
			arr, ok := val.([]interface{})
			if !ok {
				return nil, fmt.Errorf("json_path: key %q is not an array", seg.Key)
			}
			if seg.Index >= len(arr) {
				return nil, fmt.Errorf("json_path: index %d out of range for key %q (len %d)", seg.Index, seg.Key, len(arr))
			}
			current = arr[seg.Index]
		} else {
			current = val
		}
	}

	switch v := current.(type) {
	case json.Number:
		return []byte(v.String()), nil
	case float64:
		return []byte(strconv.FormatFloat(v, 'f', -1, 64)), nil
	case string:
		return []byte(v), nil
	default:
		return nil, fmt.Errorf("json_path: terminal value at %q has unsupported type %T", pattern, current)
	}
}
