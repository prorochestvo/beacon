# CLAUDE.md

A map, not a manual: what applies to every task, plus the rules whose violation is silent.
Depth lives in the project skills below, loaded on demand.

## Project skills

Invoke by name (Skill tool) when the work touches their area. Each is the full canon for
its subject; this file keeps only the tripwire.

| Skill | Load before touching |
|---|---|
| `beacon-collection` | `cmd/collector`, `rateextractor`, `application/collection`, `infrastructure/weather`, `SourceHealthAgent`, `rate_sources` rows, a source `kind` or its `options`, `BEACON_PROXY_URL` / `options.use_proxy`, `BEACON_CHROMIUM_PATH`, `cmd/doctor`, weather alert kinds, `ForecastRange`, `forecast_outlook` |
| `beacon-storage` | any `internal/repository` query, any `./migrations/*.sql`, `MaintenanceAgent`, `sqlitedb.Migrator`, `Transaction` / `ReadOnlyTransaction`, the DSN PRAGMAs, `weather_forecast_days`, reading the production database |
| `beacon-http-api` | `internal/gateway`, `cmd/web`, `cmd/wasm`, `cmd/web/static`, `configs/nginx.*`, `initData` auth, `internal.PublicError`, any `/api/v1/me` or `/api/v1/public` route |
| `beacon-forecasting` | `internal/tools/rateforecaster`, `internal/tools/rateanomaly` (load with `knowledge:forecasting`) |
| `beacon-data-privacy` | any new column on a user-scoped table, anything captured from a Telegram update, any new log field |

Generic Go conventions (style, declaration order, test structure, godoc, error discipline,
build hygiene, organisation) come from the `stack-go` plugin skills and are not restated
anywhere in this repo.

### Where new canon goes

This file is loaded whole into every session and stays there, so its size is a tax on every
conversation whatever the task touches. Keep it under **12k bytes** — this project's own
budget, tighter than the 20k default; 40k is where Claude Code warns. Route new
documentation by *when the reader needs it*, not by how important the subject feels:

- **CLAUDE.md** — what applies to every task (the binary and layer map, startup ordering,
  configuration, the gate), plus rules whose violation is **silent**. A tripwire stays
  here even after its subject has moved out.
- **A project skill** (`.claude/skills/<name>/SKILL.md`) — the depth for one subject area.
  The `description` frontmatter *is* the load trigger: name the packages, paths, symbols and
  env vars that pull it in. One that summarises the prose instead means the skill never loads
  and the knowledge is lost.
- **Neither** — incident narratives, enumerations derivable from the code, and the reasoning
  behind a decision already taken: commit bodies, `plans/` and `docs/decisions/`.

**Every subject moved into a skill leaves one line behind** — the skill carries the why,
CLAUDE.md the sentence that stops someone getting it wrong before they think to load
anything. That redundancy is what makes the split safe, so reserve it for failures that stay
silent, like a read skipping a storage tier and returning partial history without erroring.
And **measure, never estimate**: identifiers do not compress, so count with `wc -c` before
and after, then prove the move lost nothing by extracting every backticked span and figure
from the old text and accounting for each by name. Full procedure: `standards-layout` R21.

## Architecture

A self-hosted FX-rate monitor. Five binaries over one SQLite file.

| Binary | Role |
|---|---|
| `collector` | Scrapes each rate source per invocation (plain HTTP, or a chromedp-driven headless browser for JS-rendered pages), extracts the numeric rate via per-source rules, stores it; also collects weather |
| `notifier` | Check-agent evaluates subscription conditions (delta / interval / daily / cron) against latest rates and enqueues them; dispatch-agent drains the pool and sends over Telegram |
| `web` | REST API plus embedded dashboard (HTML and a WASM build); routes Telegram callbacks |
| `migrator` | Applies schema migrations — the only thing that mutates schema |
| `doctor` | Operator tooling: LLM rule generation and source auditing |

