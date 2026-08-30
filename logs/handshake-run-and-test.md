# Handoff — run & test on another machine

Handoff from session of 2026-07-03.

- **Branch:** `feat/stock-and-weather-monitoring` (local + remote, in sync).
- **Working tree:** clean.
- **Head commit:** `ded001e test(apiclient): isolate HTTP transport per test`.
- **Tests/build:** `make test` GREEN this session (33 packages, `-race` + WASM);
  `make build` clean. Verified — not assumed.

## Why this handoff exists

The session did two things: (1) fixed the pre-existing flaky apiclient test, and
(2) prepared the branch to be pulled and run on a second machine (the user's Mac).
The user's closing request: *"опиши что было сделано и как запустить чтобы
протестировать на другой машине. (все закомить и запушь в ветку)"* — describe what
shipped and how to run/test elsewhere, then commit & push everything.

Note: this handshake lives in `logs/`, which is **gitignored** (`.gitignore:32`), so
it was force-added (`git add -f`) to make it reachable via `git pull` on the other
machine. The older `logs/handshake-stock-and-weather.md` is NOT tracked (still
gitignored) — its deferred-items detail is folded into this file below.

## What shipped this session

**Flaky-test fix** (commit `ded001e`, plan
`plans/completed/260703.0001.fix-apiclient-transport-flake.md`):

- Symptom: `cmd/wasm/apiclient` tests flaked intermittently under the full parallel
  `go test -race ./...`, most often as `TestClient_Integration_SetSourceActive`, with
  a race stack naming `http.Transport.CloseIdleConnections`.
- Root cause (proven from Go 1.26 stdlib `net/http/httptest/server.go`): tests built
  their fetcher via `NewHTTPFetcher(srv.URL, nil)`; a nil client falls back to the
  process-global `http.DefaultTransport`. All tests are `t.Parallel()`, so they shared
  that one transport, and `httptest.Server.Close()` **unconditionally** calls
  `http.DefaultTransport.CloseIdleConnections()` (server.go ~line 268) — one test
  closing its server raced another's in-flight request. Class-wide, not one test.
- Fix: pass `srv.Client()` at all 11 call sites (`client_integration_test.go` helper +
  10 sites in `fetcher_http_test.go`), giving each test a transport private to its own
  server. Test-only; no production code touched (WASM prod path is `domFetcher`).
- Verification: `-race -count=200 -cpu=8` on the package, `make test`, `make build`,
  and two forced `go test -race -count=1 ./...` full runs — all clean.
- Memory note written (in `~/.claude/.../memory/`, NOT in repo):
  `httptest-nil-client-transport-race`.

**Prior sessions (already on this branch, do NOT undo without user authority):**
stocks (`LAST` kind, AAPL/CCBN keyless sources, Mini App equity chart) and weather
(parallel weather domain, per-user city via Open-Meteo, gismeteo compare, morning
summary, heat/frost/thunderstorm/rain alerts). Shipped plans are in
`plans/completed/` (`260629.0001` … `260701.0001`). Locked decisions and the full
deferred list are documented there and in the prior handshake.

## How to run & test on another machine (the core ask)

Prereqs: **Go 1.26+**, `make`, a Telegram bot token + admin chat id. Build is pure-Go
— no CGO, no system libraries.

```bash
git fetch origin
git checkout feat/stock-and-weather-monitoring     # or: git pull
cp .env.example .env        # then edit — see the two required DSNs below
make run                    # migrate -> collector(once) -> notifier(once) -> web :8080
```

**The one thing that cannot be pulled from git: `.env`.** It is gitignored and holds
the bot token; Claude is forbidden to read or create it. Copy `.env.example` → `.env`
on the target machine and fill in (formats from README, exact punctuation in
`.env.example`):

| Variable | Format |
|---|---|
| `BEACON_SQLITEDB_DSN` (required) | `sqlite://_:_@_:_/<filename>` (a local file, e.g. under `./build/`) |
| `BEACON_TELEGRAMBOT_DSN` (required) | `tbot://<admin_chat_id>:@<bot_token>/` |
| `BEACON_PROXY_URL` (optional) | `socks5://...` or `http://...` |

CLI flags: `--logs-dir` (all), `--verbosity` (all, default `warning`), `--port` (web,
default `8080`), `--timeout` (web), `--static-dir` (web, override embedded dashboard).
`make run` passes `--api-dsn` with a default of `https://localhost/`.

macOS / cross-machine notes:

- `make test` works on macOS out of the box: `-race` there tolerates `CGO_ENABLED=0`
  (Linux/CI needs `CGO_ENABLED=1`). Running it a couple of times is how you confirm
  the flaky fix holds — watch `cmd/wasm/apiclient`.
- **Chromium not needed** to run: the seeded stock/weather sources are keyless
  plain-HTTP/JSON. It is only required for `fetcher_kind='chromedp'` sources
  (`brew install --cask chromium`, or set `BEACON_CHROMIUM_PATH`).
- `collector` and `notifier` are **run-once** (cron-style): `make run` invokes each
  once, then `web` stays up. Re-run `collector` for continuous scraping.
- The Mini App (`/api/me/*`) authenticates via Telegram WebApp initData HMAC and will
  **not** auth outside Telegram. For local poking use `/admin/`, the public endpoints,
  and `/` (guest landing). `/ping` and `/health/check` are open.

## What to do first

**ASK the user which mode they are in before doing anything:**

1. *"Running/testing on the new machine now?"* → walk them through `.env` creation and
   `make run` / `make test`; help translate a `BEACON_SQLITEDB_DSN` to a local file.
   Do NOT try to read or fabricate `.env`.
2. *"Continuing feature work?"* → the deferred menu is unchanged: **wind alert**
   (needs migration `016` + an Open-Meteo daily-wind-max fetch change), **Finnhub**
   (needs an API key — first upstream secret), **PR + deploy** (user previously said
   *"pr пока не делай"* — do NOT open a PR until they confirm the hold is lifted).
   Also open: N+1 in `GET /api/me/weather/current` (accepted debt).

## Hard constraints (this topic)

- **Never read or create `.env`** (secrets). `.env.example` is the safe template.
- `logs/`, `build/`, `tmp/` are gitignored; this handshake was force-added
  deliberately — do not force-add runtime logs.
- **Migrations are immutable** once committed. Next weather migration = `016`; next
  stock migration = `010` (both currently unused/reserved).
- **No new go.mod dependency**, **no secret in `options.headers` / `condition_value`**
  (plaintext in DB + git). Auth-gated sources inject via `BEACON_*` / `dsninjector`.
- All `/api/me/*` use initData HMAC via `X-Telegram-Init-Data` **header only**;
  cross-user access returns **404, not 403**.
- Follow the agent pipeline for non-trivial work (architect plan → engineer → five
  `gocode-reviewer` lenses → fix → one targeted re-review → move plan to completed).
  Flaky/failing tests are `gocode-testdoctor` scope — the five-lens fan-out is not
  required for a test-only fix (that is how `ded001e` was handled).

## House rules (abbreviated)

- Test: `make test` (`go fmt` + `go vet` + `go test -race ./...` + WASM). Lint:
  `make lint`. Build: `make build` (never bare `go build` — outputs to `./build/`).
- Branch policy: on any branch that is not `main`/`stage`/`prod`, commit and push
  freely without asking, each commit atomic/logical.
- Persisted artifacts (commits, plans, docs, comments) are English + professional +
  profanity-free. Chat is Russian + blunt.
- **Full canon: project `CLAUDE.md` and `~/.claude/CLAUDE.md`.**

## Useful invocations

```bash
git checkout feat/stock-and-weather-monitoring && git log --oneline -6
make test                                  # green baseline (fmt+vet+race+WASM)
CGO_ENABLED=1 go test -race -count=1 ./... # force a real full run (defeats cache)
cat .env.example                           # the required env shape (never read .env)
ls plans/completed/260703.*.md             # this session's shipped plan
```

## How to start the next session

Open a fresh Claude session in this repo and send:

> прочти `logs/handshake-run-and-test.md` и продолжаем — скажи, я запускаю на новой машине или продолжаем отложенное (ветер / Finnhub / PR).
