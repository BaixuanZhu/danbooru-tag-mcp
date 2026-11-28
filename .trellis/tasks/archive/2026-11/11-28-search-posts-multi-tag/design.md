# Design — more than two content tags in search_posts

Facts: `research/multi-tag-local-and-facts.md` (live probes + upstream source,
2026-11-28).

## Goal and non-goals

Goal: `search_posts` accepts up to 6 content tags, answers exactly whenever
it can, and says so when it cannot.

Non-goals: wildcard / OR evaluation, caching, a caller-selectable scan size,
changing anything about Danbooru's own limits.

## Why a local scan is exact

Let Q = T1 ∧ … ∧ Tn ∧ M be the query (tags plus metatags) and
P = Ta ∧ Tb ∧ M the pair query sent upstream (Ta, Tb among the positive
tags). With the same `order:` metatag, Danbooru streams P's posts in the
same order O as it would stream Q's, and Q's posts are exactly the subset of
P's posts whose tag lists contain the remaining tags. Filtering P's stream
locally therefore reproduces Q in order O:

- stop once `limit` matches are collected → those are the first `limit`
  posts of Q: identical to Danbooru's own first page for Q;
- reach the end of P's listing → every post of Q was seen; fewer than
  `limit` matches means that is all of Q;
- page cap reached with fewer than `limit` matches → a true prefix of Q,
  possibly incomplete.

`complete` reports which of the three happened. Under `order:random` each
page is an independent draw, pages can overlap (dedupe by post id) and
"first `limit`" means "a sample of `limit`" — there is no top-N to match.

## Data flow

```
tools.search_posts(tags, limit, tags_mode, count_only)
  -> service.SearchPosts / CountPosts
       1. parsePostQuery(tags)      positives · negatives · metatags · unresolved colon tokens
                                   > 6 non-metatag tokens -> local error (before any request)
       2. terms = non-metatag tokens + counting metatags
            terms <= directTagLimit -> DIRECT: today's path, one request, query verbatim
       3. else SPLIT:
          a. one FetchTagsByNames over positives + unresolved colon tokens
               row found for a colon token -> positive (canonical name)
               no row                     -> pass-through token
          b. k = min(2, directTagLimit - counting metatags); k < 1 -> local error
             pair = the k positives with the smallest post_count (no row = 0)
          c. scan pages of "<pair> <pass-through tokens>"   (limit=200, only=<light fields>)
               keep a post iff every remaining positive is in its tag list
               and no negative is
               (a remaining positive or negative with a 0-post row is alias-resolved once)
          d. stop at `limit` matches (list mode) · short page · 5 pages
             a tag-limit 422 on a page request -> one retry with one tag fewer
       4. shape: tags_mode / limit as today; attach upstream_query, scanned, complete
```

Count mode runs the same scan without the `limit` stop and returns the number
of matches.

## Token classification

| Token | Class | Pair candidate | Local filter |
|---|---|---|---|
| `tag` | positive | yes | must be present |
| `-tag` | negative | no | must be absent |
| known metatag key (`rating:g`, `order:score`, `status:any`) | metatag | no — passed through | — |
| other colon token WITH a Danbooru row (`honkai:_star_rail`) | positive (canonical name) | yes | must be present |
| other colon token with NO row (`foo:bar`, a metatag missing from our list) | pass-through | no | — |
| contains `*`, or starts with `~` | refused when a scan is needed | — | — |
| `random:N` metatag | refused when a scan is needed: it caps the listing | — | — |

The 6-token cap counts every non-metatag token, before the colon tokens are
resolved (conservative; a pass-through token then only makes the query
cheaper, never invalid). Every content token joins the ONE batch lookup —
positives, unresolved colon tokens and exclusions alike — because a renamed
exclusion needs its row as much as a positive does.

## Term budget of the scan's own query

The pair query is itself a Danbooru query and obeys the same term count:
tags, plus every metatag that is NOT in `PostQuery::UNLIMITED_METATAGS`
(25 keys: `rating:`, `status:`, `id:`, ... are free; `order:`, `user:`,
`pool:`, `fav:`, `search:`, ... count — source and live probes in the
research file). So:

- `terms = non-metatag tokens + counting metatags` decides DIRECT versus
  SPLIT. A two-tag query with `order:score` is three terms for an anonymous
  client: today it 422s, with the scan it is answered.
- The scan sends `k = min(2, directTagLimit - countingMetatags)` tags: two
  when no metatag counts, one when one does.
- `k < 1`, or a scan with no positive tag at all (only negatives and
  metatags): a clear local error, no request.
