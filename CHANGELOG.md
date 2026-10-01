# Changelog

Release notes are extracted from the `## [x.y.z]` section matching the tag
(see `.github/workflows/release.yml`); replace `- Unreleased` with the date
when tagging.

## [Unreleased]

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
