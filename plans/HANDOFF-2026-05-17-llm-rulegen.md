# LLM Rule Generation — Session Handoff (2026-05-17)

This document is the single-read orientation for a fresh Claude session continuing the `llm_v2` branch work. Read top-to-bottom; deep-dive into `plans/completed/*` only when a specific decision needs the original rationale.

## TL;DR

The `fx_rate_monitor` repository got an LLM-driven rule-generation pipeline. An operator runs `./build/rulegen <source-name>` (or, after plan 017 lands, sends `/regen <source>` in Telegram). The tool fetches the source URL, feeds the body to a primary LLM (free Groq) with up to 3 attempts, escalates to a fallback LLM (paid OpenAI `gpt-5.4`) with 2 attempts, validates each candidate via a 5-stage guard chain, and persists the working rule + metadata into `rate_sources.rules` / `rate_sources.rule_metadata`. All work lives on branch `llm_v2` (not yet merged to main). Branch has 50+ commits over 10 completed plans (008–016) plus one production seed migration. Plan 017 (Telegram `/regen` command) is architected and ready for engineer; everything else for which we have empirical signal is shipping-quality.

## Repository state

- Branch: `llm_v2` (off `main`).
- Not pushed. Not merged. `make test` and `make lint` green at every commit boundary.
- Three untracked files commonly visible: `plans/017-telegram-regen-command.md` (next plan's spec), `rulegen` and `web` (build artifacts; should be `.gitignore`-d eventually, leave them be unless asked).
- A non-committed `.gitignore` modification has been visible in IDE since mid-session — that's user-side work, do not touch.

## Completed plans (chronological)

Each plan went through `architect → engineer → reviewer` per `CLAUDE.md`'s Agent Pipeline. Plan files live under `plans/completed/YYMMDD.NNNN.<slug>.md`.

| # | Slug | Summary |
|---|---|---|
| 008 | `llm-client-infrastructure` | `AIClient` interface (`Name`, `Model`, `CheckUP`, `Complete`) with four drivers: `openai` (Responses API + strict json_schema via `openai-go/v3`), `groq` (OpenAI-compatible REST), `openrouterai` (chat-completions), `stub`. DSN via `dsninjector`. Two env vars: `AI_PRIMARY_DSN` (mandatory) and `AI_FALLBACK_DSN` (optional → stub). |
| 009 | `rule-generator-with-audit-loop` | `internal/application/rulegen/` package: `Generator`, `Sanitize`, `ExecuteRule`, system prompt. `cmd/rulegen` CLI. Migration `202605.009` added `rule_metadata` column. Audit loop: 3 primary → 1 fallback. JSON-schema for `[]domain.RateSourceRule` enforced via OpenAI structured outputs. |
| 010 | `rulegen-robustness` | Split misleading `"invalid regex pattern"` error into compile vs no-match. Smart-locate body sectioning (`Locate(body, anchors, window)`). Symmetric fallback retry with `--max-fallback-attempts` (default 2). Pre-compile regex validation before executor. `Model()` added to `AIClient` interface. Shared `MinPlausibleRateValue`/`MaxPlausibleRateValue` constants. |
| 011 | `smart-locate-anchor-priority` | Strip `<head>` before Locate. Two-tier anchor priority (structural first, currency-code fallback). Added forbidden RE2 repeat-count > 1000 to prompt. Tier-1 anchor list initially included `<div class="text-lg` but a stopgap commit (`9fb6a39`) dropped it after bcc.kz exposed it as too generic. |
| 012 | `tier1-colocation-and-sanity` | Restored `<div class="text-lg` to tier-1 with co-location guard (anchor only counts if a currency code sits within ±5 KB). Per-pair plausibility table (`internal/application/rulegen/plausibility.go`) for 13 known pairs. Migration `202605.010` added `fetcher_kind` column (`plain` / `headless`). `ErrUnsupportedFetcherKind` sentinel. |
| 013 | `chromedp-fetcher` | `github.com/chromedp/chromedp v0.15.1` added. `ChromedpFetcher` behind `Fetcher` interface. Composite routing in `Generator` (`plainFetcher` / `chromedpFetcher` fields). Migration `202605.011` renamed `headless` → `chromedp`. CI workflow `apt install chromium-browser` step. Requires `CHROMIUM_PATH` env var on macOS dev (Linux deploy uses PATH lookup). |
| 014 | `chromedp-wait-strategy` | Default chromedp wait `1500ms` → `5000ms`. Added `WaitSelector` typed field to `RateSourceOptions`. Factory closure `chromedpFetcherFor func(waitSelector string) Fetcher` in `Generator`. Migration `202605.012` seeded `wait_selector="div.text-lg"` on the BCC homepage source (later deactivated; see migration 013). |
| 015 | `rulegen-http-endpoint` | `POST /api/sources/{name}/rules/generate` in `cmd/web`. Auth: Telegram `initData` + admin chat-ID match. Body: `{force_fallback, max_primary_attempts, max_fallback_attempts}` all optional. Sync execution with 120s context timeout. `rulegenLockManager` for per-source mutex (409 on contention). Sentinels `ErrSourceNotFound` and `ErrAttemptsExhausted`. Server `WriteTimeout` raised 30s → 130s. |
| 016 | `bid-ask-prompt-semantics` | Added `BID/ASK SEMANTICS` block to system prompt with multilingual label hints (EN: Buy/Sell, RU: Покупка/Продажа, KK: Сатып алу/Сату) and the spread heuristic ("LARGER number is ASK"). Condensed `RE2 CONSTRAINTS` section to stay under the 4 KB prompt-size test ceiling. |

Standalone migrations not tied to a numbered plan:

- `202605.013.rate_sources.deactivate_bcc_homepage.sql` — `UPDATE ... SET active=0 WHERE name='KZ_BCC_BID_USD_KZT'`. The BCC homepage rate widget is an Alpine.js interactive component (radio button → reactive `x-text`) that chromedp's navigate+wait pattern cannot render without click actions. The FX page (`KZ_BCC_FX_*`) covers the same USD/KZT BID via plain HTTP, so the homepage row is redundant.
- `202605.014.rate_sources.seed_jusan_halyk_usd_kzt.sql` — seeds Jusan Bank and Halyk Bank (BID + ASK each, USD/KZT). Rules were derived from cold-start `cmd/rulegen` runs and then hand-improved for production: Halyk BID had a hardcoded `"date":"2026-05-15"` anchor (would have broken next day); Jusan rules captured from non-canonical card/level widgets. Replaced with branch-agnostic Halyk anchors and main-table Jusan regexes using `[^"]*secondColumn[^"]*` / `[^"]*thirdColumn[^"]*` prefix matching to survive Next.js CSS-module hash changes.

## Architecture: rulegen pipeline

```
       ┌────────────────────────────────────────────────────────────┐
       │  cmd/rulegen CLI         POST /api/sources/{name}/...      │
       │  /regen Telegram cmd     (auth: initData + admin chat ID)  │
       └────────────────────────┬────────────────────────────────────┘
                                ↓
                   internal/application/rulegen/
                   ┌──────────────────────────────┐
                   │  Generator.Generate(ctx,name)│
                   └──────┬───────────────────────┘
                          ↓
   1. fetcher_kind switch ─→ plainFetcher (sourceaudit.HTTPFetcher)
                              chromedpFetcher (per-source factory)
                              else → ErrUnsupportedFetcherKind
                          ↓
   2. fetch raw body
                          ↓
   3. Sanitize:
        a. reject >5 MB
        b. strip <script>, <style>, <head>
        c. Locate (tier 1: <table/<tbody/<tr /<div class="text-lg/
                              <div class="rate/class="currency"/
                              class="exchange"/data-currency=)
                              ← co-located within ±5 KB of a currency code
                   tier 2: BaseCurrency + QuoteCurrency literals)
        d. truncate to 80 KB cap
                          ↓
   4. Audit loop (primary client: free Groq openai/gpt-oss-20b)
        for attempt in 1..maxPrimary:
            call AIClient.Complete(systemPrompt, buildUserMessage(src, body, transcript))
            (1) parse JSON; on parse error → append to transcript, retry
            (2) validateRulePatterns: regexp.Compile each method=regex
                pattern; on compile error → "regex did not compile: <err>;
                revise the pattern to comply with RE2 syntax" → retry
            (3) Execute: ApplyRegex / ApplyJSONPath → numeric value
                on no-match → "pattern %q produced no match in body (len=%d)" → retry
            (4) Universal range check: 0 < value < MaxInt32
            (5) Per-pair plausibility check (USD/KZT ∈ [100,1000], etc.)
                on out-of-range → "value %g rejected: outside plausible
                range %g..%g for %s/%s" → retry
            success → persist & return
                          ↓
   5. Audit loop (fallback client: paid OpenAI gpt-5.4)
        same shape, maxFallback attempts (default 2)
        receives FULL transcript of primary failures in user message
                          ↓
   6. all attempts exhausted → return ErrAttemptsExhausted
```

System prompt sections (in order, must stay intact):
- `CONTRACT` — JSON shape and rule chaining semantics
- `BID/ASK SEMANTICS` — multilingual labels + spread heuristic (plan 016)
- `METHOD SELECTION` — when to use `regex` vs `json`
- `RE2 CONSTRAINTS` — forbidden constructs (lookarounds, backrefs, `\u`, `\/`, possessive quantifiers, repeat counts > 1000)
- `POST-PROCESSING` — runtime strips commas/spaces and parses as float
- `FAILURE CONTEXT` — instructs the model to read PREVIOUS ATTEMPTS on retry
- `OUTPUT` — JSON only, no prose

`buildUserMessage` emits:
```
SOURCE: KZ_JUSAN_ASK_USD_KZT
PAIR:   USD/KZT  (ASK)
URL:    https://jusan.kz/exchange-rates
HINT:   <Options.Reserve if set>
BODY (sectioned to N KB around anchor):
<body bytes>

PREVIOUS ATTEMPTS (only on retry):
[1] rule=<json> outcome=<error/value-with-reason>
[2] ...
```

The prompt size guard in `prompt_test.go` keeps the prompt under 4 KB. If you grow it, condense an earlier section (plan 016's engineer condensed RE2 CONSTRAINTS verbose parentheticals — no rules dropped).

## Active source catalog

After all migrations applied (16 production sources, 1 deactivated):

| Name | URL | Path | Cost | Notes |
|---|---|---|---|---|
| `KZ_QAZPOST_BID_USD_KZT` (+ EUR, RUR, ASK variants) | `gateway.prod.qazpost.kz/.../currencies/ops` | JSON 123 B, free Groq, 1 attempt | $0 | 6 rows, all green |
| `KZ_NATIONALBANK_BID_USD_KZT` | `nationalbank.kz/...?query=US%20DOLLAR` | JSON 360 B, free Groq, 1 attempt | $0 | single row |
| `KZ_BCC_FX_*` (24 variants × currency × kind) | `bcc.kz/en/personal/currency-rates/` | HTML 832 KB → 80 KB locate window, gpt-5.4, 1 attempt | ~$0.02 each | tier-1 co-location guard finds the table at byte ~333 K |
| `KZ_HALYK_BID_USD_KZT`, `KZ_HALYK_ASK_USD_KZT` | `halykbank.kz/api/gradation-ccy` | JSON 73 KB, gpt-5.4 fallback (Groq 413s), 1 attempt | ~$0.01 each | seeded with hand-improved branch-agnostic rules |
| `KZ_JUSAN_BID_USD_KZT`, `KZ_JUSAN_ASK_USD_KZT` | `jusan.kz/exchange-rates` | HTML 883 KB → 67 KB, gpt-5.4 fallback, 1 attempt | ~$0.02 each | seeded with main-table rules; LLM-generated rules pointed at secondary widgets |
| `KZ_BCC_BID_USD_KZT` | `bcc.kz/kz/` | DEACTIVATED — Alpine.js interactive widget, needs click actions | n/a | redundant with `KZ_BCC_FX_BID_USD_KZT` |

## Operational runbook

### Run rulegen locally against an existing source

```bash
# .env contains GROQ_API_KEY and OPENAI_API_KEY in plaintext.
# Construct DSN values from them; never echo the keys.
set -a; . ./.env; set +a
KEY_B64_GROQ=$(printf %s "$GROQ_API_KEY" | openssl base64 -A | tr '+/' '-_')
KEY_B64_OAI=$(printf %s "$OPENAI_API_KEY" | openssl base64 -A | tr '+/' '-_')
export AI_PRIMARY_DSN="groq://_:${KEY_B64_GROQ}@api.groq.com/openai/v1?model=openai/gpt-oss-20b&timeout=30s"
export AI_FALLBACK_DSN="openai://_:${KEY_B64_OAI}@api.openai.com/v1?model=gpt-5.4&timeout=120s"
export SQLITEDB_DSN="sqlite://_:_@_:_/./tmp/rulegen-test.db"
export CHROMIUM_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"  # macOS only; Linux uses PATH

./build/rulegen <source-name>                             # full cascade
./build/rulegen --force-fallback <source-name>            # skip Groq primary, go straight to OpenAI
./build/rulegen --max-fallback-attempts=4 <source-name>   # raise fallback budget for stubborn pages
```

Output via `sed` redaction so the base64 keys never reach Claude's context:
```bash
... 2>&1 | sed -E "s/${KEY_B64_GROQ}/<GROQ>/g; s/${KEY_B64_OAI}/<OAI>/g"
```

### Add a new source

1. Insert a row into `rate_sources` (manual SQL or migration). Required columns: `name, title, base_currency, quote_currency, url, interval, kind, active, fetcher_kind`. Leave `rules='[]'` and `rule_metadata='{}'` — rulegen will populate.
2. Run `./build/rulegen <name>` to generate the rule.
3. Inspect with `sqlite3 ./tmp/rulegen-test.db "SELECT rules FROM rate_sources WHERE name='<name>';"`.
4. If LLM-generated rule has fragility (hardcoded dates, brittle class hashes, secondary widgets), hand-improve. See migration `202605.014` for a worked example.
5. Codify the source in a production seed migration when ready.

### Inspect what's in a body before sending to LLM

If a cold-start fails, the diagnostic loop is:
```bash
# Probe the URL into /tmp/probe_*.html, NOT into tmp/testdata/
curl -sSL -A "Mozilla/5.0 ..." "<URL>" -o /tmp/probe_X.html
# Inspect with Python: byte sizes, anchor positions, currency-code counts
python3 - <<'PY'
import re
with open('/tmp/probe_X.html','rb') as f: raw = f.read()
s = re.sub(rb'(?is)<script\b[^>]*>.*?</script>', b'', raw)
s = re.sub(rb'(?is)<style\b[^>]*>.*?</style>', b'', s)
s = re.sub(rb'(?is)<head\b[^>]*>.*?</head>', b'', s)
print(f"raw={len(raw):,} stripped={len(s):,}")
print(f"USD: {len(re.findall(rb'USD', s))}, <table: {len(re.findall(rb'<table', s))}")
PY
```

### Where logs go

`./tmp/logs/rulegen.YYYYMMDD.log` (controlled by `--logs-dir` flag, defaults to `$TMPDIR/logs`). Each invocation writes the full system prompt + user message + AI response + executor outcome.

## Cost ledger (session-to-date)

| Activity | Cost |
|---|---|
| Plans 011–014 smoke-tests across nano/mini/full | ~$0.30 |
| Plan 015 HTTP endpoint local validation | ~$0.05 |
| Plan 016 BID/ASK re-validation + 4-source regression | ~$0.10 |
| Cold-start Jusan + Halyk USD/KZT (BID + ASK × 2) | ~$0.15 |
| **Total spent** | **~$0.60 of $10 OpenAI budget** |

## Plan 017 (architected, NOT implemented)

`plans/017-telegram-regen-command.md` is written and locked. Add a Telegram bot command `/regen <source> [--force-fallback] [--max-fallback=N]` to `cmd/web`'s already-running bot. Key design decisions locked by architect:

1. **Direct dispatch** (not HTTP loopback) — Generator is already in-process.
2. **Move `rulegenLockManager`** from `internal/gateway/httpV1/handlers/` to `internal/application/rulegen/locks.go` so the bot command shares per-source locks with the HTTP handler. Export as `LockManager`.
3. **HTML reply format** matching existing `SendHTMLMessage*` convention.
4. **Send-then-edit pattern**: bot sends "Working on it…" message, then edits to success/failure. Requires adding `SendHTMLMessageReturning` to `TelegramBotClient` (current `SendHTMLMessage` returns only `error`, not `(messageID, error)`).
5. **Background goroutine** with `context.Background()` + 120s timeout — does NOT inherit bot's lifecycle.
6. **Admin gating**: silent ignore for non-admin senders. Telegram has no 403.

Where to slot it in: `internal/application/service/telegramapi.go::handleMessage` (line 65 — next to `/start` and `/subscriptions`). Investigate `internal/infrastructure/telegrambot/tbotclient.go::SendHTMLMessage` and `EditMessageText` (line 159) for the existing API surface.

Tasks (per plan):
1. Investigate existing command dispatch shape (confirmed architect's findings).
2. Move `rulegenLockManager` to `internal/application/rulegen/`.
3. Add `/regen` handler with parser + admin gating + background goroutine + send-then-edit.
4. Help / discoverability — update `/start` or `/help` listing if one exists.
5. Tests: parser, dispatch, lock acquisition.
6. Docs.

Acceptance: `make test` and `make lint` green; live test sending `/regen KZ_QAZPOST_BID_USD_KZT` from admin's Telegram should produce a success reply with value 467.x.

## Known limitations / future plans

1. **`--max-fallback-attempts` default of 2 is tight for complex JSON/HTML.** Jusan ASK and Halyk ASK both required 3+ attempts to converge. Bumping the default to 3 would smooth the operator experience. Out of scope for current work; flag for plan 018+.
2. **Smart-locate doesn't understand JSON.** For Halyk's 73 KB JSON, smart-locate clipped to 41 KB and centered on a non-canonical USD occurrence, which made `"030102"`-anchored regexes fail. The LLM's audit loop recovered (drops the branch anchor and uses tier-stable structure), but a JSON-aware Sanitize that skips locate for `Content-Type: application/json` (or `body[0] == '{'`) would be cleaner.
3. **Alpine.js / interactive widgets are unsupported.** chromedp's `Navigate → WaitVisible → Sleep → OuterHTML` pattern cannot trigger user gestures. The BCC homepage and (likely) Forte require click actions. Would be plan 018+ scope: extend `RateSourceOptions` with `Actions []FetchAction` (ordered click/wait/type pairs).
4. **Per-pair plausibility ranges don't distinguish BID from ASK.** USD/KZT is `[100, 1000]` for both directions. A wrong-column extraction (e.g. BID value captured for ASK source) won't be caught by plausibility alone. Plan 016 mitigated this via prompt-level BID/ASK semantics. A cross-source `ASK > BID` invariant check would be the next belt-and-suspenders layer if needed.
5. **Forte Bank**: page renders 312 KB but has zero currency-code labels — rates are shown via icons/dropdowns. Needs deeper investigation (click actions or a different URL); skip for now.
6. **Bereke Bank** (Sberbank KZ successor): does not publish exchange rates at any standard public URL. No path forward without bank-side cooperation.
7. **CI deploy hook**: `.github/workflows/{stage,prime}.yml` got an `apt install chromium-browser` step in plan 013. Not yet verified end-to-end on a real deploy.

## Memory notes (saved during session)

The following user-preference memories were saved at `~/.claude/projects/-Users-seilbekskindirov-Projects-seilbek-fx-rate-monitor/memory/`:

- `feedback_ask_before_destructive_tmp_ops.md` — don't overwrite curated fixtures (`tmp/testdata/`) or wipe test DBs without asking; scout into `/tmp/probe_*` first.
- `feedback_prefer_chromedp_over_xhr.md` — for SPA sources, default to chromedp behind `fetcher_kind`; do not chase underlying XHR endpoints.

## Continuing in a new Claude session

Likely entry points after reading this handoff:

- **Implement plan 017** — `plans/017-telegram-regen-command.md` is fully spec'd. Spawn `gocode-engineer` against it; reviewer after.
- **New cold-start tests** — adding more banks (Forte if click-actions arrive, Kaspi, Halyk EUR/RUB pairs, etc.). Use the runbook in this doc.
- **Plan 018 candidates** (from "Known limitations" above): JSON-aware smart-locate, bump default `--max-fallback-attempts`, click-action support, cross-source ASK > BID validation.
- **Merge `llm_v2` to main** — when comfortable. Squash-merge per memory rule (`feedback_linear_history_squash.md`). PR title/body format per `feedback_merge_commit_format.md`. Confirm with the user before pushing or opening PR; that's a "shared state" action that requires explicit go.

Verify the state of any plan or code claim against the actual files before acting — this handoff is a point-in-time snapshot and the branch may have moved.
