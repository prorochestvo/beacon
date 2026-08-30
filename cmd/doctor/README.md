# cmd/doctor

`doctor` is the operator maintenance umbrella binary for the beacon
service. It hosts two subcommands:

- **`doctor rulegen`** — LLM-driven extraction-rule generator. Generates or
  regenerates a rule for a named source by querying an LLM, validating the rule
  against the live source URL, and persisting the result to the SQLite database.
- **`doctor audit`** — read-only seed auditor. Probes seeded rate sources
  against their live URLs to verify that extraction rules still return plausible
  values.

Run `doctor --help` for a combined overview, or `doctor <subcommand> --help` for
per-subcommand flags and exit codes.

## Lifecycle: one-shot cron binary

Like `cmd/collector` and `cmd/notifier`, `doctor` is a one-shot process — it
opens the DB, does its work, and exits. The only long-lived binary in this
project is `cmd/web`. All three one-shot binaries are scheduled by host-side
cron wrappers; none of them have systemd units.

Suggested cadences (host-side `crontab`, times in UTC):

| Binary | Cadence | Crontab |
|--------|---------|---------|
| `collector` | hourly | `0 * * * *` |
| `notifier` | every 30 minutes | `*/30 * * * *` |
| `doctor audit --all` | weekly, Saturday 00:00 | `0 0 * * 6` |

Tighten `collector` to match your shortest source interval if needed —
pick whichever is more frequent. `notifier` cadence sets dispatch latency
for due notifications and is independent of the collector tick.

`doctor rulegen` is on-demand by design: run it after a seed migration adds a
new source, or after `doctor audit` reports a MISS. `doctor rulegen --all`
exists as a batch escape hatch (and is safe to wire to cron — see the cost
note below — but is not the default schedule).

## `doctor rulegen` — Rule generation

### When to run

- After inserting a new row into `rate_sources` via a seed migration.
- After `doctor audit` reports a MISS for a source (the extraction rule has
  likely broken due to a page change).

### Prerequisites

- The database must be fully migrated (`make migrate` or `./build/migrator`).
- `BEACON_SQLITEDB_DSN` and `BEACON_AI_PRIMARY_DSN` must be set in the environment.
- The source row must already exist in `rate_sources`.

### Usage

```
doctor rulegen <source-name> [flags]
doctor rulegen --all [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--all` | false | Iterate every active source (cron mode; always exits 0) |
| `--force-fallback` | false | Skip primary, go straight to fallback AI |
| `--max-primary-attempts N` | 3 | Max primary attempts before escalation |
| `--max-fallback-attempts N` | 2 | Max fallback attempts before total failure |
| `--logs-dir DIR` | `$TMPDIR/logs` | Path to the logs directory |
| `--verbosity LEVEL` | `warning` | Log level: debug, info, warning, error, severe, critical |

### Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `BEACON_SQLITEDB_DSN` | Yes | SQLite connection string |
| `BEACON_AI_PRIMARY_DSN` | Yes | Primary AI provider DSN |
| `BEACON_AI_FALLBACK_DSN` | No | Fallback AI provider DSN; stub used when absent |
| `BEACON_CHROMIUM_PATH` | No | Absolute path to Chromium binary; see below |

### Exit codes

| Code | Meaning (single-source mode) |
|------|------------------------------|
| 0 | Success — rule generated and persisted |
| 1 | Generation failed — source exists but no valid rule could be produced |
| 2 | Usage error — missing argument or malformed flag |
| 3 | Infrastructure error — DB unreachable or migrations not applied |

In `--all` mode the exit code is always `0` (per-source failures are logged and
counted). Exit `3` only occurs if infrastructure initialisation fails before
processing begins. Check the `rulegen --all:` summary line in stdout for
`processed/succeeded/failed/skipped` counts.

Note: the summary line prefix is the literal string `rulegen --all:` (not
`doctor rulegen --all:`) to preserve compatibility with external grep patterns
used by operators and cron scripts. This is intentional — see plan trade-off 4.

### Example invocations

