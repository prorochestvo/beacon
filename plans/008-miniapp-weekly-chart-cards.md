# Task Breakdown

## Overview

Enhance the Telegram Mini App subscriptions screen (`cmd/web/static/app/subscriptions.html` driven by `cmd/wasm/`) so every subscription card shows an inline line-chart sparkline for the selected period (default `week`), and expands on tap to reveal the raw `(label, price)` points used to draw it. Add a page-level period toggle (`week` / `month` / `year`) that re-fetches all charts on the current page. Drop page size from 20 to 10.

No new backend endpoints. Each card calls the existing public `GET /api/sources/{name}/rates/chart?period=…` directly; the 10 fetches per page run concurrently as WASM goroutines and the UI fills in per-card as each settles. The page is rendered immediately with skeleton placeholders so the user is not gated behind 10 round-trips.

The line chart is rendered as inline SVG (`<svg><polyline …/></svg>`) — no external libs, compatible with the existing CSP at `subscriptions.html` (`default-src 'self' https://telegram.org`).

## Assumptions

1. `/api/sources/{name}/rates/chart?period=week|month|year` is public (no auth) and already wired through `internal/gateway/httpV1/handlers/handlers.go::GetRatesChart` returning `[]dto.ChartPointResponse{Label string, Price float64}`. Verified.
2. WASM Go runs single-threaded, so per-page state mutations from multiple `go func()` chart-fetch goroutines do not need a mutex *for now* — they only write into their own per-card slot in a map keyed by source name. The existing `MeSubscriptionsPage` already documents this assumption (`me_subscriptions.go` lines 41–46). The new chart-state map sits next to the existing state field and follows the same convention.
3. The `subscriptions.html` CSP already permits inline SVG via `default-src 'self'`. No CSP change required. The chart is plain SVG markup inside the existing innerHTML write — no `<script>` injection.
4. The chart endpoint's `Label` is `YYYY-MM-DD` (week/month) or `YYYY-MM` (year). The expanded list renders the label verbatim — no client-side parsing beyond escape.
5. Concurrency cap of 10 (page size) is acceptable. Browsers will queue beyond the per-host connection limit anyway; the WASM goroutines do not need a semaphore.
6. Tap-to-expand state is per-source-name and lives on the page controller, not in the DOM. The toggle survives redraws of the list div. It does **not** survive a period switch — a period switch resets per-card chart state (collapsed, loading) because the underlying data series changes. Confirmed with the user.
7. The page-level period defaults to `week` on mount and on every screen entry (no localStorage persistence in v1).
8. Charts re-fetch only when the period changes or the page changes (Next/Prev). They do **not** re-fetch on search-text changes, because search changes the visible *card list*, and each card already self-fetches once visible. Re-using cached chart data across searches is out of scope.
9. The new `chartsURL` / `RatesChart` helpers in `cmd/wasm/apiclient/` follow the existing URL-builder convention (`urls.go`) and decoder pattern (`client.go`).
10. The pure rendering and state code under `cmd/wasm/ui/` and `cmd/wasm/application/` stay buildable and testable on the host toolchain (no `syscall/js`). Only `cmd/wasm/main.go` (build-tagged `js && wasm`) wires the goroutines.

## Tasks

### Task 1: Add `RatesChart` to apiclient