- A credentialed account whose real limit is below the configured direct
  limit can still refuse a scan page with the tag-limit 422. That request is
  retried once with one tag fewer (the dropped tag becomes a local filter);
  a second refusal returns the error.

### `random:` is refused (found during implementation, 2026-11-28)

`random:N` replaces the listing by a sample of at most N posts (live:
`arm_up random:5` with `limit=200` answers 5 posts). Over such a listing a
short page does not mean the listing ended, so a scan would report
`complete: true` for a cut-off answer. A query that needs a scan and carries
`random:` is therefore refused with a local error and no request. `limit:`
caps nothing on the JSON API (the same probe with `limit:5` answered 200
posts), so it rides like any free metatag. Direct queries are untouched.

## Renamed tags as local filters

Posts list their tags under the current name, and an alias antecedent
(`sailor_suit`) keeps a 0-post placeholder row. A renamed tag inside the
upstream pair is resolved by Danbooru itself. One that becomes a local
filter (a third positive, or a negative such as `-old_name`) would never
match under its old name, and the scan would report "no matches, complete"
for a query Danbooru answers. So every local-filter token whose row shows
0 posts is resolved through the alias endpoint once (the lookup
`get_tag_info` already uses); the filter then uses the current name. A
0-post token without an active alias is simply dead: it filters everything
out (a positive) or nothing (a negative). The common case, where no
local-filter token has a 0-post row, costs no extra request.

## Direct limit and the tag-limit fallback

- Anonymous: `directTagLimit = 2`. More than 2 non-metatag tokens never
  reach Danbooru as-is (verified: always 422).
- Credentialed: `directTagLimit = 6`. The full query is tried; when Danbooru
  refuses it with a tag-limit 422 the service switches to the scan. A logged-in
  free account (limit 3) gets its 3-tag queries answered directly and its
  4–6-tag queries by scan.
- The refusal must be classifiable: `internal/app` gains
  `ErrTagLimitExceeded`; the api layer wraps a 422 whose body carries
  `TagLimitError` in a `*tagLimitError` whose `Is` matches that sentinel.
  Error TEXT is unchanged, so the tools layer keeps returning the plain
  `post_search_failed` result it returns today for a 422. The sentinel lives
  in `internal/app` for the same reason `ErrUpstreamUnavailable` does: the
  service must not import `internal/api`.
- The service branches on `errors.Is(err, app.ErrTagLimitExceeded)` only.
  Any other error — including another 422 and a cancellation — is returned
  untouched.
- "Credentialed" is one rule in one place: `Client.Authenticated()`
  (both `DANBOORU_LOGIN` and `DANBOORU_API_KEY` non-empty). `main` passes it
  to the service as `WithUpstreamTagLimit(6)`; the default stays 2.

## The scan

- Page request: `FetchPosts(ctx, query, 200, page)` — `limit=200` (the
  anonymous page cap) and `only=<light fields>`.
- `only=` is sent on EVERY posts request (direct, profile sample, scan): the
  service consumes exactly those fields and a post shrinks from 3,499 to 957
  bytes. The integration test pins that the light payload still carries
  every consumed field.
- Filter: the post's tag set is the union of its five `tag_string_*`
  fields; comparisons use canonical (lowercase) names.
- Bounds: `maxScanPages = 5` → at most 1,000 posts scanned per call.
- Cost: one batch lookup plus 1–5 pages. Typical 3-tag query: 2 requests
  (about 4 s anonymous); worst case 6 requests (about 12 s anonymous, about
  5 s with credentials).

## Contracts

```go
// internal/app
var ErrTagLimitExceeded = errors.New("danbooru tag limit exceeded")

// internal/api
func (c *Client) Authenticated() bool
// page <= 0 omits the parameter; only= is always sent
func (c *Client) FetchPosts(ctx, tags string, limit, page int) ([]byte, error)

// internal/service
const MaxQueryTags = 6          // content tags per query, both modes
func WithUpstreamTagLimit(n int) Option   // default: 2 (anonymous)

// list mode (extends the PostsResult of task 11-28-search-posts-space-miss)
type PostsResult struct {
    Posts         []Post `json:"posts"`
    PossibleTags  []string `json:"possible_tags,omitempty"`
    UpstreamQuery string   `json:"upstream_query,omitempty"` // the 2-tag query actually sent
    Scanned       int      `json:"scanned,omitempty"`
    Complete      *bool    `json:"complete,omitempty"`       // nil unless the query was split
}

// count mode (extends PostCount of task 11-28-posts-count-mode)
type PostCount struct {
    Query    string `json:"query"`
    Total    int    `json:"total"`   // exact iff Complete is true
    Scanned  int    `json:"scanned,omitempty"`
    Complete *bool  `json:"complete,omitempty"`
}
```

