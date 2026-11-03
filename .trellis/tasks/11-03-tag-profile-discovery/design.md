# Design: Tag profile pair mode and discovery polish

## Layer impact

| Layer | Change |
|---|---|
| `internal/api` | `FetchTags` gains a category id; no new endpoint. |
| `internal/service` | Profile options/pair mode/ratings; post fields and limit cap; category mapping; `renamed_to`; wiki body extraction into structured fields. |
| `internal/tools` | New tool arguments, argument mapping, updated descriptions; `get_tag_wiki` returns structured fields. |
| docs | README, AGENTS.md, CHANGELOG, integration script. |

No new packages, tools, or dependencies. The api layer's `Get` remains the
single outbound choke point.

## Service contracts

### Tag profile

```go
// ProfileOptions carries the optional caller choices for one profile.
type ProfileOptions struct {
    With   string // optional second exact tag; non-empty selects pair mode
    Rating string // optional g|s|q|e filter
    Order  string // random|score|favcount; empty means random (single mode)
}

// ProfileWith is the pair-mode descriptor in the response.
type ProfileWith struct {
    Tag       string `json:"tag"`
    PostCount int    `json:"post_count"`
}

type TagProfile struct {
    TagInfo
    With       *ProfileWith       `json:"with,omitempty"`
    Order      string             `json:"order"`            // random|score|favcount|recent
    Rating     string             `json:"rating,omitempty"` // applied filter
    Sample     int                `json:"sample"`
    Ratings    map[string]float64 `json:"ratings"` // fraction per rating, 2 decimals
    Ubiquitous []ProfileTag       `json:"ubiquitous"`
    CoTags     []ProfileTag       `json:"co_tags"`
    TopPairs   []ProfilePair      `json:"top_pairs"`
}

func (s *TagService) Profile(ctx context.Context, tag string, sample int, opts ProfileOptions) (*TagProfile, error)
```

Existing `ProfilePair` stays the `top_pairs` element; the new descriptor is
`ProfileWith` to avoid the name clash.

#### Validation (all pre-flight, before any request)

- `tag`: non-empty, no space or `:` (existing rule).
- `with`: when non-empty, no space or `:`, normalized, and not equal to
  `tag` (case-insensitive).
- `rating`: empty or one of `g`, `s`, `q`, `e` (lowercase after trim).
- `order`: empty, `random`, `score`, or `favcount` (lowercase after trim).
- `with` set and `order` non-empty -> error: pair mode forces `recent`;
  `order:` with two content tags exceeds the anonymous cap.
- `sample`: clamped to [20, 200], default 200 (unchanged).

#### Single-tag flow

1. `Info(tag)` (existing `FetchTagExact` path; may resolve `renamed_to`).
2. `order` defaults to `random`.
3. If `PostCount == 0`: return an empty profile with `Order`/`Rating` set and
   no posts request.
4. Query `<info.Name> order:<order>[ rating:<rating>]`, limit `sample`.
5. `aggregateProfile(p, posts, info.Name)`; compute `Ratings`; set `Order`,
   `Rating`.

#### Pair flow

1. `Infos([tag, with])` — one request, same count as `Info`, validates both
   names and yields `with.post_count`.
2. Resolve rows case-insensitively. Missing primary -> `&NotFoundError{Name: tag}`;
   missing `with` -> `&NotFoundError{Name: with}`. The handler's wd14 rider
   uses `nf.Name`, so it reports the actually missing name.
3. Build the embedded `TagInfo` from the primary row. If the primary row has
   0 posts, resolve its alias via the same helper `Info` uses (one extra
   request on this dead-end path). If either row has 0 posts, return an empty
   profile with `With`, `Order: "recent"`, and no posts request.
4. Query `<tagRow.Name> <withRow.Name>[ rating:<rating>]`, limit `sample`
   (no `order:`). This is the 200 most recent joint posts; `Order` is
   `"recent"`.
5. `aggregateProfile(p, posts, tagRow.Name, withRow.Name)`; compute `Ratings`.

#### Ratings mix

`aggregateProfile` already deduplicates posts by id; count each post's
`rating` over the same `n` denominator and store
`roundFreq(count, n)` under its key. Only present ratings appear. A zero
sample yields `{}`.

#### Exclusions

`contentTags` / `aggregateProfile` change from one `self` string to an
exclusion set. The single-tag test remains pinned at
`arm_up order:random`; pair mode excludes both query tags from `co_tags`,
`ubiquitous`, `top_pairs`, and pair counting.

### search_posts

