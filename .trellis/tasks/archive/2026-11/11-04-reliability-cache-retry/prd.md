# Reliability pass: response cache, retry, and batch renames

## Goal

Cut repeat upstream traffic and keep every tool answering through Danbooru
outages, plus close the rename-visibility gap in the now-default batch path.
Reopens the "tier-3 reliability" scope that was previously cut for lack of
evidence; the new evidence is a consuming agent's analysis of 1,470 real tool
calls (zero reuse of repeat queries, 9% dead-end searches, a live 503 outage
inlining a full HTML page into every error) relayed by the maintainer on
2026-11-04, plus a first-hand 503 "Downbooru" outage observed during this
planning session.

## Background (confirmed facts, verified against code this session)

- `internal/api/client.go:20` — `defaultReqGap = 1500ms` hardcoded;
  `main.go:76` constructs `api.NewClient()` with no options.
- `internal/api/client.go:125-132` — non-2xx errors inline the raw response
  body (a live 503 "Downbooru" maintenance page measures 1864–2853 bytes of
  HTML); the whole `Get` performs a single `http.Do`, so there is no retry
  anywhere; 429/421 return a generic message without reading `Retry-After`.
- `internal/api/client.go:203` — `FetchPosts` doc comment still claims "NSFW
  filtering is injected by the service layer"; stale doc rot from v0.2.x that
  contradicts the test-enforced no-rating-injection boundary.
- `internal/service/lookup.go` — batch `Infos` resolves no `renamed_to`
  (deliberate, documented in AGENTS.md: `search[antecedent_name]` comma lists
  return empty — verified live earlier). Single `Info` resolves `renamed_to`
  for 0-post rows via one alias request (`internal/service/tag.go:202`,
  failure non-fatal, live rows skip).
- `internal/tools/register.go:44` — tool handlers marshal service structs
  directly, so an optional field added to a service response struct reaches
  tool output with zero tools-layer changes.

## Requirements

### R1 Response cache with stale fallback (P0)

- TTL cache living inside `Client.Get` (remains the single choke point).
- Cache key = path + normalized query params computed BEFORE credential
  injection (never key on `login`/`api_key`).
