# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run Commands

Pure-Go build, `CGO_ENABLED=0` by default. Standard `make` targets (`build`, `run`, `test`, `lint`, `format`, `clean`) — see the Makefile; `make test` runs fmt + vet + `go test -race`, `make lint` also checks forbidden imports.

Gotcha: `-race` needs cgo, so targeted race runs use `CGO_ENABLED=1 go test -race -run TestX ./<pkg>/` (macOS tolerates `0`, Linux does not). Benchmarks (`-bench=.`, no `-race`) don't need cgo.

## Architecture Overview

A self-hosted FX-rate monitor. The `collector` binary scrapes each configured rate
source on every invocation (plain HTTP, or a chromedp-driven headless browser for
JS-rendered pages), extracts the numeric rate via per-source rules, and stores it in
SQLite. The `notifier` binary runs a check-agent that evaluates user subscription
conditions (delta / interval / daily / cron) against the latest rates and enqueues
notifications, and a dispatch-agent that drains the pool and sends them over Telegram.
The `web` binary serves a REST API plus an embedded dashboard (HTML and a WASM build)
and routes Telegram callbacks. `migrator` applies schema migrations; `doctor` provides
operator tooling (LLM rule generation and source auditing). Sources use a `kind` of
`BID`, `ASK`, or `LAST` (equity / last-traded price); sources that require a custom
`User-Agent` or other header overrides store them in `RateSourceOptions.Headers` (the
`options` JSON column), applied per-request by the plain fetcher.

### Weather providers

Open-Meteo (`domain.ProviderOpenMeteo`) is the sole weather provider: global, keyless
JSON, hardcoded always-on (no `active` toggle, no per-provider config row). The
collector fetches it per tick for every distinct subscribed location, throttled by
`collection.DefaultWeatherThrottleInterval` per location. `weather_observations.provider`
is a retained vestigial column (it partitions two composite indexes) that now only ever
holds `'open-meteo'`; it was kept rather than dropped to avoid a rebuild of the largest
weather table for zero functional gain.

Egress asymmetry: `cmd/collector` honours `BEACON_PROXY_URL`; the Open-Meteo inspector
in `cmd/web` probes **direct** (cmd/web ignores `BEACON_PROXY_URL`), so a false "down"
there can't fail the deploy gate — the inspector is advisory.

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

Routes are registered in `internal/gateway/` (grep the path literals for the full list); wire shapes live in `internal/dto`. Only the contracts that aren't obvious from that code live here:

- **Auth** — the `/api/me/*` family is the only authenticated surface (HMAC algorithm in Key Patterns). The signed Telegram WebApp `initData` is accepted **only** in the `X-Telegram-Init-Data` header, never via query string (a signed payload in the URL leaks into access logs and `Referer`).
- **Ownership → 404, not 403** — reading or mutating a `/api/me/*` resource (subscription, weather city) owned by another user returns **404**, never 403, to avoid existence disclosure. Deleting a subscription does **not** cascade-delete its `rate_user_events` rows.
- **Chart endpoints** (`/api/me/rates/chart`, `/api/public/rates/chart`) — `period` is an integer-days whitelist `{7,30,90,180,360}` (default 7); anything else is 400 with a `PublicError` body. Equity (`kind=LAST`) pairs render under the `equity` category with an amber series (`#D98E04`).
- **Weather city create** — server re-validates `timezone` via `time.LoadLocation`, `latitude in [-90,90]`, `longitude in [-180,180]`, `notify_hour in [0,23]` (default 7). Creating any city also auto-ensures a forced `alert_thaw` row for that location (idempotent second `RetainWeatherUserCity`) unless the requested kind already is thaw — thaw is system-managed and always-on for every tracked city.
- **Weather alert delete** — `DELETE /api/me/weather/cities/{id}` removes one subscription row, but a direct delete of an `alert_thaw` row returns **409 + PublicError** (ownership check runs first, so cross-user/missing is still 404, never 409). To turn thaw off, remove the whole location via `DELETE /api/me/weather/locations/{location_id}`, which deletes every row (all kinds, including thaw) the caller owns there (204; 404 when the caller owns nothing at that location — no existence disclosure). The Mini App renders no ✕ on the thaw row and offers a separate per-city "Remove city" control.
- **`GET /`** — dispatcher inline script routes on `window.Telegram.WebApp.initData`: non-empty → Mini App view, empty → public view.
- **`GET /ping`** (alias `/healthz`) — liveness, always 200, touches no dependency, no auth.
- **`GET /health/check`** — readiness; runs all inspectors under a 3s bound, per-component report. Critical (`sqlite`, `telegram`) flip `status=false` → HTTP 503; advisory (`open-meteo`) appears but never forces 503 (a weather outage must not fail the deploy gate). No auth.

