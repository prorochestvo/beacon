package dom

import "strings"

// Escape replaces the four HTML-special characters with their entity equivalents.
// The replacement order (&, <, >, ") is significant: & must come first to avoid
// double-escaping the ampersands that the subsequent replacements introduce.
// The set and order match the JS esc() helper at cmd/web/static/index.html:42-48.
func Escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
