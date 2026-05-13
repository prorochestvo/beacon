CREATE TABLE IF NOT EXISTS rate_values (
	id             TEXT NOT NULL PRIMARY KEY,
	source_name     TEXT NOT NULL,
	base_currency   TEXT NOT NULL,
	quote_currency  TEXT NOT NULL,
	price          REAL NOT NULL,
	timestamp      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rate_values_lookup ON rate_values (source_name, base_currency, quote_currency, timestamp DESC);
