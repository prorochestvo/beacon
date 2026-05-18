-- Plan 'C': seed Jusan + Halyk USD/KZT (BID and ASK).
--
-- Sources were validated via cmd/rulegen cold-start tests against
-- live fetches. The persisted rules came from gpt-5.4 fallback but were
-- hand-improved for production: Halyk BID had a hardcoded date anchor
-- ('date':'2026-05-15') and Jusan rules pointed at non-canonical widgets
-- (card/level). The regexes below anchor on stable structural elements
-- (Halyk: first retail tier value_from_1=0; Jusan: main exchange-rates
-- table CSS-module class-name prefixes) so they survive small page
-- redesigns and currency value updates.
--
-- All four were verified to extract canonical values against live body:
-- Halyk BID buy_rate_1=464.8, ASK sell_rate_1=471.8 (first retail tier),
-- Jusan BID 458.80 ₸ (secondColumn), ASK 478.80 ₸ (thirdColumn).

INSERT OR IGNORE INTO rate_sources (name, title, base_currency, quote_currency, url, interval, kind, active, options, rules, rule_metadata, fetcher_kind) VALUES('KZ_HALYK_BID_USD_KZT','Halyk Bank','USD','KZT','https://halykbank.kz/api/gradation-ccy','6h','BID',1,'{}','[{"method": "regex", "pattern": "\"USD\\\\/KZT\":\\{\"value_from_1\":0,\"value_to_1\":10000,\"sell_rate_1\":[0-9.]+,\"buy_rate_1\":([0-9]+(?:\\.[0-9]+)?)"}]','{}','plain');
INSERT OR IGNORE INTO rate_sources (name, title, base_currency, quote_currency, url, interval, kind, active, options, rules, rule_metadata, fetcher_kind) VALUES('KZ_HALYK_ASK_USD_KZT','Halyk Bank','USD','KZT','https://halykbank.kz/api/gradation-ccy','6h','ASK',1,'{}','[{"method": "regex", "pattern": "\"USD\\\\/KZT\":\\{\"value_from_1\":0,\"value_to_1\":10000,\"sell_rate_1\":([0-9]+(?:\\.[0-9]+)?)"}]','{}','plain');
INSERT OR IGNORE INTO rate_sources (name, title, base_currency, quote_currency, url, interval, kind, active, options, rules, rule_metadata, fetcher_kind) VALUES('KZ_JUSAN_BID_USD_KZT','Jusan Bank','USD','KZT','https://jusan.kz/exchange-rates','6h','BID',1,'{}','[{"method": "regex", "pattern": "USD<!--[\\s\\S]*?KZT</span></div></td><td>[\\s\\S]*?</td><td class=\"[^\"]*secondColumn[^\"]*\"><span><div class=\"LazyLoad\"></div>([0-9]+(?:\\.[0-9]+)?)"}]','{}','plain');
INSERT OR IGNORE INTO rate_sources (name, title, base_currency, quote_currency, url, interval, kind, active, options, rules, rule_metadata, fetcher_kind) VALUES('KZ_JUSAN_ASK_USD_KZT','Jusan Bank','USD','KZT','https://jusan.kz/exchange-rates','6h','ASK',1,'{}','[{"method": "regex", "pattern": "USD<!--[\\s\\S]*?KZT</span></div></td><td>[\\s\\S]*?</td><td class=\"[^\"]*secondColumn[^\"]*\">[\\s\\S]*?</td><td class=\"[^\"]*thirdColumn[^\"]*\"><span><div class=\"LazyLoad\"></div>([0-9]+(?:\\.[0-9]+)?)"}]','{}','plain');
