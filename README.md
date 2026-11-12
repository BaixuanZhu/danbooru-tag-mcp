# danbooru-tag-mcp

[![CI](https://github.com/BaixuanZhu/danbooru-tag-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/BaixuanZhu/danbooru-tag-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BaixuanZhu/danbooru-tag-mcp)](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases/latest)
[![Platform](https://img.shields.io/badge/platform-Windows%2010%2F11-0078D4?logo=windows&logoColor=white)](#install)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/github/license/BaixuanZhu/danbooru-tag-mcp)](LICENSE)

A Danbooru tag lookup [MCP](https://modelcontextprotocol.io) server for local AI
image generation. Your AI client looks tags up instead of recalling them —
exact names, aliases, categories, post counts, co-occurrence stats and wiki
rules, resolved before it writes the prompt.

Windows 10/11 on x64 or ARM64. Anonymous access works, so there is no API key
to obtain before the first query.

## Quick start

Install per-user (Windows 10/11, no admin rights):

```powershell
iwr -useb "https://raw.githubusercontent.com/BaixuanZhu/danbooru-tag-mcp/main/install.ps1" | iex
```

Point your MCP client at the command:

```json
{
  "mcpServers": {
    "danbooru-tags": {
      "command": "danbooru-tag-mcp",
      "args": []
    }
  }
}
```

That's it — the client picks up the eight tools below on its next session.

## What the client gets back

Live responses, abridged for length. Post counts move daily; these were
captured on 2026-11-12.

`search_tags` for *blue hair* — the names that actually exist, so the model
stops guessing `bluehair`:

```json
{"tags": [
  {"id": 10953, "name": "blue_hair", "category": 0, "post_count": 1234156},
  {"id": 545704, "name": "blue_hairband", "category": 0, "post_count": 33332},
  {"id": 1564040, "name": "blue_hair-chan_(ramchi)", "category": 4, "post_count": 120}
]}
```

`get_tag_info` for `blue_hair` — the count, plus whether the WD14 taggers know
the name at all:

```json
{"id": 10953, "name": "blue_hair", "category": 0, "post_count": 1234156,
 "wd14": {"status": "live", "sources": [
   {"vocab": "wd-eva02-large-tagger-v3", "count": 704952},
   {"vocab": "wd-v1-4-moat-tagger-v2", "count": 564360}]}}
```

`get_tag_profile` for `arm_up`, all-ages sample of 200 — what the tag actually
renders alongside, ranked by in-sample frequency, and which combinations are
specific to it rather than merely common:

```json
{"name": "arm_up", "post_count": 314588, "rating": "g", "sample": 200,
 "ratings": {"g": 1},
 "ubiquitous": [
   {"tag": "smile", "category": 0, "freq": 0.51},
   {"tag": "solo", "category": 0, "freq": 0.51}],
 "co_tags": [
   {"tag": "looking_at_viewer", "category": 0, "freq": 0.5},
   {"tag": "long_sleeves", "category": 0, "freq": 0.48},
   {"tag": "1girl", "category": 0, "freq": 0.46},
   {"tag": "open_mouth", "category": 0, "freq": 0.45}],
 "top_pairs": [
   {"pair": "pokemon + pokemon_(creature)", "freq": 0.05, "lift": 16.7},
   {"pair": "japanese_clothes + kimono", "freq": 0.05, "lift": 15.4}]}
```

The full profile returns 30 co-occurring tags and 10 ranked pairs. Every field
is documented under [Tools](#tools).

## Features

- **8 MCP tools** — search tags, exact and batch lookup, aliases, related
  tags, wiki pages, post search, and distribution profiles.
- **Batch lookup** — 20 exact names in one call instead of 20, with a
  found/missing split and a WD14 verdict on every name.
- **Distribution profiles** — what a tag actually renders as: co-occurring
  tags by in-sample frequency, corpus constants like `1girl`/`solo` split
  into `ubiquitous`, combinations ranked by lift, and the sample's rating
  mix. `with` profiles a tag pair instead, answering what `arm_up` plus
  `holding_weapon` renders as.
- **Structured wiki pages** — the DText body distilled into `summary`,
  `intro_tags`, `rules`, `see_also` and `example_posts`, so rules like
  "if X, use Y instead" arrive as data rather than prose to re-parse.
- **Tagger-era detection** — every name is checked against the pinned WD14
  vocabularies and classified as `live`, `tagger_era` (renamed upstream, the
  canonical name included) or `unknown`, so LoRA card captions stop
  dead-ending at "tag not found".
- **Post tag lists** — each post comes back with its full tag list split by
  category, so mining a few posts shows which tags naturally go together. No
  rating filter by default; add `rating:g` for all-ages only.
- **Built to stay installed** — an on-disk cache that serves stale answers
  during outages, `upgrade` verifying SHA256 and swapping the binary
  atomically, a per-user installer that registers PATH, and 1.5 s request
  spacing (500 ms with credentials) that keeps anonymous access viable.

Tags come back as plain strings with no model coupling, so they paste
straight into ComfyUI, Stable Diffusion or Illustrious prompt fields.

## Install

Requires Windows 10/11 on x64 or ARM64. Every option installs per-user, needs
no admin rights, and puts the install directory on the user PATH, so the bare
`danbooru-tag-mcp` command works afterwards.

### Option 1: setup wizard (recommended)

Download `danbooru-tag-mcp-windows-<arch>-setup.exe` from the
[latest release](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases/latest)
and double-click it. It installs to `%LOCALAPPDATA%\Programs\danbooru-tag-mcp`.

### Option 2: one-line PowerShell

```powershell
iwr -useb "https://raw.githubusercontent.com/BaixuanZhu/danbooru-tag-mcp/main/install.ps1" | iex
```

Custom install directory:

```powershell
.\install.ps1 -InstallDir "D:\tools\danbooru-tag-mcp"
```

### Option 3: portable zip

Download `danbooru-tag-mcp-windows-<arch>.zip` from the
[release page](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases),
extract `danbooru-tag-mcp.exe` anywhere, and run it once — the first run
silently adds its own directory to the user PATH (idempotent).

## Configure your MCP client

Point your MCP client (Claude Desktop, ZCode, etc.) at the command:

```json
{
  "mcpServers": {
    "danbooru-tags": {
      "command": "danbooru-tag-mcp",
      "args": []
    }
  }
}
```

The bare command name works because the install directory is on the user PATH;
alternatively use the full exe path.

### Plugin instead of JSON (ZCode / Claude Code)

Add this repository as a plugin marketplace and install `danbooru-tag-mcp` —
the plugin ships the `danbooru-tags` server definition, so there is no JSON to
hand-edit. It wires configuration only: finish one of the install options
above first, so the CLI is actually on PATH.

- **ZCode**: Plugin Marketplace → Add → `BaixuanZhu/danbooru-tag-mcp` →
  install the `danbooru-tag-mcp` plugin.
- **Claude Code**: `/plugin install danbooru-tag-mcp --marketplace BaixuanZhu/danbooru-tag-mcp`
  (one step, Claude Code 2.1.275+), or add the marketplace first with
  `claude plugin marketplace add BaixuanZhu/danbooru-tag-mcp` and install
  from there.

`danbooru-tag-mcp upgrade` keeps the CLI updated independently of the plugin.
See [plugins/danbooru-tag-mcp/README.md](plugins/danbooru-tag-mcp/README.md).

## Tools

| Tool | What it does |
|------|--------------|
| `search_tags` | Search tags by keyword, ordered by post count |
| `get_tag_info` | Exact info for one tag, with a WD14 verdict on the name |
| `get_tags_info` | The same for up to 20 names in one request |
| `get_related_tags` | Tags that co-occur with a given tag |
| `get_tag_alias` | Resolve an alias or misspelling to its canonical tag |
| `get_tag_wiki` | A tag's wiki page as structured fields |
| `search_posts` | Posts matching a tag combination, with their tag lists |
| `get_tag_profile` | What a tag actually renders as, from a post sample |

<details>
<summary>Parameters and return fields for all eight tools</summary>

#### `search_tags`

`query` (required, e.g. `blue hair`) · `limit` (10) · `category`
(`general`/`artist`/`copyright`/`character`/`meta`) · `min_posts` (0, drops
tags below the threshold) · `refresh` (false)

When no result carries posts, the response adds `wd14_hits` — WD14 tagger
vocabulary names containing the query.

#### `get_tag_info`

`name` (required, e.g. `blue_hair`) · `refresh` (false)

Returns category and post count plus a `wd14` verdict: `live` (current
Danbooru), `tagger_era` (WD14 vocabulary only, likely renamed — the canonical
name is in `renamed_to`) or `unknown` (neither). A 0-post name with an active
alias also returns `renamed_to`.

#### `get_tags_info`

`names` (required, e.g. `["blue_hair","1girl"]`, max 20) · `refresh` (false)

Returns `found` (each with the fields of `get_tag_info`, `renamed_to`
included on 0-post rows) and `missing` (absent from Danbooru, each still
carrying its `wd14` verdict).

#### `get_related_tags`

`tag` (required) · `limit` (10) · `refresh` (false)

Meta-category tags like `highres` are filtered out as noise.

#### `get_tag_alias`

`name` (required, e.g. `sailor_suit`) · `refresh` (false)

`null` means the input is already canonical.

#### `get_tag_wiki`

`title` or `other_names` (one required) · `limit` (5) · `include_description`
(false) · `refresh` (false)

Returns `summary` (first paragraph), `intro_tags` (definition-section link
targets), `rules` (disambiguation and usage sentences), `see_also`,
`example_posts`, the raw `linked_tags` union and multilingual `other_names`.
The raw DText body is not returned; `include_description` adds the intro prose
as plain text, for character appearance descriptions.

#### `search_posts`

`tags` (required, space-separated) · `limit` (5, max 20) · `tags_mode`
(`full` default, `none` drops the per-post tag lists, `diff` keeps only tags
not already in the query)

No rating filter by default; add a `rating:g/s/q/e` metatag to narrow. Each
post carries `score`, `fav_count`, `large_file_url` and `tags` split by
category: `copyright` / `artist` / `character` / `general` / `meta`.

#### `get_tag_profile`

`tag` (required, e.g. `arm_up`) · `sample` (200, range 20-200) · `with` ·
`rating` (`g`/`s`/`q`/`e`) · `order` (random by default, or `score` /
`favcount` for a top-post view, which is biased)

Samples a page of the tag's posts and returns `co_tags` ordered by in-sample
frequency, `ubiquitous` corpus constants (`freq >= 0.5`), `top_pairs`
combinations ranked by lift, and the `ratings` mix. Random sampling covers the
whole population; `rating` filters the sample; `with` profiles the 200 most
recent joint posts of a tag pair instead.

</details>

The six tag tools (`search_tags`, `get_tag_info`, `get_tags_info`,
`get_related_tags`, `get_tag_alias`, `get_tag_wiki`) read from the
[response cache](#response-cache): repeat calls inside the TTL skip the
network round trip, and during a Danbooru outage the last cached answer is
served instead of an error, marked with `stale: true` and `cached_at` (when
it was stored). Their `refresh` parameter (default false) bypasses the cache
for a single call and re-fetches live. Post search and profiles are never
cached, so they carry no `refresh`.

The six tag tools (`search_tags`, `get_tag_info`, `get_tags_info`,
`get_related_tags`, `get_tag_alias`, `get_tag_wiki`) read from the
[response cache](#response-cache): repeat calls inside the TTL skip the
network round trip, and during a Danbooru outage the last cached answer is
served instead of an error, marked with `stale: true` and `cached_at` (when
it was stored). Their `refresh` parameter (default false) bypasses the cache
for a single call and re-fetches live. Post search and profiles are never
cached, so they carry no `refresh`.

## Response cache

Repeat queries are answered from an on-disk cache instead of hitting Danbooru
again:

| Data | TTL |
|------|-----|
| Related tags (`get_related_tags`) | 30 days |
| Wiki pages, aliases | 7 days |
| Tag searches (`search_tags`) | 24 hours |
| Exact tag lookups (`get_tag_info`, `get_tags_info`) | 7 days |
| Posts (`search_posts`, `get_tag_profile`) | never cached |

- Location: `%LOCALAPPDATA%\danbooru-tag-mcp\cache` (per user, persists
  across restarts, capped at 2000 entries / 64 MB with oldest-first
  eviction). It holds only API response bodies and is safe to delete at any
  time; the uninstaller removes it.
- Fresh hits are served byte-identically to a network response — no markers.
- When Danbooru is unreachable (after retries), the expired cached answer is
  served instead of an error, marked `stale: true` plus `cached_at` (when it
  was stored) on the affected tool response.
- Empty results (`[]`) are never cached, so new tags appear as soon as
  Danbooru knows them.
- Exact tag lookups keep a tag's identity row for 7 days (like wiki pages,
  name and category barely change), so `post_count` can trail live Danbooru
  by up to 7 days — use `refresh: true` when a decision needs current
  counts.
- `refresh: true` re-fetches live and restarts the entry's TTL clock.
- Set `DANBOORU_MCP_NO_CACHE=1` to disable the cache entirely (useful for
  debugging).

## CLI

```
danbooru-tag-mcp            run the MCP server (what MCP clients launch)
danbooru-tag-mcp upgrade    self-update via GitHub Release (SHA256 verified)
danbooru-tag-mcp version    print the version
danbooru-tag-mcp help       show help
```

## Environment variables

- `DANBOORU_LOGIN` — your Danbooru account name (the login you use on the
  site). Optional.
- `DANBOORU_API_KEY` — your Danbooru API key, generated from the API key
  section of your Danbooru profile. Optional. When both `DANBOORU_LOGIN` and
  `DANBOORU_API_KEY` are set, every request is sent authenticated with your
  account (higher rate limits and content access); otherwise the client is
  anonymous and Danbooru applies lower limits.
- `DANBOORU_REQ_GAP_MS` — minimum spacing between Danbooru requests, in
  milliseconds. `0` disables the gap; invalid values fall back to the
  default with a warning. Defaults: 1500 (anonymous) or 500 when
  `DANBOORU_LOGIN` and `DANBOORU_API_KEY` are both set.
- `DANBOORU_MCP_NO_BOOTSTRAP` — skip the startup PATH registration when
  non-empty.
- `DANBOORU_MCP_NO_CACHE` — disable the [response cache](#response-cache)
  when non-empty.

## Building from source

Requires Go 1.26+ and GNU Make (bundled with Git Bash):

```bash
make test        # unit tests
make integration # online smoke test against the live Danbooru API (~1 min)
make build       # dev build -> dist/<arch>/ (bootstrap disabled)
make release     # portable zip + checksums.txt
make installer   # NSIS setup exe (requires makensis: scoop install nsis)
```

Releases are automated: pushing a `v*` tag makes GitHub Actions run the test
suite and publish the setup exes, portable zips, and `checksums.txt` for both
architectures.

## Verifying a download

Binaries are not Authenticode-signed; integrity rests on reproducible builds,
SHA-256 checksums and build provenance.

```bash
sha256sum -c --ignore-missing checksums.txt   # integrity vs the manifest
gh attestation verify --repo BaixuanZhu/danbooru-tag-mcp danbooru-tag-mcp.exe   # provenance
```

[How the builds stay byte-reproducible, and how to rebuild one yourself →
](docs/reproducible-builds.md)
