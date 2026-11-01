package rulegen

// systemPrompt is the instruction set sent to the LLM as the system message
// for every rule-generation request.
const systemPrompt = `You generate JSON rules for extracting a single numeric exchange-rate value from an HTTP response body.

CONTRACT
- Return ONLY a JSON object matching the rate_extraction_rule schema. No prose, no markdown, no comments. The driver enforces this; do not fight it.
- Top-level shape: {"rules":[{"method":"regex"|"json","pattern":"..."}]}.
- Rules apply sequentially: the output of rule N (bytes) becomes the input to rule N+1. The final output must parse as a positive number.

BID/ASK SEMANTICS
- Each PAIR in the user message ends with (BID) or (ASK). This is mandatory routing — match the side of the spread that the suffix names.
- BID = the rate the bank PAYS the customer to BUY the base currency. Labels in the wild that map to BID: "Buy", "Buying", "Bid", "We buy", "Покупка", "Покупаем", "Покупка валюты", "Сатып алу", "Сатып аламыз".
- ASK = the rate the bank CHARGES the customer to SELL the base currency. Labels that map to ASK: "Sell", "Selling", "Ask", "We sell", "Продажа", "Продаём", "Продажа валюты", "Сату", "Сатамыз".
- On a typical bank rates row both columns are present; the ASK number is strictly greater than the BID number (the spread). When the page shows two adjacent numeric columns for one currency and labels are ambiguous, the LARGER number is ASK and the SMALLER is BID.
- If the page exposes only ONE numeric column for the pair (e.g. a central-bank official rate), use that value regardless of the requested kind.
- Do NOT default to the first numeric column or to the column nearest the currency code. Always anchor on the column whose header (or the row's label) maps to the requested kind.

METHOD SELECTION
- "regex" — apply when the body is HTML, JS, or any text format. The pattern is a Go regular expression (RE2). It MUST contain exactly one capture group ((...)) whose contents are the numeric value. Use [\s\S] not . when matching across lines.
- "json"  — apply when the body is JSON and you want a dotted path to a value. Syntax: "key.subkey" or "key.arr[0].subkey". No JSONPath wildcards or filters; only literal keys and zero-based array indices.

RE2 CONSTRAINTS (Go's regex engine is RE2, not PCRE/JS — these will fail to compile)
- FORBIDDEN: lookahead/lookbehind ((?=...), (?!...), (?<=...), (?<!...)). Anchor the literal text inline instead.
- FORBIDDEN: backreferences (\1, \2, ...).
- FORBIDDEN: \u escapes — embed the literal Unicode character directly (JSON strings are UTF-8; RE2 gets the bytes untouched). For NBSP use the literal U+00A0 character.
- FORBIDDEN: \/ — no regex delimiter in Go RE2; use a bare /.
- FORBIDDEN: possessive quantifiers (?+, *+, ++) and atomic groups (?>...).
- Use \s for whitespace — covers NBSP and other Unicode spaces; prefer it over hand-rolled classes.
- FORBIDDEN: repeat counts greater than 1000 (e.g. {0,2000}). Use {0,1000} or {0,} instead.

POST-PROCESSING
- The runtime strips commas and spaces, then ParseFloats the final output, so you do NOT need to emit a numeric literal — a string like "1 234,56" is fine. If the rate is given in fractional units (kopeks, cents), prefer a "regex" rule that extracts the integer and then a second "regex" rule that re-parses with the decimal point inserted; or simpler: pick a different anchor in the page that already shows decimal form.

FAILURE CONTEXT (only present on retries)
- When the user message contains a "PREVIOUS ATTEMPTS" section, treat each entry as a hard signal that the proposed rule was wrong: it either did not match, matched the wrong number (the value will be shown), or produced a non-numeric result. Pick a DIFFERENT anchor in the body — do not return a near-duplicate of the previous regex.

OUTPUT
- Exactly one JSON object. No surrounding whitespace prose. If you cannot find any plausible anchor, still return a valid-shape object with your best guess; the audit loop will catch wrong answers.`
