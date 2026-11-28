# Implementation plan — lift ranking for get_related_tags

Depends on the count-mode task, which lands first and adds
`Client.FetchPostCount` + `TagFetcher.FetchPostCount` (reused here with an
empty `tags` for the whole-site count). Do not re-add them.

## Ordered checklist

1. **Baseline** — `go build ./... && go vet ./... && go test ./...` green.
2. **api** — `FetchRelated` sends `limit=500`; update its test. No other
   api change.
3. **service: response parsing** — `danbooruRelatedResponse` gains the
   top-level `post_count`.
4. **service: memo** — `TagService` gets the whole-site count fields and
   `sitePostCount(ctx) (int, bool)` (mutex held across the fetch, success
   memoized only; unparsable / non-positive body = failure).
5. **service: ranking** — `RelatedTag.Lift *float64` (omitempty); a pure
   helper (explicit params, table-testable) takes the candidates, the query
   count A and N and returns the ranked slice; `Related` wires fetch →
   candidates → memo → rank → limit.
6. **Tests first where the behavior is new** (write, see them fail against
   the old code where possible, then implement):
   - `TestRelated_LiftMathAndRanking` — hand-built entries with known
     frequency / post_count and a mocked N; assert each `lift` (rounded),
     the order, that `limit` is applied AFTER ranking, and that the query
     tag and category 5 are still excluded.
   - `TestRelated_SiteCountFetchedOnce` — two calls, `countCalls == 1`.
   - `TestRelated_SiteCountFailureDegradesAndRetries` — failing count →
     upstream order and NO `lift` key in the marshaled JSON; the next call
     requests N again.
   - `TestRelated_EmptyResultSkipsSiteCount` — not-found shape → no N
     request, `{"related":[]}`.
   - `TestRelated_SupportFloorSinksLowOverlapEntries` — below-floor entries
     keep relative upstream order at the end and still carry `lift`.
   - `TestRelated_ZeroPostCountKeepsUpstreamOrder` — missing top-level
     count → lift attached, no re-ranking.
   - Existing `TestRelated_*` tests stay green (self + meta exclusion,
     limit); extend them rather than rewrite.
7. **tools** — description gains the lift clause only; schema pins updated.
8. **Docs, same change** — CHANGELOG `[Unreleased]` / Changed; README
   `get_related_tags` table row and parameter section; AGENTS.md boundary
   paragraph ("Related drops …" gains: lift ranking, pool 500, support floor,
   memo, degrade); integration call + assertions.
9. **Verify** — `gofmt -l .`, `go vet ./...`, `go test ./...`,
   `go test -race ./...` (the memo is the reason the race run matters),
   `make build` + `bash scripts/integration-test.sh`.

## Integration assertions (live)

- `get_related_tags {"tag":"arm_up","limit":10}`: every entry has a numeric
  `lift`; the list is sorted by lift descending; the order equals sorting
  the entries by `frequency / post_count` descending (N-independent check of
  the formula); no category-5 entry; no `highres`.
- The existing "upstream drift canary" check keeps its shape checks.

## Validation commands

- `go test ./internal/service/... -run 'Related' -v`
- `make test`, `make test-race`, `bash scripts/integration-test.sh`

## Review gates

- After step 6: the ranking tests pass without touching `FetchRelated`'s
  mock response shape beyond adding `post_count`.
- After step 9: race detector clean on the memo (run `Related` concurrently
  in one test).

## Rollback points

- Steps 2–5 are one coherent change; revert the commit to roll back.
