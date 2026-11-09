-- Accept any upper bound on Halyk's first USD/KZT tier.
--
-- https://halykbank.kz/api/gradation-ccy moved the first tier's upper bound from 10000 to
-- 30000, and the seeded rules matched the literal 10000, so both sources have missed since
-- 2026-11-06. The rules still anchor on "value_from_1":0, so they keep reading the
-- smallest-amount tier, and accept a number or null as its upper bound so the next
-- regrading does not break them again. The rules guard keeps a later doctor rulegen fix
-- from being overwritten.
UPDATE rate_sources
SET rules = '[{"method": "regex", "pattern": "\"USD\\\\/KZT\":\\{\"value_from_1\":0,\"value_to_1\":(?:[0-9]+|null),\"sell_rate_1\":([0-9]+(?:\\.[0-9]+)?)"}]'
WHERE name = 'KZ_HALYK_ASK_USD_KZT' AND instr(rules, '\"value_to_1\":10000,') > 0;
UPDATE rate_sources
SET rules = '[{"method": "regex", "pattern": "\"USD\\\\/KZT\":\\{\"value_from_1\":0,\"value_to_1\":(?:[0-9]+|null),\"sell_rate_1\":[0-9.]+,\"buy_rate_1\":([0-9]+(?:\\.[0-9]+)?)"}]'
WHERE name = 'KZ_HALYK_BID_USD_KZT' AND instr(rules, '\"value_to_1\":10000,') > 0;