```go
type Post struct {
    ID           int      `json:"id"`
    Rating       string   `json:"rating"`
    Score        int      `json:"score"`
    FavCount     int      `json:"fav_count"`
    Tags         PostTags `json:"tags"`
    PreviewURL   string   `json:"preview_file_url"`
    LargeFileURL string   `json:"large_file_url"` // empty when no large version
}

const maxPostLimit = 20
```

`SearchPosts` checks `limit > maxPostLimit` before `FetchPosts` and returns
`"too many posts requested: %d (max 20)"`. The tag-count check stays first.
`danbooruPost` gains the three matching fields; the projection copies them.

### search_tags

```go
// CategoryID resolves a category name to Danbooru's numeric id:
// general 0, artist 1, copyright 3, character 4, meta 5.
func CategoryID(name string) (int, bool)

func (s *TagService) Search(ctx context.Context, query string, limit, category int) ([]Tag, error)
```

`-1` means no filter. `TagFetcher.FetchTags` and `api.Client.FetchTags` gain
the same `category int`; the api layer sets `search[category]` only when
`>= 0`. The tool handler lowercases/trims the argument, maps it via
`CategoryID`, and errors before the service call on an unknown name.

### renamed_to

```go
type TagInfo struct {
    Tag
    WD14      WD14Status `json:"wd14"`
    RenamedTo string     `json:"renamed_to,omitempty"`
}
```

`Info` calls a private helper for 0-post rows only:

```go
func (s *TagService) resolveRenamedTo(ctx context.Context, info *TagInfo) {
    if info.PostCount != 0 {
        return
    }
    if alias, err := s.Alias(ctx, info.Name); err == nil && alias != nil {
        info.RenamedTo = alias.Consequent
    }
}
```

Alias lookup is an enhancement: a failure (network, 5xx, empty body) must not
turn a found tag row into an error, hence the ignored error. The existing
0-post tests keep working (their mock returns an unparsable empty alias body).
Pair mode reuses the helper on its primary row. `Infos` (batch) does not call
it.

## Wiki structured extraction

`get_tag_wiki` stops returning raw DText. The service extracts deterministic
fields from the body and drops it from the response.

```go
type WikiPage struct {
    Title        string   `json:"title"`
    Summary      string   `json:"summary"`
    Description  string   `json:"description,omitempty"` // only with include_description
    IntroTags    []string `json:"intro_tags"`
    Rules        []string `json:"rules"`
    SeeAlso      []string `json:"see_also"`
    ExamplePosts []int    `json:"example_posts"`
    LinkedTags   []string `json:"linked_tags"`
    OtherNames   []string `json:"other_names"`
}

func (s *TagService) Wiki(ctx context.Context, title, otherNames string, limit int, includeDescription bool) ([]WikiPage, error)
```

`Body` is removed from the public type; `danbooruWikiResponse` keeps the raw
body for extraction only. `includeDescription` only controls whether
`Description` is populated; the rest of the extraction always runs.

Parsing rules (DText-lite, no new dependency):

- Split the body on `h1.`-`h6.` headings (`(?m)^h[1-6]\.\s*(.+)$`); the
  intro is the text before the first heading.
- `summary`: first non-empty paragraph of the intro (fallback: first
  non-empty paragraph of the whole body), with `[[target|display]]` flattened
  to display text (`target` when display is empty), whitespace collapsed.
- `description`: when `includeDescription` is true, the full intro section
  with links flattened to display text and paragraph breaks preserved; omit
  otherwise. This is filtered intro prose, not the raw body.
- `intro_tags`: unique `[[...]]` link targets in the intro; exclude targets
  whose lowercase form starts with `tag_group:`, `tag group:`, `help:`,
  `list of`, or `list_of`. Raw target text (spaces/title case) matches the
  existing `linked_tags` convention.
- `rules`: sentences from the intro plus sections whose heading is not
  Examples/See also/External links, where the sentence contains at least one
  link and a directive keyword (`instead`, `not to be confused`, `should`,
  `use `, `only `, `do not`, `don't`, `aren't`, `avoid`, `overlap`,
  `subjective`, `note`, `caution`, `see `); flatten links to display text,
  collapse whitespace, dedupe, cap at 6 entries / 500 chars each.
- `see_also`: unique link targets under a heading containing "see also"
  (case-insensitive), same noise filter.
- `example_posts`: unique `!post #<id>` IDs from the whole body, in order,
  cap 20. `!asset` IDs are not posts and are ignored.
- `linked_tags`: unchanged union of all links (existing `extractLinkedTags`).
- Empty body or no matches -> `""` for `summary` and empty (non-nil) slices
  for the lists; never `null` in JSON.

