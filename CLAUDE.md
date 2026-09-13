# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

It is deliberately a map, not a manual. It holds what applies to every task plus the rules
whose violation is silent; the depth lives in the project skills listed below and is loaded
on demand.

## Project skills

Invoke these by name (Skill tool) when the work touches their area. Each is the full canon
for its subject — this file only keeps the tripwire.

| Skill | Load before touching |
|---|---|
| `beacon-collection` | `cmd/collector`, `cmd/doctor`, `rateextractor`, `application/collection`, `infrastructure/weather`, `SourceHealthAgent`, `rate_sources` rows, `BEACON_PROXY_URL` / `options.use_proxy`, weather alert kinds, `ForecastRange`, `forecast_outlook` |
| `beacon-storage` | any `internal/repository` query, any `./migrations/*.sql`, `MaintenanceAgent`, `sqlitedb.Migrator`, `weather_forecast_days`, reading the production database |
| `beacon-http-api` | `internal/gateway`, `cmd/web`, `cmd/wasm`, `cmd/web/static`, `configs/nginx.*`, `internal.PublicError`, any `/api/v1/me` or `/api/v1/public` route |
| `beacon-forecasting` | `internal/tools/rateforecaster`, `internal/tools/rateanomaly` (load with `knowledge:forecasting`) |
| `beacon-data-privacy` | any new column on a user-scoped table, anything captured from a Telegram update, any new log field |

Generic Go conventions (style, declaration order, test structure, godoc, error discipline,
build hygiene, organisation) come from the `stack-go` plugin skills and are not restated
anywhere in this repo.

### Where new canon goes

This file is loaded whole into every session and stays there, so its size is a tax on every
conversation regardless of what the task touches. Keep it under **20k chars**; 40k is where
Claude Code warns about performance. Route new documentation by *when the reader needs it*,
not by how important the subject feels:

- **CLAUDE.md** — what applies to every task (the binary map, layer table, env vars, the
  working agreement), plus rules whose violation is **silent**. A tripwire keeps its place
  here even after its subject has moved out.
- **A project skill** (`.claude/skills/<name>/SKILL.md`) — the depth for one subject area.
  The `description` frontmatter *is* the load trigger: name the packages, paths, symbols
  and env vars that should pull it in. A description that summarises the prose instead of
  naming triggers means the skill never loads and the knowledge is lost.
- **Neither** — incident narratives, enumerations derivable from the code, and the
  reasoning behind a decision already taken. Those belong in commit bodies, `plans/` and
  `docs/decisions/`.

**Every subject moved into a skill leaves one line behind**, under Tripwires below. The skill
carries the why; CLAUDE.md carries the sentence that stops someone getting it wrong before
they think to load anything. This is not redundancy — it is the whole reason the split is
safe. Reserve it for failures that do not announce themselves: a read that skips a storage
tier returns partial history without erroring, and an identity-adjacent column is far cheaper
to prevent than to revert from production.

**Measure, never estimate.** Count with `wc -c` before and after: this file is mostly
contracts and identifiers, which do not compress, so a guess runs high. After moving
content, extract every backticked span and figure from the old text, confirm each still
appears somewhere in the new set, and account for every casualty by name. Full
procedure: `standards-layout` R21.

## Build & Run Commands

Pure-Go build, `CGO_ENABLED=0` by default. Standard `make` targets (`build`, `run`, `test`, `lint`, `format`, `clean`) — see the Makefile; `make test` runs fmt + vet + `go test -race` + the WASM suite, `make lint` also checks forbidden imports.

**`make test` is the gate; `make lint-new` is advisory until the lint backlog is cleared** — it lints only what changed since `LINT_BASE` (default `origin/alpha`), while `make lint` scans the whole tree as a worklist. Both run **two** steps, `golangci-lint run` *and* `scripts/lint-checks.sh`, so a green `golangci-lint` is not a green gate.

