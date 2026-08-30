-- weather_gismeteo_cities is the single source of truth for gismeteo coverage,
-- replacing the hand-curated Go gismeteoCities map. It is keyed by the Open-Meteo
-- geocoding id (== weather_user_cities.location_id == LocationKey(geo)) and stores the
-- two components needed to build the forecast URL (<base>/weather-<slug>-<id>/) plus a
-- human label for operators editing by hand. location_id is the PRIMARY KEY: one
-- coverage row per city, not per user. slug and gismeteo_id are data tokens copied
-- verbatim from the former gismeteoCities literal; label is operator-facing prose and
-- must never be used to build the URL. No foreign keys: an empty table is a valid state
-- ("gismeteo coverage disabled"), not an error.
CREATE TABLE IF NOT EXISTS weather_gismeteo_cities (
    location_id  TEXT NOT NULL PRIMARY KEY,
    slug         TEXT NOT NULL,
    gismeteo_id  INTEGER NOT NULL,
    label        TEXT NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO weather_gismeteo_cities (location_id, slug, gismeteo_id, label) VALUES
    ('1526384', 'almaty', 5205, 'Almaty, Kazakhstan'),
    ('1526273', 'astana', 5164, 'Astana, Kazakhstan'),
    ('1518980', 'shymkent', 5324, 'Shymkent, Kazakhstan'),
    ('524901', 'moscow', 4368, 'Moscow, Russia');