- **Description:** Extend `cmd/wasm/apiclient/client.go` with a typed `RatesChart(ctx, name, period) ([]dto.ChartPointResponse, error)` method. Add a `chartURL(name, period string) string` helper in `cmd/wasm/apiclient/urls.go` matching the existing URL-builder style (`url.PathEscape(name)`, `url.Values{}` for query). Add unit tests in `urls_test.go` and `client_test.go`.
- **Acceptance Criteria:**
  - [ ] `chartURL("usd-eur", "week")` returns `/api/sources/usd-eur/rates/chart?period=week`.
  - [ ] `chartURL` URL-encodes the name (`url.PathEscape`) and the period value.
  - [ ] When `period` is empty, `chartURL` omits the query param entirely (let the server default kick in — matches the handler's own default to `week`).
  - [ ] `Client.RatesChart` decodes a `[]dto.ChartPointResponse` body and returns it.
  - [ ] `Client.RatesChart` propagates `Fetcher.FetchJSON` errors verbatim (HTTP-status errors are already surfaced by the fetcher; no special handling needed).
  - [ ] `TestRatesChartURL` covers: happy path, empty period (no query param), period with each of `week|month|year`, name needing escape.
  - [ ] `TestClient_RatesChart` covers: happy path with two points, decode error on invalid JSON, fetcher error propagation.
  - [ ] Godoc on `RatesChart` mirrors the existing `MeSubscriptions` style.
- **Pitfalls:**
  - Do **not** validate the `period` string client-side. The server is the source of truth for accepted values; rejecting unknown periods in the client just duplicates logic and traps callers.
  - URL-builder helpers in `urls.go` are lowercase-named (unexported) — keep the convention.
- **Complexity:** Easy
- **Code Example:**
  ```go
  // chartURL builds the /api/sources/{name}/rates/chart URL. period is left out
  // of the query string when empty so the server can apply its default (week).
  func chartURL(name, period string) string {
      base := "/api/sources/" + url.PathEscape(name) + "/rates/chart"
      if period == "" {
          return base
      }
      v := url.Values{}
      v.Set("period", period)
      return base + "?" + v.Encode()
  }

  // RatesChart fetches aggregated chart data for a source over the given period.
  // period must be one of "week", "month", "year", or "" to let the server default.
  // The endpoint is public — no headers required.
  func (c *Client) RatesChart(ctx context.Context, name, period string) ([]dto.ChartPointResponse, error) {
      raw, err := c.fetcher.FetchJSON(ctx, "GET", chartURL(name, period), nil, nil)
      if err != nil {
          return nil, err
      }
      var out []dto.ChartPointResponse
      if err := json.Unmarshal(raw, &out); err != nil {
          return nil, fmt.Errorf("decode chart points: %w", err)
      }
      return out, nil
  }
  ```

### Task 2: SVG sparkline renderer in `cmd/wasm/ui/chart.go`

- **Description:** Create `cmd/wasm/ui/chart.go` (new file) exposing a pure function `RenderSparkline(points []dto.ChartPointResponse, opts SparklineOptions) string` that returns the inline SVG markup for a polyline. Build-untagged so it runs under both host and js+wasm. Add a sibling `chart_test.go` with table-driven subtests under one `TestRenderSparkline`. The renderer is referenced from the card-render helper introduced in Task 4.
- **Acceptance Criteria:**
  - [ ] File location: `cmd/wasm/ui/chart.go`. Package `ui`. No build tags.
  - [ ] Exported type `SparklineOptions` with at least: `Width int`, `Height int`, `StrokeColor string`, `FillColor string` (optional area-under-line fill — empty string = no fill in v1, polyline only).
  - [ ] `RenderSparkline` signature: `func RenderSparkline(points []dto.ChartPointResponse, opts SparklineOptions) string`.
  - [ ] **Zero points:** returns a placeholder SVG with the same `viewBox` but no `<polyline>` — e.g. an empty centered `<text>` reading "no data" (escaped). Must not panic.
  - [ ] **One point:** renders a horizontal line at the vertical midpoint (no division-by-range needed when range=0).
  - [ ] **All-equal prices (range = 0):** renders a horizontal line at the vertical midpoint. Never divides by zero.
  - [ ] **N≥2 points with variance:** renders `<polyline points="x1,y1 x2,y2 …" />` where x is linearly spaced `[0..Width]` and y is `Height - (price - min) / (max - min) * Height` (SVG y-axis is top-down so we invert).
  - [ ] Output is a self-contained `<svg viewBox="0 0 W H" preserveAspectRatio="none" …>`. The SVG itself must be CSS-styleable from `subscriptions.html` — emit `class="card-chart"` on the root, not inline width/height pixels, so the card CSS controls layout. (Width/Height in `SparklineOptions` define the `viewBox` coordinate system only.)
  - [ ] All string interpolation passes user-controlled values through `dom.Escape`. In practice the only strings interpolated are the `class` attribute (constant) and numeric coordinates (formatted via `strconv.FormatFloat`); the `Label` field is **not** rendered in the SVG (it's rendered separately in the expanded list).
  - [ ] Coordinates use `strconv.FormatFloat(_, 'f', 2, 64)` so the polyline output is stable for golden-string assertions in tests.
  - [ ] `TestRenderSparkline` has subtests, each `t.Parallel()`:
    - `"zero points renders empty placeholder"`
    - `"one point renders horizontal line at midpoint"`
    - `"all equal prices renders horizontal line at midpoint"`
    - `"two points renders ascending diagonal"`
    - `"price range scales to viewBox height"`
    - `"negative prices are handled"` (the API can in principle return negatives; do not assume positive)
    - `"viewBox attribute matches Width and Height options"`
    - `"output contains class=\"card-chart\""`
- **Pitfalls:**
  - SVG y-axis is top-down. Forgetting the `Height - …` inversion gives a chart mirrored upside-down — and the bug is invisible in a single-point test because the line is flat.
  - Float formatting with `%v` or `%g` produces variable-width output (`1` vs `1.000000`) that breaks golden assertions. Use `strconv.FormatFloat(_, 'f', 2, 64)`.
  - Do not embed `<style>` blocks in the SVG; the CSP forbids `style-src` from random origins and the existing stylesheet at `subscriptions.html` is `'unsafe-inline'`-permitted but bloating the SVG is wasted bytes per card. Style with a class.
  - `preserveAspectRatio="none"` is required so the SVG stretches with the card width on resize. Without it the chart letterboxes.
  - Do not call `dom.Escape` on numeric coordinates — that's pointless and obscures the data flow. Escape only attribute values that originate from user-controlled strings (none, in this renderer).
- **Complexity:** Medium
- **Code Example:**
  ```go
  // SparklineOptions controls the SVG viewBox dimensions and stroke/fill of the
  // polyline rendered by RenderSparkline. Width and Height define the viewBox
  // coordinate space only — actual layout size is controlled by the consumer's
  // CSS (class="card-chart").
  type SparklineOptions struct {
      Width       int
      Height      int
      StrokeColor string // CSS color string; empty defaults to "currentColor"
      FillColor   string // unused in v1; reserved for area fill
  }

  // RenderSparkline returns the inline SVG markup for a line chart of points.
  // Edge cases: zero points → placeholder; one point or all-equal prices →
  // horizontal line at midpoint. Never panics.
  func RenderSparkline(points []dto.ChartPointResponse, opts SparklineOptions) string {
      if opts.Width <= 0 {
          opts.Width = 100
      }
      if opts.Height <= 0 {
          opts.Height = 30
      }
      stroke := opts.StrokeColor
      if stroke == "" {
          stroke = "currentColor"
      }

      var b strings.Builder
      fmt.Fprintf(&b, `<svg class="card-chart" viewBox="0 0 %d %d" preserveAspectRatio="none">`,
          opts.Width, opts.Height)

      if len(points) == 0 {
          fmt.Fprintf(&b, `<text x="%d" y="%d" text-anchor="middle" class="card-chart-empty">no data</text>`,
              opts.Width/2, opts.Height/2)
          b.WriteString(`</svg>`)
          return b.String()
      }

      minP, maxP := points[0].Price, points[0].Price
      for _, p := range points[1:] {
          if p.Price < minP {
              minP = p.Price
          }
          if p.Price > maxP {
              maxP = p.Price
          }
      }
      rangeP := maxP - minP

      coords := make([]string, 0, len(points))
      for i, p := range points {
          var x, y float64
          if len(points) == 1 {
              x = float64(opts.Width) / 2
          } else {
              x = float64(i) * float64(opts.Width) / float64(len(points)-1)
          }
          if rangeP == 0 {
              y = float64(opts.Height) / 2
          } else {
              y = float64(opts.Height) - (p.Price-minP)/rangeP*float64(opts.Height)
          }
          coords = append(coords,
              strconv.FormatFloat(x, 'f', 2, 64)+","+strconv.FormatFloat(y, 'f', 2, 64))
      }

      fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="1.5" points="%s"/>`,
          stroke, strings.Join(coords, " "))
      b.WriteString(`</svg>`)
      return b.String()
  }
  ```

### Task 3: Per-page period state and per-card chart state on `MeSubscriptionsPage`

- **Description:** Extend `cmd/wasm/application/me_subscriptions.go` to track the page-level `Period` (default `"week"`) and a per-source-name chart-state map keyed by `SourceName`. Add methods `SetPeriod(ctx, period)`, `LoadChart(ctx, name)`, and `ToggleExpand(name)`. Update `MeSubscriptionsState` with the new fields. `fetchAndStore` clears the chart map and the expanded set on every page change / period change. The pure data flow stays testable under the host toolchain — no `syscall/js`.
- **Acceptance Criteria:**
  - [ ] New type `ChartState` with fields `Loading bool`, `Loaded bool`, `Error error`, `Points []dto.ChartPointResponse`. The three booleans are mutually exclusive in the sense that the UI inspects them in order: error wins, then loaded, then loading.
  - [ ] `MeSubscriptionsState` gains `Period string`, `Charts map[string]ChartState`, `Expanded map[string]bool`. Both maps are non-nil after construction.
  - [ ] `NewMeSubscriptionsPage` initialises `Period = "week"`, allocates both maps empty.
  - [ ] `Period` constants: define `MeSubscriptionsPeriodWeek = "week"`, `MeSubscriptionsPeriodMonth = "month"`, `MeSubscriptionsPeriodYear = "year"` and the default constant `MeSubscriptionsDefaultPeriod = MeSubscriptionsPeriodWeek`. Keep them in the same file. Do not change `MeSubscriptionsPageSize` — see Task 7 for that.
  - [ ] `SetPeriod(ctx, period)` validates the period against the three constants (returns a `PublicError` with message `"Invalid period."` on unknown values — yes, an `internal.PublicError`, because this is a user-visible validation), clears `Charts` and `Expanded` if the period actually changed, updates `Period`, and returns nil. It does **not** itself trigger any fetches — the caller in `main.go` issues `LoadChart` for each visible source in goroutines.
  - [ ] `LoadChart(ctx, name)`:
    - Sets `Charts[name] = ChartState{Loading: true}` synchronously before the fetch.
    - Calls `client.RatesChart(ctx, name, state.Period)`.
    - On success, replaces with `ChartState{Loaded: true, Points: points}`.
    - On error, replaces with `ChartState{Error: err}` and returns the error.
    - Does **not** check whether the chart is already loaded — the caller is responsible. (Period switches and page switches clear the map first, so the no-op cache check would never fire anyway.)
  - [ ] `ToggleExpand(name)` flips `Expanded[name]` (defaulting to `true` when the key is absent). Returns nothing.
  - [ ] `fetchAndStore` (existing): at the end of a successful fetch, clears both `Charts` and `Expanded` to empty maps so the new page starts with all cards collapsed and all charts unloaded. Do the same on error paths to avoid showing stale charts under an error state.
  - [ ] All existing `MeSubscriptionsPage` tests still pass without modification. Add new tests in `me_subscriptions_test.go`:
    - `TestMeSubscriptionsPage_SetPeriod`:
      - `"valid period updates state and clears charts"`
      - `"unchanged period leaves charts intact"` (no-op short-circuit)
      - `"invalid period returns PublicError and does not change state"`
    - `TestMeSubscriptionsPage_LoadChart`:
      - `"happy path stores points and Loaded=true"`
      - `"fetch error stores Error and returns it"`
      - `"loading flag is set before fetch completes"` (use a fetcher that captures the state snapshot on entry)
      - `"second call replaces prior state"` (e.g. error → success)
    - `TestMeSubscriptionsPage_ToggleExpand`:
      - `"absent key sets to true"`
      - `"true flips to false"`
    - `TestMeSubscriptionsPage_NextPage` (extend existing): `"NextPage clears Charts and Expanded"`
- **Pitfalls:**
  - Maps in Go zero-value to nil, not empty. Forgetting to allocate them in `NewMeSubscriptionsPage` produces a nil-map write panic on the first `LoadChart`.
  - Wrapping the `"Invalid period."` validation in a `PublicError` is required by CLAUDE.md (`internal.NewPublicError`). A bare `errors.New` would be incorrect — this is a user-facing message.
  - `SetPeriod` clearing maps on the *same* period would create a flash where loaded charts vanish and re-load. Short-circuit when the value is unchanged.
  - Concurrency: `LoadChart` is called from 10 goroutines simultaneously. Per the existing concurrency note in `me_subscriptions.go` (single-threaded Go-WASM), this is safe today. Document it in the godoc on `LoadChart`. If WASM ever becomes multi-threaded, add a `sync.Mutex` around the map writes — explicitly call this out in the godoc so the future fix is obvious.
  - Do **not** introduce a `nowFn func() time.Time` field on `MeSubscriptionsPage` for any reason (per memory `feedback_no_clock_func_field`). No `time` source is needed here; this is just a hedge against scope creep.
- **Complexity:** Medium
- **Code Example:**
  ```go
  // ChartState is the per-card chart-fetch state. The UI inspects fields in
  // priority order: Error wins (renders an inline error glyph), then Loaded
  // (renders the polyline), then Loading (renders a skeleton placeholder).
  type ChartState struct {
      Loading bool
      Loaded  bool
      Error   error
      Points  []dto.ChartPointResponse
  }

  // SetPeriod updates the page-level period. Returns a PublicError with message
  // "Invalid period." when period is not one of MeSubscriptionsPeriodWeek,
  // MeSubscriptionsPeriodMonth, or MeSubscriptionsPeriodYear. When the period
  // actually changes, all per-card chart and expand state is cleared so the
  // caller can issue fresh LoadChart calls for the visible cards.
  func (p *MeSubscriptionsPage) SetPeriod(_ context.Context, period string) error {
      switch period {
      case MeSubscriptionsPeriodWeek, MeSubscriptionsPeriodMonth, MeSubscriptionsPeriodYear:
      default:
          return internal.NewPublicError("Invalid period.")
      }
      if p.state.Period == period {
          return nil
      }
      p.state.Period = period
      p.state.Charts = map[string]ChartState{}
      p.state.Expanded = map[string]bool{}
      return nil
  }
  ```

### Task 4: UI — render period toggle, card chart slot, expand-on-tap

- **Description:** Extend `cmd/wasm/ui/me_subscriptions.go` to (a) render a period toggle bar above the card list, (b) render a per-card chart slot inside each card with a stable `id` keyed by source name so individual goroutines can update it without re-rendering the whole list, (c) render an expanded section under the chart when `Expanded[name]` is true. Pure rendering — no syscall/js. Use `dom.Escape` for everything that originates from user-controlled data.
- **Acceptance Criteria:**
  - [ ] New helper `renderPeriodToggle(currentPeriod string) string` emits three buttons (`<button data-period="week"…>`) inside a `<div class="period-toggle" id="me-period-toggle">`. The active period gets `class="active"`. Buttons carry `data-period` only (no `data-section`) — the main.go handler matches by element id.
  - [ ] `RenderMeSubscriptions` injects the toggle bar between the search bar and the list wrapper.
  - [ ] `renderMeSubCard` is split: existing inline fields stay; a new `renderChartSlot(item, chartState)` is invoked between `card-time` and `badges`. The slot is wrapped in `<div class="card-chart-slot" id="card-chart-{escapedName}">…</div>` so the main.go goroutine targets it by id.
  - [ ] Chart slot states:
    - `Charts[name]` absent **or** `Loading == true` → renders a CSS-skeleton placeholder div (`<div class="card-chart-skeleton"></div>`), no SVG.
    - `Error != nil` → renders a one-line muted message: `<span class="card-chart-error">chart unavailable</span>`. The actual `Error.Error()` text is **not** shown to keep the UI quiet; the console-log path in main.go carries the full error for debugging.
    - `Loaded == true` → renders the SVG via `RenderSparkline(state.Points, …)`.
  - [ ] When `Expanded[name]` is true and `Loaded == true`, render an expanded list under the chart: `<ul class="card-chart-expanded">…<li><span class="label">{escapedLabel}</span><span class="price">{formatted price}</span></li>…</ul>`. Price formatting uses `%.4f` to match the existing `card-price` formatting.
  - [ ] The card root element carries `data-source-name="{escapedName}"` so the delegated click handler in `main.go` can identify which card was tapped.
  - [ ] **Tap target is the chart slot, not the whole card.** Tapping anywhere on the chart slot toggles expand; tapping elsewhere on the card (e.g. a badge) does nothing. This avoids accidental toggles when the card grows tall and the user is just scrolling.
  - [ ] All escaping is preserved. The XSS test from `me_subscriptions_test.go` (`"XSS payload in source_title is escaped"`) must still pass; add an analogous test for `SourceName` in a card's `data-source-name` attribute.
  - [ ] CSS additions in `cmd/web/static/app/subscriptions.html`:
    - `.period-toggle` (flex row, 8px gap).
    - `.period-toggle button` (existing button color), `.period-toggle button.active` (filled style).
    - `.card-chart-slot` (height ~36px, full width, cursor: pointer).
    - `.card-chart` (display: block, width: 100%, height: 36px).
    - `.card-chart-skeleton` (animated `background-position` shimmer using a CSS gradient — pure CSS, no JS).
    - `.card-chart-error` (muted color).
    - `.card-chart-expanded` (small list, monospace numeric column).
  - [ ] Tests in `me_subscriptions_test.go` (new subtests inside the existing `TestRenderMeSubscriptions`):
    - `"period toggle shows three buttons with current period active"`
    - `"chart slot renders skeleton when Charts entry is missing"`
    - `"chart slot renders skeleton when Loading is true"`
    - `"chart slot renders SVG when Loaded is true"`
    - `"chart slot renders error message when Error is non-nil"`
    - `"expanded list renders label and price for each point"`
    - `"expanded list hidden when Expanded[name] is false"`
    - `"expanded list hidden when chart not yet loaded even if Expanded is true"`
    - `"chart-slot id is escaped source name"` (XSS on `SourceName`)
- **Pitfalls:**
  - The slot id (`card-chart-{name}`) must be unique. Source names are admin-controlled in the DB but are technically free-form strings; collisions with HTML id rules (spaces, quotes) would break `getElementById`. `dom.Escape` is HTML-attribute-safe but not id-safe — verify the existing source-name conventions (kebab-case slugs) hold. If not, switch the id to a hash or use a `data-` attribute lookup instead.
  - The user can switch period while a chart fetch is in-flight. The `LoadChart` goroutine writes into `Charts[name]` regardless of whether the period it was launched for is still current. Mitigation: the period switch in `SetPeriod` clears the map; if a stale fetch finishes after that, it re-populates the map with old data. **Solution:** in `LoadChart`, capture the period at goroutine launch and after the fetch verify `state.Period` still matches before writing. If it doesn't, drop the result on the floor. Document this in Task 6.
  - Skeleton CSS must be standalone — Telegram WebApp theming uses CSS vars (`var(--tg-theme-…-color)`). The shimmer animation must work in both light and dark themes; test in DevTools by overriding `--tg-theme-bg-color`.
- **Complexity:** Medium
- **Code Example:**
  ```go
  func renderPeriodToggle(current string) string {
      var b strings.Builder
      b.WriteString(`<div class="period-toggle" id="me-period-toggle">`)
      for _, p := range []string{
          application.MeSubscriptionsPeriodWeek,
          application.MeSubscriptionsPeriodMonth,
          application.MeSubscriptionsPeriodYear,
      } {
          cls := ""
          if p == current {
              cls = " active"
          }
          fmt.Fprintf(&b, `<button class="period-btn%s" data-period="%s">%s</button>`,
              cls, p, dom.Escape(p))
      }
      b.WriteString(`</div>`)
      return b.String()
  }
  ```

### Task 5: Page size 20 → 10

- **Description:** Drop the default page size from 20 to 10. One line in `cmd/wasm/main.go`. Also update the package-level constant `MeSubscriptionsPageSize` in `cmd/wasm/application/me_subscriptions.go` from 20 to 10 — it is the single source of truth for the default and the `NewMeSubscriptionsPage` zero-value fallback already routes through it.
- **Acceptance Criteria:**
  - [ ] `runRenderMeSubscriptions` in `cmd/wasm/main.go` calls `application.NewMeSubscriptionsPage(client, initData, 10)`.
  - [ ] `MeSubscriptionsPageSize` constant is `10`.
  - [ ] No other call site references `20` as a page-size literal — `grep -rn "NewMeSubscriptionsPage" cmd/` is sanity-checked and the only consumer is `main.go`.
  - [ ] Existing tests in `me_subscriptions_test.go` that construct pages with `10` already use `10`; no changes needed.
- **Pitfalls:**
  - Forgetting one of the two call sites leaves a constant of 10 and a call of 20 — the call wins because `NewMeSubscriptionsPage` only falls back to the constant when the passed value is `<= 0`. Both must be updated, or the constant becomes a dead-code claim.
  - Server-side: confirm `/api/me/subscriptions` does not enforce a minimum page size higher than 10. (It almost certainly doesn't, but worth a one-line check before the engineer assumes.)
- **Complexity:** Easy

### Task 6: Wire goroutines in `main.go` — bind toggle, expand handler, fan out chart fetches

- **Description:** Update `cmd/wasm/main.go::bindMeSubsHandlers` to (a) bind the period-toggle click on `#me-period-toggle` to `page.SetPeriod` + fanout, (b) bind the chart-slot click via delegation on `#me-subs-list` (looking at `closest('.card-chart-slot')`) to `page.ToggleExpand` + slot-only redraw, (c) after `LoadInitial` in `runRenderMeSubscriptions`, fan out one `go func()` per visible card calling `page.LoadChart`. Each chart goroutine, on completion, redraws **only its own slot** by id — not the whole list — so it doesn't trample the search input or other in-flight chart goroutines.
- **Acceptance Criteria:**
  - [ ] After `LoadInitial`, before `bindMeSubsHandlers` returns, a fanout loop launches one goroutine per `page.State().Items` entry calling `page.LoadChart(ctx, item.SourceName)`. Each goroutine then updates the DOM at `#card-chart-{name}` with the new HTML.
  - [ ] Period toggle: clicking a `[data-period]` button calls `page.SetPeriod(ctx, period)`, then on success redraws the whole list (`redrawList()`) and re-fans-out chart fetches for the new period. On `PublicError`, logs `console.warn` with the user-visible message and does nothing else.
  - [ ] Expand handler: delegated click on `#me-subs-list`. Reads `data-source-name` from the nearest ancestor matching `.card-chart-slot` (use `target.closest('.card-chart-slot')` via `js.Value.Call("closest", ".card-chart-slot")`). Calls `page.ToggleExpand(name)` and replaces only the `#card-chart-{name}` slot HTML via a new UI helper `ui.RenderMeSubCardChartSlot(state, name) string`.
  - [ ] **Stale-fetch guard:** every `LoadChart` goroutine captures the period at launch time. Before writing the result into state inside `LoadChart`, the method compares the captured period against the current `state.Period`. Mismatch → return early without mutating state. The UI never sees stale data after a period switch.
  - [ ] Page navigation (NextPage / PrevPage) goroutines, after `redrawList()`, re-fan-out chart fetches for the new page's items.
  - [ ] Search-text change (existing OnSearch flow) re-fans-out chart fetches *only when* the resulting item list changed — but for v1 keep it simple and always re-fan-out after the debounced search settles. The cost is at most 10 fetches per debounce cycle, which is acceptable.
  - [ ] All new `js.Func` allocations from `dom.On` are registered with `scr.addRelease` so the existing unmount cleanup keeps working — no leaked function-table entries.
  - [ ] No new test file. The wiring lives in `//go:build js && wasm`-tagged main.go and is exercised manually in the browser. The pure state methods it calls are covered by Task 3 tests.
