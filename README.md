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
  wasm/          # GOOS=js GOARCH=wasm dashboard renderer
internal/
  application/   # collection / notification agents, REST + Telegram services
  domain/        # RateSource, RateValue, RateUserSubscription, RateUserEvent, …
  gateway/       # http.ServeMux wiring; httpV1/{router,handlers,routes,dto}
  repository/    # one repo per table; each owns its own DDL via Migration()
  infrastructure/# sqlitedb client + migrator, telegrambot client, scheduler
  tools/         # rateextractor, rateforecaster, threadsafe (buffer + cache)
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

Static assets are served from `/` by the embedded FS (or `--static-dir`).

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

## Deployment

`deploy/monitor.service` is a reference systemd unit; `deploy/README.md` walks
through creating a dedicated user, installing the binary, and enabling the unit.
`configs/srv.{prime,stage}_monitor.service` are the production units used by
the `make deploy_environment` target (which `scp`s nginx + systemd config to the
target host).

## License

See [`LICENSE`](LICENSE).
