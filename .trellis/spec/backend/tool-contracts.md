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
