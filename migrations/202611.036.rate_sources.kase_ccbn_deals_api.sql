-- Repoint CCBN from the rendered page to the deals API.
--
-- The page is now an Angular SSR shell and its class="last-deal" markup is gone. The
-- deals API returns a JSON array of the latest deals, newest first, and is reachable from
-- warden with HTTP 200. The pattern is anchored to the first array element, so a newest
-- deal with a null or non-numeric price misses instead of silently yielding an older
-- deal's price. It terminates on [,}] so exponent notation or an unexpected format misses
-- rather than truncating to a plausible-looking number. rule_metadata is reset because the
-- seed metadata described the HTML rule. The url guard keeps a later doctor rulegen fix
-- from being overwritten.
UPDATE rate_sources
SET url = 'https://kase.kz/api/instruments/last-deals/CCBN', rules = '[{"method":"regex","pattern":"^\\s*\\[\\s*\\{[^}]*?\"price\":\\s*([0-9]+(?:\\.[0-9]+)?)[,}]"}]', rule_metadata = '{}'
WHERE name = 'KZ_KASE_LAST_CCBN_KZT' AND url = 'https://kase.kz/en/investors/shares/CCBN';