Fields use raw link targets to match `linked_tags`; callers can pass them to
`get_tags_info`, which normalizes spaces to underscores.

Example response for `arm_up`:

```json
{
  "title": "arm_up",
  "summary": "A single arm is raised up, and the hand is around eye level or above.",
  "intro_tags": ["arms up", "outstretched arm", "hand up", "hand on own head", "hand on own face"],
  "rules": [
    "If both arms are raised, use arms up instead.",
    "If the arm is reaching outwards, away from the body, use outstretched arm instead.",
    "If the hand is below eye level, use hand up instead.",
    "If the hand is touching the head, use hand on own head or hand on own face instead."
  ],
  "see_also": ["arms up", "arm above head", "arm behind head", "hand on own face", "hand on own head", "hand up", "outstretched arm", "raised fist", "stretching"],
  "example_posts": [11522437, 11495343, 10733108, 11569077],
  "linked_tags": ["arms up", "..."],
  "other_names": ["..."]
}
```

With `include_description:true`, the same response gains a `description`
field containing the full intro prose (links flattened, paragraph breaks
preserved); `body` stays absent.

## Tool schemas

| Tool | New/changed arguments |
|---|---|
| `search_tags` | `category` (optional string) |
| `search_posts` | `limit` description: default 5, max 20 |
| `get_tag_profile` | `with`, `rating`, `order` (all optional strings) |
| `get_tag_info` | description mentions `renamed_to` |
| `get_tag_wiki` | adds `include_description` (optional bool, default false); response gains optional `description`, still no `body` |

Descriptions (terse, one clause per behavioral rule):

- `search_tags`: `Search Danbooru tags by keyword, ordered by post count. When
  no result has posts, adds wd14_hits (WD14 tagger vocab names containing the
  query). category filters by general|artist|copyright|character|meta`
- `get_tag_info`: `Get exact info for a Danbooru tag. wd14 field: live
  (current Danbooru), tagger_era (WD14 tagger vocab only, likely renamed),
  unknown (neither). Adds renamed_to when a 0-post name has an active alias`
- `search_posts`: `... Max 2 content tags per query ... Each post carries
  score, fav_count, large_file_url, and its tag list split by category
  (copyright/artist/character/general/meta)`
- `get_tag_profile`: `Distribution profile of one tag from a post sample
  (default 200): co_tags by in-sample frequency, ubiquitous (freq >= 0.5),
  top_pairs by lift, ratings mix. order: random (default), score, favcount
  (top-post biased). rating: g/s/q/e filter. with: second tag; profiles the
  200 most recent joint posts instead`
- `get_tag_wiki`: `Get a tag's wiki page as structured fields: summary,
  intro_tags, rules, see_also, example_posts, linked_tags, other_names.
  include_description also returns the intro prose. Pass title (exact
  canonical tag) or other_names (multilingual alias substring)`

Argument descriptions:

- profile `with`: `second exact tag; profiles the 200 most recent joint posts
  (order forced to recent)`
- profile `rating`: `filter sampled posts by rating: g|s|q|e`
- profile `order`: `single-tag sample ordering: random (default), score,
  favcount (top-post biased); not allowed with with`
- search_tags `category`: `filter by category:
  general|artist|copyright|character|meta`
- get_tag_wiki `include_description`: `also return the intro prose as plain
  text (character appearance descriptions)`

## Handler error mapping

`get_tag_profile` already maps `*service.NotFoundError` to
`{"error":"tag_not_found","message":...,"wd14":...}`. Change the wd14 source
from the requested `tag` to `nf.Name`, so a missing `with` carries its own
verdict. Other errors keep `profile_failed`.

## Response examples

Single tag, explicit rating filter:

```json
{
  "id": 475187,
  "name": "arm_up",
  "category": 0,
  "post_count": 314201,
  "wd14": {"status": "live"},
  "order": "random",
  "rating": "g",
  "sample": 50,
  "ratings": {"g": 1.0},
  "ubiquitous": [],
  "co_tags": [],
  "top_pairs": []
}
```

Pair mode:

```json
{
  "id": 475187,
  "name": "arm_up",
  "category": 0,
  "post_count": 314201,
  "wd14": {"status": "live"},
  "with": {"tag": "holding_weapon", "post_count": 436404},
  "order": "recent",
  "sample": 200,
  "ratings": {"e": 0.01, "g": 0.59, "q": 0.01, "s": 0.39},
  "ubiquitous": [],
  "co_tags": [],
  "top_pairs": []
}
```

## Test plan

`internal/api/client_test.go`

