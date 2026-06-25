package rulegen

import (
	"bytes"
	"fmt"
	"regexp"
)

// Locate returns the smallest window of body centred on the earliest qualifying
// structural anchor match. A tier-1 hit at offset i qualifies only when a
// non-empty currency anchor occurs inside [i-coLocationBytes, i+coLocationBytes];
// coLocationBytes <= 0 disables the check (legacy: any tier-1 hit wins).
//
// With no qualifying tier-1 hit, Locate falls back to the earliest currency
// anchor match (tier-2). When neither tier hits it returns body unchanged and
// found=false.
//
// Anchors are deduplicated per tier; empty strings are skipped. The window is
// clamped to [0, len(body)].
func Locate(body []byte, structural, currency []string, windowBytes, coLocationBytes int) ([]byte, bool) {
	if hit, ok := smallestQualifyingTier1Hit(body, structural, currency, coLocationBytes); ok {
		return windowAround(body, hit, windowBytes), true
	}
	if hit, ok := firstHit(body, currency); ok {
		return windowAround(body, hit, windowBytes), true
	}
	return body, false
}

// Sanitize strips <script>, <style>, and <head> blocks (case-insensitive,
// non-greedy), then uses Locate to find the region around the best anchor, and
// caps the result at maxBodyBytesForLLM bytes.
//
// Tier-1 structural anchors are tried first; a hit qualifies only when a
// currency anchor appears within ±defaultCoLocationBytes (the co-location guard
// prevents marketing headings from capturing the window). Sanitize falls back
// to currency anchors (tier-2) only when no structural anchor qualifies; when
// neither matches, the leading maxBodyBytesForLLM bytes are returned.
//
// Returns the sanitized body and the pre-strip byte count (for log messages).
// If the raw body exceeds maxRawBodyBytes (5 MB), it returns an error and
// callers must abort before any LLM call.
func Sanitize(body []byte, structural, currency []string) ([]byte, int, error) {
	original := len(body)
	if original > maxRawBodyBytes {
		return nil, original, fmt.Errorf("rulegen: body size %d bytes exceeds limit of %d bytes (5 MB); narrow the URL", original, maxRawBodyBytes)
	}

	out := scriptRe.ReplaceAll(body, nil)
	out = styleRe.ReplaceAll(out, nil)
	out = headRe.ReplaceAll(out, nil)

	if located, found := Locate(out, structural, currency, locateWindowBytes, defaultCoLocationBytes); found {
		out = located
	} else if len(out) > maxBodyBytesForLLM {
		out = out[:maxBodyBytesForLLM]
	}

	if len(out) > maxBodyBytesForLLM {
		out = out[:maxBodyBytesForLLM]
	}

	return out, original, nil
}

const (
	maxRawBodyBytes    = 5 * 1024 * 1024 // 5 MB hard reject before stripping
	maxBodyBytesForLLM = 80 * 1024       // 80 KB sent to the LLM after locate/truncate
	locateWindowBytes  = 80 * 1024       // ±40 KB centred on the earliest anchor match

	// defaultCoLocationBytes is the radius around a tier-1 anchor hit within
	// which a currency anchor must appear for the hit to qualify. 5 KB gives
	// ~12× headroom over the few-hundred-byte slack of the production seed
	// regexes ([\s\S]{0,400}?), while still rejecting a marketing heading
	// 280 KB away from the rate table.
	defaultCoLocationBytes = 5 * 1024
)

var (
	scriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	styleRe  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	headRe   = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head>`)
)

// smallestQualifyingTier1Hit scans every occurrence of each distinct structural
// anchor and returns the smallest offset passing the co-location check;
// coLocationBytes <= 0 makes every hit pass (legacy). A safety cap of 1,000
// occurrences per anchor prevents O(n²) scanning on adversarial bodies.
func smallestQualifyingTier1Hit(body []byte, structural, currency []string, coLocationBytes int) (int, bool) {
	seen := make(map[string]bool, len(structural))
	best := -1
	for _, a := range structural {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		needle := []byte(a)
		cursor := 0
		for safety := 0; safety < 1000; safety++ {
			rel := bytes.Index(body[cursor:], needle)
			if rel < 0 {
				break
			}
			i := cursor + rel
			if coLocationBytes <= 0 || hasCurrencyWithin(body, i, currency, coLocationBytes) {
				if best < 0 || i < best {
					best = i
				}
			}
			cursor = i + 1
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// hasCurrencyWithin reports whether any non-empty currency anchor in the slice
// appears within body[max(0, centre-radius) : min(len(body), centre+radius)].
func hasCurrencyWithin(body []byte, centre int, currency []string, radius int) bool {
	start := centre - radius
	if start < 0 {
		start = 0
	}
	end := centre + radius
	if end > len(body) {
		end = len(body)
	}
	window := body[start:end]
	for _, c := range currency {
		if c == "" {
			continue
		}
		if bytes.Contains(window, []byte(c)) {
			return true
		}
	}
	return false
}

// firstHit returns the smallest byte offset of the first matching anchor in
// anchors. It deduplicates and skips empty strings. Returns (0, false) when
// no anchor matches.
func firstHit(body []byte, anchors []string) (int, bool) {
	seen := make(map[string]bool, len(anchors))
	best := -1
	for _, a := range anchors {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		i := bytes.Index(body, []byte(a))
		if i < 0 {
			continue
		}
		if best < 0 || i < best {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// windowAround returns the slice of body centred on centre with total width
// windowBytes, clamped to valid bounds.
func windowAround(body []byte, centre, windowBytes int) []byte {
	half := windowBytes / 2
	start := centre - half
	if start < 0 {
		start = 0
	}
	end := centre + half
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}
