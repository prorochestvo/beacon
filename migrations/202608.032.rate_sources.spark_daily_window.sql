-- Widen the batched quote window so it spans a day boundary.
--
-- The batched URL introduced in 202608.031 used the endpoint's default window, which is
-- the current day at five-minute granularity. That works until 00:00 UTC, when the new day
-- has no bars yet: continuously-traded symbols get "close": null rather than an array, and
-- the rule fails with `key "close" is not an array`.
--
-- Observed in production on the first midnight after that migration shipped — exactly five
-- sources, exactly once: BTC, DOGE, ETH, SOL and XRP. The fifteen equities were unaffected
-- because their window still held the previous session; only the round-the-clock symbols
-- roll into an empty day. One in four crypto collections lost, every day.
--
-- range=5d&interval=1d makes the array span several daily bars, so it is never empty at a
-- boundary: at 00:00 the newest element is simply the previous day's close, a few seconds
-- stale instead of a hard failure. Accuracy is unchanged — measured against
-- chart.meta.regularMarketPrice across crypto and equities, the deltas are the same
-- 0.00-0.05% the five-minute window gave. The payload also shrinks, from ~75 points per
-- symbol to ~5.
--
-- The rules are deliberately left alone: close[-1] still means "the newest close", which is
-- the whole reason the index is relative rather than literal.
UPDATE rate_sources
SET url = 'https://query1.finance.yahoo.com/v8/finance/spark?symbols=BTC-USD,DOGE-USD,ETH-USD,SOL-USD,XRP-USD,AAPL,AMD,AMZN,AVGO,COIN,GOOGL,META,MSFT,NFLX,NVDA,PLTR,QQQ,SPCX,SPY,TSLA&range=5d&interval=1d'
WHERE url = 'https://query1.finance.yahoo.com/v8/finance/spark?symbols=BTC-USD,DOGE-USD,ETH-USD,SOL-USD,XRP-USD,AAPL,AMD,AMZN,AVGO,COIN,GOOGL,META,MSFT,NFLX,NVDA,PLTR,QQQ,SPCX,SPY,TSLA';
