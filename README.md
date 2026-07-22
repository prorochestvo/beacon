# beacon

A small, self-hosted monitor for **FX rates and weather**, written in pure Go. It
scrapes exchange-rate sources and Open-Meteo, stores everything in SQLite, evaluates
per-user conditions (price thresholds and schedules), and pushes alerts through a
Telegram bot with an embedded WASM Mini App and dashboard.

## Features

- **FX rate tracking** — scrapes configurable web sources (plain HTTP, or a headless
  Chromium browser for JS-rendered pages) and extracts the numeric rate per source, each
  on its own collection interval.
- **Weather tracking** — per-user cities via Open-Meteo, with always-on thaw alerts and
  a daily morning summary.
- **Telegram notifications** — per-user conditions (`delta`, `interval`, `daily`, `cron`)
  delivered over a Telegram bot; a WASM Mini App handles subscriptions, charts, and rate
  history.
- **Pure-Go SQLite** — `modernc.org/sqlite`, so it builds with `CGO_ENABLED=0` and
  cross-compiles cleanly; the dashboard, WASM bundle, and migrations are all embedded.

## Binaries

| Binary | Role |
|--------|------|
| `collector` | Scrapes rate sources and fetches weather on each run. |
| `notifier`  | Evaluates subscription conditions and dispatches Telegram alerts. |
| `web`       | REST API, embedded dashboard + Mini App, Telegram callback router. |
| `migrator`  | Applies SQL schema migrations — the only binary that mutates schema. |
| `doctor`    | Operator tooling: LLM rule generation and source auditing. |

## Requirements

- Go **1.26+** and `make`
- A Telegram bot token + admin chat id (for `notifier` and `web`)
- **Chromium/Chrome on the host** — only for rate sources with `fetcher_kind='chromedp'`
  (JS-rendered pages), found on `PATH` or via `BEACON_CHROMIUM_PATH`. Not needed for the
  build.

No CGO, no system libraries.

## Quick start

```bash
make build    # builds all binaries + cmd/web/static/app.wasm
make test     # go fmt + go vet + go test -race ./...
make run      # sources .env, then starts collector, notifier, web
```

Runtime config comes from environment variables (see `.env.example`). The DSNs:

| Variable | Required by | Format |
|----------|-------------|--------|
| `BEACON_SQLITEDB_DSN`    | collector, notifier, web | `sqlite://_:_@_:_/<filename>` |
| `BEACON_TELEGRAMBOT_DSN` | notifier, web            | `tbot://<admin_chat_id>:@<bot_token>/` |
| `BEACON_PROXY_URL`       | collector (optional)     | `socks5://user:pass@host:port` or `http://...` |

`collector` and `notifier` are one-shot — schedule them with cron or a systemd timer at
whatever cadence your shortest source needs; `web` is the long-running server. Pass
`--help` to any binary for its flags. `make lint`, `make format`, and `make clean` cover
the rest — see the Makefile.

## Further docs

Implementation and operations detail lives next to the code, not here:

- **Deployment, backups, nginx/Cloudflare edge** — [`deploy/README.md`](deploy/README.md)
- **Operator tooling (`rulegen`, `audit`)** — [`cmd/doctor/README.md`](cmd/doctor/README.md)
- **Rate source config** — [`configs/sources.example.json`](configs/sources.example.json)
- **HTTP API surface** — route constants in `internal/gateway/httpV1/routes/routes.go`
- **Architecture & conventions** — [`CLAUDE.md`](CLAUDE.md)

## License

See [`LICENSE`](LICENSE).