- **Pitfalls:**
  - **The stale-fetch guard belongs in `LoadChart` (application layer), not in main.go.** Putting it in main.go means every future caller has to re-implement it. Application owns the invariant; main.go just launches goroutines.
  - `closest()` returns null when there is no match; the WASM wrapper sees a `js.Value` whose `IsNull()` is true. Always guard before calling `.Get("dataset")` on it. A null deref here crashes the WASM runtime, not just the handler.
  - Re-rendering the whole list on every chart resolution destroys focus on the search input. Per-slot redraw via id is mandatory.
  - Race window: if the user mashes Next + period-toggle in quick succession, multiple `LoadChart` goroutines from different generations may be in flight. The stale-fetch guard (period match) handles period changes; page changes are handled because `fetchAndStore` clears `Charts` on success, and stale chart goroutines from the previous page write into a cleared map — the data is incorrect for the new visible items. **Mitigation:** the guard should additionally key on a monotonic generation counter incremented on every `fetchAndStore` / `SetPeriod`. Capture the generation at goroutine launch; mismatch → drop. Easier and more bulletproof than comparing fields one by one.
  - When `SetPeriod` returns a `PublicError`, do **not** show the fallback alert / error UX — this is a developer bug (someone hand-crafted a wrong button), not a user error. `console.warn` is enough.
