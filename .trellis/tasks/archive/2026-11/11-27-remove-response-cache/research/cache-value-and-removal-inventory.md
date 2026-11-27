# Cache value and removal inventory

Date: 2026-11-27. Numbers come from replaying the real MCP call history
against the shipped 0.6.0 cache policy, plus a line-level inventory of the
repository at `main` (acd06f9).

## Method

- Call history: the local ZCode session store, table `part`, rows with
  `type = "tool"` and `tool` starting `mcp__danbooru-tags__`. It carries
  arguments, output, status and start/end time of every call.
- Sample: 1,522 calls, 2026-09-24 to 2026-11-27 (64 days, 43+ sessions).
  Only this machine's consumers; `~/.claude/projects` holds none.
- Replay: each call is expanded into the upstream requests the current code
  would issue (path + sorted encoded params = cache key); a request is a hit
  when the same key was answered within its TTL class.
- Modelling caveats: pre-0.5.0 outputs cannot tell what the alias request
  behind a 0-post row would have returned, so those alias requests were
  skipped; 14 cancelled calls count as hits only if their key was seen
  before. Counts below are rounded for that reason.
- Cross-checks: live anonymous probes of `/counts/posts.json`,
  `/related_tag.json`, `/tag_implications.json`, `/wiki_pages.json`.

## What the cache did in 64 days

| Mechanism | Times it fired | Effect |
|---|---|---|
| Fresh hit | ~200 (196-206) | ~190 calls fully served from cache; ~292 s of tool latency saved at the then 1500 ms gap |
| Stale fallback | 1 | 2026-11-06, a body stored 36.8 days earlier |

- Hit rate: ~12-14% of cacheable requests; 19.5-20.7% in the busy week
  09-29..10-06; 4.2% in all other time; **0 of 44-48 since the 0.6.0
  release** (traffic since then: about 2 calls per day).
- 81-83% of cacheable requests are first-ever keys (1,070 of 1,286 distinct
  keys seen exactly once).
- 88 of ~1,672 requests were `/posts.json`, never cacheable.
- Genuine transient upstream failures: 8 (4 transport, 4 upstream 5xx; 4 of
  them in the 2026-11-04..06 Downbooru window). 7 were on never-seen keys.
- `refresh` parameter: 0 uses since it shipped on 2026-11-12.
- Ceilings if the policy were tuned instead of removed: no expiry at all
  247 hits (15.6%); all classes 30d, 243; caching empty `[]` bodies, +35;
  search 24h->7d, +5; exact 7d->30d, +34; per-name entity store, <= ~84.
  None changes the picture.
- Live cache directory after 15 days: 31-34 entries, ~90-100 KB.

## The fallback-only proposal (rejected)

A consuming agent proposed keeping only the failure fallback and dropping
fresh hits (network first, serve last-known-good marked `stale`). Reasons it
lost:

1. It deletes the part that fired ~200 times and keeps the part that fired
   once.
2. Network-first makes an outage slower for already-seen keys: the stale
   answer arrives only after the retry budget (>= 4 s of backoff, up to ~94 s
   on a hang), while a fresh entry today returns in ~1 ms.
3. It removes only ~1/4-1/3 of the cache-related code: the 205-line store,
   its 365 test lines, the cache directory and the `stale` fields all stay.
4. It is still a breaking release (`refresh` leaves 6 tools).
5. It works against the tier-1 proposals, which rely on fresh hits (a daily
   site-wide post count for lift, a 24h count cache, alias resolution for
   renamed names in the batch).

## Request costs after removal (accepted, PRD R9)

- `get_tag_profile`: row lookup + posts request on every call; 26 of 35
  historical calls had the row lookup served from cache.
- `get_tags_info`: 1 + (one alias request per 0-post row) on every call.
- Overall upstream volume returns to the pre-0.6.0 level.

## Inventory at `main` (acd06f9)

### Production code

| Location | What |
|---|---|
| `internal/cache/cache.go` (205 lines), `refresh.go` (26) | the package |
| `internal/api/client.go:19` | import |
| `client.go:40-43` | `tagsExactClass` |
| `client.go:45-72` | `defaultCacheTTLs`, `ttlClass` |
| `client.go:93-94` | `store`, `cacheTTLs` fields |
| `client.go:119-132` | `WithCache` |
| `client.go:137-140, 149` | TTL map copy in `NewClient` |
| `client.go:195-216, 249-269` | cache key, fresh hit, stale fallback, `Put` in `Get` |
| `client.go:300-303` | `isEmptyJSONList` |
| `client.go:317-324` | `transientError` |
| `client.go:190, 332` and the seven `Fetch*` (499-569) | `cache.Result` return types |
| `internal/service/tag.go:14, 83-115, 142-170` | import, `TagFetcher`, `CacheMarks`, `marksFrom`, embedded fields |
| `tag.go:238, 261, 373, 390, 440` | `marksFrom` call sites |
| `internal/service/lookup.go:29, 74` | embedded field, constructor |
| `internal/service/profile.go:164, 210` | "profiles never carry cache markers" |
| `internal/tools/register.go:11, 86-94` | import, `withRefresh` |
| `register.go:133, 144, 158, 169, 188, 236` | six `refresh` params |
| `register.go:271, 293, 321, 337, 355, 421` | `ctx = withRefresh(...)` |
| six tool descriptions | 130-char clause "Serves expired cached data ... updates the cache" |
| `main.go:77, 93-107, 142` | `clientOptions`, `WithCache`, usage line |

### Tests

| File | Delete | Modify |
|---|---|---|
| `internal/cache/*_test.go` (14 tests) | all | — |
| `internal/api/client_test.go` | `newCachedTestClient` (39-44), import, 13 tests (list in `design.md`) | `res.Body` call sites |
| `internal/tools/register_test.go` | `TestToolSchemas_NoRefreshOnUncachedTools` (115-128) | `TestToolSchemas_PropertiesExist` rows 88-97 |
| `internal/service/tag_test.go` | `TestCacheableResponses_StaleMarksMarshaled` | mock fetcher returns |
| `internal/service/profile_test.go` | `TestProfile_NeverCarriesCacheMarks` | — |

### Docs, scripts, metadata

| File | Lines |
|---|---|
| `AGENTS.md` | 45-50, 51-57, 82-120, 252, 326-328 |
| `.trellis/spec/backend/cache-and-resilience.md` | whole file |
| `.trellis/spec/backend/index.md` | 20 |
| `.trellis/spec/backend/error-handling.md` | 48-65 (lesson about fallbacks) |
| `README.md` | 50, tool table 128-133, 139-144, 146-175, 201 |
| `CHANGELOG.md` | 0.6.0 entry (history, stays) + new 0.7.0 entry |
| `server.json` | 4 (description says "cached search") |
| `scripts/integration-test.sh` | 11-17, 39-42 |
| `installer/danbooru-tag-mcp.nsi` | 128-131 (keep, re-comment) |
| `install.ps1` | 229 |

### Weight

- Cache package: 231 production lines + 389 test lines; roughly 340
  production lines in total across the code base (9% of 3,848).
- Tool descriptions: the clause is 130 chars x 6 (~200 tokens) plus the
  `refresh` parameter x 6; about 250 tokens per client session.

## Decision

Maintainer, 2026-11-27: delete the cache entirely; effort goes to data
quality. The tool-call volume does not need a cache.
