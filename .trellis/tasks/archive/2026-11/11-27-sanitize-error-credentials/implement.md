# Implementation plan

Lightweight task: PRD plus this ordered checklist.

## Pre-flight

- [ ] `python ./.trellis/scripts/task.py current --source` points at this
      task.
- [ ] Baseline is green: `go vet ./...`, `make test`.

## Steps

1. Add `TestGet_TransportErrorsOmitCredentials` to
   `internal/api/client_test.go` (design in `research/leak-paths.md`). Run it
   alone before touching production code and keep the failing output:
   `go test ./internal/api -run TestGet_TransportErrorsOmitCredentials -v`.
2. Add `sanitizeTransportError` to `internal/api/client.go` and apply it to
   every error return of `doOnce`.
3. Re-run the new test, then the full suites.
4. Docs:
   - `CHANGELOG.md`: `## [0.6.1] - Unreleased` with a Fixed entry.
   - `AGENTS.md`: one sentence in the retry/error-shrink reliability
     paragraph.
   - `.trellis/spec/backend/error-handling.md`: new "Common Mistake" entry.
   - `.trellis/spec/backend/cache-and-resilience.md`: one row in the
     validation matrix (network error text carries scheme + host + path
     only).
5. `make build`, then the stdio smoke test from AGENTS.md (initialize +
   tools/list) to prove the server still starts.

## Validation commands

```bash
gofmt -l internal/ main.go        # must print nothing
go vet ./...
make test
make test-race
```

## Rollback

A small diff in two Go files plus docs; `git checkout -- <files>` reverts.
Releasing is the maintainer's step: bump `server.json`, push tag `v0.6.1`.
