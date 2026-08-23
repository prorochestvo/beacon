# Task Breakdown

## Overview

Issue #127 asks for a weather view that answers three questions per day — rain or not, snow
or not, above or below 0 °C — over a horizon of several weeks, plus a warning channel for
the same information. Reconnaissance (posted on the issue) established that Open-Meteo's
`v1/forecast` endpoint Beacon already calls returns all four required daily variables for
**16 days**, that the 35-day ensemble is the only honest route past that, and that the
seasonal APIs cannot serve this at all (no daily precipitation aggregate).

The owner settled the four open decisions on 2026-08-21:

1. **16 days now**, with the 35-day ensemble left as a later source swap behind the same
   table rather than a second parallel feature.
2. **Thresholds**: a rain day is `rain_sum ≥ 1.0 mm`, a snow day is `snowfall_sum ≥ 1.0 cm`
   (Open-Meteo returns rain in mm and snowfall in cm — verified live), and the 0 °C axis is
   **three-state**, not binary: above (`min > 0`), crossing (`min ≤ 0 < max`), below
   (`max ≤ 0`).
3. **Notify as well as view.** Far-day forecasts flip before the day arrives, so the
   notification is a **once-per-day digest gated on content change**, not a per-flip alert.
4. **Refresh once a day.**

This plan delivers: a new `weather_forecast_days` table with its own retention, a separate
Open-Meteo request that does not touch the existing decoder, a collector agent on a
daily gate, a `forecast_outlook` notification kind that sends only when the outlook actually
changes, a 16-day strip on the existing weather view screen, and the documentation each of
those needs.

## Assumptions

- **Open-Meteo returns 16 daily rows with the needed variables.** Verified live on
  2026-08-21 for Astana: `forecast_days=16` with
  `daily=temperature_2m_max,temperature_2m_min,rain_sum,snowfall_sum,precipitation_sum,precipitation_probability_max,weather_code`
  returned 16 dates (`2026-08-21` → `2026-09-05`) and `daily_units` of
  `{"rain_sum":"mm","snowfall_sum":"cm","temperature_2m_max":"°C"}`. **Snowfall is
  centimetres, rain is millimetres** — the two thresholds are not the same unit and must not
  be collapsed into one constant.
- **`daily[0]` semantics are preserved by construction, not by care.** The long-range fetch
  is a **new provider method issuing its own HTTP request** (`ForecastRange`); neither
  `OpenMeteo.Forecast` nor `decodeOpenMeteoForecast` is modified. The morning summary and the
  four daily-metric latches keep reading exactly the bytes they read today. The cost is one
  extra request per location per day, which the free tier does not notice (16 days × 7
  variables ≈ 1 weighted call; the budget is 10,000/day).
- **The 48 h sweep cannot reach the new rows.** `RemoveWeatherObservationsOlderThan`
  (`cmd/collector/main.go:158`) deletes from `weather_observations` by `captured_at`. The new
  table is a different table with its own `forecast_date`-keyed retention.
- **The new table is not tiered.** `beacon-storage`'s hot/archive rule governs append-only
  telemetry (`rate_values`, `execution_history`). `weather_forecast_days` is a bounded
  working set — `locations × 16` rows, upserted in place, with past days deleted — so it has
  no archive twin and no `UNION ALL` read. Stating this here so the rule is not
  cargo-culted onto it in review.
- **`rate_sources`' "no runtime state on a config table" rule does not apply**, because
  nothing rewrites `weather_user_cities` rows wholesale: the subscription upsert path
  deliberately preserves `last_notified_at` and `alert_latched` (see `beacon-http-api`,
  "Subscription upserts rewrite settings, never cursors"). The new `notify_state` column
  joins those two under the same rule.
- **Privacy classification.** `weather_forecast_days` is keyed on `location_id` and holds
  public meteorological numbers — no user column at all. `weather_user_cities.notify_state`
  holds a derived signature of that public forecast, and is system-managed dedup state of
  exactly the same nature as the existing `alert_latched` and `last_notified_at`. Neither is
  on the `beacon-data-privacy` off-limits list and neither is identity-adjacent; no policy
  change is needed.
