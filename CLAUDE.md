# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Language

The same rule that governs plan files governs every persisted text artifact in this
repo (plans, commit messages, PR descriptions, code comments, docs):

- **Prose is English.** Write all persisted prose in English regardless of the prompt
  language. This includes the user's own conclusions, decisions, and reasoning — when
  capturing what the user concluded or decided (even a direct quote of something they
  said in Russian), record it in English, not verbatim in the original language.
- **Literal data tokens stay verbatim — never translate them.** Strings that exist to
  be matched, parsed, or linked against something external (e.g. currency/column
  labels like `Покупка`/`Сату` scraped from bank pages, identifiers, config keys,
  fixture values, regex literals) must be preserved byte-for-byte even when non-English.
  Translating them silently breaks logic and linkage. The English-only rule is about
  prose, not data. When unsure whether something is prose or data, ask before changing it.

## Build & Run Commands

All commands assume a pure-Go build with `CGO_ENABLED=0` (recommended default). Adjust
if your project legitimately needs CGO.

```bash
make build    # Builds binaries to ./build/
make run      # Runs the application (migrations + service start, if applicable)
make test     # go fmt + go vet + go test -race ./...
make lint     # go vet + checks for forbidden imports
make format   # go fmt ./...
make clean    # Removes binaries + go mod tidy
```

Targeted test runs (`-race` needs cgo — use `CGO_ENABLED=1`; macOS tolerates 0,
Linux does not):
```bash
# Single top-level test
CGO_ENABLED=1 go test -race -run TestFunctionName ./<package>/

# Single subtest
CGO_ENABLED=1 go test -race -run 'TestFunctionName/subtest_name' ./<package>/

# Verbose output (see every subtest pass/fail)
CGO_ENABLED=1 go test -race -v ./<package>/

# Benchmarks (no -race, so cgo not required)
CGO_ENABLED=0 go test -bench=. -benchmem -run=^$ ./<package>/

# Coverage
CGO_ENABLED=1 go test -race -coverprofile=cover.out ./... && go tool cover -html=cover.out
```

## Architecture Overview

A self-hosted FX-rate monitor. The `collector` binary scrapes each configured rate
source on every invocation (plain HTTP, or a chromedp-driven headless browser for
JS-rendered pages), extracts the numeric rate via per-source rules, and stores it in
SQLite. The `notifier` binary runs a check-agent that evaluates user subscription
conditions (delta / interval / daily / cron) against the latest rates and enqueues
notifications, and a dispatch-agent that drains the pool and sends them over Telegram.
The `web` binary serves a REST API plus an embedded dashboard (HTML and a WASM build)
and routes Telegram callbacks. `migrator` applies schema migrations; `doctor` provides
operator tooling (LLM rule generation and source auditing).

### Weather providers

Two weather providers feed the daily morning summary:

- **Open-Meteo** (`domain.ProviderOpenMeteo`): global, keyless JSON API; the primary
  source of truth. All cities can use it. Egress in `cmd/collector` honours
  `BEACON_PROXY_URL`; the `OpenMeteoInspector` in `cmd/web` probes direct.
- **Gismeteo** (`domain.ProviderGismeteo`): plain-HTTP scrape of `www.gismeteo.kz`
  (`.kz` — Russian-language, metric units). Used for cross-provider comparison in the
  morning summary. Coverage is limited to a hand-curated map
  (`internal/infrastructure/weather/gismeteo.go`: `gismeteoCities`) keyed by the
  Open-Meteo geocoding id — cities outside the map skip gismeteo silently. Egress in
  `cmd/collector` honours `BEACON_PROXY_URL`. The `GismeteoInspector` in `cmd/web`
  probes the host directly (cmd/web does not parse `BEACON_PROXY_URL`), so it tests
  a different egress path than the collector; a false "down" cannot fail the deploy
  gate because the inspector is registered advisory.

### Layer Responsibilities

| Layer | Location | Role |
|-------|----------|------|
| Entry point | `cmd/<binary>/` | Composition root per binary (collector, notifier, web, migrator, doctor, wasm) |
| Application | `internal/application/` | Business logic: collection, notification, rulegen, sourceaudit, REST/Telegram services |
| Domain | `internal/domain/` | Value objects / models, no logic |
| DTO | `internal/dto/` | JSON wire contract shared by the server (gateway) and the WASM client |
| Gateway | `internal/gateway/` | Routers, controllers, middleware |
| Repository | `internal/repository/` | Persistence queries |
| Infrastructure | `internal/infrastructure/` | External clients (SQLite, Telegram, AI providers) |
| Tools | `internal/tools/` | Cross-cutting utilities |
| Frontend | `cmd/wasm/` | GOOS=js GOARCH=wasm dashboard (apiclient, application, ui, dom) |

### Key Patterns