- TTL by path: `/related_tag.json` 30d; `/wiki_pages.json` and
  `/tag_aliases.json` 7d; `/tags.json` 24h; `/posts.json` NEVER cached
  (`order:random` sampling semantics + profile's whole-population claims).
- Fresh hits are served transparently — byte-identical responses, no
  markers (user decision 2026-11-05: stale-only marking).
- When the upstream is unreachable after retries are exhausted, serve the
  expired entry instead of erroring, marked with two optional top-level
  fields `stale: true` + `cached_at` (RFC3339) on the affected tool
  responses; omitted entirely in every other case.
- Do not cache empty-array bodies (`[]`): dead-end searches are cheap to
  refetch and caching them would delay new-tag visibility for the whole TTL.
- Opt-in live re-fetch: each of the 6 cacheable tools (`search_tags`,
  `get_tag_info`, `get_tags_info`, `get_related_tags`, `get_tag_alias`,
  `get_tag_wiki`) takes `refresh` (boolean, default false) — skip the cache
  read, fetch live (retry rules unchanged), write the result back to the
  cache (TTL clock restarts); if the live fetch fails, the stale fallback
  still applies (user decision 2026-11-05). posts/profile tools get no
  param (nothing is cached to bypass).
- Cache read/write/parse failures are non-fatal: fall through to network.
- Storage: per-user location under `%LOCALAPPDATA%\danbooru-tag-mcp\`,
  atomic temp+rename writes, persists across restarts; documented in
  install.ps1/README and cleaned by the NSIS uninstaller.
- `DANBOORU_MCP_NO_CACHE=1` disables the cache (used by the integration
  script and available to users for debugging).
- Off by default in unit tests (opt-in `Option`).

### R2 Error shrink + bounded retry (P0)

- Non-2xx error bodies: if content-type is HTML, carry status + `<title>`
  only (live case: `danbooru 503 (Downbooru)` instead of 1864+ bytes);
  otherwise truncate to ~300 bytes.
- Retry: up to 2 retries on 5xx and network errors, 1s/3s backoff,
  ctx-interruptible waits, never retry on context cancellation; 429 reads
  and honors `Retry-After` (capped).
- Resolution order: retry → stale fallback (R1) → shrunken error.
- Fix the stale `FetchPosts` comment at `client.go:203`.

### R3 `get_tags_info` gains `renamed_to` (P0)

- `Found` rows with `post_count == 0` resolve `renamed_to` via one alias
  request each, reusing `resolveRenamedTo` semantics (failure non-fatal,
  live rows skip).
- Bounding: always resolve — the natural bound is `maxBatchNames` (≤20 extra
  requests worst case), amortized to zero by R1's 7d alias cache on repeat
  batches. If the deferred alias comma-list probe (research/live-probes.md)
  later succeeds, collapse to one request as a follow-up.
- Update the tool description and the AGENTS.md contract paragraph that
  currently documents "Infos does NOT resolve aliases".

### R4 P1 tag-alongs (in scope — user decision 2026-11-05: take all three)

- R4a `search_tags` `min_posts`: local post-filter applied after fetch,
  default 0 (behavior unchanged); may return fewer than `limit`.
- R4b Request gap: `DANBOORU_REQ_GAP_MS` env override, `0` allowed (user
  decision 2026-11-05). Shipped defaults: 1500ms anonymous (unchanged),
  500ms when credentials are present — browser-parity reasoning (below
  organic logged-in usage rates), with R2's 429/Retry-After handling as the
  safety net; revisit if 429s appear in practice.
- R4c `search_posts` `tags_mode: full|none|diff` (named `tags_mode`, not
  `tags`, to avoid colliding with the tool's existing `tags` query param —
  implementation decision 2026-11-05): response tag-list mode, default
  `full`; `none` omits the per-post tag lists (id/rating/score/fav/url
  remain); `diff` = per-post tag lists minus the query's own content tags
  (metatags excluded from the subtraction).

## Out of scope (recorded, not rejected forever)

- Wiki batch lookup — blocked on the `title_normalize` comma probe
  (research/live-probes.md).
- `with`-mode randomization via `page=N` — multi-request, low payoff;
  `order: "recent"` disclosure already in output.
- `get_related_tags` latency — covered by R1's 30d cache on repeat calls.
- Tool description slimming — marginal; new features add clauses, keep them
  terse instead.

## Open questions

None blocking. Deferred research (comma-list probes) is recorded in
`research/live-probes.md` and does not gate this scope.

## Acceptance criteria

- [ ] Unit tests: TTL expiry per path, stale-serve on 5xx with `stale` +
      `cached_at` marker, transparent fresh hit (no markers), key ignores
      credentials, `/posts.json` never cached, empty bodies not cached,
      cache failure falls through to network, `refresh:true` bypasses a
      fresh hit + bumps the stored entry + still stale-serves on failure.
- [ ] Unit tests: retry counts/backoff on 5xx and timeout, no retry on ctx
      cancellation, HTML error collapsed to status + title, non-HTML body
      truncation, `Retry-After` honored.
- [ ] Unit tests: batch resolves `renamed_to` on 0-post rows, non-fatal on
      alias failure, live rows skip the extra request.
- [ ] Unit tests: `min_posts` filter (default-0 unchanged), req-gap env
      parsing incl. `0` and invalid values, `tags` mode full/none/diff with
      metatag-aware subtraction.
- [ ] All pinned boundary tests stay green (no rating injection, posts
      query verbatim, wiki extraction fixtures, profile sampling,
      register_test schema pins updated deliberately where schemas change).
- [ ] `make integration` still exercises live upstream (cache disabled via
      `DANBOORU_MCP_NO_CACHE`), including a stale-serve scenario if
      practical.
- [ ] Tool descriptions and AGENTS.md updated where behavior contracts
      change; README documents cache location, both new env vars, and the
      credential-tier gap default; CHANGELOG entry added.