- **The outlook kind is opt-in, not forced.** `alert_thaw` and `rain_alert` are forced onto
  every tracked city; `forecast_outlook` is added by the user like heat and frost. The
  16-day **view**, by contrast, appears for every tracked city, because collection is driven
  by `ObtainDistinctWeatherLocations` and is kind-agnostic. Making the digest forced later is
  a one-line change in `Service.ensureForcedKinds`.
- **The Mini App keeps its 2×2 navigation.** No fifth screen: the strip renders inside the
  existing `RenderMeWeatherCurrent` city card, and the days ride on the existing
  `GET /api/v1/me/weather/current` response so the screen keeps its single round trip.

## Tasks

### Task 1: `weather_forecast_days` table and repository

- Description: add `migrations/202608.033.weather_forecast_days.table_initiate.sql` and
  `internal/repository/weatherforecastday.go`. Columns: `id`, `location_id`, `provider`,
  `forecast_date`, `captured_at`, `temp_max`, `temp_min`, `rain_sum`, `snowfall_sum`,
  `precip_sum`, `precip_prob_max`, `weather_code`. `UNIQUE (location_id, provider,
  forecast_date)` plus an index on `(location_id, provider, forecast_date)` for the range
  read. Repository methods: `RetainWeatherForecastDays` (batch `INSERT … ON CONFLICT DO
  UPDATE` inside one write transaction), `ObtainForecastDays(ctx, locationID, provider,
  fromDate string, limit int)`, `ObtainLatestForecastCapture(ctx, locationID, provider)`, and
  `RemoveForecastDaysBefore(ctx, date string)`. Column and table names go through `const`
  declarations like every other repository file.
- Acceptance Criteria:
  - Re-running the same day's fetch updates rows in place; the row count for one location
    stays at 16 after repeated writes.
  - Writes use `r.db.Transaction`, reads use `r.db.ReadOnlyTransaction`; a whole day's 16
    rows are written in **one** transaction, not sixteen.
  - `ObtainLatestForecastCapture` returns `internal.ErrNotFound` when the location has never
    been fetched.
  - `CheckUP` exists and matches the pattern of the sibling repositories.
- Pitfalls & edge cases: the migration number must be the next free global counter (033 —
  032 is the last applied); an applied filename is immutable. Do not add a foreign key to
  `weather_user_cities` — a location outliving its last subscriber must age out by date, not
  cascade.
- Complexity: Medium

### Task 2: Domain — forecast day, thresholds, and the outlook

- Description: add `internal/domain/weatherforecast.go` with `WeatherForecastDay`, the two
  threshold constants (`WeatherRainDayThresholdMM = 1.0`, `WeatherSnowDayThresholdCM = 1.0`),
  a `WeatherZeroState` enum (`Unknown` / `Above` / `Crossing` / `Below`), the predicates
  `IsRainDay`, `IsSnowDay`, `ZeroState`, and a `WeatherOutlook` built from an ordered day
  slice that exposes `NotableDays()` and `Signature()`.
  - **Notable** means: a rain day, **or** a snow day, **or** a day whose zero-state differs
    from the calendar-previous day's. The third clause is what keeps a Kazakh winter from
    reporting "below zero" on all 15 days: what a reader needs is the day the regime
    *changes*, not the fact that February is cold.
  - The outlook is built over `[today … today+15]` but reports only days **after** today —
    today is present solely as the zero-state baseline for day 1, and today is already
    covered by the morning summary and the existing alerts.
  - `Signature()` is a deterministic, compact, versioned string:
    `o1:<date>:<flags>;<date>:<flags>` where flags are `R`/`S` (in that fixed order) followed
    by one of `+ ~ - ?`. The `o1:` prefix means a future format change re-notifies once
    instead of diffing two incomparable encodings. An outlook with no notable days has
    signature `"o1:"` — non-empty, and therefore distinguishable from `""`, which means
    "never evaluated".
- Acceptance Criteria:
  - A day with `rain_sum = 0.9` is not a rain day; `1.0` is. A day with `snowfall_sum = 0.9`
    is not a snow day; `1.0` is.
  - `ZeroState` returns `Unknown` when either bound is nil, and never guesses.
  - Signature is stable across two calls on equal input and changes when any notable day's
    flags change.
  - Table-driven tests cover: all-clear, rain-only, snow-only, a zero-crossing run
    (above → crossing → below → crossing → above yields exactly the transition days), and
    nil temperature bounds.