### Static asset caching

`app.wasm` (~4 MB) and `wasm_exec.js` are served under content-hashed URLs (`/app.<8hex>.wasm`, `/wasm_exec.<8hex>.js`) so nginx caches them 7 days immutable. The 8-hex is the first 4 bytes of SHA-256 over the **raw** (uncompressed) bytes, computed at `cmd/web` boot — hashing raw means a gzip-level change alone doesn't bust the URL. `cmd/web` rewrites `/app.wasm` and `/wasm_exec.js` to their hashed forms in the in-memory HTML for `/`, `/index.html`, `/admin/`, `/admin/index.html`. Go origin serves a pre-built `app.wasm.gz` sibling with `Content-Encoding: gzip`; nginx gzips `wasm_exec.js` on the wire.

The nginx regex location in `configs/nginx.beacon_common_settings.conf` (`^/(app|wasm_exec)\.[a-f0-9]{8}\.(wasm|js)$`) **must** sit above the catch-all `location /` — nginx evaluates regex locations in source order. Unhashed `/app.wasm` / `/wasm_exec.js` fall through to `http.FileServer`, so a browser holding stale HTML still loads the current bytes.

> `cmd/doctor` is the operator-only umbrella for LLM rule (re)generation and source auditing (`rulegen` single/`--all`, `audit --all`/`--source`). Usage, exit codes, and env vars: `cmd/doctor/README.md` + godoc.

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

Repository files in `internal/repository/` reference table and column names
exclusively through `const` declarations (e.g. `rateSourceTableName`,
`rateSourceNameFieldName`) so a schema rename surfaces at compile time and via
`grep`, never via a runtime "no such column" error.

Deploy flow:
```
make build         # builds all binaries including ./build/migrator
make migrate       # applies any pending .sql files (no-op if up to date)
make run           # starts collector, notifier, web
```

### Environment Variables

- `BEACON_SQLITEDB_DSN` — SQLite connection string, parsed via `dsninjector.Unmarshal`. Format: `sqlite://<path-to-db-file>`
- `BEACON_TELEGRAMBOT_DSN` — Telegram bot credentials parsed via `dsninjector.Unmarshal`. Format: `<adminChatID>:<botToken>@<host>` where `Addr()` returns the token and `Login()` returns the admin chat ID.
- `BEACON_PROXY_URL` — optional outbound proxy URL, parsed via `dsninjector.Unmarshal`. Format: `<scheme>://<host>:<port>` (e.g. `http://127.0.0.1:7788`). When unset or empty all outbound traffic is direct. Used by `cmd/collector` (plain and chromedp rate sources) and `cmd/doctor` (AI provider calls and chromedp fetcher). Telegram Bot API traffic bypasses the proxy unconditionally — the bypass is enforced in code via a hardcoded `Proxy: nil` transport in `internal/infrastructure/telegrambot/tbotclient.go`. Do not configure `HTTPS_PROXY`, `HTTP_PROXY`, or `NO_PROXY` for proxy routing — they are not consulted by any component in this project.
- `BEACON_CHROMIUM_PATH` — optional absolute path to the Chromium/Chrome binary for `fetcher_kind='chromedp'` sources. Read by `cmd/collector` and `cmd/doctor`. When unset, chromedp searches PATH (`chromium`, `chromium-browser`, `google-chrome`, `chrome`).
- `BEACON_AI_PRIMARY_DSN` (required) and `BEACON_AI_FALLBACK_DSN` (optional) — AI provider DSNs read only by `cmd/doctor rulegen`. See `cmd/doctor/README.md` for the DSN format and provider details.

> The public HTTPS origin of the `cmd/web` server is **not** an env var — see the `--api-dsn` CLI flag on the `cmd/web` binary, baked into the systemd unit's `ExecStart` line.

> Never read or edit `.env` files.

### Frontend

Static assets live in `cmd/web/static/` (embedded via `//go:embed static`); the WASM bundle builds from `cmd/wasm` (`GOOS=js GOARCH=wasm`) to `cmd/web/static/app.wasm`, sharing `internal/dto` wire types with the server. `make build` produces it.

The `webAppURL` BotFather setting must point to `https://<host>/` (trailing slash, no path suffix) — update it whenever the host changes.

### Deployment

