# Accept more than two content tags in search_posts

## Goal

`search_posts` rejects more than 2 content tags locally today, mirroring
Danbooru's anonymous limit, so an agent that wants three tags has to search a
pair and eyeball the tag lists itself. Let it pass up to 6: when Danbooru will
not answer the query as-is, the server answers it with a bounded local scan —
the two rarest tags go upstream, the remaining tags are applied from each
post's own tag list — and tells the caller exactly how complete the answer is.

## Background

Facts in `research/multi-tag-local-and-facts.md` (probes and source,
2026-11-28):

- Splitting into several 2-tag requests cannot answer an AND query; fetching
  a 2-tag sub-query and filtering locally can, exactly, because every post
  carries its full tag list.
- Danbooru's limit follows the account: anonymous 2, logged-in below Gold 3,
  Gold and above unlimited. The local validator hard-codes 2, so it also
  blocks a logged-in user's 3-tag queries.

## Requirements

- R1: `search_posts` (list and count mode) accepts up to 6 content tags per
  query. Queries within the direct limit are unchanged: one upstream request,
  byte-identical payload.
- R2: above the direct limit the query is answered by a bounded local scan:
  the two rarest positive tags (one batch post-count lookup) plus the query's
  metatags go upstream as one 2-tag query; pages of up to 200 light posts are
  filtered locally by the remaining positive tags and `-tag` exclusions.
- R3: the scan is bounded — at most 5 pages, and it stops early once `limit`
  matches are collected (list mode) or the listing ends.
- R4: split answers are transparent. List mode adds `upstream_query`,
  `scanned` and `complete`; count mode adds `scanned` and `complete`, and its
  `total` is exact only when `complete` is true (otherwise a lower bound).
- R5: `complete` is true exactly when the returned posts are the whole
  answer: `limit` matches were found, or the listing ended. Otherwise the
  posts are a true prefix of the answer and `complete` is false.
- R6: the direct limit follows the account. Anonymous never tries more than 2.
  With credentials the full query is tried first (up to 6) and the scan is the
  fallback when Danbooru refuses it with a tag-limit 422. That refusal is a
  classified error (`app.ErrTagLimitExceeded`) — never a bare `err != nil`,
  never `upstream_unavailable`.
- R7: metatags pass through verbatim; no layer injects or rewrites them.
  Nothing is cached between calls.
- R8: token classification uses the exact metatag predicate (task
  11-28-profile-name-validation). A colon token whose key is not a known
  metatag is a content tag only when Danbooru carries that exact name, so
  `firefly_(honkai:_star_rail)` works while an unlisted future metatag passes
  through instead of becoming a filter that can never match.
- R9: wildcard (`*`) and OR (`~`) tokens cannot be evaluated locally: a query
  that needs a scan and contains one is refused with a clear local error.
  Direct queries are untouched.
- R10: the tool description grows by at most two clauses; docs change in
  README, CHANGELOG and AGENTS.md.
- R11: the scan's own query obeys Danbooru's term budget. Metatags outside
  `PostQuery::UNLIMITED_METATAGS` (`order:`, `user:`, `pool:`, ... count;
  `rating:`, `status:`, `id:`, ... do not) reduce the tags the scan sends,
  and the same term count decides whether a query is direct or split. When
  the counting metatags leave no room for a tag, or a scan has no positive
  tag, the query is refused with a clear local error and no request.
- R12: a locally filtered token whose Danbooru row has 0 posts (a renamed
  tag) is resolved through the alias endpoint once, so the scan never
  answers "no matches, complete" for a query Danbooru answers.

## Acceptance Criteria

- [x] Unit tests (mock fetcher): classification (plain, negated, known
      metatag, colon tag with and without a Danbooru row), rarest-pair
      choice, local filtering, metatag pass-through, scan bounds and early
      stop, both `complete: true` cases and the false case, count mode over a
      scan, the light fetch params, and the direct path staying
      byte-identical.
- [x] Unit tests for R11/R12: the term count, the scan's tag budget with
      and without a counting metatag, the two local refusals, the one-step
      narrowing, and a renamed local filter matching under its current name.
- [x] Unit tests (httptest api + service): the tag-limit 422 raises the
      sentinel and triggers the scan only for a credentialed client; an
      anonymous client never sends a 3-tag query; any other error is passed
      through untouched.
- [x] Live integration (anonymous): a 3-tag query returns only posts that
      carry all three tags with `complete: true`; a colon-bearing tag works;
      the 3-tag query with `order:score` is answered by a single-tag scan;
      count mode over 3 tags reports `scanned`; 7 tags are refused locally;
      the light post payload still carries every consumed field.
- [x] The credentialed direct path (3+ tags answered as-is; 422 fallback) is
      verified live with the free-level test account the maintainer provided
      on 2026-11-28 — its credentials live only in the gitignored `.env` at
      the repo root, are sourced explicitly, are redacted from every output,
      and never reach git.
- [x] `make test`, `make test-race`, `make integration` green.

## Notes

- Complex task: `design.md` + `implement.md` are required before start.
- Must land AFTER the other four 11-28 tasks: it builds on the metatag
  predicate (profile-name-validation), the `PostsResult` struct
  (space-miss), `CountPosts` / `FetchPostCount` (count-mode).
- It changes two things the earlier tasks pinned: the local cap becomes 6
  (so the over-limit tests of 11-28-search-posts-space-miss move to the
  empty-result path) and `isMetatagToken` gives way to the exact predicate.
