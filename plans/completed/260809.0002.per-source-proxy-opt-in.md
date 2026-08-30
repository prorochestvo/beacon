# Task Breakdown

## Overview

Issue #16 removed the proxy from collection on measurement, but did so by deleting the
capability rather than by controlling it — which left the all-or-nothing switch that #16
itself named as the real problem, just flipped to "off". This restores the capability
under a per-source control: `BEACON_PROXY_URL` says a proxy exists,
`rate_sources.options.use_proxy` says a given source wants it, and only their conjunction
proxies anything. Absent means direct, so configuring the variable alone re-routes
nothing and no source changes behaviour as part of this work. It is a mechanism, not a
policy change.

## Assumptions

- `options` is already the per-source fetch-behaviour JSON column (`Headers` lives there
  and 20 sources use it), so no schema migration is needed and rows written before the
  option existed decode to `false`.
- `RateExtractor.Run` receives the whole `*domain.RateSource`, and `fetchHtmlPage` already
  takes per-source data, so the flag rides an existing path.
- No source is opted in by this change. The measured conclusion from #16 — direct is
  faster, more available and less suspicious for these hosts — stands unchanged.

## Tasks

### Task 1: Carry the flag on the source

- Description: Add `UseProxy bool \`json:"use_proxy,omitempty"\`` to
  `domain.RateSourceOptions`, documenting the two-level contract and that the chromedp
  fetcher ignores it.
- Acceptance Criteria:
  - Options JSON without the key decodes to `false`, and a zero-valued `RateSourceOptions`
    marshals without emitting the key, so the default is never written back into stored rows.
- Pitfalls & edge cases: `omitempty` is what keeps the default out of rewritten rows —
  `cmd/doctor rulegen` rewrites source rows wholesale.
- Complexity: Easy

### Task 2: Two clients in the extractor, chosen per source

- Description: `RateExtractor` gains a `proxyClient` alongside `httpClient`.
  `NewRateExtractor` always builds the direct client and builds the proxied one only when
  a proxy URL was configured. Add `NewRateExtractorWithHTTPClients` for tests that
  exercise routing; keep `NewRateExtractorWithHTTPClient` as the no-proxy delegation.
  `fetchHtmlPage` takes the flag and selects the client; `Run` passes
  `source.Options.UseProxy`.
- Acceptance Criteria:
  - A source without the flag never reaches the proxy, even with one configured.
  - A flagged source reaches the proxy.
  - A flagged source with no proxy configured falls back to direct and logs that it did.
  - Every fetch logs its route (`via=direct` / `via=proxy`).
- Pitfalls & edge cases:
  - The request context deadline is derived from the selected client's `Timeout`, so both
    clients must carry one. A zero `http.Client.Timeout` means "no timeout" to `net/http`
    and "already expired" to `context.WithTimeout` — an inverted meaning that silently
    fails every fetch. Worth a separate guard; out of scope here.
  - Do not consult the Go proxy env triplet; the explicit variable is the only input.
- Complexity: Medium

### Task 3: Key the cache and the tombstone on the route

- Description: Replace the URL-only cache key and the URL-keyed `failedURLs` tombstone
  with a shared `fetchKey(rawURL, useProxy)`.
- Acceptance Criteria:
  - Two sources on the same URL disagreeing about the proxy each get their own response,
    not whichever fetched first.
  - A failure on one route does not tombstone the other.
  - The stale comment claiming "every source has a unique URL" is corrected.
- Pitfalls & edge cases: headers stay out of the key on purpose — the 20 Yahoo sources
  share a URL *and* their headers, which is what makes batching work. The comment must
  say so rather than repeat the claim that URLs are unique, which batching made false.
- Complexity: Easy

### Task 4: Wire the collector, keep chromedp and weather direct

- Description: `cmd/collector` resolves `BEACON_PROXY_URL` through `proxyutil.ResolveURL`
  and passes it to `NewRateAgent`. `NewRateAgent` forwards it to the plain extractor only.
- Acceptance Criteria:
  - With the variable set and no source opted in, collection is direct.
  - Weather stays direct regardless.
  - Chromedp is constructed with an empty proxy URL.
- Pitfalls & edge cases:
  - `proxyutil.ResolveURL` logs and can `log.Fatalf`, so it must be called in `main` after
    the logger exists — a package initialiser would send both to a stderr the cron
    wrappers discard, the same defect that once left `build:` unattributable.
  - `NewRateAgent` passed one proxy URL to *both* extractors. Forwarding the real value
    unchanged would make a configured variable route every chromedp source at once, which
    is exactly the all-or-nothing behaviour being removed.
- Complexity: Medium

### Task 5: Documentation

- Description: Correct `CLAUDE.md` (the egress section now describes a default, not an
  absence; the env-var entry; the cache-key note), the `cmd/collector` package doc, and
  the `proxyutil` package doc.
- Acceptance Criteria: no surviving claim that `cmd/collector` does not read
  `BEACON_PROXY_URL`, and the chromedp and weather exclusions are stated.
- Complexity: Easy

## Execution Order

1. Task 1
2. Task 2
3. Task 3
4. Task 4
5. Task 5

## Risks

- **A configured proxy URL is now live plumbing rather than dead config.** The guard is
  the default and the tests that pin it from both ends: `NewRateExtractor` with a proxy
  configured must not route an unflagged source, and the collector-level test points
  `BEACON_PROXY_URL` at a closed port so a successful fetch could only have gone direct.
- **Chromedp is a standing trap.** Its proxy is a browser-launch argument on a subprocess
  shared by the tick, so it cannot be per-source. It is wired with an empty URL and a
  comment saying why; anyone giving it the real value re-creates the all-or-nothing switch.
  There are no active chromedp sources today, so the mistake would be silent.
- **Opting a source in is untested against a real upstream.** Nothing is opted in, so the
  proxied path is exercised only by tests. The first real use should be verified against
  the actual host before being left running.

## Trade-offs

- **Fall back to direct rather than fail when the flag is set and no proxy exists.** A
  missing environment variable should not stop collecting a source that reaches its host
  either way; the fallback is logged, so the misconfiguration is visible rather than
  silent. The alternative — failing that source — was considered and rejected as trading a
  config error for a data gap.
- **Route in the key, headers not.** The route is a boolean and cheap to key on, and a
  per-source proxy makes the collision reachable for real. Headers are a map, and the one
  case of sources sharing a URL is the batched Yahoo set, which shares headers too. Adding
  headers to the key is the fix if a disagreeing pair is ever created.
- **Chromedp left unimplemented rather than approximated.** Restarting Chromium per
  source to vary its proxy would cost a browser launch per source for zero active sources.
