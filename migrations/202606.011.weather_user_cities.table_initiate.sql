CREATE TABLE IF NOT EXISTS weather_user_cities (
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
	gismeteo_city_id  INTEGER,
	notify_kind       TEXT NOT NULL DEFAULT 'morning_summary',
	notify_hour       INTEGER NOT NULL DEFAULT 7,
	last_notified_at  TEXT,
	updated_at        TEXT NOT NULL,
	created_at        TEXT NOT NULL,
	UNIQUE (user_type, user_id, location_id, notify_kind)
);
CREATE INDEX IF NOT EXISTS idx_weather_user_cities_user ON weather_user_cities (user_type, user_id);
CREATE INDEX IF NOT EXISTS idx_weather_user_cities_location ON weather_user_cities (location_id);
