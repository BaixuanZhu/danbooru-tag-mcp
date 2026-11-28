# Tool Contracts

> Executable request/response contracts of the MCP tools: one scenario per
> behavior that spans layers (tools -> service -> api). Error classification
> rules live in [Error Handling](./error-handling.md); retry, error shrink
> and the upstream marker in [Resilience](./resilience.md).

---

## Scenario: search_posts count mode (`count_only`)

### 1. Scope / Trigger

- Cross-layer contract (task 11-28-posts-count-mode, 2026-11-28): one tool
  parameter switches the endpoint and the payload shape.

### 2. Signatures

- Tool: `search_posts(tags, count_only?: boolean = false, limit?, tags_mode?)`.
- api: `func (c *Client) FetchPostCount(ctx, tags string) ([]byte, error)`
  (`GET /counts/posts.json`; `tags` is set only when non-empty, so an empty
  value counts the whole site; always through `Client.Get`).
- service:
  `type PostCount struct { Query string `json:"query"`; Total int `json:"total"` }`,
  `func (s *TagService) CountPosts(ctx, tags string) (*PostCount, error)`,
  `var ErrPostCountTimedOut = errors.New(...)`.
- tools: `TagService.CountPosts`; the handler routes on
  `getBoolArg(args, "count_only")` after its `tags == ""` rejection and maps
  every error through `failResult(err, "post_search_failed")`.

### 3. Contracts

- Request: `tags` as sent (trimmed) and verbatim. `limit` and `tags_mode`
  are neither read nor validated in count mode.
- Response: `{"query":"<tags>","total":<n>}`, `query` first. Always live;
  nothing is stored between calls.
- Validation: the same local content-tag cap `SearchPosts` runs
  (`checkPostQuery`), identical text with the space-split hint, before any
  request.
- Metatags are not rejected: the endpoint honors them (live 2026-11-28: the
  four `rating:` slices of `arm_up` sum exactly to its unfiltered total).
  No layer injects a rating filter.

### 4. Validation & Error Matrix

