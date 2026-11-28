# Changelog

Release notes are extracted from the `## [x.y.z]` section matching the tag
(see `.github/workflows/release.yml`); replace `- Unreleased` with the date
when tagging.

## [Unreleased]

### Added

- `search_posts` now names a likely space-split miss instead of answering a
  bare empty array. A space inside one tag name is the multi-tag syntax, so
  `blue hair` asked Danbooru for two near-empty tokens and returned `[]`
  with no hint. When no posts come back and two adjacent query tokens join
  into a WD14 tagger-vocabulary name, the response adds `possible_tags`:
  `{"posts":[],"possible_tags":["blue_hair"]}`. The same hint rides the
  local too-many-tags error, where a space-split pair burns two of the
  slots (`blue hair one two three four five` is rejected although
  `blue_hair` alone is a legal query). The candidates come from the
  embedded vocabularies, so the diagnostic costs no request; the server
  never retries or rewrites the query, and every other payload is
  unchanged.
- `search_posts` gains a count mode: `count_only: true` answers
  `{"query":"arm_up","total":317377}` (how many posts match the tags)
  instead of listing them. The total is Danbooru's own count for the query
  (exact for plain tag queries), taken from `/counts/posts.json` without
  fetching any post; `limit` and `tags_mode` do not apply, the same tag
  limit is checked locally first, and metatags work as in a post search
  (`arm_up rating:g` counts only the all-ages posts). When the query needs
  the multi-tag scan below, the count runs over the scan and the answer
  adds `scanned` and `complete` — `total` is a lower bound until
  `complete` is true. Every call is live;
  nothing is cached. When Danbooru cannot count a query because its count
  timed out (expensive metatags such as `filetype:` can cause this), the
  call returns an error rather than `0`, which would read as "no such
  posts".
- `search_posts` accepts up to 6 content tags per query (it stopped at 2).
  Danbooru itself answers only 2 tags anonymously (3 with a login), so a
  longer query is answered by a local scan: the rarest tags go upstream as
  one listing request, and the rest are checked against each post's own
  tag list — which reproduces the query exactly, in Danbooru's order.
  Split answers add `upstream_query` (the query actually sent), `scanned`
  (posts examined) and `complete` (false = a prefix, or a lower-bound
  count); count mode runs the same scan. Metatags that consume Danbooru's
  term budget (`order:` counts, `rating:` is free) shrink the upstream
  part to one tag, and tokens a local scan cannot evaluate — `random:`
  (it caps the listing), wildcards `*`, OR `~` — refuse the query with a
  clear local error instead of a wrong answer. With credentials the whole
  query is tried directly first and the scan takes over on Danbooru's
  tag-limit 422, so a logged-in free account answers 3 tags directly and
  4-6 by scan.

### Changed

- Every `/posts.json` request now sends `only=` restricted to the fields
  the server consumes, shrinking a post from ~3.5 KB to ~1 KB on the wire
  (direct answers, `get_tag_profile` samples and scan pages alike); the
  response shape is unchanged. The space-split `possible_tags` diagnostic
  now recognizes colon-bearing pairs (`kafka_(honkai: star_rail)` names
  `kafka_(honkai:_star_rail)` where it used to stay a bare empty answer),
  because the metatag check became an exact key list; with it went the old
  "rating: and most other metatags do not count" parenthetical from the
  too-many-tags error — the counting rule is now Danbooru's own.
- `get_related_tags` now ranks its entries by lift and gives each one a
  `lift` field: how many times more often the tag co-occurs with the query
  tag than its share of all posts predicts (`frequency x site-wide posts /
  post_count`). Danbooru's own order is plain frequency, which puts corpus
  constants such as `1girl` and `solo` on top and hides the tag-specific
  companions; by lift they come first. Entries backed by fewer than five
  sampled posts keep Danbooru's order at the end instead of being ranked,
  `limit` is applied after the ranking, and the request to Danbooru grows
  from 100 to 500 candidate entries. The site-wide post count the formula
  divides by is fetched once per process; while it is unavailable the answer
  is exactly the previous one: Danbooru's order, no `lift`. An empty answer
  is now `{"related":[]}` instead of `{"related":null}`.

### Fixed

- `get_tag_profile` no longer rejects valid Danbooru names that contain a
  colon. The `tag` and `with` arguments used to refuse any input holding a
  `:`, which turned away real names such as `firefly_(honkai:_star_rail)`
  and `honkai:_star_rail`: a colon inside a name is not metatag syntax,
  and `get_tag_info`, `get_tag_wiki` and `search_tags` accept them. The
  check now mirrors Danbooru's own rule for what a tag name may not begin
  with: a known metatag key such as `order:` or `rating:`, and edit-only
  syntax such as `artist:` or `character:` (case-insensitive, an optional
  leading `-` or `~` included) are still rejected with the same message,
  and so is whitespace-separated multi-tag input, now for every whitespace
  character, not only the space.