| Layer | Location | Role |
|-------|----------|------|
| Entry point | `cmd/<binary>/` | Composition root per binary, plus `wasm` |
| Application | `internal/application/` | What the answer is, free of transport |
| Domain | `internal/domain/` | Value objects / models, no logic |
| DTO | `internal/dto/` | JSON wire contract shared by the server (gateway) and the WASM client |
| Gateway | `internal/gateway/` | Receiving and rendering: HTTP routers, middleware, Telegram update loop |
| Repository | `internal/repository/` | Persistence queries |
| Infrastructure | `internal/infrastructure/` | External clients (SQLite, Telegram, AI providers) |
| Tools | `internal/tools/` | Cross-cutting utilities |
| Frontend | `cmd/wasm/` | GOOS=js GOARCH=wasm dashboard |

**Startup ordering.** Anything that logs or can `log.Fatalf` on bad config belongs in `main`
*after* the logger exists, never in a package initialiser: the cron wrappers discard stderr,
so a line emitted earlier is attributable to nothing. Operators grep the marker sequence
`logger -> settings -> dependencies -> repositories -> runners`.

## Tripwires

Each fails without an error; the reasoning is in the named skill.

**Collection egress is direct by default.** Two levels must agree: `BEACON_PROXY_URL` says
a proxy exists, `rate_sources.options.use_proxy` says the source wants it. No source is
opted in today; the default is a measured decision (issue #16) — do not reverse it
casually. Chromedp and weather stay direct regardless.
**Never widen `OpenMeteo.Forecast`'s `daily` block**: index `[0]` *is* today for the morning
summary and all four daily-metric latches, which is why the multi-week fetch is a separate
call (`ForecastRange`, its own table, its own daily cadence). **Skill: `beacon-collection`.**

**`rate_values` and `execution_history` are tiered.** Each has an `*_archive` twin in the
same file: reads must span both via `UNION ALL`, writes touch hot only — getting this wrong
returns partial history without erroring. Writes open `Transaction` (`BEGIN IMMEDIATE`),
reads `ReadOnlyTransaction`; the write path for a read serialises it for nothing. Applied
migration filenames are **immutable**, and service binaries call
`sqlitedb.RequireMigratedSchema`, refusing to start against an unmigrated database.
**Long-range forecast rows belong in `weather_forecast_days`, never `weather_observations`**:
the collector sweeps that table by `captured_at` at 48 h every tick, so a row describing a
day two weeks out is gone a day and a half after it is written, with no error anywhere. Two
columns look free to change and are not — `weather_observations.provider`, and anything
runtime-valued added to `rate_sources`. **Skill: `beacon-storage`.**

**`/api/v1/me/*` is the only authenticated surface**, and the check runs **once**, in
`middleware.TelegramInitData` mounted over `routes.MePrefix` — **a new authenticated route
belongs on that inner mux; putting it on the outer one is a bypass, and nothing will say
so.** Signed `initData` is accepted **only** in the `X-Telegram-Init-Data` header, never a
query string, which would leak it into access logs and `Referer`. A `/api/v1/me/*` resource
owned by another user returns **404, never 403** — existence is not disclosed, anywhere.
`GET /ping` (alias `/healthz`) is liveness and touches no dependency; `GET /health/check` is
readiness and probes every dependency for real. Both unauthenticated.
**Skill: `beacon-http-api`.**

**There is no staging.** An `r_*` tag, prerelease or not, flips the production symlink and
restarts the service. Tags are cut from `alpha`, not `main` — see the working agreement. Do
not tag casually, and delete the superseded alpha tag, local and remote, once the new one is
live. An `s_*` tag runs the gate only, contacting no host. Release layout, CI security
boundary, `make init` / `make deploy-configs`, one-symlink rollback: `deploy/README.md`.

## Configuration

Env var names, formats, which binary needs which, and the Chromium PATH fallback are
declared once in `internal/env.go` (all parsed via `dsninjector.Unmarshal`) — read them
there, not from a copy. `.env.example` has worked DSN examples.

- **The standard proxy variables do nothing here.** No component consults `HTTPS_PROXY`,
  `HTTP_PROXY` or `NO_PROXY`; `BEACON_PROXY_URL`, resolved through `proxyutil.ResolveURL`, is
  the only knob, and Telegram Bot API traffic bypasses it unconditionally
  (**skill: `beacon-collection`**).
- **The public HTTPS origin is not an env var.** It is the `--api-dsn` flag on `cmd/web`
  (format `https://<host>/`, parsed by `dsninjector.Parse`), hardcoded in the systemd unit's
  `ExecStart` line — never in `.env`.
- Required config must be present at startup; the binary calls `log.Fatalf` on any missing
  value. Never log a settings parser error — it carries the credential (`make lint` fails
  on it).
- **Never read or edit `.env` files.**

## Data & Privacy

Store the **minimum personal data required** to run as a Telegram bot — not zero PII, but
nothing beyond what delivering notifications requires.

Pre-approved for user-scoped tables, no discussion needed: Telegram `chat_id`, IANA
timezone, BCP-47 locale, and coordinates of a city the user picked from a geocoding search.

**Off limits without an explicit policy change**: `@username` or any name, phone, email,
photo, biometrics, device- or IP-derived location, IP address, device fingerprint,
user-agent. Same list for log output — `chat=<chat_id>` is fine, nothing else is.

Anything not on either list: **do not persist it yet, ask first** — identity-adjacent
columns are far easier to prevent than to revert from a production database. Full policy,
per-field guardrails, and borderline classification: **skill `beacon-data-privacy`**.

## Constraints

- **Forbidden imports**: CGO-dependent SQLite drivers (e.g. `github.com/mattn/go-sqlite3`)
  must never appear in `go.mod` — persistence is pure-Go via `modernc.org/sqlite`.
  Enforced via `make lint`.
- **Scratch files** go to `./tmp/` (e.g. `./tmp/probe_*`), never the repo root; bare
  `go build ./cmd/web` drops a `./web` binary in the root, which is not gitignored.
- **Remote hosts** are read-freely, mutate-never without explicit per-action approval.

## Working agreement

Plan-first pipeline; the canonical procedure is the `pipeline:working-agreement` skill —
load it before starting non-trivial work. Project delta:

- **Gate:** `make test` (fmt, `go vet`, the full `go test -race` suite, then WASM tests) plus
  `make lint-new` — the mergeable gate, linting only what changed since `origin/alpha`,
  while `make lint` scans the whole tree as a worklist. Both run **two** steps,
  `golangci-lint run` *and* `scripts/lint-checks.sh` — a green `golangci-lint` is **not** a
  green `make lint`. `make test` opens with `go clean -cache`, so a full run rebuilds
  `modernc.org/sqlite` from scratch: minutes, not seconds. `-race` needs cgo, so a targeted
  rerun is `CGO_ENABLED=1 go test -race -run TestX ./<pkg>/` (macOS tolerates `0`, Linux does
  not); benchmarks (`-bench=.`, no `-race`) don't. **On the pi5 `make test` dies compiling
  `modernc.org/sqlite` under `-race`** — rerun as `go test -race -p 1`, the only route to a
  green gate on the one machine that runs it.
- **Lenses:** standard set — see `pipeline:working-agreement`, which includes lens O.
- **Branching:** branch `type/<issue>-<slug>` **off `alpha`** and open the PR against
  `alpha` — work integrates there and release tags are cut from it. `main` only ever moves to
  the latest **non-prerelease** tag, so it trails `alpha` by a whole alpha series. Never
  commit to either directly. Two silent traps: **a merge into `alpha` does not close its
  issue** — GitHub honours `Closes #N` only on the default branch, `main` — so close it by
  hand, naming the squash commit and its tag; and **`gh pr create` defaults to `main`**, the
  stale release pointer, so pass `--base alpha`.