- **Repository pattern** — each repository type owns its own SQL, migration, and query helper functions. Queries execute inside explicit transactions (`r.db.Transaction(ctx)`). Repositories are passed as interfaces into service and handler layers.
- **Configuration injection** — `BEACON_SQLITEDB_DSN` and `BEACON_TELEGRAMBOT_DSN` are read via `dsninjector.Unmarshal(envName)` at startup in `cmd/web/main.go` and live in the systemd `EnvironmentFile`. The public HTTPS origin is passed via the `--api-dsn` CLI flag (format: `https://<host>/`, parsed by `dsninjector.Parse`) and is hardcoded in the systemd unit's `ExecStart` line — never in `.env`. All three configs must be present at startup; the binary calls `log.Fatalf` on any missing value.
- **Embedded assets** — `cmd/web/main.go` embeds the `static/` directory via `//go:embed static`. All static files served by `http.FileServer` live under `cmd/web/static/`.
- **Auth: Telegram WebApp initData HMAC** — the `/api/me/...` endpoint family authenticates callers by verifying the Telegram WebApp `initData` HMAC-SHA256 signature. The signing algorithm uses `secret_key = HMAC_SHA256("WebAppData", botToken)` (the string literal is the key; the token is the message). Implementation lives in `internal/tools/tgwebapp/initdata.go`. The handler injects the validator as a function field so tests can substitute a fake without real bot tokens. No other endpoint requires this auth.

### HTTP Routes

- `GET /api/sources` — list all configured rate sources with latest execution status
- `GET /api/sources/{name}/rates` — most recent rate values for a named source
- `GET /api/sources/{name}/history` — execution history for a named source
- `GET /api/sources/{name}/events/failed` — paginated failed events for a source
- `GET /api/sources/{name}/subscriptions` — grouped subscription statistics for a source
- `GET /api/sources/{name}/subscriptions/list` — paginated subscription details for a source
- `GET /api/sources/{name}/events/daily` — daily aggregated event counts for a source
- `PATCH /api/sources/{name}/active` — enable or disable a named source
- `GET /api/stats` — global statistics (source counts, error count)
- `GET /api/errors/execution` — paginated failed execution history records
- `GET /api/events/pending` — all currently pending notification events
- `GET /api/notifications` — last N notification pool records
- `GET /api/notifications/failed` — all failed notification pool records
- `GET /api/me/subscriptions` — caller's own subscriptions enriched with latest rate values; authenticated via Telegram WebApp initData HMAC (`X-Telegram-Init-Data` header only; the signed payload must not be passed via query string because it would leak into access logs and Referer headers)
- `GET /api/me/subscriptions/raw` — caller's own subscriptions as one row per condition (no rate enrichment); used by the WASM subscription editor to populate and refresh its list; authenticated via Telegram WebApp initData HMAC (`X-Telegram-Init-Data` header only)
- `POST /api/me/subscriptions` — create a new subscription for the caller; body: `{"source_name": "...", "condition_type": "delta|interval|daily|cron", "condition_value": "..."}`. Returns `{"id": "..."}` on 201; authenticated via `X-Telegram-Init-Data` header only
- `PATCH /api/me/subscriptions/{id}` — update the condition of an existing subscription; body: `{"condition_type": "...", "condition_value": "..."}`. Returns 204 on success; returns 404 when the subscription is not found **or belongs to a different user** (ownership mismatch returns 404, not 403, to avoid existence disclosure); authenticated via `X-Telegram-Init-Data` header only
- `DELETE /api/me/subscriptions/{id}` — delete a subscription; returns 204 on success; returns 404 when not found or belonging to a different user (same 404-on-cross-user rule as PATCH); `rate_user_events` rows are **not** cascade-deleted when a subscription is removed; authenticated via `X-Telegram-Init-Data` header only
- `GET /api/me/rates/chart` — sparkline-list chart data for the caller's subscribed pairs; authenticated via Telegram WebApp initData HMAC (`X-Telegram-Init-Data` header only). Query params: `period` (integer days, must be one of `{7, 30, 90, 180, 360}`, default 7; any other value returns 400 with a PublicError body)
- `GET /api/me/rates/history` — paginated rate-collection events for the calling user's subscribed sources matching a canonical pair label; authenticated via Telegram WebApp initData HMAC (`X-Telegram-Init-Data` header only). Query params: `pair` (required, e.g. `USD/KZT`), `source_title` (optional, filters to one provider by its human-readable title, which is unique per provider; unknown value returns 200 with empty items), `page` (default 1), `limit` (default 20, max 100)
- `POST /api/me/profile` — upsert the caller's profile preferences (IANA `timezone`, optional BCP-47 `locale`) so notification timestamps render in local time; body: `{"timezone":"Asia/Almaty"}`. Fire-and-forget from the Mini App on mount. Returns 204 on success, 400 on a timezone that fails `time.LoadLocation`; authenticated via `X-Telegram-Init-Data` header only
- `GET /api/me/weather/current` — latest stored Open-Meteo observation for each of the caller's subscribed cities, deduplicated by `location_id`. Returns `{"items":[{"location_id":"...","display_name":"...","timezone":"...","has_data":bool,...}]}`. Cities whose first collection has not yet completed return an item with `has_data:false` (not an error). Sunrise/sunset are pre-formatted as `"15:04"` in the city's IANA timezone by the server. Authenticated via `X-Telegram-Init-Data` header only.
- `GET /api/me/weather/cities/search?q=` — geocoding search via Open-Meteo; returns up to 5 `WeatherCitySearchItem` matches for the given query string. Returns 200 with empty items on blank or whitespace-only query. Open-Meteo egress is direct (no proxy). Authenticated via `X-Telegram-Init-Data` header only.
- `GET /api/me/weather/cities` — list the authenticated caller's saved city weather subscriptions, ordered by `created_at`. Returns `{"items": [...]}`. Authenticated via `X-Telegram-Init-Data` header only.
- `POST /api/me/weather/cities` — create a new city weather subscription; body: `{"location_id":"...", "display_name":"...", "latitude":..., "longitude":..., "timezone":"...", "country":"...", "admin1":"..."}`. Server re-validates `timezone` via `time.LoadLocation`, `latitude` ∈ [-90,90], `longitude` ∈ [-180,180], and `notify_hour` ∈ [0,23] (defaults to 7 when absent). Returns `{"id":"..."}` on 201. Authenticated via `X-Telegram-Init-Data` header only.
- `DELETE /api/me/weather/cities/{id}` — delete a city subscription; returns 204 on success; returns 404 when not found or belonging to a different user (cross-user access returns 404, not 403, to avoid existence disclosure). Authenticated via `X-Telegram-Init-Data` header only.
- `GET /api/public/rates/chart` — paginated system-wide sparkline-list chart; no authentication. Query params: `page` (default 1), `limit` (default 20, max 100), `period` (integer days, must be one of `{7, 30, 90, 180, 360}`, default 7; any other value returns 400 with a PublicError body)
- `GET /` — unified Mini App / guest landing page (served by embedded static file server). Dispatcher inline script checks `window.Telegram.WebApp.initData`: non-empty → `_wasm.renderMeSubscriptions()`, empty → `_wasm.renderPublicSubscriptions()`
- `GET /admin/` — operator dashboard (served by embedded static file server, `cmd/web/static/admin/index.html`; no dedicated route needed)
- `GET /ping` — liveness probe; always returns 200 `{"status":"ok"}`, touches no dependency. No authentication. `GET /healthz` is a backward-compatible alias for this endpoint.
- `GET /health/check` — readiness probe; runs all registered dependency inspectors under a bounded 3s timeout and returns per-component JSON: `{"status": bool, "server": {"version": "...", "uptime": "..."}, "services": {"sqlite": "ok|<error>", "telegram": "ok|<error>", "open-meteo": "ok|<error>", "gismeteo": "ok|<error>"}}`. Critical inspectors (sqlite, telegram) set `status=false` and return HTTP 503 on failure. Advisory inspectors (open-meteo, gismeteo) appear in the report but never flip the aggregate to 503 — a third-party weather outage must not fail the deploy health-gate. Returns 200 when all critical inspectors are healthy. No authentication; deployed to health-gate in CI and referenced by uptime monitors.

