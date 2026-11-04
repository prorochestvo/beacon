-- The archive twin of rate_values: every value ever recorded, kept forever.
--
-- Same file, same schema, different table. Living in the same database file is what makes
-- the roll-over a plain INSERT...SELECT plus DELETE inside one transaction — SQLite gives
-- no atomicity across attached databases under journal_mode=WAL, so a second file would
-- force a copy-and-verify protocol instead of a move, and with it a permanent duplicate of
-- the hot window.
--
-- One deliberate difference from migrations/202605.002: no foreign key to rate_sources.
-- The archive outlives the sources that produced it, and ON DELETE CASCADE would erase a
-- decade of history the moment somebody removed a dead source. Referential integrity is
-- the hot tier's job; this table is an append-only historical record.
--
-- The indexes mirror the hot table's rather than being trimmed, because this tier is a
-- live read path: every query spans both tiers via UNION ALL, so a predicate that rides an
-- index on one side must ride the same index on the other or the archive branch degrades
-- into a full scan of the largest table in the deployment.
CREATE TABLE IF NOT EXISTS rate_values_archive (
    id              TEXT NOT NULL PRIMARY KEY,
    source_name     TEXT NOT NULL,
    base_currency   TEXT NOT NULL,
    quote_currency  TEXT NOT NULL,
    price           REAL NOT NULL,
    timestamp       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rate_values_archive_lookup
    ON rate_values_archive (source_name, base_currency, quote_currency, timestamp DESC);

-- The roll-over selects by timestamp alone, across all sources, and archive retention
-- (when ever enabled) deletes by the same predicate.
CREATE INDEX IF NOT EXISTS idx_rate_values_archive_timestamp
    ON rate_values_archive (timestamp);
