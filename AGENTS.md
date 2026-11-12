# AGENTS.md

Danbooru tag lookup MCP server (Go). With no arguments it serves 8 tools over
stdio (`search_tags` / `get_tag_info` / `get_tags_info` / `get_related_tags` /
`get_tag_alias` / `get_tag_wiki` / `search_posts` / `get_tag_profile`) to
help local AI image generation pick correct Danbooru tags; it is also a
self-updatable CLI (`upgrade` / `version` subcommands). User-facing docs live
in `README.md`; agent-facing conventions live here.

## Language convention (important)

Everything in this repo is written in **English only**: code comments, doc
comments, CLI output, logs, test names, Makefile/PowerShell echoes. The user
reads English comfortably; Chinese text garbles under the zh-CN Windows console
code page (this bit `make` echoes before). Do not introduce Chinese anywhere.

## Common commands

```bash
make build                          # dev build (ldflags injects Bootstrap=off, no bootstrap) -> dist/<arch>/
make build-dist                     # release build (bootstrap on)
make run ARGS="version"             # build and run a subcommand
make release                        # build + deterministic zip + checksums.txt -> dist/
make zip                            # deterministic zip of the already-built exe (no rebuild)
make installer                      # build + NSIS setup exe -> dist/danbooru-tag-mcp-windows-<arch>-setup.exe
make installer-only                 # package setup exe from the already-built exe (no rebuild)
make dist-all                       # both release assets (current GOARCH)
go vet ./...                        # static check
make test / make test-race          # tests / race detector
make integration                # online smoke: every tool against live Danbooru (+ tagger-era wd14 checks)
```

Smoke test (verifies MCP handshake and tool listing):

```bash
printf '%s\n%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0.0.1"}}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | /tmp/danbooru-mcp.exe
```

## Architecture and layer rules

All Go code lives under `internal/`. Dependency direction: `main.go` (command
routing only) -> `internal/tools` -> `internal/service` -> `internal/api`;
layers are decoupled via interfaces for mock-based unit tests:

