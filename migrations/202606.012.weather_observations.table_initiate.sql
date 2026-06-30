CREATE TABLE IF NOT EXISTS weather_observations (
	id               TEXT NOT NULL PRIMARY KEY,
	location_id      TEXT NOT NULL,
	provider         TEXT NOT NULL,
	latitude         REAL NOT NULL DEFAULT 0,
	longitude        REAL NOT NULL DEFAULT 0,
	captured_at      TEXT NOT NULL,
	forecast_date    TEXT NOT NULL,
	temp_max         REAL,
	temp_min         REAL,
	precip_sum       REAL,
	precip_prob_max  INTEGER,
	weather_code     INTEGER,
	sunrise          TEXT,
	sunset           TEXT,
	temp_current     REAL,
	temp_feels       REAL,
	humidity         INTEGER,
	wind_speed       REAL,
	wind_dir         INTEGER,
	precip           REAL,
	cloud_cover      INTEGER
);
CREATE INDEX IF NOT EXISTS idx_weather_observations_loc_prov_captured ON weather_observations (location_id, provider, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_weather_observations_loc_prov_date ON weather_observations (location_id, provider, forecast_date);
