---
name: beacon-forecasting
description: Beacon's rules for rate forecasting and anomaly detection — the Forecaster contract and its newest-first input, which repository query and index to go through, stable Method identifier strings, the shipping bar a new model must clear against baselines, and where anomaly detection lives. Load alongside the knowledge:forecasting skill before writing or reviewing anything in internal/tools/rateforecaster or internal/tools/rateanomaly, or any prediction, backtest or anomaly work over rate history.
---

# Beacon forecasting

Method doctrine — baseline-first, series profiling, walk-forward backtesting, metrics,
leakage traps — comes from the `knowledge:forecasting` skill. Load that too. What follows
is Beacon-specific.

Note that `internal/tools/rateforecaster/` does not exist yet. These are the rules the
package must be built to, not a description of code on disk.

- **Contract**: `Forecaster.Forecast(ctx, []*domain.RateValue) (domain.ForecastResult, error)`
  in `forecaster.go`. `rates` is **newest-first**; return `ErrInsufficientData` when
  `len(rates) < 3`; implementations must be safe for concurrent use. One implementation per
  file alongside `moving_average.go`, `linear_regression.go`, `composite.go`.
- **Data access**: `ObtainLastNRateValuesBySourceName` (`internal/repository/ratevalue.go`);
  query through the compound index `idx_rate_values_lookup`
  (`source_name, base_currency, quote_currency, timestamp DESC`), never bypass it.
- **`ForecastResult.Method` strings** are stable short identifiers that land in logs and DB
  rows (`"moving_average"`, `"linear_regression"`, `"composite"`; new ones like `"ar2"`,
  `"holt_winters"`, `"naive_last"`). Never encode hyperparameters in the string — those are
  struct fields.
- **Shipping bar**: a new model must beat naive last-value **and** the existing
  `MovingAverageForecaster` on the same walk-forward window, or it doesn't ship. For FX,
  weigh directional accuracy alongside error metrics. The backtest ships in the same change,
  as subtests of the implementation's `Test*` function; report the metrics table (new model
  + baselines, same window) in chat — numbers, not adjectives.
- **Profile on real history**: `make backups` pulls production data to
  `./backups/beacon.sqlite` — use it; don't invent synthetic fixtures when real data is on
  disk.
- **Dependencies**: the bar is "gonum can't do this cleanly" (AR(p) fits via
  `gonum/mat.Solve` on the lag design matrix — no regression dep needed). New deps are
  proposed in the plan for explicit approval.
- **Anomaly detection is a separate concern** — a parallel `Detector` in
  `internal/tools/rateanomaly/` (mirroring the rateforecaster layout), not a `Forecaster`
  implementation. Cheapest credible start: residual threshold
  `|observed − predicted| > k·σ` over an existing forecaster.

Reads of rate history span both storage tiers; see the `beacon-storage` skill before
writing the query.
