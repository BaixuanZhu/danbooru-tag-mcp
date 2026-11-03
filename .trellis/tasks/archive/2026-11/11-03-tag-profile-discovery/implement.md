# Implementation plan

Read `AGENTS.md` (project conventions), `research/api-probes.md` (live
evidence), and `design.md` (contracts) before touching code. Keep every
comment and string in English.

## 1. Service data model

Files: `internal/service/tag.go`, `internal/service/profile.go`,
`internal/service/lookup.go`

- [ ] `TagInfo` gains `RenamedTo string` with `json:"renamed_to,omitempty"`.
- [ ] `Post` gains `Score int`, `FavCount int`, `LargeFileURL string`; extend
      `danbooruPost` with the matching JSON fields and copy them in
      `SearchPosts`.
- [ ] Add `maxPostLimit = 20` and reject `limit > maxPostLimit` before
      `FetchPosts`; keep the tag-count check first.
- [ ] Add `CategoryID(name string) (int, bool)` for general 0, artist 1,
      copyright 3, character 4, meta 5.
- [ ] `Search` and `TagFetcher.FetchTags` gain `category int` (`-1` = all).

## 2. API client

File: `internal/api/client.go`

- [ ] `FetchTags(ctx, namePattern, limit, orderByCount, category int)`; set
      `search[category]` only when `category >= 0`.
- [ ] Update `internal/api/client_test.go`'s existing call and add a category
      query-param assertion.

## 3. Profile service

File: `internal/service/profile.go`

- [ ] Add `ProfileOptions` and `ProfileWith`; extend `TagProfile` with
      `With`, `Order`, `Rating`, `Ratings`.
- [ ] Change `Profile` signature to
      `Profile(ctx, tag string, sample int, opts ProfileOptions)`.
- [ ] Validate all options pre-flight: exact single tags (`with` no space/`:`),
      `with != tag`, rating in `g|s|q|e`, order in `random|score|favcount`,
      no `order` with `with`.
- [ ] Single mode: `Info(tag)` -> query `<row name> order:<order>[ rating:<x>]`.
      Preserve the existing `arm_up order:random` default.
- [ ] Pair mode: `Infos([tag, with])`; missing primary or `with` returns
      `*NotFoundError{Name: <missing name>}`; a 0-post primary or `with`
      returns an empty profile (resolve the primary alias first); otherwise
      query `<tag row> <with row>[ rating:<x>]` with `Order = "recent"` and no
      `order:`.
- [ ] `aggregateProfile` / `contentTags` take an exclusion set; exclude both
      pair tags. Initialize `Ratings` to `{}` for every profile.
- [ ] Compute `Ratings` from the deduped sample with `roundFreq`.

## 4. Renamed resolution

File: `internal/service/tag.go`

- [ ] Add `resolveRenamedTo(ctx, *TagInfo)` (0-post rows only; alias lookup
      errors are non-fatal).
- [ ] Call it from `Info` and from the pair-mode primary row. Do not call it
      from `Infos` (batch would cost N requests).

## 5. Wiki structured extraction

File: `internal/service/tag.go`

- [ ] Change `WikiPage`: remove `Body`; add `Summary string`,
      `Description string` (`omitempty`), `IntroTags []string`,
      `Rules []string`, `SeeAlso []string`, `ExamplePosts []int`. Keep
      `Title`, `OtherNames`, `LinkedTags`.
- [ ] Change `Wiki(ctx, title, otherNames string, limit int,
      includeDescription bool)`; `danbooruWikiResponse` keeps the raw `body`
      for extraction only.
- [ ] Add private helpers (table-testable, explicit params):
      - heading split (`(?m)^h[1-6]\.\s*(.+)$`); intro = before first heading.
      - DText link flattening (`[[target|display]]` -> display, or target when
        display is empty) for `summary` and `rules`.
      - noise filter: lowercase targets starting with `tag_group:`,
        `tag group:`, `help:`, `list of`, `list_of`.
      - `summary`: first non-empty intro paragraph (fallback: first non-empty
        body paragraph), whitespace collapsed.
      - `description`: when `includeDescription` is true, the full intro with
        links flattened to display text and paragraph breaks preserved; omit
        otherwise.
      - `intro_tags`: unique link targets in the intro, noise-filtered.
      - `rules`: sentences in the intro plus sections whose heading is not
        Examples/See also/External links, requiring at least one link and a
        directive keyword (`instead`, `not to be confused`, `should`, `use `,
        `only `, `do not`, `don't`, `aren't`, `avoid`, `overlap`, `subjective`,
        `note`, `caution`, `see `); flatten, collapse, dedupe, cap 6 entries /
        500 chars each.
      - `see_also`: unique link targets under a heading containing "see also",
        noise-filtered.
      - `example_posts`: unique `!post #<id>` IDs in order, cap 20; ignore
        `!asset`.