### Static asset caching

Two heavy static assets are served under content-hashed URLs so nginx can cache them
at the edge with `Cache-Control: public, max-age=604800, immutable` (7 days):

| Asset | Hashed URL shape | Gzip strategy |
|-------|-----------------|---------------|
| `app.wasm` (~4 MB raw) | `/app.<8hex>.wasm` | Go origin serves pre-built `app.wasm.gz` sibling with `Content-Encoding: gzip` when client sends `Accept-Encoding: gzip` |
| `wasm_exec.js` (~17 KB) | `/wasm_exec.<8hex>.js` | nginx dynamically gzips on the wire (`application/javascript` is in `gzip_types`) |

The 8-hex component is the first 4 bytes of SHA-256 over the raw (uncompressed) asset
bytes, computed at `cmd/web` startup. Hashing raw bytes means a change in gzip
compression level alone does not invalidate the cache-busting URL.

At boot, `cmd/web` builds a `hashedAssetRegistry` from the active FS (embedded or
`--static-dir` override), then rewrites `index.html` and `admin/index.html` in memory,
replacing `/app.wasm` and `/wasm_exec.js` with their hashed forms. The rewritten HTML
is cached in a `htmlCache` struct and served on `GET`/`HEAD` for `/`, `/index.html`,
`/admin/`, and `/admin/index.html`. The boot log emits a line of the form:

```
hashed assets: app=<8hex> wasm_exec=<8hex>
```

so operators can sanity-check active hashes after each deploy.

The nginx regex location in `configs/nginx.beacon_common_settings.conf` matches
`^/(app|wasm_exec)\.[a-f0-9]{8}\.(wasm|js)$` and applies the 7-day immutable cache
header. It **must** appear above the catch-all `location /` block in the file — nginx
evaluates regex locations in source order.

