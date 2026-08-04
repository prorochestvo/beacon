# Deployment

Tests run on every push/PR to `main` (`.github/workflows/ci.main.yml`).
Production deployment is driven by `.github/workflows/release.yml`, which fires
on an `r_*` release tag — see that file for the live procedure.

## On-server layout

The host follows the standard release layout: immutable per-version artifact
sets and a channel symlink the service runs through.

```
/opt/beacon/                 root:root          0755   base dir — CI cannot write
    .env                     root:root          0600   secrets
    collector.sh notifier.sh root:root          0750   cron wrappers (call bin/release/*)
    beacon.sqlite            root:root          0600   DB (+ -wal/-shm)
    logs/                    root:root          0750
    backups/                 root:root          0755   sqlite_dump output
    artifacts/               github_aide:github_aide 0755   immutable builds, by VERSION_ID
        20260628T…-r_0.0.1/      webapp collector notifier migrator (+x)
    bin/                     github_aide:github_aide 0755
        release -> ../artifacts/20260628T…-r_0.0.1
```

`VERSION_ID = <UTC YYYYMMDDhhmmss>-r_<semver>`. The CI deploy user (`github_aide`)
may write **only** inside `artifacts/` and `bin/` — base-dir write is what would
let it replace `.env`/`*.sh`/the DB, so it is deliberately denied. A deploy is:
upload `artifacts/<VID>/`, flip `bin/release` (relative symlink), run migrations
(`sudo systemctl start beacon-migrate`), restart the webapp
(`sudo systemctl restart beacon`), health-gate on `/health/check` (readiness probe),
and on failure flip `bin/release` back to the previous VERSION_ID and restart — rollback is one
symlink. Old versions are pruned to the newest 5 not referenced by any channel.

## Units, config, sudoers

- `configs/beacon.service` — long-lived webapp; `ExecStart=/opt/beacon/bin/release/webapp …`.
- `configs/beacon-migrate.service` — one-shot migrator (`bin/release/migrator`), run
  at deploy after the flip; runs as root so the deploy user never writes the DB.
- `collector`/`notifier` — one-shot, invoked by host cron wrappers that call
  `bin/release/{collector,notifier}`.
- `configs/beacon-deploy.sudoers` → `/etc/sudoers.d/beacon-deploy` (0440): grants
  `github_aide` exactly `systemctl restart beacon` and `systemctl start beacon-migrate`.
- Configuration (DB DSN, Telegram bot token, admin chat ID) lives in
  `/opt/beacon/.env`; the public origin is baked into the unit's `--api-dsn`,
  never the env file. `make init` provisions all of the above.

## Hardening (recommended follow-up)

The service currently runs as **root** (`User=root`), so a build shipped by a
leaked CI key is executed by root on restart. The CI user is already confined to
`artifacts/`+`bin/`, but to cap the blast radius, de-root the service: create a
dedicated `beacon` system user, move the DB to `/var/lib/beacon` (`beacon:beacon
0750`), set `User=beacon` on both units (+ `NoNewPrivileges`, `ProtectSystem=strict`,
`ReadWritePaths=/var/lib/beacon /opt/beacon/logs`), and make `.env` `0640 root:beacon`
so cron one-shots running as `beacon` can read it. Then a leaked CI key tops out at
"ship an artifact that runs as `beacon`" — never root.

## Outbound proxy

All outbound HTTP/HTTPS traffic from `cmd/collector` (rate-source scrapes, chromedp
browser connections) and `cmd/doctor` (AI provider calls, chromedp fetcher) is routed
through the proxy configured by `BEACON_PROXY_URL`. Telegram Bot API traffic is **always
direct** — the bypass is enforced in code via a hardcoded no-proxy transport in
`internal/infrastructure/telegrambot/tbotclient.go`, so the bot continues to deliver
notifications even when everything else is gated behind the proxy.

To enable, add one line to `/opt/beacon/.env`:

```
BEACON_PROXY_URL=http://127.0.0.1:7788
```

The same env file is sourced by the systemd unit and by the host-side cron
wrappers, so the proxy setting applies to all relevant binaries: `cmd/collector`
and any operator-invoked `cmd/doctor` run that inherits the same shell
environment. (`cmd/web` and `cmd/notifier` do not parse `BEACON_PROXY_URL` — their only
outbound target is Telegram, which is always direct.)

Do **not** set `HTTPS_PROXY`, `HTTP_PROXY`, or `NO_PROXY` for proxy routing — they
are not consulted by any component in this project.

**Failure mode.** If the proxy process is down, every outbound rate-source
scrape fails immediately with "connection refused" on `127.0.0.1:7788`.
These errors are persisted to `execution_history` and surface via
`GET /api/errors/execution`. The collector log emits
`execution: completed with errors: …` for each affected source. Telegram
notifications are unaffected because the Telegram bypass is hardcoded. To verify
the proxy is active from the deploy host:

    curl -fs -x http://127.0.0.1:7788 https://api.ipify.org

At startup `cmd/collector` and `cmd/doctor` each log one line confirming the proxy
state (`proxy: BEACON_PROXY_URL=http://127.0.0.1:7788` or `proxy: not configured`); grep
the log for `proxy:` to confirm.