- [ ] Initialize every list to a non-nil empty slice so JSON emits `[]`;
      `summary` is `""` when absent.
- [ ] Keep `extractLinkedTags` unchanged for `linked_tags` (raw union).
- [ ] Do not attempt semantic condition->tag mapping; `rules` is filtered
      prose.

## 6. Tools layer

File: `internal/tools/register.go`

- [ ] `TagService` interface: `Search(..., category int)` and
      `Profile(..., opts service.ProfileOptions)`.
- [ ] `search_tags`: add `category` argument; map via `service.CategoryID`;
      unknown category -> `search_failed` before the request.
- [ ] `search_posts`: update the limit description to `default 5, max 20`.
- [ ] `get_tag_info`: description mentions `renamed_to`.
- [ ] `get_tag_profile`: add `with`, `rating`, `order`; pass
      `ProfileOptions`. In the `NotFoundError` branch, use
      `svc.WD14Info(nf.Name)` (not the requested tag) so a missing `with` gets
      its own verdict.
- [ ] `get_tag_wiki`: description switches to the structured fields; add
      `include_description` (bool, default false) via a new `getBoolArg`
      helper.
- [ ] Update descriptions exactly as specified in `design.md`.

## 7. Unit tests

- [ ] `internal/service/tag_test.go`: update `Search` calls with `-1`; add
      category forwarding, `CategoryID`, `SearchPosts` fields, limit cap,
      `renamed_to` (present/live/alias-error).
- [ ] `internal/service/profile_test.go`: update `Profile` calls with
      `ProfileOptions{}`; keep the `arm_up order:random` assertion; add pair
      query/exclusions/descriptor, rating filter + mix, score/favcount echo,
      invalid options, missing/0-post `with`, 0-post primary alias, canonical
      names.
- [ ] `internal/service/tag_test.go` wiki fixtures from real bodies
      (`arm_up`, `sailor`, `1girl`, `firefly_(honkai:_star_rail)`,
      `highres`):
      - `TestWiki_StructuredFields`: arm_up -> summary, 5 intro tags, 4 rules,
        9 see_also, 4 example posts; marshal and assert no `body` key.
      - `TestWiki_RulesCaptureDisambiguation`: sailor and 1girl.
      - `TestWiki_CharacterIntroTags`: firefly intro tags, see_also, 6 example
        posts, empty rules.
      - `TestWiki_IncludeDescription`: firefly with `includeDescription=true`
        contains the appearance prose; false omits it; `body` absent in both.
      - `TestWiki_NoiseAndSections`: highres has no intro tags; tag_group /
        list-of excluded; External links not folded into see_also.
      - `TestWiki_EmptyBody`: empty fields, no panic.
      - Keep `TestWiki_ExtractsLinkedTags` for the unchanged union.
- [ ] `internal/tools/register_test.go`: add `category`, `with`, `rating`,
      `order` property checks.
- [ ] `internal/service/lookup_test.go`: no signature change expected; verify
      the batch remains alias-free.

Run `make test` after this step and fix failures before continuing.

## 8. Integration script

File: `scripts/integration-test.sh`

- [ ] Change the existing `get_tag_wiki` call to `include_description:true`;
      assert `summary` / `description` / `intro_tags` / `example_posts`
      present, `body` absent, `linked_tags` still present.
- [ ] Add tool calls for pair profile (`arm_up` + `holding_weapon`, sample 50),
      rating-filtered profile (`rating:g`, sample 20), category search
      (`category:"character"`), `sailor_suit` -> `renamed_to`, and
      `search_posts` limit 21 rejection.
- [ ] Assert: `order == "recent"`, `with.tag`, both query tags absent from all
      ranked sections, rating sample all-g, category rows category 4,
      `renamed_to == "sailor"`, limit error payload.
- [ ] Update the call-count comment and `DANBOORU_MCP_IT_SLEEP` 45 -> 60.

## 9. Documentation

