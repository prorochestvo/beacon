-- The archive tier: every rate value ever recorded, kept forever.
--
-- This is a standalone database with its own migration chain, not a copy of the hot
-- schema. Two differences from migrations/202605.001 are deliberate:
--
--   * No foreign key to rate_sources. The archive outlives the sources that produced
--     it — deleting a source must never cascade away its history, which is exactly
--     what ON DELETE CASCADE would do here. Referential integrity is the hot tier's
--     job; the archive is an append-only historical record.
--   * The id carries no ON CONFLICT behaviour of its own, because every write into
--     this table is an INSERT OR IGNORE keyed on the id copied from the hot tier.
--     That is what makes the reconciliation pass idempotent and re-runnable.
--
-- The compound index mirrors idx_rate_values_lookup so a deep-history query costs the
-- same here as it does in the hot tier — the archive is a live read path for windows
-- past the hot horizon, not cold storage.
CREATE TABLE IF NOT EXISTS rate_values (
    id              TEXT NOT NULL PRIMARY KEY,
    source_name     TEXT NOT NULL,
    base_currency   TEXT NOT NULL,
    quote_currency  TEXT NOT NULL,
    price           REAL NOT NULL,
    timestamp       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rate_values_lookup
    ON rate_values (source_name, base_currency, quote_currency, timestamp DESC);

-- The reconciliation pass reads MAX(timestamp) on every collector tick to find its
-- watermark; without this index that is a full scan of the largest table in the
-- deployment, once per tick, forever.
CREATE INDEX IF NOT EXISTS idx_rate_values_timestamp
    ON rate_values (timestamp DESC);
