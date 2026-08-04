-- The archive's execution history: every collector outcome ever recorded, kept forever.
--
-- Same columns as migrations/202605.005, including the Unix-seconds INT timestamp, so
-- the ordinary ExecutionHistoryRepository reads this table verbatim. The two lookup
-- indexes are mirrored because this tier is a live read path, not cold storage: the
-- failed-run view and its unbounded count are answered from here once the hot tier is
-- pruned, and both need the same access patterns the hot tier has.
--
-- The third index has no counterpart in the hot schema. Reconciliation reads
-- MAX(timestamp) on every collector tick to find its resume point; without it that is a
-- full scan of a table that only ever grows, once per tick, forever.
CREATE TABLE IF NOT EXISTS execution_history (
    id          TEXT    NOT NULL PRIMARY KEY,
    source_name TEXT    NOT NULL,
    success     BOOLEAN NOT NULL,
    error       TEXT    NOT NULL DEFAULT '',
    timestamp   INT     NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_execution_history_lookup_latest
    ON execution_history (source_name, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_execution_history_lookup_errors
    ON execution_history (source_name, success, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_execution_history_timestamp
    ON execution_history (timestamp DESC);
