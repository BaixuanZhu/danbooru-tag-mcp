# Implementation plan — reliability pass

Ordered checklist; each step leaves the tree green (`go vet ./... && make
test`). Commit boundaries can fall between any two steps.

## Step 1 — api: error shrink + bounded retry (R2), no interface change yet

- [ ] `summarizeErrorBody(ct string, body []byte) string` (private, table-
      tested): HTML → `<title>` text; else 300-byte truncate.
- [ ] `Get`: extract the single `http.Do` into a helper; add retry loop
      (2 retries, 1s/3s backoff, ctx-interruptible, no retry on ctx cancel,
      429 → `Retry-After` capped 30s replaces first backoff).
- [ ] Wire `summarizeErrorBody` into the 4xx/5xx error path; 429/421
      message mentions `Retry-After` when present.
- [ ] Fix stale `FetchPosts` comment (`client.go:203`).
- [ ] Tests (`client_test.go`, httptest): 500-then-200 succeeds; exhausted
      retries error; ctx cancel aborts without retry; HTML body collapses
      to `danbooru 503 (Downbooru)`; long JSON body truncates at 300B;
      Retry-After honored.

## Step 2 — internal/cache package

- [ ] `Result`, `Store`, `Open/Get/Put` per design (per-key files
      `v1/<sha256>.json`, base64 body, atomic temp+rename, 2000-entry cap
      with oldest-first eviction).
- [ ] `cache_test.go`: round-trip, TTL-agnostic Get/Put, corrupt file →
      miss, missing dir → lazy create, eviction under entry and byte caps.

## Step 3 — api: cache integration + gap tiers (R1 core, R4b)

- [ ] TTL table; key before credential injection; fresh-hit short-circuit
      (skips rate-limit wait); `[]` bodies never cached; `/posts.json`
      never touches the Store.
- [ ] Stale-serve after retry exhaustion (stderr WARN, `Result{Stale}`).
- [ ] `WithCache(dir string)` Option; `DANBOORU_MCP_NO_CACHE` handled at
      the `main.go serve()` call site.
- [ ] Gap tiers in `NewClient`: `DANBOORU_REQ_GAP_MS` (0 valid, invalid →
      warn + tier default) > credentials → 500ms > 1500ms.
- [ ] Tests: key ignores credentials, TTL expiry per path, posts never
      cached, empty body not cached, stale-serve on 5xx, cache Store error
      falls through to network, env parsing (t.Setenv).

## Step 4 — TagFetcher returns cache.Result (interface churn)

- [ ] Change all 7 `FetchX` signatures in `internal/api` +
      `TagFetcher` (`tag.go:80`) + `mockFetcher` (`tag_test.go:31-60`).
- [ ] Service methods read `res.Body`; no behavior change yet.
- [ ] Full `make test` green (purely mechanical pass).

## Step 5 — stale markers + refresh control on cacheable tools (R1 visible half)

- [ ] Add `Stale`/`CachedAt` (`omitempty`) to the 6 cacheable response
      structs; fill from `Result` where the owning fetch can be stale.
- [ ] Tests: stale response marshals the two fields; fresh/absent omits
      them (pin the "transparent fresh hit" contract).
- [ ] `cache.WithRefresh`/`RefreshRequested`; 6 cacheable tools gain the
      `refresh` boolean param (handler wraps ctx); `Client.Get` skips the
      cache read and the success `Put` resets `stored_at`.
- [ ] Tests: `refresh:true` bypasses a fresh cache hit and bumps
      `stored_at`; refresh failure still stale-serves; posts/profile tools
      carry no param.
- [ ] Description clauses (register.go): stale-serve clause AND `refresh`
      clause on the 6 cacheable tools + `register_test.go` schema pins
      updated.

## Step 6 — R3 batch renamed_to

- [ ] `Infos`: `for i := range result.Found { s.resolveRenamedTo(ctx,
      &result.Found[i]) }`.
- [ ] Tests: 0-post rows get `renamed_to`, live rows issue no alias call
      (count via mock), alias failure non-fatal.
- [ ] `get_tags_info` description + AGENTS.md contract paragraph rewrite.

## Step 7 — R4a min_posts + R4c tags mode

- [ ] `Search(..., minPosts int)` post-unmarshal filter; verify
      `hasLiveTag`/wd14 fallback interplay in the tools handler (filtered-
      to-empty still counts as no-live-result).
