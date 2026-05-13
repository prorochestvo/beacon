CREATE TABLE IF NOT EXISTS rate_user_events (
	id          TEXT NOT NULL PRIMARY KEY,
	user_type    TEXT NOT NULL,
	user_id      TEXT NOT NULL,
	message     TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'pending',
	sent_at      TEXT,
	last_error   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rate_user_events_status  ON rate_user_events (status);
CREATE INDEX IF NOT EXISTS idx_rate_user_events_user    ON rate_user_events (user_type, user_id);
CREATE INDEX IF NOT EXISTS idx_rate_user_events_created ON rate_user_events (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_rate_user_events_failed ON rate_user_events (created_at DESC) WHERE status = 'failed';