- Pitfalls & edge cases: the two thresholds carry different units — a shared constant is a
  bug. Days must be sorted by date before the previous-day comparison; the repository returns
  them ordered, but the domain must not depend on that silently.
- Complexity: Medium

### Task 3: `OpenMeteo.ForecastRange`

- Description: add `ForecastRange(ctx, lat, lng float64) ([]domain.WeatherForecastDay,
  error)` to `internal/infrastructure/weather/openmeteo.go`, requesting
  `daily=temperature_2m_max,temperature_2m_min,rain_sum,snowfall_sum,precipitation_sum,precipitation_probability_max,weather_code`
  with `timezone=auto` and `forecast_days=openMeteoForecastRangeDays` (16), decoded by a new
  `decodeOpenMeteoForecastRange`. It routes through the existing `o.get`, so it inherits the
  5-attempt retry policy, the 429 exclusion and the backoff cap unchanged.
- Acceptance Criteria:
  - `Forecast`, `decodeOpenMeteoForecast` and the existing `forecast_days=2` request are
    **byte-for-byte unmodified**; `git diff` on this task shows only additions to the file.
  - The decoder tolerates short or ragged arrays: a `daily` block whose optional arrays are
    shorter than `time` yields nil pointers for the missing entries rather than panicking.
  - Existing `openmeteo_test.go` cases pass untouched; new cases cover a full 16-day
    payload, an empty `daily` block, and a truncated variable array.
- Pitfalls & edge cases: `timezone=auto` makes `daily.time[i]` a **city-local** calendar
  date; store the string verbatim and never re-derive it from a UTC instant.
- Complexity: Medium

### Task 4: `WeatherForecastAgent` on a once-per-day gate

- Description: add `internal/application/collection/weatherforecastagent.go` — a one-shot
  agent iterating `ObtainDistinctWeatherLocations`, skipping any location whose newest stored
  `captured_at` falls on the **current UTC calendar day**, fetching `ForecastRange` for the
  rest and upserting the 16 rows. Per-location failure isolation and the detached
  `context.Background()` write, both for the same reasons documented on `WeatherAgent`. Run
  ends with an unconditional `RemoveForecastDaysBefore(yesterdayUTC)` and a proof-of-execution
  line: `weather forecast: fetched=… skipped=… failed=… total=…`. Wire it into
  `buildRunners` in `cmd/collector/main.go`.
- Acceptance Criteria:
  - A second `Run` in the same UTC day fetches nothing and logs `skipped` for every location.
  - The first `Run` of a new UTC day fetches every location.
  - One location failing its fetch does not prevent the others from being stored, and the
    joined error names the failing location.
  - Retention deletes `forecast_date < (UTC today − 1 day)` — one day of slack so no city's
    still-current local day is ever deleted out from under it, whatever its UTC offset.
- Pitfalls & edge cases: the gate is a **calendar-day** comparison, not `now.Sub(last) ≥
  24h`; the latter drifts an hour per day against an hourly cron and eventually lands after
  the user's notify hour. Do not fold this into `WeatherAgent` — its hourly throttle and this
  daily one answer different questions, and merging them puts the current-conditions fetch at
  risk for no gain.
- Complexity: Medium

### Task 5: The `forecast_outlook` notification kind

