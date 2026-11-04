-- alert_thaw fires on TempMax > 0 alone (internal/domain/weatheruser.go,
-- evaluateAlertCondition), which is the default warm-season state for most cities most
-- of the year. Seeding alert_latched = 0 (armed) here would make every backfilled row
-- fire a spurious mass "Thaw" notification on the very first post-deploy check tick,
-- because EvaluateLatched fires on the transition into a met condition when the
-- previous latch was false. Seeding alert_latched = 1 (pre-latched) is strictly safe: a
-- city that genuinely has not thawed yet (TempMax <= 0) re-arms to 0 on the next tick
-- with NO notification either way (EvaluateLatched re-arms on an unmet condition
-- regardless of prevLatched); only a later, real freeze-then-thaw transition fires.
INSERT OR IGNORE INTO weather_user_cities (
    id, user_type, user_id, location_id, display_name,
    latitude, longitude, timezone, country, admin1,
    gismeteo_city_id, notify_kind, notify_hour, last_notified_at,
    condition_value, alert_latched, updated_at, created_at
)
SELECT
    'WUC' || strftime('%Y%m%d%H%M%S','now') || 'Z0T' || upper(hex(randomblob(16))),
    src.user_type, src.user_id, src.location_id, MIN(src.display_name),
    MIN(src.latitude), MIN(src.longitude), MIN(src.timezone),
    MIN(src.country), MIN(src.admin1),
    NULL, 'alert_thaw', 7, NULL,
    '', 1,
    strftime('%Y-%m-%dT%H:%M:%SZ','now'),
    strftime('%Y-%m-%dT%H:%M:%SZ','now')
FROM weather_user_cities AS src
WHERE src.notify_kind <> 'alert_thaw'
  AND NOT EXISTS (
      SELECT 1 FROM weather_user_cities AS t
      WHERE t.user_type   = src.user_type
        AND t.user_id     = src.user_id
        AND t.location_id = src.location_id
        AND t.notify_kind = 'alert_thaw'
  )
GROUP BY src.user_type, src.user_id, src.location_id;
