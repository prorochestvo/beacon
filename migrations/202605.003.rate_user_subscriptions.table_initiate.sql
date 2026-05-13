CREATE TABLE IF NOT EXISTS rate_user_subscriptions (
	id                  TEXT NOT NULL PRIMARY KEY,
	user_type            TEXT NOT NULL,
	user_id              TEXT NOT NULL,
	source_name          TEXT NOT NULL,
 	condition_type       TEXT NOT NULL DEFAULT 'delta',
 	condition_value      TEXT NOT NULL DEFAULT '10',
 	latest_notified_rate  REAL NOT NULL DEFAULT 0,
	updated_at           TEXT NOT NULL,
	created_at           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rate_user_subscriptions_usrSubscriptions ON rate_user_subscriptions (user_type, user_id);
CREATE INDEX IF NOT EXISTS idx_rate_user_subscriptions_userType ON rate_user_subscriptions (user_type);
CREATE INDEX IF NOT EXISTS idx_rate_user_subscriptions_userID ON rate_user_subscriptions (user_id);
CREATE INDEX IF NOT EXISTS idx_rate_user_subscriptions_sourceName ON rate_user_subscriptions (source_name);