- [ ] `README.md`: update the five tool rows and the Features bullets
      (pair/order/rating/ratings mix, post fields + cap, category, renamed_to,
      structured wiki fields / no body).
- [ ] `AGENTS.md`: profile, result shaping, tagger-era, search, and a new
      wiki-extraction section (deterministic fields, noise filter, rules
      heuristic, body removed).
- [ ] `CHANGELOG.md`: add `## [0.5.0] - Unreleased` with the feature list and a
      breaking-change note for `get_tag_wiki` (raw `body` removed).
- [ ] Do not bump `server.json` or tag a release in this task; that is the
      release step.

## 10. Verification

```bash
gofmt -l internal main.go          # must print nothing
go vet ./...
make test
make test-race
make build
make integration                   # online; see risk below
```

Manual checks:

- `get_tag_profile {tag:"arm_up", with:"holding_weapon", sample:50}` ->
  `order:"recent"`, `with.post_count` present, no `arm_up`/`holding_weapon`
  in ranked sections.
- `get_tag_profile {tag:"arm_up", rating:"g", sample:20}` -> `ratings` has
  `g:1`.
- `search_tags {query:"firefly", category:"character"}` -> only category 4.
- `get_tag_info {name:"sailor_suit"}` -> `renamed_to:"sailor"`.
- `get_tag_wiki {title:"arm_up"}` -> summary, 5 intro tags, 4 rules, 9
  see_also, 4 example posts, no `body`.
- `get_tag_wiki {title:"firefly_(honkai:_star_rail)",
  include_description:true}` -> `description` contains the appearance prose;
  default calls omit it.
- `search_posts {tags:"1girl", limit:21}` -> error before any request.

## Risks / notes

- `get_tag_wiki` is a breaking change: the raw `body` field is gone. Consumers
  must switch to `summary` / `intro_tags` / `rules` / `see_also` /
  `example_posts`; `include_description:true` adds the intro prose (not the
  raw body) for character appearance descriptions.
- `description` is intro-only; prose under other headings is not included in
  this version.
- `rules` is a keyword+link heuristic, not a parser. Rule sentences without a
  link (or without a keyword) are dropped; the tests must pin the sampled
  pages, and docs must not promise complete rule coverage.
- `intro_tags` / `see_also` keep raw link targets (spaces/title case), matching
  `linked_tags`; callers normalize via `get_tags_info` when needed.
- `order:random` and `order:score` at limit 200 intermittently return HTTP
  500 from Danbooru; no retry is added by decision, so `make integration` can
  flake on the existing profile call. Re-run once before treating it as a
  failure.
- Pair mode is recent-200 by decision; do not describe it as a whole-population
  sample in docs or tests.
- `Infos` canonicalizes case; pair queries must use the row names, not the raw
  arguments.
- `Ratings` must be initialized so a zero sample serializes as `{}`, not
  `null`.
- `renamed_to` is single-tag only; do not add it to `get_tags_info`.
- Do not commit or push until the user asks.

## Acceptance mapping

| Requirement | Verified by |
|---|---|
| R1 pair query/exclusions | `TestProfile_PairRecentQueryAndExclusions`, integration call |
| R2 single order/rating | `TestProfile_OrderEcho`, `TestProfile_RatingFilter` |
| R3 ratings mix | `TestProfile_RatingsDistribution` |
| R4 missing/0-post with | `TestProfile_MissingWith`, `TestProfile_ZeroPostWith` |
| R5 pre-flight validation | `TestProfile_RejectsInvalidOptions` |
| R6 post fields/limit | `TestSearchPosts_Fields`, `TestSearchPosts_RejectsLimitOverMax` |
| R7 category filter | `TestSearch_ForwardsCategory`, `TestCategoryID`, api test |
| R8 renamed_to | `TestInfo_RenamedTo`, `TestInfo_LiveRowSkipsAlias`, integration |
| R9 schemas | `TestToolSchemas_PropertiesExist` |
| R10 full verification | `make test`, `make test-race`, `make integration` |
| R11 structured wiki fields | `TestWiki_StructuredFields`, integration assertion |
| R12 deterministic extraction | `TestWiki_RulesCaptureDisambiguation`, `TestWiki_NoiseAndSections` |
| R13 empty body | `TestWiki_EmptyBody` |
| R14 real fixtures | the wiki fixture tests above |
| R15 opt-in description | `TestWiki_IncludeDescription`, integration call |
