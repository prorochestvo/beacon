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
| `beacon-collection` | `cmd/collector`, `rateextractor`, `application/collection`, `infrastructure/weather`, `SourceHealthAgent`, `rate_sources` rows, `BEACON_PROXY_URL` / `options.use_proxy`, weather alert kinds |
| `beacon-storage` | any `internal/repository` query, any `./migrations/*.sql`, `MaintenanceAgent`, `sqlitedb.Migrator`, reading the production database |
| `beacon-http-api` | `internal/gateway`, `cmd/web`, `cmd/wasm`, `cmd/web/static`, `configs/nginx.*`, any `/api/me` or `/api/public` route |
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

- **CLAUDE.md** — what applies to every task (the binary map, layer table, key patterns,
  env vars, error handling, the working agreement), plus rules whose violation is
  **silent**. A tripwire keeps its place here even after its subject has moved out.
- **A project skill** (`.claude/skills/<name>/SKILL.md`) — the depth for one subject area.
  The `description` frontmatter *is* the load trigger: name the packages, paths, symbols
  and env vars that should pull it in. A description that summarises the prose instead of
  naming triggers means the skill never loads and the knowledge is lost.
- **Neither** — incident narratives, enumerations derivable from the code, and the
  reasoning behind a decision already taken. Those belong in commit bodies, `plans/` and
  `docs/decisions/`.

**Every subject moved into a skill leaves one line behind.** The skill carries the why;
CLAUDE.md carries the sentence that stops someone getting it wrong before they think to
load anything. This is not redundancy — it is the whole reason the split is safe. Reserve
it for failures that do not announce themselves: a read that skips a storage tier returns
partial history without erroring, and an identity-adjacent column is far cheaper to prevent
than to revert from production.

**Measure, never estimate.** This file is mostly contracts and identifiers, which do not
compress — only the prose around them does, so a guess at what a rewrite will save runs
high. Count with `wc -c` before and after. After moving content, prove nothing was dropped
rather than assuming it: extract every backticked span and figure from the old text, confirm
each still appears somewhere in the new set, and account for every apparent casualty by
name.

## Build & Run Commands

Pure-Go build, `CGO_ENABLED=0` by default. Standard `make` targets (`build`, `run`, `test`, `lint`, `format`, `clean`) — see the Makefile; `make test` runs fmt + vet + `go test -race`, `make lint` also checks forbidden imports.

Gotcha: `-race` needs cgo, so targeted race runs use `CGO_ENABLED=1 go test -race -run TestX ./<pkg>/` (macOS tolerates `0`, Linux does not). Benchmarks (`-bench=.`, no `-race`) don't need cgo. `make test` starts with `go clean -cache`, so a full run rebuilds `modernc.org/sqlite` from scratch — minutes, not seconds.

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

Sources use a `kind` of `BID`, `ASK`, or `LAST` (equity / last-traded price). Per-source
fetch behaviour — header overrides, the proxy opt-in — lives in the `options` JSON column
(`domain.RateSourceOptions`). Several sources may share one URL and therefore one fetch;
that batching is load-bearing and easy to break. **Skill: `beacon-collection`.**

