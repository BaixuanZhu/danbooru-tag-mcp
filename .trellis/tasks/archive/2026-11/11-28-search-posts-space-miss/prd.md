# Diagnose space-split misses in search_posts

## Goal

`search_posts` splits its query on spaces — that is the multi-tag syntax —
so a space inside one tag name (`blue hair` meaning `blue_hair`) silently
becomes two near-0-post tokens and returns `[]` with no hint. Give the
caller a facts-only diagnostic instead of changing the splitting.

## Background

Defect found 2026-11-27, rated P2; facts in
`research/space-split-miss-facts.md`. The repo convention is facts-only
output: the server reports facts, the consuming agent decides. The likely
shape is a diagnostic on the empty result (0-post tokens plus a
vocabulary-backed underscore-join suggestion à la `wd14_hits`), NOT an
automatic retry with a rewritten query.

## Requirements

- R1: when the result is empty and an adjacent pair of plain query tokens
  joins (underscore) into a name the embedded WD14 vocabularies carry, the
  response carries `possible_tags` naming the candidate(s) — shape settled
  in the design decisions below.
- R2: no behavior change when the query is genuinely good: valid queries
  stay byte-identical, genuinely dead queries stay bare empty (no
  diagnostic).
- R3: diagnostics trigger no extra Danbooru requests beyond what the
  existing wd14 fallback already does (vocabulary lookups are local and
  embedded).
- R4: tool description gains at most one clause.
- R5: the local too-many-tags error carries the same hint when candidates
  exist (the 2-tag-cap interaction is fixed, not ruled out — see D3).

## Design decisions (settled 2026-11-28, before implementation)

- D1 Detection (local only, no request): a candidate is a pair of ADJACENT
  whitespace tokens `a b`, neither a metatag-looking token, whose
  underscore-joined lowercase form `a_b` the embedded WD14 vocabularies
  carry (`vocab.Store.Lookup`). Candidates keep query order, are deduped
  and capped at 3. "Token has no posts" is NOT verified — that would cost
  requests (R3); an empty result plus a vocabulary-backed join is the
  signal. A nil vocab store yields no candidates.
- D2 Empty path (success payload): `{"posts":[],"possible_tags":["blue_hair"]}`
  — `possible_tags` is omitted when there are no candidates, so every other
  payload stays byte-identical (`{"posts":[...]}`, `{"posts":[]}`).
- D3 Over-limit path (the local `too many content tags` error): fixed. When
  candidates exist, the error message gains a suffix naming each one —
  `...; drop tags or split the query; 'blue hair' may be one tag: blue_hair`.
  Without candidates the message is byte-identical to today. Example:
  `blue hair order:random` counts 3 tokens locally although the intended
  `blue_hair order:random` is a legal 2-tag query.
- D4 `SearchPosts` returns a small result struct instead of a bare slice —
  `PostsResult{Posts []Post `json:"posts"`; PossibleTags []string
  `json:"possible_tags,omitempty"`}` — and the service fills
  `PossibleTags` when the posts are empty and candidates exist; the handler
  just marshals it. The dead-end knowledge stays with the code that knows
  the result is empty, key order is stable, and every other payload stays
  byte-identical. The multi-tag task (11-28-search-posts-multi-tag) adds its
  scan fields to the same struct instead of changing the signature twice.
- D5 No auto-retry, no query rewrite: the server reports the candidate, the
  agent decides.
- D6 Scope guard: the crude `isMetatagToken` (any colon = metatag) behind
  the 2-tag count and diff subtraction is out of scope and unchanged.

## Acceptance Criteria

- [x] Unit tests: `blue hair` yields `possible_tags: ["blue_hair"]`; a
      genuinely dead query (`not_a_real_tag_xyz`, `blue xyzzy`, a single
      token, a nil vocab store) yields none; metatag tokens are never
      joined; the handler's non-empty payload has no `possible_tags` key.
- [x] `blue hair order:random` is still rejected locally and its message
      names `blue_hair`; an over-limit query without candidates keeps the
      exact message of today.
- [x] No extra fetcher call on either path (mock call counters).
- [x] Live integration assertions for the empty path, the over-limit path
      and a genuinely dead query (checks 22-24).
- [x] `make test`, `make test-race` green; README + CHANGELOG rows.

## Notes

- Lightweight-to-medium; PRD-only valid. The diagnostic shape is settled
  above; implement directly.
- R5 / D3 (the over-limit hint) is pinned against today's cap of 2. The
  later task 11-28-search-posts-multi-tag raises the cap to 6, which turns
  `blue hair order:random` into an empty-result case: its tests then move to
  the D2 path and the message suffix survives only above 6 tags.
- With the cap at 2 the empty path can name at most one candidate (a third
  content token is over the limit first); the cap of 3 is reachable on the
  over-limit path.
