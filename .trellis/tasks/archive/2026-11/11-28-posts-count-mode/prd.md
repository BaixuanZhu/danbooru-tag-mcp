# Add count mode to search_posts

## Goal

Consuming agents often need "how many posts match this query" (population
sizing, sanity checks before sampling) but today must fetch up to 20 full
posts to eyeball a total. Add a count mode that answers from
`/counts/posts.json` without fetching post bodies.

## Background

Verified live on 2026-11-27, recorded in
`research/counts-endpoint-facts.md`:

- `/counts/posts.json?tags=...` returns exact, uncapped totals anonymously
  (whole-site N = 12,583,571; `arm_up` 317,272 equals its `post_count`).
- The same tag-count limit applies: 3 content tags return HTTP 422
  (TagLimitError), so the existing local 2-tag pre-validation applies
  unchanged.
- Metatag handling (`rating:` / `order:` / `status:`) on the counts endpoint
  was NOT probed — design must probe before promising support.

## Requirements

- R1: `search_posts` accepts a count mode (design picks the parameter shape:
  `mode: "count"` vs a boolean vs a dedicated field) that switches the
  request to `/counts/posts.json` and returns at least `{query, total}`.
- R2: counts are always live; nothing may be cached.
- R3: the local 2-content-tag pre-validation applies identically in count
  mode; over-limit queries fail with the same clear local error.
- R4: upstream failures use the standard `upstream_unavailable` marking.
- R5: tool description gains at most one clause; docs limited to README +
  CHANGELOG rows.

## Acceptance Criteria

- [x] Unit tests (mock fetcher) pin the count parsing and the local
      validation in count mode.
- [x] A live integration assertion checks a known tag's count equals its
      `post_count` (check 25: `blue_hair`, tolerance 10 for posts uploaded
      between the two calls).
- [x] README + CHANGELOG updated; schemas and pin tests move together.
- [x] `make test`, `make test-race`, `make integration` green.

## Notes

- Medium task. If the parameter-shape question settles quickly, the design
  can be folded into a short section of `implement.md` instead of a separate
  `design.md`.
