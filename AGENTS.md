# AGENTS.md

Danbooru tag lookup MCP server (Go). With no arguments it serves 7 tools over
stdio (`search_tags` / `get_tag_info` / `get_related_tags` / `get_tag_alias` /
`get_tag_wiki` / `search_posts` / `get_tag_profile`) to
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
make release                        # portable zip + checksums.txt -> dist/ (for GitHub Releases)
make installer                      # NSIS setup exe -> dist/danbooru-tag-mcp-windows-<arch>-setup.exe
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
  choke point** for all outbound requests (1.5s rate limit + mutex, context
  cancellation, UA/credential injection, 5MB body cap). New API endpoints must
  go through it; never use `http` directly.
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
(`PostTags`, parsed from the API's `tag_string_*` fields — no extra request);
`Related` drops both the query tag itself and meta-category (5) entries,
because meta tags like `highres` co-occur with everything and drown out
content tags. Category ids are 0=general, 1=artist, 3=copyright, 4=character,
5=meta (Danbooru's actual API numbering — verified live, do not "fix" to the
0-4 continuous scheme). Both behaviors are pinned by unit tests and the
integration script.

**Tag distribution profile (get_tag_profile, test-enforced, do not break)**:
answers "what does this big tag actually render as" (arm_up: 309k posts,
mostly bent-arm poses, drowning arm_above_head's 3.2k) by sampling ONE
request — `<tag> order:random`, default/limit 200 posts (the anonymous
per-page cap, verified live) — and aggregating locally in
`internal/service/profile.go`: singles counted per post (query tag and
meta-category dropped), split at freq >= 0.5 into `ubiquitous` (corpus
constants like 1girl/solo — verified live to be the raw top 10 of every big
tag, not a property of the tag) vs freq-ordered `co_tags`; `top_pairs`
combinations ranked by **lift** (joint freq over the product of singles
freqs) because naive pair frequency reproduces the same 1girl+solo baseline
every time — lift surfaces tag-specific poses (arm_up: holding_weapon+sword,
lift ~12). Two boundaries pinned by unit tests: the sample query must stay
`<tag> order:random` with **no rating filter** (`Profile` calls
`fetcher.FetchPosts` directly, NOT `SearchPosts` — a rating:explicit sample
would describe arm_up's 5% explicit subpopulation, pinned by
`TestProfile_UbiquitousSplitAndExclusions`), and the tool is single-tag
only because `order:` counts toward the 2-tag query limit (verified live:
`arm_up 1boy order:random` 422s). Renamed/0-post tags return an empty
profile whose embedded `wd14` verdict tells the story, mirroring
`get_tag_info`'s NotFound handling.

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
rides the `wd14` verdict on that error payload. `search_tags` adds
`wd14_hits` (vocabulary substring matches, best snapshot count first) when no
result carries posts (`hasLiveTag`). The wd14 policy translation (probe the
canonical form via `get_tag_alias`, or describe in prose) stays with the
consuming agent — the server reports facts only.

## Key conventions and gotchas

- **stdout carries JSON-RPC only**: in MCP server mode all logs must go to
  stderr (`main.go` sets `log.SetOutput(os.Stderr)`). The `version` / `help` /
  `upgrade` subcommands are CLI mode and may write stdout; never add stdout
  output on the server-mode path or the stdio transport breaks immediately.
- **Self-update conventions** (shared by `internal/upgrade` and
  `install.ps1`; keep both in sync): asset naming
  `danbooru-tag-mcp-{GOOS}-{GOARCH}.zip` (contains a single
  `danbooru-tag-mcp.exe`); `checksums.txt` in sha256sum format matched by bare
  file name (tolerates binary-mode `*` prefix; `make release` generates it);
  Windows cannot overwrite a running exe, so replacement goes through a `.bak`
  rename, with leftovers cleaned by `CleanupStaleBak` on next startup.
  Publishing a release: push a `v0.2.0`-format tag; the workflow builds and
  uploads the assets automatically (no manual upload).
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
  `DANBOORU_MCP_NO_BOOTSTRAP`) runs only on the weekly schedule or manual
  dispatch, never in the release path. It is the canary for upstream API
  drift (the 2024 related_tag.json revamp broke parsing while unit tests
  stayed green). The repo pins LF line endings via `.gitattributes`
  (shell scripts break under Git Bash / CI windows runners when autocrlf
  converts them to CRLF).
- Release: push a `v*` tag and `.github/workflows/release.yml` runs tests,
  builds amd64+arm64 NSIS setup exes via `make installer` and portable zips
  via `make release`, generates `checksums.txt` (zips only), and publishes
  the GitHub Release (with `install.ps1` as an asset). Release notes come
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
  repo root has a `go.work` (`use .`).
- Dev environment is Windows + Git Bash (Git Bash path-converts args like
  `/v`, so use `MSYS_NO_PATHCONV=1` when calling `reg` and similar tools).
