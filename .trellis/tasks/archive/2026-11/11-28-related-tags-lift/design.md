# Design — lift ranking for get_related_tags

Facts this design rests on are pinned in `research/related-tag-lift-facts.md`
(upstream source + live probes, 2026-11-28).

## Goal and non-goals

Goal: `get_related_tags` ranks entries by lift so tag-specific companions land
inside the caller's `limit`, and every entry carries the number.

Non-goals (considered, rejected — see Trade-offs): a caller-selectable
order, a category filter, artist/copyright exclusion, any disk or response
cache.

## Formula

    lift(B | A) = P(B | A) / P(B) = frequency × N / B

- `frequency` is Danbooru's own field: the share of the query tag's posts
  that carry B, estimated from up to 5000 sampled posts (exact when the query
  tag has ≤ 5000 posts).
- `B` is the entry's own `post_count`; `N` is the whole-site post count.
- No extra request is needed for the query tag's count: the related response
  carries it at the top level (`post_count`).
- Output is rounded to two decimals.

## Data flow

```
tools.get_related_tags(tag, limit)
  -> service.Related
       1. FetchRelated(tag)          GET /related_tag.json?query=<tag>&limit=500
       2. parse; candidates = entries - query tag - category 5   (upstream order)
       3. no candidates -> {"related": []}                        (no N request)
       4. N, ok = sitePostCount()     GET /counts/posts.json      (memoized)
          !ok -> candidates as-is: no lift, upstream order        (degraded)
       5. lift + support per candidate; rank; apply limit
  -> {"related":[{tag, category, post_count, similarity, frequency, lift}, ...]}
```

### Pool size

`FetchRelated` asks upstream for `limit=500` (the whole candidate pool,
~190 KB for the largest tags) instead of the default 100, because the
high-lift companions sit at frequency ranks 13–90 for arm_up and farther
down for other tags — a pool of 100 would cap the recall of the ranking.

### Empty answer (measured before implementation, 2026-11-28)

Step 3 above states the intent, not the pre-lift bytes: `Related` built its
slice from nil, so an empty answer marshaled as `{"related":null}` (probed
on HEAD b984af4 with a mock). This task makes it `[]`, the convention
`PostsResult` already follows (an empty answer is a list, never null); a
deliberate one-line change, recorded in the CHANGELOG.

### Support floor

`support = frequency × min(A, 5000)` is the number of sampled posts behind
the entry (the exact joint count when A ≤ 5000).

- Entries with `support >= 5` are ranked by lift (stable sort, so upstream
  frequency order breaks ties).
- Entries below the floor are appended afterwards in upstream order: they
  are sunk, never dropped (facts-only output; live data shows 14–17-post
  companions of mid-size tags are real and useful, while 1–2-post overlaps of
  tiny tags are not evidence).
- `5` matches `profileMinPairCount` in `get_tag_profile`.

### Whole-site count memo

- Lives in `TagService`: the count plus a mutex, memoizing SUCCESS only.
- The mutex is held across the one fetch so the count is requested at most
  once per process even when mcp-go's concurrent workers call at the same
  time (they all need the value; the wait is bounded by the HTTP timeout and
  happens once).
- A failure is not memoized: the next call retries, so a transient error at
  startup never disables lift for the process lifetime.
- In-process only. Nothing is written to disk (0.7.0 removed the cache).
- A memoized N drifts as the site grows; N is a common factor of every lift
  in a response, so the ranking is unaffected and only the rounded values
  lag slightly.

## Contracts

```go
// internal/api
FetchRelated(ctx, tag)        // /related_tag.json, limit=500
FetchPostCount(ctx, tags)     // /counts/posts.json (no params when tags == "")

// internal/service
type RelatedTag struct {
    Tag        string   `json:"tag"`
    Category   int      `json:"category"`
    PostCount  int      `json:"post_count"`
    Similarity float64  `json:"similarity"`
    Frequency  float64  `json:"frequency"`
    Lift       *float64 `json:"lift,omitempty"` // absent only when N is unavailable
}
func (s *TagService) Related(ctx, tag, limit) (*RelatedResult, error)
```

- Additive shape: no existing field changes name or type.
- `lift` is a pointer so "not computed" and a value are distinct; it is
  computed for every candidate whenever N is available, including the
  below-floor tail.
- An entry with `post_count == 0` or `frequency == 0` gets no lift (cannot
  occur with today's upstream logic; defensive).
- A missing/zero top-level `post_count` makes every support 0, so the list
  stays in upstream order with lift attached — the safe degradation.

## Compatibility

- Behavior change, deliberate: the ORDER of entries becomes lift-descending
  (CHANGELOG under Changed). `limit` keeps its meaning (max entries) and is
  applied after the ranking, so the default call returns 10 entries as
  before — different ones.
- Each upstream request grows from ~100 to ~500 entries (~190 KB) because
  every call is live since 0.7.0. Parse cost is negligible; download is the
  only cost.
- Degraded mode (N unavailable) is byte-compatible with the pre-lift
  response.

## Failure matrix

| Condition | Behavior |
|---|---|
| related request fails after retries | uniform `upstream_unavailable` (unchanged) |
| not-found tag | `{"related": []}`, no N request (unchanged) |
| N request fails (any class) | entries in upstream order, no `lift`; retried next call |
| N body unparsable / not a positive integer | same as a failed N request |

## Trade-offs considered

- **Min support 20**: rejected — discards real companions of mid-size tags
  (`kerosene_heater` J=16 for `warming_hands`).
- **Dropping below-floor entries**: rejected — hides facts; sinking keeps
  them available at larger `limit`.
- **`order` parameter (frequency / lift)**: rejected for now — one more
  parameter on every session's tool list to preserve the order that is the
  problem; revisit if a consumer asks.
- **Ranking by cosine_similarity**: cosine is not normalized by the singles'
  frequencies (1girl keeps a high cosine against everything); rejected.
- **Support-scaled (shrunk) lift score**: statistically nicer, opaque to the
  consumer; rejected — the floor plus the exposed `frequency` and
  `post_count` let the agent judge.

## Rollout / rollback

Single commit. Rollback is a revert: no stored state, no schema change, and
the degraded path already equals the old response.
