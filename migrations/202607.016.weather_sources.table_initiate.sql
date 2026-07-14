-- weather_sources externalises per-provider deploy configuration that previously
-- lived as Go constants (active flag, throttle interval, base URL, User-Agent).
-- Provider is an enum, not a lifecycle-cascading parent: no foreign keys point here,
-- so a stray provider-row deletion never cascade-wipes observations or subscriptions.
-- active is INTEGER because SQLite has no boolean type (matches rate_sources.active).
-- The gismeteo base_url and User-Agent are data tokens copied byte-for-byte from
-- gismeteoBaseURL / gismeteoUserAgent; throttle_interval is a Go duration string
-- ("3h" = DefaultGismeteoThrottleInterval), empty meaning "use the compiled-in default".
-- open-meteo.base_url is intentionally empty: its client has hardcoded endpoints.
CREATE TABLE IF NOT EXISTS weather_sources (
    provider          TEXT NOT NULL PRIMARY KEY,
    title             TEXT NOT NULL DEFAULT '',
    active            INTEGER NOT NULL DEFAULT 1,
    base_url          TEXT NOT NULL DEFAULT '',
    throttle_interval TEXT NOT NULL DEFAULT '',
    options           TEXT NOT NULL DEFAULT '{}'
);

INSERT OR IGNORE INTO weather_sources
    (provider, title, active, base_url, throttle_interval, options)
VALUES
    ('open-meteo', 'Open-Meteo', 1, '', '', '{}'),
    ('gismeteo', 'Gismeteo', 1, 'https://www.gismeteo.kz', '3h',
     '{"user_agent":"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"}');