Stale-HTML recovery: unhashed paths `/app.wasm` and `/wasm_exec.js` are not intercepted
by the handler and fall through to the `http.FileServer`, which serves the current bytes.
A browser tab holding HTML from a previous deploy can still load the wasm successfully.

> Rule (re-)generation and seed auditing are operator-only tools, invoked
> via the umbrella binary `cmd/doctor`:
>   doctor rulegen <source>              # single-source mode
>   doctor rulegen --all                 # batch mode (intended for cron)
>   doctor audit --all                   # probe all seeded sources
>   doctor audit --source <name>         # probe one source
> See `cmd/doctor/README.md` and `cmd/doctor/main.go` godoc for usage,
> exit codes, and environment variables.

### Database

Engine: SQLite, accessed via the pure-Go `modernc.org/sqlite` driver (no CGO).

Three PRAGMAs are applied on connection open:
- `foreign_keys=ON` and `busy_timeout=5000` are passed as `?_pragma=`
  query parameters on the DSN (see `connectionOptions` in
  `sqlclient.go`). The `modernc.org/sqlite` driver re-applies them in
  its `Open` hook on every new connection the `database/sql` pool
  opens, which is the only way to keep these per-connection settings
  consistent across `SetMaxOpenConns(N>1)`.
- `journal_mode=WAL` is persisted in the database file header and is
  set once via `db.Exec` inside `NewSQLiteClientEx`.

`busy_timeout` (5 s) is the driver-level retry window for concurrent
writers; it must stay strictly less than the Go-level `Timeout` so the
context deadline always fires after the driver retry expires.

Foreign keys point from `rate_values`, `rate_user_subscriptions`, and
`rate_user_events` to `rate_sources(name)` with `ON DELETE CASCADE` —
deleting a source destroys all dependent rows. See the warning on
`RemoveRateSource` before wiring it to any endpoint.

Schema lives at the project root: `./migrations/*.sql`. The sibling Go file
`./migrations/embed.go` (`package migrations`) exposes those files as
`var MigrationsFS embed.FS` so they can be consumed without disk I/O at runtime.

`cmd/migrator` is the **only** thing that mutates schema. It embeds
`migrations.MigrationsFS` at build time, opens the DB via `BEACON_SQLITEDB_DSN`, and
calls `sqlitedb.Migrator.Run(ctx)`. Idempotent: applied filenames are tracked in
`__schema_migrations`.

Service binaries (`cmd/web`, `cmd/collector`, `cmd/notifier`) DO NOT migrate on
startup. They call `sqlitedb.RequireMigratedSchema(ctx, db)` immediately after
opening the DB; a missing or empty `__schema_migrations` table causes
`log.Fatalf("schema not initialised: run cmd/migrator before starting the service")`.

Migration files live at `./migrations/*.sql`. Filename convention:
`<YYYYMM>.<NNN>.<table>.<description>.sql` (e.g.
`202605.001.rate_sources.table_initiate.sql`). The `<NNN>` segment is a
**global** zero-padded counter across all tables — files are applied in
lexicographic order, which the naming makes the execution order. Once
applied to any production database the filename is **immutable**: renaming
triggers a duplicate apply.

Historical exception (2026-05-31): migrations 001–010 were squashed to 001–007
by folding the three FK-cascade rebuilds (old 007/008/009) into their respective
`_initiate.sql` files and renumbering old 010 to 006. This one-time override was
authorised by the sole operator because no production environment existed yet.
Any DB that previously applied the old 10-file set must be wiped before redeploy:
stop all services (`cmd/web`, `cmd/collector`, `cmd/notifier`), delete the DB file
and its `-shm`/`-wal` sidecars, then run the standard deploy so `cmd/migrator`
repopulates from the new 7-file baseline. This carve-out is a closed, dated
exception — the immutability rule applies in full to all future migrations.

Repository files in `internal/repository/` reference table and column names
exclusively through `const` declarations (e.g. `rateSourceTableName`,
`rateSourceNameFieldName`) so a schema rename surfaces at compile time and via
`grep`, never via a runtime "no such column" error.

`weather_user_cities.gismeteo_city_id` (added in migration 011) is currently always
NULL — it is reserved for a future "comparison available" UI hint. Gismeteo eligibility
is determined at runtime from the curated `gismeteoCities` map in
`internal/infrastructure/weather/gismeteo.go`, not from this column.

Deploy flow:
```
make build         # builds all binaries including ./build/migrator
make migrate       # applies any pending .sql files (no-op if up to date)
make run           # starts collector, notifier, web
```

Deployment ordering: the `.github/workflows/release.yml` deploy job (triggered
by an `r_*` release tag) runs the migrator via the `beacon-migrate` one-shot
systemd unit after flipping the `bin/release` channel symlink and before
restarting the service. The service systemd unit in `configs/` does **not**
invoke the migrator via `ExecStartPre` — schema reconciliation is a deploy-time
step, not a startup-time step.

### Environment Variables

