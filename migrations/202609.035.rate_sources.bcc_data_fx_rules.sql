-- Re-anchor the BCC FX rules on the JSON the page now carries instead of the table it
-- used to render.
--
-- Every one of the 24 KZ_BCC_FX_* sources stopped producing a value after
-- 2026-08-24T04:00:07Z. The fetch itself never broke -- the collector kept getting HTTP 200
-- and a ~1.07 MB body -- but bcc.kz/en/personal/currency-rates/ stopped rendering the rate
-- table server-side. The seeded rules anchor on `<div class="text-lg">CCY</div>` followed by
-- one or two `<div class="text-right">` cells; the page now holds 18 `text-lg` and 2
-- `text-right` occurrences in the whole document, so every rule fails with
-- REGEX_NO_MATCH. Nine of the twelve pairs BCC quotes have no other source -- AED, CAD,
-- CHF, GBP, GOLD, JPY, SILVER, TRY and UZS -- so those went dark outright; only USD, EUR
-- and RUB stayed covered, by Halyk, Jusan, QazPost and the National Bank.
--
-- The rates now arrive as an HTML-entity-escaped JSON document in an Alpine.js attribute:
--
--   <div x-ref="fx" data-fx='{"0":{"currencyPairId":14,...,"currencyMain":"USD",
--     "currencyMinor":"KZT",...,"sell":449.35,"buy":450.95,
--     "lastUpdateDateTime":"14.09.2026 09:05:14","currencyMainName":"USD",...}, ...}'>
--
-- Three things decide the shape of the replacement rules:
--
--   * The anchor is `currencyMain`, not `currencyMainName`. Only the former precedes the
--     prices inside each object; `currencyMainName` (which is where the strings GOLD and
--     SILVER appear) comes after them, and a regex cannot look backwards. The metals are
--     therefore matched by their ISO codes XAU and XAG.
--   * `sell` is BID and `buy` is ASK. The names are the customer's, not the bank's: `sell`
--     is the lower of the two. Verified against the last values we stored -- USD ASK 459.75
--     / BID 458.35 on 2026-08-24 against `buy` 450.95 / `sell` 449.35 live, with the spreads
--     on GOLD and SILVER matching the same way round.
--   * The 600-character bound is measured, not guessed. In the live document the longest
--     anchor-to-value distance is 393 characters (CHF ASK) while the closest two pair
--     objects sit 1232 apart, so a currency missing a price cannot have a neighbour's read
--     in its place.
--
-- No `parse_float` step, unlike the rules migration 202606.008 hardened. That step exists to
-- normalise thousand-separator spaces out of rendered HTML, and it formats with %.3f on the
-- way through -- which is lossy now that the payload is JSON: the page quotes UZS as
-- 0.03367 and RUB as 5.3089, and %.3f stores those as 0.034 and 5.309. JSON numbers need
-- none of that normalisation, and the end of the rule pipeline parses the captured text at
-- full precision. The low-priced pairs therefore gain real precision here, because the old
-- table rendered two decimals where the JSON carries five significant digits.

UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;USD&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_USD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;USD&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_USD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;EUR&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_EUR_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;EUR&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_EUR_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;RUB&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_RUB_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;RUB&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_RUB_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;GBP&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_GBP_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;GBP&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_GBP_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;AED&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_AED_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;AED&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_AED_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;CAD&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_CAD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;CAD&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_CAD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;CHF&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_CHF_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;CHF&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_CHF_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;TRY&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_TRY_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;TRY&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_TRY_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;UZS&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_UZS_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;UZS&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_UZS_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;JPY&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_JPY_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;JPY&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_JPY_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;XAU&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_GOLD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;XAU&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_GOLD_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;XAG&quot;[\\s\\S]{0,600}?&quot;sell&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_BID_SILVER_KZT';
UPDATE rate_sources SET rules = '[{"method":"regex","pattern":"currencyMain&quot;:&quot;XAG&quot;[\\s\\S]{0,600}?&quot;buy&quot;:([0-9.]+)"}]' WHERE name = 'KZ_BCC_FX_ASK_SILVER_KZT';
