-- 202606.008: harden a handful of seed extraction rules surfaced by a manual audit.
--   * KZ_BCC_FX_BID_<CCY>_KZT (10 rows, USD/AED/CAD/CHF/EUR/GBP/JPY/RUB/TRY/UZS):
--     widen the numeric capture from ([0-9.]+) to ([0-9][0-9 .]*) so a thousand-separator
--     space (e.g. "1 234.56") no longer turns into a silent REGEX_NO_MATCH; add an
--     explicit parse_float step so the rule declares its normalization intent rather
--     than relying on the end-of-pipeline strip.
--   * KZ_NATIONALBANK_BID_USD_KZT: switch the rule from regex over JSON to method=json
--     with path records[0].amount — anchors to the first record explicitly instead of
--     gambling on the substring "amount" being unique in the response. Note the value
--     of MethodJSONPath in internal/domain/ratesource.go is "json", not "json_path".
--   * KZ_JUSAN_{BID,ASK}_USD_KZT: jusan.kz/exchange-rates 301-redirects to
--     alataucitybank.kz/exchange-rates. The Go http.Client follows it silently today,
--     but Jusan is expected to drop the redirect once their domain migration completes.
--     Point our URL at the final destination directly.
--   * KZ_BCC_BID_USD_KZT: dropped as a functional duplicate of KZ_BCC_FX_BID_USD_KZT.
--     Both express BCC's BID USD/KZT; the former was seeded inactive with a chromedp
--     fetcher against the bcc.kz/kz/ homepage widget, the latter active against the
--     currency-rates page. The active one already covers the pair; keeping the inactive
--     dupe just adds noise to /api/sources and the audit report.
--     This DELETE cascades to rate_values, rate_user_subscriptions and rate_user_events
--     for this source via the FK ON DELETE CASCADE declared in 202605.{002,003,004}.
--     Sole-operator project; no subscriptions on inactive sources are expected.

UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*AED\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_AED_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*CAD\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_CAD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*CHF\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_CHF_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*EUR\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_EUR_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*GBP\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_GBP_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*JPY\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_JPY_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*RUB\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_RUB_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*TRY\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_TRY_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*USD\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_USD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"<div class=\"text-lg[^\"]*\">\\s*UZS\\s*</div>[\\s\\S]{0,400}?<div class=\"text-right\">\\s*([0-9][0-9 .]*)\\s*</div>"},{"method":"parse_float"}]' WHERE name = 'KZ_BCC_FX_BID_UZS_KZT';

UPDATE rate_sources SET rules = '[{"method":"json","pattern":"records[0].amount"}]' WHERE name = 'KZ_NATIONALBANK_BID_USD_KZT';

UPDATE rate_sources SET url = 'https://alataucitybank.kz/exchange-rates' WHERE name IN ('KZ_JUSAN_BID_USD_KZT', 'KZ_JUSAN_ASK_USD_KZT');

DELETE FROM rate_sources WHERE name = 'KZ_BCC_BID_USD_KZT';
