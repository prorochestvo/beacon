CREATE TABLE IF NOT EXISTS rate_sources (
	name          TEXT NOT NULL PRIMARY KEY,
	title         TEXT NOT NULL,
	base_currency  TEXT NOT NULL,
	quote_currency TEXT NOT NULL DEFAULT 'KZT',
	url           TEXT NOT NULL,
	interval      TEXT NOT NULL DEFAULT '10m',
	kind          TEXT NOT NULL,
	active        INTEGER NOT NULL DEFAULT 1,
	options       TEXT NOT NULL DEFAULT '{}',
	rules         TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_rate_sources_name ON rate_sources (name);
CREATE INDEX IF NOT EXISTS idx_rate_sources_currency ON rate_sources (base_currency,base_currency);
CREATE INDEX IF NOT EXISTS idx_rate_sources_kind ON rate_sources (kind);
CREATE INDEX IF NOT EXISTS idx_rate_sources_active ON rate_sources (active);
