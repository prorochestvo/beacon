---
name: beacon-http-api
description: Beacon's HTTP surface and browser client — endpoint contracts that are not obvious from the router code (chart period whitelist, weather city create validation, the forced alert rows and their 409, liveness vs readiness), the content-hashed WASM asset URLs and the nginx location ordering they depend on, and the Mini App's 2x2 screen navigation. Load before adding or changing anything under internal/gateway, cmd/web, cmd/wasm, cmd/web/static, configs/nginx.*, or any /api/me or /api/public route.
---

# Beacon HTTP API and Mini App

Routes are registered in `internal/gateway/` (grep the path literals for the full list);
wire shapes live in `internal/dto`. Only the contracts that are not obvious from that code
are written down here.

## Endpoint contracts

- **Auth** — the `/api/me/*` family is the only authenticated surface. The signed Telegram
  WebApp `initData` is accepted **only** in the `X-Telegram-Init-Data` header, never via
  query string (a signed payload in the URL leaks into access logs and `Referer`). The HMAC
  algorithm is in CLAUDE.md's Key Patterns; implementation in
  `internal/tools/tgwebapp/initdata.go`.
- **Ownership → 404, not 403** — reading or mutating a `/api/me/*` resource (subscription,
  weather city) owned by another user returns **404**, never 403, to avoid existence
  disclosure. Deleting a subscription does **not** cascade-delete its `rate_user_events`
  rows.
- **Chart endpoints** (`/api/me/rates/chart`, `/api/public/rates/chart`) — `period` is an
  integer-days whitelist `{7,30,90,180,360}` (default 7); anything else is 400 with a
  `PublicError` body. Equity (`kind=LAST`) pairs render under the `equity` category with an
  amber series (`#D98E04`).
- **Weather city create** — the server re-validates `timezone` via `time.LoadLocation`,
  `latitude in [-90,90]`, `longitude in [-180,180]`, `notify_hour in [0,23]` (default 7).
- **`GET /`** — dispatcher inline script routes on `window.Telegram.WebApp.initData`:
  non-empty → Mini App view, empty → public view.
- **`GET /ping`** (alias `/healthz`) — liveness, always 200, touches no dependency, no auth.
- **`GET /health/check`** — readiness; runs all inspectors under a 3 s bound, per-component
  report. Critical (`sqlite`, `telegram`) flip `status=false` → HTTP 503; advisory
  (`open-meteo`) appears but never forces 503 (a weather outage must not fail the deploy
  gate). No auth.

## Forced weather subscriptions

`alert_thaw` and `rain_alert` are **forced, system-managed rows**. Creating any city
auto-ensures both for that location, skipping whichever kind the request itself created:
thaw is always upserted, `rain_alert` is inserted at threshold `60` **only when absent**, so
a user-tuned threshold is never stomped.

Deleting one directly (`DELETE /api/me/weather/cities/{id}`) is **409 + PublicError**; the
ownership check runs first, so cross-user or missing stays 404, never 409. Turning them off
means removing the whole location — `DELETE /api/me/weather/locations/{location_id}` deletes
every kind the caller owns there including the forced ones (204; 404 when the caller owns
nothing there, no existence disclosure).

The Mini App reflects this: no per-city thaw row at all — the forced subscriptions are
surfaced once as a single note above the city list — while the rain row IS listed, because
its threshold is user-tunable, but carries no delete control. Each city offers a "Remove
city" control.

## Subscription upserts rewrite settings, never cursors

`notify_hour` and thresholds are rewritten; `last_notified_at` and `alert_latched` are
insert-only. That is what makes the "+ Add alert" form safe to re-use for editing: re-adding
a deleted `morning_summary` (with its 0–23 hour picker) cannot emit a second summary the
same day, and re-adding `rain_alert` with a new percentage — which is how its threshold is
retuned — never re-fires the alert.

## Static asset caching

`app.wasm` (~4 MB) and `wasm_exec.js` are served under content-hashed URLs
(`/app.<8hex>.wasm`, `/wasm_exec.<8hex>.js`) so nginx caches them 7 days immutable. The
8-hex is the first 4 bytes of SHA-256 over the **raw** (uncompressed) bytes, computed at
`cmd/web` boot — hashing raw means a gzip-level change alone doesn't bust the URL.
`cmd/web` rewrites `/app.wasm` and `/wasm_exec.js` to their hashed forms in the in-memory
HTML for `/`, `/index.html`, `/admin/`, `/admin/index.html`. The Go origin serves a
pre-built `app.wasm.gz` sibling with `Content-Encoding: gzip`; nginx gzips `wasm_exec.js`
on the wire.

The nginx regex location in `configs/nginx.beacon_common_settings.conf`
(`^/(app|wasm_exec)\.[a-f0-9]{8}\.(wasm|js)$`) **must** sit above the catch-all
`location /` — nginx evaluates regex locations in source order. Unhashed `/app.wasm` /
`/wasm_exec.js` fall through to `http.FileServer`, so a browser holding stale HTML still
loads the current bytes.

## Frontend

Static assets live in `cmd/web/static/` (embedded via `//go:embed static`); the WASM bundle
builds from `cmd/wasm` (`GOOS=js GOARCH=wasm`) to `cmd/web/static/app.wasm`, sharing
`internal/dto` wire types with the server. `make build` produces it.

The `webAppURL` BotFather setting must point to `https://<host>/` (trailing slash, no path
suffix) — update it whenever the host changes.

**Mini App navigation** — the four authenticated screens form a 2×2 matrix of a **section**
(rates / weather) and a **mode** (view / manage):

| | view (home) | manage (settings) |
|---|---|---|
| **Rates** | `RenderMeSubscriptions` | `RenderMeSubscriptionsEdit` |
| **Weather** | `RenderMeWeatherCurrent` | `RenderMeWeatherCities` |

The vertical section rail (`cmd/wasm/ui/section_rail.go`, wrapped around every screen via
`RenderSectionShell`) changes the **section only**; the manage gear (home) and the ← Back
button (settings) change the **mode only**, so entering or leaving settings always stays in
the section the user was in. Each cell is its own screen mount, so the active tab is implied
by which screen rendered the rail — there is no tab state anywhere. Rail clicks are
delegated from the stable `#app` container, never bound to the rail node: the weather and
editor screens replace `#app` innerHTML on every redraw. Auth failure short-circuits before
the shell, so a screen with no content renders no rail.
