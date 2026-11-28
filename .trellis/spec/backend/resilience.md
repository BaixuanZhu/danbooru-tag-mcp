# Resilience Guidelines

> Conventions for the retry policy, error shrink, and the uniform
> upstream-unavailable result in `internal/api` / `internal/tools`
> (retry + shrink introduced by task 11-04-reliability-cache-retry;
> the response cache it shipped with was removed in 0.7.0 by task
> 11-27-remove-response-cache — every tool answers from live Danbooru, and
> the sentinel that used to gate the stale fallback now marks upstream
> failures instead). Domain-specific boundary text lives in AGENTS.md; this
> file carries the engineering contract.

---

## Scenario: outbound-request resilience layer

### 1. Scope / Trigger

Any change to `internal/api/client.go` (the single outbound choke point),
the tools-layer failure mapping in `internal/tools/register.go`
(`failResult`), or any new fallback/retry path that masks upstream errors.
Cross-layer contract (`TagFetcher` returns `([]byte, error)`) and env
wiring (`DANBOORU_REQ_GAP_MS`) are involved.

### 2. Signatures

```go
// internal/app — the leaf sentinel both api and tools depend on
var ErrUpstreamUnavailable = errors.New("danbooru unavailable")
// second sentinel: a classified 422 refusal that may trigger a fallback
var ErrTagLimitExceeded = errors.New("danbooru tag limit exceeded")

// internal/api
type upstreamError struct{ err error } // Is(app.ErrUpstreamUnavailable) == true
// wraps a 422 whose body carries "TagLimitError"; Error() is the unchanged
// shrunken text, Is matches ONLY app.ErrTagLimitExceeded
type tagLimitError struct{ err error }
// Get(ctx, path, params) ([]byte, error); the eight Fetch* likewise
// FetchPosts(ctx, tags, limit, page) sends only=<light fields> on every call
// doWithRetry(ctx, reqURL) ([]byte, error)

// internal/tools
func failResult(err error, code string) (*mcp.CallToolResult, error)
```

### 3. Contracts

- Retry classes are exactly three: 5xx, transport/network errors, and
  429/421. A definitive 4xx is an answer from a reachable upstream — one
  attempt, never retried, never marked.
- When retries are exhausted on a transient class, `doWithRetry` wraps the
  LAST error (transport error made credential-safe by `doOnce`,
  `rateLimitedError` for 429/421, `httpStatusError` for 5xx) in
  `*upstreamError`. Its `Error()` is the cause alone; `Is` matches only
  `app.ErrUpstreamUnavailable`, so `errors.Is` against
  `context.Canceled` / `DeadlineExceeded` keeps working through `Unwrap`.
- The tools layer maps errors through `failResult`: an upstream failure
  becomes `isError: true` with
  `{"error":"upstream_unavailable","message":"Danbooru is not answering
  right now: <cause>. All tools of this server are affected; retry
  later."}` — the SAME result on every tool, so an agent learns one rule
  (stop calling this server). Anything else keeps the tool's own code as a
  plain text result (`isError: false`).
- `tag_not_found` is reserved for a genuine not-found (`*NotFoundError`,
  payload carries the local `wd14` verdict); other `get_tag_info` failures
  use `tag_info_failed`. Never reuse a not-found code for an outage — that
  makes agents try other spellings, all futile.
- Concurrency contract: `c.mu` guards ONLY the rate-limit accounting
  (`lastReq`); sleeps (`waitTurn` account-then-sleep loop), network
  execution (retries, backoffs, Retry-After waits) run unlocked. mcp-go's
  stdio server runs concurrent tool-call workers, so no slow caller may
  park the mutex.
- Transport errors are sanitized at their single creation point (`doOnce`
  routes every error through `sanitizeTransportError`): URL reduced to
  scheme + host + path, because the query carries the injected credentials
  and error text lands verbatim in agent context and logs.

### 4. Validation & Error Matrix

| Condition | Behavior |
|---|---|
| 5xx / network error / 429 | retry (up to 2, 1s/3s, ctx-interruptible) |
| 429 with Retry-After | first backoff replaced by header value, capped 30s |
| retries exhausted on a transient class | error wrapped in `*upstreamError`; tools return the uniform marked result |
| ctx canceled | abort immediately: no retry, plain error, NOT marked |
| definitive 4xx (401/403/404/422) | shrunken error after one attempt; NOT marked |
| 422 whose body carries `TagLimitError` | wrapped in `*tagLimitError` (text unchanged); service may fall back to the local scan via `errors.Is(err, app.ErrTagLimitExceeded)` only — any other consumer sees the plain error |
| validation / parse error (service layer) | tool's own code, plain text result; NOT marked |
| HTML error body | error carries status + `<title>` only |
| non-HTML error body | truncated to 300 bytes |
| transport failure error text | sanitized: URL keeps scheme + host + path only — query/userinfo dropped (`TestGet_TransportErrorsOmitCredentials`) |
| genuine not-found | unmarked `tag_not_found` payload with `wd14` verdict (get_tag_info / get_tag_profile) |

### 5. Good/Base/Bad Cases

- Good: 503 outage -> 3 attempts -> the client sees `IsError == true` and
  `upstream_unavailable` on whichever tool it called, with the shrunken
  `danbooru 503 (Downbooru)` cause in `message`.
- Base: every unit test uses `WithReqGap(0)` and injects zero backoffs via
  the Client fields — no real sleeps in tests (the 429 marker test keeps a
  1s `Retry-After` to prove the header path).
- Bad (caught in review): mapping errors to results with `err != nil`
  checks that ignore the sentinel — a parse error would then masquerade as
  an outage, or an outage as a missing tag. Always branch on
  `errors.Is(err, app.ErrUpstreamUnavailable)` FIRST (failResult), and on
  `errors.As(err, &nf)` for the not-found rider, before any generic code.

### 6. Tests Required

- Transient classification and marking:
  `TestGet_UpstreamFailuresAreMarked` (5xx / 429 / refused marked; 4xx and
  cancellation unmarked), `TestGet_RetriesExhaustedErrors`,
  `TestGet_ContextCancelAbortsWithoutRetry`,
  `TestGet_NonRetryable4xxReturnsAfterOneAttempt` (one attempt, shrunken
  error, unmarked).
- Uniform result: `TestHandlers_UpstreamFailureIsUniform` (all 8 tools,
  mock service), `TestChain_UpstreamFailureReachesTheClient` (real chain
  against a local 503; plus a genuine not-found staying unmarked with its
  `wd14` verdict).
- Credential hygiene: `TestGet_TransportErrorsOmitCredentials`.
- Shape stability: `TestSearchPosts_TagsModeDefaultFullIsByteIdentical`.
