-- Rename the legacy "headless" fetcher_kind to "chromedp".
-- "headless" describes the requirement; "chromedp" names the implementation,
-- which is more honest now that Plan 013 has landed.
-- Affects every row, not just the one seeded BCC entry, by design: if an
-- operator inserted additional "headless" rows after 202605.010, they get
-- migrated consistently.
UPDATE rate_sources SET fetcher_kind = 'chromedp' WHERE fetcher_kind = 'headless';