- [ ] `SearchPosts(..., tagsMode)`: `none` (PostTags → omitempty), `diff`
      (query content-tag subtraction, metatags excluded).
- [ ] Tool params + description clauses + schema pins.
- [ ] Tests: default-0 unchanged, filter shrinks results, none/diff
      shapes, metatag exclusion from diff subtraction.

## Step 8 — wiring, docs, packaging

- [ ] `main.go serve()`: `WithCache(<LOCALAPPDATA>/danbooru-tag-mcp/cache)`
      unless `DANBOORU_MCP_NO_CACHE`; Open failure → no cache + stderr warn.
- [ ] `scripts/integration-test.sh`: export `DANBOORU_MCP_NO_CACHE=1`.
- [ ] NSIS uninstaller: remove cache dir; install.ps1/README notes.
- [ ] README (cache, env vars, 500ms tier, new params), AGENTS.md
      (internal/cache bullet, retry/gap/stale conventions), CHANGELOG
      `[0.6.0]`.
- [ ] `server.json` version bump (release-time).

## Step 9 — Get lock granularity fix

Rationale: mcp-go v1.1.1's stdio server runs 5 concurrent tool-call
workers (module source stdio.go:379,538), so concurrent `Client.Get`
calls are production reality — but `Get` held `c.mu` for the whole call
(cache read, rate-limit wait, network execution with retries ≈ 94s worst
case, cache write), collapsing throughput to ~1 call per retry cycle,
queuing 1ms cache hits behind 94s outages, and making queued callers
uncancellable (Go mutexes are not ctx-aware). The fix restructures `Get`
so `c.mu` guards only the `lastReq` accounting.

- [x] Cache read (`Store.Get`) before any lock acquisition; a fresh hit
      returns without ever touching `c.mu` (Store is concurrency-safe).
- [x] Rate limiter extracted as `waitTurn(ctx)`: loop of accounting under
      the lock / sleeping outside it, with a re-check so simultaneous
      wakeups still keep consecutive request starts >= reqGap apart;
      ctx cancellation aborts with `ctx.Err()`; `lastReq` still stamped
      at issue time.
- [x] `doWithRetry` (backoffs, Retry-After waits) and `Store.Put` run
      with no lock held; the stale-fallback entry stays per-call local
      state captured at the cache-read step (semantics unchanged).
- [x] `mu` documented as guarding only `lastReq`; other fields fixed
      after construction.
- [x] `Get` doc comment updated with the concurrency contract while
      keeping the single-choke-point framing.
- [x] Tests: `TestGet_CacheHitNotBlockedByInFlightRetry` (regression
      pin — verified it FAILS on the pre-fix code with the cache hit
      queued 502ms behind the retry cycle, PASSES after) and
      `TestGet_ConcurrentRequestsPreserveRateGap` (8 concurrent misses,
      every consecutive server arrival >= reqGap - 10ms slack).
- [x] Docs: concurrency contract item in
      `.trellis/spec/backend/cache-and-resilience.md`, one clause in the
      AGENTS.md reliability paragraph.
- [x] Validation: `gofmt -l .` empty, `go vet ./...`,
      `go test -race -count=1 ./internal/api/`, `make test`,
      `make test-race` all green; no existing test needed adjusting.

## Validation commands

```bash
go vet ./... && make test && make test-race
make build && printf '%s\n%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0.0.1"}}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | dist/*/danbooru-tag-mcp.exe
make integration   # only once Danbooru is back up (currently 503)
```

## Risky files / rollback points

- `internal/api/client.go` — the choke point; every step keeps it the only
  outbound path. Cache-off behavior must equal Step-1 behavior exactly.
- `internal/tools/register_test.go` — schema pins; update deliberately in
  the same step as the description change, never wholesale.
- `internal/service/tag_test.go` — mock churn in Step 4 is mechanical;
  any behavioral diff there means the step leaked behavior change.
- Rollback: cache state is entirely under `cache/v1/` (delete dir);
  `DANBOORU_MCP_NO_CACHE=1` restores pre-cache runtime (retry/errors
  remain). Each step is an independent commit boundary.

## Pre-start checklist

- [x] PRD convergence pass done; no blocking open questions.
- [x] design.md + implement.md reviewed by user (this review).
- [ ] Comma-list probes retried when Danbooru returns (follow-up, may
      simplify R3; does not block).