- Update the existing `FetchTags` call with `-1`.
- Add `TestFetchTags_CategoryFilter`: assert `search[category]=4` is sent and
  the name pattern/limit/order are unchanged.

`internal/service/tag_test.go`

- Update `Search` calls with `-1`; add `TestSearch_ForwardsCategory` and
  `TestCategoryID`.
- `SearchPosts`: assert `score`, `fav_count`, `large_file_url`; add
  `TestSearchPosts_RejectsLimitOverMax` (no mock fetch recorded).
- `Info`: add 0-post + alias -> `renamed_to`; live row -> no alias request;
  alias error -> row still returned.
- Wiki fixtures from real bodies (`arm_up`, `sailor`, `1girl`,
  `firefly_(honkai:_star_rail)`, `highres`):
  - `TestWiki_StructuredFields`: arm_up -> summary, 5 intro tags, 4 rules,
    9 see_also, 4 example posts; marshal the page and assert no `body` key.
  - `TestWiki_RulesCaptureDisambiguation`: sailor's "not to be confused with"
    and 1girl's inanimate-object rule.
      - `TestWiki_CharacterIntroTags`: firefly -> intro tags include appearance
        tags, see_also includes Sam, 6 example posts, rules empty.
      - `TestWiki_IncludeDescription`: firefly with `includeDescription=true`
        contains the appearance prose (grey hair / blue gradient / outfit);
        false omits it; `body` absent in both.
  - `TestWiki_NoiseAndSections`: highres has no intro tags; tag_group/list-of
    links excluded; External links not folded into see_also.
  - `TestWiki_EmptyBody`: empty fields, no panic.

`internal/service/profile_test.go`

- Update `Profile` calls to pass `ProfileOptions{}`.
- Keep `TestProfile_UbiquitousSplitAndExclusions` asserting
  `arm_up order:random`.
- Add:
  - pair query/exclusions/`with` descriptor/`order:"recent"`;
  - rating filter query and `ratings` mix;
  - score/favcount order echo;
  - invalid `with`/`rating`/`order`, `order`+`with`, `with == tag`;
  - missing `with` -> `NotFoundError{Name: with}`;
  - 0-post `with` -> empty profile, no posts request;
  - 0-post primary + alias -> `renamed_to`;
  - pair uses canonical row names when the input case differs.

`internal/tools/register_test.go`

- Add `category` (search_tags) and `with`/`rating`/`order` (profile) to the
  property checks.

`scripts/integration-test.sh`

- Change the existing `get_tag_wiki` call to `include_description:true`;
  assert `summary` / `description` / `intro_tags` / `example_posts` present,
  `body` absent, `linked_tags` still present.
- Add pair profile (`arm_up` + `holding_weapon`, sample 50), rating-filtered
  profile (`rating:"g"`, sample 20), category search, `sailor_suit`
  `renamed_to`, and an over-limit `search_posts` (limit 21) rejection.
- Assert: pair `order == "recent"`, `with.tag`, both tags absent from ranked
  sections, rating sample all-g, category rows category 4, `renamed_to ==
  "sailor"`, error payload for limit 21.
- Bump the call count comment and `DANBOORU_MCP_IT_SLEEP` from 45 to 60.

## Docs

- `README.md`: Features bullets for pair/rating/order/ratings mix,
  search_posts fields + cap, search_tags category, renamed_to, and structured
  wiki fields (optional `description`); update the Tools table rows (including
  `get_tag_wiki`'s no-body response).
- `AGENTS.md`: profile section (pair mode, recent sample, order/rating
  opt-ins, ratings mix, exclusions, tag-cap rationale), result-shaping
  section (new post fields, limit 20), tagger-era section (renamed_to),
  search section (category), and a wiki-extraction section (deterministic
  fields, noise filter, rules heuristic, opt-in intro description, body
  removed).
- `CHANGELOG.md`: add `## [0.5.0] - Unreleased` with the feature list and a
  breaking-change note for `get_tag_wiki` (raw `body` removed).

## Non-goals / rejected alternatives

- **Pair random sampling**: would need `counts/posts.json` plus a random page,
  and page 1000 biases pairs over 200k posts. Rejected in favor of recent-200.
- **Raw wiki body**: removed in favor of structured fields (user decision).
  No semantic condition/tag mapping; `rules` is filtered prose, and the raw
  body is not retained. The opt-in `description` returns filtered intro
  prose, not the raw body.
- **5xx retry**: user decision; profile may fail transiently.
- **Batch `renamed_to`**: alias comma-list lookup returns empty.
- **New tools**: no `resolve_tag`; deepen the existing eight.