## [0.7.0] - 2026-11-28

### Breaking

- The on-disk response cache (shipped in 0.6.0) is removed: every tool now
  answers from live Danbooru on every call. The `refresh` parameter leaves
  the six tag tools (`search_tags`, `get_tag_info`, `get_tags_info`,
  `get_related_tags`, `get_tag_alias`, `get_tag_wiki`) and the `stale` /
  `cached_at` response fields are gone; a stale client sending
  `refresh:true` gets it silently ignored, so drop the parameter. The
  `DANBOORU_MCP_NO_CACHE` environment variable is a no-op and no longer
  documented. What the cache actually delivered did not justify its upkeep:
  over 64 days of real call history it saved about 292 seconds of tool
  latency and rescued exactly one outage, and `refresh` was never used
  once. Bounded retry and shrunken error text — the parts that carried the
  value — are unchanged. A leftover
  `%LOCALAPPDATA%\danbooru-tag-mcp\cache` directory from 0.6.x is inert
  (never read, written or deleted at runtime; the uninstaller still removes
  it) and can be deleted manually at any time. With no cache, request costs
  that hits used to hide are paid on every call: `get_tag_profile` issues
  its row lookup plus the posts request, and `get_tags_info` one batch
  request plus one alias request per 0-post row.
- Upstream failures are now unmistakable: when a request fails on a network
  error, a 5xx or a 429/421 and stays failed after retries, every one of
  the eight tools returns the same result flagged at the protocol level
  (`isError: true`) with one code —
  `{"error":"upstream_unavailable","message":"Danbooru is not answering
  right now: <cause>. All tools of this server are affected; retry
  later."}`. Agents matching the old per-tool codes must switch to the new
  code. `get_tag_info` no longer reports outages as `tag_not_found`: that
  code now appears only for a genuine not-found (unchanged payload with the
  `wd14` verdict), while its other non-upstream failures get the new
  `tag_info_failed` code. A definitive 4xx, a validation error, a parse
  error and a caller cancellation stay unmarked plain results.

### Changed

- `help` (`-h` / `--help`) is rewritten: the Commands section documents
  every accepted spelling (aliases included) and what each command does and
  returns, the exit codes are listed, and the install instructions are gone
  — they restated a subset of the README and only mattered before the
  binary existed. The Commands section is rendered from the same table the
  router dispatches through, so a command cannot be accepted without being
  documented.

### Fixed

- Transport errors no longer leak credentials: when a request fails
  mid-flight (timeout, DNS failure, connection refused, cancellation), the
  error text previously embedded the full request URL including the
  `login` / `api_key` query parameters, and that text reached MCP tool
  error payloads. Error messages now keep the scheme, host and path of the
  request plus the underlying cause, but never the query string. Error
  semantics are unchanged (`errors.Is` against `context.Canceled` /
  `context.DeadlineExceeded` still matches; retry behavior is untouched).

## [0.6.0] - 2026-11-12

### Added

- On-disk response cache with stale fallback: the six tag tools
  (`search_tags`, `get_tag_info`, `get_tags_info`, `get_related_tags`,
  `get_tag_alias`, `get_tag_wiki`) cache Danbooru responses per user under
  `%LOCALAPPDATA%\danbooru-tag-mcp\cache` — related tags 30d, wiki/aliases
  7d, exact tag lookups 7d (a tag's identity row is reference data; its
  `post_count` can trail live counts, `refresh` re-fetches), tag searches
  24h. Posts are never cached (a replayed `order:random`
  page would lie about being a random sample), and empty `[]` results are
  never cached either, so new tags stay visible as soon as Danbooru knows
  them. Fresh hits are served byte-identically with no markers; when
  Danbooru is unreachable after retries, the last cached answer is served
  instead of an error, marked `stale: true` + `cached_at`. Every cacheable
  tool takes `refresh` (default false) to bypass the cache for one call and
  restart the entry's TTL clock. The cache is capped (2000 entries / 64 MB,
  oldest-first eviction), is safe to delete at any time, is removed by the
  NSIS uninstaller, and is disabled with `DANBOORU_MCP_NO_CACHE=1`.
- Bounded retry + shrunken errors: 5xx, network errors and 429 are retried
  twice with 1s/3s backoff (context-interruptible, never on caller
  cancellation; `Retry-After` is honored and capped at 30s). Non-2xx error
  bodies collapse — an HTML maintenance page renders as
  `danbooru 503 (Downbooru)` instead of inlining 2-3 KB of markup, other
  bodies truncate at 300 bytes.
