# cmd/rulegen

`rulegen` is an operator-invoked tool that generates an extraction rule for a
rate source by querying an LLM, validating the rule against the live source URL,
and persisting the result to the SQLite database.

Run it once after adding a new source row (via seed migration) or when an
existing rule stops matching (e.g. a bank redesigns its page).

## When to run

- After inserting a new row into `rate_sources` via a seed migration.
- After `cmd/sourceaudit` reports a MISS for a source (the extraction rule has
  likely broken due to a page change).

## Prerequisites

- The database must be fully migrated (`make migrate` or `./build/migrator`).
- `SQLITEDB_DSN`, `AI_PRIMARY_DSN` must be set in the environment.
- The source row must already exist in `rate_sources`.

## Usage

```
rulegen <source-name> [flags]
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--force-fallback` | false | Skip primary, go straight to fallback (one attempt) |
| `--max-primary-attempts N` | 3 | Max primary attempts before escalation |
| `--logs-dir DIR` | `$TMPDIR/logs` | Path to the logs directory |
| `--verbosity LEVEL` | `warning` | Log level: debug, info, warning, error, severe, critical |

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success — rule generated and persisted |
| 1 | Generation failed — source exists but no valid rule could be produced |
| 2 | Usage error — missing argument or malformed flag |
| 3 | Infrastructure error — DB unreachable or migrations not applied |

## Example invocations

```bash
# Generate a rule using the primary AI client (default 3 attempts)
SQLITEDB_DSN=sqlite://_:_@_:_/./build/monitor.db \
AI_PRIMARY_DSN=groq://_:<base64url(KEY)>@api.groq.com/openai/v1?model=llama-3.1-8b-instant \
./build/rulegen halyk_usd

# Force fallback (skip primary, one attempt with fallback client)
./build/rulegen halyk_usd --force-fallback

# Inspect flags
./build/rulegen --help
```

Successful output looks like:

```
OK source=halyk_usd rules=1 value=450.25 attempts=2 escalated=false provider=Groq[llama-3.1-8b-instant] model=Groq[llama-3.1-8b-instant]
```

## Cost note

Each invocation makes at most `max-primary-attempts + 1` LLM calls (default: 4
calls maximum). With `--force-fallback` it makes exactly 1 call to the fallback
client.

The primary is typically a free Groq key (no cost). If `AI_FALLBACK_DSN` points
at a paid provider such as `anthropic/claude-*` via OpenRouter, a single
escalated invocation can cost on the order of cents to dollars depending on body
size. Check your provider's pricing before running `rulegen` on many sources in
quick succession.

Logs for each invocation are written to `--logs-dir/rulegen.YYYYMMDD.log`. The
log contains the full prompt, the AI response, and the rule execution outcome,
which is the primary audit trail.

## Body size notes

The constant `maxBodyBytesForLLM` (200 KB, defined in
`internal/application/rulegen/sanitizer.go`) controls how much of the page is
sent to the LLM after stripping `<script>` and `<style>` blocks. If the rate
value on the page is past the 200 KB mark, rule generation will fail with a
non-matching extraction. Mitigation: find a narrower endpoint (JSON API,
per-currency URL) and update the source row's `url` field.

If the raw body exceeds 5 MB before stripping, `rulegen` aborts immediately
without making any LLM call.

## Headless / chromedp sources

Sources built on React or similar client-side frameworks serve a near-empty HTML
shell that is hydrated in the browser via JavaScript. The plain HTTP fetcher sees
little usable text in these cases. `rulegen` ships a chromedp-based fetcher that
spawns a headless Chrome instance, navigates to the URL, waits for the `<body>`
element to be visible, adds a 5 s network-idle window (default), and captures the
fully-rendered `outerHTML`. For sources where a DOM selector is available to signal
hydration completion, see the `options.wait_selector` override in the Tuning section below.

To mark a source as requiring the chromedp fetcher, set `fetcher_kind='chromedp'`
in the seed migration for that source (see
`migrations/202605.010.rate_sources.add_fetcher_kind.sql` for the pattern).

### Chromium installation

The build does **not** require Chromium. It is a runtime dependency only when
`rulegen` is invoked against a source with `fetcher_kind='chromedp'`.

Install on the deploy host:
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

### CHROMIUM_PATH environment variable

If the Chromium binary is not on PATH, or you want to use a specific version,
set `CHROMIUM_PATH` to the absolute path before invoking `rulegen`:

```bash
export CHROMIUM_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
./build/rulegen KZ_BCC_BID_USD_KZT
```

When `CHROMIUM_PATH` is unset, chromedp searches PATH in this order:
`chromium`, `chromium-browser`, `google-chrome`, `chrome`.

### Behaviour and latency

- Each `rulegen` invocation spawns a fresh Chrome process for the chromedp fetch,
  then terminates it. Cold-start overhead is ~2–3 s on the ARM deploy host.