- Description:
  - `migrations/202608.034.weather_user_cities.add_notify_state.sql`:
    `ADD COLUMN notify_state TEXT NOT NULL DEFAULT ''`, plus
    `SetWeatherNotifyState(ctx, id, state)` on the city repository and the field on
    `domain.WeatherUserCity`.
  - `domain.WeatherNotifyForecastOutlook = "forecast_outlook"`: accepted by `Validate` with
    an empty `ConditionValue`, **not** an alert kind (absent from
    `weathercheckagent.go`'s `alertKinds` slice, `UsesForecastDateCap` returns false, no
    `EvaluateLatched` path).
  - A third phase in `WeatherCheckAgent.Run`: load cities of this kind, gate on
    `IsMorningDue` (reusing `NotifyHour` and `last_notified_at`), read the location's forecast
    days, build the outlook, compare `Signature()` with the stored `notify_state`, and queue a
    rendered digest as a `domain.RateUserEvent` only when they differ. On a successful
    enqueue, write `notify_state` and advance `last_notified_at`; when the signature is
    unchanged, advance `last_notified_at` alone.
  - `RenderForecastOutlook(city, outlook, prevSignature)` in `weatherrender.go`, matching the
    existing Telegram HTML style: escaped city name, one line per notable day
    (`Fri 23 Aug — 🌧 1.3 mm · ▲ +22.4 / ▼ +13.6`), a `🆕` marker on days that are new or
    changed against `prevSignature` (suppressed when `prevSignature` is empty, so a first
    digest is not a wall of markers), and a trailing `cleared:` line naming days that dropped
    out.
