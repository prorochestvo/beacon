// Package proxyutil resolves and redacts the operator-configured outbound proxy
// URL. It is shared by the cmd/collector and cmd/doctor binaries, which both
// read PROXY_URL at startup.
package proxyutil

import (
	"log"
	"net/url"
	"os"

	"github.com/prorochestvo/dsninjector"
)

// ResolveURL reads envName, parses it via dsninjector.Unmarshal, and returns the
// URL string. Returns "" when unset or empty (no proxy). Calls log.Fatalf on a
// present-but-unparseable value — a malformed proxy URL is an operator config
// error that must be fixed before the service starts.
//
// Emits one startup line via log.Printf (the same sink as every other startup
// line, so it reaches stdout and the file logger regardless of verbosity level):
//   - "proxy: not configured" when the variable is absent.
//   - "proxy: PROXY_URL=<redacted>" when a valid URL is found; userinfo
//     credentials are stripped from the logged value.
func ResolveURL(envName string) string {
	_, ok := os.LookupEnv(envName)
	if !ok {
		log.Printf("proxy: not configured")
		return ""
	}
	dsn, err := dsninjector.Unmarshal(envName)
	if err != nil {
		log.Fatalf("settings: %s: %s", envName, err.Error())
	}
	raw := dsn.Driver() + "://" + dsn.Addr()
	log.Printf("proxy: PROXY_URL=%s", RedactURL(raw))
	return raw
}

// RedactURL strips the password from a proxy URL before logging. A URL with no
// userinfo or one that fails to parse is returned unchanged.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User(u.User.Username())
	return u.String()
}