- **Complexity:** Hard
- **Code Example:**
  ```go
  // fanOutChartFetches issues one goroutine per visible source. Each goroutine
  // calls page.LoadChart and, on completion, redraws only its own slot.
  fanOutChartFetches := func() {
      gen := page.SnapshotGeneration() // see Task 3 amendment: add a generation counter
      for _, item := range page.State().Items {
          name := item.SourceName
          go func() {
              if err := page.LoadChart(ctx, name, gen); err != nil {
                  // Stale generations / fetch errors are logged but the slot
                  // is still re-rendered: LoadChart wrote the Error state.
                  js.Global().Get("console").Call("warn",
                      "chart fetch failed for "+name+":", err.Error())
              }
              slotID := "card-chart-" + name
              slot := doc.Call("getElementById", slotID)
              if !slot.IsNull() && !slot.IsUndefined() {
                  slot.Set("innerHTML", ui.RenderMeSubCardChartSlot(page.State(), name))
              }
          }()
      }
  }
  ```

  Note for Task 3: extend the application contract so `LoadChart(ctx, name, gen int64) error` accepts a generation token, and `MeSubscriptionsPage` exposes `SnapshotGeneration() int64`. `SetPeriod`, `fetchAndStore` (page change), and any future state-resetting operation increment the generation. Tests in Task 3 must cover: `"LoadChart with stale generation does not mutate state and returns nil"`.