- `BEACON_SQLITEDB_DSN` — SQLite connection string, parsed via `dsninjector.Unmarshal`. Format: `sqlite://<path-to-db-file>`
- `BEACON_TELEGRAMBOT_DSN` — Telegram bot credentials parsed via `dsninjector.Unmarshal`. Format: `<adminChatID>:<botToken>@<host>` where `Addr()` returns the token and `Login()` returns the admin chat ID.
- `BEACON_PROXY_URL` — optional outbound proxy URL, parsed via `dsninjector.Unmarshal`. Format: `<scheme>://<host>:<port>` (e.g. `http://127.0.0.1:7788`). When unset or empty all outbound traffic is direct. Used by `cmd/collector` (plain and chromedp rate sources) and `cmd/doctor` (AI provider calls and chromedp fetcher). Telegram Bot API traffic bypasses the proxy unconditionally — the bypass is enforced in code via a hardcoded `Proxy: nil` transport in `internal/infrastructure/telegrambot/tbotclient.go`. Do not configure `HTTPS_PROXY`, `HTTP_PROXY`, or `NO_PROXY` for proxy routing — they are not consulted by any component in this project.
- `BEACON_CHROMIUM_PATH` — optional absolute path to the Chromium/Chrome binary for `fetcher_kind='chromedp'` sources. Read by `cmd/collector` and `cmd/doctor`. When unset, chromedp searches PATH (`chromium`, `chromium-browser`, `google-chrome`, `chrome`).
- `BEACON_AI_PRIMARY_DSN` (required) and `BEACON_AI_FALLBACK_DSN` (optional) — AI provider DSNs read only by `cmd/doctor rulegen`. See `cmd/doctor/README.md` for the DSN format and provider details.

> The public HTTPS origin of the `cmd/web` server is **not** an env var — see the `--api-dsn` CLI flag on the `cmd/web` binary, baked into the systemd unit's `ExecStart` line.

> Never read or edit `.env` files.

### Key Dependencies

- `modernc.org/sqlite` — pure-Go SQLite driver (no CGO)
- `github.com/OvyFlash/telegram-bot-api` — Telegram Bot API client
- `github.com/chromedp/chromedp` — headless-Chrome driver for JS-rendered rate sources
- `github.com/openai/openai-go/v3` — OpenAI client used by `doctor rulegen`
- `github.com/robfig/cron/v3` — cron-expression parsing for subscription conditions
- `github.com/prorochestvo/dsninjector` — DSN parsing for config injection
- `github.com/prorochestvo/loginjector` — daily-rotated file logger
- `github.com/patrickmn/go-cache` — in-memory cache
- `github.com/shirou/gopsutil/v4` — host/process stats for diagnostics
- `github.com/twinj/uuid` — UUID generation
- `github.com/stretchr/testify` — test assertions
- Go version: `1.26`

### Frontend

Static assets live under `cmd/web/static/`, embedded into the `web` binary via
`//go:embed static`. The WASM bundle is built from `cmd/wasm` (`GOOS=js GOARCH=wasm`)
to `cmd/web/static/app.wasm` and shares the `internal/dto` wire types with the server.
`make build` produces the bundle as part of the standard build.

Directory layout under `cmd/web/static/`:
- `index.html` — unified Mini App / guest landing page (served at `/`)
- `admin/index.html` — operator dashboard (served at `/admin/`)
- `app.wasm` / `app.wasm.gz` — WASM bundle

The `webAppURL` Telegram BotFather setting points to `https://<host>/` (trailing slash,
no path suffix). Update it in BotFather whenever the host changes.

### Deployment

The host uses the standard release layout: immutable `/opt/beacon/artifacts/<VERSION_ID>/`
build sets and a `bin/release` channel symlink the units run through
(`ExecStart=/opt/beacon/bin/release/webapp …`). The CI deploy user may write only
inside `artifacts/` and `bin/`; `.env`, the DB, and the base dir are root-owned and
out of its reach. The `.github/workflows/release.yml` deploy job (triggered by an `r_*`
release tag) uploads a new `artifacts/<VERSION_ID>/`, flips the symlink, runs migrations via
the `beacon-migrate` one-shot unit (root, so the deploy user never writes the DB),
restarts `beacon`, and health-gates on `/health/check` (readiness probe) with a one-symlink rollback. Schema
reconciliation is a deploy-time step, not a startup-time step — the service unit does
not invoke the migrator via `ExecStartPre`. `make init` provisions the layout, the two
units (`beacon.service`, `beacon-migrate.service`), the narrow
`/etc/sudoers.d/beacon-deploy`, and the Cloudflare nginx vhost. See `deploy/README.md`.

## Error Handling

The project separates user-facing errors from internal failures.

### `PublicError` — user-facing errors

`internal.PublicError` is the mechanism for surfacing safe, human-readable messages to
end users. Any error message that is **safe to show** to a user is wrapped with
`internal.NewPublicError(...)` at the point where the error is created (typically in the
service layer). `internal/errors.go` also defines `TraceError`, `StackTraceError`,
`HttpCodeError`, and the `ErrNotFound` sentinel.