**Collection egress is direct by default.** Two levels must agree before anything is
proxied: `BEACON_PROXY_URL` says a proxy exists, `rate_sources.options.use_proxy` says the
source wants it. No source is opted in today, and the default is a measured decision
(issue #16) — do not reverse it casually. Chromedp and weather stay direct regardless.

> `cmd/doctor` is the operator-only umbrella for LLM rule (re)generation and source auditing (`rulegen` single/`--all`, `audit --all`/`--source`). Usage, exit codes, and env vars: `cmd/doctor/README.md` + godoc.

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

- **Repository pattern** — each repository type owns its own SQL, migration, and query helper functions. Queries execute inside explicit transactions (`r.db.Transaction(ctx)` to write, `r.db.ReadOnlyTransaction(ctx)` to read). Repositories are passed as interfaces into service and handler layers.
- **Configuration injection** — `BEACON_SQLITEDB_DSN` and `BEACON_TELEGRAMBOT_DSN` are read via `dsninjector.Unmarshal(envName)` at startup in `cmd/web/main.go` and live in the systemd `EnvironmentFile`. The public HTTPS origin is passed via the `--api-dsn` CLI flag (format: `https://<host>/`, parsed by `dsninjector.Parse`) and is hardcoded in the systemd unit's `ExecStart` line — never in `.env`. All three configs must be present at startup; the binary calls `log.Fatalf` on any missing value.
- **Startup ordering** — anything that logs or can `log.Fatalf` on bad config belongs in `main` *after* the logger exists, never in a package initialiser: the cron wrappers discard stderr, so a line emitted earlier is attributable to nothing. Operators grep the marker sequence `logger -> settings -> dependencies -> repositories -> runners`.
- **Embedded assets** — `cmd/web/main.go` embeds the `static/` directory via `//go:embed static`. All static files served by `http.FileServer` live under `cmd/web/static/`.
- **Auth: Telegram WebApp initData HMAC** — the `/api/me/...` endpoint family authenticates callers by verifying the Telegram WebApp `initData` HMAC-SHA256 signature. The signing algorithm uses `secret_key = HMAC_SHA256("WebAppData", botToken)` (the string literal is the key; the token is the message). Implementation lives in `internal/tools/tgwebapp/initdata.go`. The handler injects the validator as a function field so tests can substitute a fake without real bot tokens. No other endpoint requires this auth.

### HTTP surface

Routes are registered in `internal/gateway/`; wire shapes live in `internal/dto`. Two rules
hold everywhere and are easy to break silently:

- The `/api/me/*` family is the **only** authenticated surface, and the signed `initData` is
  accepted **only** in the `X-Telegram-Init-Data` header — never a query string, which would
  leak a signed payload into access logs and `Referer`.
- A `/api/me/*` resource owned by another user returns **404, never 403**. Existence is not
  disclosed, anywhere.

Everything else — per-endpoint contracts, the forced weather subscriptions and their 409,
content-hashed WASM URLs and the nginx location ordering, Mini App navigation — is in the
**`beacon-http-api`** skill.

`GET /ping` (alias `/healthz`) is liveness and touches no dependency; `GET /health/check` is
readiness and probes every dependency for real. Both are unauthenticated.

### Database

Engine: SQLite, accessed via the pure-Go `modernc.org/sqlite` driver (no CGO).

Three PRAGMAs are applied on connection open:
- `foreign_keys=ON` and `busy_timeout=5000` are passed as `?_pragma=`
  query parameters on the DSN (see `connectionOptions` in
  `config.go`). The `modernc.org/sqlite` driver re-applies them in
  its `Open` hook on every new connection the `database/sql` pool
  opens, which is the only way to keep these per-connection settings
  consistent across `SetMaxOpenConns(N>1)`.
- `journal_mode=WAL` is persisted in the database file header and is
  set once via `db.Exec` inside `NewSQLiteClientEx`.

`busy_timeout` (5 s) is the driver-level retry window for lock
contention; it must stay strictly less than the Go-level `Timeout` so
the context deadline always fires after the driver retry expires.

**Writes open `BEGIN IMMEDIATE`; reads stay deferred.** The DSN also carries
`_txlock=immediate`, and the driver applies that begin mode only when
`sql.TxOptions.ReadOnly` is false — so `Transaction` takes the WAL write lock at
`BEGIN` while `ReadOnlyTransaction` keeps a plain deferred `BEGIN` and still runs
concurrently with a writer.

That split is what makes `busy_timeout` reachable at all. A deferred transaction
begins as a reader and *promotes* at its first write, and SQLite refuses to invoke
the busy handler on a promotion — two connections both waiting to promote would
deadlock — so it returns `SQLITE_BUSY` on the spot. Collector/notifier/web
contention therefore lost writes in milliseconds while a 5 s retry window sat
unused (12 rate values and 5 `execution_history` rows in one production log).
Taking the lock at `BEGIN` is not a promotion, so the wait is real.

Consequences for new code:

- **`Transaction` is for paths that write.** Reads go through `ReadOnlyTransaction` or
  they serialise against each other for nothing. `SQLiteClient.Rollback` — and `Ping`, and
  through it the `/health/check` inspector — is read-only for that reason: a readiness
  probe queued behind a collector tick would report a busy database as a dead one.
- **Two write transactions cannot be open at once** in one process; the second `BEGIN`
  waits for the first. Open, write and commit inside one function, as every repository
  method does.

Foreign keys point from `rate_values`, `rate_user_subscriptions`, and
`rate_user_events` to `rate_sources(name)` with `ON DELETE CASCADE` —
deleting a source destroys all dependent rows. See the warning on
`RemoveRateSource` before wiring it to any endpoint.

Two things that look free to change and are not. **`weather_observations.provider` only
ever holds `'open-meteo'` but partitions two composite indexes** — dropping the vestigial
column degrades them. And **runtime state never goes on `rate_sources`**: `RetainRateSource`
rewrites those rows wholesale (`cmd/doctor rulegen` does exactly that), so a column added
there is destroyed by an unrelated config write — which is why the source-health latch lives
in its own `rate_source_health` table.

**`rate_values` and `execution_history` are tiered.** Each has an `*_archive` twin in the
same file: reads must span both via `UNION ALL`, writes touch hot only. Getting this wrong
returns partial history without erroring. Schema lives at `./migrations/*.sql` and applied
filenames are **immutable**. Both, plus roll-over, retention, VACUUM and how to read a
production snapshot: **skill `beacon-storage`**. `cmd/migrator` is the only thing that
mutates schema; service binaries call `sqlitedb.RequireMigratedSchema` and refuse to start
against an unmigrated database.

### Environment Variables

- `BEACON_SQLITEDB_DSN` — SQLite connection string, parsed via `dsninjector.Unmarshal`. Format: `sqlite://<path-to-db-file>`
- `BEACON_TELEGRAMBOT_DSN` — Telegram bot credentials parsed via `dsninjector.Unmarshal`. Format: `<adminChatID>:<botToken>@<host>` where `Addr()` returns the token and `Login()` returns the admin chat ID.
- `BEACON_PROXY_URL` — optional outbound proxy URL. Format: `<scheme>://<host>:<port>` (e.g. `http://127.0.0.1:7788`). Resolved through `proxyutil.ResolveURL`. `cmd/doctor` proxies through it unconditionally; `cmd/collector` routes nothing through it on its own — see the egress rule above and the `beacon-collection` skill. Telegram Bot API traffic bypasses any proxy unconditionally, enforced by a hardcoded `Proxy: nil` transport in `internal/infrastructure/telegrambot/tbotclient.go`. Do not configure `HTTPS_PROXY`, `HTTP_PROXY`, or `NO_PROXY` — no component in this project consults them.
- `BEACON_CHROMIUM_PATH` — optional absolute path to the Chromium/Chrome binary for `fetcher_kind='chromedp'` sources. Read by `cmd/collector` and `cmd/doctor`. When unset, chromedp searches PATH (`chromium`, `chromium-browser`, `google-chrome`, `chrome`).
- `BEACON_AI_PRIMARY_DSN` (required) and `BEACON_AI_FALLBACK_DSN` (optional) — AI provider DSNs read only by `cmd/doctor rulegen`. See `cmd/doctor/README.md` for the DSN format and provider details.

> The public HTTPS origin of the `cmd/web` server is **not** an env var — see the `--api-dsn` CLI flag on the `cmd/web` binary, baked into the systemd unit's `ExecStart` line.

> Never read or edit `.env` files.

### Deployment

Standard release layout: immutable `/opt/beacon/artifacts/<VERSION_ID>/` build sets and a `bin/release` channel symlink the units run through. **Security boundary**: the CI deploy user may write only under `artifacts/` and `bin/`; `.env`, the DB, and the base dir are root-owned and out of reach. The `release.yml` job (on an `r_*` tag) uploads a new `artifacts/<VERSION_ID>/`, flips the symlink, runs migrations via the **`beacon-migrate` one-shot unit (root, so the deploy user never writes the DB)**, restarts `beacon`, and health-gates on `/health/check` with one-symlink rollback. Schema reconciliation is deploy-time, not startup-time — the service unit has no `ExecStartPre` migrator. `make init` provisions the layout, both units, the narrow `/etc/sudoers.d/beacon-deploy`, and the nginx vhost. See `deploy/README.md`.

There is **no staging**: an `r_*` tag, prerelease or not, flips the production symlink and
restarts the service. Tags are cut from `alpha`, not `main` — see the working agreement. Do
not tag casually. Delete the superseded alpha tag, local and remote, once the new one is
live. Remote hosts are read-freely, mutate-never without explicit per-action approval.

## Error Handling

`internal.PublicError` (in `internal/errors.go`, alongside `TraceError`, `StackTraceError`, `HttpCodeError`, and the `ErrNotFound` sentinel) carries messages **safe to show to end users**. Wrap at the point the error is created (service layer) with `internal.NewPublicError("...")` when the failure meaningfully tells the user something; return a plain `error` for everything else (DB down, unexpected nil, ...). The controller catches every sub-handler error and sends `PublicError.Details()` for a public error, else a generic fallback constant.

Every controller test on an error branch must assert: (1) a response was actually sent (user not left in silence), (2) its text equals `PublicError.Details()` for a public error, (3) its text equals the fallback constant for a plain error.

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

All non-trivial work follows the plan-first pipeline:

1. **Plan** — the `architect` agent writes `plans/NNN-slug.md` (create via the
   `pipeline:new-plan` skill). No source edits before a plan exists.
2. **Implement** — the `engineer` agent executes the plan's tasks with tests.
3. **Review** — three `reviewer` agents launched in parallel in ONE message, each
   prompt naming its lens (A: correctness & tests, B: security & operations,
   C: performance & architecture) and the changed files. Full three-lens fan-out is
   mandatory on the first review; the post-fix re-review is ONE solo reviewer scoped
   to the changed lines.
4. **Gate** — `make test` must be green before review; a red tree goes to the
   `testdoctor` agent first, at any stage.
5. **Complete** — the orchestrator merges the three reports, deduplicates, resolves
   conflicting verdicts (naming what was rejected and why; the user has final say).
   P0/P1 findings loop back to the engineer. Only when every P0/P1 is fixed or
   explicitly accepted: move the plan via the `pipeline:complete-plan` skill.

Plans live in `plans/` (active), `plans/completed/` (shipped, `YYMMDD.NNNN.slug.md`),
`plans/history/` (abandoned/superseded). One plan per concern.

Branch as `type/<issue>-<slug>` **off `alpha`** and open the PR against `alpha` — work
integrates there and release tags are cut from it. `main` only ever moves to the latest
**non-prerelease** tag, so it trails `alpha` by a whole alpha series. Never commit to
either directly.

Two silent traps. **A merge into `alpha` does not close its issue** — GitHub honours
`Closes #N` only on the default branch, `main`; close it by hand, naming the squash commit
and its tag. And **`gh pr create` defaults to `main`**, the stale release pointer, so pass
`--base alpha`.
