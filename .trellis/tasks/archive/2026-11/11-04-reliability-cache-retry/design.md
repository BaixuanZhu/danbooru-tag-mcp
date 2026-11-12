# Design — reliability pass (cache, retry, batch renames)

## Architecture overview

New leaf package `internal/cache` sits below both `internal/api` (uses the
`Store`) and `internal/service` (names the `Result` type in its
`TagFetcher` interface). Dependency arrows stay acyclic:

```
tools -> service -> TagFetcher (service-defined, returns cache.Result)
                     ^ implemented by api.Client (imports cache)
main.go: api.NewClient(WithCache(...)) + service.NewTagService(client, ...)
```

`internal/cache` imports nothing beyond stdlib — same leaf discipline as
`internal/vocab`.

## internal/cache

```go
// Result carries a response body plus cache provenance. Stale is false on
// every network path; only expired-cache fallback sets it.
type Result struct {
    Body     []byte
    Stale    bool
    CachedAt time.Time // zero unless Stale
}

type Store struct{ dir string } // one JSON file per key, key = sha256 hex

func Open(dir string) (*Store, error)
func (s *Store) Get(key string) (body []byte, storedAt time.Time, ok bool)
func (s *Store) Put(key string, body []byte) error // atomic: temp file + rename
```

- **Per-key files** (`<dir>/v1/<sha256>.json`, entry = `{stored_at, body}`
  with `body` base64 via stdlib JSON): O(1) writes, no whole-cache rewrite,
  no memory footprint, natural last-writer-wins across multiple server
  processes. Eviction: dual cap — 2000 entries AND 64MB total payload
  (worst case without the byte cap is 2000 related_tag bodies at
  50–100KB each); on `Put`, sweep oldest-by-mtime first until under both
  (best effort, non-fatal).
- All Store errors are returned, never panicked; the api layer treats every
  cache error as "miss".

## api.Client changes (client.go — stays the single choke point)

### Get flow ( reordered )

1. Compute cache key = `path + "?" + params.Encode()` **before** credential
   injection. `url.Values.Encode` sorts keys → deterministic.
2. If path is cacheable and Store has a fresh entry (TTL table below) →
   return `Result{Body: cached}`, no network, no rate-limit wait.
3. Rate-limit wait (unchanged, ctx-interruptible).
4. Attempt loop: `http.Do` + response read (unchanged 5MB cap).
   - OK → `Put` (unless body is exactly `[]` or path uncacheable), return.
   - Retryable (5xx, network/timeout error, 429) → backoff 1s/3s
     (ctx-interruptible; on 429 replace first backoff with `Retry-After`,
     capped at 30s), retry up to 2 times.
   - ctx canceled → return immediately, never retry, never stale-serve.
   - Non-retryable 4xx → shrunken error.
5. Retries exhausted on a retryable failure → Store lookup ignoring TTL:
   hit → log `[WARN] danbooru unreachable (<cause>), serving stale <path>
   (age <dur>)` to stderr, return `Result{Body, Stale: true, CachedAt}`.
   Miss → shrunken error.

### TTL table

| Path | TTL |
|---|---|
| `/related_tag.json` | 30d |
| `/wiki_pages.json` | 7d |
| `/tag_aliases.json` | 7d |
| `/tags.json` | 24h |
| `/posts.json` | never (no Store interaction at all) |

### Error shrink

`summarizeErrorBody(contentType, body)`: HTML → `resp.Header.Get("Content-Type")`
contains `text/html` → status + `<title>` text (downstrip, fallback to
first 100 bytes if no title); otherwise truncate body to 300 bytes. Error
format: `danbooru 503 (Downbooru)` / `danbooru 422: <truncated>`. The 429/421
branch gains `Retry-After` in its message when present.

### Request gap (R4b)

`NewClient` default resolution order:
1. `DANBOORU_REQ_GAP_MS` set → parse; invalid or negative → keep tier default
   + one stderr warn. `0` honored (no gap).
2. Else credentials present (`DANBOORU_LOGIN` + `DANBOORU_API_KEY` non-empty)
   → 500ms.
3. Else 1500ms (unchanged).

`WithReqGap` still overrides everything (tests keep using it).

### Cache wiring