- `get_tags_info`: 0-post rows now resolve `renamed_to` (one extra alias
  request each, live rows skip it, failures are non-fatal), closing the
  rename-visibility gap the batch had versus `get_tag_info`.
- `search_tags` `min_posts`: local post-count filter applied after the
  fetch (default 0 keeps all; may return fewer than `limit`).
- `search_posts` `tags_mode`: `full` (default), `none` (drop the per-post
  tag lists; id/rating/score/fav/url remain) or `diff` (per-post tag lists
  minus the query's own content tags, metatags excluded from the
  subtraction).

### Changed

- Request gap tiers: `DANBOORU_REQ_GAP_MS` overrides the spacing between
  Danbooru requests in milliseconds (`0` disables the gap; invalid values
  warn and keep the default). The default gap is now 1500ms anonymous and
  500ms when `DANBOORU_LOGIN` + `DANBOORU_API_KEY` are both set
  (browser-parity reasoning, below organic logged-in rates; the 429 /
  `Retry-After` handling is the safety net).

## [0.5.0] - 2026-11-03

### Breaking

- `get_tag_wiki` no longer returns the raw DText `body`. It is replaced by
  deterministic structured fields: `summary` (first intro paragraph, links
  flattened), `intro_tags` (definition/appearance link targets, noise like
  `tag_group:`/`help:`/`list of` filtered), `rules` (disambiguation/usage
  sentences containing a link and a directive keyword, capped),
  `see_also` (links under a See also heading) and `example_posts`
  (`!post #id` ids; `!asset` ignored). `title`, `other_names` and
  `linked_tags` keep their shapes. The new opt-in `include_description`
  argument adds `description` — the full intro prose as plain text with links
  flattened and paragraph breaks preserved — so character appearance
  descriptions (hair/eyes/outfit relationships) survive without the raw
  body. There is no raw-body fallback by design.

### Added

- `get_tag_profile` pair mode (`with`): profiles the 200 most recent joint
  posts of a tag pair (`<tag> <with>`, no `order:` — two content tags plus
  order: exceeds the anonymous 2-tag limit, verified live 422), reports
  `order: "recent"` plus a `with: {tag, post_count}` descriptor, and
  excludes both query tags from every ranked section.
- `get_tag_profile` sample controls: `order` (`random` default / `score` /
  `favcount`, top-post biased and documented as such) and `rating`
  (`g|s|q|e`, an exempt metatag that composes with single and pair modes);
  every profile now also reports the sample's `ratings` mix (fraction per
  rating over the deduplicated sample).
- `search_posts`: each post now carries `score`, `fav_count` and
  `large_file_url` (inspectable size) alongside the preview URL; `limit` is
  capped at 20 (rejected locally above that — a post averages ~44 tags, so
  the payload stays bounded).
- `search_tags` `category` filter (`general|artist|copyright|character|meta`),
  mapped to the numeric ids the API actually accepts (names return empty —
  verified live).
- `get_tag_info` `renamed_to`: on 0-post placeholder rows (renamed-away
  names), one extra alias request resolves the canonical tag
  (`sailor_suit` -> `renamed_to: "sailor"`); live rows pay no extra request
  and alias failures are non-fatal.

## [0.4.0] - 2026-10-31

### Added

- `get_tags_info` (8th MCP tool): batch exact lookup for up to 20 tag names
  in a single Danbooru request (`search[name_normalize]` accepts a
  comma-separated list — verified live), replacing N× `get_tag_info` calls
  when vetting a candidate prompt list. Returns `found` rows (same fields as
  `get_tag_info`, including the `wd14` verdict; 0-post placeholder rows
  count as found and classify `tagger_era`) plus `missing` names, each still
  carrying its local `wd14` verdict so tagger-era names stay actionable.
  One request regardless of batch size, so the client's 1.5s rate-limit gap
  is paid once; response is fixed-size rows only.
- Reproducible release builds + SHA-256 standardization (no Authenticode):
  the exe and portable zips are byte-reproducible — exact toolchain pinned
  in `go.mod`, `CGO_ENABLED=0 -buildvcs=false` builds, and a deterministic
  zip helper (`scripts/zipfile`) pinning entry timestamps to the commit time
  (`SOURCE_DATE_EPOCH`). `checksums.txt` now covers the NSIS setup exes too
  (both consumers match by bare file name and ignore extra lines), every
  artifact gets a free keyless GitHub build-provenance attestation, and a
  new `verify-repro` release job rebuilds the exe from the tagged source on
  Linux and compares it byte-for-byte against the published asset — proof
  the release was built from the tag untampered. The setup exe remains
  non-reproducible (makensis embeds build metadata) and is covered by
  checksums + attestation.
