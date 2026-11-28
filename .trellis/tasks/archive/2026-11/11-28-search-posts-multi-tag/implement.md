# Implementation plan — more than two content tags in search_posts

Run LAST among the 11-28 tasks. It builds on:

- `internal/service/metatag.go` (task 11-28-profile-name-validation),
- `PostsResult` (task 11-28-search-posts-space-miss),
- `CountPosts` and `Client.FetchPostCount` (task 11-28-posts-count-mode).

Read `design.md` first; the step numbers below follow its sections.

## Ordered checklist

1. **Baseline** — `go build ./... && go vet ./... && go test ./...` green.
2. **app** — `ErrTagLimitExceeded` sentinel.
3. **api**
   - `FetchPosts(ctx, tags, limit, page)`: always send
     `only=<light fields>`; send `page` when > 1. Update the two existing
     callers (`SearchPosts`, the profile sample) and the test doubles.
   - Wrap a 422 whose body contains `TagLimitError` in the tag-limit error
     (text unchanged, `Is` matches the new sentinel).
   - `Authenticated()`.
   - Tests: `only=` and `page` parameters; `TagLimitError` 422 is marked;
     another 422 is not; 5xx still takes the upstream path; `Authenticated`.
4. **service — parse** — `parsePostQuery` (positives, negatives, metatags,
   unresolved colon tokens; the 6-token cap; the `*` / `~` refusal), the
   exact metatag predicate replacing `isMetatagToken` in the query helpers,
   and the term count: `terms` = non-metatag tokens + counting metatags
   (keys outside `unlimitedMetatagKeys`, a 25-key transcription of
   `PostQuery::UNLIMITED_METATAGS` with a size test like the other lists).
5. **service — scan** — a pure, table-testable filter (post tag set vs
   positives / negatives) plus the page loop with the three stop rules and
   id dedupe; the batch lookup that resolves colon tokens and picks the
   pair (direct `FetchTagsByNames`, NOT `Infos`); the scan's tag budget
   `k = min(2, directTagLimit - countingMetatags)` with its two local
   refusals (`k < 1`; no positive tag); the one-step narrowing when a page
   request answers the tag-limit error; and the alias lookup for a
   local-filter token whose row has 0 posts (reuse the alias request
   `get_tag_info` uses; pair members and pass-throughs are never resolved).
6. **service — wiring** — `WithUpstreamTagLimit`, `PostsResult` /
   `PostCount` extensions, `SearchPosts` and `CountPosts` routing
   (direct → tagged-limit fallback → scan).
7. **Tests** (write against the old code first where the behavior is new):
   - `TestParsePostQuery_*` — every row of the classification table; the
     cap fires before any request; the wildcard refusal.
   - `TestScan_PicksRarestPairAndCanonicalNames` — `1girl arm_up sword`
     → pair `arm_up sword`; `ARM_UP` → canonical `arm_up` in query and
     filter.
   - `TestScan_ClassifiesColonTokensByRowPresence` —
     `firefly_(honkai:_star_rail) 1girl solo` becomes a 3-positive scan;
     `foo:bar` (no row) is passed through, never a local filter.
   - `TestScan_FiltersLocally` — positive must be present, negative must be
     absent, metatags ride the upstream query verbatim.
   - `TestScan_StopsAtLimitAndAtListingEnd` — `complete: true` both ways.
   - `TestScan_PageCapReportsIncomplete` — 5 full pages, too few matches →
     `complete: false`, `scanned: 1000`.
   - `TestScan_DedupesRandomPages`.
   - R11 / R12 (added 2026-11-28 after a design review):
     `TestParsePostQuery_CountingMetatags` (`order:` / `user:` count,
     `rating:` / `status:` / `id:` do not; the 25-key list is complete),
     `TestScan_BudgetShrinksWithCountingMetatags` (`1girl arm_up sword`
     sends two tags, the same with `order:score` sends one, anonymous),
     `TestScan_RefusesWhenMetatagsUseTheBudget` and
     `TestScan_RefusesWithoutPositiveTag` (clear local errors, no request),
     `TestScan_NarrowsOnTagLimitRefusal` (a page request answering the
     sentinel is retried once with one tag fewer; a second refusal returns
     the error), `TestScan_RenamedLocalFilterUsesCurrentName` (a third
     positive with a 0-post row and an active alias matches under the new
     name; the same for a negative), and
     `TestScan_NoAliasLookupForPairMembersOrLiveFilters` (no alias request
     in the common case).
   - `TestSearchPosts_DirectPathByteIdentical` — ≤ direct-limit query: one
     request, payload without the scan fields.
   - `TestCountPosts_OverScan` — exact when the listing ends, lower bound
     otherwise.
   - `TestSearchPosts_TagLimitFallback` (httptest api + real service): a
     credentialed client's 3-tag query → 422 → scan, exactly one extra
     path; an anonymous client's 3-tag query never reaches the API as-is;
     a non-tag-limit 422 is returned untouched.
   - Existing pins that move: the 3-tag rejections in `tag_test.go` and the
     over-limit tests of 11-28-search-posts-space-miss (`blue hair
     order:random` now resolves to the empty-result path with
     `possible_tags`).
8. **tools** — description only (cap sentence + one scan sentence);
   schema pins. `main.go` passes `WithUpstreamTagLimit` from
   `client.Authenticated()`.
9. **Docs, same change** — CHANGELOG `[Unreleased]` (Added + Changed);
   README `search_posts` section; AGENTS.md: the "Rating filter boundary"
   paragraph's 2-tag sentences and a new boundary paragraph for the scan;
   `.trellis/spec/backend/resilience.md` (sentinel row) and
   `error-handling.md` (a classified fallback that is allowed).
10. **Integration** — rewrite check 9 (3 tags now succeed), add: the
    3-tag list query (every post carries all three, `complete: true`),
    the same 3 tags with `order:score` (a single-tag scan: `upstream_query`
    names one tag), the colon-tag query, the 3-tag count, 7 tags refused,
    and a pin that the light payload still carries `large_file_url`, `score`,
    `fav_count` and the tag lists. The scan calls cost several requests
    each: raise the stdin hold time (`DANBOORU_MCP_IT_SLEEP` default) so the
    last response still lands inside it, and make the echo line count
    requests rather than the highest id.
11. **Verify** — `gofmt -l .`, `go vet ./...`, `go test ./...`,
    `go test -race ./...`, `make build` + `bash scripts/integration-test.sh`.

## Credentialed live check

The maintainer provided a free-level test account on 2026-11-28 so the agent
can exercise the credentialed path itself. Its credentials live only in the
gitignored `.env` at the repo root (`.gitignore` carries `.env` and `.env.*`).
Rules for that check:

- Source the file explicitly (`set -a; . ./.env; set +a`), pipe every output
  through a redaction filter for both values, and never commit the file or
  echo it. After touching it, confirm `git status` does not list it.
- The account is a `Member`: 3 tags answer directly, 4+ return 422 "more
  than 3 tags" (verified 2026-11-28), so both branches are testable live.
- With the built exe: `search_posts` with 3 tags → a direct answer (no scan
  fields); with 4 and 6 tags → split answers carrying `upstream_query` and
  `complete`; with 7 tags → refused locally.
- Anonymous stays covered by the integration script, which never reads the
  file.

## Validation commands

- `go test ./internal/api/... ./internal/service/... -run 'Scan|TagLimit|ParsePostQuery|CountPosts' -v`
- `make test`, `make test-race`, `bash scripts/integration-test.sh`

## Rollback points

- Steps 2–3 (api) and 4–6 (service) are one coherent change; revert the
  single commit to roll back.
