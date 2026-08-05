-- A key/value sidecar for state that belongs to the service rather than to any domain
-- entity. Currently one key: last_vacuum_at.
--
-- VACUUM is what makes the roll-over mean anything on disk. Deleting rows from the hot
-- tables marks their pages free inside the database file, but SQLite never returns those
-- pages to the operating system on its own — without VACUUM the file would keep every byte
-- it has ever grown to, and the whole exercise would show up as exactly zero change in df.
--
-- VACUUM is also expensive: it rebuilds the database into a temporary copy, so it needs
-- transient free space on the order of the file's own size. That is why it is cadence-gated
-- through this table rather than run on every tick, and why the stamp is written only after
-- a successful run — a VACUUM that loses the write lock must retry on the next tick, not
-- skip a whole interval.
CREATE TABLE IF NOT EXISTS service_meta (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
);
