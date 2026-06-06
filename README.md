# fx_rate_monitor

A small self-hosted FX/exchange-rate monitor written in pure Go. It scrapes rates from
configurable web sources, stores them in SQLite, evaluates per-user notification
conditions (delta / interval / daily / cron), and dispatches alerts through a Telegram
bot. A minimal HTTP dashboard (HTML + WASM) ships embedded in the `web` binary.

## Features

- **Multi-source extraction** — regex, JSONPath, `parse_float`, and a `store_as_rate`
  pass-through method for sources that already publish a numeric value.
- **Per-source schedules** — each source declares its own interval (`10m`, `1h`, …);
  the collector applies a grace window of `interval/4` (clamped to `[30s, 1h]`) so
  near-simultaneous runs don't race.
- **Subscription conditions** — `delta` (absolute price change), `interval`,
  `daily` (HH:MM:SS), and `cron` (5-field). Evaluated by `RateCheckAgent`.
- **Telegram bot UI** — fully stateless inline-keyboard flows for adding/listing/
  deleting subscriptions; all flow state travels in `callback_data`.

### Bot commands

| Command | Available to | Description |
|---------|-------------|-------------|
| `/start` | All users | Opens the subscription management menu |
| `/subscriptions` | All users | Same as `/start` |
| `/regen <source> [--force-fallback] [--max-fallback=N]` | Admin only | Trigger rule (re-)generation for a named source; reply is edited in place when finished |
- **REST + dashboard** — `cmd/web` exposes a `/api/...` v1 surface and serves an
  embedded vanilla-JS dashboard (with a WASM alternative built from `cmd/wasm`).
- **Pure Go SQLite** — `modernc.org/sqlite`, so the project builds with
  `CGO_ENABLED=0` and cross-compiles cleanly.

## Repository layout

```
cmd/
  collector/     # scrapes active rate sources on each invocation
  notifier/      # check-agent + dispatch-agent, drains the notification pool
  web/           # HTTP dashboard + REST API + Telegram callback router
  wasm/          # GOOS=js GOARCH=wasm frontend: renderer + apiclient/application/ui/dom
internal/
  application/   # collection / notification agents, REST + Telegram services
  domain/        # RateSource, RateValue, RateUserSubscription, RateUserEvent, …
  dto/           # JSON wire DTOs shared by the server (gateway) and the WASM client
  gateway/       # http.ServeMux wiring; httpV1/{handlers,routes}
  repository/    # one repo per table; each owns its own DDL via Migration()
  infrastructure/# sqlitedb client + migrator, telegrambot client
  tools/         # rateextractor, labelfmt, tgwebapp, threadsafe
  errors.go      # PublicError / TraceError / StackTraceError / HttpCodeError
  logger.go      # cyclic file logger built on loginjector
configs/         # nginx, systemd, sources example
deploy/          # reference systemd unit
plans/           # planning workflow (see CLAUDE.md)
```

## Requirements

- Go **1.26+**
- `make`
- A Telegram bot token + admin chat id (for `notifier` and `web`)

**Deploy-host runtime dependency (not required for the build):** Chromium or Google
Chrome must be installed on the host for any rate source with `fetcher_kind='chromedp'`
(JS-rendered pages). The `cmd/doctor` binary looks for `chromium`, `chromium-browser`,
`google-chrome`, or `chrome` on PATH, or uses `CHROMIUM_PATH` if set. Install once:
```bash
sudo apt-get install -y chromium-browser   # Debian/Ubuntu (Oracle Cloud ARM Free Tier)
brew install --cask chromium               # macOS dev
```

No CGO, no system libraries. The build embeds the dashboard, the WASM bundle, and
the migration scaffold.

## Configuration

All runtime configuration is passed via environment variables, normally loaded
from a project-local `.env` (Make sources it for you on `make run`). See
`.env.example` for the canonical shape:

| Variable          | Required by                | Format |
|-------------------|---------------------------|--------|
| `SQLITEDB_DSN`    | collector, notifier, web  | `sqlite://_:_@_:_/<filename>` |
| `TELEGRAMBOT_DSN` | notifier, web             | `tbot://<admin_chat_id>:@<bot_token>/` |
| `PROXY_URL`       | collector (optional)      | `socks5://user:pass@host:port` or `http://...` |

CLI flags accepted by the binaries:

| Flag | Binaries | Default | Notes |
|------|----------|---------|-------|
| `--logs-dir`   | all          | `$TMPDIR/logs` | cyclic file logger output |
| `--verbosity`  | all          | `warning`      | `debug \| info \| warning \| error \| severe \| critical` |
| `--port`       | web          | `8080`         | must be in `(1000, 32000)` |
| `--timeout`    | web          | `30s`          | Go duration; must be `> 10s` |
| `--static-dir` | web          | (embedded FS)  | overrides the embedded dashboard for local development |

## Build & run

```bash
make build    # builds collector, notifier, web, plus cmd/web/static/app.wasm
make test     # go fmt + go vet + go test -race ./...
make lint     # go fmt + go vet
make format   # go fmt ./...
make clean    # removes ./build/, generated wasm, and runs go mod tidy
```

The `run` target builds first, then starts the binaries sequentially with `.env`
sourced:

```bash
make run
```

For day-to-day development you typically run the three binaries separately, e.g.:

```bash
set -a; . .env; set +a
go run ./cmd/collector --logs-dir ./build/logs
go run ./cmd/notifier  --logs-dir ./build/logs
go run ./cmd/web       --logs-dir ./build/logs --static-dir ./cmd/web/static
```

