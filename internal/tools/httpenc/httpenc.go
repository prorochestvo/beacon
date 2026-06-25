// Package httpenc provides helpers for HTTP content-encoding negotiation.
package httpenc

import "strings"

// AcceptsGzip reports whether the Accept-Encoding header lists gzip with a
// non-zero q-value. Substring matching is wrong: "gzip;q=0" declines the
// encoding under RFC 7231, so this parses per-element and respects the q-value.
func AcceptsGzip(headerVal string) bool {
	for _, part := range strings.Split(headerVal, ",") {
		part = strings.TrimSpace(part)
		name, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
				continue
			}
			q := strings.TrimSpace(v)
			if q == "0" || (strings.HasPrefix(q, "0.") && strings.TrimLeft(q[2:], "0") == "") {
				return false
			}
		}
		return true
	}
	return false
}