### Task 7: CSS skeleton + period toggle styles in `subscriptions.html`

- **Description:** Add the CSS rules enumerated in Task 4 to `cmd/web/static/app/subscriptions.html`. Keep the additions inside the existing single `<style>` block (the CSP permits `style-src 'unsafe-inline'`). Use only Telegram theme CSS variables (`var(--tg-theme-…)`) for colors so the chart is legible in both light and dark themes.
- **Acceptance Criteria:**
  - [ ] `.period-toggle` rule with `display: flex; gap: 8px; margin-bottom: 12px;`.
  - [ ] `.period-toggle button` matches existing `.pagination button` (color, radius, padding) but smaller (e.g. `padding: 4px 12px; font-size: 13px;`).
  - [ ] `.period-toggle button.active` reverses fg/bg for clear active-state indication.
  - [ ] `.card-chart-slot` with `height: 40px; cursor: pointer; margin: 6px 0;`.
  - [ ] `.card-chart` with `display: block; width: 100%; height: 100%; color: var(--tg-theme-button-color, #2196f3);` (so the polyline picks up the theme color via `currentColor`).
  - [ ] `.card-chart-skeleton` with a CSS-only shimmer (`background: linear-gradient(90deg, …); background-size: 200% 100%; animation: shimmer 1.4s infinite ease-in-out;`) and a `@keyframes shimmer` block.
  - [ ] `.card-chart-error` with `color: var(--tg-theme-hint-color, #888); font-size: 11px; font-style: italic;`.
  - [ ] `.card-chart-expanded` with `list-style: none; padding: 6px 0 0; font-size: 12px;` and `.card-chart-expanded li { display: flex; justify-content: space-between; padding: 2px 0; }`.
  - [ ] No new external network requests (no Google Fonts, etc.) — CSP would block them.
