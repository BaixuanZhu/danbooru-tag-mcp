# Remove the on-disk response cache and mark upstream failures

## Goal

Delete the response cache layer shipped in 0.6.0 so that every tool answers
from live Danbooru, keeping only the reliability pieces that carry the value:
bounded retry and error shrink. Breaking change, released as 0.7.0.

Second outcome, in the same release: an upstream outage becomes unmistakable
to an agent — one error code, on every tool, flagged at the protocol level —
so it stops calling instead of trying other names and tools.

User value: the maintainer wants maintenance effort spent on data quality,
not on a subsystem the real workload does not use. A side effect is a data
quality gain of its own — `post_count` and search results can no longer be
hours or days old without any marker.

## Background (confirmed facts)

- The cache (v0.6.0, released 2026-11-12) is `internal/cache` (231
  production lines, 14 tests / 389 test lines) plus the policy in
  `Client.Get` and the `CacheMarks` plumbing in `internal/service` — about
  9% of the 3,848 non-test Go lines.
- Measured value over the real call history (1,522 calls in 64 days,
  2026-09-24 to 2026-11-27, replayed against the shipped policy; method and
  numbers in `research/cache-value-and-removal-inventory.md`):
  - Fresh hits: about 200, saving about 292 s of tool latency in 64 days at
    the then 1500 ms request gap — 220 s of it in one busy week, and none
    since the 0.6.0 release (traffic is about 2 calls per day).
  - Stale fallback: 8 genuine transient upstream failures in 64 days; in
    exactly one of them a stored body existed for the same key.
  - `refresh`: never used since it shipped.
- What the history shows actually helps through failures is retry: 4 of the
  8 transient failures were network-level errors, which retry absorbs.
- A consuming agent proposed a fallback-only variant (keep last-known-good,
  drop fresh hits). It was measured and rejected: it removes the part that
  fires and keeps the part that fired once, is slower during outages for
  already-seen keys, and still removes only about a quarter of the code.
- Every reference to the cache is inventoried in
  `research/cache-value-and-removal-inventory.md`.
- What an agent receives during an outage today was captured by running the
  real handlers against a local 503 / 429 / refused server
  (`research/outage-signalling.md`):
  - A fresh cache hit during an outage is returned with no marker at all;
    only an expired entry carries `stale`. This disappears with the cache.
  - Without a cache entry, every tool returns a text result with
    `isError: false`, under eight different codes, and `get_tag_info`
    reports the outage as `tag_not_found` (`register.go:308`).
  - Nothing tells the agent that every tool of the server is affected.

## Requirements

- R1: delete `internal/cache` and every trace of the cache in
  `internal/api/client.go` — the TTL table and `ttlClass`, `WithCache`, the
  store fields, the fresh-hit branch, the stale fallback, the `Put` path and
  `isEmptyJSONList`. `Client.Get` always goes to the network.
- R2: `TagFetcher` methods return `([]byte, error)`; no response struct
  carries `CacheMarks`, and no tool response can contain `stale` or
  `cached_at`.
- R3: the six tag tools lose their `refresh` parameter and the
  "Serves expired cached data ... refresh:true ..." description clause.
  Tool schemas and their pin tests change together.
- R4: `DANBOORU_MCP_NO_CACHE` is removed from the code, the usage text, the
  README and `scripts/integration-test.sh`.
- R5: behavior that must not change — the retry policy (up to 2 retries on
  5xx, network errors and 429/421; 1 s / 3 s backoff; `Retry-After` capped
  at 30 s; no retry on caller cancellation), error shrink, the request-gap
  tiers and `DANBOORU_REQ_GAP_MS`, credential injection and the
  credential-safe error text from task `11-27-sanitize-error-credentials`,
  the rating-filter boundary, and every successful response shape byte for
  byte. Error responses change only as R10 specifies.
- R6: the invariant that only the deleted stale-serve test still pins
  survives under a new test: a definitive 4xx returns after one attempt and
  surfaces its shrunken error.
- R7: documents match the code — `AGENTS.md` (architecture bullets, the
  reliability paragraph, the batch paragraph, the CI paragraph), the
  `.trellis/spec/backend` resilience guide and its index row, `README.md`
  (feature bullet, tools paragraph, "Response cache" section, env-var row,
  `refresh` in the tool table), `CHANGELOG.md` (`## [0.7.0] - Unreleased`
  with breaking notes) and the `server.json` description (no "cached").
- R8: a leftover `%LOCALAPPDATA%\danbooru-tag-mcp\cache` directory from an
  earlier version is neither read, written nor deleted at runtime. The NSIS
  uninstaller keeps removing it so it does not outlive the product, with a
  comment saying why.