Standard release layout: immutable `/opt/beacon/artifacts/<VERSION_ID>/` build sets and a `bin/release` channel symlink the units run through. **Security boundary**: the CI deploy user may write only under `artifacts/` and `bin/`; `.env`, the DB, and the base dir are root-owned and out of reach. The `release.yml` job (on an `r_*` tag) uploads a new `artifacts/<VERSION_ID>/`, flips the symlink, runs migrations via the **`beacon-migrate` one-shot unit (root, so the deploy user never writes the DB)**, restarts `beacon`, and health-gates on `/health/check` with one-symlink rollback. Schema reconciliation is deploy-time, not startup-time — the service unit has no `ExecStartPre` migrator. `make init` provisions the layout, both units, the narrow `/etc/sudoers.d/beacon-deploy`, and the nginx vhost. See `deploy/README.md`.

## Error Handling

`internal.PublicError` (in `internal/errors.go`, alongside `TraceError`, `StackTraceError`, `HttpCodeError`, and the `ErrNotFound` sentinel) carries messages **safe to show to end users**. Wrap at the point the error is created (service layer) with `internal.NewPublicError("...")` when the failure meaningfully tells the user something; return a plain `error` for everything else (DB down, unexpected nil, ...). The controller catches every sub-handler error and sends `PublicError.Details()` for a public error, else a generic fallback constant.

Every controller test on an error branch must assert: (1) a response was actually sent (user not left in silence), (2) its text equals `PublicError.Details()` for a public error, (3) its text equals the fallback constant for a plain error.

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
- **Godoc on exported identifiers**: every exported Type/Func/Method/Var/Const gets a doc comment starting with its name and ending with a period; one `// Package <name>` per package (`// Command <name>` for `cmd/*`). Skip it if it would only restate the signature. Document the non-obvious: concurrency guarantees, which methods return `PublicError` vs plain errors, lifecycle contracts ("caller must Close"), error-sentinel conditions. Preserve existing WHY-comments verbatim; comment unexported symbols only when intent is non-obvious.
- **Build outputs live in `./build/`, scratch in `./tmp/`, logs in `./logs/`**:
  Never run `go build` without `-o ./build/<name>` — bare `go build ./cmd/web`
  drops a `./web` binary in the project root, which is **not** in `.gitignore` and
  would be picked up by `git add .`. The same applies to any throwaway artifacts,
  fixtures, or intermediate files: use `./tmp/` (e.g. `./tmp/probe_*`) rather than
  the repo root. Runtime / cyclic logs go to `./logs/`. Only these three directories
  are gitignored at the root.

## Planning Workflow

Non-trivial work is tracked as a Markdown plan file before implementation.

- **Active** (`plans/`) — `NNN-slug.md`, zero-padded sequential; next number = highest existing prefix across `plans/`, `completed/`, and `history/`; readable slug (`002-add-rate-limiting.md`, not `004-task.md`).
- **Completed** (`plans/completed/`) — `YYMMDD.NNNN.slug.md`, where `NNNN` is a daily index resetting to `0001` each day. Move here only when every acceptance criterion is met and `make test` passes.
- **Archived** (`plans/history/`) — abandoned/superseded plans, keeping their original `NNN-` filename.

Rules: one plan per concern; create (or confirm) the plan before touching source; if implementation diverges, update the plan before completing it. Each plan carries: Overview, Assumptions, Tasks (each with Description / Acceptance Criteria / Pitfalls / Complexity), Execution Order, Risks, Trade-offs.

## Agent Pipeline

Non-trivial tasks run a three-stage pipeline; no stage is skipped:

1. **`gocode-architect`** → writes/updates the plan file in `plans/` (see Planning Workflow) before any code.
2. **`gocode-engineer`** → implements the plan tasks plus their tests.
3. **`gocode-reviewer` ×5, parallel** — launched in a **single message** (five tool calls) so they run concurrently; each prompt names its lens and states what to SKIP to avoid overlap:
   - **A** correctness, races, edge cases, error paths
   - **B** tests, coverage, flakiness, fixtures
   - **C** ops, observability, log volume, operator UX
   - **D** security, input validation, secrets, auth boundaries
   - **E** performance & architecture — allocations, blocking I/O, leaks, API-contract / exported-surface stability, layering

The orchestrator (main session) synthesises the five reports, resolves conflicting verdicts (naming the rejected suggestion; user has final say), and gates completion: the plan moves to `plans/completed/` only once every Blocker/Major is fixed or explicitly accepted. `make test` must be green before review — hand a red tree to **`gocode-testdoctor`** (scoped to the minimal patch that goes green, no redesign) first. After a fix, re-review is a **single** pass scoped to the changed lines, not another five-way fan-out.
