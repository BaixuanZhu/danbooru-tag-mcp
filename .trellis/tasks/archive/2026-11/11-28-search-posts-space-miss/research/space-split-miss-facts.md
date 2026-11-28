# Space-split silent empty — defect facts (found 2026-11-27)

- `search_posts` splits its query on spaces; that is the multi-tag syntax,
  so a space INSIDE one tag name ("blue hair" meaning blue_hair) silently
  becomes two tokens. Both end up as tags with ~no posts and the response is
  a bare `[]` with no hint — a dead end for the consuming agent.
- P2 severity: no wrong answers are produced (empty), but the caller gets no
  signal that the query itself was malformed.
- Fix direction agreed during analysis: diagnostics, not re-parsing — the
  repo convention is facts-only output (the server reports facts; the agent
  decides). Candidate shape: on an empty result, if a token has no posts and
  the underscore-joined form matches the embedded WD14 vocabularies (the
  `wd14_hits` machinery is already local and free), name the candidate in
  the response. NO automatic retry with a rewritten query.
- Consistency: `get_tag_info` / `search_tags` already model this pattern
  (`wd14` verdict / `wd14_hits` on dead ends) — reuse it.
- The 2-tag cap interacts: "blue hair" consumes both slots, so legitimate
  single-tag intent plus a metatag would trip the local too-many-tags error
  instead of reaching the empty path — check how that path reports and
  whether the same diagnostic belongs there.

## Live facts (anonymous probes, 2026-11-28)

- `blue` and `hair` both exist on Danbooru as 0-post placeholder rows;
  `blue_hair` has ~1.24M posts. `/posts.json?tags=blue hair` returns `[]`
  and `/counts/posts.json?tags=blue hair` returns 0 — the silent dead end.
- Embedded vocabularies: `blue_hair`, `long_hair` and `gold_footwear` are
  carried by both snapshots; bare `blue`, `hair` and `solo_male` by neither.
  So `blue hair` yields exactly one candidate (`blue_hair`) and its tokens
  are not vocabulary names themselves.
- Local validator today: `blue hair order:random` counts 3 content tokens
  (`order:` counts) and is rejected locally with `too many content tags`,
  although the intended `blue_hair order:random` is a legal 2-tag query.
