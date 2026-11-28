# /counts/posts.json — verified facts (live probes, 2026-11-27)

- Anonymous access returns EXACT, uncapped totals: whole-site
  N = 12,583,571 (2026-11-27); arm_up = 317,272, matching its `post_count`
  exactly. No cap, no auth needed.
- Tag limit matches /posts.json: 3 content tags -> HTTP 422
  (TagLimitError). The 2-tag local pre-validation therefore applies to
  count mode unchanged.
- NOT probed: metatag handling (`rating:` / `order:` / `status:`) on the
  counts endpoint. Design must probe (or verify the request path) before
  promising metatag support; if unsupported, the local validator should
  reject them in count mode with the same clear error text as post mode.
- Response shape is the standard Danbooru `{"counts": {"posts": N}}` —
  confirm the exact key with one live call during implementation.
- Always live: the 0.7.0 release removed the response cache; nothing may be
  reintroduced (maintainer decision 2026-11-27).

## Live facts (anonymous probes, 2026-11-28) — supersede the "NOT probed" note

- Response shape confirmed: `{"counts":{"posts":<int>}}`. Whole-site
  N = 12,592,609; `tags=arm_up` → 317,377, equal to the tag row's
  `post_count` on the same day.
- Metatags ARE supported: `arm_up rating:g|s|q|e` → 108,422 / 158,579 /
  33,056 / 17,320 (sum = 317,377, exactly the unfiltered total); a
  metatag-only query `rating:g` → 4,674,251; `order:random` does not change
  the count; `status:deleted` (8,122) and negation `-sword` (303,940) work.
  So the local validator needs no count-mode-specific rejection.
- `null` count: `arm_up filetype:png` → `{"counts":{"posts":null}}`. Upstream
  source (`app/controllers/counts_controller.rb`): the action runs
  `PostQuery#fast_count` under a statement timeout and answers null when the
  count times out; it may also estimate when an exact count is unavailable.
  A count is therefore "what Danbooru reports" — exact for plain tag
  queries, possibly unavailable for expensive metatags. That null is a
  definitive answer from a reachable upstream, not an outage: it must map to
  an unmarked error, never to `0` and never to `upstream_unavailable`.
- Tag limit identical to /posts.json: 3 content tags → HTTP 422
  (`PostQuery::TagLimitError`); an unknown tag → 0.
