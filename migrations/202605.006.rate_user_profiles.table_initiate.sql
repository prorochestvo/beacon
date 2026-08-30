CREATE TABLE IF NOT EXISTS rate_user_profiles (
	user_type   TEXT NOT NULL,
	user_id     TEXT NOT NULL,
	timezone    TEXT NOT NULL,
	locale      TEXT NOT NULL DEFAULT '',
	updated_at  TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	PRIMARY KEY (user_type, user_id)
);
CREATE INDEX IF NOT EXISTS idx_rate_user_profiles_userType ON rate_user_profiles (user_type);
