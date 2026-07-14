-- Migration 014: add nullable hourly_json column to weather_observations.
-- Stores the Open-Meteo hourly forecast block (time + precipitation_probability +
-- temperature_2m) as a JSON array for rain-alert evaluation. NULL for rows stored
-- before this migration and for Gismeteo observations, which have no hourly data.
ALTER TABLE weather_observations ADD COLUMN hourly_json TEXT;
