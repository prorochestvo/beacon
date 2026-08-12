# Task Breakdown

## Overview

`/api/me/*` is the project's only authenticated surface, and every one of its fourteen
handlers opens with the same four lines: read `X-Telegram-Init-Data`, call
`h.validateInitData`, 401 on error, keep the returned user id. Nothing enforces those lines.
A handler added to the family without them is a silent authentication bypass — it serves
another user's data with no error and no log line. The duplication is the mechanism; the
absent enforcement is the risk (issue #36).

This replaces the copies with one `middleware.TelegramInitData` wrapper and mounts the
`/api/me/` family on its own `http.ServeMux` behind it, so a route registered there is
authenticated by construction rather than by remembering. Handlers stop authenticating and
read an already-validated caller id from the request context.

The guard that makes this safe already exists: `router_auth_test.go` (from #47) walks every
`/api/me/*` route against four unauthenticated header variants — 56 cases — and
`TestMeRouteTableIsComplete` keeps its table honest against `routes.go`. That test must stay
green across the whole refactor, and it is the evidence that no check was dropped.

## Assumptions

- All eleven authenticated route constants share the `/api/me/` prefix — verified against
  `internal/gateway/httpV1/routes/routes.go:54-109`. A future authenticated route outside
  that prefix would not be covered by the mount and would need its own wrapping.
- `botToken`, `meSubscriptionsMaxAge`, `nowFn` and `validateInitData` are used **only** by
  the auth check — verified by grep across `internal/gateway/httpV1/handlers`. All four can
  move wholesale; none has a second consumer to strand.
- Handlers that do not need the caller id (`SearchWeatherCities` discards it today) simply
  stop asking for it.
- The Mini App client is unaffected: the header name, the 401 body and the accepted
  credential do not change.

## Tasks

### Task 1: The middleware and its context contract

- Description: add `TelegramInitData` to `internal/gateway/middleware`. It reads the
  credential from the `X-Telegram-Init-Data` header only, validates it, writes the caller's
  Telegram id onto the request context under an unexported key type, and answers 401 with
  the existing body on any failure. Expose `UserIDFrom(ctx) (int64, bool)` as the only way
  to read it. The validator, bot token, max age and clock arrive as configuration, carrying
  over the two sanctioned test seams from `Handler`.
- Acceptance Criteria:
  - A signed request reaches the wrapped handler with the id readable via `UserIDFrom`.
  - An absent, empty, malformed or unsigned header yields 401 and the wrapped handler is
    never invoked.
  - The credential is read from the header only; a query-string copy is ignored.
  - The context key is unexported and typed, so no other package can collide with or forge it.
- Pitfalls & edge cases:
  - The 401 body must stay byte-identical (`{"error":"unauthorized"}`); the Mini App and the
    existing tests both match on it.
  - `UserIDFrom` must report `ok=false` rather than a zero id, so a caller cannot mistake an
    unauthenticated request for user 0.
  - Do not log the credential or any part of it — it is a signed payload (see the data
    privacy policy).
- Complexity: Medium

### Task 2: Mount the authenticated family behind it

- Description: in `httpV1.NewRouter`, register the eleven `/api/me/*` routes on a dedicated
  `http.ServeMux` and mount it once as `mux.Handle("/api/me/", TelegramInitData(cfg)(meMux))`.
  The outer mux keeps every public and admin route.
- Acceptance Criteria:
  - All eleven routes resolve exactly as before, including the `{id}` and `{location_id}`
    wildcards and the `subscriptions/raw` vs `subscriptions` precedence.
  - Method mismatches inside the family still produce 405, not 404.
  - No public or admin route changes behaviour.
- Pitfalls & edge cases:
  - Go 1.22 `ServeMux` resolves by specificity, not registration order; the mounted prefix
    pattern `/api/me/` carries no method, so method matching happens on the inner mux.
  - Path values (`r.PathValue("id")`) must survive the mount — the inner mux does the
    pattern matching, so they should, but this needs asserting rather than assuming.
  - The mount is what makes forgetting impossible; adding a `/api/me/*` route to the outer
    mux by mistake would bypass it, which Task 4's test must catch.
- Complexity: Medium

### Task 3: Strip the check from the fourteen handlers

- Description: remove the four inline lines from each handler and read the caller id from
  the context instead, via a small fail-closed helper on `Handler`. Delete `botToken`,
  `nowFn`, `validateInitData` and `meSubscriptionsMaxAge` from the handlers package, and
  `BotToken` from `handlers.Config`.
- Acceptance Criteria:
  - No handler calls a validator; `grep` for `validateInitData` in the handlers package
    returns nothing.
  - A handler whose route was somehow not mounted behind the middleware answers 401 rather
    than serving — the helper fails closed.
  - `SearchWeatherCities`, which discarded the id, stops asking for it entirely.
- Pitfalls & edge cases:
  - `Config.BotToken` disappearing changes the constructor contract — `cmd/web` passes it
    through `NewRouter`, which now hands it to the middleware instead.
  - The helper must answer 401, not 500: a missing id is an unauthenticated request as far
    as the caller is concerned, and 500 would be a disclosure the 404-not-403 rule already
    guards against elsewhere.
- Complexity: Medium

### Task 4: Prove the mount, not just the middleware

- Description: add a test that a `/api/me/*` route registered outside the authenticated mux
  cannot serve an unauthenticated caller, so the mount itself is covered rather than only
  the wrapper.
- Acceptance Criteria:
  - `router_auth_test.go`'s 56 cases stay green unchanged.
  - Removing the middleware from the mount makes the guard fail — verified by planting it.
  - The new test fails if a route is moved from the inner mux to the outer one.
- Pitfalls & edge cases:
  - The existing guard passes booby-trapped stubs whose methods panic; that mechanism must
    keep working, since it is what distinguishes "rejected" from "served nothing".
- Complexity: Easy

### Task 5: Rewrite the handler tests onto the new contract

- Description: 129 subtests inject `h.validateInitData`; they must instead build a request
  carrying an authenticated context. Add one test helper for that. The 20 subtests that
  assert 401 for a bad header are now testing the middleware through a handler — delete
  them, having first confirmed route-by-route that `router_auth_test.go` covers the same
  ground.
- Acceptance Criteria:
  - No test injects a validator into a handler.
  - Every deleted 401 subtest is accounted for by a named case in the router guard.
  - `me_weather_test.go`'s helpers build their handlers through the same path.
- Pitfalls & edge cases:
  - Deleting coverage is the dangerous half of this task. The router guard covers each route
    against four header variants; a per-handler subtest that asserted something *else* about
    the 401 (a body, a header) must be kept, not deleted.
  - The middleware needs its own tests for the cases the handlers used to cover: expiry via
    the injected clock, a forged signature, a header-vs-query-string check.
- Complexity: Hard

### Task 6: Update the canon

- Description: `CLAUDE.md`'s Key Patterns bullet states that `Handler`'s only sanctioned test
  seams are `validateInitData` and `nowFn`. After this change they live on the middleware.
  Update that bullet and the HTTP surface section, keeping the file inside its 20k budget.
- Acceptance Criteria:
  - The auth bullet describes where the check actually happens.
  - `wc -c CLAUDE.md` is under 20 000.
  - The `beacon-http-api` skill is checked for statements this invalidates.
- Pitfalls & edge cases:
  - The rule that `/api/me/*` accepts the credential in the header only, and that a
    resource owned by another user is 404 and never 403, must survive verbatim.
- Complexity: Easy

## Execution Order

1. Task 1 — the middleware, with its own tests, before anything depends on it.
2. Task 2 — mount it; the existing 56-case guard should stay green with handlers still
   double-checking, which is the safe intermediate state.
3. Task 3 — strip the handlers, now that the mount is proven.
4. Task 4 — cover the mount itself.
5. Task 5 — rewrite the handler tests.
6. Task 6 — canon last, once the shape is settled.

## Risks

- **Deleting the 20 per-handler 401 tests removes real coverage if the router guard does not
  actually cover the same routes.** Mitigation: enumerate them against the guard's table
  before deleting, and record the mapping in the PR.
- **The mount changes routing, not just authentication.** A `ServeMux` precedence surprise
  would show up as a 404 or a 405 on a route that worked. Mitigation: the guard walks every
  route, and Task 2's criteria call out wildcards and the `raw` precedence explicitly.
- **Task 3 touches every authenticated handler at once.** Mitigation: the intermediate state
  after Task 2 is safe — both the middleware and the handlers check — so the strip can be
  verified against a green tree rather than a moving one.
- **`Config.BotToken` leaving the handlers package is a breaking constructor change.** Only
  `httpV1.NewRouter` constructs handlers, so the blast radius is one call site plus tests.

## Trade-offs

- **Mounting a sub-mux rather than wrapping each route.** Wrapping per route is a smaller
  diff and keeps one flat registration list; mounting makes forgetting structurally
  impossible, which is the entire point of the issue. Taking the mount.
- **A fail-closed context read in each handler instead of no read at all.** It is one line
  per handler that a stricter design would avoid by passing the id as a parameter, which
  `http.HandlerFunc` does not allow. The line is a safety net, not a second authentication.
- **Moving the two sanctioned seams rather than removing them.** They exist because initData
  is signed with a real bot token and aged against the wall clock; that constraint follows
  the check to its new home rather than disappearing with it.
