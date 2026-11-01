-- The archive twin of execution_history: every collector outcome ever recorded.
--
-- Same reasoning as rate_values_archive — same file so the roll-over is one atomic
-- transaction, same columns so the ordinary read helpers can span both tiers, same
-- indexes because both tiers answer live queries.
--
-- This table matters more than its row count suggests. The failed-run view reports a count
-- of every failure since the beginning; a hot tier bounded at 180 days cannot produce that
-- number, and a silently smaller one reads as "things got better".
--
-- The timestamp stays INT Unix seconds, matching the hot table rather than the RFC3339 text
-- rate_values uses. The two tiers are compared against each other by the roll-over and
-- unioned by every read, so they have to agree on the storage format.
CREATE TABLE IF NOT EXISTS execution_history_archive (
    id          TEXT    NOT NULL PRIMARY KEY,
    source_name TEXT    NOT NULL,
    success     BOOLEAN NOT NULL,
    error       TEXT    NOT NULL DEFAULT '',
    timestamp   INT     NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_execution_history_archive_lookup_latest
    ON execution_history_archive (source_name, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_execution_history_archive_lookup_errors
    ON execution_history_archive (source_name, success, timestamp DESC);

-- The roll-over and archive retention both select by timestamp alone, across all sources.
CREATE INDEX IF NOT EXISTS idx_execution_history_archive_timestamp
    ON execution_history_archive (timestamp);
