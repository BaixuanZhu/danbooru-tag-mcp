# Profile name validation — defect facts (found 2026-11-27)

- Location: `internal/service/profile.go` — the single-tag input is rejected
  when `strings.ContainsAny(raw, " :")` (around line 110 in the pre-0.7.0
  tree; re-locate after the rebase).
- Breaking example (verified live): `firefly_(honkai:_star_rail)` — a real,
  live Danbooru name with wiki and related data — is rejected by
  `get_tag_profile` while `get_tag_info` / `get_tag_wiki` / `search_tags`
  accept it.
- Space case: Danbooru names use underscores; `normalizeTag` (TrimSpace,
  space→underscore) already exists and is used elsewhere in the service —
  profile may simply normalize instead of rejecting.
- DESIGN MUST first re-read why the check exists: most likely it guards
  caller metatags (`order:` / `rating:`) from being injected into the sample
  query (`arm_up order:random` 422s live, so the query-shape rules are
  real). Whatever the guard truly blocks must keep failing with the same
  clear error text — pin that text in a test before changing the check.
- Pair mode: the `with` parameter goes through the same code path — fix
  both together and pin both.

## Upstream source facts (danbooru/danbooru `master`, read 2026-11-28)

- `app/logical/tag_name_validator.rb`: a tag name may not match
  `/\A(<PostQueryBuilder::METATAGS>|<PostEdit::METATAGS>):(.+)\z/i` — Danbooru
  itself refuses to create a tag that begins with a known metatag key plus a
  colon. Every other colon-bearing name is legal, INCLUDING names that start
  with `word:` (`honkai:_star_rail`, the copyright tag), so "colon in the
  leading position" is NOT a usable metatag test — it would keep rejecting
  valid names. Names may not begin with `-`, `~`, `_`, `%`, brackets or
  quotes, and never contain spaces.
- `app/logical/post_query_builder.rb`: `PostQueryBuilder::METATAGS` is the
  post-search key list (base keys + `*_count` keys + their plural synonyms +
  the `*tags` category counters); matching is case-insensitive (`/io`).
- Consequence for the guard: the exact line between "metatag" and "valid
  name" is membership of the pre-colon key in that list.
- In the current tree the guard is NOT the injection barrier: both modes
  build the sample query from the resolved row names (`info.Name`,
  `tagRow.Name`, `withRow.Name`), never from the raw argument. It only turns
  an obvious misuse into a clear local error before the network.

## The validator's second half (found in review, 2026-11-28)

- The research above recorded the validator regex with both alternations,
  but the first implementation transcribed only `PostQueryBuilder::METATAGS`
  (101 keys). Verified against upstream source: `PostEdit::METATAGS`
  (`app/logical/post_edit.rb`) = `PRE_METATAGS` (`parent -parent rating
  source status` + `TagCategory.mapping.keys`) + `POST_METATAGS` (`newpool
  newfavgroup pool -pool favgroup -favgroup fav -fav child -child upvote
  downvote -upvote -downvote disapproved status -status`). It holds 24
  unique keys; 11 are shared with the search list and 13 are not:
  `artist copyright character general meta art copy char ch co gen newpool
  newfavgroup`. (A first review pass reported only 7 of them; the count
  above comes from parsing the source files.)
- These are edit syntax, not search syntax, so they were added as a second
  list (`danbooruEditMetatagKeyList`) that only `isSingleTagName` consults.
  `isDanbooruMetatag` stays the exact post-search predicate that
  11-28-search-posts-multi-tag needs for query classification.
- Live probe (anonymous): no tag with posts begins with any of the 13
  prefixes. `art:`, `artist:`, `char:`, `character:`, `copy:`, `copyright:`,
  `general:`, `newfavgroup:` and `newpool:` have only 0-post placeholder
  rows; `ch:`, `co:`, `gen:` and `meta:` have no row at all, so the wider
  guard cannot turn away a live tag. Under `honkai:` sit three live tags:
  `honkai:_star_rail` (140,538 posts), `honkai:_nexus_anima` (205) and
  `honkai:_star_rail_eidolon_parody` (40).

### /tags.json search-parameter traps (probed while checking the above)

- `search[name_like]=<prefix>*` works (wildcard prefix search).
- `search[name]=<prefix>*` does NOT: a wildcard in `search[name]` returns an
  empty list (the control `honkai:*` returned 0 rows although the tags
  exist), so it reads as "no such tag" rather than "unsupported".
- `search[name_starts_with]` and `search[fuzzy_name]` are silently ignored
  on `/tags.json`: the response is the default listing, unrelated rows that
  are identical for every value. Run a known-hit control next to any new
  search form before believing an empty answer.
