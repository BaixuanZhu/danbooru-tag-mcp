# Cache and Resilience Guidelines

> Conventions for the response cache, retry policy, and fallback behavior
> in `internal/cache` / `internal/api` (introduced by task
> 11-04-reliability-cache-retry). Domain-specific boundaries (TTL values,
> which paths are cacheable) live in AGENTS.md; this file carries the
> engineering contract.

---

## Scenario: outbound-request resilience layer

### 1. Scope / Trigger

Any change to `internal/api/client.go` (the single outbound choke point),
`internal/cache/*`, or any new fallback/retry path that masks upstream
errors. Cross-layer contract (`TagFetcher` returns `cache.Result`) and env
wiring (`DANBOORU_REQ_GAP_MS`, `DANBOORU_MCP_NO_CACHE`) are involved.

### 2. Signatures

```go
// internal/cache — stdlib-only leaf package (same discipline as internal/vocab).
type Result struct { Body []byte; Stale bool; CachedAt time.Time }
func Open(dir string) (*Store, error)
func (s *Store) Get(key string) (body []byte, storedAt time.Time, ok bool)
func (s *Store) Put(key string, body []byte) error
func WithRefresh(ctx context.Context) context.Context
func RefreshRequested(ctx context.Context) bool

// internal/api
func WithCache(dir string) Option          // Open failure -> WARN + cache-less, never fatal
// TagFetcher (service): all FetchX return (cache.Result, error)
```

### 3. Contracts

- Cache key = `path + "?" + params.Encode()` computed on a COPY of params
  BEFORE credential injection. Never let `login`/`api_key` near the key —
  a mutated shared map leaks credentials into keys and splits the cache
  per user (`TestGet_CacheKeyIgnoresCredentials`).
- Never-cache classes: `/posts.json` (random-sample semantics) and `[]`
  bodies (dead-end searches must not delay new-tag visibility).
- Fresh hits are transparent: byte-identical response, no markers, and
  they skip the rate-limit wait. Only stale fallback adds `stale: true` +
  `cached_at` (RFC3339), on exactly the 6 cacheable tool responses.
- Per-call control crossing layers (tools -> service -> api) goes through
  ctx flags (`cache.WithRefresh`), never through interface signature
  growth. Service passes ctx opaque; api reads the flag inside `Get`.
- Store layout: one JSON file per key under `<dir>/v1/<sha256>.json`,
  atomic temp+rename, dual cap 2000 entries / 64MB, oldest-mtime swept
  first. All Store errors degrade to miss; nothing panics.

### 4. Validation & Error Matrix

| Condition | Behavior |
|---|---|
| 5xx / network error / 429 | retry (up to 2, 1s/3s, ctx-interruptible) |
| 429 with Retry-After | first backoff replaced by header value, capped 30s |
| ctx canceled | abort immediately: no retry, NO stale fallback |
| retryable failure, retries exhausted, expired entry exists | stale-serve with marker + stderr WARN |
| definitive 4xx (401/403/404/422) | shrunken error; NEVER stale-serve |
| HTML error body | error carries status + `<title>` only |
| non-HTML error body | truncated to 300 bytes |
| Store corrupt/unwritable | WARN + behave cache-less |

### 5. Good/Base/Bad Cases

- Good: 503 outage -> retries -> stale wiki served with `stale:true`; next
  call with `refresh:true` after recovery refetches and resets the TTL.
- Base: every unit test uses `WithReqGap(0)` and injects zero backoffs /
  short TTLs via the Client fields — no real sleeps in tests.
- Bad (caught in review): gating stale fallback on `err != nil` instead of
  the transient classification — a revoked API key (403) then silently
  replays pre-revocation data forever.

### 6. Tests Required

- Transient classification: `TestGet_NonRetryableErrorDoesNotStaleServe`,
  `TestGet_RetriesExhaustedErrors`, `TestGet_ContextCancelAbortsWithoutRetry`.
- Cache contracts: `TestGet_CacheKeyIgnoresCredentials`,
  `TestGet_PostsNeverCached`, `TestGet_EmptyArrayBodyNotCached`,
  `TestGet_StaleServeAfterRetriesExhausted`,
  `TestGet_RefreshBypassesFreshHitAndBumpsStoredAt`.
- Shape stability: `TestSearchPosts_TagsModeDefaultFullIsByteIdentical`,
  `TestCacheableResponses_StaleMarksMarshaled` (fresh omits both fields).

### 7. Wrong vs Correct

#### Wrong

```go
if err != nil {
    if body, at, ok := c.store.Get(key); ok { // fires on ANY error
        return cache.Result{Body: body, Stale: true, CachedAt: at}, nil
    }
    return nil, err
}
```

#### Correct

```go
var te transientError
if errors.As(err, &te) { // only 5xx / network / 429 are transient
    if body, at, ok := c.store.Get(key); ok {
        return cache.Result{Body: body, Stale: true, CachedAt: at}, nil
    }
}
return nil, err
```

**Why**: a fallback that fires on definitive errors converts "your
credentials are revoked" into "here is yesterday's data" — the worst
failure mode a lookup service can have, because it is silent.
