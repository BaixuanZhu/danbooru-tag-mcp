# More than two content tags — verified facts (live probes + upstream source, 2026-11-28)

All probes were anonymous against live Danbooru. Source references are
danbooru/danbooru `master` as read on 2026-11-28.

## Danbooru's tag limit (`app/models/user.rb`, `app/logical/post_query.rb`)

- `User.tag_query_limit(level)`: `ANONYMOUS` (0) → 2; any other level below
  `GOLD` (30) → 3 (so a logged-in free account may send 3 tags); `GOLD` and
  above → unlimited; members also get unlimited while a site promotion is on.
  The repo comment "Gold unlocks 6" is outdated.
- `validate_tag_limit!` raises `TagLimitError` when `term_count > tag_limit`;
  `term_count` counts tags AND every metatag that is not in
  `UNLIMITED_METATAGS` (`status rating limit is id date age filesize filetype
  parent child md5 width height duration mpixels ratio score upvote downvotes
  favcount embedded tagcount pixiv_id pixiv`). `order:` is NOT unlimited — it
  counts, which matches the repo's existing "order: counts" rule.
- A negated tag counts as a tag: `arm_up sword -1girl` → HTTP 422.
- The 422 body starts with
  `{"success":false,"error":"PostQuery::TagLimitError","message":"You cannot
  search for more than <n> tags at a time."` — the class name sits inside the
  first 300 bytes the api layer keeps, and the message carries the account's
  actual limit.
- Live (anonymous): `1girl arm_up sword` → 422; `arm_up sword` → 200.

### Verified with a real account (2026-11-28)

The maintainer provided a free-level test account (level 20, `Member`) for
this work; its credentials live only in the gitignored `.env` at the repo
root and are never committed.

- 2 tags → 200; **3 tags → 200**; 4, 6 and 7 tags → 422
  `PostQuery::TagLimitError`, "You cannot search for more than 3 tags at a
  time." The source table is live behavior: a logged-in free account gets 3.
- `/counts/posts.json?tags=1girl arm_up sword` → 200
  `{"counts":{"posts":8545}}` — the counts endpoint follows the same limit.
- Consequence for today's code: the local validator (max 2) rejects 3-tag
  queries that a logged-in free user is allowed to make.
- Consequence for testing: with this account both credentialed branches can
  be exercised live — 3 tags answer directly, 4–6 tags take the 422 fallback.

## Why "split into several 2-tag requests" does not work, and what does

- Intersecting the result sets of two 2-tag queries needs every post of both
  sides — unbounded.
- Every post carries its full tag list (`tag_string_*`), so membership in the
  remaining tags can be decided locally and exactly. A query with n tags is
  therefore answerable as: fetch posts of a 2-tag sub-query, filter locally.
- Live proof: `arm_up sword` (page of 200) → 125 posts also carry `1girl`,
  a 62 % pass rate; the filtered list is exactly `1girl arm_up sword`.

## Cost facts

- `only=<fields>` works anonymously on `/posts.json` and shrinks a post from
  3,499 to 957 bytes when restricted to the fields the service consumes
  (`id,rating,score,fav_count,preview_file_url,large_file_url,tag_string_copyright,tag_string_artist,tag_string_character,tag_string_general,tag_string_meta`).
- `limit=200` is the anonymous page cap (returns 200); `page=2` returns a
  disjoint page under the default ordering.
- Rarity input in ONE request:
  `/tags.json?search[name_normalize]=1girl,arm_up,sword&limit=3&only=name,post_count`
  → 114 bytes (`arm_up` 317,380 / `sword` 385,398 / `1girl` 8,717,103), so
  the rarest pair here is `arm_up sword` — also the best pair.
- `search[name_normalize]` canonicalizes case and spacing and returns no row
  for a name Danbooru does not carry.

## Names that look like metatags

- Colon-bearing real tags exist (`firefly_(honkai:_star_rail)`,
  `honkai:_star_rail`, `:d`). Danbooru's parser treats `key:value` as a
  metatag only when `key` is in `PostQueryBuilder::METATAGS`; any other colon
  token is a plain tag and counts toward the limit.
- The repo's current `isMetatagToken` (any colon = metatag) therefore
  miscounts them; under a local scan it would also turn such a name into a
  pass-through token and produce a second 422 instead of a result.
- A robust classification needs no guesswork: a colon token whose key is not
  a known metatag is a content tag iff the batch lookup returns a row for
  that exact name. A future metatag missing from our list then passes
  through (correct, merely unfiltered) instead of becoming a local filter
  that can never match.

## Limits of a local scan

- Counting (count mode) over n > 2 tags is exact only when the scan reaches
  the end of the 2-tag listing; otherwise the count is a lower bound.
- `order:random` re-randomizes every page, so pages may overlap; dedupe by
  post id and treat "limit reached" as a random sample rather than a top-N.
- Wildcards (`tag*`) and OR (`~tag`) are expanded server-side by Danbooru and
  cannot be evaluated from a post's tag list; they must stay in the upstream
  query or be refused.

## The scan's own query obeys the same term budget (found 2026-11-28)

Upstream source (`app/logical/post_query.rb`, read 2026-11-28):

- `term_count = tag_names.size + wildcards.size + metatags.count {
  !it.name.in?(UNLIMITED_METATAGS) }`; `validate_tag_limit!` raises
  `TagLimitError` when `term_count > tag_limit`.
- `UNLIMITED_METATAGS` holds 25 keys: `status rating limit is id date age
  filesize filetype parent child md5 width height duration mpixels ratio
  score upvote downvotes favcount embedded tagcount pixiv_id pixiv`. Every
  other metatag counts as a term: `order:`, `user:`, `pool:`, `fav:`,
  `search:`, the `*_count` keys, ...
- Consequence for a local scan: the pair query `<tag> <tag> <metatags
  verbatim>` is itself a Danbooru query. With one counting metatag it is
  three terms and 422s for an anonymous client, so the scan can send only
  ONE tag then; with two counting metatags it cannot run at all. The same
  term count decides whether a query is direct or needs the scan.

Live (anonymous, 2026-11-28):

| Query | Result |
|---|---|
| `arm_up sword order:score` | 422 `PostQuery::TagLimitError` |
| `arm_up order:score` | 200 |
| `arm_up rating:g order:score` | 200 (`rating:` is free) |
| `arm_up sword status:any` | 200 |
| `arm_up sword favcount:>5` | 200 |
| `arm_up sword user:albert` | 422 (`user:` counts) |
| `arm_up sword -rating:e` | 200 |

## Renamed tags as local filters (found 2026-11-28)

- Posts list their tags under the CURRENT name, and an alias antecedent
  (`sailor_suit`, `gold_footwear`, `barefoot_sandals`) keeps a 0-post
  placeholder row (verified live earlier for `get_tag_info`).
- In a scan, a renamed tag inside the upstream pair is resolved by Danbooru
  itself. One that becomes a LOCAL filter (a third positive, or a negative
  such as `-old_name`) never matches under its old name, so the scan would
  report "no matches, complete" for a query Danbooru answers. Example shape:
  `sailor_suit gold_footwear barefoot_sandals 1girl` (three old names plus
  one live tag) puts two old names in the pair and filters on the third.
- Rule adopted: every local-filter token whose row shows 0 posts is resolved
  through the alias endpoint once (the lookup `get_tag_info` already uses);
  the filter then uses the current name. This costs no request in the common
  case, where no local-filter token has a 0-post row.
