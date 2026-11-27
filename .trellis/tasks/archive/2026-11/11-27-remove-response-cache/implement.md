# Implementation plan

Ordered checklist. Lands after task `11-27-sanitize-error-credentials` is
committed, because both edit `internal/api/client.go`.

## 0. Pre-flight

- [ ] `python ./.trellis/scripts/task.py current --source` points at this
      task.
- [ ] The P0 commit is on the branch:
      `git log --oneline -5`, and
      `grep -n sanitizeTransportError internal/api/client.go` finds it.
- [ ] Baseline is green: `go vet ./...`, `make test`.

## 1. api layer — `internal/api/client.go`

- [ ] Remove the cache pieces listed in `design.md`; `Get`, `doWithRetry`
      and the seven `Fetch*` methods return `([]byte, error)`.
- [ ] Add `ErrUpstreamUnavailable` to `internal/app` (with its doc comment).
- [ ] Rename `transientError` to `upstreamError`, give it the `Is` method,
      and have `doWithRetry` wrap the last error when retries are exhausted.
- [ ] Rewrite the `Get` and `doWithRetry` doc comments for the new contract.
- [ ] `client_test.go`: delete the helper, the import and the 13 cache
      tests; fix `res.Body` call sites; add
      `TestGet_NonRetryable4xxReturnsAfterOneAttempt`,
      `TestGet_UpstreamFailuresAreMarked` and
      `TestChain_UpstreamFailureReachesTheClient`.

Steps 1-4 form one unit: the tree compiles per package again only after
step 4. `go build ./internal/api/` is the step-1 checkpoint.

## 2. delete the package

- [ ] `git rm -r internal/cache`.

## 3. service layer

- [ ] `TagFetcher` signatures, `CacheMarks` / `marksFrom`, embedded fields,
      `profile.go` lines 164 and 210, imports.
- [ ] `tag_test.go`: mock returns `[]byte`; delete
      `TestCacheableResponses_StaleMarksMarshaled`.
      `profile_test.go`: delete `TestProfile_NeverCarriesCacheMarks`.
- [ ] Checkpoint: `go test ./internal/service/`.

## 4. tools layer

- [ ] `register.go`: remove the six `refresh` params and clauses,
      `withRefresh` and the import.
- [ ] `register_test.go`: drop the six `refresh` rows; delete
      `TestToolSchemas_NoRefreshOnUncachedTools`.
- [ ] `register.go`: add `failResult`, route the eight handlers through it,
      and give `get_tag_info` the code `tag_info_failed` for its other
      errors.
- [ ] Add `TestHandlers_UpstreamFailureIsUniform` with a mock `TagService`.
- [ ] Checkpoint: `go test ./internal/tools/`.

## 5. entry point and scripts

- [ ] `main.go`: `clientOptions`, the usage line, imports.
- [ ] `scripts/integration-test.sh`: env var and wording.
- [ ] `installer/danbooru-tag-mcp.nsi`: comment only (lines 128-131 stay).
- [ ] `install.ps1`: hint text only (line 229).

## 6. docs

- [ ] `AGENTS.md` — five places listed in `design.md`, plus the
      `internal/app/` bullet.
- [ ] `.trellis/spec/backend`: rewrite `cache-and-resilience.md` as
      `resilience.md` (including the uniform upstream result), update the
      index row, adjust the `error-handling.md` lesson.
- [ ] `README.md`, `CHANGELOG.md` (breaking notes for both the removal and
      the error codes), `server.json` description.

## 7. Validation

```bash
gofmt -l internal/ main.go
go vet ./...
make test
make test-race
git grep -n -E 'internal/cache|CacheMarks|marksFrom|withRefresh|tagsExactClass|defaultCacheTTLs|ttlClass|WithCache|DANBOORU_MCP_NO_CACHE|transientError'
```

The grep must only hit `CHANGELOG.md` (the 0.6.0 history entry) and files
under `.trellis/tasks/`. Second guard:

```bash
git grep -n 'tag_not_found' internal/tools/register.go
```

It must show only the two not-found branches (`get_tag_info`,
`get_tag_profile`), never an upstream or generic failure path.

- [ ] `make build`, then the stdio smoke test from AGENTS.md. Confirm
      `tools/list` has no `refresh` property and no description mentions
      cached data.
- [ ] Fresh-run check: with `LOCALAPPDATA` pointing at a temp directory and
      `DANBOORU_MCP_NO_BOOTSTRAP=1`, call one tool and confirm no
      `danbooru-tag-mcp\cache` directory appears.
- [ ] Leftover check: the same, with a file placed under
      `danbooru-tag-mcp\cache\v1\` beforehand; confirm its hash and mtime
      are unchanged after the call.
- [ ] `make integration` (live, anonymous). `order:random` can answer 500
      once in a while; rerun a failed step before treating it as a defect.

## Risky points

- Keep the "c.mu guards only the rate-limit accounting" wording: it is the
  documented concurrency contract.
- Do not change which attempts are retried while repurposing
  `transientError`.
- Keep `sanitizeTransportError` on every error return of `doOnce`.
- `go.mod` / `go.sum` do not change (a stdlib-only package is removed).
- Every manual run of the exe needs `DANBOORU_MCP_NO_BOOTSTRAP=1` (or a
  `make build` dev binary) so the user PATH is never written.

## Rollback points

Commit only once step 7 is green. Before that,
`git restore --staged internal/cache && git checkout -- .` reverts the whole
task.
