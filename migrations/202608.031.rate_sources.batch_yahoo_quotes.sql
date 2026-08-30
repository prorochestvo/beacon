-- Collapse the 20 Yahoo sources onto one batched request.
--
-- Each of them fetched /v8/finance/chart/<SYMBOL> and kept a single number from it, so one
-- collection tick made 20 outbound requests to the same host — 80 a day, against a
-- rate-limited undocumented endpoint. Every other host in the deployment sees four.
--
-- /v8/finance/spark takes a symbol list and answers for all of them at once, keyed by
-- ticker, with no authentication (unlike /v7/finance/quote, which answers 401 without a
-- crumb). Measured: 20 requests and ~8s become 1 request and 0.70s; the price is identical
-- for crypto and within 0.05% for equities, that difference being the lag of the last
-- five-minute bar against the live quote — noise at a six-hour collection interval.
--
-- No fetch-path change is needed. rateextractor caches responses by URL for 30 minutes, so
-- pointing every Yahoo source at the same URL makes them share one fetch automatically;
-- each keeps its own rule and picks its own symbol out of the shared payload.
--
-- MAINTENANCE, and it is a real cost: the symbol list lives in the URL, and the URL has to
-- be identical across the sources for them to share a fetch. Adding a 21st Yahoo source
-- therefore means rewriting all of the URLs, not just adding a row. Getting that wrong
-- degrades rather than breaks — a source with a superset URL simply fetches separately —
-- but it quietly gives back the saving.
--
-- The rule addresses the series by its end. close[] grows through the trading session, so
-- a literal index would name a different moment on every request and eventually a missing
-- one; close[-1] is always the newest point. A gap (upstream writes null for a period with
-- no trade) fails that one source for that one tick rather than reporting a stale price as
-- current, and only that source: they share a fetch, not a rule.
UPDATE rate_sources
SET url = 'https://query1.finance.yahoo.com/v8/finance/spark?symbols=BTC-USD,DOGE-USD,ETH-USD,SOL-USD,XRP-USD,AAPL,AMD,AMZN,AVGO,COIN,GOOGL,META,MSFT,NFLX,NVDA,PLTR,QQQ,SPCX,SPY,TSLA',
    -- Both assignments read the pre-update row, so this still sees the chart URL the
    -- symbol has to be taken from.
    rules = '[{"method":"json","pattern":"' ||
            replace(url, 'https://query1.finance.yahoo.com/v8/finance/chart/', '') ||
            '.close[-1]"}]'
WHERE active = 1
  AND url LIKE 'https://query1.finance.yahoo.com/v8/finance/chart/%';