- **Pitfalls:**
  - The shimmer animation must be GPU-accelerated (animating `background-position`, not `background`) to keep 60fps on low-end Android in Telegram. The CSS rule above already follows this.
  - `currentColor` on the SVG polyline depends on the parent's `color`. Setting `.card-chart` color also drives the SVG fill of the empty-state `<text>` element; verify both look correct.
- **Complexity:** Easy

### Task 8: Verify `make test` and `make build` are green

- **Description:** After Tasks 1–7 land, run `make test` (which is `go fmt && go vet && go test -race ./...`) and `make build`. Both must pass. Fix any vet, race, or compile issues surfaced. The WASM build (`cmd/wasm/main.go`) is the most likely failure surface because the pure-Go test toolchain does not exercise it.
- **Acceptance Criteria:**
  - [ ] `make test` passes with exit 0.
  - [ ] `make build` produces `./build/web` (with embedded `app.wasm`) without errors.
  - [ ] `go vet ./cmd/wasm/...` passes — note that `go vet` on a `js+wasm`-tagged package requires `GOOS=js GOARCH=wasm`; the Makefile already handles this.
  - [ ] No new entries in `go.mod` (the plan is implementable with the existing dependency set: `encoding/json`, `net/url`, `strconv`, `strings`, `fmt`, plus the existing `internal/dto` and `cmd/wasm/*` packages).
