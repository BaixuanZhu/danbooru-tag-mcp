# Keep credentials out of transport error text

## Goal

A network failure must never deliver the Danbooru `login` / `api_key` to the
MCP client. Today the text of a transport error embeds the full request URL,
including both credentials, and every tool handler forwards `err.Error()`
verbatim in its error payload. Ship as a patch release (0.6.1).

User value: the maintainer runs the server with credentials, and tool
results land in agent context, session stores and model-provider logs. A
timeout or refused connection currently writes the account key into all of
them.

## Background (confirmed facts)

- `internal/api/client.go:226-234` injects `login` and `api_key` into the
  query when both are configured; `client.go:236-239` builds `reqURL` from
  that query.
- `client.go:377-380`: a failed `c.httpClient.Do(req)` is returned raw. Go
  reports it as a `*url.Error` reading `Get "<full URL>": <cause>`, and the
  URL carries the query string. Reproduced with fake values:
  `Get "http://127.0.0.1:9/tags.json?api_key=FAKE_KEY_0123456789&login=FAKE_LOGIN": dial tcp ...`.
- The text then flows unchanged: `doWithRetry` wraps it in `transientError`
  whose `Error()` delegates to the inner text (`client.go:323`), `Get`
  returns it and logs it at WARN (`client.go:256`), and the tool handlers
  print it through `errResp(code, err.Error())` (`internal/tools/register.go`,
  e.g. lines 304, 308, 327).
- Only transport-level failures carry the URL: timeout, DNS failure,
  connection refused, caller cancellation. Non-2xx responses are summarized
  from status and body and never include the URL.
- Real-world evidence (counted, values never printed): the local ZCode
  session store holds four tool outputs — three sessions, 2026-09-28 to
  2026-10-23, produced by pre-0.6.0 builds — whose error text embeds the
  maintainer's real API key. Six session rows across four sessions quote it.
- The 0.6.0 code path is unchanged: driving the installed 0.6.0 exe with a
  cancelled request still prints the full URL
  (`Get "https://danbooru.donmai.us/tags.json?limit=1&search%5Bname%5D=blue_hair": context canceled`).
- The on-disk response cache never stored the URL (keys exclude credentials,
  bodies are upstream payloads), so no cache entry needs cleaning.

## Requirements

- R1: no error returned by `Client.Get` may contain the `login` or
  `api_key` value, nor any query string of the request URL, for any
  transport-level failure (refused, timeout, DNS, cancellation).
- R2: the error stays useful — it keeps the operation, the scheme, host and
  path of the request, and the underlying cause.
- R3: error semantics are unchanged: `errors.Is(err, context.Canceled)` and
  `errors.Is(err, context.DeadlineExceeded)` keep working; retry behavior,
  backoff, `Retry-After` handling, error shrink and the rate-limit gap are
  untouched.
- R4: the fix sits at the single point where transport errors are created
  (`doOnce`), so no downstream layer can reintroduce the URL.
- R5: a regression test pins R1-R3 and is shown to fail on the pre-fix code.
- R6: docs follow — CHANGELOG `## [0.6.1] - Unreleased`, one sentence in the
  AGENTS.md reliability paragraph, and a rule in
  `.trellis/spec/backend/error-handling.md`.

## Acceptance Criteria

- [ ] New test: with credentials configured, a request cancelled in flight
      returns an error whose text contains neither the login nor the api key,
      still satisfies `errors.Is(err, context.DeadlineExceeded)`, and still
      names the request path.
- [ ] The same test is run once before the fix and fails there (red first),
      and passes after it.
- [ ] Existing retry, cancellation and error-shrink tests stay green:
      `go vet ./...`, `make test`, `make test-race`.
- [ ] CHANGELOG, AGENTS.md and `.trellis/spec/backend/error-handling.md`
      describe the new rule.

## Technical Notes

- Approach: `doOnce` passes every returned error through
  `sanitizeTransportError`. When `errors.As` finds a `*url.Error`, it returns
  a new `*url.Error` with the same `Op` and `Err` but a URL reduced to
  scheme + host + path (query, userinfo and force-query flag removed).
  Anything else passes through untouched.
- Alternatives considered and rejected are recorded in
  `research/leak-paths.md`: literal-value redaction, and moving credentials
  to an `Authorization` header.

## Out of Scope

- Rotating the exposed key (maintainer action; recommended right after
  0.6.1 ships).
- Changing how credentials are sent (query parameters today).
- The GitHub client in `internal/upgrade` (it sends no credentials).
