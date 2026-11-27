# Leak paths for credentials in error text

Date: 2026-11-27. All findings verified against the code at `main`
(acd06f9) and by running the installed 0.6.0 exe anonymously. No real
credential value appears in this file.

## Where the credentials live

`Client.Get` (`internal/api/client.go:226-234`) copies the caller's params
and sets `login` and `api_key` when both are configured, then builds
`reqURL = baseURL + path + "?" + params.Encode()` (`client.go:236-239`).
The credentials are therefore in the request URL's query string.

## How they reach the client

1. `doOnce` (`client.go:377-380`) returns the raw error from
   `c.httpClient.Do(req)`. `net/http` reports transport failures as
   `*url.Error{Op, URL, Err}` whose `Error()` is `Op "URL": Err`. Go only
   strips a password embedded in the URL's userinfo (`stripPassword`); the
   query string is kept.
2. `doWithRetry` wraps the error in `transientError` (`client.go:342`),
   whose `Error()` is the inner text (`client.go:323`).
3. `Get` logs it at WARN when a stale entry is served (`client.go:256`) and
   returns it.
4. Every tool handler returns `errResp(code, err.Error())`
   (`internal/tools/register.go`, e.g. lines 304, 308, 327). The text becomes
   the MCP tool result.

## Reproduction

- Standalone stdlib program with a closed port and fake values:

  ```
  Get "http://127.0.0.1:9/tags.json?api_key=FAKE_KEY_0123456789&login=FAKE_LOGIN": dial tcp 127.0.0.1:9: connectex: No connection could be made because the target machine actively refused it.
  ```

- Installed 0.6.0 exe, anonymous, request cancelled by closing stdin:

  ```
  {"error":"tag_not_found","message":"Get \"https://danbooru.donmai.us/tags.json?limit=1\u0026search%5Bname%5D=blue_hair\": context canceled"}
  ```

  With credentials configured the same message carries `api_key=...` and
  `login=...`.

## Observed exposure

- ZCode session store (`part` table): 4 tool outputs with the real key in
  the error text (`get_tag_info` x3, `get_tag_alias` x1; 3 sessions;
  2026-09-28, 2026-10-08, 2026-10-23). 6 rows in total across 4 sessions
  quote such a URL (the other 2 are text parts repeating it).
- ZCode daily logs and rollout files: 0 occurrences.
- Response cache directory: no occurrence (keys exclude credentials, bodies
  are upstream payloads).

## Paths checked and found clean

- Non-2xx handling: `httpStatusError` / `summarizeErrorBody` use status and
  body only. HTML bodies collapse to the `<title>`, other bodies truncate at
  300 bytes; neither Danbooru nor Cloudflare error pages echo the request
  URL in the title. A non-HTML proxy page that echoes the full URL within
  its first 300 bytes is theoretically possible and is listed as a residual
  risk, not fixed here.
- `io.ReadAll` errors carry no URL.
- `internal/cache` errors mention file paths only.
- `internal/upgrade` requests carry no credentials (no token support).

## Alternatives considered

| Option | Verdict | Reason |
|---|---|---|
| Reduce the `*url.Error` URL at the source (`doOnce`) | Chosen | Fixes the only real path, keeps the message useful, one small function |
| Redact literal credential values from every error string | Rejected | Fragile for short logins (a login like `ai` would mangle every message), needs URL-encoded variants, and treats the symptom |
| Pattern redaction of `login=` / `api_key=` in error strings | Rejected | Same as above, plus it would also strip upstream text that legitimately mentions those parameter names |
| Send credentials in an `Authorization` header | Deferred | Removes the secret from URLs entirely, but changes the auth mechanism and needs a live check with real credentials; not a patch |

## Test design

Model on `TestGet_ContextCancelAbortsWithoutRetry`
(`internal/api/client_test.go:265-287`): an `httptest` handler holds the
request until the client gives up, the caller uses a 50 ms context timeout.
With `WithCredentials("probe_login", "probe_api_key_0123456789")` the
resulting `*url.Error` is the real production shape. Assert: error text
contains neither value, still satisfies
`errors.Is(err, context.DeadlineExceeded)`, and still contains the path.
On the pre-fix code the test must fail on the api key assertion.
