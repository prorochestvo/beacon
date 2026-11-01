-- Gismeteo has been removed as a weather data source. This table (migration
-- 017) held the curated Open-Meteo-location-id -> gismeteo-city coverage map;
-- nothing references it any more. No foreign keys point into this table.
DROP TABLE IF EXISTS weather_gismeteo_cities;
