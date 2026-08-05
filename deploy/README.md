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

### Config drift

**`make init` is the only thing that installs `configs/`, and the release pipeline never
touches them.** A change to a systemd unit, an nginx snippet or the backup script therefore
ships in the repository, passes CI, appears in a release, and silently does not take
effect. That is not hypothetical: snapshot compression sat in git for three weeks while the
host kept writing uncompressed backups, and was found by reading the output of a manual
command rather than by anything checking.

`make config-drift` compares each installed file against its repository copy and reports
`same` / `DIFFERS` / `MISSING` / `UNKNOWN`. It is read-only and needs no privileges. The
same check runs on every release as a non-fatal step, writing to the job summary and
raising a workflow warning when anything has fallen behind.

`UNKNOWN` is its own state on purpose: `/etc/sudoers.d/beacon-deploy` is `0440 root:root`
and cannot be read by the SSH account, so the check has no opinion about it. Reporting that
as `same` would be a false negative and as `DIFFERS` a false alarm — both worse than saying
so.

The file list is parsed out of the `init` recipe rather than kept in a manifest beside it,
so the check covers whatever init installs by construction; a manifest would be a second
place to forget. `/opt/beacon/backups/.env` is excluded because it is a template the
operator edits after installation and is *supposed* to diverge.

The check reports; it never fixes and never fails a deploy. Shipping binaries is not made
unsafe by a stale nginx snippet, and a blocking check here would create pressure to widen
the CI user's privileges so it could install configs automatically — the boundary the whole
release layout exists to hold.

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

**Collection does not use it.** `cmd/collector` reaches every rate source and Open-Meteo
directly and does not read `BEACON_PROXY_URL`; a value left in `/opt/beacon/.env` is inert.
Only `cmd/doctor` still honours it, for AI provider calls and its chromedp fetcher.

That was decided on measurement (issue #16), and the short version is that the tunnel cost
more than it bought:

| | direct | via proxy |
|---|---|---|
| origin seen by the target | Kazakhstani host in Astana | foreign datacenter, rotating daily |
| `halykbank.kz` answers | `server: nginx` | `server: ddos-guard` |
| latency | baseline | 2.9× to 12.6× slower |
| availability | — | 207 `connection refused` in one outage window |

Volume was never the issue: each host sees four requests a day, and `qazpost` reports
`x-ratelimit-remaining: 199` against the one we spend.

Telegram Bot API traffic is **always** direct regardless — the bypass is enforced in code
via a hardcoded no-proxy transport in `internal/infrastructure/telegrambot/tbotclient.go`.
`cmd/web` and `cmd/notifier` never parsed `BEACON_PROXY_URL` at all.

To point `cmd/doctor` at a proxy, add one line to `/opt/beacon/.env`:

```
BEACON_PROXY_URL=http://127.0.0.1:7788
```

Do **not** set `HTTPS_PROXY`, `HTTP_PROXY`, or `NO_PROXY` for proxy routing — they
are not consulted by any component in this project.

To verify a proxy is reachable from the deploy host:

    curl -fs -x http://127.0.0.1:7788 https://api.ipify.org

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

**Every line — on stdout and in the log file — is prefixed with an RFC3339
timestamp**, so match these markers as substrings rather than anchoring to the start
of a line. Stdout has always carried a prefix; the log file gained one only recently,
which is why incidents before that could be located in the file by line number but
never dated. Each run's first logged line names the build that wrote everything under
it.

The offset is part of the stamp (`Z` on a UTC host), so a host whose timezone changes
says so in the log rather than silently renumbering history.

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

Compression changes nothing about that. A gzipped snapshot is not a database at all until
it is expanded — SQLite answers `file is not a database (26)` for it, with or without
`immutable=1` — and the expanded file is byte-identical to what the snapshot always was,
`journal_mode=WAL` in its header included. So the `immutable=1` requirement belongs to the
decompressed file, exactly as it did before, and it is the only wrinkle either way:

```bash
gunzip -cf /opt/beacon/backups/beacon.20260804.sqlite* > /tmp/inspect.sqlite
sqlite3 "file:/tmp/inspect.sqlite?immutable=1" "SELECT COUNT(*) FROM rate_values;"
```

From a workstation, `make db-inspect` does all of that: it streams the newest snapshot down
(decompressing on the fly), reports its age, and opens it locally, so the host needs neither
a `sqlite3` binary nor scratch space. `make db-inspect ARGS="<sql>"` runs one query and exits.
The age matters: snapshots are cut at 00:00 UTC, so immediately after a deploy the newest
one predates the migration you are trying to confirm.

`make backups` pulls the same snapshot plus the service logs into one local archive at
`./backups/beacon.<stamp>.tar.gz`, reporting the snapshot's age alongside it. That archive
is the restore point to take before a risky deploy — and being on a different machine than
the database it copies, it is a better one than another file on the host's own 94%-full
disk.

### Cutting a snapshot on demand

This needs root **on the host** and cannot be driven from a workstation. The live database
is `0600 root:root`, so only root can read it consistently; the SSH account (`pi5_aide`)
has no passwordless sudo. A `make db-snapshot` target used to exist and was removed — its
whole body was an `ssh -t … sudo …` that could only ever stop on a password prompt, and
`db-inspect` pointed operators at it as if it were a way forward.

As root on the host:

```bash
/opt/beacon/backups/sqlite_dump.sh
```

then `make backups` from the workstation to pull the result down.

To make it non-interactive — for a scripted pre-deploy backup — add one narrow line to
sudoers for the operator account, mirroring the CI grant in `configs/beacon-deploy.sudoers`:

```
<operator> ALL=(root) NOPASSWD: /opt/beacon/backups/sqlite_dump.sh
```

Not installed by `make init` on purpose. Today an SSH key alone gets an unprivileged shell;
with that line it additionally gets "read the entire database as root", which is a
permanent widening of what a leaked key is worth in exchange for saving one manual command
on a deploy that happens every few weeks.

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
# -f makes this work for either extension: gunzip decompresses a .sqlite.gz, and
# copies an already-uncompressed .sqlite through unchanged. Both kinds coexist —
# snapshots predating compression, and the cp fallback's set.
gunzip -cf /opt/beacon/backups/beacon.<YYYYMMDD>.sqlite* > "$DB"
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
