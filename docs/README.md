# docs/

Journal of project reviews and product decisions. The goal is to keep a
chronological record of what was inspected, what was decided, and why — so that
months later the context can be reconstructed without digging through the git
log.

## Layout

```
docs/
├── README.md          # this file
├── reviews/           # snapshots of project state (pros/cons, recommendations)
└── decisions/         # ADRs: decisions made on recommendations from reviews/
```

The `docs/swagger/` directory (excluded via `.gitignore`) holds generated
Swagger output and is unrelated to this journal.

## File naming

Same convention as `plans/completed/` from `CLAUDE.md`:

```
YYMMDD.NNNN.slug.md
```

- `YYMMDD` — creation date (`260505` for 2026-05-05).
- `NNNN` — daily sequential index, starting at `0001`.
- `slug` — kebab-case, readable at a glance (`project-review-wasm-focus`,
  not `review-1`).

## What goes where

| Type | Location | When |
|------|----------|------|
| Review / audit | `docs/reviews/` | A point-in-time snapshot: "this is what the project looks like now, here is what bothers me". May contain recommendations but does not record decisions. |
| ADR | `docs/decisions/` | After a concrete decision is made on a recommendation from a review (do it / reject / defer). One ADR — one decision. |

An ADR file always references the review it grew out of and records: the
problem, the options considered, the chosen option, and the consequences.

## Relationship to `plans/`

- `docs/reviews/` answers "what's wrong in the project and why".
- `docs/decisions/` answers "what did we decide to do about it".
- `plans/NNN-slug.md` answers "how exactly are we doing it" — the technical
  implementation plan for a specific decision.

So the normal flow is: a review surfaces a problem → an ADR records the
decision → a plan describes the implementation → code carries it out.
