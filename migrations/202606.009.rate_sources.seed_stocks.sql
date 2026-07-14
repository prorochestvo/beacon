-- 202606.009: seed two equity last-price sources (AAPL via Yahoo Finance v8 JSON,
-- CCBN via KASE HTML). Both use kind=LAST. Migration 010 is reserved and unused.
--
-- To rotate a stale User-Agent without breaking filename immutability, add a
-- new numbered migration that updates the row, e.g.:
--   UPDATE rate_sources SET options='{"headers":{"User-Agent":"<new UA>"}}' WHERE name='US_YAHOO_LAST_AAPL_USD';
-- Never edit this file — the filename is immutable once applied to any database.
--
-- The interval='6h' cadence means a delta subscriber gets at most one alert per
-- US trading session. Lower it only if the collector cron ticks more frequently
-- and the Yahoo rate-limit budget allows.
INSERT OR IGNORE INTO rate_sources (name, title, base_currency, quote_currency, url, interval, kind, active, options, rules, rule_metadata, fetcher_kind) VALUES('US_YAHOO_LAST_AAPL_USD','Yahoo Finance','AAPL','USD','https://query1.finance.yahoo.com/v8/finance/chart/AAPL','6h','LAST',1,'{"headers":{"User-Agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"}}','[{"method":"json","pattern":"chart.result[0].meta.regularMarketPrice"}]','{}','plain');
INSERT OR IGNORE INTO rate_sources (name, title, base_currency, quote_currency, url, interval, kind, active, options, rules, rule_metadata, fetcher_kind) VALUES('KZ_KASE_LAST_CCBN_KZT','Kazakhstan Stock Exchange','CCBN','KZT','https://kase.kz/en/investors/shares/CCBN','6h','LAST',1,'{}','[{"method":"regex","pattern":"class=\"last-deal\"[^>]*><div[^>]*class=\"value\"[^>]*>\\s*([0-9][0-9 ,.]*)"}]','{}','plain');
