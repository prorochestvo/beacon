// Package proxyutil resolves and redacts the operator-configured outbound proxy
// URL. It is shared by the cmd/collector and cmd/doctor binaries, which both read
// BEACON_PROXY_URL at startup and then differ on what they do with it: doctor
// proxies every call, while the collector only makes the proxy available and routes
// a source through it when that source sets options.use_proxy.
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
// That failure names the variable and nothing else, deliberately. dsninjector.Parse
// embeds its entire input in the error it returns, so logging that error would put
// the raw value — proxy userinfo here, the bot token and AI keys at the other
// settings call sites — into the persisted log file. RedactURL below covers only
// the success path; dropping the error text is what covers the other one.
//
// Emits one startup line via log.Printf (the same sink as every other startup
// line, so it reaches stdout and the file logger regardless of verbosity level):
//   - "proxy: not configured" when the variable is absent.
//   - "proxy: BEACON_PROXY_URL=<redacted>" when a valid URL is found; userinfo
//     credentials are stripped from the logged value.
func ResolveURL(envName string) string {
	_, ok := os.LookupEnv(envName)
	if !ok {
		log.Printf("proxy: not configured")
		return ""
	}
	dsn, err := dsninjector.Unmarshal(envName)
	if err != nil {
		log.Fatalf("settings: %s: unparseable value (contents not logged)", envName)
	}
	raw := dsn.Driver() + "://" + dsn.Addr()
	log.Printf("proxy: BEACON_PROXY_URL=%s", RedactURL(raw))
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
