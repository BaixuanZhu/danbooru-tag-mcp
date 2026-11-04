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
