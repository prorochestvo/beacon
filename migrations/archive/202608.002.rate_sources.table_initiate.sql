-- The archive's copy of rate_sources: what each archived source_name actually is.
--
-- The archive exists to answer questions about history, and a table of prices keyed by
-- an opaque source_name answers only half of one. Two concrete needs:
--
--   * The paged history query groups rows by provider title, which it reads by joining
--     rate_sources. Routing that query to the archive without this table leaves the join
--     with nothing to hit.
--   * Analysis over the full history needs to know a source's pair, kind and origin URL
--     at all, and the hot tier is free to drop a source once it stops being collected.
--
-- The columns are identical to migrations/202605.001 on purpose: the same repository
-- code then runs verbatim against either tier, with no second copy of the SQL to keep
-- in step. The secondary indexes are not mirrored — they serve collector and dashboard
-- filters (active, kind, currency) that never run here, and the join this table exists
-- for resolves through the primary key. Seeds are not mirrored either: rows arrive
-- through the reconciliation pass, which upserts the live set on every tick.
--
-- Rows are never deleted here, including when a source disappears from the hot tier:
-- its archived values outlive it and still need a title.
CREATE TABLE IF NOT EXISTS rate_sources (
    name           TEXT NOT NULL PRIMARY KEY,
    title          TEXT NOT NULL,
    base_currency  TEXT NOT NULL,
    quote_currency TEXT NOT NULL DEFAULT 'KZT',
    url            TEXT NOT NULL,
    interval       TEXT NOT NULL DEFAULT '10m',
    kind           TEXT NOT NULL,
    active         INTEGER NOT NULL DEFAULT 1,
    options        TEXT NOT NULL DEFAULT '{}',
    rules          TEXT NOT NULL DEFAULT '[]',
    rule_metadata  TEXT NOT NULL DEFAULT '{}',
    fetcher_kind   TEXT NOT NULL DEFAULT 'plain'
);