- ZCode / Claude Code plugin distribution: this repository is now a plugin
  marketplace (`.claude-plugin/marketplace.json`, mirrored at the repo root
  for ZCode), and `plugins/danbooru-tag-mcp/` packages the `danbooru-tags`
  MCP server definition so installing the plugin configures the client with
  zero manual JSON. The plugin is configuration-only by design: it launches
  the bare `danbooru-tag-mcp` command from PATH (the CLI still comes from
  the installer channels and stays updatable via `upgrade`), sets only
  `DANBOORU_MCP_NO_BOOTSTRAP=1`, and never declares credential variables —
  empty `env` placeholders would override inherited user credentials and
  silently downgrade to anonymous.

## [0.3.0] - 2026-10-01

### Added

- `get_tag_profile` (7th MCP tool): distribution profile of a single tag,
  answering "what does this big tag actually render as" with data instead of
  accumulated trial-and-error. Samples one random page of the tag's posts
  (default 200, `order:random`) and aggregates locally: `co_tags` ordered by
  in-sample frequency (the query tag and meta-category tags excluded),
  `ubiquitous` for corpus-constant tags (`freq >= 0.5`, e.g. `1girl`/`solo`
  — the baseline every big tag shares), and `top_pairs` characteristic
  combinations ranked by lift (co-occurrence above chance, e.g. arm_up
  surfaces `holding_weapon + sword`). Single-tag only by design: the sample
  query `<tag> order:random` already sits at Danbooru's 2-tag limit, and the
  sample is deliberately not rating-filtered so it describes the whole
  population.

### Changed

- `search_posts` no longer defaults to `rating:explicit`: the query goes out
  verbatim, so results reflect a tag's whole population (arm_up is ~95%
  g/s/q — the old default sampled a 5% subpopulation and contradicted
  `get_tag_profile`). Agents that want R-18 add `rating:e` themselves, and
  rating metatags are exempt from the 2-tag query limit, so opting in costs
  nothing. The server stays uncensored — it never hides a rating, it just no
  longer picks one for you.

## [0.2.2] - 2026-10-01

### Added

- `get_tag_info` classifies every name against the pinned WD14 tagger
  vocabularies via a local `wd14` verdict: `live` (current Danbooru, with
  per-vocabulary snapshot counts), `tagger_era` (tagger vocab only — likely
  renamed since, see `get_tag_alias`), `unknown` (neither). Computed from
  embedded data, so it is returned even when the API call fails; a name
  absent from Danbooru carries the verdict on the not-found error payload.
- `search_tags` adds `wd14_hits` (vocabulary names containing the query,
  best snapshot count first) when no result carries posts, turning dead-end
  queries into actionable candidates.
- Vendored snapshots of `wd-v1-4-moat-tagger-v2` and
  `wd-eva02-large-tagger-v3` under `internal/vocab/data/` (the HF dataset
  repos are now login-gated, which is exactly why the data ships in-tree).

### Changed

- `internal/vocab/data/` is a plugin directory: every `*.csv` is one
  vocabulary whose id is the file name minus the extension; adding, updating
  or removing a vocabulary is a data change only.

### Misc

- MIT license, README badges + demo GIF, MCP registry metadata
  (`server.json`).

## [0.2.1] - 2026-09-30

### Added

- `search_posts` returns each post's full tag list split by category
  (copyright/artist/character/general/meta), parsed from the API's
  `tag_string_*` fields with no extra request.

### Changed

- `get_related_tags` drops meta-category entries (`highres` & co.), which
  co-occur with everything and drown out content tags.
- MCP tool descriptions trimmed to one clause per behavioral rule (they are
  injected into every client session).

## [0.2.0] - 2026-09-25

### Added

- `get_tag_alias` and `get_tag_wiki` MCP tools (6 tools total); wiki lookup
  supports multilingual `other_names` search and extracts `[[tag]]` links.
- Double-click NSIS setup installer in the release pipeline, alongside the
  PowerShell installer.
- Unit CI on every push/PR plus a scheduled online smoke test against the
  live API.

### Changed

- Tag tools aligned with the current Danbooru API behavior and account
  limits (2 content tags per anonymous post search, pre-validated locally).

## [0.1.0] - 2026-09-24

### Added

- Initial release: stdio MCP server with `search_tags`, `get_tag_info`,
  `get_related_tags` and `search_posts`; rate-limited API client with
  optional credentials; self-update via GitHub Releases (SHA256 verified);
  per-user install with automatic PATH registration.