- **internal/api/**: Danbooru REST HTTP client. `Client.Get` is the **single
  choke point** for all outbound requests (rate-limit gap + mutex, context
  cancellation, UA/credential injection, 5MB body cap, bounded retry, error
  shrink, and the response-cache read/write/stale-fallback policy — see the
  reliability boundary below). New API endpoints must go through it; never
  use `http` directly.
- **internal/cache/**: persistent response cache, a stdlib-only leaf package
  (same discipline as internal/vocab). `Store` keeps one JSON entry per key
  under `<dir>/v1/<sha256>.json` (atomic temp+rename writes; oldest-by-mtime
  eviction under a dual cap: 2000 entries AND 64MB total payload);
  `Result` carries the body plus stale provenance; `WithRefresh` /
  `RefreshRequested` thread the per-call cache-bypass flag through ctx.
  Knows nothing about HTTP, Danbooru, or TTLs — the api layer owns those.
- **internal/service/**: business logic and JSON parsing; depends on the
  `TagFetcher` interface (unaware of the concrete client) and on the
  `vocab.Store` for WD14 classification.
- **internal/tools/**: mcp-go tool registration and handlers; depends on the
  `TagService` interface. Tool instances (`SearchTagsTool` etc.) are
  package-level exported vars and `register_test.go` statically validates
  their schemas — changing tool definitions requires updating those tests.
- **internal/vocab/**: WD14 tagger vocabularies as a plugin directory —
  every `data/*.csv` is go:embed'd as one vocabulary whose id is the file
  name minus the extension; adding, updating or removing a vocabulary is a
  data change only (provenance and rules in `internal/vocab/data/README.md`).
  Leaf package with no dependencies; nil `*Store` answers miss so callers
  can run unenriched.
- **internal/app/**: `Version` (ldflags injected), `Fail`, `UserAgent`, and
  the bootstrap switch (`BootstrapEnabled`).
- **internal/env/**: user PATH injection (registry `HKCU\Environment` +
  WM_SETTINGCHANGE broadcast; never setx).
- **internal/upgrade/**: GitHub Release self-update (version compare ->
  download zip -> SHA256 verify via checksums.txt -> atomic .bak replace).

**Subcommand routing** (main.go): no args or `serve` = MCP server mode (keeps
zero-config client compatibility); `upgrade` / `version` / `help` are CLI
mode, where writing to stdout is allowed.

**Response cache + retry + error shrink (reliability boundary, test-enforced,
do not break)**: `Client.Get` caches the four stable endpoints with per-class
TTLs (`/related_tag.json` 30d, `/wiki_pages.json` + `/tag_aliases.json` 7d;
`/tags.json` splits by query shape — substring search 24h, exact-name lookup
(`search[name]`/`search[name_normalize]`) 7d via the `tagsExactClass` TTL
class: identity rows are reference data like wiki/alias while substring
searches must keep surfacing new tags, and the accepted cost is an exact
row's `post_count` trailing live Danbooru by up to 7 days — `refresh:true`
when a decision needs current counts); the cache key is `path + "?" + params.Encode()` computed
BEFORE credential injection, so credentials never reach disk and
anonymous/authenticated callers share entries. `/posts.json` never touches
the Store (replayed `order:random` pages and whole-population profile
samples would be lies) and empty `[]` bodies are never cached (dead-end
searches stay cheap to refetch; caching them would hide new tags for a whole
TTL). A fresh hit returns before the rate-limit wait, byte-identical and
unmarked (concurrency contract: the cache read is lock-free — a hit never
queues behind a network caller; `c.mu` guards only the rate-limit
accounting while sleeps, retries, and cache writes run unlocked); after
retries are exhausted on a retryable failure the expired
entry is served instead of an error, marked `stale: true` + `cached_at`
(RFC3339) on the affected tool response — stale-only marking (user decision:
fresh-hit markers add payload with no actionable information), so both fields
are `omitempty` and absent everywhere else. The 6 cacheable tools take
`refresh` (boolean, default false): the tools handler wraps ctx via
`cache.WithRefresh`, `Get` skips the fresh-hit shortcut and runs the network
path unchanged, and a successful `Put` restarts the entry's TTL clock; a
failed refresh still stale-serves. Retries: up to 2 retries on 5xx, network
errors, and 429/421 with 1s/3s backoff (ctx-interruptible waits; caller
cancellation never retries and never stale-serves; a parseable `Retry-After`,
capped 30s, replaces the first backoff). Non-2xx error bodies are shrunk:
HTML collapses to status + `<title>` (live Downbooru 503 shape:
`danbooru 503 (Downbooru)` instead of ~2-3KB of markup), other bodies
truncate at 300 bytes. Request gap tiers: `DANBOORU_REQ_GAP_MS` (0 valid =
no gap; invalid or negative warns once on stderr and keeps the tier default)
> credentialed 500ms (`DANBOORU_LOGIN` + `DANBOORU_API_KEY` both set) >
anonymous 1500ms; `WithReqGap` still overrides everything (tests use it).
`DANBOORU_MCP_NO_CACHE` (checked in main.go `clientOptions`, alongside an
empty-LOCALAPPDATA guard) disables the cache; every Store failure degrades
to cache-less with a stderr WARN — a broken cache must never break requests.

**Rating filter boundary (test-enforced, do not break)**: **no layer injects
a rating filter** — neither the api layer's `FetchPosts`
(`TestFetchPosts_DoesNotAddRatingFilter`) nor the service layer's
`SearchPosts` (`TestSearchPosts_NoRatingInjection` asserts the query goes
out verbatim). Rating choice belongs to the caller: a `rating:g/s/q/e`
metatag narrows a query and is exempt from the tag-count limit, so opting
in costs nothing toward the 2-tag cap. History: v0.1.0–0.2.x defaulted
`SearchPosts` to `rating:explicit` as an R-18-allowed product statement;
dropped in 0.3.0 because a default filter biases research queries toward a
~5% subpopulation (arm_up is 95% g/s/q — verified live) and contradicted
`get_tag_profile`'s whole-population sample; "uncensored, unfiltered"
expresses the product stance without picking a rating for the caller. The
`search_posts` tool description documents the g/s/q/e ladder so MCP clients
know the opt-in syntax. `SearchPosts` still pre-validates the tag count
locally (max 2; content tags and `order:` metatags count, `rating:`/
`status:`/`id:` style metatags are exempt — verified empirically) so
over-limit queries fail with a clear error instead of a Danbooru 422.

**Result shaping (inspiration discovery, the product's weak spot turned
strength)**: `SearchPosts` returns each post's full tag list split by category
(`PostTags`, parsed from the API's `tag_string_*` fields — no extra request)
plus `score`/`fav_count`/`large_file_url`, and caps `limit` at 20
pre-flight (a post averages ~44 tags — verified live — so 20 bounds the
payload at ~20 KB). `SearchPosts` also takes a tags mode: `full` (default,
byte-identical to the old shape) / `none` (per-post `PostTags` omitted;
id/rating/score/fav/url remain) / `diff` (each post's lists minus the
query's own content tags, metatags excluded from the subtraction).
`Related` drops both the query tag itself and
meta-category (5) entries, because meta tags like `highres` co-occur with
everything and drown out content tags. Category ids are 0=general, 1=artist,
3=copyright, 4=character, 5=meta (Danbooru's actual API numbering — verified
live, do not "fix" to the 0-4 continuous scheme). `Search` takes a numeric
category id (-1 = no filter); the API rejects category names (verified live)
so the tools layer maps via `service.CategoryID` before any request. `Search`
also takes `minPosts` (local post-count filter applied after the fetch,
default 0 keeps all and may return fewer than `limit`; a filtered-to-empty
result still counts as no-live-result for the wd14 fallback). These
behaviors are pinned by unit tests and the integration script.

**Wiki structured extraction (get_tag_wiki, test-enforced, do not break)**:
the raw DText `body` is REMOVED from the response (breaking change, user
decision) and replaced by deterministic fields extracted in
`internal/service/tag.go` (`extractWikiPage`): split on `h1.`–`h6.` headings
(intro = before the first heading, bodies carry \r\n so line endings are
normalized first); `summary` = first intro paragraph with `[[target|display]]`
links flattened to display text; `intro_tags` = intro link targets minus
noise (`tag_group:`, `help:`, `list of`); `rules` = sentences carrying a link
AND a directive keyword ("instead", "not to be confused", "should", "use",
...), capped 6/500 chars — a keyword heuristic, not a parser, so do not
promise complete rule coverage; `see_also` = links under a See also heading;
`example_posts` = unique `!post #id` ids (`!asset` ignored). The opt-in
`include_description` adds `description` (full intro prose, links flattened,
paragraph breaks preserved) so character appearance relationships survive —
it is filtered intro text, NOT the raw body. `linked_tags` keeps its old raw
union shape. Tests pin real live bodies (`arm_up`, `sailor`, `1girl`,
`firefly_(honkai:_star_rail)`, `highres` fixtures in `wiki_test.go`); update
fixtures deliberately if the upstream pages change.

**Tag distribution profile (get_tag_profile, test-enforced, do not break)**:
answers "what does this big tag actually render as" (arm_up: 309k posts,
mostly bent-arm poses, drowning arm_above_head's 3.2k) by sampling ONE
request and aggregating locally in `internal/service/profile.go`: singles
counted per post (query tags and meta-category dropped), split at
freq >= 0.5 into `ubiquitous` (corpus constants like 1girl/solo — verified
live to be the raw top 10 of every big tag, not a property of the tag) vs
freq-ordered `co_tags`; `top_pairs` combinations ranked by **lift** (joint
freq over the product of singles freqs) because naive pair frequency
reproduces the same 1girl+solo baseline every time — lift surfaces
tag-specific poses (arm_up: holding_weapon+sword, lift ~12). The response
echoes `order`/`rating` and carries a `ratings` mix (fractions over the
deduped sample). Boundaries pinned by unit tests: the DEFAULT single-tag
sample query must stay `<tag> order:random` with **no rating filter**
(`Profile` calls `fetcher.FetchPosts` directly, NOT `SearchPosts` — a
rating:explicit sample would describe arm_up's 5% explicit subpopulation,
pinned by `TestProfile_UbiquitousSplitAndExclusions`; `order: score` /
`favcount` and `rating: g/s/q/e` are caller opt-ins, documented as
top-post-biased/filtered views); `order:` counts toward the 2-tag query
limit (verified live: `arm_up 1boy order:random` 422s), so pair mode
(`with`) DROPS order: entirely and samples `<tag> <with>` — the 200 most
recent joint posts, `order: "recent"`, both query tags excluded from every
ranked section — and rejects a caller-supplied `order`. `rating:` composes
with both modes (exempt from the tag cap). Renamed/0-post tags return an
empty profile whose embedded `wd14` verdict (and `renamed_to`, resolved on
the primary row only) tells the story, mirroring `get_tag_info`'s NotFound
handling; a missing pair tag returns `*NotFoundError{Name: <missing name>}`
so the handler's wd14 rider (which uses `nf.Name`) covers the right name.

**Tagger-era detection (WD14 layer, test-enforced, do not break)**:
`internal/vocab` embeds the WD14 tagger vocabularies (`wd-v1-4-moat-tagger-v2`
and `wd-eva02-large-tagger-v3`) as immutable snapshots loaded from the plugin
directory `data/*.csv` — the whole value is historical lookup, so they are
never refreshed; re-pinning is deliberate and trips
`TestDefault_PinnedSnapshots`, while `TestDefault_PluginDirectoryIsTheRegistry`
pins the load-exactly-what's-in-data contract. CSV `category` uses Danbooru numbering plus
**9 = the tagger's rating buckets** (`general`/`sensitive`/...), which are
tagger output columns, not Danbooru tags. `get_tag_info` attaches a `wd14`
verdict computed locally (available even when the API call fails): `live`
requires a Danbooru row **with posts** — Danbooru returns 0-post placeholder
rows for renamed-away names (verified live: `barefoot_sandals`,
`gold_footwear`, even `painted_toenails` all return rows), and those classify
as `tagger_era` when a vocabulary carries them (`pinned by
TestInfo_ZeroCountRowIsTaggerEra`) or `unknown` when none does. A name truly
absent from Danbooru yields a `*service.NotFoundError`, and the tools handler
rides the `wd14` verdict on that error payload. On 0-post rows `Info` also
resolves `renamed_to` via one extra alias request (`resolveRenamedTo`, live
rows skip it; alias errors are non-fatal) — every sampled active alias
antecedent keeps a 0-post row (verified live). `Infos` (batch) resolves
`renamed_to` the same way since 0.6.0 (see the batch paragraph).
`search_tags` adds
`wd14_hits` (vocabulary substring matches, best snapshot count first) when no
result carries posts (`hasLiveTag`). The wd14 policy translation (probe the
canonical form via `get_tag_alias`, or describe in prose) stays with the
consuming agent — the server reports facts only.

**Batch tag lookup (get_tags_info, test-enforced, do not break)**: resolves
up to 20 exact names in **one** Danbooru request — `search[name_normalize]`
accepts a comma-separated list while `search[name]` does NOT (both verified
live); the cap (`maxBatchNames`) and the empty-input check fire pre-flight,
before any fetch (`TestInfos_RejectsBadInputBeforeAnyRequest`). Semantics
align with `get_tag_info`: 0-post placeholder rows are `found` and classify
`tagger_era`; names absent from Danbooru go to `missing`, each carrying the
local `wd14` verdict (the batch counterpart of the NotFoundError rider);
all-missing is a valid answer, not an error. `Found` rows carry the canonical
API row name (rows are matched case-insensitively because the API normalizes
case while `normalizeTag` does not). Since 0.6.0 `Found` rows with
`post_count == 0` also resolve `renamed_to` (superseding the old
"batch does not resolve aliases" boundary): `Infos` loops
`s.resolveRenamedTo(ctx, &result.Found[i])` — one extra alias request per
0-post row, live rows skip it, alias failures are non-fatal. The bound is
`maxBatchNames` (<=20 extra requests worst case), amortized to zero by the
7d alias cache on repeat batches. `search[antecedent_name]` comma lists
return empty and no `_any`/`_normalize` variant exists (probed live
2026-11-12: those params are silently ignored — the response is
byte-identical to the no-search-param default listing), so the resolution
cannot collapse into the batched request itself.
Deliberately NOT batched: related/wiki/profile/posts — those have unbounded
payloads; the batch stays on fixed-size rows so the response is a few KB
max. `Info` keeps using `FetchTagExact` (its tests pin that path; do not
migrate it).

**Plugin distribution (ZCode / Claude Code, configuration-only)**:
`plugins/danbooru-tag-mcp/` is the plugin source — a pure MCP wiring plugin,
no skills, no binaries, no download logic. Its `.mcp.json` launches the bare
command `danbooru-tag-mcp` (PATH resolution) with only
`DANBOORU_MCP_NO_BOOTSTRAP=1` in `env`, so the exe itself stays owned by the
installer channels and `upgrade`; the plugin version (in
`.claude-plugin/plugin.json`, `.zcode-plugin/plugin.json`, and both
marketplace files) is the packaging version, decoupled from the CLI version —
bump it only when the plugin files change, and keep the four files in sync
(two manifests + root `marketplace.json` + `.claude-plugin/marketplace.json`,
which are identical mirrors; ZCode reads either location, Claude Code reads
the `.claude-plugin/` one). Never declare `DANBOORU_LOGIN` /
`DANBOORU_API_KEY` in the plugin's `env` — not even as empty placeholders:
explicitly declared values override the inherited environment in MCP
clients, so an empty string silently downgrades authenticated users to
anonymous (server-side, `client.go` injects credentials only when both are
non-empty). Credentials flow through user-level environment variables by
inheritance; this is documented in the plugin README.

## Key conventions and gotchas

- **stdout carries JSON-RPC only**: in MCP server mode all logs must go to
  stderr (`main.go` sets `log.SetOutput(os.Stderr)`). The `version` / `help` /
  `upgrade` subcommands are CLI mode and may write stdout; never add stdout
  output on the server-mode path or the stdio transport breaks immediately.
- **Self-update conventions** (shared by `internal/upgrade` and
  `install.ps1`; keep both in sync): asset naming
  `danbooru-tag-mcp-{GOOS}-{GOARCH}.zip` (contains a single
  `danbooru-tag-mcp.exe`); `checksums.txt` in sha256sum format matched by bare
  file name (tolerates binary-mode `*` prefix; covers both zips AND both
  setup exes — the consumers match by bare file name and ignore extra lines);
  Windows cannot overwrite a running exe, so replacement goes through a `.bak`
  rename, with leftovers cleaned by `CleanupStaleBak` on next startup.
  Publishing a release: push a `v0.2.0`-format tag; the workflow builds and
  uploads the assets automatically (no manual upload).
- **Reproducible builds (release integrity model — no Authenticode)**: the
  exe and portable zips are byte-reproducible; that is the trust story and
  must not regress. The contract: exact toolchain pinned via the `toolchain`
  directive in `go.mod` (CI reads it with `go-version-file`), build flags
  `CGO_ENABLED=0 -trimpath -buildvcs=false` in both Makefile build targets,
  and zips written by `scripts/zipfile` with entry timestamps pinned to
  `SOURCE_DATE_EPOCH` (= commit time; Compress-Archive is banned for release
  zips — local-time DOS timestamps break determinism). Gotcha: the zip
  recipe strips GOOS/GOARCH (`env -u`) around `go run` because command-line
  make variables are exported to recipes — `make zip GOARCH=arm64` would
  otherwise cross-compile the packager itself and fail on the runner (this
  broke the first v0.4.0 release run). The release workflow
  builds each arch exe **once**, then packages via `make installer-only` /
  `make zip` (both no-rebuild; do not reintroduce phony rebuilds before
  packaging), runs a `verify-repro` job that rebuilds from the tagged source
  on Linux and compares hashes with the published asset, and attaches free
  keyless `actions/attest-build-provenance` attestations. Known boundary:
  the NSIS setup exe is NOT byte-reproducible (makensis embeds build
  metadata) — covered by checksums + attestation only; documented in README.
- **Bootstrap guards** (`app.BootstrapEnabled`): skip when any holds —
  `DANBOORU_MCP_NO_BOOTSTRAP` non-empty, build-injected `Bootstrap=off`
  (`make build` dev artifacts), or the exe is under the system Temp dir (`go
  run` temp binary). Prevents ephemeral paths from being written into user PATH.
- Credentials come from `DANBOORU_LOGIN` / `DANBOORU_API_KEY` env vars; when
  unset the client is anonymous (rate and content limits apply).
- **CI** (`.github/workflows/ci.yml`): unit tests run on every push/PR; the
  online smoke (`make integration` / `scripts/integration-test.sh` — every
  tool called against the live API plus a tagger-era name whose `wd14`
  verdict must ride the response, anonymous, PATH-isolated via
  `DANBOORU_MCP_NO_BOOTSTRAP` and cache-disabled via
  `DANBOORU_MCP_NO_CACHE` so it always verifies the live upstream, never a
  warm cache) runs only on the weekly schedule or manual
  dispatch, never in the release path. It is the canary for upstream API
  drift (the 2024 related_tag.json revamp broke parsing while unit tests
  stayed green). The repo pins LF line endings via `.gitattributes`
  (shell scripts break under Git Bash / CI windows runners when autocrlf
  converts them to CRLF).
- Release: push a `v*` tag and `.github/workflows/release.yml` runs tests,
  builds each arch exe once, packages amd64+arm64 NSIS setup exes via
  `make installer-only` and deterministic portable zips via `make zip`,
  generates `checksums.txt` (zips + setup exes), attaches build-provenance
  attestations, and publishes the GitHub Release (with `install.ps1` as an
  asset); the `verify-repro` job then proves the published exe matches a
  from-source rebuild. Release notes come
  from a `## [x.y.z]` section in `CHANGELOG.md` when present, otherwise
  auto-generated. `make` targets accept `VERSION=` and `GOARCH=` overrides,
  which is how the workflow pins them. Bump the `version` in `server.json`
  (MCP registry metadata) to match the tag before pushing a release tag.
- Install: two routes kept in sync — `install.ps1` (PowerShell 5.1+) and the
  NSIS installer `installer/danbooru-tag-mcp.nsi` (`make installer`; CI
  installs NSIS via `choco install nsis` because choco does not refresh the
  job PATH, the workflow adds `C:\Program Files (x86)\NSIS` explicitly).
  Both install per-user to `%LOCALAPPDATA%\Programs\danbooru-tag-mcp` (no
  UAC) and share the install-dir registry value
  `HKCU\Software\danbooru-tag-mcp\InstallDir`, so they reuse each other's
  directory. PATH registration is delegated to the exe itself: the installer
  runs `danbooru-tag-mcp.exe version` once (nsExec, no console flash) to
  trigger the startup bootstrap. The NSIS stub is x86 and runs under
  emulation on ARM64 while dropping the native arch exe. The uninstaller
  removes files, Start Menu shortcut, and both registry keys — it does NOT
  touch the user PATH (same behavior as jvm).
  **Keep script strings ASCII-only** — under `iwr | iex` the install.ps1 body
  is decoded with the host's default code page; a UTF-8 BOM breaks parsing on
  PowerShell 5.1.
- **Tool descriptions are context cost**: every MCP client injects the
  tools/list payload into each session, so keep descriptions in
  `internal/tools` terse English — one clause per behavioral rule (defaults,
  limits, output shape), no rationale prose, no usage coaching. Deeper
  explanation belongs in README.
- Test style: api layer uses `httptest.Server`; service layer uses mock
  structs implementing `TagFetcher`; api tests use `WithReqGap(0)` to disable
  real throttling; pure logic is extracted into private functions taking
  explicit params for table-driven tests; registry operations are only tested
  with `DANBOORU_MCP_TEST_REGISTRY=1`. Test naming `TestXxx_Behavior`.
- Go 1.26, single third-party dependency `github.com/mark3labs/mcp-go`. The
  exact patch toolchain is pinned in `go.mod` (`toolchain` directive) for
  reproducible builds — bump it deliberately and together with CI. The
  repo root has a `go.work` (`use .`).
- Dev environment is Windows + Git Bash (Git Bash path-converts args like
  `/v`, so use `MSYS_NO_PATHCONV=1` when calling `reg` and similar tools).
<!-- TRELLIS:START -->
# Trellis Instructions

These instructions are for AI assistants working in this project.

This project is managed by Trellis. The working knowledge you need lives under `.trellis/`:

- `.trellis/workflow.md` — development phases, when to create tasks, skill routing
- `.trellis/spec/` — package- and layer-scoped coding guidelines (read before writing code in a given layer)
- `.trellis/workspace/` — per-developer journals and session traces
- `.trellis/tasks/` — active and archived tasks (PRDs, research, jsonl context)

If a Trellis command is available on your platform (e.g. `/trellis:finish-work`, `/trellis:continue`), prefer it over manual steps. Not every platform exposes every command.

If you're using Codex or another agent-capable tool, additional project-scoped helpers may live in:
- `.agents/skills/` — reusable Trellis skills
- `.codex/agents/` — optional custom subagents

Managed by Trellis. Edits outside this block are preserved; edits inside may be overwritten by a future `trellis update`.

<!-- TRELLIS:END -->
