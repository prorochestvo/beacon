-- migration 015: index on weather_user_cities(notify_kind) to speed up
-- ObtainDueWeatherUserCities queries that filter by notify_kind.
CREATE INDEX IF NOT EXISTS idx_weather_user_cities_notify_kind ON weather_user_cities (notify_kind);
