---
name: beacon-collection
description: How Beacon's collector reaches upstreams and what it does with the results — per-source proxy opt-in and why direct is the default, batched sources sharing one fetch (the 20 Yahoo rows), the Open-Meteo weather provider with its retry policy and alert edge semantics, the 16-day long-range forecast on its own daily gate and the content-gated outlook digest, and the source-health alerting that reports a source gone silent. Load before touching cmd/collector, internal/tools/rateextractor, internal/application/collection, internal/infrastructure/weather, notification.SourceHealthAgent, any rate_sources row or seed migration, anything involving BEACON_PROXY_URL or options.use_proxy, weather alert kinds, the rain/thaw/heat/frost latches, collection.WeatherForecastAgent, OpenMeteo.Forecast or ForecastRange, or the forecast_outlook notify kind and its notify_state signature.
---

# Beacon collection

Everything the collector fetches, and what it does with the answer.

## Egress: direct by default, per-source opt-in

`cmd/collector` reaches every upstream directly unless a source row opts out. Two levels
must agree before anything is proxied: `BEACON_PROXY_URL` says a proxy exists, and
`rate_sources.options.use_proxy` says *that* source wants it. Absent means direct, so
configuring the variable alone re-routes nothing and opting a source in is a deliberate
per-row act (issue #28). No source is opted in today.

The default is a decision taken on measurement (issue #16), not an omission, and it is
worth not reversing casually:

- **There is no volume to hide.** Four requests a day per Kazakhstani host, four to the
  batched Yahoo endpoint; `gateway.prod.qazpost.kz` publishes its own budget as
  `x-ratelimit-remaining: 199`, against which one is spent.
- **Direct is the least suspicious origin available**: a Kazakhstani host in Astana
  reaching Kazakhstani bank sites, versus a foreign datacenter address that rotates daily —
  the signature of evasion rather than of a client.
- **One bank already treated the two differently.** `halykbank.kz` answers `server: nginx`
  direct and `server: ddos-guard` through the tunnel, reproducibly. The proxy is what put
  collection behind bot protection.
- **It cost availability**: 207 `proxyconnect ... connection refused` in one log window
  killed the proxied sources until `vpntunnel` came back. **And latency**, 2.9× to 12.6×
  depending on host.

`cmd/doctor` still honours the proxy unconditionally: it talks to AI providers, which is a
different question with different exposure.

Two things stay direct regardless of the flag. **Chromedp** takes its proxy as a
browser-launch argument on one Chromium subprocess shared by the whole tick, so it cannot
vary per source — `NewRateAgent` therefore passes it an empty proxy URL on purpose, or a
configured variable would silently re-route every chromedp source at once. (There are no
active chromedp sources; all 56 are `plain`.) **Weather** is one global keyless provider,
not a source with rules, and `cmd/web`'s health inspector probes it directly.

A source that asks for the proxy when none is configured **falls back to direct and logs
it** rather than failing: a missing environment variable should not become a data gap for
a source that reaches its host either way.

## Batched sources share one fetch

`rateextractor` caches responses for 30 minutes, so several sources pointing at the *same*
URL cost one outbound request between them, each picking its own value out of the shared
payload with its own rule. The 20 Yahoo sources use this: they all fetch
`/v8/finance/spark?symbols=<all 20>&range=5d&interval=1d` and select `<TICKER>.close[-1]`,
turning 20 requests a tick into one — 80 a day into four, matching every other host.
(`/v7/finance/quote` would be the tidier endpoint but answers 401 without a crumb.)

**`range=5d&interval=1d` is load-bearing, not decoration.** The endpoint's default window
is the current day, which at 00:00 UTC has no bars yet: round-the-clock symbols come back
with `"close": null` and the rule fails. That cost the five crypto sources one collection
in four, every day, until it was found in production. A multi-day window is never empty at
a boundary — the newest element is simply the previous day's close — and the price is
unchanged either way.

Two consequences before touching those rows. The symbol list lives in the URL and the URLs
must be **identical** to share a fetch, so adding a Yahoo source means rewriting all of
them, not adding a row — getting it wrong degrades rather than breaks (the odd one out
simply fetches separately) but silently gives the saving back. And the rule addresses the
series by its end (`close[-1]`, negative indices count backwards) because the array grows
through the session; a literal index would name a different moment on every request.
`ApplyJSONPath` accepts hyphens in keys for the same reason — `BTC-USD` is a key, not a
subtraction.

## The fetch key

The **response cache and the failure tombstone are keyed on URL *and* route**, not URL
alone. Sources sharing a URL is how batching works, so two of them disagreeing about the
proxy would otherwise be served whichever response landed first — and a direct failure
would tombstone a proxied source that would have succeeded. Headers are still not in the
key: the 20 Yahoo sources share a URL *and* their headers, so that collision stays benign.
Add headers to the key before creating a pair that disagrees.

Sources needing a custom `User-Agent` or other header overrides store them in
`RateSourceOptions.Headers` (the `options` JSON column), applied per-request by the plain
fetcher — the same column `UseProxy` lives in. Never put secrets there: the values are
plaintext in the database and in git-tracked migration files.

## Weather: Open-Meteo

Open-Meteo (`domain.ProviderOpenMeteo`) is the sole weather provider: global, keyless
JSON, hardcoded always-on (no `active` toggle, no per-provider config row). The collector
fetches it per tick for every distinct subscribed location, throttled by
`collection.DefaultWeatherThrottleInterval` per location. `weather_observations.provider`
is a retained vestigial column (it partitions two composite indexes) that now only ever
holds `'open-meteo'`; it was kept rather than dropped to avoid a rebuild of the largest
weather table for zero functional gain.

Weather collection is direct, like everything else the collector fetches. The Open-Meteo
inspector in `cmd/web` has always probed direct, so the two agree; a false "down" there
still cannot fail the deploy gate, because the inspector is advisory.

**Transient failures are retried inside the client.** `OpenMeteo.get` — the single seam
both `Forecast` and `Geocode` pass through — re-sends up to `openMeteoMaxAttempts` (5) on
5xx and connection-level faults, with a jittered backoff that honours `ctx` so a cancelled
tick stops instead of sleeping. Without it each intermittent 5xx dropped that location for
the whole run (167 of them in one production log). **429 is deliberately not retried**: the
API is keyless and limits by IP, so a re-send earns the same refusal and pushes further
into the limit. Neither is any other 4xx — those will not become valid by being sent again.
One log line per retry and per recovery, silent on a clean first attempt, so a degrading
provider stays distinguishable from a healthy one. The `/health/check` inspector has its
own client and deliberately does **not** retry — a retry there would hide the flakiness it
exists to surface.

**The budget was sized on production, and its two halves answer different questions**
(issue #27). *Attempts* recover requests: measured 21% at attempt 2 and 25% at attempt 3,
flat, so each further attempt buys about what the last one did. *Waiting* does not — the
503s arrive in episodes lasting about three hours on one location, and an hour between
hourly ticks does not clear one, which is why `openMeteoRetryBackoffCap` stops the doubling
at 500 ms. Five attempts is not a round number: it is the largest budget whose scheduled
waiting still fits inside `weatherGeoTimeout`, the 5 s deadline the Mini App city search
puts on the *same* client. Raising the budget means moving that deadline in the same change,
and `TestRetryScheduleFitsTheTightestCaller` is what says so out loud.

## The long-range forecast

`WeatherForecastAgent` (collector, its own runner beside `WeatherAgent`) stores 16 daily
rows per subscribed location in `weather_forecast_days`. Everything about it is deliberately
separate from the current-conditions path.

- **A separate HTTP request, not a wider one.** `OpenMeteo.ForecastRange` issues its own
  call; `Forecast` and `decodeOpenMeteoForecast` are untouched. `Forecast` decodes daily
  index `[0]`, and that index *is* today for the morning summary and for `alert_heat`,
  `alert_frost`, `alert_thunderstorm` and `alert_thaw` — all four read `obs.TempMax` /
  `TempMin` / `WeatherCode`. Widening the request or the decode risks shifting what those
  five things mean with nothing to report it. The second request costs about one weighted
  API call per location per day against a budget of 10,000. It still routes through
  `OpenMeteo.get`, so it inherits the retry policy unchanged.
- **The gate is a UTC calendar day, not 24 elapsed hours.** Against an hourly cron, "at
  least 24 h since the last capture" drifts an hour later every day and eventually lands
  after the subscriber's notify hour, so the digest would read a forecast a day older than
  it needed to be. A calendar day pins the fetch to the first tick after midnight UTC.
- **Retention keeps one day of slack.** `RemoveForecastDaysBefore` runs on every tick
  whatever the fetches did, with a cutoff of yesterday UTC: offsets run from −12 to +14, so
  one extra day is what makes "past" unambiguous for every subscriber.
- **The units are not interchangeable.** Open-Meteo reports `rain_sum` in **millimetres**
  and `snowfall_sum` in **centimetres**. A rain day is ≥ 1 mm, a snow day ≥ 1 cm; the two
  thresholds are numerically equal and dimensionally different, so a single shared constant
  is a bug.
- **The bar is not `> 0`.** Models smear small amounts across most days of a long-range run,
  so at any trace above zero nearly every day of a 16-day window comes back wet.

## The outlook digest is content-gated, not latched

`forecast_outlook` is the one notify kind outside the latch model entirely: it is absent
from `alertKinds`, `UsesForecastDateCap` is false for it, and it never reaches
`EvaluateLatched`. A day two weeks out changes its mind several times before it arrives, so
a latch per condition would either send every flip or, with a dead band wide enough to stop
that, say nothing at all.

Instead `WeatherCheckAgent.runOutlookPhase` compares `domain.WeatherOutlook.Signature()`
against the `weather_user_cities.notify_state` column and queues a message only when they
differ. Three properties are load-bearing:

- **The cursor advances on every evaluation that had data**, not only on a send. That is
  what bounds the digest at one message per city per local day *regardless of how often the
  collector refreshes the forecast underneath it* — a guarantee the fetch cadence must not
  be able to take away by changing.
- **An empty signature means "never evaluated"** and is distinct from an evaluated outlook
  with nothing in it, which encodes as the version prefix alone (`o1:`). The distinction is
  what keeps a first digest from opening with "nothing to report".
- **A day is notable if it brings rain, snow, or a change in the freezing regime** relative
  to the last classified day before it. Reporting every cold day would fill a Kazakh winter
  digest with the fact that February is cold; what the reader needs is the day the regime
  turns.

The `o1:` prefix on the signature exists so a future change to the encoding re-notifies
every subscriber exactly once rather than diffing two encodings that do not mean the same
thing. Bump it when the encoding changes meaning.

## Weather alert edge semantics

Every alert kind is edge-triggered through the per-row `alert_latched` boolean, and
`domain.EvaluateLatched` reports the transition as a `domain.AlertEdge`. The four
daily-metric kinds (`alert_heat`, `alert_frost`, `alert_thunderstorm`, `alert_thaw`) notify
on entry only, re-arm silently, and are capped to one notification per `forecast_date`
(`WeatherNotifyKind.UsesForecastDateCap`, cursor in `last_notified_at`).

`rain_alert` is the exception on both counts: it notifies on **both** edges (distinct "Rain
alert" / "Rain cleared" messages) and is exempt from the daily cap, because its metric is a
rolling 6 h window whose two transitions routinely share one `forecast_date`. Its anti-flap
guard is the hysteresis band instead — fires at `maxProb ≥ threshold`, clears only at
`maxProb ≤ max(threshold − 20, 0)`, holds the latch in between.

## Source health alerting

`notification.SourceHealthAgent` (notifier, before the dispatch so a transition detected
this tick is delivered this tick) tells the admin chat when a rate source stops producing
data and when it starts again. Before it, a source could fail on every run for weeks in
silence — the per-run tombstone that stops one dead source burning the whole run also
stopped anyone hearing about it.

- **Health is measured as silence, not as a failure count.** A source that runs and fails
  writes failure rows a counter would catch; a source that stops being attempted at all
  writes nothing, so its counter stays at zero while it is equally dead. `now -
  lastSuccessAt` catches both. The failure count still rides in the message — it is what
  separates "failing loudly" from "no longer running".
- **The window is relative to the source's own `interval`**: `max(DefaultSourceStaleFloor,
  DefaultSourceStaleFactor × interval)` — 18 h at production's uniform 6 h. A fixed run
  count would mean different things per source the moment intervals diverge; the floor
  exists because the collector is cron-driven and cannot attempt anything more often than
  it runs.
- **Edge-triggered via `rate_source_health`** — one message down, one message up, nothing
  in between. Repeating every run would swap an unnoticed silence for an ignored alarm.
  The latch is a **separate table, not a column on `rate_sources`**, because
  `RetainRateSource` rewrites source rows wholesale (`cmd/doctor rulegen` does exactly
  that), and runtime state living there is destroyed by an unrelated config write.
- **Delivery reuses the notification pool**: the event is addressed to
  `tbot.AdminChatID()` and `RateDispatchAgent` already parses `UserID` as a chat id, so an
  operator alert gets the same persistence, retry and failure audit as a user's. Nothing
  gains a Telegram client — least of all the collector, which would be the component
  reporting on its own failure.
- `ObtainSourceCollectionHealth` reads the **hot tier only**, unlike every other query in
  that repository: health is a question about the last few hours against a tier bounded at
  180 days.
- Weather locations are **not** covered: they write no `execution_history`, so there is no
  persisted per-location outcome to measure a gap against.
