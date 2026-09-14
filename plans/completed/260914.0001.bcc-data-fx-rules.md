# Task Breakdown

## Overview

Issue #146: all 24 `KZ_BCC_FX_*` sources stopped producing values after
**2026-08-24T04:00:07Z**. The collector still fetches the page (HTTP 200, ~1.07 MB body) and
`SourceHealthAgent` reported the outage correctly at 2026-08-24T22:00:01Z, so neither
collection nor alerting is at fault: `bcc.kz/en/personal/currency-rates/` stopped rendering
its rate table server-side. The seeded rules anchor on `<div class="text-lg">CCY</div>`
followed by `<div class="text-right">` cells; the live document now holds 18 `text-lg` and 2
`text-right` occurrences in total, and the quotes arrive as an HTML-entity-escaped JSON
document inside an Alpine.js `data-fx` attribute.

Nine of the twelve pairs BCC quotes have no second provider — AED, CAD, CHF, GBP, GOLD,
JPY, SILVER, TRY and UZS — so those went dark outright. Only USD, EUR and RUB stayed
covered, by Halyk, Jusan, QazPost and the National Bank. The owner noticed the metals
because those are the pairs they subscribe to, not because they were the only casualties.

The deliverable is one migration rewriting the `rules` of all 24 rows, plus a test that
compiles those rules against a fixture shaped like the new page — the rules are data, so
nothing else in the build would catch a pattern that matches nothing.

## Findings that shape the fix

- **The anchor must be `currencyMain`, not `currencyMainName`.** The strings `GOLD` and
  `SILVER` appear only in `currencyMainName`, which sits *after* the prices in each object;
  a regex cannot look backwards. The metals are therefore matched on the ISO codes `XAU`
  and `XAG`, which `currencyMain` carries.
- **`sell` is BID, `buy` is ASK.** The field names are the customer's, not the bank's, so
  `sell` is the lower price. Confirmed against the last values stored on 2026-08-24 — USD
  ASK 459.75 / BID 458.35 then, `buy` 450.95 / `sell` 449.35 live — with GOLD and SILVER
  spreads agreeing the same way round.
- **The 600-character bound is measured.** In the live document the longest anchor-to-value
  distance is 393 characters (CHF ASK) and the closest two pair objects are 1232 apart, so
  a pair missing a price cannot pick up its neighbour's.
- **No `parse_float` step.** That step normalises thousand-separator spaces out of rendered
  HTML and formats with `%.3f` on the way through, which is lossy now the payload is JSON:
  UZS quotes 0.03367 and would store as 0.034. The end of the rule pipeline already parses
  the capture at full precision.

## Tasks

1. **`migrations/202609.035.rate_sources.bcc_data_fx_rules.sql`** — 24 `UPDATE` statements
   re-anchoring each source's `rules` on the escaped JSON. Filename deliberately excludes
   `seed`, so `sourceaudit.ParseSeedFiles`' `*.seed*.sql` glob and its 57-source count are
   untouched.
2. **`migrations/bccfx_rules_test.go`** — read the rules back out of the embedded migration,
   assert each is a single `regex` rule, and apply it to an inline fixture carrying the real
   key order and per-object length. A second test omits one pair's `sell` and requires the
   rule to fail rather than read the next currency's price.
3. **Gate** — `make test`.
4. **Review** — standard staged lens set, then lens O.

## Acceptance criteria

- All 24 patterns extract the correct value from the live page (verified out of band before
  the migration was written, and in the test against the fixture).
- ASK > BID for every pair.
- UZS and RUB keep the five significant digits the JSON carries.
- A pair whose price key is absent yields an extraction failure, not a neighbour's quote.

## Out of scope

- **Deploying.** There is no staging: applying this needs an `r_*` tag, which flips the
  production symlink. That is the owner's call.
- **Second providers for the nine single-sourced pairs.** One bank's markup change taking
  nine pairs out at once is what turned a rule breakage into a three-week data gap, but
  adding providers is its own piece of work — recorded in the backlog rather than smuggled
  in here.
- **Alerting.** It worked; the message was sent and not acted on.
