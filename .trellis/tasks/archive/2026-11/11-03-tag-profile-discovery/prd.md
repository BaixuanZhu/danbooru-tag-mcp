# PRD: Tag profile pair mode and discovery polish

## Goal

Close the two inspiration-discovery gaps left after v0.4.0 (what a tag pair
renders as, and how the sample's rating/sort affects the answer), polish the
lookup tools that feed prompt assembly, and replace `get_tag_wiki`'s raw DText
body with deterministic structured fields. No new MCP tools: eight is already
a fixed per-session context cost.

## Context

The current tool set answers "what is this tag" well. The weak spot is
discovery:

- `get_tag_profile` profiles one tag only. Users think in pairs
  (`arm_up` + `holding_weapon`), and the pair query cannot use `order:random`
  because the anonymous 2-content-tag cap counts `order:` (verified 422).
- The profile does not expose the sample's rating mix or let the caller choose
  a representative vs top-post view.
- `search_posts` returns a 150px preview URL, too small to actually inspect.
- `search_tags` cannot filter by category, so character/artist lookups drown
  in general tags.
- `get_tag_wiki` returns raw DText; the useful parts (definition,
  disambiguation rules, appearance tags) are mixed with examples, see-also,
  and external links, so every consumer re-parses prose.
- Character wikis additionally carry appearance prose (hair/eye/outfit
  relationships) that `intro_tags` flattens into a bag of tags; the
  relationships are lost when the body is removed.
- Renamed tags return a 0-post placeholder row plus `tagger_era`, but not the
  canonical name, forcing a second `get_tag_alias` call.

## Verified evidence (2026-11-03, anonymous live API)

Full probe log: `research/api-probes.md`.

- Pair query without `order:` works: `arm_up holding_weapon&limit=200` returns
  200 posts, all carrying both tags, spanning ~1 month; co-tags are useful
  (`sword` 115, `holding_sword` 112). Joint total is 18,371.
- `rating:` is exempt from the tag cap and composes with single and pair
  queries (`... rating:g` returned all-g samples).
- `order:score` / `order:favcount` work, but sorted samples are heavily biased
  (top-favcount arm_up: 106/200 explicit vs ~5% whole-population explicit).
- `order:random` / `order:score` at limit 200 intermittently return HTTP 500
  (`ActiveRecord::QueryCanceled`); retries succeed. User decision: no retry in
  this task.
- `search_posts` 5-post projection: 4.3 KB now, 4.9 KB with
  `score`/`fav_count`/`large_file_url` (+15%); 200 posts would be ~170 KB.
- `search[category]=<numeric id>` works; names do not. Ids: general 0,
  artist 1, copyright 3, character 4, meta 5.
- All sampled active aliases leave a 0-post antecedent row; `sailor_suit` ->
  `sailor` is one. Batch alias lookup by comma list does not work.
- Wiki bodies: 200-page sample, p50 339 B, p95 1.8 KB, max 7.9 KB; bodies carry
  disambiguation rules (`arm_up` -> four "if ..., use ... instead" sentences)
  and character appearance prose that `linked_tags` alone cannot express. A
  deterministic prototype (`summary` / `intro_tags` / `rules` / `see_also` /
  `example_posts`) recovered all four arm_up rules, the sailor
  "not to be confused with serafuku" rule, 20+ firefly intro tags, and example
  post IDs, with no semantic parsing. Payload shrinks (firefly body 2.1 KB ->
  ~0.5 KB structured). User decision: replace `body` with these fields.

## In scope

1. `get_tag_profile`
   - `with` optional second exact tag: profile the 200 most recent joint posts
     of the pair (2-tag query, no `order:`), exclude both tags from
     `co_tags` / `ubiquitous` / `top_pairs`, report
     `"order": "recent"` and `with: {tag, post_count}`.
   - `rating` optional `g|s|q|e` filter (metatag, exempt from the cap).
   - `order` optional `random` (default) | `score` | `favcount` for single-tag
     mode; sorted modes are documented as top-post biased. Pair mode forces
     `recent` and rejects a non-empty `order`.
   - Add `ratings` mix to the output (fractions over the deduped sample).
   - Reject invalid/empty values pre-flight; reject `with == tag`.
2. `search_posts`
   - Add `score`, `fav_count`, `large_file_url` per post.
   - Reject `limit > 20` pre-flight (payload bound; default stays 5).
3. `search_tags`
   - Add optional `category`: `general|artist|copyright|character|meta`,
     mapped to Danbooru's numeric ids.
4. `get_tag_info`
   - Add `renamed_to` when the exact row has 0 posts and an active alias
     exists. Single extra request only on that dead-end path; alias failure is
     non-fatal. Not added to `get_tags_info` (batch alias unsupported).
5. `get_tag_wiki`
   - Replace the raw `body` with deterministic structured fields: `summary`
     (first paragraph), `intro_tags` (links in the definition section),
     `rules` (directive sentences containing links), `see_also` (links under
     See also), `example_posts` (`!post` IDs). Keep `title`, `other_names`,
     and `linked_tags`.
   - Optional `include_description` (default false): when true, add
     `description` — the intro prose as plain text (links flattened) — so
     character appearance descriptions survive. This is filtered intro prose,
     not the raw body; examples, see-also, and external links stay out.
   - No semantic if-then mapping: `rules` retains the filtered sentence text.
6. Docs and tests
   - Update tool descriptions (terse), README, AGENTS.md, CHANGELOG.
   - Unit tests for every new branch; extend the integration script.

## Out of scope

- Semantic parsing of wiki prose into condition/tag mappings; `rules` is
  keyword+link filtering only. No raw `body` field (removed by decision); the
  opt-in `description` is filtered intro prose, not the raw body.
- 5xx retry, caching, or other tier-3 reliability work (user decision).
- Pair-mode `order:random` / random-page sampling; pair mode is recent-200 by
  decision.
- `order`/`sort` in pair mode; `order:` + 2 content tags exceeds the
  anonymous cap.
- `renamed_to` in `get_tags_info`; alias comma-list lookup does not work.
- New tools (e.g. multilingual `resolve_tag`); deepen existing tools first.
- Release/tag/publishing; that stays a separate release step.

## Requirements

- **R1** `get_tag_profile` with `with` sends exactly
  `<tag> <with>[ rating:<x>]` (canonical row names, no `order:`), returns
  `order = "recent"`, `with = {tag, post_count}`, and excludes both query tags
  from every ranked section.
- **R2** Single-tag mode sends `<tag> order:<random|score|favcount>[ rating:<x>]`
  and echoes `order` and `rating`.
- **R3** `ratings` maps each rating present in the deduped sample to a
  two-decimal fraction; empty sample -> `{}`.
- **R4** `with` missing from Danbooru -> `*NotFoundError` whose name is the
  missing `with` (the handler's wd14 rider must use that name). `with` present
  with 0 posts -> valid empty profile with `with.post_count = 0`, no posts
  request.
- **R5** Invalid `with` (space/colon), `with == tag`, invalid `rating`, invalid
  `order`, or `order` with `with` -> pre-flight error, no request.
- **R6** `search_posts` includes `score`, `fav_count`, `large_file_url`;
  `limit > 20` errors before any request.
- **R7** `search_tags` forwards a numeric category id; unknown category errors
  before any request; no category leaves the query unchanged.
- **R8** `get_tag_info` on a 0-post row with an active alias includes
  `renamed_to`; a live row makes no alias request; an alias fetch error still
  returns the tag row.
- **R9** Tool schemas expose the new arguments; `register_test.go` pins them.
- **R10** `go vet ./...`, `make test`, `make test-race` pass; the integration
  script covers pair mode, rating filter, category filter, `renamed_to`, and
  the structured wiki fields.
- **R11** `get_tag_wiki` returns `summary`, `intro_tags`, `rules`, `see_also`,
  `example_posts` (plus `description` when requested); no `body` key.
  `title`, `other_names`, and `linked_tags` keep their shapes.
- **R12** Extraction is deterministic: the intro is the text before the first
  heading; `intro_tags` excludes `tag_group:` / `help:` / `list of` noise;
  `rules` are sentences in the intro plus non-example/non-see-also/non-external
  sections that contain a directive keyword and at least one link, deduped and
  capped; `see_also` is the links under a "See also" heading; `example_posts`
  are unique `!post #<id>` IDs, capped.
- **R13** Empty or missing bodies produce valid empty fields, never a panic or
  an error.
- **R14** Tests pin the extraction on real fixtures (`arm_up`, `sailor`,
  `1girl`, `firefly`, `highres`); the integration script asserts `body` is
  absent.
- **R15** `include_description` defaults to false. When true, `description`
  is the full intro section with DText links flattened to display text and
  paragraph breaks preserved; the raw `body` key remains absent.

## Acceptance Criteria

- [ ] Pair profile query is exactly `<tag> <with>` (plus optional rating), 200
      most recent joint posts, `order:"recent"`, both query tags excluded.
- [ ] Single profile supports random/score/favcount, echoes order/rating, and
      keeps the default query `arm_up order:random` (existing test unchanged).
- [ ] `ratings` fractions are correct and deterministic for mixed samples.
- [ ] `search_posts` adds the three fields and rejects limit 21+.
- [ ] `search_tags` sends `search[category]=<id>` for each category name.
- [ ] `get_tag_info` returns `renamed_to` for `sailor_suit` -> `sailor` and
      omits it for live tags.
- [ ] `get_tag_wiki` returns `summary` / `intro_tags` / `rules` / `see_also` /
      `example_posts` and no `body`; `arm_up` yields 4 rules; `firefly` yields
      intro tags and example posts; `linked_tags` remains.
- [ ] `get_tag_wiki` with `include_description:true` returns Firefly's
      appearance prose (hair/eye/outfit relationships); default calls omit
      `description`.
- [ ] README, AGENTS.md, CHANGELOG updated.
- [ ] Unit tests, race tests, vet, and the online integration script pass.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Pair sample | 200 most recent joint posts | One request, no tag-cap violation, current-trends view; user choice. |
| Profile retry | None | User decision; transient 500 remains caller-visible. |
| `with` validation | Batch `Infos([tag, with])` | Same request count as `Info`, validates both and yields `with.post_count`. |
| Missing `with` error | `*NotFoundError{Name: with}` | Handler already serializes a wd14 rider; using `nf.Name` makes it correct for either tag. |
| Wiki output | Replace `body` with structured fields | User decision; deterministic extraction recovered the useful rules/tags and shrinks the payload; no semantic parsing. |
| Character prose | Opt-in `description` | Appearance relationships are useful but not needed every call; the default stays compact and the raw body is still gone. |
| Pair order | Forced `recent` | `order:` would make 3 counted fields -> 422. |
| `search_posts` cap | 20 | Bounds a 44-tag/post payload at ~20 KB; default 5 unchanged. |
