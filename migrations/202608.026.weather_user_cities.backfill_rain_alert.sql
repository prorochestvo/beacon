-- rain_alert becomes forced and system-managed, one row per tracked city, mirroring the
-- alert_thaw backfill in 202607.021. The default threshold 60 matches
-- weatherDefaultRainThreshold in internal/gateway/httpV1/handlers/me_weather.go, which
-- seeds the same row for cities added after this migration.
--
-- alert_latched is seeded 0 (armed), the OPPOSITE of the thaw backfill, and the difference
-- is load-bearing. rain_alert notifies on both latch edges: entering the condition ("rain
-- expected") and leaving it ("rain cleared"). Seeding 1 (pre-latched) would therefore make
-- every backfilled row emit a spurious "rain cleared" message on the first post-deploy
-- check tick, because no rain in the next 6 hours is the normal state for most cities most
-- of the time. Seeding 0 is safe in the opposite direction: the only message the first tick
-- can produce is a truthful "rain expected" for a city that genuinely has rain forecast
-- within the window.
--
-- INSERT OR IGNORE plus the NOT EXISTS guard make re-application a no-op, and the GROUP BY
-- collapses a city tracked under several notify kinds into exactly one new row.
INSERT OR IGNORE INTO weather_user_cities (
    id, user_type, user_id, location_id, display_name,
    latitude, longitude, timezone, country, admin1,
    notify_kind, notify_hour, last_notified_at,
    condition_value, alert_latched, updated_at, created_at
)
SELECT
    'WUC' || strftime('%Y%m%d%H%M%S','now') || 'Z0R' || upper(hex(randomblob(16))),
    src.user_type, src.user_id, src.location_id, MIN(src.display_name),
    MIN(src.latitude), MIN(src.longitude), MIN(src.timezone),
    MIN(src.country), MIN(src.admin1),
    'rain_alert', 7, NULL,
    '60', 0,
    strftime('%Y-%m-%dT%H:%M:%SZ','now'),
    strftime('%Y-%m-%dT%H:%M:%SZ','now')
FROM weather_user_cities AS src
WHERE src.notify_kind <> 'rain_alert'
  AND NOT EXISTS (
      SELECT 1 FROM weather_user_cities AS t
      WHERE t.user_type   = src.user_type
        AND t.user_id     = src.user_id
        AND t.location_id = src.location_id
        AND t.notify_kind = 'rain_alert'
  )
GROUP BY src.user_type, src.user_id, src.location_id;