**Rule**: if a function can fail in a way that meaningfully communicates something to
the user, return a public error. For all other failures (DB down, unexpected nil, etc.)
return a plain error — the controller will send a generic fallback.

#### Creating a public error (service layer)

```go
import "github.com/seilbekskindirov/beacon/internal"

// User should know about this
return internal.NewPublicError("Invalid input. Source name must not be empty.")

// Internal failure — user gets generic message
return fmt.Errorf("db query failed: %w", err)
```

#### Error handling in the controller

The controller catches all errors from sub-handlers and sends the appropriate message.

```go
const errFallbackMessage = "Something went wrong. Try again later."
```

| Situation | What service returns | What user sees |
|-----------|---------------------|----------------|
| Expected business failure (validation, state) | `internal.NewPublicError("...")` | The exact message from `PublicError.Details()` |
| Unexpected / infrastructure failure | plain `error` | The fallback message |
| No error | `nil` | Normal happy-path response |

### Testing the error path

Every controller test that exercises an error branch **must** assert:

1. That a response was actually sent (the user is not left in silence).
2. That the sent text equals `PublicError.Details()` when the error is a `PublicError`.
3. That the sent text equals the fallback constant when the error is a plain error.

## Data & Privacy

This project stores the **minimum personal data required** to function as a
Telegram bot. The stance is not "zero PII" — that ship sailed when we started
keying subscriptions by Telegram `chat_id`. The stance is "no PII beyond what is
strictly necessary for the bot to deliver notifications."

### Pre-approved fields

These may be stored in user-scoped tables without further discussion:

- **Telegram `chat_id`** (column: `user_id` in `rate_user_subscriptions`,
  `rate_user_events`, `rate_user_profiles`). Unavoidable — the bot has no
  other way to address a user. Already PII under GDPR (stable persistent
  identifier), but the cost of avoiding it is "no bot."
- **IANA timezone** (e.g. `Asia/Almaty`, `Europe/Moscow`). Low-sensitivity:
  one of ~400 values, weak identifying power on its own.
- **BCP-47 locale** (e.g. `ru-RU`, `kk-KZ`, `en-US`). Same as timezone —
  low-sensitivity, useful for future localisation of notification text.
- **City coordinates** (`latitude`, `longitude` in `weather_user_cities`). These
  are user-volunteered preferences — the user explicitly searches for and selects a
  named city from a geocoding result list. They are not device-collected, geolocation
  API, or IP-derived coordinates. The coordinates are stored to request weather data
  for the chosen city and carry no more identifying power than the city name itself.
  Guardrails: values are server-re-validated (lat ∈ [-90,90], lng ∈ [-180,180]) before
  persistence; the Open-Meteo geocoding call is the only source of coordinate values.

### Off-limits fields

Do **not** add any of these to user-scoped tables without an explicit policy
change. If a feature request seems to require one of these, push back on the
design before writing SQL — there is usually a way to achieve the same UX
without persisting the field:

- Telegram `@username` / display name / first name / last name.
- Phone, email, or any other contact channel.
- Photo URL or any biometric.
- Device-collected or IP-derived precise location (lat/lng). Note: city coordinates
  explicitly chosen by the user from a geocoding search result are **pre-approved** (see
  above) — this prohibition is about coordinates obtained without the user's active
  selection (geolocation API, IP geolocation, etc.).
- IP address, device fingerprint, browser user-agent string.

### When a request looks borderline

If asked to add a field that is not on either list above, classify it first
and surface the trade-off before persisting it. Examples of borderline cases
that warrant a sanity check:

- Subscription notes / tags entered by the user (free text → may contain PII).
- Last-active timestamp at high precision (the bot already has chat_id; do we
  also need to know exactly when each user opens the Mini App?).
- Per-user notification preferences beyond the minimal set already stored.

The default for "I'm not sure if this is OK" is **don't persist it yet, ask
first**. Schema changes that add identity-adjacent columns are easier to
prevent than to revert from a production database.

### Logs

The same policy applies to log output, with one practical relaxation: the
bot's existing log lines already include `chat=<chat_id>` for observability
and that is fine. Do not log `@username`, message body content, or any other
off-limits field. The `middleware [200] GET /api/me/subscriptions` access-log
format intentionally omits the `X-Telegram-Init-Data` header for the same
reason.

## Constraints

- **Forbidden imports**: CGO-dependent SQLite drivers (e.g. `github.com/mattn/go-sqlite3`)
  must never appear in `go.mod` — persistence is pure-Go via `modernc.org/sqlite`.
  Enforced via `make lint`.
- **Testing**: Use `github.com/stretchr/testify`; run tests with `-race`; parallel
  subtests preferred where there's no shared mutable state.