`collector` and `notifier` are designed as one-shot processes — schedule them with
cron / a systemd timer at whatever cadence your shortest source needs. `web` is a
long-running server that also drives the Telegram subscription bot.

### Targeted tests

```bash
CGO_ENABLED=0 go test -race -run TestRateAgent ./internal/application/collection/
CGO_ENABLED=0 go test -race -run 'TestRateUserSubscription_IsDue/cron_due' ./internal/domain/
CGO_ENABLED=0 go test -race -coverprofile=cover.out ./...
go tool cover -html=cover.out
```

## HTTP API

Route constants live in `internal/gateway/httpV1/routes/routes.go`. The current
v1 surface:

- `GET   /api/sources` — list all sources
- `PATCH /api/sources/{name}/active` — toggle a source on/off
- `GET   /api/sources/{name}/rates` — recent rate values
- `GET   /api/sources/{name}/rates/chart` — aggregated chart data
- `GET   /api/sources/{name}/history` — execution history
- `GET   /api/sources/{name}/events/failed` — failed events for a source
- `GET   /api/sources/{name}/events/daily` — daily aggregated event counts
- `GET   /api/sources/{name}/subscriptions` — grouped subscription stats
- `GET   /api/sources/{name}/subscriptions/list` — paginated subscription detail
- `GET   /api/events/pending` — pending notification events
- `GET   /api/notifications` — last N notifications
- `GET   /api/notifications/failed` — failed notifications
- `GET   /api/errors/execution` — recent failed execution history
- `GET   /api/stats` — global counters
- `GET   /api/public/rates/chart` — paginated system-wide sparkline list; no authentication. Query params: `page` (default 1), `limit` (default 20, max 100)

Static assets are served from `/` by the embedded FS (or `--static-dir`). The
site root (`/`) is the unified entry point: an inline dispatcher in
`index.html` inspects `window.Telegram.WebApp.initData` and routes Telegram
Mini App users to the per-user subscriptions view; visitors without initData
get the public sparkline list. The operator dashboard lives at `/admin/`.

## Sources

Sources are stored in the `rate_sources` table. A source declares its `URL`,
`Interval`, base/quote currency pair, kind (`BID` or `ASK`), and a list of
extraction `Rules`. An example shape (see `configs/sources.example.json`):

```json
{
  "rates_vs_kzt": [
    {
      "name": "Halyk Bank",
      "url":  "https://halykbank.kz/exchange-rates",
      "rules": [
        {
          "method":  "regex",
          "pattern": "USD[^0-9]*([0-9]+[.,][0-9]+)",
          "base_currency":  "USD",
          "quote_currency": "KZT"
        }
      ],
      "interval": "10m"
    }
  ]
}
```

Supported extraction methods: `regex`, `json` (JSONPath), `parse_float`, and
`store_as_rate`.

## Operator tools (`cmd/doctor`)

`cmd/doctor` is the umbrella maintenance binary for operator-side tasks. It
hosts two subcommands:

### `doctor rulegen` — LLM rule generation

Generates or regenerates an extraction rule for a named source using an LLM.
It fetches the source URL, asks the LLM for a rule, validates the rule against
the live body, and persists the result to the database. Run it once after
inserting a new source row (via a seed migration).

```bash
# Build first
make build

# Generate a rule for the "halyk_usd" source
SQLITEDB_DSN=sqlite://_:_@_:_/./build/monitor.db \
AI_PRIMARY_DSN=groq://_:<base64url(KEY)>@api.groq.com/openai/v1?model=llama-3.1-8b-instant \
./build/doctor rulegen halyk_usd

# Skip primary and go straight to fallback (useful when source is hard)
./build/doctor rulegen halyk_usd --force-fallback

# Regenerate rules for every active source (cron mode; always exits 0)
./build/doctor rulegen --all

# See all flags
make doctor-help
```

### `doctor audit` — seed source auditing

Probes seeded rate sources against their live URLs to verify that the
extraction rules still return plausible values. Run from the repository root
(seed-glob is relative to CWD).

```bash
# Audit every seeded source
make audit ARGS="--all"

# Audit one source by exact name
make audit ARGS="--source halyk_usd"

# Audit sources matching a regex
make audit ARGS="--only '^halyk_'"

# Verbose per-source table output
make audit ARGS="--all -v"
```

For full usage, exit codes, cost notes, Chromium setup, and troubleshooting
guidance see `cmd/doctor/README.md`.

## Deployment

CI workflows in `.github/workflows/{stage,prime}.yml` drive every deploy:
checksum-validate binaries, pause cron wrappers, run `cmd/migrator`, swap
the webapp binary, restart the unit, resume cron. The live systemd units are
`configs/srv.{stage,prime}_monitor.service`. See `deploy/README.md` for the
operator-facing summary, including the "Exit code & alerting" contract.

### Post-deploy: BotFather Menu Button URL

After any deploy that changes the public hostname, update the Mini App URL in
BotFather to match the unified entry point:

1. Open BotFather → `/mybots` → your bot → `Bot Settings` → `Menu Button`.
2. Set the URL to `https://<host>/` (trailing slash required).

The exact value the bot emits in its reply keyboard is logged at `cmd/web`
startup as `settings: webAppURL=...`. Until BotFather is updated, users
whose Mini App button was cached pointing at the old
`/tbot-miniapp/subscriptions.html` path will receive a 404 — that file no
longer exists in the embedded static FS.

## License

See [`LICENSE`](LICENSE).
