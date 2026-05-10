# Monitor

A lightweight, portable tool for collecting, storing, monitoring, and forecasting rates (currency exchange rates, and more) from multiple sources.

Sends Telegram alerts when rates change significantly and provides short-term forecasts using moving average and linear regression.

## Features

- Configurable rate sources via JSON (regex-based HTML extraction)
- SQLite storage — pure Go, no CGO, no external database required
- Change detection with absolute and percentage thresholds
- Telegram notifications with forecast included
- Moving average + linear regression forecasting
- Periodic scheduler with configurable interval
- VPN / proxy support (Mullvad-compatible)
- Structured logging (`log/slog`)
- Single static binary, cross-platform

## Installation

```bash
git clone https://github.com/seilbekskindirov/monitor.git
cd monitor
CGO_ENABLED=0 go build -o monitor ./cmd/monitor
```

Or via `make`:

```bash
make build
```

## Usage

```bash
./monitor \
  --db rates.db \
  --config configs/sources.example.json \
  --interval 10m \
  --telegram-token <TOKEN> \
  --telegram-chat <CHAT_ID>
```

### All flags

| Flag | Default | Description |
|---|---|---|
| `--db` | `rates.db` | SQLite database file path |
| `--config` | — | JSON sources config file |
| `--interval` | `10m` | Polling interval (e.g. `5m`, `1h`) |
| `--telegram-token` | — | Telegram bot token |
| `--telegram-chat` | — | Telegram chat ID |
| `--abs-threshold` | `0.5` | Notify if change > N KZT |
| `--pct-threshold` | `0.2` | Notify if change > N% |
| `--proxy` | — | HTTP/SOCKS5 proxy URL |
| `--log-format` | `text` | `text` or `json` |

## Sources config

Sources are defined in a JSON file. Each source specifies a URL and a list of regex extraction rules:

```json
{
  "rates_vs_kzt": [
    {
      "name": "Halyk Bank",
      "url": "https://halykbank.kz/exchange-rates",
      "rules": [
        {
          "method": "regex",
          "pattern": "USD[^0-9]*([0-9]+[.,][0-9]+)",
          "base_currency": "USD",
          "quote_currency": "KZT"
        }
      ],
      "interval": "10m"
    }
  ]
}
```

See `configs/sources.example.json` for a working example.

## Telegram notification format

```
📊 USD/KZT rate changed

Source: Halyk Bank
Old value: 502.10
New value: 503.70
Change: +1.60 (+0.32%)
Time: 15:45

📈 Forecast:
  Next week: 504.50
  Next month: 506.20
```

## VPN / Proxy

Pass a proxy URL via `--proxy` to route all HTTP requests through it:

```bash
# Mullvad SOCKS5 (when connected)
./fxmon --proxy socks5://10.64.0.1:1080 ...

# Or set the standard env var
HTTPS_PROXY=socks5://10.64.0.1:1080 ./fxmon ...
```

## Project structure

```
cmd/monitor/          — entry point
internal/
  config/           — CLI flag and JSON config loading
  model/            — domain types (Rate, ForecastResult)
  storage/          — SQLite persistence
  extractor/        — HTTP fetch + regex rate extraction
  detector/         — change significance detection
  notifier/         — Telegram notifications
  forecaster/       — moving average + linear regression
  scheduler/        — periodic job runner
configs/            — example source configs
migrations/         — SQL schema migrations
```

## Development

```bash
make test    # run all tests
make lint    # run golangci-lint
make build   # build binary
make run ARGS="--config configs/sources.example.json --interval 1m"
```

### Frontend testing

The WASM frontend is tested in two layers:

- `make test` — runs all pure-Go tests, including the `application/` controllers,
  `ui/` rendering, and `apiclient/` end-to-end via `httptest`. No Node required.
- `make test-wasm` — runs tests under the WASM runtime for the small `dom`
  glue layer. Requires Node.js 18+. Optional; useful for local verification.

DOM event-binding lifecycle (`cmd/wasm/main.go` mount/unmount, `dom.On` releases)
is exercised only by manual browser smoke — see [ADR-002](docs/decisions/260509.0001.adopt-fetcher-seam-no-headless-wasm-ci.md)
for the CI-strategy rationale.

## CI/CD

No automated CI pipeline exists yet. When one is added, `make test` is the
intended build-gate command (see [ADR-002](docs/decisions/260509.0001.adopt-fetcher-seam-no-headless-wasm-ci.md)
for the WASM-side strategy).

Planned (not yet wired):

- **Pull requests** — run `make test` and `make build` on every PR: vet, race-detector tests, build, golangci-lint.
- **Release** — on `v*.*.*` tags: build static binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` and publish them as GitHub Release assets.

## License

MIT