- The three scan fields appear only on split answers; direct answers are
  byte-identical to today's.
- `complete` is a pointer so an omitted field and `false` are distinct.

## Failure matrix

| Condition | Behavior |
|---|---|
| more than 6 non-metatag tokens | local `too many content tags … (max 6 …)`, no request |
| `*` / `~` token in a query that needs a scan | local error naming the token, no request |
| `random:` in a query that needs a scan | local error naming it, no request (it caps the listing) |
| counting metatags use the whole tag budget of a scan (`k < 1`) | local error naming them, no request |
| a scan with no positive tag (only negatives and metatags) | local error, no request |
| credentialed direct attempt → 422 `TagLimitError` | scan (classified via the sentinel) |
| tag-limit 422 on a scan page request | retried once with one tag fewer; a second refusal returns the plain error |
| local-filter token with a 0-post row | one alias lookup; the filter uses the current name |
| any request fails after retries | uniform `upstream_unavailable` |
| another 4xx (e.g. `pool:` counts upstream) | plain shrunken error, as today |
| page cap hit with fewer than `limit` matches | posts found so far, `complete: false` |
| caller cancellation | abort immediately, plain error, no retry |

## Compatibility

- Queries within the direct limit: no change at all.
- The local error text keeps its shape; "max 2" becomes "max 6" and appears
  only above 6.
- 3–6-tag queries that were rejected locally now succeed.
- Tool description: the cap sentence is rewritten and grows by two clauses
  (what happens beyond Danbooru's own term limit; the fields a split answer
  adds).
- The space-split diagnostic (`possible_tags`) now uses the exact key-list
  predicate too, so a pair whose first token carries a colon is a candidate:
  `kafka_(honkai: star_rail)` is named as `kafka_(honkai:_star_rail)` where
  it used to stay a bare empty answer.

## As built (2026-11-28)

- `internal/service/postquery.go` (`parsePostQuery`, `MaxQueryTags`,
  `anonymousTagLimit`, `unlimitedMetatagKeyList` lives in `metatag.go`) and
  `internal/service/scan.go` (`planScan`, `scan`, `scanPages`,
  `searchPostsByScan`, `countPostsByScan`); routing in `SearchPosts` /
  `CountPosts` through `checkPostQuery` and `directAttempt`.
- The api layer classifies the 422 in `doWithRetry` on the raw body
  (`isTagLimitRefusal`) and wraps it in `*tagLimitError`: `Error()` is the
  shrunken text, `Is` matches only `app.ErrTagLimitExceeded`.
- `only=` (`postListFields`) rides every posts request: direct answers, the
  profile sample and scan pages.
- Settled where the design left a choice: `scanned` counts distinct posts;
  the pair keeps query order (so does `upstream_query`: pair, then metatags
  and pass-through tokens as typed); narrowing drops the less rare tag; a
  `limit` of 0 or less stops the scan at `maxPostLimit` (20); the fallback
  from a refused direct query exists only when the configured limit is above
  the anonymous 2; `main` passes `MaxQueryTags` for a credentialed client.
- Verification: 47 mutants through `go test -overlay`, 46 killed, one
  equivalent (a row's name and its lowercased token coincide for every row a
  lookup can find).
- Known boundary (found by the check agent, accepted): pass-through colon
  tokens also ride the upstream query and consume one of its terms, so with
  `k = 1` such a query can surface Danbooru's raw 422 (with `k = 2` the
  narrowing absorbs it). The error is honest — Danbooru refuses the same
  query directly — and the formula stays as designed.

## Trade-offs considered

- **Memoizing the account's limit** (parsing "more than N" from the 422):
  rejected for v1 — it only saves one round trip for logged-in free accounts
  using 4–6 tags; revisit if that shows up in call history.
- **Looking the level up via `/users/{login}.json`**: rejected — one more
  endpoint and a copy of the level table, while the 422 fallback is needed
  anyway for drift.
- **A larger or configurable scan**: rejected — five pages is a constant;
  a parameter would cost context in every session for a rare case.
- **Classifying colon tokens by the metatag list alone**: rejected — a
  metatag missing from the list would become a local filter that never
  matches; the batch lookup makes the classification factual.

## Rollout / rollback

Lands after the other four 11-28 tasks, as one commit. Revert restores the
2-tag validator; there is no stored state.
