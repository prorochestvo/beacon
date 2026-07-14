# beacon

A small self-hosted FX/exchange-rate monitor written in pure Go. It scrapes rates from
configurable web sources, stores them in SQLite, evaluates per-user notification
conditions (delta / interval / daily / cron), and dispatches alerts through a Telegram
bot. A minimal HTTP dashboard (HTML + WASM) ships embedded in the `web` binary.

## Features

- **Multi-source extraction** — regex, JSONPath, `parse_float`, and a `store_as_rate`
  pass-through for sources that already publish a numeric value.
- **Per-source schedules** — each source declares its own collection interval (`10m`, `1h`, …).
- **Subscription conditions** — `delta` (absolute price change), `interval`,
  `daily` (HH:MM:SS), and `cron` (5-field).
- **Telegram Mini App** — per-user subscription management (create / edit / delete,
  charts, rate history) runs in an embedded WASM Mini App. The chat bot itself presents
  a read-only "Latest updates" digest plus a button that launches the Mini App; it
  responds to `/start` and `/subscriptions`, which open the same menu.
- **REST API + dashboard** — `cmd/web` exposes a versioned `/api/...` surface and serves
  an embedded operator dashboard plus the WASM Mini App.
- **Pure Go SQLite** — `modernc.org/sqlite`, so the project builds with `CGO_ENABLED=0`
  and cross-compiles cleanly.

## Binaries

| Binary | Role |
|--------|------|
| `collector` | Scrapes active rate sources on each invocation and stores values. |
| `notifier`  | Evaluates subscription conditions and dispatches Telegram alerts. |
| `web`       | REST API, embedded dashboard + Mini App, Telegram callback router. |
| `migrator`  | Applies SQL schema migrations — the only binary that mutates schema. |
| `doctor`    | Operator tooling: LLM rule generation and source auditing. |

## Requirements

- Go **1.26+**
- `make`
- A Telegram bot token + admin chat id (for `notifier` and `web`)

**Deploy-host runtime dependency (not required for the build):** Chromium or Google
Chrome must be installed on the host for any rate source with `fetcher_kind='chromedp'`
(JS-rendered pages). The `cmd/doctor` binary looks for `chromium`, `chromium-browser`,
`google-chrome`, or `chrome` on PATH, or uses `BEACON_CHROMIUM_PATH` if set. Install once:
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

| Variable                  | Required by                | Format |
|---------------------------|---------------------------|--------|
| `BEACON_SQLITEDB_DSN`    | collector, notifier, web  | `sqlite://_:_@_:_/<filename>` |
| `BEACON_TELEGRAMBOT_DSN` | notifier, web             | `tbot://<admin_chat_id>:@<bot_token>/` |
| `BEACON_PROXY_URL`       | collector (optional)      | `socks5://user:pass@host:port` or `http://...` |

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

## HTTP API

Route constants are the source of truth — see
`internal/gateway/httpV1/routes/routes.go`. The surface splits into three groups:

- **Operator + public** (no auth) — source listings, rate and execution history,
  notification diagnostics, global stats, the public sparkline chart, `GET /ping` (liveness),
  and `GET /health/check` (readiness). `GET /healthz` is a backward-compatible alias for `/ping`.
- **Mini App** (`/api/me/...`) — the caller's own subscriptions (CRUD), charts, history,
  and profile. Authenticated by the Telegram WebApp `initData` HMAC, which must be passed
  in the `X-Telegram-Init-Data` header only — never the query string, to keep it out of
  access logs and Referer headers.
- **Static** — served from `/` by the embedded FS (or `--static-dir`). The site root is a
  unified entry point: an inline dispatcher in `index.html` inspects
  `window.Telegram.WebApp.initData` and shows the per-user Mini App when present, otherwise
  the public sparkline list. The operator dashboard lives at `/admin/`.

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

## Operator tools (`cmd/doctor`)

`cmd/doctor` is the umbrella maintenance binary. `doctor rulegen` generates or
regenerates a source's LLM extraction rule — run it once after seeding a new
source row, before the collector can scrape it. `doctor audit` probes seeded
sources against their live URLs to confirm the rules still return plausible
values; run it from the repository root, since the seed glob is relative to CWD.

`make doctor-help` lists the rulegen flags; `make audit ARGS="..."` drives the
auditor. For full usage, exit codes, cost notes, Chromium setup, and
troubleshooting see `cmd/doctor/README.md`.

## Deployment

