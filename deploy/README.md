# Deployment

Production deployments are driven by the CI workflows in
`.github/workflows/stage.yml` and `.github/workflows/prime.yml`. They
checksum-validate every binary, pause cron wrappers, run `cmd/migrator`,
swap the webapp binary, restart the unit, and resume cron — see those
files for the live procedure.

## Live systemd units

| Environment | Unit file |
|-------------|-----------|
| Stage | `configs/srv.stage_monitor.service` |
| Prime | `configs/srv.prime_monitor.service` |

Each unit runs the long-lived `*_monitor_webapp` binary; the
`*_monitor_collector` and `*_monitor_notifier` binaries are one-shot
processes invoked by host-side cron wrappers between deploys.

Configuration (DB DSN, Telegram bot token, admin chat ID) lives in
`/opt/monitor/.{stage,prime}_monitor.env`. The public origin is baked
into the unit's `ExecStart` line (`--api-dsn`), never read from the
env file. See `CLAUDE.md` for the full configuration contract.

## Exit code & alerting

`cmd/collector` and `cmd/notifier` exit with status `0` whenever the
setup phase completes (logger, DB, migrations, repositories, runner
construction). Per-source / per-notification failures are persisted
to the database (`execution_history`, notification pool) and logged to
stdout, but they do **not** cause a non-zero exit. Cron wrappers that
previously alerted on a non-zero exit code must instead watch stdout
for these marker lines:

```
execution: completed with errors: ...   # one or more per-source failures
execution: stopped by signal: ...       # SIGTERM/SIGINT interrupted the run
```

Either or both may appear in a single run; both are followed by the
closing `execution: done` line. A run that emits neither marker
completed cleanly. Failed-source detail is available via the HTTP
routes `GET /api/errors/execution` and `GET /api/notifications/failed`.

`chromedp`-kind sources share one Chromium subprocess per collector
tick and execute sequentially (the underlying CDP socket is not
concurrency-safe). Each source has a 30 s navigation timeout, so a
tick with N chromedp sources can take up to ~30 s × N in the worst
case. Pick the cron interval accordingly — for example, five
chromedp sources need at least a 150 s gap between invocations to
avoid overlapping ticks.

The `cmd/web` `http server: listening on N port` line fires only after
the kernel has bound the port, so monitoring probes may use it as a
reliable readiness marker. For an in-process readiness check the webapp
also serves `GET /healthz` which runs a cheap repository read and
returns `{"status":"ok"}` (200) when the database is reachable or
`{"status":"unavailable"}` (503) otherwise.

Error-level log entries are written to the rotating log file only.
No automatic Telegram alert hook is wired today — monitor the log
files directly or configure an external alert on systemd unit
failure (`OnFailure=`) to page on critical issues.

## Backup & restore

The SQLite file is the entire persistent state of the service. Run a
nightly online backup with `deploy/cron/sqlite-backup.sh` — it uses the
SQLite `.backup` API which is safe with live writers (collector,
notifier, web all keep running).

### Installation

Copy the script to the deploy host once per environment:

```bash
scp deploy/cron/sqlite-backup.sh "$REMOTE:/opt/monitor/sqlite-backup.sh"
ssh "$REMOTE" chmod +x /opt/monitor/sqlite-backup.sh
```

Install the cron entry (root, runs at 03:14 local time):

```cron
14 3 * * * \
  ENV_FILE=/opt/monitor/.stage_monitor.env \
  BACKUP_DIR=/var/backups/monitor/stage \
  RETENTION_DAYS=14 \
  /opt/monitor/sqlite-backup.sh \
  >> /opt/monitor/logs/stage/sqlite-backup.log 2>&1
```

Mirror the same line with the prime env file + backup dir on the prime
host.

### Restore drill

Stop the live service so no writer holds the destination path:

```bash
systemctl stop stage_monitor
```

Replace the live DB with a chosen snapshot:

```bash
DB="$(awk -F= '/^SQLITEDB_DSN/{print $2}' /opt/monitor/.stage_monitor.env)"
DB="${DB#sqlite://}"
mv "$DB" "$DB.before-restore"
[[ -f "$DB-wal" ]] && mv "$DB-wal" "$DB-wal.before-restore"
[[ -f "$DB-shm" ]] && mv "$DB-shm" "$DB-shm.before-restore"
cp /var/backups/monitor/stage/stage_monitor.<YYYY-MM-DD>.sqlite "$DB"
chown root:root "$DB"
chmod 600 "$DB"
```

The WAL and SHM sidecars must move out of the way too — otherwise SQLite
re-attaches the previous live WAL on next open and replays uncommitted
pages into the restored snapshot, corrupting it.

Restart the service and check `/healthz` returns 200:

```bash
systemctl start stage_monitor
curl -fs http://localhost:8010/healthz
```

Exercise the restore at least once per environment before relying on
the backup chain in an incident.

## Breaking changes

### `GET /api/me/subscriptions` — `?initData=` query fallback removed

The `X-Telegram-Init-Data` header is now the only accepted form of
authentication. The previous `?initData=...` query-string fallback was
removed because the HMAC-signed token would otherwise land in HTTP
access logs and `Referer` headers for up to its 24h validity window.

Operators with saved curl commands or monitoring probes that hit the
endpoint with `?initData=...` now receive a generic 401 — update them
to pass the value via the header instead. The Telegram WebApp JS SDK
sends the header automatically, so production traffic is unaffected.
