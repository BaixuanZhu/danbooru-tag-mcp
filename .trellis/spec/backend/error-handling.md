# Error Handling

> How errors are handled in this project.

---

## Overview

<!--
Document your project's error handling conventions here.

Questions to answer:
- What error types do you define?
- How are errors propagated?
- How are errors logged?
- How are errors returned to clients?
-->

(To be filled by the team)

---

## Error Types

<!-- Custom error classes/types -->

(To be filled by the team)

---

## Error Handling Patterns

<!-- Try-catch patterns, error propagation -->

(To be filled by the team)

---

## API Error Responses

<!-- Standard error response format -->

(To be filled by the team)

---

## Common Mistakes

### Common Mistake: fallback paths that fire on definitive errors

**Symptom**: revoked credentials (401/403) or invalid queries (422)
silently return stale cached data instead of surfacing the error.

**Cause**: gating a fallback (stale cache, default value, retry) on
`err != nil` instead of classifying the error first. Only transient
failures (5xx, network, 429) mean "the answer is unavailable"; a 4xx is a
definitive answer from a reachable upstream.

**Fix**: wrap retryable failures in a sentinel type
(`transientError` in `internal/api/client.go`) and gate every fallback on
`errors.As`.

**Prevention**: any new fallback or masking path must answer "which error
classes may trigger me?" before review. See
[Cache and Resilience Guidelines](./cache-and-resilience.md) §4.

### Common Mistake: unbounded error bodies from upstream HTML pages

**Symptom**: multi-KB maintenance-page HTML inlined into tool errors,
flooding MCP client context.

**Cause**: `fmt.Errorf("...: %s", string(body))` on non-2xx responses.

**Fix**: `summarizeErrorBody` — HTML content-type collapses to
`status + <title>`; everything else truncates to 300 bytes
(`TestGet_HTMLErrorCollapsedToTitle`, `TestGet_ErrorBodyTruncated`).

### Common Mistake: transport errors quoting the full request URL

**Symptom**: a timeout, DNS failure, connection refused, or cancellation
puts the caller's credentials (`login` / `api_key` query parameters) into
the tool error payload, which lands in agent context, session stores, and
model-provider logs.

**Cause**: `net/http` reports transport failures as a `*url.Error` whose
text is `Op "<full URL>": <cause>`, and the URL carries the query string
the api layer injected the credentials into. Go only strips the userinfo
password — never the query. Tool handlers forward `err.Error()` verbatim.

**Fix**: sanitize at the single point where transport errors are created:
`doOnce` routes every error through `sanitizeTransportError`
(`internal/api/client.go`), which rebuilds the `*url.Error` with the URL
reduced to scheme + host + path while keeping `Op` and `Err` intact — so
the cause chain (and `errors.Is(err, context.Canceled /
DeadlineExceeded)`) still works. Values that are not `*url.Error` pass
through unchanged.

**Prevention**: never return a raw transport error from a layer that
injects credentials into the request; never "redact" by string-replacing
literal credential values (short logins would mangle unrelated text). Pin
the contract with `TestGet_TransportErrorsOmitCredentials`.
