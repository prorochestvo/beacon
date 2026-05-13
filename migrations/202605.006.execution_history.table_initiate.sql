CREATE TABLE IF NOT EXISTS execution_history (
	id          TEXT    NOT NULL PRIMARY KEY,
	source_name TEXT    NOT NULL,
	success    BOOLEAN NOT NULL,
	error      TEXT    NOT NULL DEFAULT '',
	timestamp  INT     NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_execution_history_lookup_latest ON execution_history (source_name, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_execution_history_lookup_errors ON execution_history (source_name, success, timestamp DESC);
