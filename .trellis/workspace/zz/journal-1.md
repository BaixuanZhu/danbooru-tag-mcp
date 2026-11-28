# Journal - zz (Part 1)

> AI development session journal
> Started: 2026-11-03

---



## Session 1: Three-task pipeline: credential sanitization, cache removal, help rewrite
<!-- trellis-session: v=2 fp=fc6f7293a53aa13b -->

**Date**: 2026-11-28
**Task**: Three-task pipeline: credential sanitization, cache removal, help rewrite
**Branch**: `main`

### Summary

Shipped three Trellis tasks to main: 0d684e1 fix (sanitizeTransportError strips query from transport errors, 0.6.1), 5e3b8a1 feat! (internal/cache deleted, uniform upstream_unavailable on all 8 tools, tag_info_failed split, refresh params removed, integration test anti-regression assertions, 0.7.0), 3fcb66e feat (help rewrite: table-driven router+usage, aliases, env vars, exit codes, drift tests). All checks GO; gofmt/vet/test/test-race/integration green; make integration 32 checks live-anonymous pass.

### Git Commits

| Hash | Message |
|------|---------|
| `3fcb66e` | feat: rewrite help output - complete commands, aliases, env vars, exit codes |

### Status

[OK] **Completed**


## Session 2: Five-task pipeline: profile names, space-split miss, count mode, lift ranking, multi-tag scan
<!-- trellis-session: v=2 fp=25c3b93e96eee0ce -->

**Date**: 2026-11-28
**Task**: Five-task pipeline: profile names, space-split miss, count mode, lift ranking, multi-tag scan
**Branch**: `main`

### Summary

Landed the five 11-28 backlog Trellis tasks on main as five work commits plus one archive commit. All tasks went implement (background agents) -> check (independent PASS) -> spec update -> commit; gates (gofmt/vet/test/race) and the live anonymous integration run (44 checks) green at every step; the credentialed path was verified live with the maintainer's test account whose credentials live only in the gitignored .env.

### Main Changes

- get_tag_profile accepts colon-bearing tag names (guard mirrors both halves of Danbooru's TagNameValidator: 101 search keys + 13 edit-only keys)
- search_posts names a space-split miss: possible_tags on an empty answer, the same hint on the too-many-tags error
- search_posts count mode: count_only answers {query,total} from /counts/posts.json with the timeout-vs-drift distinction
- get_related_tags ranks by lift (frequency x N / post_count, stable sort, support floor 5, pool 500, N memoized per process); empty answer is {related:[]} not null
- search_posts accepts up to 6 content tags: beyond the term budget a local scan answers exactly (rarest tags upstream, rest filtered from each post's own tag lists), adding upstream_query/scanned/complete; wildcard/~OR/random: refused locally; credentialed direct-then-fallback on app.ErrTagLimitExceeded; counting metatags shrink the scan; renamed local filters resolve one alias
- only=<light fields> rides every /posts.json request; token classification everywhere uses the exact metatag key lists (old any-colon isMetatagToken removed)

### Git Commits

| Hash | Message |
|------|---------|
| `5394949` | fix: get_tag_profile accepts tag names containing a colon - guard mirrors TagNameValidator |
| `107df30` | feat: search_posts names a space-split miss - possible_tags on an empty answer, same hint on the tag-limit error |
| `b984af4` | feat: search_posts count mode - count_only answers {query,total} from /counts/posts.json |
| `d93ff6a` | feat: get_related_tags ranks by lift - tag-specific companions first, site-wide count memoized per process |
| `85b1c31` | feat: search_posts answers up to 6 content tags - beyond Danbooru's term budget the rest is scanned locally |
| `2ae746d` | chore(task): archive the five 11-28 tasks - profile name validation, space-split miss, count mode, lift ranking, multi-tag scan |

### Testing

- [OK] go test + race green after every task; integration script grew to 44 live checks (scan answers 9/23/27-31, light-field pin 8, count 25, lift 26) all passing against live Danbooru
- [OK] credentialed live check: 3 tags direct, 4/6 tags split, 7 refused, 4-term query -> 422 fallback -> complete scan; mutation batteries (go test -overlay): 46/47 killed (1 equivalent) for the scan, 4 survivors total across lift (2 equivalent, 2 pre-existing)

### Status

[OK] **Completed**

### Next Steps

- Registry republish after maintainer confirmation; rotate the old leaked API key; upgrade the PixAI machine's copy; CHANGELOG stays under ## [Unreleased] until the next version tag