```bash
# Generate a rule using the primary AI client (default 3 attempts)
BEACON_SQLITEDB_DSN=sqlite://_:_@_:_/./build/beacon.db \
BEACON_AI_PRIMARY_DSN=groq://_:<base64url(KEY)>@api.groq.com/openai/v1?model=llama-3.1-8b-instant \
./build/doctor rulegen halyk_usd

# Force fallback (skip primary, one attempt with fallback client)
./build/doctor rulegen halyk_usd --force-fallback

# Regenerate rules for all active sources
./build/doctor rulegen --all

# Inspect flags
make doctor-help
```

Successful single-source output:

```
OK source=halyk_usd rules=1 value=450.25 attempts=2 escalated=false provider=Groq[llama-3.1-8b-instant] model=Groq[llama-3.1-8b-instant]
```

### Cost note

Each invocation makes at most `max-primary-attempts + 1` LLM calls (default: 4
calls maximum). With `--force-fallback` it makes exactly 1 call to the fallback
client.

The primary is typically a free Groq key (no cost). If `BEACON_AI_FALLBACK_DSN` points
at a paid provider such as `anthropic/claude-*` via OpenRouter, a single
escalated invocation can cost on the order of cents to dollars depending on body
size. Check your provider's pricing before running `rulegen` on many sources in
quick succession.

With `--all` and ~10 active sources, the run can make up to 40 LLM calls. Set a
generous cron timeout (at least 30 minutes). Logs are written to
`<logs-dir>/doctor.YYYYMMDD.log`.

### Body size notes

The constant `maxBodyBytesForLLM` (200 KB, defined in
`internal/application/rulegen/sanitizer.go`) controls how much of the page is
sent to the LLM after stripping `<script>` and `<style>` blocks. If the rate
value on the page is past the 200 KB mark, rule generation will fail. Mitigation:
find a narrower endpoint (JSON API, per-currency URL) and update the source row's
`url` field.

If the raw body exceeds 5 MB before stripping, `doctor rulegen` aborts
immediately without making any LLM call.

## `doctor audit` — Seed source auditing

### When to run

- Before deploying a new source to confirm the extraction rule is correct.
- As a scheduled health check to detect rule breakage caused by page changes.

### Usage

```
doctor audit [flags]
```

Run from the repository root — `--seed-glob` is relative to the process CWD.

| Flag | Default | Description |
|------|---------|-------------|
| `--all` | false | Audit every seeded source |
| `--source NAME` | "" | Audit one source by exact name |
| `--only REGEX` | "" | Audit sources whose names match a regex |
| `--seed-glob GLOB` | `migrations/*.seed*.sql` | Glob pattern for seed SQL files |
| `-v` | false | Verbose: print per-source table |

Exactly one of `--all`, `--source`, or `--only` must be supplied.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | All probes OK |
| 1 | At least one source reported MISS |
| 2 | Usage error (missing or mutually exclusive flags, bad regex) |
| 3 | Infrastructure error (seed-glob failure, no sources found, auditor error) |

Note: the 1/2/3 split is finer-grained than the old `cmd/sourceaudit` which exited 1
for everything non-zero. Scripts using `[ $? -ne 0 ]` continue to work unchanged;
scripts watching exit code 1 specifically as "MISS" now also benefit from code 2
(usage errors) and code 3 (infrastructure failures) being distinguishable.

### Example invocations

```bash
make audit ARGS="--all"
make audit ARGS="--source halyk_usd"
make audit ARGS="--only '^halyk_'"
make audit ARGS="--all -v"

# Direct invocation
./build/doctor audit --all
./build/doctor audit --source halyk_usd
```

## Headless / chromedp sources

Sources built on React or similar client-side frameworks serve a near-empty HTML
shell hydrated in the browser via JavaScript. The plain HTTP fetcher sees little
usable text. `doctor rulegen` ships a chromedp-based fetcher that spawns a
headless Chrome instance, navigates to the URL, waits for the `<body>` element to
be visible, adds a 5 s network-idle window (default), and captures the fully-
rendered `outerHTML`.

To mark a source as requiring chromedp, set `fetcher_kind='chromedp'` in the seed
migration for that source.

### Chromium installation