Gotcha: `-race` needs cgo, so targeted race runs use `CGO_ENABLED=1 go test -race -run TestX ./<pkg>/` (macOS tolerates `0`, Linux does not). Benchmarks (`-bench=.`, no `-race`) don't need cgo. `make test` starts with `go clean -cache`, so a full run rebuilds `modernc.org/sqlite` from scratch — minutes, not seconds. On the 8 GB Pi (no swap) that rebuild is OOM-killed under `-race`; rerun as `go test -race -p 1`, or cut an `s_*` tag and let CI run the gate. When `node` is missing `make test` skips the WASM suite with a warning and still exits 0.

## Architecture

A self-hosted FX-rate monitor. The `collector` binary scrapes each configured rate
source on every invocation (plain HTTP, or a chromedp-driven headless browser for
JS-rendered pages), extracts the numeric rate via per-source rules, and stores it in
SQLite. The `notifier` binary runs a check-agent that evaluates user subscription
conditions (delta / interval / daily / cron) against the latest rates and enqueues
notifications, and a dispatch-agent that drains the pool and sends them over Telegram.
The `web` binary serves a REST API plus an embedded dashboard (HTML and a WASM build)
and routes Telegram callbacks. `migrator` applies schema migrations; `doctor` provides
operator tooling (LLM rule generation and source auditing).

| Layer | Location | Role |
|-------|----------|------|
| Entry point | `cmd/<binary>/` | Composition root per binary (collector, notifier, web, migrator, doctor, wasm) |
| Application | `internal/application/` | What the answer is, free of transport: collection, notification, chart, digest, rulegen, sourceaudit |
| Domain | `internal/domain/` | Value objects / models, no logic |
| DTO | `internal/dto/` | JSON wire contract shared by the server (gateway) and the WASM client |
| Gateway | `internal/gateway/` | Receiving and rendering: HTTP routers, middleware, Telegram update loop |
| Repository | `internal/repository/` | Persistence queries |
| Infrastructure | `internal/infrastructure/` | External clients (SQLite, Telegram, AI providers) |
| Tools | `internal/tools/` | Cross-cutting utilities |
| Frontend | `cmd/wasm/` | GOOS=js GOARCH=wasm dashboard (apiclient, application, ui, dom) |

Routes are registered in `internal/gateway/`; wire shapes live in `internal/dto`. `GET /ping`
(alias `/healthz`) is liveness and touches no dependency; `GET /health/check` is readiness and
probes every dependency for real. Both are unauthenticated. Per-endpoint contracts, the
forced weather subscriptions and their 409, content-hashed WASM URLs and the nginx location
ordering they depend on, and Mini App navigation: **skill `beacon-http-api`**.

Persistence is SQLite through the pure-Go `modernc.org/sqlite` driver (no CGO):
`foreign_keys=ON` and `busy_timeout=5000` ride on the DSN as `?_pragma=` parameters,
`journal_mode=WAL` is persisted in the file header. `cmd/migrator` is the only thing that
mutates schema; it lives at `./migrations/*.sql` and applied filenames are **immutable**.
Source `kind`, the `options` JSON column and `cmd/doctor`, the operator-only umbrella for
rule generation and source auditing: **skill `beacon-collection`**.

## Tripwires

Each of these fails **without an error**. The reasoning, and everything that does announce
itself, is in the named skill.

