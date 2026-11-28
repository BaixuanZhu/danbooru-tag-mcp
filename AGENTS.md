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
  shrink, and the upstream-failure marker — see the reliability boundary
  below). New API endpoints must go through it; never use `http` directly.
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
- **internal/app/**: `Version` (ldflags injected), `Fail`, `UserAgent`,
  `ErrUpstreamUnavailable` (the upstream-failure sentinel `errors.Is`
  matches; api raises it, tools turn it into the uniform result), and the
  bootstrap switch (`BootstrapEnabled`).
- **internal/env/**: user PATH injection (registry `HKCU\Environment` +
  WM_SETTINGCHANGE broadcast; never setx).
- **internal/upgrade/**: GitHub Release self-update (version compare ->
  download zip -> SHA256 verify via checksums.txt -> atomic .bak replace).

**Subcommand routing** (main.go): no args or `serve` = MCP server mode (keeps
zero-config client compatibility); `upgrade` / `version` / `help` are CLI
mode, where writing to stdout is allowed.

**Retry + error shrink + upstream marking (reliability boundary,
test-enforced, do not break)**: every tool answers from live Danbooru — the
response cache (0.6.0) was removed in 0.7.0, so there is no stale-serve
fallback and no `refresh`/`stale`/`cached_at` anywhere. The one in-process
exception is the whole-site post count that `get_related_tags` memoizes as
the divisor of lift (see Lift ranking below): a number, never a response.
`Client.Get` is a
straight line: rate-limit wait, credential injection, retry loop.
Concurrency contract: `c.mu` guards only the rate-limit accounting while
sleeps and retries run unlocked. Retries: up to 2 retries on 5xx, network
errors, and 429/421 with 1s/3s backoff (ctx-interruptible waits; caller
cancellation never retries; a parseable `Retry-After`, capped 30s, replaces
the first backoff). A definitive 4xx returns after ONE attempt — pinned by
`TestGet_NonRetryable4xxReturnsAfterOneAttempt`. Non-2xx error bodies are
shrunk: HTML collapses to status + `<title>` (live Downbooru 503 shape:
`danbooru 503 (Downbooru)` instead of ~2-3KB of markup), other bodies
truncate at 300 bytes. Transport errors are sanitized at creation
(`sanitizeTransportError` in `doOnce`): the `*url.Error` URL keeps only
scheme + host + path because the query string carries the injected
`login`/`api_key` and the text flows verbatim into MCP tool error payloads
(`TestGet_TransportErrorsOmitCredentials`; cause chain and cancellation
semantics preserved). Request gap tiers: `DANBOORU_REQ_GAP_MS` (0 valid =
no gap; invalid or negative warns once on stderr and keeps the tier default)
> credentialed 500ms (`DANBOORU_LOGIN` + `DANBOORU_API_KEY` both set) >
anonymous 1500ms; `WithReqGap` still overrides everything (tests use it).
**Upstream marker contract (R10-R12)**: when a request still fails after
retries on one of the three transient classes (transport error, 5xx,
429/421), `doWithRetry` wraps the shrunken cause in `*upstreamError`, whose
`Is` matches `app.ErrUpstreamUnavailable`. The tools layer's `failResult`
turns that into ONE uniform marked result on every tool:
`isError: true` + `{"error":"upstream_unavailable","message":"Danbooru is
not answering right now: <cause>. All tools of this server are affected;
retry later."}` (`TestHandlers_UpstreamFailureIsUniform` pins all 8 tools;
`TestChain_UpstreamFailureReachesTheClient` runs the real api -> service ->
tools chain against a local 503). Only those three classes are marked: a
definitive 4xx, a validation error, a parse error and a caller cancellation
stay unmarked plain text results. `tag_not_found` is reserved for a genuine
not-found (payload with the `wd14` verdict unchanged); other `get_tag_info`
failures get `tag_info_failed`.

**Rating filter boundary (test-enforced, do not break)**: **no layer injects
a rating filter** — neither the api layer's `FetchPosts`
(`TestFetchPosts_DoesNotAddRatingFilter`) nor the service layer's
`SearchPosts` (`TestSearchPosts_NoRatingInjection` asserts the query goes
out verbatim). Rating choice belongs to the caller: a `rating:g/s/q/e`
metatag narrows a query and is exempt from Danbooru's term count (`rating:`
is in `PostQuery::UNLIMITED_METATAGS`), so opting in costs nothing toward
the term budget. History: v0.1.0–0.2.x defaulted
`SearchPosts` to `rating:explicit` as an R-18-allowed product statement;
dropped in 0.3.0 because a default filter biases research queries toward a
~5% subpopulation (arm_up is 95% g/s/q — verified live) and contradicted
`get_tag_profile`'s whole-population sample; "uncensored, unfiltered"
expresses the product stance without picking a rating for the caller. The
`search_posts` tool description documents the g/s/q/e ladder so MCP clients
know the opt-in syntax. Queries are pre-parsed locally (`checkPostQuery`):
up to `MaxQueryTags = 6` content tags are accepted, and a query beyond the
configured term budget is answered by the multi-tag scan (next paragraph)
instead of failing with a Danbooru 422.

**Multi-tag scan boundary (search_posts beyond the term budget,
test-enforced, do not break)**: the configured limit is Danbooru's own term
budget, not a fixed 2 — anonymous `2` (`anonymousTagLimit`), credentialed
`MaxQueryTags` (`main` passes `WithUpstreamTagLimit` from
`Client.Authenticated()`). `terms()` counts non-metatag tokens plus
counting metatags (keys OUTSIDE `unlimitedMetatagKeyList`, the 25-key
transcription of `PostQuery::UNLIMITED_METATAGS` — `rating:`/`status:`/
`id:` are free; `order:`/`user:`/`pool:` count) and decides direct versus
scan. Within the limit the query goes out verbatim and the answer is
byte-identical to the pre-scan shape; beyond it `planScan` + `scan`
(`internal/service/scan.go`) answer it: one batch `lookupTags` over ALL
content tokens (positives, unresolved colon tokens and exclusions alike —
a colon token with a Danbooru row is a content tag under its canonical
name, without one it passes through and is never a local filter), the
`k = min(2, tagLimit - countingMetatags)` rarest positives go upstream as
one listing (query order breaks rarity ties; metatags and pass-through
tokens ride verbatim), and the remaining positives/negatives are filtered
locally against each post's five `tag_string_*` fields — which reproduces
the query exactly, in Danbooru's order. Pages are `limit=200`, at most 5,
id-deduped; the scan stops at `limit` matches, a short page or the page
cap and reports `complete` (a pointer, so absent means "not split", false
means a prefix or a lower-bound count) plus `scanned` (distinct posts) and
`upstream_query` — split answers only, never on direct ones. A
credentialed direct attempt refused with the tag-limit 422 falls back to
the scan, classified ONLY via `errors.Is(err, app.ErrTagLimitExceeded)`
(the api layer wraps a 422 whose body carries `TagLimitError` in
`*tagLimitError`; the error TEXT is unchanged); any other error is
returned untouched, and a scan page refused the same way narrows ONCE (the
less rare tag drops and becomes a local filter; a second refusal returns
the plain error). A local-filter token whose row shows 0 posts is
alias-resolved once (`resolveRenamedTo`) and filtered under its current
name — posts list tags under the current name and renamed antecedents keep
0-post rows. Tokens a local scan cannot evaluate are refused with a clear
local error when a scan is needed: wildcards `*`, OR `~`, and `random:`
(it caps the listing at N, so a short page would not mean the listing
ended); more than 6 content tags are refused locally before any request.
Count mode (`CountPosts`) shares the routing and runs the same scan
without the `limit` stop; `total` is exact only when `complete` is true,
otherwise a lower bound. Known boundary: pass-through colon tokens also
ride the upstream query and consume one of its terms, so with `k = 1` such
a query can surface Danbooru's raw 422 (with `k = 2` the narrowing absorbs
it) — the error is honest, Danbooru refuses the same query directly.
Pinned by `TestParsePostQuery_*`, `TestScan_*`,
`TestSearchPosts_DirectPathByteIdentical`, `TestSearchPosts_TagLimitFallback`,
`TestCountPosts_OverScan`, `TestSearchPostsHandler_ScanFieldsRideThePayload`
and the real api -> service -> tools chain in
`internal/api/tag_limit_chain_test.go`; integration checks 9, 23 and 27-31.

**Post count mode (search_posts `count_only`, test-enforced, do not break)**:
`count_only: true` switches `search_posts` from listing to counting: one
`GET /counts/posts.json?tags=<tags>` through `Client.Get`
(`FetchPostCount`; an empty `tags` omits the parameter and counts the whole
site, which the related-tags lift reuses), answered as
`{"query":<tags as sent, trimmed>,"total":<n>}` and always live. It runs the
SAME local tag-count validation as `SearchPosts` (both call
`checkPostQuery`, so the error text, space-split hint included, cannot
drift) while `limit` and `tags_mode` are neither read nor validated; metatags
are not rejected (verified live: the four `rating:` slices of `arm_up` sum
exactly to its unfiltered total) and no layer injects a rating filter. Three
outcomes stay distinct: an integer is the answer; an explicit
`{"counts":{"posts":null}}` — Danbooru's count timed out, a definitive
answer from a reachable upstream — is `service.ErrPostCountTimedOut`,
delivered unmarked as `post_search_failed` ("danbooru could not count this
query (count timed out); simplify the query or retry later"), never `0` and
never `upstream_unavailable`; a missing `posts` key or a non-integer is
upstream shape drift reported as a parse error, so drift is never dressed up
as a retryable timeout. A transport/5xx/429 failure of the counts request is
the uniform `upstream_unavailable` result; a 422 is the shrunken unmarked
error after one attempt. Pinned by `TestFetchPostCount_*`,
`TestChain_CountModeReachesTheClient`, `TestCountPosts_*` and
`TestSearchPostsHandler_CountModePayloadBytes` (plus its `_CountOnly…`
siblings); integration check 25 compares the count with the tag row's
`post_count` within 10.

**Result shaping (inspiration discovery, the product's weak spot turned
strength)**: `SearchPosts` returns each post's full tag list split by category
(`PostTags`, parsed from the API's `tag_string_*` fields — no extra request)
plus `score`/`fav_count`/`large_file_url`, and caps `limit` at 20
pre-flight (a post averages ~44 tags — verified live — so 20 bounds the
payload at ~20 KB). `SearchPosts` also takes a tags mode: `full` (default,
byte-identical to the old shape) / `none` (per-post `PostTags` omitted;
id/rating/score/fav/url remain) / `diff` (each post's lists minus the
query's own content tags, metatags excluded from the subtraction).
`SearchPosts` returns a `PostsResult`, and a space inside one tag name is
the multi-tag syntax: `blue hair` asks for two near-empty tokens and used
to answer a bare `[]`. When no posts come back and two adjacent plain
tokens join into a WD14 vocabulary name, `possible_tags` names the
candidates (local `tagPairCandidates`, query order, deduped, max 3;
omitted otherwise, so every other payload stays byte-identical), and the
local too-many-tags error gains one `; '<pair>' may be one tag: <name>`
segment per candidate. Facts only — no retry, no query rewrite, no extra
request — and a token the exact predicate `isDanbooruMetatag` classifies
as a metatag is never joined, while colon-bearing tag names now are
(`kafka_(honkai: star_rail)` names `kafka_(honkai:_star_rail)`; the old
any-colon `isMetatagToken` is gone — the posts path classifies tokens with
the key lists, see the scan boundary paragraph)
(`TestSearchPostsHandler_PayloadBytesUnchanged`,
`TestSearchPosts_SpaceSplit*`).
`Related` drops both the query tag itself and
meta-category (5) entries, because meta tags like `highres` co-occur with
everything and drown out content tags; what remains is ranked by lift (next
paragraph). Category ids are 0=general, 1=artist,
3=copyright, 4=character, 5=meta (Danbooru's actual API numbering — verified
live, do not "fix" to the 0-4 continuous scheme). `Search` takes a numeric
category id (-1 = no filter); the API rejects category names (verified live)
so the tools layer maps via `service.CategoryID` before any request. `Search`
also takes `minPosts` (local post-count filter applied after the fetch,
default 0 keeps all and may return fewer than `limit`; a filtered-to-empty
result still counts as no-live-result for the wd14 fallback). These
behaviors are pinned by unit tests and the integration script.

**Lift ranking (get_related_tags, test-enforced, do not break)**: after the
exclusions, `Related` ranks the candidates by `lift = frequency x N /
post_count` (two decimals; N = whole-site post count, A = the related
response's top-level `post_count`, the query tag's own count). Danbooru's
order is plain frequency, which puts corpus constants such as `1girl` and
`solo` on top of every big tag (live: arm_up's tag-specific companions sit
at frequency ranks 13-90). The candidate pool is `relatedPoolSize = 500`
entries (`FetchRelated` sends `limit=500`; the endpoint's default page of
100 would cap the ranking's recall). Support floor: `support = frequency x
min(A, 5000)`; entries with support >= 5 (`relatedSupportFloor`, the number
`profileMinPairCount` also uses) are ranked by lift with a STABLE sort, so
upstream frequency order breaks ties; the rest follow in upstream order,
sunk and never dropped. `limit` is applied AFTER the ranking. `Lift
*float64` (`omitempty`) is attached to every candidate whenever N is
available, sunk ones included; an entry with `post_count == 0` or
`frequency == 0` gets none, and a missing or zero A makes every support 0
(upstream order, `lift` attached). N is memoized per process on `TagService`
(`sitePostCount`: the mutex is held across the one request, so concurrent
tool calls share it; success only — a request error of any class, an
unparsable body or an explicit null count is requested again next call).
While N is unavailable the answer is exactly the pre-lift payload: upstream
order, no `lift`. No candidates means no N request and `{"related":[]}`
(it used to be `null`). No `order` parameter and no category filter: both
were rejected as context cost. Pinned by `TestRelated_LiftMathAndRanking`,
`TestRelatedLift_FormulaRoundingAndGuards` (two decimals; the nil guards),
`_SiteCountFetchedOnce`, `_SiteCountFailureDegradesAndRetries`,
`_EmptyResultSkipsSiteCount`, `_SupportFloorSinksLowOverlapEntries`,
`_SupportIsCappedAtTheSampleSize`, `_ZeroPostCountKeepsUpstreamOrder`,
`_UncomputableLiftIsOmitted`, `_PayloadBytesWithoutSiteCount`,
`_NonPositiveLimitKeepsAll`, `_ConcurrentCallsFetchSiteCountOnce` and
`_EqualLiftsKeepUpstreamOrderAfterSwaps` (a stable sort can only be pinned
on an input that an unstable sort scrambles: an all-equal block is already
sorted, so `sort.Slice` leaves it untouched); the real api -> service ->
tools chain in `TestChain_RelatedLiftReachesTheClient`; integration check
26.

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
Name validation (`tag` and `with` alike): one exact tag name is a single
whitespace-separated token that does not begin with a reserved prefix
(`isSingleTagName` in `internal/service/metatag.go`: one optional leading
`-`/`~` is skipped, the key lookup is case-insensitive). A colon inside a
name is NOT metatag syntax — `firefly_(honkai:_star_rail)` and
`honkai:_star_rail` are real tags, which the old any-colon guard wrongly
rejected. The reserved prefixes mirror both halves of Danbooru's own
`TagNameValidator`: the post-search keys (`PostQueryBuilder::METATAGS`, 101
keys; this list is also the exact line between a search metatag and a plain
token, so reuse `isDanbooruMetatag` when classifying query tokens) and the
edit-only keys (`PostEdit::METATAGS`, 13 beyond the first list:
`artist:`/`character:`-style category prefixes, `newpool:`,
`newfavgroup:`). No tag with posts begins with any of them (verified live
2026-11-28). The guard is a clear-error path only: sample queries are always
built from resolved row names, never from caller text
(`TestProfile_MetatagRejectionText` pins both messages,
`TestProfile_AcceptsColonNames_*` the accepted names,
`TestMetatagKeyLists_MatchUpstreamSnapshot` the list sizes).

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
`maxBatchNames` (<=20 extra requests worst case), and since the response
cache was removed (0.7.0) those alias requests are paid on every call.
`search[antecedent_name]` comma lists
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
- **Local credentials file**: `.env` at the repo root may hold
  `DANBOORU_LOGIN` / `DANBOORU_API_KEY` for manual credentialed tests
  (gitignored through `.env` / `.env.*`). It must never be committed, echoed
  or printed, and nothing loads it automatically: source it explicitly
  (`set -a; . ./.env; set +a`), redact both values from any output, and keep
  test suites anonymous — the integration script unsets both variables so a
  credentialed shell cannot turn the online smoke into a logged-in run.
- **CI** (`.github/workflows/ci.yml`): unit tests run on every push/PR; the
  online smoke (`make integration` / `scripts/integration-test.sh` — every
  tool called against the live API plus a tagger-era name whose `wd14`
  verdict must ride the response, anonymous, PATH-isolated via
  `DANBOORU_MCP_NO_BOOTSTRAP` so it never writes the user PATH or registry;
  every call reaching the live upstream is structural since the 0.7.0 cache
  removal, and the script also pins that no tool exposes `refresh` or
  carries `stale`/`cached_at` markers) runs only on the weekly schedule or
  manual dispatch, never in the release path. It is the canary for upstream
  API drift (the 2024 related_tag.json revamp broke parsing while unit tests
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
