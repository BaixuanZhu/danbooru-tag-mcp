# Live API probes (2026-11-03)

Anonymous requests against `https://danbooru.donmai.us`, no credentials.
Purpose: validate the proposed profile/search changes before planning.

## 1. Wiki body: what is in it, and can it be dropped?

Sample: `/wiki_pages.json?limit=200&search[is_deleted]=false` (the 200 most
recently updated wiki pages; the default order is `updated_at` desc).

| Metric | Value |
|---|---|
| Pages | 200 |
| Empty bodies | 7 |
| p50 / p90 / p95 / p99 | 339 / 1248 / 1833 / 5232 bytes |
| Max | 7,908 bytes (`tag_group:text`) |

Representative bodies:

- `arm_up` (720 B): definition plus disambiguation rules ("If both arms are
  raised, use `[[arms up]]` instead. ... If the hand is touching the head, use
  `[[hand on own head]]` ..."), examples, see-also links.
- `firefly_(honkai:_star_rail)` (2,101 B): character intro, appearance prose
  with links (hair/eyes/outfit), see-also list, external links.
- `1girl` (801 B): tag semantics and edge cases ("Only living characters who
  are visibly present count towards this tag..."), examples.
- `honkai:_star_rail` (976 B): series description plus external links.
- `highres` (383 B): technical definition plus see-also.
- `wlop`: no wiki page (artist wikis are often absent).

Conclusion:

- `linked_tags` captures the link targets but not the rules that bind them.
  `arm_up`'s value is exactly the "if X, use Y" mapping, which a link list
  loses.
- Body sizes are small (median 339 B; 95th percentile 1.8 KB). A 5-page wiki
  response is typically 1.7-6 KB, worst sampled ~8 KB/page.
- The body is small and the prose rules are the valuable part. Truncation
  would be wrong (the earlier "context bomb" claim was wrong; an 8 KB guard
  would hit ~1% of pages). The final user decision is to remove the body and
  replace it with deterministic structured fields (section 7), which retain
  the rule sentences as `rules` text.

## 2. get_tag_profile sample feasibility

### 2.1 Two content tags + order: rejected

`/posts.json?tags=arm_up holding_weapon order:random&limit=5` -> HTTP 422

```json
{"success":false,"error":"PostQuery::TagLimitError","message":"You cannot search for more than 2 tags at a time."}
```

Pair profiling cannot use `order:random`/`order:score`; it must drop the order
metatag.

### 2.2 Pair query without order works

`/posts.json?tags=arm_up holding_weapon&limit=200` -> 200 posts, all carry
both tags.

- `created_at` range: 2026-10-09 .. 2026-11-03 (~1 month).
- Ratings: g 117, s 79, q 3, e 1.
- Top co-tags: holding 200, weapon 200, 1girl 123, solo 118, sword 115,
  holding_sword 112, looking_at_viewer 109, long_hair 107.
- `arm_up sword` -> 200 posts; top co-tags include holding_weapon 162,
  holding_sword 155.
- `arm_up holding_weapon rating:g` -> 200 posts, all g.
- `/counts/posts.json?tags=arm_up holding_weapon` -> `{"counts":{"posts":18371}}`.

Decision (user): pair mode samples the 200 most recent joint posts (one
request, current-trends view), not a random page.

### 2.3 Rating filter is exempt from the tag cap

- `arm_up order:random rating:g&limit=50` -> 50, all g.
- `arm_up holding_weapon rating:g&limit=200` -> 200, all g.

So `rating:` composes with both modes.

### 2.4 order:score / order:favcount

- `arm_up order:score&limit=200` -> 200 (works, but see 2.5).
- `arm_up order:favcount&limit=200` -> 200; ratings e 106, s 22, q 71, g 1.

This is a heavily biased "top posts" view (whole-population arm_up is ~95%
g/s/q), so sorted samples must be labeled as biased.

### 2.5 Transient 500s on heavy orderings

Several `order:random` / `order:score` requests at limit 200 returned:

```json
HTTP 500 {"success":false,"error":"ActiveRecord::QueryCanceled","message":"The database timed out running your query."}
```

The same query often succeeds on retry (`arm_up order:random` failed at
limit 100, succeeded at 150/200; failed again later, succeeded again).
`limit=20`/`50` worked during the same window.

User decision: no retry in this task; profile may fail transiently and the
caller retries. Integration runs can hit this.

## 3. search_posts payload

`/posts.json?tags=arm_up&limit=5`:

- API response: 16,800 B.
- Current MCP projection: 4,262 B.
- With `score`, `fav_count`, `large_file_url`: 4,902 B (+15%).
- Average tags/post: 44.
- `large_file_url`, `score`, `fav_count` are present on all sampled posts.
- 200 posts would be ~170 KB current, so a limit cap is warranted.

## 4. search_tags category filter

- `search[name_matches]=*firefly*&search[category]=4` -> character rows.
- `search[category]=character` (name) -> empty; the API wants numeric ids.
- `search[name_matches]=*wlop*&search[category]=1` -> artist row (`wlop`,
  401 posts).

Mapping: general 0, artist 1, copyright 3, character 4, meta 5.

## 5. Renamed 0-post tag + alias

Recent active aliases (5 sampled): every antecedent has a 0-post tag row:

| Antecedent | Consequent | Antecedent post_count |
|---|---|---|
| gonzojelly | genjiro | 0 |
| 176_kwang | kobuttan | 0 |
| rosutokun | handsomedao | 0 |
| lily-laily | mina_minoru | 0 |
| 9nojo | nojo0300 | 0 |

`sailor_suit` -> `sailor`: antecedent row post_count 0.

Batch alias lookup does NOT work:
`search[antecedent_name]=sailor_suit,sailor` returned `[]` while the
single-name query returns the alias. So `renamed_to` is single-tag only.

## 6. Pagination facts (for the rejected pair-random option)

- `page=1001` -> HTTP 410 "You cannot go beyond page 1000."
- `arm_up` has 314,201 posts = 1,571 pages, so page sampling would miss older
  posts for big tags.
- `/counts/posts.json?tags=...` works anonymously and returns joint counts
  (18,371 for arm_up+holding_weapon), but the user chose recent-200 pair
  sampling instead.

## 7. Wiki structured extraction prototype

A deterministic DText-lite extractor was prototyped against the same live
pages (no semantic model):

- Split the body on `h1.`-`h6.` headings; the intro is the text before the
  first heading.
- `summary`: first intro paragraph, links flattened to display text.
- `intro_tags`: unique link targets in the intro, excluding `tag_group:` /
  `tag group:` / `help:` / `list of` noise.
- `rules`: intro and non-example/non-see-also sentences containing a link and
  a directive keyword, links flattened, deduped.
- `see_also`: links under a "See also" heading.
- `example_posts`: `!post #<id>` IDs (`!asset` ignored).

Results:

| Page | Body | Summary | Intro tags | Rules | See also | Example posts |
|---|---|---|---|---|---|---|
| `arm_up` | 720 B | yes | 5 | 4 | 9 | 4 |
| `sailor` | 415 B | yes | 1 (`serafuku`) | 1 ("not to be confused") | 2 | 2 |
| `1girl` | 801 B | yes | 5 | 1 (inanimate objects) | 11 | 3 |
| `firefly_(honkai:_star_rail)` | 2,101 B | yes | 20+ (appearance + identity) | 0 | 1 (`Sam`) | 6 |
| `highres` | 383 B | yes | 0 | 0 | 4 | 0 |

Notes:

- `firefly`'s `Appearance` section is a list of outfit/example posts, not a
  tag list; the useful appearance tags are already in the intro.
- The intro also carries the character appearance prose (Firefly: "long grey
  hair with a light blue gradient end, two-tone blue-purple eyes, ...").
  `intro_tags` flattens that to a bag of tags and loses the relationships.
  User decision: return it as an opt-in `description` (full intro, links
  flattened), default off, so the default payload stays compact and the raw
  body stays removed.
- `arm_up`'s four rules are exactly the "if both arms raised -> arms_up;
  reaching outwards -> outstretched_arm; hand below eye level -> hand_up;
  hand on head -> hand_on_own_head/face" sentences.
- `sailor`'s rule ("should only apply if ... not to be confused with
  [[serafuku]]") is preserved.
- `1girl`'s "inanimate objects don't count" sentence is preserved.
- Some useful nuance is prose without a directive keyword (for example
  `blue_hair`'s overlap discussion); adding `overlap` / `subjective` to the
  keyword set captures it, but this remains a heuristic, not a parser.
- The structured output is smaller than the body: firefly 2.1 KB -> roughly
  0.5 KB for the sampled fields.
- User decision: replace the raw `body` with these fields; no semantic
  if-condition/tag mapping, and no raw-body fallback.
