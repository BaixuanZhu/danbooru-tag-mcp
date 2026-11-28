# Add lift scoring to get_related_tags

## Goal

`get_related_tags` returns Danbooru's related tags in upstream order
(frequency descending), so corpus constants like `1girl`/`solo` dominate the
top and tag-specific companions sit at ranks 13-90 where the default `limit`
cuts them off. Add a lift score per entry so specific companions surface
first. Highest-value item of the tier-1 consumer proposal (analysis verdict
2026-11-27).

## Background

All API facts verified live on 2026-11-27, recorded in
`research/related-tag-lift-facts.md`. Key ones:

- `/related_tag.json` has no lift; entries carry `frequency` plus cosine /
  jaccard / overlap coefficients, ordered by frequency desc, default 100
  entries, `limit=500` accepted.
- Lift is computable as `frequency x N / post_count`, where N = the
  site-wide post count from `/counts/posts.json` (exact and uncapped
  anonymously; N = 12,583,571 on 2026-11-27) and post_count = the tag's own.
- The exact semantics of Danbooru's `frequency` field must be pinned during
  design before the formula ships (see research).

## Requirements

- R1: every related entry carries a `lift` field computed with the verified
  formula; the change is additive — no existing field disappears, no shape
  breaks. Entries are RANKED by lift so specific companions land inside the
  caller's `limit` (the goal); the pool upstream is asked for is widened
  accordingly.
- R2: N comes from `/counts/posts.json`, fetched at most once per process
  (in-process memo only, success memoized — a failure is retried on the
  next call). The disk cache was deleted in 0.7.0; none may be
  reintroduced.
- R3: if the site-wide count request fails, the response degrades
  gracefully: entries come back in upstream (frequency) order without a
  `lift` field, exactly the pre-lift behavior. A full upstream failure of
  the related request itself follows the standard `upstream_unavailable`
  marking.
- R4: `limit` composes with lift unchanged in meaning (max entries to
  return) and is applied AFTER the lift ranking; meta-category (5) exclusion
  and query-tag exclusion stay as pinned today. (The tool exposes no
  category filter — see research.)
- R5: tool description gains at most one clause mentioning lift; docs change
  only in README + CHANGELOG rows.

## Acceptance Criteria

- [x] Unit tests with a mock fetcher pin the lift math, the lift ranking,
      the support floor (below-floor entries sink, never vanish), and the
      once-per-process N fetch (second call must not re-request N).
- [x] A failed N request degrades (no `lift`, upstream order) and is retried
      on the next call; an empty related result requests no N.
- [x] Live spot check (anonymous, 2026-11-28): for `arm_up` (317,410 posts;
      site N = 12,592,609) Danbooru's frequency order starts `1girl` (lift
      1.03), `solo` (1.11), `long_hair` (1.13); the lift order starts
      `arm_behind_head` (14.77), `hand_on_own_head` (8.40), `armpits` (8.03),
      `hand_in_own_hair` (4.94), `outstretched_arm` (4.85), `waving` (4.52).
      Integration check 26 pins it live: sorted by lift, order consistent
      with `frequency / post_count`, and a lift >= 3 on top.
- [x] `make test`, `make test-race`, `make integration` green; tool schema
      pins updated together.
- [x] README + CHANGELOG rows added.

## Notes

- Complex task: `design.md` + `implement.md` are required before
  `task.py start`.
- The `frequency`-field semantics check is the first design step — DONE
  2026-11-28 (source + live counts; see research).
- Verification history (2026-11-28): the implement agent was interrupted by
  an account usage cap before reporting; the main session repaired its
  mangled test file and finished the docs. Two mutation batteries then ran
  through `go test -overlay`: 11 mutants (main session) and 90 (the check
  agent). The first battery found a test that could not fail (an unstable
  sort survived: `sort.Slice` leaves an all-equal block untouched), fixed by
  `TestRelated_EqualLiftsKeepUpstreamOrderAfterSwaps`; the second found the
  `min(A, 5000)` cap, the two-decimal rounding and the `Client.Get` route
  unpinned and added tests for them. Final: 97 distinct mutants, 4 left
  standing (two equivalent, two pre-existing gaps on the `limit` parameter).