- R9: request costs that cache hits used to hide are accepted and
  documented: `get_tag_profile` always issues its row lookup plus the posts
  request; `get_tags_info` issues one batch request plus one alias request
  per 0-post row on every call.
- R10: an upstream failure is unmistakable. When a request fails on a
  network error, a 5xx or a 429/421 and stays failed after retries, each of
  the eight tools returns the same result, flagged at the protocol level:
  `isError: true` and
  `{"error":"upstream_unavailable","message":"Danbooru is not answering right now: <cause>. All tools of this server are affected; retry later."}`.
  The `<cause>` is the shrunken, credential-safe error text (for example
  `danbooru 503 (Downbooru)` or `rate limited (danbooru 429), retry-after 1s`).
- R11: `tag_not_found` is returned only for a genuine not-found (today's
  payload with the `wd14` verdict is unchanged and stays a normal result).
  Other `get_tag_info` failures get their own code, `tag_info_failed`.
- R12: only the three upstream classes are marked. A definitive 4xx, a
  validation error, a parse error and a caller cancellation stay unmarked
  plain text results, with their codes unchanged except for R11.

## Acceptance Criteria

- [ ] `git grep -n -E 'internal/cache|CacheMarks|marksFrom|withRefresh|tagsExactClass|defaultCacheTTLs|ttlClass|WithCache|DANBOORU_MCP_NO_CACHE|transientError'` over Go sources returns nothing.
- [ ] `tools/list` exposes no `refresh` property on any tool, and no tool description mentions cached data.
- [ ] A fresh run creates no `%LOCALAPPDATA%\danbooru-tag-mcp\cache` directory; a run with an existing leftover directory leaves it byte-identical.
- [ ] The retry, `Retry-After`, cancellation and error-shrink tests are green, the P0 credential test is green, and a new test pins R6.
- [ ] Against a local 503, 429 and refused server, each of the eight tools returns `isError: true` with `"error":"upstream_unavailable"` and the R10 message — pinned by a test over all eight handlers, plus one test that runs the whole chain (api → service → tools).
- [ ] `tag_not_found` never appears for an upstream failure; a test pins it.
- [ ] A 4xx, a validation error, a parse error and a caller cancellation are not marked — pinned by tests.
- [ ] `gofmt -l internal/ main.go` prints nothing; `go vet ./...`, `make test` and `make test-race` pass.
- [ ] `make integration` passes against live Danbooru (anonymous).
- [ ] No document listed in R7 still describes the cache as current behavior.

## Key Decisions

- Full deletion, not the fallback-only variant (maintainer decision,
  2026-11-27, on the measurements above).
- `refresh`, `stale` and `cached_at` are removed outright, with no
  deprecation period: `refresh` was never used, and the two fields only ever
  appeared during outages.
- Leftover cache directories are left alone at runtime (R8). Deleting user
  directories on upgrade is a destructive step nobody asked for, and the
  uninstaller already covers cleanup.
- The breaking change is released as 0.7.0. Whether that release also
  carries the tier-1 feature proposals is decided at tag time.
- `transientError` is not deleted but repurposed: it becomes the marker of
  an upstream failure that the tools layer turns into the uniform result.
- One code for all three upstream classes (`upstream_unavailable`), not one
  per class. The cause stays in `message`, so an agent needs a single rule:
  on `upstream_unavailable`, stop calling this server. A separate
  `rate_limited` code was considered and dropped as one more thing to learn.
- Only upstream failures get `isError: true` (maintainer decision,
  2026-11-27: "上游不可用错误要明确标识，避免 agent 无效调用"). A
  not-found stays a normal result because it carries the `wd14` verdict.
  Validation errors keep today's shape.

## Out of Scope

- The tier-1 proposals (related-tag lift, posts count mode, profile name
  validation) — separate tasks. Note for them: without a response cache, the
  lift feature should fetch the site-wide post count once per process, and a
  count mode always queries live.
- Sanitizing error text (task `11-27-sanitize-error-credentials`), which
  must land first because both tasks edit `client.go`.
- Changing how credentials are sent.
- Any in-process memoization.
- Failing fast during a known outage (a circuit breaker). A well-behaved
  agent stops after the first marked failure, and calls already in flight
  would not benefit.
- Shortening the retry budget for a hanging upstream: the marker can arrive
  only after about 94 s, later than most client timeouts.
- Mentioning the error code in the tool descriptions: the payload explains
  itself, and descriptions cost context in every session.
