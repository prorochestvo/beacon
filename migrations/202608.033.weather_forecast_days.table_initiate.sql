-- Long-range daily forecast: one row per (location, provider, forecast day).
--
-- Deliberately NOT weather_observations. That table is swept on every collector tick by
-- RemoveWeatherObservationsOlderThan(48h), keyed on captured_at, so a row describing a day
-- two weeks out would be deleted a day and a half after it was written and the view could
-- never show a day more than two out. Retention here is keyed on forecast_date instead: a
-- day is kept until it is in the past.
--
-- Deliberately NOT tiered either. rate_values and execution_history are append-only
-- telemetry and each carry an *_archive twin; this is a bounded working set of
-- locations x 16 rows upserted in place on the natural key below, so there is nothing an
-- archive tier could hold and no reason for a read to union two branches.
--
-- No foreign key to weather_user_cities. A location whose last subscriber leaves simply
-- stops being refreshed and ages out within the horizon; cascading instead would tie the
-- lifetime of public meteorological data to one user's subscription row.
--
-- Units follow the provider and are not interchangeable: rain_sum and precip_sum are
-- millimetres, snowfall_sum is CENTIMETRES.
CREATE TABLE IF NOT EXISTS weather_forecast_days (
    id              TEXT NOT NULL PRIMARY KEY,
    location_id     TEXT NOT NULL,
    provider        TEXT NOT NULL,
    forecast_date   TEXT NOT NULL,
    captured_at     TEXT NOT NULL,
    temp_max        REAL,
    temp_min        REAL,
    rain_sum        REAL,
    snowfall_sum    REAL,
    precip_sum      REAL,
    precip_prob_max INTEGER,
    weather_code    INTEGER,
    UNIQUE (location_id, provider, forecast_date)
);

-- The UNIQUE constraint above already indexes (location_id, provider, forecast_date), which
-- is what the per-city window read rides. This one serves the retention delete, which spans
-- every location at once and would otherwise scan the table.
CREATE INDEX IF NOT EXISTS idx_weather_forecast_days_date ON weather_forecast_days (forecast_date);
