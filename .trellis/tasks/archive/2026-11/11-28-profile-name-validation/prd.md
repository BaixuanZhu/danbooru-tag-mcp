# Stop rejecting valid tag names in get_tag_profile

## Goal

`get_tag_profile` rejects valid Danbooru names containing `:` or a space
(`internal/service/profile.go`, `strings.ContainsAny(raw, " :")`), so
`firefly_(honkai:_star_rail)` — a real name with live wiki and related data —
errors out, while `get_tag_info` / `search_tags` accept it. Fix the guard so
it blocks only what it was built to block.

## Background

Facts in `research/profile-name-validation-facts.md` (defect found
2026-11-27, rated P1):

- Colons occur inside valid names (parenthesized qualifiers like
  `_(honkai:_star_rail)`); a colon by itself is not metatag syntax.
- The check's original intent must be re-read first: most likely it guards
  caller metatags (`order:`, `rating:`) from being injected into the
  single-tag sample query. Whatever that guard really blocks must keep
  failing with the same clear error.
- Spaces: Danbooru names use underscores; `normalizeTag` (TrimSpace +
  space→underscore) already exists in the service — normalizing may be
  better than rejecting.

## Requirements

- R1: names containing `:` inside (parenthesized qualifiers or otherwise
  valid Danbooru names) are accepted and sampled normally.
- R2: the real guard stays — the injection case it was built for (expected:
  caller `order:` / `rating:` metatags in the sample query) still fails with
  a clear error, pinned by a test BEFORE the check is changed.
- R3: `with` (pair mode) applies the same rules to both tags; both paths
  pinned.
- R4: behavior is unchanged for every name that works today (test both
  modes before/after).
- R5: the metatag predicate is pinned on its own — known keys (mixed case,
  with a leading `-` / `~`) are metatags; `honkai:_star_rail`,
  `firefly_(honkai:_star_rail)`, `:d` and unknown keys such as `foo:bar` are
  not.

## Design decisions (settled 2026-11-28, before implementation)

- D1 Metatag rule: reject a name only when, after an optional leading `-` or
  `~`, it begins with a reserved prefix (case-insensitive): a known Danbooru
  post-search metatag key (`PostQueryBuilder::METATAGS`) or one of the
  edit-only keys of `PostEdit::METATAGS`. This mirrors both halves of
  Danbooru's own `TagNameValidator`, which forbids creating such a tag, so
  nothing valid is rejected — not even `honkai:_star_rail`, which starts
  with `word:`. Both "any colon" (today) and "colon in the leading position"
  reject valid names.
- D2 Spaces: a whitespace-separated input stays rejected. A space is never
  part of a Danbooru name and is Danbooru's multi-tag separator, so
  `arm_up 1boy` must keep the clear local error instead of becoming a lookup
  of `arm_up_1boy` (a misleading "tag not found"). Any whitespace counts
  (`strings.Fields`), not only the literal space.
- D3 The guard may be narrow because it is not the injection barrier: both
  modes build the sample query from the resolved row names, never from the
  raw argument (see research). An unlisted future metatag key costs one
  request and a not-found, never a wrong sample.
- D4 Error text is unchanged for every input rejected today.
- D5 `search_posts` is out of scope: its `isMetatagToken` (any colon = exempt
  from the 2-tag cap) stays as is, because the exemption side is the safe
  side — an unlisted metatag must never be counted as a content tag. Only a
  comment records that the two predicates differ on purpose.
- D6 (review follow-up, 2026-11-28) the edit-only keys live in a second list
  that only `isSingleTagName` consults; `isDanbooruMetatag` remains the
  exact post-search predicate, which the later multi-tag task relies on for
  query classification. Facts in `research/profile-name-validation-facts.md`.

## Acceptance Criteria

- [x] `firefly_(honkai:_star_rail)` profiles successfully in single mode and
      in pair mode with `honkai:_star_rail` (unit tests with a mock fetcher;
      both also covered by live integration calls 19 and 20).
- [x] Metatag-injection rejections keep their exact error text, pinned by a
      test that was green BEFORE the guard changed.
- [x] `make test` + `make test-race` green; README needs no change (it never
      documented the rejection).

## Notes

- Lightweight: PRD-only is valid. The fix is likely a line plus tests, but
  the guard's purpose must be understood first so it is not lost.