`Option WithCache(dir string)`; `main.go serve()` passes
`%LOCALAPPDATA%/danbooru-tag-mcp/cache` unless `DANBOORU_MCP_NO_CACHE=1`
(cache disabled → today's behavior + still retry/error-shrink). Bootstrap
guards are NOT consulted — cache writes are data, not PATH/config, and a
`go run` binary warming a user cache is harmless. Store Open failure →
serve without cache + stderr warn.

### Refresh (R1 control surface, per-call cache bypass)

- `internal/cache` owns the flag: `WithRefresh(ctx) context.Context` /
  `RefreshRequested(ctx) bool` (private ctx key). ctx is the per-call
  channel tools → service → api; no interface signature changes.
- Tools layer: the 6 cacheable tools gain a `refresh` boolean param
  (default false); the handler wraps the ctx before calling the service.
  Service passes ctx through untouched; `Client.Get` checks the flag right
  after key computation: skip the fresh-hit shortcut, run the network path
  (retry rules unchanged), `Put` on success — `stored_at` resets, so the
  TTL clock restarts.
- A refresh that fails follows the normal chain: retry → stale fallback
  (still marked `stale`) → shrunken error. A refresh served from the
  network is never marked stale.
- Deliberately no invalidate-without-refetch operation: for a per-call
  agent consumer it is a second control surface with no additional
  capability over `refresh`.

### TagFetcher signatures

All 7 `FetchX` methods change `([]byte, error)` → `(cache.Result, error)`.
One mock (`mockFetcher`, `internal/service/tag_test.go:31-60`) updates
mechanically: every stub returns `cache.Result{Body: m.body}`. This is the
race-free way to thread provenance — alternatives (last-meta getters,
ctx smuggling) either race under concurrent handlers or invert layer deps.

## service layer

- Every cacheable-path response struct gains
  `Stale bool \`json:"stale,omitempty"\`` +
  `CachedAt string \`json:"cached_at,omitempty"\`` (RFC3339), filled only
  when `res.Stale` (tools marshal structs directly —
  `register.go:44` — so no tools-layer change for the fields).
  Affected: `search_tags`, `get_tag_info`, `get_tags_info`,
  `get_related_tags`, `get_tag_alias`, `get_tag_wiki`. Posts/profile paths
  never stale → no fields there.
- **R3** `Infos`: after building `Found`, loop
  `s.resolveRenamedTo(ctx, &result.Found[i])` — the method already no-ops
  live rows and swallows alias errors (`tag.go:202`).
- **R4a** `Search` gains `minPosts int`; filter after unmarshal, before
  wd14 fallback logic in the tools layer is affected (fallback triggers on
  empty result — filtering to empty must still count as "no result carries
  posts" for `hasLiveTag` semantics; verify against register.go handler).
- **R4c** `SearchPosts` gains `tagsMode` (enum full/none/diff); the TOOL
  param is named `tags_mode` (`tags` is already the query param). `none`:
  omit `PostTags` (becomes `omitempty`). `diff`: parse the query's content
  tags (space-split, drop tokens containing `:`), subtract from every
  per-post category list. Default `full` = byte-identical to today.

## tools layer

- New params: `search_tags.min_posts` (number, default 0),
  `search_posts.tags_mode` (enum full|none|diff, default full).
- Description clauses (terse, one clause per rule):
  - 6 cacheable tools: "may serve expired cached data marked `stale`+
    `cached_at` when Danbooru is unreachable".
  - `get_tags_info`: "0-post rows carry `renamed_to` (one extra request
    each)". Update the "one request" wording to reflect the +N.
  - `search_posts`: "`tags=none` drops tag lists; `diff` keeps only tags
    not in the query".
  - `search_tags`: "`min_posts` filters results below the threshold".
- `register_test.go` schema pins updated deliberately alongside.

## Cross-cutting

- **stdout purity**: all new logging (WARN stale-serve, gap warnings) goes
  to stderr via `log` — server mode invariant unchanged.
- **Pinned boundaries**: no rating injection anywhere (cache replays bodies
  verbatim; it cannot rewrite queries); posts never cached; profile
  sampling unaffected (FetchPosts path untouched by cache).
- **Integration script**: set `DANBOORU_MCP_NO_CACHE=1` in
  `scripts/integration-test.sh` so it keeps verifying live upstream; add a
  stale-serve unit test instead of an online one.
- **Installer/uninstaller**: NSIS uninstaller `RMDir /r` the cache dir
  (`$LOCALAPPDATA\danbooru-tag-mcp\cache`); install.ps1/README document it
  (it is safe to delete at any time).
- **Docs**: AGENTS.md — new `internal/cache` bullet + retry/gap/stale
  conventions + rewrite the batch "does NOT resolve aliases" paragraph;
  README — cache, env vars (`DANBOORU_REQ_GAP_MS`,
  `DANBOORU_MCP_NO_CACHE`), credential-tier 500ms default, new tool params;
  CHANGELOG `[0.6.0]`; `server.json` version bump at release time.
- **Compatibility/rollback**: cache file format is self-contained under
  `cache/v1/` — deleting the directory rolls back all state; behavior with
  cache disabled = old behavior + retry + shrunken errors. No breaking
  response-shape change: new fields are `omitempty`, new params default to
  today's behavior.

## Trade-offs considered

- **Per-key files vs single JSON**: single file = whole-file rewrite per
  Put + big memory map; per-key files chosen for O(1) writes and
  multi-process tolerance (last-writer-wins per key).
- **Interface churn vs last-meta getter**: getter races under concurrent
  mcp handlers (fetch/LastMeta interleave across goroutines);
  `cache.Result` in the signature is mechanical and total.
- **No `cached_at` on fresh hits**: user decision — fresh-hit markers add
  payload on every call with no actionable information; stale-only markers
  carry all the trust-calibration value.
- **500ms credential default is argued, not measured**: Danbooru is down
  (see research/live-probes.md); browser-parity + 429 self-healing is the
  mitigation, revisit on evidence.
