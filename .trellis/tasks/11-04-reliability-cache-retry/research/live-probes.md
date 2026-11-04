# Live probes: Danbooru outage evidence & comma-list questions

Date: 2026-11-04/05 (planning session for 11-04-reliability-cache-retry)

## Outage observed first-hand

Every Danbooru endpoint probed this session returned **HTTP 503 with an HTML
maintenance page** (`<title>Downbooru</title>`), body size 1864 bytes. The
consuming-agent feedback measured 2853 bytes for the same class of page —
size varies by page variant; either way `internal/api/client.go:131` inlines
the full HTML into the error string today. This is the concrete failure mode
R1 (stale fallback) and R2 (error shrink) address.

## Deferred probes (blocked by the outage — retry when Danbooru is back)

1. `tag_aliases.json`: does `search[antecedent_name_any]` (or any `_any` /
   `_normalize` variant) accept a comma-separated list?
   Known: `search[antecedent_name]` comma lists return empty (documented in
   AGENTS.md; re-confirmation attempt blocked by the 503).
   Impact: if it works, `get_tags_info` can resolve `renamed_to` for the
   whole batch in ONE extra request and R3's per-name alias loop collapses.
2. `wiki_pages.json`: does `search[title_normalize]` accept comma lists?
   Impact: if yes, wiki batch lookup (currently out of scope, P2) becomes a
   trivial follow-up feature.

Neither blocks this task: R3 ships with per-name alias resolution (bounded
by `maxBatchNames`, amortized by the 7d alias cache) and simplifies later if
probe 1 lands.

## Probe method gotcha (Git Bash + curl)

`curl` treats `[...]` in URLs as range globbing. Symptom: `curl: (3) bad
range in position N` — and with `-s`, silently empty output. Fix: pass `-g`
(disable globbing) or percent-encode the brackets.

Working probe shape:

```bash
curl -sg -A "danbooru-tag-mcp/<ver> (planning probe)" \
  "https://danbooru.donmai.us/tag_aliases.json?search[antecedent_name_any]=a,b&limit=10&only=antecedent_name,consequent_name,status"
```

The `only=` param keeps probe payloads small; keep ~2s between calls.
