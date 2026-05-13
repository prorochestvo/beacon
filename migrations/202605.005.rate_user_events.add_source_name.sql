ALTER TABLE rate_user_events ADD COLUMN source_name TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_rate_user_events_source ON rate_user_events (source_name);
