-- Per-source alert state for collection health.
--
-- The health signal itself is derived from execution_history and needs no storage. What
-- does need storing is whether the current outage has already been announced: without it
-- the notifier would repeat the same alert on every run, trading silence for noise.
-- Presence of alerted_at is the latch — set on the transition into unhealthy, cleared on
-- the transition back out, untouched in between.
--
-- Kept out of rate_sources deliberately. That table is rewritten wholesale by
-- RetainRateSource, which cmd/doctor rulegen calls after regenerating a source's rules, so
-- runtime state living there is destroyed by an unrelated config write. The forced weather
-- thresholds already hit that and needed a keep-existing workaround in the upsert; a
-- separate table has no such hazard.
--
-- ON DELETE CASCADE because this row describes a source rather than outliving it: deleting
-- the source ends the thing being monitored, and a stale latch for a name that no longer
-- exists could only ever produce a recovery notice for a source nobody has.
CREATE TABLE IF NOT EXISTS rate_source_health (
    source_name TEXT NOT NULL PRIMARY KEY REFERENCES rate_sources(name) ON DELETE CASCADE,
    alerted_at  TEXT NOT NULL
);
