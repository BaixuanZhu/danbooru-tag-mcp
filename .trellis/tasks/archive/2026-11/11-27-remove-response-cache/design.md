# Design — remove the response cache

## Target shape

`Client.Get` returns `([]byte, error)` and is a straight line again:
credential injection, `waitTurn`, `doWithRetry`. No store, no TTL, no cache
key, no fresh-hit shortcut, no fallback. A request that still fails after
retries is an error.

## internal/api/client.go

Removed: the `internal/cache` import (line 19), `tagsExactClass` (40-43),
`defaultCacheTTLs` (45-62), `ttlClass` (64-72), the `store` and `cacheTTLs`
fields (93-94), `WithCache` (119-132), the TTL map copy in `NewClient`
(137-140, 149), the cache block of `Get` (key, fresh hit, stale body, `Put`;
195-216 and 249-269) and `isEmptyJSONList` (300-303). `transientError`
(317-324) stays as a type, renamed and repurposed (see "Upstream failures").

Signatures: `Get(ctx, path, params) ([]byte, error)` and
`doWithRetry(ctx, reqURL) ([]byte, error)`; the seven `Fetch*` methods
return `([]byte, error)`.

`doWithRetry` keeps its classification of attempts. When retries are
exhausted on a transient class it returns that last error wrapped as an
upstream failure; the inner errors are the transport error (made
credential-safe by `doOnce`), `rateLimitedError(...)` for 429/421 and
`httpStatusError(...)` for 5xx.

The concurrency contract stays and keeps its comment: `c.mu` guards only the
rate-limit accounting; sleeps and network execution run unlocked.

## internal/service

`TagFetcher` (`tag.go:83-95`) returns `([]byte, error)` on all seven
methods. `CacheMarks` and `marksFrom` (`tag.go:97-115`) are deleted, and so
is the embedded field in `TagInfo`, `SearchResult`, `RelatedResult`,
`AliasResult`, `WikiResult` (`tag.go:142-170`) and `TagsInfo`
(`lookup.go:29`). The two "profiles never carry cache markers" lines in
`profile.go` (164, 210) go. The `time` import of `tag.go` is reviewed (it
was used by `marksFrom`).

## internal/tools

`withRefresh` (`register.go:86-94`), the `internal/cache` import and the six
`ctx = withRefresh(ctx, args)` lines go. The six tag tools lose
`mcp.WithBoolean("refresh", ...)` (133, 144, 158, 169, 188, 236) and the
clause "Serves expired cached data marked stale+cached_at when Danbooru is
unreachable; refresh:true re-fetches live and updates the cache".
`getBoolArg` stays (`include_description` uses it).

## Upstream failures (PRD R10-R12)

### The shared marker

`internal/app` gains one sentinel:

```go
// ErrUpstreamUnavailable is matched (errors.Is) by every error that means
// Danbooru could not serve a request even after retries: a transport
// failure, a 5xx, or a 429/421. The api layer raises it; the tools layer
// turns it into one uniform result on every tool.
var ErrUpstreamUnavailable = errors.New("danbooru unavailable")
```

`internal/app` is the existing leaf for cross-package infrastructure and
imports nothing internal, so `api` and `tools` can both depend on it
without touching the layer direction (`tools` still never imports `api`).

### internal/api/client.go

`transientError` becomes `upstreamError`: same field, same `Error()` (the
cause alone), plus `Is(app.ErrUpstreamUnavailable) == true`. `doWithRetry`
wraps the last error with it when retries are exhausted on a transient
class. A non-retryable 4xx, a caller cancellation and a fatal error are
returned unwrapped, as today.

### internal/tools/register.go

One helper replaces the eight hand-written error branches:

```go
// failResult maps a service error to a tool result. An upstream failure
// becomes the same marked result on every tool, so an agent stops calling
// instead of trying other names and tools; anything else keeps the tool's
// own code as a plain text result.
func failResult(err error, code string) (*mcp.CallToolResult, error)
```

- Upstream failure: `mcp.NewToolResultError` carrying
  `{"error":"upstream_unavailable","message":"Danbooru is not answering
  right now: <err>. All tools of this server are affected; retry later."}`.
- Anything else: today's `errResp(code, err.Error())`, a plain text result.

The eight handlers call it with their current codes. `get_tag_info` gains
`tag_info_failed` for its errors that are neither a not-found nor an
upstream failure (today they are reported as `tag_not_found`). The
not-found branches of `get_tag_info` and `get_tag_profile` stay in front of
it and unchanged.

## main.go

`clientOptions` (93-107) is deleted and `serve` calls `api.NewClient()`. The
`DANBOORU_MCP_NO_CACHE` line leaves the usage text (142). `os` is still
used (`log.SetOutput`); `path/filepath` becomes unused.

## Tests

Deleted:

- `internal/cache/` — all 14 tests.
- `internal/api/client_test.go` — `newCachedTestClient` (39-44), the
  `internal/cache` import, and 13 tests: `TestDefaultCacheTTLs_Pinned`,
  `TestTtlClass_TagsQueryShape`, `TestGet_CacheFreshHitIsTransparent`,
  `TestGet_CacheKeyIgnoresCredentials`,
  `TestGet_CacheTTLExpiryRefetches`,
  `TestGet_TagsExactOutlivesSearchTTL`,
  `TestGet_EmptyArrayBodyNotCached`,
  `TestGet_StaleServeAfterRetriesExhausted`,
  `TestGet_NonRetryableErrorDoesNotStaleServe`,
  `TestGet_StoreFailureFallsThroughToNetwork`,
  `TestGet_RefreshBypassesFreshHitAndBumpsStoredAt`,
  `TestGet_RefreshFailureStillStaleServes`,
  `TestGet_CacheHitNotBlockedByInFlightRetry`.