**Collection — skill `beacon-collection`.** Egress is direct by default: two levels must
agree before anything is proxied, `BEACON_PROXY_URL` says a proxy exists and
`rate_sources.options.use_proxy` says the source wants it. No source is opted in today, and
the default is a measured decision (issue #16) — do not reverse it casually. Chromedp and
weather stay direct regardless. **Never widen `OpenMeteo.Forecast`'s `daily` block**: its
index `[0]` *is* today for the morning summary and all four daily-metric latches, which is
why the multi-week fetch is a separate call (`ForecastRange`, its own table, its own daily
cadence). Several sources may share one URL and therefore one fetch; that batching is
load-bearing and easy to break.

**Storage — skill `beacon-storage`.** **Writes go through `Transaction` (`BEGIN IMMEDIATE`),
reads through `ReadOnlyTransaction`**: a read on the write path serialises against every
other read and never says so — including `SQLiteClient.Rollback`, `Ping` and the
`/health/check` inspector, which are read-only precisely so a readiness probe queued behind a
collector tick cannot report a busy database as a dead one. A deferred transaction that
*promotes* at its first write is refused the busy handler and gets `SQLITE_BUSY` on the spot,
so the 5 s retry window never applies to it. **Two write transactions cannot be open at
once** in one process: open, write and commit inside one function.

**`rate_values` and `execution_history` are tiered.** Each has an `*_archive` twin in the
same file: reads must span both via `UNION ALL`, writes touch hot only, and getting it wrong
returns partial history without erroring. **Long-range forecast rows belong in
`weather_forecast_days`, never in `weather_observations`** — the collector sweeps that table
by `captured_at` at 48 h on every tick, so a row describing a day two weeks out is gone a day
and a half after it is written. Two columns look free to change and are not:
**`weather_observations.provider`** only ever holds `'open-meteo'` but partitions two
composite indexes, and **runtime state never goes on `rate_sources`** because
`RetainRateSource` rewrites those rows wholesale (`cmd/doctor rulegen` does exactly that),
which is why the source-health latch lives in its own `rate_source_health` table.
**Deleting a source destroys its history**: `rate_values`, `rate_user_subscriptions` and
`rate_user_events` cascade from `rate_sources(name)` — read the warning on `RemoveRateSource`
before wiring it to any endpoint. Service binaries call `sqlitedb.RequireMigratedSchema` and
refuse to start against a schema behind their own build.

**HTTP — skill `beacon-http-api`.** The `/api/v1/me/*` family is the **only** authenticated
surface, and the check runs **once**, in `middleware.TelegramInitData` mounted over
`routes.MePrefix` — **a new authenticated route belongs on that inner mux; putting it on the
outer one is a bypass, and nothing will say so.** Handlers read the caller via
`middleware.UserIDFrom` and refuse without it. The signed `initData` is accepted **only** in
the `X-Telegram-Init-Data` header, never a query string, which would leak a signed payload
into access logs and `Referer`. A `/api/v1/me/*` resource owned by another user returns
**404, never 403** — existence is not disclosed, anywhere. `internal.PublicError` (in
`internal/errors.go`, alongside `TraceError`, `StackTraceError`, `HttpCodeError` and the
`ErrNotFound` sentinel) carries messages **safe to show to end users**: wrap where the error
is created, return a plain `error` for everything else, and the controller renders
`Details()` or a generic fallback. **Every controller test on an error branch owes three
assertions** — a response was sent at all, its text for a public error, its text for a plain
one. The first is the one that catches a handler returning without writing anything.

**Startup ordering.** Anything that logs or can `log.Fatalf` on bad config belongs in `main`
*after* the logger exists, never in a package initialiser: the cron wrappers discard stderr,
so a line emitted earlier is attributable to nothing. Operators grep the marker sequence
`logger -> settings -> dependencies -> repositories -> runners`.

**There is no staging.** An `r_*` tag, prerelease or not, flips the production symlink and
restarts the service. Tags are cut from `alpha`, not `main` — see the working agreement. Do
not tag casually. Delete the superseded alpha tag, local and remote, once the new one is
live. An **`s_*` tag runs the gate only** — lint, tests, production-shape build, no host
contact — for when the full gate will not run locally. Remote hosts are read-freely,
mutate-never without explicit per-action approval.

## Configuration

- `BEACON_SQLITEDB_DSN` — SQLite connection string, parsed via `dsninjector.Unmarshal`. Format: `sqlite://<path-to-db-file>`
- `BEACON_TELEGRAMBOT_DSN` — Telegram bot credentials parsed via `dsninjector.Unmarshal`. Format: `<adminChatID>:<botToken>@<host>` where `Addr()` returns the token and `Login()` returns the admin chat ID.
- `BEACON_PROXY_URL` — optional outbound proxy. Format: `<scheme>://<host>:<port>` (e.g. `http://127.0.0.1:7788`), resolved through `proxyutil.ResolveURL`. `cmd/doctor` proxies unconditionally; `cmd/collector` routes nothing through it on its own — see the egress tripwire above. Telegram Bot API traffic bypasses any proxy, enforced by a hardcoded `Proxy: nil` transport in `internal/infrastructure/telegrambot/tbotclient.go`. `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` are consulted by no component here.
- `BEACON_CHROMIUM_PATH` — optional absolute path to the Chromium/Chrome binary for `fetcher_kind='chromedp'` sources. Read by `cmd/collector` and `cmd/doctor`. When unset, chromedp searches PATH (`chromium`, `chromium-browser`, `google-chrome`, `chrome`).
- `BEACON_AI_PRIMARY_DSN` (required) and `BEACON_AI_FALLBACK_DSN` (optional) — AI provider DSNs read only by `cmd/doctor rulegen`. See `cmd/doctor/README.md` for the DSN format and provider details.

> The public HTTPS origin of the `cmd/web` server is **not** an env var — see the `--api-dsn` CLI flag on the `cmd/web` binary, baked into the systemd unit's `ExecStart` line.

> Never read or edit `.env` files.

**Deployment.** Immutable `/opt/beacon/artifacts/<VERSION_ID>/` build sets behind a
`bin/release` channel symlink. **Security boundary**: the CI deploy user may write only under
`artifacts/` and `bin/`; `.env`, the DB and the base dir are root-owned and out of reach. The
`release.yml` job (on an `r_*` tag) uploads an artifact set, flips the symlink, migrates via
the **`beacon-migrate` one-shot unit (root, so the deploy user never writes the DB)**,
restarts `beacon`, and health-gates on `/health/check` with one-symlink rollback —
reconciliation is deploy-time, and the service unit has no `ExecStartPre` migrator.
`make init` provisions the layout, both units, the sudoers grants and the nginx vhost;
`make deploy-configs` ships later `configs/` changes passwordlessly, except the two sudoers
files and the installer itself, which stay with `init` because an installer that could
rewrite its own grant would be passwordless root. See `deploy/README.md`.

## Data & Privacy

This project stores the **minimum personal data required** to function as a Telegram bot —
not zero PII, but nothing beyond what delivering notifications requires.

Pre-approved for user-scoped tables, no discussion needed: Telegram `chat_id`, IANA
timezone, BCP-47 locale, and coordinates of a city the user picked from a geocoding search.

**Off limits without an explicit policy change**: `@username` or any name, phone, email,
photo, biometrics, device- or IP-derived location, IP address, device fingerprint,
user-agent. Same list for log output — `chat=<chat_id>` is fine, nothing else is.

Anything not on either list: **do not persist it yet, ask first.** Identity-adjacent columns
are far easier to prevent than to revert from a production database. Full policy, the
guardrails on each pre-approved field, and how to classify a borderline one: **skill
`beacon-data-privacy`**.

## Constraints

- **Forbidden imports**: CGO-dependent SQLite drivers (e.g. `github.com/mattn/go-sqlite3`)
  must never appear in `go.mod` — persistence is pure-Go via `modernc.org/sqlite`.
  Enforced via `make lint`.
- **Scratch files** go to `./tmp/` (e.g. `./tmp/probe_*`), never the repo root; bare
  `go build ./cmd/web` drops a `./web` binary in the root, which is not gitignored.

## Working agreement

Plan-first pipeline; the canonical procedure is the `pipeline:working-agreement` skill — load it
before starting non-trivial work. Project delta:

- **Gate:** `make test`
- **Lenses:** standard staged set — see `pipeline:working-agreement`.
- **Branching:** `type/<issue>-<slug>` off **`alpha`**, PR into **`alpha`** (pass `--base alpha`
  explicitly — `gh pr create` defaults to `main`, the stale release pointer). `main` only moves
  to the latest non-prerelease tag and trails `alpha`; never commit to either directly. A merge
  into `alpha` does not auto-close issues (`Closes #N` fires only on the default branch) — close
  them by hand, naming the squash commit and its tag.
