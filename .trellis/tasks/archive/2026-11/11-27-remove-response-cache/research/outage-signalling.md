# Outage signalling — what an agent receives today

Date: 2026-11-27. Captured by driving the real tool handlers (package
`tools`, registered on an mcp-go server) against a local `httptest` server
that plays an outage. The client had zero retry backoff and a temporary
cache directory. Code at `main` (acd06f9). The probe file was deleted after
the run. With the real 1 s / 3 s backoff every retried call takes about 4 s
longer than shown here.

## With the cache (current 0.6.0 behavior)

| Situation | `isError` | Attempts | Result |
|---|---|---|---|
| healthy call, fills the cache | false | 1 | normal row |
| upstream 503, key stored minutes ago (inside its TTL) | false | 0 | the cached row, **no marker at all** |
| upstream 503, entry past its TTL | false | 3 | the cached row plus `"stale":true,"cached_at":"2026-11-27T14:47:59Z"` |

A fresh hit during an outage is indistinguishable from a live answer: the
agent cannot tell that Danbooru is down.

## Without a usable cache entry (every tool; always the case once the cache is removed)

Upstream returns 503 with the live Downbooru HTML page:

| Tool | `isError` | Text |
|---|---|---|
| `get_tag_info` | false | `{"error":"tag_not_found","message":"danbooru 503 (Downbooru)"}` |
| `get_tags_info` | false | `{"error":"tags_info_failed","message":"danbooru 503 (Downbooru)"}` |
| `search_tags` | false | `{"error":"search_failed","message":"danbooru 503 (Downbooru)"}` |
| `get_related_tags` | false | `{"error":"related_failed","message":"danbooru 503 (Downbooru)"}` |
| `get_tag_alias` | false | `{"error":"alias_failed","message":"danbooru 503 (Downbooru)"}` |
| `get_tag_wiki` | false | `{"error":"wiki_failed","message":"danbooru 503 (Downbooru)"}` |
| `search_posts` | false | `{"error":"post_search_failed","message":"danbooru 503 (Downbooru)"}` |
| `get_tag_profile` | false | `{"error":"profile_failed","message":"danbooru 503 (Downbooru)"}` |

429 with `Retry-After: 1`:

```
{"error":"tag_not_found","message":"rate limited (danbooru 429), retry-after 1s"}
```

Connection refused:

```
{"error":"tag_not_found","message":"Get \"http://127.0.0.1:7262/tags.json?limit=1\u0026search%5Bname%5D=purple_hair\": dial tcp 127.0.0.1:7262: connectex: No connection could be made because the target machine actively refused it."}
{"error":"post_search_failed","message":"Get \"http://127.0.0.1:7262/posts.json?limit=1\u0026tags=purple_hair\": dial tcp ..."}
```

## Defects

1. `get_tag_info` reports every upstream failure under `tag_not_found`
   (`internal/tools/register.go:308` — the non-`NotFoundError` branch reuses
   the not-found code). An agent that reads `error` concludes the tag does
   not exist and may try other spellings, all futile.
2. Every tool error is a text result with `isError: false`
   (`errResp` → `mcp.NewToolResultText`, `register.go:36-50`): a
   protocol-level success. Client records show these calls as `completed`.
3. Network errors carry the raw request URL (task
   `11-27-sanitize-error-credentials`).
4. Eight different codes for one situation; nothing tells the agent that
   every tool of the server is affected.

## Timing

- Fast-failing outage (503 / refused): 3 attempts plus 1 s and 3 s backoff,
  about 4-5 s per call.
- Hang (no response): each attempt waits for the 30 s client timeout, three
  attempts plus backoff is about 94 s. A client that gives up earlier shows
  the agent its own timeout, not this server's message.

## Target

One code on every tool, flagged at the protocol level:

```
{"error":"upstream_unavailable","message":"Danbooru is not answering right now: danbooru 503 (Downbooru). All tools of this server are affected; retry later."}
```

with `isError: true`. `tag_not_found` stays reserved for a genuine not-found.