- **One `Test*` per method, scenarios as subtests**: each tested method/function gets
  exactly one top-level test function named after it (e.g. `TestEncode` for `Encode`),
  and every scenario for that method lives as a `t.Run("descriptive name", ...)`
  subtest inside it. Do **not** create separate top-level tests like
  `TestEncode_EmptyInput`, `TestEncode_Unicode`, `TestEncode_Error` — these belong
  as subtests of a single `TestEncode`. Methods on a type follow the same rule with
  the standard `TestType_Method` form (e.g. `TestUser_Validate`).
  ```go
  func TestEncode(t *testing.T) {
      t.Parallel()

      t.Run("empty input returns empty string", func(t *testing.T) {
          t.Parallel()
          // ...
      })

      t.Run("unicode is preserved", func(t *testing.T) {
          t.Parallel()
          // ...
      })

      t.Run("returns error on invalid byte", func(t *testing.T) {
          t.Parallel()
          // ...
      })
  }
  ```
- **No CGO**: `CGO_ENABLED=0` must be set for all build and test commands (unless the
  project intentionally requires CGO).
- **Compile-time interface checks**: Every mock/stub struct in test files must have a
  `var _ interfaceName = &mockStruct{}` assertion at the top of the file.
- **No section-divider comments**: Do not use `// --- section ---` or `// ----` style
  separator comments. Let the code structure speak for itself.
- **No skipped errors**: Never use `_` to discard error return values in production or
  test code. Always capture the error and assert/check it. The only exceptions are
  `fmt.Fprint*` writes to loggers, `Rollback()` calls in error-recovery paths, and
  resource `.Close()` in `t.Cleanup` / `defer`.
- **Godoc on exported identifiers**: Every exported identifier (Type, Func, Method,
  Var, Const) gets a doc comment that starts with the identifier name and ends with
  a period — e.g. `// Encode returns the base64-encoded form of v.` Each package
  has exactly one `// Package <name> ...` declaration; `cmd/*` entry points use
  `// Command <name> ...` instead. Skip the comment entirely if it would only
  restate the signature — no `// Foo is a Foo.` fluff. Document concurrency
  guarantees, which methods return `PublicError` vs plain errors, constructor
  lifecycle contracts ("caller must Close"), and error sentinel conditions.
  Preserve existing WHY-comments verbatim; do not overwrite a substantive comment
  with a generic restatement. Unexported symbols only get comments when intent is
  non-obvious — do not bulk-add comments to private helpers.
- **Build outputs live in `./build/`, scratch in `./tmp/`, logs in `./logs/`**:
  Never run `go build` without `-o ./build/<name>` — bare `go build ./cmd/web`
  drops a `./web` binary in the project root, which is **not** in `.gitignore` and
  would be picked up by `git add .`. The same applies to any throwaway artifacts,
  fixtures, or intermediate files: use `./tmp/` (e.g. `./tmp/probe_*`) rather than
  the repo root. Runtime / cyclic logs go to `./logs/`. Only these three directories
  are gitignored at the root.
## Planning Workflow

All non-trivial work is tracked as a Markdown plan file before implementation begins.

### Directory layout

```
plans/
├── NNN-task-slug.md     # active / in-progress plans (e.g. 001-fix-auth.md)
├── completed/           # plans for fully shipped tasks (e.g. 260422.0001.fix-auth.md)
└── history/             # archived / cancelled plans
```

### File naming

- **Active plans (`plans/`)** — zero-padded sequential index + kebab-case slug:
  `NNN-description.md` (e.g. `001-fix-unauthorized-middleware.md`, `002-add-rate-limiting.md`).
  Pick the next number by checking the highest existing prefix across `plans/`, `plans/completed/`,
  and `plans/history/`.

- **Completed plans (`plans/completed/`)** — date prefix + zero-padded daily index (4 digits) + slug:
  `YYMMDD.NNNN.description.md` (e.g. `260422.0001.fix-unauthorized-middleware.md`).
  `NNNN` resets to `0001` each day and increments for each additional completion on that day.

- **Archived plans (`plans/history/`)** — keep the original `NNN-` filename from `plans/`.

### Lifecycle

1. **Create** — before touching code, produce a plan file in `plans/` using the `NNN-slug.md`
   naming convention described above.
2. **Implement** — work through the tasks defined in the plan. The plan file stays in
   `plans/` while work is in progress.
3. **Complete** — once every acceptance criterion is met and `make test` passes, rename
   and move the file to `plans/completed/` using the date-based convention:
   ```bash
   mv plans/001-fix-auth.md plans/completed/260422.0001.fix-auth.md
   ```
4. **Archive** — if a plan is abandoned or superseded without being fully implemented or
   if we need to save intermediate data or task execution logs, move it to `plans/history/` instead.

### Plan file format

Every plan file follows this structure:

```markdown
# Task Breakdown

## Overview
## Assumptions
## Tasks
### Task N: <Title>
- Description:
- Acceptance Criteria:
- Pitfalls & edge cases:
- Complexity: Easy / Medium / Hard
## Execution Order
## Risks
## Trade-offs
```

### Rules