- `internal/tools/register_test.go` —
  `TestToolSchemas_NoRefreshOnUncachedTools` (115-128).
- `internal/service/tag_test.go` —
  `TestCacheableResponses_StaleMarksMarshaled`.
- `internal/service/profile_test.go` —
  `TestProfile_NeverCarriesCacheMarks`.

Modified:

- `register_test.go` `TestToolSchemas_PropertiesExist` (88-97): drop the six
  `refresh` rows.
- `tag_test.go` mock fetcher: stubs return `[]byte`.
- every `res.Body` in service code and tests becomes the returned body.

Added:

- `TestGet_NonRetryable4xxReturnsAfterOneAttempt` in `client_test.go`: the
  422 half of the deleted stale-serve test without the cache — one attempt,
  the shrunken error surfaces. Today that invariant is pinned only by the
  test being deleted (PRD R6).
- `TestGet_UpstreamFailuresAreMarked` in `client_test.go`: a 5xx, a 429 and
  a refused connection, each after retries, satisfy
  `errors.Is(err, app.ErrUpstreamUnavailable)`; a 4xx and a caller
  cancellation do not (PRD R10, R12).
- `TestHandlers_UpstreamFailureIsUniform` in `internal/tools`: a mock
  `TagService` whose every method returns an error wrapping the sentinel;
  all eight tools answer `IsError == true` with the same `error` code
  (PRD R10).
- `TestChain_UpstreamFailureReachesTheClient` in `client_test.go` (package
  `api`, so backoffs can be zeroed): a local 503 server, the real service,
  the real handlers — the marked result comes out; a real not-found from
  `get_tag_info` still returns the unmarked `tag_not_found` payload with its
  `wd14` verdict (PRD R11).

## Docs and scripts

| File | Change |
|---|---|
| `AGENTS.md:45-50` | `Client.Get` bullet: drop the response-cache clause |
| `AGENTS.md:51-57` | delete the `internal/cache/` bullet |
| `AGENTS.md:82-120` | rewrite as "Retry + error shrink (reliability boundary, test-enforced, do not break)": keep retry classes, backoff, `Retry-After`, the cancellation rule, error shrink, request-gap tiers and the credential-safe error text; drop every cache sentence; add the upstream marker contract (R10-R12) |
| `AGENTS.md` `internal/app/` bullet | mention `ErrUpstreamUnavailable` |
| `AGENTS.md:252` | batch paragraph: the alias bound is no longer "amortized to zero by the 7d alias cache" — it is paid on every call |
| `AGENTS.md:326-328` | CI paragraph: the smoke test no longer disables a cache |
| `.trellis/spec/backend/cache-and-resilience.md` | replace with a resilience-only guide (retry classes, cancellation, error shrink, credential-safe error text, request gap, the uniform upstream result), renamed `resilience.md` |
| `.trellis/spec/backend/index.md:20` | index row follows the rename |
| `.trellis/spec/backend/error-handling.md:48-65` | the lesson "a fallback must classify errors first" stays as a lesson; note that the `transientError` sentinel left with the cache |
| `README.md:50` | "Outage-tolerant" bullet: retries and shrunken errors, no cache claim |
| `README.md:128-133, 139-144, 146-175, 201` | drop `refresh` from the tool table; delete the tools paragraph, the "Response cache" section and the env-var row |
| `CHANGELOG.md` | `## [0.7.0] - Unreleased`: Removed, with the breaking notes |
| `server.json:4` | description without "cached search" (the version is bumped at tag time) |
| `scripts/integration-test.sh:11-17, 39-42` | drop `DANBOORU_MCP_NO_CACHE` and the cache wording |
| `installer/danbooru-tag-mcp.nsi:128-131` | keep the two `RMDir` lines; comment: leftover from 0.6.x |
| `install.ps1:229` | uninstall hint: a leftover cache directory from 0.6.x can be deleted |

## Compatibility

- Breaking for API consumers: `refresh` (six tools) disappears; `stale` and
  `cached_at` never appear again. Neither was observed in use.
- `DANBOORU_MCP_NO_CACHE` becomes a no-op if it is still set.
- Upstream request volume returns to the pre-0.6.0 level; composite calls
  pay their full cost every time (PRD R9).
- During an outage every call ends in the uniform `upstream_unavailable`
  result (PRD R10). Agents that matched the old per-tool codes, or the
  misleading `tag_not_found`, must switch to the new code.
- Existing cache directories are inert (PRD R8).

## Trade-offs

- Given up: about 292 s of tool latency per 64 days at historical traffic,
  and the one fallback rescue in 64 days (PRD Background).
- Gained: about 9% of production lines, 389 test lines, about 250 tokens of
  tool description per client session, one fewer documented boundary, and
  responses that are always live.

## Rollback

Revert the removal commits. A released 0.7.0 cannot be downgraded by
configuration; users who want the cache stay on 0.6.x.
