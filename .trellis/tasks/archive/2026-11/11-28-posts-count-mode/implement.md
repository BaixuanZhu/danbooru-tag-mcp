# Count mode for search_posts — design and execution plan

The parameter-shape question settled during planning (2026-11-28), so the
design is folded into this file instead of a separate `design.md`.

## Design

### Parameter shape

- A boolean `count_only` on `search_posts`, opt-in like `include_description`.
  Chosen over `mode: "count"` (collides conceptually with the existing
  `tags_mode` enum) and over a ninth tool (every tool costs context in every
  session; one clause on an existing tool is cheaper).
- In count mode `limit` and `tags_mode` do not apply: they are not read and
  not validated (`limit: 21` with `count_only` is fine — no posts are
  fetched).

### Request and response

- One request: `GET /counts/posts.json?tags=<tags>` through `Client.Get`
  (the single choke point — retry, shrink and the upstream marker apply for
  free). The api method is `FetchPostCount(ctx, tags string) ([]byte, error)`;
  an empty `tags` omits the parameter (the related-tags lift task reuses it
  for the whole-site count).
- Payload: `{"query": <tags as sent, trimmed>, "total": <int>}` from a new
  service type `PostCount`.
- Always live: nothing is stored between calls.

### Validation (identical to post mode)

- The same local 2-content-tag check runs before any request, with the same
  error text (including the joined-tag hint the space-split task adds to
  it). Extract the check shared by `SearchPosts` and the new `CountPosts`
  into one private helper so the two modes cannot drift.
- Metatags are NOT rejected: the endpoint honors them (rating sums exactly,
  order is ignored for counting).

### Failure mapping

- `{"counts":{"posts":null}}` (Danbooru's count timed out) is an unmarked
  error with the tool's own code:
  `{"error":"post_search_failed","message":"danbooru could not count this
  query (count timed out); simplify the query or retry later"}`. Never `0`,
  never `upstream_unavailable`.
- 422 (over the tag limit upstream) surfaces as the usual shrunken unmarked
  error after one attempt.
- A network/5xx/429 failure of the counts request is marked by the api layer
  and becomes the uniform `upstream_unavailable` result.

## Execution plan

1. Baseline: `go build ./... && go vet ./... && go test ./...` green.
2. api: `FetchPostCount` + tests (path and params with/without tags; a 503
   comes back marked).
3. service: `TagFetcher.FetchPostCount`, `mockFetcher` double fields,
   `PostCount`, `CountPosts`, shared query validation; tests for parsing,
   verbatim tags (no rating injection — mirror `TestSearchPosts_NoRatingInjection`),
   local validation before any request, the null count, an unparsable body,
   and upstream errors passing through marked.
4. tools: `TagService.CountPosts`, `count_only` in the schema, handler
   routing, `brokenService` double, uniform-outage case for count mode,
   schema pins.
5. Docs in the same change: CHANGELOG `[Unreleased]` / Added, README
   `search_posts` parameter line and one sentence, AGENTS.md boundary
   paragraph, integration calls + assertions.
6. Verify: `gofmt -l .`, `go vet ./...`, `go test ./...`, `go test -race ./...`,
   `make build` + `bash scripts/integration-test.sh`.

## Validation commands

- `go test ./internal/api/... ./internal/service/... ./internal/tools/... -run 'Count' -v`
- `make test`, `make test-race`, `bash scripts/integration-test.sh`

## Rollback

Single commit; revert restores the eight-parameter `search_posts` with no
stored state involved.

## As built (2026-11-28)

- The count code sits in `internal/service/count.go` (`ErrPostCountTimedOut`
  is exported so the tools tests use the real value; `PostCount`,
  `CountPosts`, `parsePostCount`); the shared validation is
  `TagService.checkPostQuery` in `tag.go`.
- An explicit `null` and upstream shape drift are told apart on purpose: only
  the literal `null` is the timed-out error; a missing `counts.posts` key or
  a non-integer value is a parse error quoting at most 32 bytes of the value,
  so drift is never reported as a retryable timeout.
- Left as found (outside this design): a whitespace-only `tags` passes the
  handler's `tags == ""` rejection in both modes, so count mode answers the
  whole-site total for it.