- Acceptance Criteria:
  - At most **one** digest per city per local day, whatever the tick rate: `last_notified_at`
    advances on every evaluation that had data, not only on a send.
  - An unchanged outlook sends nothing.
  - A city whose `notify_state` is `""` and whose outlook is empty stores the signature and
    sends nothing — a first contact reporting "nothing to report" is noise.
  - An outlook that goes from notable to empty **does** send ("no rain, snow or freezing
    transitions in the next 15 days"), because that is a real change.
  - No forecast rows for the location yet → skip **without** advancing, so the first digest
    fires once collection catches up (same rule the morning-summary phase already follows).
  - A failed enqueue leaves both `notify_state` and `last_notified_at` untouched.
  - Existing morning-summary and alert-phase tests still pass unmodified.
- Pitfalls & edge cases: the window and the "today" baseline are computed in the **city's**
  timezone, not UTC; a `LoadLocation` failure is logged and skipped exactly as the
  morning-summary phase does. The digest must not be added to `alertKinds` — that loop
  evaluates against a single `WeatherObservation` and would mis-handle this kind silently.
- Complexity: Hard

### Task 6: DTO, service and handler — days on the weather view

- Description: add `dto.WeatherForecastDayItem` (`date`, `label`, `temp_max`, `temp_min`,
  `rain_sum`, `snowfall_sum`, `rain`, `snow`, `zero_state`, `weather_code`,
  `condition_emoji`) and hang `Days []WeatherForecastDayItem \`json:"days,omitempty"\`` off
  `WeatherCurrentItem`. `weather.Service.ObtainMeCurrent` loads the forecast days per
  distinct location alongside the observation it already loads; the handler maps them, with
  `label` (`"Fri 23 Aug"`) and `zero_state` (`"above"` / `"crossing"` / `"below"` /
  `""`) resolved **server-side**, matching how `SunriseLocal` is already handled so the WASM
  client needs no tzdata.
- Acceptance Criteria:
  - A city with no stored forecast returns `has_data` as today and simply omits `days`;
    the client renders the card exactly as it does now.
  - The window starts at the city's local today and returns at most 16 entries.
  - A location that errors on the forecast read fails the whole list only for a
    non-`ErrNotFound` error, matching the existing observation-loading contract.
  - No new route: the endpoint stays `GET /api/v1/me/weather/current` under `MePrefix`, so
    the authenticated mount and the 404-not-403 ownership rule are inherited unchanged.
- Pitfalls & edge cases: `days` must be `omitempty` — an always-present empty array would
  change the wire shape for every existing client state.
- Complexity: Medium

### Task 7: The 16-day strip in the Mini App

- Description: render the strip inside `renderWeatherCurrentCard`
  (`cmd/wasm/ui/me_weather_current.go`) as a horizontally scrollable row of day chips —
  weekday/date label, a 🌧 and/or ❄ badge, the zero indicator (`▲` above, `↕` crossing, `▼`
  below), and max/min. Add the matching CSS to the inline stylesheet in
  `cmd/web/static/index.html`, next to the existing `.weather-current-*` block and using the
  same `--tg-theme-*` variables. Add `forecast_outlook` to the alert-kind dropdown, the
  label map and the no-threshold branch in `cmd/wasm/ui/me_weather_cities.go`.
- Acceptance Criteria:
  - Every server string passes through `dom.Escape`; numeric fields render only when their
    pointer is non-nil.
  - A day with neither rain nor snow still renders its chip with the temperature and zero
    indicator — the acceptance criteria require the reader to distinguish precipitation days
    from dry ones, which needs both to be visible.
  - The strip scrolls within the card without widening the page; the section rail and the
    manage gear keep their current geometry.
  - Adding a `forecast_outlook` subscription from the manage screen hides the threshold
    input.
- Pitfalls & edge cases: the weather screen replaces `#app` innerHTML on every redraw, so any
  new interactive control must be delegated from `#app`, never bound to a chip node.
- Complexity: Medium

### Task 8: Documentation

- Description: one line in `CLAUDE.md` for the tripwire that does not announce itself — the
  daily-sweep/tiering distinction of the new table — and the depth into the skills:
  `beacon-collection` gains the daily forecast gate and the separate-request rationale,
  `beacon-storage` gains `weather_forecast_days` and its retention, `beacon-http-api` gains
  the `days` extension and the opt-in-vs-forced distinction for the new kind. Update the
  `dto` godoc listing the notify kinds (three places name them today).
- Acceptance Criteria:
  - `CLAUDE.md` stays under 20k chars (`wc -c` before and after, not an estimate).
  - Every kind list in the codebase that enumerates notify kinds mentions the new one:
    `internal/dto/weather.go` godoc, `routes.go` where relevant, the WASM dropdown.
- Complexity: Easy

## Execution Order

1. Task 1 — table and repository (everything else stores or reads through it)
2. Task 2 — domain types and classification (no dependencies; parallel with 1 in principle)
3. Task 3 — `ForecastRange` (needs Task 2's type)
4. Task 4 — collector agent (needs 1 and 3)
5. Task 5 — notification kind and digest (needs 1, 2 and rows from 4)
6. Task 6 — DTO, service, handler (needs 1 and 2)
7. Task 7 — Mini App strip (needs 6)
8. Task 8 — documentation (last, once the shapes are settled)

## Risks

- **The 16-day tail is not trustworthy and the UI must not pretend otherwise.** Beyond
  roughly day 10 a deterministic run is barely better than climatology. This plan ships a
  badge for all 16 days because the acceptance criteria ask for one; the honest mitigation is
  the ensemble swap behind the same table, and the day-11+ chips should be visually muted
  when that lands. Flagged, not solved.
- **Digest fatigue.** One message per city per day *when something changed* is bounded, but a
  volatile week can still mean seven messages. If that grates, the cheapest dial is to
  restrict notable days to a shorter sub-window (say the next 7) while the view keeps all 16.
- **An extra daily request per location** on a keyless, IP-limited API. At today's scale
  (a handful of locations) this is noise against a 10,000/day budget; at a hundred locations
  it is still noise. It only matters if the ensemble swap multiplies it.
- **`make lint` may not run on this machine** (8 GB Pi, no swap, golangci-lint OOM-killed
  twice at 1.44 GB). Check `free -m` first; fall back to an `s_*` tag, which runs the gate
  against no host.

## Trade-offs

- **A second HTTP request instead of widening the existing one.** Widening `Forecast` to 16
  days and decoding the whole array would save a request and put the day-0 semantics of the
  morning summary and four latches at risk of a silent change. The extra request costs
  approximately nothing and removes the risk entirely rather than guarding against it.
- **Digest over per-day alerts.** A per-day alert needs per-(city, date, kind) latch state
  and still floods when a distant day oscillates. The digest needs one column and is bounded
  at one message per city per day by construction. What it gives up is immediacy: a change at
  14:00 is reported the next morning.
- **Three-state zero axis instead of the binary the issue proposed.** "Above/below" is what
  the issue asked for; "crossing" is the state that actually matters underfoot, and it is
  free to compute. The existing `alert_thaw` keeps its own binary `TempMax`-keyed convention
  for today — the two coexist because they answer different questions, and the plan does not
  touch that evaluator.
- **No new screen.** The strip rides on the existing weather view, keeping the 2×2 navigation
  and one round trip, at the cost of a taller card.
- **No archive tier for the new table.** It is bounded and upserted, so tiering would add a
  union read and a roll-over step to protect against growth that cannot happen.
