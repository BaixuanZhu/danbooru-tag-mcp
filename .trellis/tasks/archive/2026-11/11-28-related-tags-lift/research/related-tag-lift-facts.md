# Related-tag lift — verified API facts (live probes, 2026-11-27)

All facts probed anonymously against live Danbooru on 2026-11-27 during the
consumer-proposal analysis. Re-probe if upstream drifts.

## /related_tag.json?search[name]=<tag>

- Ordered by frequency, descending. `search[order]=*` is silently ignored.
- `search[category]=<id>` works (numeric ids).
- Default 100 entries; `limit=500` accepted (~175 KB payload).
- Entry fields: `tag`, `category`, `cosine_similarity`, `frequency`,
  `jaccard_similarity`, `overlap_coefficient`. NO `post_count`, NO lift.
- For arm_up, specific companion tags sit at ranks 13-90 (of 91) — invisible
  at the tool's default limit 10. This is the gap lift closes.

## Lift formula

- Lift = frequency x N / post_count, with N = the site-wide post count
  (/counts/posts.json) and post_count = the tag's own. This mirrors the
  normalization get_tag_profile's top_pairs already uses (joint / (a x b)).
- DESIGN MUST PIN the exact denominator semantics of Danbooru's `frequency`
  field (joint over the corpus vs over the tag's own posts) before shipping:
  verify by computing lift for 2-3 known pairs and comparing against
  get_tag_profile's sample-based lift (arm_up: holding_weapon + sword,
  lift ~12).

## Site-wide post count N

- /counts/posts.json is exact and uncapped anonymously: N = 12,583,571 on
  2026-11-27; arm_up 317,272 equals its post_count exactly.
- Agreed plan: fetch once per process, in-process memo only. The disk cache
  was deleted in 0.7.0 (maintainer decision 2026-11-27); none may be
  reintroduced.

## Consumer value (analysis verdict 2026-11-27)

Highest-value tier-1 proposal: lift surfaces tag-specific poses/weapons
above 1girl/solo, extending what get_tag_profile's top_pairs already proved.

## Corrections and pinned semantics (2026-11-28) — supersede the notes above where they differ

Sources: danbooru/danbooru `master` (`app/logical/related_tag_query.rb`,
`app/logical/related_tag_calculator.rb`) plus anonymous live probes.

### Response shape (corrects "NO post_count")

- Top level: `query`, `post_count`, `tag`, `related_tags`, `wiki_page_tags`.
  `post_count` IS present at the top level: the query's own post count A.
  Each entry nests `tag` (with the related tag's `post_count` B) plus
  `cosine_similarity`, `jaccard_similarity`, `overlap_coefficient`,
  `frequency`. The first entry is the query tag itself (all metrics 1.0).
- Not-found shape: HTTP 200 with `post_count: 0`, `tag: null`,
  `related_tags: []`.

### Sizing parameters (`RelatedTagQuery`)

- `tag_sample_size` (default 500, clamp 0..1000) is the CANDIDATE POOL: the
  top tags by overlap count. `limit` (default 100, clamp 0..1000) is the
  final entry count. `search_sample_size` defaults to 5000.
- `limit=500` returns the whole pool: 500 entries, ~190 KB (arm_up and
  1girl alike). The default 100 returns ~100 entries.
- `order` accepts cosine | jaccard | overlap | default (frequency desc,
  category, post_count desc, name). There is no lift order upstream.
- The endpoint validates the tag limit too (anonymous: 2 tags → 422).

### `frequency` semantics — pinned

- `frequency = overlap_count / sample_count`, `sample_count =
  min(search_count, 5000)`: the share of the QUERY tag's posts that also
  carry the related tag, P(B|A), estimated from up to 5000 posts (md5
  order); exact over all posts when A <= 5000. It is NOT a corpus-wide
  joint probability.
- Verified live on arm_up (A = 317,377): for six entries,
  `counts(arm_up B) / (frequency × A)` = 0.96 .. 1.10 (sampling error).
- Lift = P(B|A) / P(B) = `frequency × N / B`, with B = the entry's own
  `post_count` and N the whole-site count (`/counts/posts.json`, 12,592,609
  on 2026-11-28). The formula needs no extra request for A.
- `cosine = overlap / sqrt(A·B)` with `overlap = min(frequency × A, B)`
  (the same joint estimate), which is why cosine alone does not rank like
  lift: it is not normalized by the singles' frequencies.

### Measured effect on arm_up (default limit 10)

- Frequency order (the tool today): highres, 1girl, solo, long_hair,
  looking_at_viewer, breasts, commentary_request, smile, open_mouth, blush.
- Lift order: arm_behind_head 14.77, hand_on_own_head 8.40, armpits 8.04,
  hand_in_own_hair 4.94, outstretched_arm 4.85, waving 4.52,
  standing_on_one_leg 4.23, arm_at_side 4.16, leg_up 4.06, `;d` 3.75.

### Support and noise (decides the floor)

- Support of an entry = `frequency × min(A, 5000)`: the exact joint count
  when A <= 5000, the sampled overlap count otherwise.
- Big tags: with `limit=500` the pool's tail still has support 54 (1girl)
  to 73 (arm_up) — no noise.
- Mid tags (A ≈ 480, full population): entries go down to joint counts of
  5–8, and those low-count entries are REAL, useful companions —
  warming_hands → breathing_on_hands (J=309), kerosene_heater (J=16),
  breathing_on_another's_hands (J=14); blade_arm → protoman.exe (J=52).
  A support floor of 20 would discard them, so the floor must stay low (5,
  the same number `get_tag_profile` uses as its minimum pair count) and
  below-floor entries must be sunk rather than dropped.
- Fandom clusters do float for some tags (arm_on_own_head →
  mon-musu_quest:_paradox, J=17); that is a true fact, and every entry
  carries `category`, `post_count` and `frequency`, so the consumer can
  judge. No artist/copyright exclusion is added.
- This tool has no `category` parameter today; the API's `category` filter
  is not exposed, so the PRD's "category filter" clause has nothing to
  preserve.
