-- 202607.019: add SpaceX (SPCX) to the Yahoo equity watchlist. SpaceX completed
-- its IPO on 2026-06-12 and trades on Nasdaq under SPCX, so it now has a Yahoo
-- Finance v8 chart endpoint like every other US_YAHOO_LAST_ row. Shares the
-- watchlist's title, interval, options, rules, and fetcher_kind verbatim
-- (see 202607.018). Never edit this file — the filename is immutable once applied.
INSERT OR IGNORE INTO rate_sources (name, title, base_currency, quote_currency, url, interval, kind, active, options, rules, rule_metadata, fetcher_kind) VALUES('US_YAHOO_LAST_SPCX_USD','Yahoo Finance','SPCX','USD','https://query1.finance.yahoo.com/v8/finance/chart/SPCX','6h','LAST',1,'{"headers":{"User-Agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"}}','[{"method":"json","pattern":"chart.result[0].meta.regularMarketPrice"}]','{}','plain');
