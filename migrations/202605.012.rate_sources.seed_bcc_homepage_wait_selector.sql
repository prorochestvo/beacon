-- Plan 014: set wait_selector on the BCC homepage source so the chromedp
-- fetcher blocks until the rate widget hydrates, instead of relying on a
-- fixed post-body sleep.
--
-- div.text-lg is the structural marker every other seeded BCC source uses
-- to locate the rate row (see 202605.007.rate_sources.seed_initial.sql).
-- The bcc.kz/kz/ homepage reuses the same component on its rate widget;
-- waiting for it ensures the post-hydration DOM contains real rate values.
--
-- json_set replaces the wait_selector key if it already exists, otherwise
-- adds it. Existing options content (e.g. reserve) is preserved.
-- COALESCE guards against any historical NULL options row.
UPDATE rate_sources
SET    options = json_set(COALESCE(options, '{}'), '$.wait_selector', 'div.text-lg')
WHERE  name = 'KZ_BCC_BID_USD_KZT';