- **One plan per concern.** Don't bundle unrelated changes in a single plan file.
- **Plan before code.** Claude must create (or confirm an existing) plan file before
  writing or modifying any source files.
- **Keep plans honest.** If implementation diverges from the plan, update the plan file
  before moving it to `completed/`.
- **Slug matches intent.** The description part of the filename should be readable at a glance:
  `002-add-rate-limiting.md`, `003-migrate-sqlite-to-postgres.md`, not `004-task.md`.

## Agent Pipeline

All non-trivial tasks follow a three-stage pipeline using specialized agents. The review
stage fans out to **five `gocode-reviewer` instances running in parallel**, each
with a distinct lens. A separate `gocode-testdoctor` agent is invoked on-demand
whenever tests fail, at any stage.

```
User describes task
    ↓
1. gocode-architect
    → Creates plan file at plans/NNN-slug.md (see Planning Workflow)
    ↓
2. gocode-engineer
    → Implements the tasks defined in the plan
    ↓
3. gocode-reviewer × 5 (run in parallel — single message, five tool calls)
    Lens A: correctness, races, edge cases, error paths
    Lens B: tests, coverage, flakiness, fixtures
    Lens C: ops, observability, log volume, operator UX
    Lens D: security, input validation, secrets, auth boundaries
    Lens E: performance & architecture — allocations, blocking I/O,
            goroutine/resource leaks, API contracts (breaking changes,
            exported surface stability), interface boundaries, layering
            (dependency direction), future-proofing
    ↓
   Orchestrator synthesises all five reports, deduplicates findings,
   resolves conflicts (e.g. one reviewer flags as Blocker what another
   accepts as a trade-off), and presents the merged punch list to the user.
    ↓
  ❌ Blocker/Major found?  → Back to gocode-engineer with the consolidated findings.
                             After fix, run ONE targeted reviewer pass on the changed
                             lines (not all 5 again) before re-approval.
  ⚠️  Tests failing?        → gocode-testdoctor diagnoses and patches, then rerun the
                             targeted reviewer pass.
  ✅ All five approve?      → Orchestrator moves the plan: mv plans/NNN-slug.md
                             plans/completed/YYMMDD.NNNN.slug.md
```

### Agent responsibilities

| Agent | Owns | Output |
|-------|------|--------|
| `gocode-architect` | Planning, decomposition, trade-offs | New plan file in `plans/` |
| `gocode-engineer` | Implementation, tests for new code | Code + tests in the repo |
| `gocode-reviewer` (×5, parallel) | Lens-specific verdicts, severity-ranked findings, patch sketches | Five independent review reports |
| `gocode-testdoctor` | Triage of failing tests, minimal patches | Code/test fixes, re-run of `make test` |

The orchestrating agent (the main Claude session driving the pipeline) owns
synthesis: merging the five reports, resolving conflicting verdicts, deciding
which findings to act on, and moving the plan to `completed/` once everyone
signs off.

### Rules

- **No skipping stages.** Every task starts with the architect and ends with the five-reviewer fan-out.
- **Plan file first.** The architect MUST produce a plan file before any code is written. If a plan already exists for the task, update it rather than creating a new one.
- **Five reviewers, five lenses, one message.** All five `gocode-reviewer` agents are launched in a single tool-call batch (multiple `Agent` blocks in one message) so they run in parallel. Each prompt names the lens explicitly and tells the agent what to SKIP (the other lenses) to avoid duplicated work.
- **No solo reviewer pass on first review.** Even for small changes the full five-lens fan-out is required, because the lenses catch genuinely different classes of issue (Reviewer A won't see test gaps, Reviewer D won't see log-volume problems). Skipping lenses is what the orchestrator does AFTER a Blocker/Major fix, not BEFORE the first verdict.
- **Lens prompts are self-contained.** Each reviewer's prompt must include: (1) the lens name, (2) what to focus on, (3) what to SKIP (so it doesn't restate other lenses), (4) the file list, (5) the deliverable shape (Blocker / Major / Minor / Nit with file:line + patch sketch), (6) the word cap (typically 600 words).
- **Re-review after fixes is single-pass.** Once an engineer addresses Blocker/Major findings, the orchestrator runs ONE reviewer pass scoped to the changed lines, not the full fan-out. Re-running all five each iteration is expensive and rediscovers nothing.
- **Conflict resolution is explicit.** When reviewers disagree (one says Blocker, another says trade-off), the orchestrator chooses, names the rejected suggestion, and explains the reasoning to the user before moving on. The user has final say.
- **Orchestrator gates completion.** The plan moves to `plans/completed/` only after every reviewer's Blocker and Major findings are addressed (either fixed, or explicitly accepted with rationale). The rename uses the standard `YYMMDD.NNNN.slug.md` format.
- **`make test` must pass** before review begins. If it fails, hand the logs to `gocode-testdoctor` first — reviewers should not waste time on a red tree.
- **Testdoctor is scoped.** It patches tests or the minimal production code needed to make the failure go away. It does not redesign or refactor.