- Total per-fetch wall clock is typically 8–15 s (navigation + 5 s default idle wait, or until the wait_selector is visible).
- The default hard timeout is 30 s. If Chrome cannot navigate and capture the DOM
  within that window, `Fetch` returns an error wrapping `context.DeadlineExceeded`.
  Operators can simply retry: `./build/rulegen <source-name>`.
- Flags: `--headless`, `--disable-gpu`, `--no-sandbox` (required in systemd/root),
  `--disable-blink-features=AutomationControlled`.

### CI / deploy host setup

The GitHub runner that builds and tests this project does **not** need Chromium.
All chromedp subtests in `chromedpfetcher_test.go` call `findChromiumOrSkip(t)`
and skip cleanly when no binary is on PATH.

The deploy host **does** need Chromium for any source with `fetcher_kind='chromedp'`.
The CI workflows (`.github/workflows/{prime,stage}.yml`) deploy via SSH and run
`cmd/migrator` before swapping binaries. Chromium installation on the host is a
one-time manual step and is **not** automated in the workflow files — the install
command is idempotent and should be run once by the operator:

```bash
# On the deploy host (run once after first deployment of plan 013)
sudo apt-get install -y chromium-browser
```

If Chromium is missing when the first cron invocation of `rulegen` fires against
a `chromedp` source, the run fails with a clear error:
`exec: "chromium": executable file not found in $PATH`. Install Chromium and retry.

## Tuning for chromedp sources

Sources with `fetcher_kind = "chromedp"` render through a headless Chrome
instance, so the LLM receives the post-hydration DOM rather than the raw
HTML. The rendered DOM is typically 30–50 % larger than the raw response
and contains hydrated framework state, which makes pattern discovery
harder than for plain HTTP sources. Two operator-side levers:

1. **`options.wait_selector`** (per-source). Set the `wait_selector` key
   on the source's `options` JSON column to a CSS selector that appears
   only after the rate table has loaded. The fetcher will block on
   `WaitVisible(selector)` instead of falling back to a fixed
   post-`body` sleep. Example seed snippet:
   ```sql
   UPDATE rate_sources
   SET    options = json_set(COALESCE(options, '{}'), '$.wait_selector', 'div.text-lg')
   WHERE  name = 'KZ_BCC_BID_USD_KZT';
   ```
   Use the same structural marker the extraction rule will key off
   (e.g. `div.text-lg` for BCC rows). The 30 s wall-clock timeout still
   caps the wait; a missing selector surfaces as
   `chromedp fetch <url>: context deadline exceeded`.

2. **`--max-fallback-attempts=4`** (per-invocation). Chromedp-rendered
   DOMs are noisier than plain HTML; if the first two fallback
   attempts both fail with rule-validation or plausibility errors,
   a larger budget often converges. Example:
   ```bash
   ./build/rulegen --force-fallback --max-fallback-attempts=4 KZ_BCC_BID_USD_KZT
   ```

The default post-body sleep is 5 s (raised from 1.5 s in plan 014)
and applies only when `wait_selector` is empty.

## Running against the deployed database

On the deploy host with the service's `EnvironmentFile` sourced:

```bash
set -a; . /etc/monitor/prime.env; set +a
./build/rulegen <source-name>
```

SQLite supports one writer at a time. Running `rulegen` while `cmd/web` is
active may block briefly; the DSN's `_busy_timeout` option (already set by the
project) makes this transparent in practice. Do not run two `rulegen` instances
in parallel against the same database.

## See also

The same audit loop is reachable through two additional surfaces:

**HTTP endpoint** — admin callers can POST directly to `cmd/web`:

```
POST /api/sources/{name}/rules/generate
X-Telegram-Init-Data: <signed initData>
```

Use the CLI when you need direct shell access or when running outside the bot
admin's Telegram context.

**Telegram bot command** — the `cmd/web` Telegram bot accepts `/regen` from
the configured admin chat (DM only — group usage is silently ignored because
the bot compares `msg.Chat.ID` against the admin chat id, which is set from the
`TELEGRAMBOT_DSN` environment variable):

```
/regen <source-name> [--force-fallback] [--max-fallback=N]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force-fallback` | false | Skip primary, use fallback AI for the first attempt |
| `--max-fallback=N` | 0 (use default budget) | Override fallback attempt count (1–10) |

The bot replies with a "Working on rules…" message immediately and then edits
it in place once the generation finishes (success or failure). The 120 s wall-clock
ceiling is the same as the HTTP endpoint.

Example (send in the admin DM):
```
/regen KZ_HALYK_BID_USD_KZT
/regen KZ_BCC_BID_USD_KZT --force-fallback
/regen KZ_JUSAN_ASK_USD_KZT --max-fallback=5
```

**Security note (HTTP endpoint):** if using the `?initData=` query-string fallback
for curl testing, the admin's signed token appears in the URL (browser history,
access logs, HTTP proxies). Prefer the `X-Telegram-Init-Data` header when possible.