Tests run on every push/PR to `main` (`.github/workflows/ci.main.yml`). An `r_*`
release tag triggers `.github/workflows/release.yml`, which uploads the four
binaries into an immutable `/opt/beacon/artifacts/<VERSION_ID>/`, flips the
`bin/release` channel symlink, migrates (`beacon-migrate` one-shot) and restarts
the webapp, then health-gates on `/health/check` with an automatic one-symlink
rollback. The CI deploy user can write only inside `artifacts/` and `bin/`. See
`deploy/README.md` for the on-server layout, units, sudoers, and hardening.

### Edge (nginx + Cloudflare)

Public traffic terminates at Cloudflare and is proxied to nginx on the host,
which forwards to the web binary on loopback:

| Public domain | Loopback |
|---------------|----------|
| `beacon.seilbekskindirov.dev` | `127.0.0.1:8000` |

`make init` provisions the host in one shot: it sets `/opt/beacon` ownership,
installs the systemd unit and the backup script, ships `configs/nginx.beacon*.conf`,
fetches the Cloudflare origin-pull CA, and enables the vhost. nginx authenticates
the edge via Authenticated Origin Pulls and serves the shared
`*.seilbekskindirov.dev` Cloudflare **Origin** certificate, which is
operator-placed at `/etc/nginx/certificates/cloudflare/seilbekskindirov.dev.{pem,key}`
(never shipped from the repo); `init` skips the nginx reload with a warning until
both files are present.

### Post-deploy: BotFather Menu Button URL

After any deploy that changes the public hostname, update the Mini App URL in
BotFather to match the unified entry point:

1. Open BotFather → `/mybots` → your bot → `Bot Settings` → `Menu Button`.
2. Set the URL to `https://beacon.seilbekskindirov.dev/` (trailing slash required).

The exact value the bot emits in its reply keyboard is logged at `cmd/web`
startup as `settings: webAppURL=...`. Until BotFather is updated, a Menu Button
cached against the previous host returns 404.

## Backups

Two independent halves: a host-side dump script that snapshots the databases and
ships them off-box, and a local Make target that pulls those snapshots back.

### Host-side daily dump (`configs/sqlite_dump.sh`)

Runs on the deploy host (not locally). Each invocation writes a consistent
snapshot of every present database to `/opt/beacon/backups/beacon.<YYYYMMDD>.sqlite`
(via the `sqlite3` online backup, so it is safe under WAL), mirrors new snapshots
to Google Drive with `rclone`, and prunes both stores. An absent database is
skipped, not an error.

On any failure — a broken snapshot or a Google Drive `rclone` error — it logs the
reason, sends **one** `#BEACON backup FAILED` message to the admin chat, and exits
non-zero. A clean run instead sends a `#BEACON backup OK` summary — new snapshot size,
retained inventory, and any pruned files — silenced by `NOTIFY_ON_SUCCESS=0`. Drive
failures are isolated: the local snapshot and local retention prune always run even when
the mirror is down, so a cloud outage never compromises on-host backups. Alert creds come
from `BEACON_TELEGRAMBOT_DSN` in the service env file by default (no extra config); verify
the wiring on the host without leaking the token via `sqlite_dump.sh selftest`.

| Retention | Default | Override (in `.env`) |
|-----------|---------|----------------------|
| Local host | 7 days  | `LOCAL_RETENTION_DAYS`  |
| Google Drive | 14 days | `REMOTE_RETENTION_DAYS` |

`make init` ships the script to `/opt/beacon/backups/sqlite_dump.sh`
and installs `configs/sqlite_dump.env.example` as `/opt/beacon/backups/.env` **only
if it does not already exist** (your edited `.env` is never overwritten). Optional
overrides — `GDRIVE_REMOTE`, the two retentions, the `ALERT_TELEGRAM_*` alert creds,
and `MAIN_ENV` — load from that adjacent `.env`;
rclone needs no config override there, it auto-discovers `~/.config/rclone/rclone.conf`
of the user the cron runs as.

Schedule it once with cron (the deploy step does **not** install the crontab):

```cron
0 0 * * * /opt/beacon/backups/sqlite_dump.sh > /opt/beacon/logs/backup.log 2>&1
```

Multiple hosts can share one Drive account: keep the same rclone OAuth app on each,
but give every host a unique `GDRIVE_REMOTE` subfolder in its `.env` (e.g.
`gdrive:backups/<host>/beacon`) so filenames never collide.

### Local pull (`make backups`)

Pulls the latest host snapshot **plus** the service logs into one archive
under `./backups/`:

```bash
make backups
# -> ./backups/beacon.<stamp>.tar.gz   (latest DB snapshot + logs)
```

`./backups/` is gitignored.

## License

See [`LICENSE`](LICENSE).
