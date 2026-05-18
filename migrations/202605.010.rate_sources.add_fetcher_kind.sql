ALTER TABLE rate_sources ADD COLUMN fetcher_kind TEXT NOT NULL DEFAULT 'plain';
UPDATE rate_sources SET fetcher_kind = 'headless' WHERE name = 'KZ_BCC_BID_USD_KZT';