- **Pitfalls:**
  - `go test -race` may surface a data race if the stale-fetch guard is implemented sloppily (e.g. reading `p.state.Generation` without a mutex while a goroutine is writing). In the WASM runtime there's no real concurrency, but the host `go test` toolchain runs the application-layer tests with real goroutines. **Solution:** acknowledge the cross-environment friction upfront. Either (a) keep the application-layer tests single-goroutine (the existing pattern), or (b) add a `sync.Mutex` around state in `MeSubscriptionsPage`. Task 3 recommends (a) — write the new tests to sequentially call `LoadChart` and `SetPeriod` rather than racing them — to avoid introducing a mutex the WASM runtime doesn't need.
- **Complexity:** Easy

## Execution Order

```
Task 1 (apiclient.RatesChart)
  → Task 3 (state changes, depends on Task 1 for client method)

Task 2 (SVG renderer)        ← independent, can run in parallel with Task 1
Task 5 (page size 20→10)     ← independent, trivial
Task 7 (CSS)                 ← independent of the Go changes; can land first

Task 3 (state)               ← depends on Task 1
  → Task 4 (UI render)       ← depends on Task 2 (renderer) and Task 3 (state shape)
    → Task 6 (main.go wiring) ← depends on Tasks 3, 4

Task 8 (verify)              ← last
```

