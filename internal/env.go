package internal

// Environment variable names, declared once for the whole project.
//
// These were previously re-declared per binary — thirteen declarations of six names
// across five composition roots — which is not one greppable source of truth but the
// typo-drift surface that looks like one. A rename now touches one line and every
// consumer follows; getting it wrong is a compile error rather than a service that
// starts and silently ignores its configuration.
//
// Keep this file stdlib-only. Everything imports internal, so a dependency here
// invites a cycle.
const (
	// EnvSQLiteDSN is the SQLite connection string, parsed via dsninjector.Unmarshal.
	// Format: sqlite://<path-to-db-file>. Required by every binary that opens the
	// database.
	EnvSQLiteDSN = "BEACON_SQLITEDB_DSN"

	// EnvTelegramBotDSN is the Telegram bot credential, parsed via
	// dsninjector.Unmarshal. Format: <adminChatID>:<botToken>@<host>, where Addr()
	// returns the token and Login() the admin chat id. Required by cmd/web and
	// cmd/notifier.
	//
	// This value IS the bot token; never log it, and never log a parser error that
	// carries it — see the settings guard in `make lint`.
	EnvTelegramBotDSN = "BEACON_TELEGRAMBOT_DSN"

	// EnvProxyURL is the optional outbound proxy. Format: <scheme>://<host>:<port>.
	// cmd/doctor proxies through it unconditionally; cmd/collector only makes it
	// available, and a rate source reaches it solely by setting options.use_proxy —
	// so setting this alone leaves collection direct.
	EnvProxyURL = "BEACON_PROXY_URL"

	// EnvChromiumPath is the optional absolute path to the Chromium/Chrome binary for
	// fetcher_kind='chromedp' sources. When unset, chromedp searches PATH (chromium,
	// chromium-browser, google-chrome, chrome).
	EnvChromiumPath = "BEACON_CHROMIUM_PATH"

	// EnvAIPrimaryDSN and EnvAIFallbackDSN are the AI provider credentials read only
	// by cmd/doctor rulegen; the primary is required there, the fallback optional.
	// See cmd/doctor/README.md for the DSN format.
	//
	// Both carry API keys. The logging rule for EnvTelegramBotDSN applies here too.
	EnvAIPrimaryDSN  = "BEACON_AI_PRIMARY_DSN"
	EnvAIFallbackDSN = "BEACON_AI_FALLBACK_DSN"
)
