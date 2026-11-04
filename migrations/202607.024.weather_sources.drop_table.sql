-- The per-provider config table (migration 016) bought nothing once gismeteo
-- is gone: Open-Meteo's row was already inert (only `active` mattered) and a
-- missing row already defaulted to active, so "always-on" is what production
-- already did. Open-Meteo is now hardcoded always-on in Go; this table and
-- its whole load-config-and-degrade code path are removed. No foreign keys
-- point into this table.
DROP TABLE IF EXISTS weather_sources;