Parallelisable: Tasks 1, 2, 5, 7 can be done concurrently by separate engineer sessions if needed.
Strict sequential chain: 3 → 4 → 6.

## Risks

1. **Stale-fetch ordering bug.** Period and page switches issue new fanouts while old goroutines are still pending. Without the generation guard in Task 3 / Task 6, stale data wins the race and the user sees week-period charts after switching to month. Mitigated by the generation token; the test suite must cover it.
2. **CSP violation.** If the engineer adds any `<script>` or external CSS in the SVG or in `subscriptions.html`, the page silently breaks under Telegram. The existing CSP allows `self` + `telegram.org` only. Mitigation: SVG renderer emits no scripts and no `<style>` blocks; CSS lives in the existing inline `<style>` block.
3. **Concurrency in `make test`.** WASM runs single-threaded but the host test toolchain doesn't. If application-layer tests run goroutines that race the state, `-race` will flag it. Mitigation: keep the new tests sequential (covered in Task 8 pitfalls).
4. **Source-name id collisions / invalid HTML ids.** Source names are admin-controlled strings. A name containing whitespace or special characters would produce an invalid `id="card-chart-…"` attribute. Mitigation: rely on the existing slug convention; if violated, fall back to a hashed id or attribute-selector lookup. Add a comment explaining the assumption in the renderer.
5. **Chart fetches amplify bot load.** Page size of 10 means each Mini App view triggers 10 chart requests. If a power user paginates rapidly, this multiplies. v1 has no debounce or cache. Mitigation: acceptable for the current scale; revisit if `cmd/collector` shows chart-handler latency spikes in prod.
6. **Tap-target confusion with vertical scroll.** Mobile users scrolling the card list may inadvertently trigger expand-on-tap if the tap-vs-scroll discrimination is naive. Mitigation: scoping the tap to `.card-chart-slot` (40px tall) reduces this; if it still bites, add a `touchmove`-cancels-click guard in a follow-up.

## Trade-offs

1. **Inline SVG over canvas:** SVG is text in the DOM, plays well with CSS theming via `currentColor`, and integrates trivially into the existing innerHTML render path. Canvas would be faster for hundreds of charts but the v1 ceiling is 10, so SVG wins on simplicity. Reversible if profiling shows otherwise.
2. **Per-card fetch over batch endpoint:** No new backend route. Each card hits the existing `/rates/chart` endpoint. 10 parallel fetches per page is cheap for the server (SQLite read-only, no auth check) and frees us from defining a new DTO. If the bot scales 10× and chart fetches dominate request count, a batch endpoint becomes the right call — explicitly noted as v2 scope.
3. **Period state at page level, not per card:** A page-wide toggle is one selector, one decision per session. Per-card period would multiply UI surface 10×. Loses fine-grained control; gains UX clarity.
4. **Expanded list shows the same data as the chart, no extra fetch:** The chart endpoint already returns the aggregated points. Re-using them keeps the expanded view consistent with the chart (no risk of label/price mismatch) and avoids a second round-trip. Trade-off: the expanded view shows aggregated bucket prices, not raw individual rates. The user has not asked for raw rates here, only "the same data points as the chart" — confirmed.
5. **Chart fetches run independently of search-text changes:** Re-fetching after every debounced search burns network for no UX gain (the same set of cards might still be visible). v1 takes the simple path: fan-out on every list redraw. v2 could diff items and only re-fetch newly-added ones.
6. **Generation counter over per-fetch context cancellation:** Could also use `context.WithCancel` and cancel old fetches on period switch. Counter is simpler — no context plumbing per goroutine, no leaked goroutines (each one exits naturally once the fetch settles), and the trade-off is exactly one wasted HTTP round-trip per period change, which is fine. If chart fetches ever become expensive enough to matter, swap to context cancellation then.
