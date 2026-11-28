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

**Symptom**: revoked credentials (401/403) or invalid queries (422) are
treated as "the answer is unavailable" — the failure that motivated this
lesson was a stale-cache fallback replaying old data for a revoked key
(the cache itself left with 0.7.0, but the classification rule stays).

**Cause**: gating a fallback (default value, retry, synthetic result) on
`err != nil` instead of classifying the error first. Only transient
failures (5xx, network, 429) mean "the answer is unavailable"; a 4xx is a
definitive answer from a reachable upstream.

**Fix**: wrap retryable failures in a sentinel type (today:
`upstreamError` in `internal/api/client.go`, whose `Is` matches
`app.ErrUpstreamUnavailable` — it descends from the old `transientError`
sentinel that gated the removed stale fallback) and gate every fallback or
result-mapping branch on `errors.Is` / `errors.As`, never on `err != nil`.

**Prevention**: any new fallback or masking path must answer "which error
classes may trigger me?" before review. See
[Resilience Guidelines](./resilience.md) §4.

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

### Common Mistake: treating every colon as metatag syntax

**Symptom**: `get_tag_profile` refused the real tags
`firefly_(honkai:_star_rail)` and `honkai:_star_rail` with "metatags and
multi-tag queries are not supported", while `get_tag_info`, `get_tag_wiki`
and `search_tags` resolved the same names.

**Cause**: the guard was `strings.ContainsAny(raw, " :")` — "a colon means
a metatag". Danbooru decides by the text BEFORE the colon: `key:value` is a
metatag only when `key` is in `PostQueryBuilder::METATAGS` (matched
case-insensitively); a colon anywhere else belongs to the tag name, and
Danbooru's own `TagNameValidator` relies on that same list to keep real
tags from looking like metatags.

**Fix**: `internal/service/metatag.go`. `metatagKey(name)` skips one
leading `-` / `~` and returns the lowercased text before the first colon.
`isDanbooruMetatag` looks that key up in `danbooruMetatagKeys`
(`PostQueryBuilder::METATAGS`, 101 keys, transcribed 2026-11-28); the
edit-only keys of `PostEdit::METATAGS` sit in a second list,
`danbooruEditMetatagKeys` (13 keys), behind `isDanbooruEditMetatag`,
because a post query does not know them: they only fence names.
`isSingleTagName(raw)` = non-blank, one whitespace-separated token, neither
kind of metatag; `Profile` calls it for both `tag` and `with`, and the two
error strings are unchanged.

| Input (`tag` / `with`) | Verdict |
|---|---|
| `arm_up`, `honkai:_star_rail`, `firefly_(honkai:_star_rail)`, `:d` | accepted — one tag name |
| `order:random`, `Rating:G`, `-rating:e`, `~status:deleted`, `gentags:5` | rejected — search metatag key |
| `artist:bkub`, `character:x`, `gen:1girl`, `newpool:x` | rejected — edit-only key |
| `arm_up 1boy`, tab- or newline-separated input | rejected — not one token |
| empty, whitespace only | rejected |

The predicates are pure mirrors of the upstream regex, so
`isDanbooruMetatag("rating:g extra")` is true (the value may hold spaces);
single-token input is `isSingleTagName`'s rule, not the predicate's.

**Prevention**: never validate or classify tag tokens with "contains `:`".
When a wrong verdict rejects input or counts a token against a limit, use
the known-key predicate; a colon token whose key is not listed is a content
tag unless Danbooru says otherwise. Keep the two lists apart: query
classification wants the search keys only. The one deliberately broader
check is `isMetatagToken` in `tag.go` (`search_posts` tag counting):
exempting a token from the cap is the safe side there, so it differs from
`isDanbooruMetatag` on purpose. The guard is a clear-error path, not the
injection barrier — profile sample queries are built from resolved row
names, never from caller text.

Pinned by `TestIsDanbooruMetatag_OnlyKnownKeysMatch`,
`TestIsDanbooruEditMetatag_OnlyEditKeysMatch`, the list guards
(`..._RecognizesEveryListedKey`, `..._AreUniqueLowercase[AndDisjoint]`,
`TestMetatagKeyLists_MatchUpstreamSnapshot`),
`TestIsSingleTagName_RejectsMultiTokenAndMetatag`,
`TestProfile_MetatagRejectionText` (exact messages),
`TestProfile_AcceptsColonNames_Single` / `_Pair`,
`TestProfile_ColonFirstNameReachesTheApi` and
`TestProfile_RejectsAnyWhitespace`; integration checks 19-21 cover both
names live and the edit-only rejection.
