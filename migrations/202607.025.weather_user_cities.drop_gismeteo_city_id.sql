-- Rebuild weather_user_cities without the deprecated, always-NULL gismeteo_city_id
-- column (migration 011). SQLite cannot DROP an indexed column in place, and
-- gismeteo eligibility no longer exists, so the column is removed via a full rebuild.
-- No foreign keys reference this table and it has none, so no PRAGMA foreign_keys
-- toggling is needed (and PRAGMA is inert inside the migrator's transaction anyway).
-- Every surviving column is carried forward, including alert_latched / condition_value /
-- last_notified_at (live forced-thaw subscription state).
CREATE TABLE weather_user_cities_new (
    id                TEXT NOT NULL PRIMARY KEY,
    user_type         TEXT NOT NULL,
    user_id           TEXT NOT NULL,
    location_id       TEXT NOT NULL,
    display_name      TEXT NOT NULL DEFAULT '',
    latitude          REAL NOT NULL DEFAULT 0,
    longitude         REAL NOT NULL DEFAULT 0,
    timezone          TEXT NOT NULL DEFAULT '',
    country           TEXT NOT NULL DEFAULT '',
    admin1            TEXT NOT NULL DEFAULT '',
    notify_kind       TEXT NOT NULL DEFAULT 'morning_summary',
    notify_hour       INTEGER NOT NULL DEFAULT 7,
    condition_value   TEXT NOT NULL DEFAULT '',
    last_notified_at  TEXT,
    alert_latched     INTEGER NOT NULL DEFAULT 0,
    updated_at        TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    UNIQUE (user_type, user_id, location_id, notify_kind)
);

INSERT INTO weather_user_cities_new (
    id, user_type, user_id, location_id, display_name,
    latitude, longitude, timezone, country, admin1,
    notify_kind, notify_hour, condition_value, last_notified_at,
    alert_latched, updated_at, created_at
)
SELECT
    id, user_type, user_id, location_id, display_name,
    latitude, longitude, timezone, country, admin1,
    notify_kind, notify_hour, condition_value, last_notified_at,
    alert_latched, updated_at, created_at
FROM weather_user_cities;

DROP TABLE weather_user_cities;

ALTER TABLE weather_user_cities_new RENAME TO weather_user_cities;

CREATE INDEX IF NOT EXISTS idx_weather_user_cities_user        ON weather_user_cities (user_type, user_id);
CREATE INDEX IF NOT EXISTS idx_weather_user_cities_location    ON weather_user_cities (location_id);
CREATE INDEX IF NOT EXISTS idx_weather_user_cities_notify_kind ON weather_user_cities (notify_kind);