Chromium is a runtime dependency only — not required for the build. Install on the
deploy host:

```bash
# Debian/Ubuntu (Oracle Cloud ARM Free Tier)
sudo apt-get install -y chromium-browser

# macOS (local development)
brew install --cask chromium
```

After installation, confirm the binary is on PATH:

```bash
which chromium-browser   # Debian/Ubuntu
which chromium           # Arch / Snap variants
```

### BEACON_CHROMIUM_PATH environment variable

If the Chromium binary is not on PATH, or you want to use a specific version, set
`BEACON_CHROMIUM_PATH` to the absolute path before invoking `doctor rulegen`:

```bash
export BEACON_CHROMIUM_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
./build/doctor rulegen KZ_BCC_BID_USD_KZT
```

When `BEACON_CHROMIUM_PATH` is unset, chromedp searches PATH in this order:
`chromium`, `chromium-browser`, `google-chrome`, `chrome`.

### Behaviour and latency

- Each invocation spawns a fresh Chrome process, then terminates it. Cold-start
  overhead is ~2–3 s on the ARM deploy host.
- Total per-fetch wall clock is typically 8–15 s (navigation + 5 s default idle
  wait, or until the wait_selector is visible).
- The default hard timeout is 30 s. If Chrome cannot navigate and capture the DOM
  within that window, `Fetch` returns an error wrapping `context.DeadlineExceeded`.
  Operators can simply retry: `./build/doctor rulegen <source-name>`.
- Flags used: `--headless`, `--disable-gpu`, `--no-sandbox` (required in
  systemd/root), `--disable-blink-features=AutomationControlled`.

### CI / deploy host setup

The GitHub runner does not need Chromium. All chromedp subtests in
`chromedpfetcher_test.go` call `findChromiumOrSkip(t)` and skip cleanly when no
binary is on PATH.

The deploy host needs Chromium for any source with `fetcher_kind='chromedp'`.
Installation on the host is a one-time manual step:

```bash
# On the deploy host (run once after first deployment)
sudo apt-get install -y chromium-browser
```

If Chromium is missing when `doctor rulegen` fires against a `chromedp` source,
the run fails with:
`exec: "chromium": executable file not found in $PATH`. Install Chromium and retry.

## Tuning for chromedp sources

Sources with `fetcher_kind = "chromedp"` receive the post-hydration DOM, which is
typically 30–50 % larger than the raw response and contains hydrated framework
state. Two operator-side levers:

1. **`options.wait_selector`** (per-source). Set the `wait_selector` key on the
   source's `options` JSON column to a CSS selector that appears only after the rate
   table has loaded. The fetcher will block on `WaitVisible(selector)` instead of a
   fixed post-body sleep. Example:
   ```sql
   UPDATE rate_sources
   SET    options = json_set(COALESCE(options, '{}'), '$.wait_selector', 'div.text-lg')
   WHERE  name = 'KZ_BCC_BID_USD_KZT';
   ```

2. **`--max-fallback-attempts=4`** (per-invocation). Chromedp-rendered DOMs are
   noisier than plain HTML; if the first two fallback attempts fail, a larger budget
   often converges:
   ```bash
   ./build/doctor rulegen --force-fallback --max-fallback-attempts=4 KZ_BCC_BID_USD_KZT
   ```

The default post-body sleep is 5 s and applies only when `wait_selector` is empty.

## Running against the deployed database

On the deploy host with the service's `EnvironmentFile` sourced:

```bash
set -a; . /opt/beacon/.env; set +a
./build/doctor rulegen <source-name>
```

SQLite supports one writer at a time. Running `doctor rulegen` while `cmd/web` is
active may block briefly; the DSN's `_busy_timeout` option (already set by the
project) makes this transparent in practice. Do not run two `doctor rulegen`
instances in parallel against the same database.

## Telegram bot integration

The Telegram bot's `/regen` command (admin DM only) invokes the same generation
logic as `doctor rulegen` via the `cmd/web` HTTP endpoint. The summary line prefix
`rulegen --all:` in stdout is preserved so existing grep patterns on cron logs
continue to match.