| Condition | Result |
|---|---|
| `tags` empty | `post_search_failed` "tags parameter is required" (handler, no request) |
| over the content-tag cap | the shared local error, no request, unmarked |
| integer `counts.posts` | `{"query":...,"total":<n>}` |
| explicit `{"counts":{"posts":null}}` (Danbooru's count timed out) | `ErrPostCountTimedOut` -> unmarked `post_search_failed`, message "danbooru could not count this query (count timed out); simplify the query or retry later"; never `0`, never `upstream_unavailable` |
| `counts.posts` missing or not an integer (shape drift) | parse error "failed to parse post count: ..." quoting at most 32 bytes of the value; never reported as a timeout |
| 422 (upstream tag limit) | shrunken unmarked error after one attempt |
| transport / 5xx / 429 on the counts request | uniform `upstream_unavailable` |

### 5. Good / Base / Bad Cases

Example values are from the anonymous probe of 2026-11-28.

- Good: `{"tags":"arm_up rating:g","count_only":true}` ->
  `{"query":"arm_up rating:g","total":108422}`.
- Base: `{"tags":"blue_hair","count_only":true,"limit":21}` -> a count;
  `limit` is ignored, not rejected.
- Bad: `{"tags":"arm_up filetype:png","count_only":true}` -> the timed-out
  error, never `{"total":0}`.

### 6. Tests Required

- api: `TestFetchPostCount_*` (URL with and without tags, 503 marked, 422
  unmarked after one attempt).
- service: `TestCountPosts_*` (parsing, the three outcomes, verbatim tags,
  validation before any request).
- tools: `TestSearchPostsHandler_CountModePayloadBytes` and
  `TestSearchPostsHandler_PayloadBytesUnchanged` (list mode unchanged with
  `count_only` absent and `false`, pinned by
  `TestSearchPostsHandler_CountOnlyFalseKeepsListModeBytes`); the
  count-mode row of `TestHandlers_UpstreamFailureIsUniform`; the chain test
  `TestChain_CountModeReachesTheClient`.
- Integration check 25: the count of `blue_hair` matches its tag row's
  `post_count` within 10.

### 7. Wrong vs Correct

Wrong: any error becomes a default or an outage.

```go
total, err := countOrZero(res) // null -> 0, drift -> 0
if err != nil {
    return nil, app.ErrUpstreamUnavailable // a timeout dressed up as an outage
}
```

Correct: the service tells the three outcomes apart, the handler classifies.

```go
total, err := parsePostCount(res) // null -> ErrPostCountTimedOut; drift -> parse error
if err != nil {
    return nil, err // failResult: unmarked text, or upstream_unavailable only for a marked error
}
```

---

## Scenario: get_related_tags lift ranking

### 1. Scope / Trigger

- Cross-layer contract (task 11-28-related-tags-lift, 2026-11-28): a new
  response field, a changed entry order, a larger upstream request, and a
  per-process memo shared by concurrent tool calls.

### 2. Signatures

- Tool: `get_related_tags(tag, limit? = 10)`; the schema is unchanged and
  the description gains one clause.
- api: `FetchRelated(ctx, tag)` -> `GET /related_tag.json?query=<tag>&limit=500`
  (`relatedPoolSize`); `FetchPostCount(ctx, "")` supplies the whole-site
  count.
- service: `RelatedTag.Lift *float64` (`json:"lift,omitempty"`),
  `danbooruRelatedResponse.PostCount`, `sitePostCount(ctx) (int, bool)`,
  `relatedLift(frequency, postCount, sitePosts) *float64`,
  `rankRelatedByLift(candidates, queryPosts, sitePosts) []RelatedTag`,
  `relatedSupportFloor = 5`, `relatedSampleCap = 5000`.

### 3. Contracts

- `lift(B|A) = frequency * N / B`, rounded to two decimals (`frequency` is
  Danbooru's own estimate from up to 5000 sampled posts of A).
- `support = frequency * min(A, 5000)`; entries with `support >= 5` are
  ranked by lift descending with a stable sort (upstream frequency order
  breaks ties); the rest follow in upstream order, never dropped; `limit`
  is applied after the ranking.
- `lift` is attached to every candidate whenever N is available; an entry
  with `post_count == 0` or `frequency == 0` carries none.
- N is memoized in memory only, success only, with the mutex held across the
  request (concurrent callers share one fetch).

### 4. Validation & Error Matrix

| Condition | Behavior |
|---|---|
| related request fails after retries | uniform `upstream_unavailable`; no N request |
| not-found tag (no candidates) | `{"related":[]}`; no N request |
| N request fails (any class), unparsable body, explicit null, or a value <= 0 | Danbooru's order, no `lift` key; N is requested again on the next call |
| top-level `post_count` missing or 0 | every support is 0: upstream order, `lift` attached |

### 5. Good / Base / Bad Cases

- Good: for `arm_up` the first entry is a tag-specific pose companion
  (lift about 14.8 in the 2026-11-28 probe), not `1girl`.
- Base: `limit=3` returns the three highest-lift supported entries.
- Bad: the N request answers 503 -> the pre-lift payload, field for field;
  the next call recovers on its own.

### 6. Tests Required

- `TestRelated_LiftMathAndRanking`, `TestRelatedLift_FormulaRoundingAndGuards`
  (two decimals and the nil guards), `_SupportFloorSinksLowOverlapEntries`,
  `_SupportIsCappedAtTheSampleSize`, `_ZeroPostCountKeepsUpstreamOrder`,
  `_UncomputableLiftIsOmitted`, `_EqualLiftsKeepUpstreamOrderAfterSwaps`
  (the stable sort), `_NonPositiveLimitKeepsAll`, `_SiteCountFetchedOnce`,
  `_SiteCountFailureDegradesAndRetries`, `_EmptyResultSkipsSiteCount`,
  `_PayloadBytesWithoutSiteCount`, `_ConcurrentCallsFetchSiteCountOnce`
  (run under the race detector); api `TestFetchRelated_BuildsCorrectURL` and
  the real-chain `TestChain_RelatedLiftReachesTheClient`; tools
  `TestGetRelatedTagsHandler_PayloadBytes`, `TestGetRelatedTagsTool_DescriptionNamesLiftOnce`;
  integration check 26.
- A mutation battery (through `go test -overlay`, never on the tree) must
  stay at zero survivors: unstable sort, no support floor, no rounding, no
  memo, sunk ranked first, formula inverted, failure memoized, mutex not
  held across the fetch, empty answer requesting N, limit before ranking,
  empty answer as `null`, a missing or moved `min(A, 5000)` cap, rounding to
  one or three decimals, and a `FetchRelated` that bypasses `Client.Get`.

### 7. Wrong vs Correct

Wrong: a sort that does not promise stability loses the tie-break.

```go
sort.Slice(ranked, func(i, j int) bool { return relatedLiftHigher(ranked[i], ranked[j]) })
```

Correct: stability is part of the contract, and it is pinned on an input an
unstable sort scrambles.

```go
sort.SliceStable(ranked, func(i, j int) bool { return relatedLiftHigher(ranked[i], ranked[j]) })
```
