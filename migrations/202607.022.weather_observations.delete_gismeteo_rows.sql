-- Gismeteo has been removed as a weather data source (Open-Meteo is the sole
-- provider). Delete its historical observation rows; the column that
-- discriminates them, weather_observations.provider, is retained as a
-- vestigial constant ('open-meteo') rather than dropped — see the
-- accompanying plan for the rationale (no PK/UNIQUE involvement, two
-- composite indexes stay optimal, zero rebuild cost on the largest weather
-- table). 'gismeteo' is a literal data token matched against the column;
-- keep it byte-for-byte.
DELETE FROM weather_observations WHERE provider = 'gismeteo';