For interactive `cmd/doctor` invocations, source the env file first so the
proxy applies:

    set -a; source /opt/beacon/.env; set +a
    /opt/beacon/doctor rulegen --all

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
reliable readiness marker. For in-process health checks the webapp exposes two endpoints:
`GET /ping` (liveness — always 200, touches no dependency) and
`GET /health/check` (readiness — probes SQLite and the Telegram bot,
returns per-component JSON; 200 when all healthy, 503 when any are down).
`GET /healthz` is kept as a backward-compatible alias for `/ping`.

Error-level log entries are written to the rotating log file only.
No automatic Telegram alert hook is wired today — monitor the log
files directly or configure an external alert on systemd unit
failure (`OnFailure=`) to page on critical issues.

## Backup & restore

The SQLite file is the entire persistent state of the service. Snapshot
creation runs on the host via `configs/sqlite_dump.sh` (installed by
`make init`): a daily online backup, safe under WAL, written to
`/opt/beacon/backups/beacon.<YYYYMMDD>.sqlite.gz` and mirrored to Google
Drive. See the project `README.md` for install, scheduling, and retention. The
restore drill below is the operator-facing half not covered there.

Snapshots are gzipped (~4× on real data: 8.1 MB → 2.1 MB), and local retention is
**3 days** against 14 on Google Drive. Both numbers exist because the host disk is
the scarce resource — roughly 1.2 GB free — while the remote is not: the local
copies make a same-week restore fast, the remote mirror is the archive of record.
The rclone sync always completes before the local prune, so shortening local
retention can never drop a snapshot the remote has not already taken. Override
either with `LOCAL_RETENTION_DAYS` / `REMOTE_RETENTION_DAYS` in
`/opt/beacon/backups/.env`.

Both extensions are handled everywhere: snapshots predating compression and the
`cp` fallback's uncompressed set still restore, prune, and mirror normally.

### Reading the database without stopping anything

The live DB is root-owned `0600`, so inspection goes through the snapshots, which are
world-readable. A plain read-only open of one fails:

```
$ sqlite3 -readonly /opt/beacon/backups/beacon.20260804.sqlite "SELECT 1"
Error: in prepare, attempt to write a readonly database (8)
```

`journal_mode=WAL` is persisted in the database file header, so SQLite wants to create
`-shm`/`-wal` sidecars beside the file — impossible in a root-owned backup directory.
Open it as a URI with `immutable=1` instead, which tells SQLite the file cannot change
and skips WAL setup:

```bash
sqlite3 "file:/opt/beacon/backups/beacon.20260804.sqlite?immutable=1" \
  "SELECT filename, applied_at FROM __schema_migrations ORDER BY filename DESC LIMIT 5;"
```

Current snapshots are gzipped, so decompress before opening — `immutable=1` still applies
to the decompressed file:

```bash
gunzip -c /opt/beacon/backups/beacon.20260804.sqlite.gz > /tmp/inspect.sqlite
sqlite3 "file:/tmp/inspect.sqlite?immutable=1" "SELECT COUNT(*) FROM rate_values;"
```

From a workstation, `make db-inspect` does all of that: it streams the newest snapshot down
(decompressing on the fly), reports its age, and opens it locally, so the host needs neither
a `sqlite3` binary nor scratch space. `make db-inspect ARGS="<sql>"` runs one query and exits.
The age matters: snapshots are cut at 00:00 UTC, so immediately after a deploy the newest
one predates the migration you are trying to confirm. `make db-snapshot` cuts a fresh one
on demand (interactive `sudo` on the host).

To make that snapshot non-interactive — for a scripted post-deploy check — add one narrow
line to sudoers for the operator account, mirroring the CI grant in
`configs/beacon-deploy.sudoers`:

```
<operator> ALL=(root) NOPASSWD: /opt/beacon/backups/sqlite_dump.sh
```

Not installed by `make init` on purpose: it widens what a compromised operator key can do,
so it is an explicit decision rather than a default.

### Restore drill

Stop the live service so no writer holds the destination path:

```bash
systemctl stop beacon
```

Replace the live DB with a chosen snapshot:

```bash
DB="$(awk -F= '/^BEACON_SQLITEDB_DSN/{print $2}' /opt/beacon/.env)"
DB="${DB#sqlite://}"
mv "$DB" "$DB.before-restore"
[[ -f "$DB-wal" ]] && mv "$DB-wal" "$DB-wal.before-restore"
[[ -f "$DB-shm" ]] && mv "$DB-shm" "$DB-shm.before-restore"
gunzip -c /opt/beacon/backups/beacon.<YYYYMMDD>.sqlite.gz > "$DB"
chown root:root "$DB"
chmod 600 "$DB"
```

The WAL and SHM sidecars must move out of the way too — otherwise SQLite
re-attaches the previous live WAL on next open and replays uncommitted
pages into the restored snapshot, corrupting it.

Restart the service and verify readiness:

```bash
systemctl start beacon
curl -fs http://localhost:8000/health/check
```

Exercise the restore at least once per environment before relying on
the backup chain in an incident.
