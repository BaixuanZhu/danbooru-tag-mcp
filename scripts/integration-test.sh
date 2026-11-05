#!/usr/bin/env bash
# Online integration smoke test for danbooru-tag-mcp.
#
# Runs the real binary over stdio and calls every MCP tool against the
# live Danbooru API (anonymous), asserting response shapes — including a
# tagger-era name whose wd14 verdict must ride the not-found error. This is
# the canary for upstream API drift: when /related_tag.json was revamped in
# 2024 the fixture-based unit tests stayed green — only a live call catches
# that.
#
# Isolation: DANBOORU_MCP_NO_BOOTSTRAP=1 and DANBOORU_MCP_NO_CACHE=1 are
# exported unconditionally below, so the test binary never writes the user
# PATH or registry (even when a release-flavored exe is passed in) and every
# tool call hits the live Danbooru API instead of a warm local cache — this
# is the upstream-drift canary, so replayed bodies would defeat it. The only
# file written is a temp output file, removed on exit; nothing outside the
# repo is modified.
#
# Usage:
#   bash scripts/integration-test.sh [exe]
#   Default exe: dist/amd64/danbooru-tag-mcp.exe (build it first: make build).
#   stdin hold-open time is tunable via DANBOORU_MCP_IT_SLEEP (default 60s).

set -euo pipefail

EXE="${1:-dist/amd64/danbooru-tag-mcp.exe}"
SLEEP="${DANBOORU_MCP_IT_SLEEP:-60}"

if [ ! -f "$EXE" ]; then
  echo "[integration] exe not found: $EXE (build it first: make build)" >&2
  exit 1
fi
if ! command -v python >/dev/null 2>&1; then
  echo "[integration] python is required for the JSON assertions" >&2
  exit 1
fi

# Hard isolation: never touch the machine PATH/registry, whatever the flavor
# of the binary under test, and never serve a cached response — the smoke
# test must verify the live upstream.
export DANBOORU_MCP_NO_BOOTSTRAP=1
export DANBOORU_MCP_NO_CACHE=1

OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT

echo "[integration] exe: $EXE (holding stdin open ${SLEEP}s for 18 tool calls)"

req() { printf '%s\n' "$1"; }

{
  req '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"integration","version":"0.0.1"}}}'
  req '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  req '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
  req '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_tags","arguments":{"query":"blue_hair","limit":3}}}'
  req '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_tag_info","arguments":{"name":"blue_hair"}}}'
  req '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_related_tags","arguments":{"tag":"blue_hair","limit":3}}}'
  req '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"get_tag_alias","arguments":{"name":"sailor_suit"}}}'
  req '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_tag_wiki","arguments":{"title":"firefly_(honkai:_star_rail)","include_description":true}}}'
  req '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"search_posts","arguments":{"tags":"1girl rating:e","limit":2}}}'
  req '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"search_posts","arguments":{"tags":"1girl blue_hair long_hair"}}}'
  req '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"get_tag_info","arguments":{"name":"gold_footwear"}}}'
  req '{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"search_tags","arguments":{"query":"gold footwear","limit":5}}}'
  req '{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"get_tag_profile","arguments":{"tag":"arm_up","sample":200}}}'
  req '{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"get_tags_info","arguments":{"names":["blue_hair","gold_footwear","not_a_real_tag_xyz"]}}}'
  req '{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"get_tag_profile","arguments":{"tag":"arm_up","with":"holding_weapon","sample":50}}}'
  req '{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"get_tag_profile","arguments":{"tag":"arm_up","rating":"g","sample":20}}}'
  req '{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"search_tags","arguments":{"query":"firefly","category":"character","limit":5}}}'
  req '{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"get_tag_info","arguments":{"name":"sailor_suit"}}}'
  req '{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"search_posts","arguments":{"tags":"1girl","limit":21}}}'
  sleep "$SLEEP"
} | "$EXE" > "$OUT"

echo "[integration] asserting responses..."
python - "$OUT" <<'PYEOF'
import json
import sys

resp = {}
with open(sys.argv[1], encoding='utf-8') as fh:
    for line in fh:
        line = line.strip()
        if line:
            msg = json.loads(line)
            if msg.get('id') is not None:
                resp[msg['id']] = msg

failures = []

def check(cond, label):
    print(('  ok   ' if cond else '  FAIL ') + label)
    if not cond:
        failures.append(label)

def payload(rid):
    msg = resp.get(rid)
    if msg is None:
        failures.append('id %d: no response' % rid)
        return {}
    if 'error' in msg:
        failures.append('id %d: jsonrpc error %s' % (rid, msg['error']))
        return {}
    content = msg.get('result', {}).get('content')
    if not content:
        failures.append('id %d: empty content' % rid)
        return {}
    return json.loads(content[0]['text'])

# 1. initialize handshake
info = resp.get(1, {}).get('result', {}).get('serverInfo', {})
check(info.get('name') == 'danbooru-tags', 'initialize: server reports danbooru-tags')

# 2. all eight tools registered
names = sorted(t['name'] for t in resp.get(2, {}).get('result', {}).get('tools', []))
expected = sorted(['search_tags', 'get_tag_info', 'get_tags_info',
                   'get_related_tags', 'get_tag_alias', 'get_tag_wiki',
                   'search_posts', 'get_tag_profile'])
check(names == expected, 'tools/list: exactly the eight tools')

# 3. search_tags
tags = payload(3).get('tags')
check(bool(tags) and tags[0].get('post_count', 0) > 0,
      'search_tags: results carry name + post_count')

# 4. get_tag_info
tag = payload(4)
check(tag.get('name') == 'blue_hair' and tag.get('post_count', 0) > 0,
      'get_tag_info: exact tag lookup')

# 5. get_related_tags: the upstream drift canary (broken by the 2024 revamp)
related = payload(5).get('related')
check(bool(related) and bool(related[0].get('tag'))
      and isinstance(related[0].get('similarity'), float)
      and all(e.get('category') != 5 for e in related),
      'get_related_tags: entries carry tag + similarity, meta tags excluded')

# 6. get_tag_alias
alias = payload(6).get('alias')
check(bool(alias) and alias.get('consequent') == 'sailor',
      'get_tag_alias: sailor_suit resolves to sailor')

# 7. get_tag_wiki: structured fields replace the raw body. The firefly page
#    must distill into summary/intro_tags/rules/see_also/example_posts, keep
#    linked_tags, expose the appearance prose only via include_description,
#    and never carry a body key.
# The "or [{}]" keeps an upstream outage survivable: every page7 check below
# then FAILs cleanly instead of crashing the whole assertion run on None[0].
page7 = (payload(7).get('wiki_pages') or [{}])[0]
check(bool(page7.get('summary')) and 'Honkai: Star Rail' in page7['summary'],
      'get_tag_wiki: summary distilled from the intro')
check(len(page7.get('intro_tags', [])) >= 20
      and 'grey hair' in page7['intro_tags'],
      'get_tag_wiki: intro_tags carry the appearance tag targets')
check('body' not in page7, 'get_tag_wiki: raw body absent')
check(bool(page7.get('description'))
      and 'grey hair' in page7['description']
      and '[[[' not in page7['description'],
      'get_tag_wiki: include_description returns the flattened intro prose')
check(len(page7.get('example_posts', [])) > 0
      and all(isinstance(i, int) for i in page7['example_posts']),
      'get_tag_wiki: example post ids extracted')
check(len(page7.get('linked_tags', [])) > 0,
      'get_tag_wiki: linked_tags union still present')

# 8. search_posts honors the caller's rating metatag + per-post categorized
#    tag list. (No default rating is injected — that boundary is pinned
#    deterministically by TestSearchPosts_NoRatingInjection at the mock layer.)
posts = payload(8).get('posts')
check(bool(posts) and all(p.get('rating') == 'e' for p in posts)
      and all(isinstance(p.get('tags', {}).get('general'), list)
              and len(p['tags']['general']) > 0 for p in posts),
      'search_posts: caller rating metatag honored + categorized tag list per post')

# 9. tag-count pre-validation (rejected locally, no network round trip)
err = payload(9)
check(err.get('error') == 'post_search_failed'
      and 'too many content tags' in err.get('message', ''),
      'search_posts: over-limit query rejected locally')

# 10. get_tag_info on a tagger-era name: gold_footwear exists only in the
#     pinned WD14 vocabularies; Danbooru keeps it as a 0-post placeholder
#     row, which must be classified tagger_era with both snapshot sources.
info10 = payload(10)
check(info10.get('name') == 'gold_footwear'
      and info10.get('post_count') == 0
      and info10.get('wd14', {}).get('status') == 'tagger_era'
      and len(info10['wd14'].get('sources', [])) == 2,
      'get_tag_info: 0-post tagger-era name reports wd14 tagger_era + both sources')

# 11. search_tags fallback: with no live (post-carrying) Danbooru result,
#     wd14_hits must surface the WD14 snapshot names containing the query.
search11 = payload(11)
check(search11.get('wd14_hits')
      and any(h.get('name') == 'gold_footwear' and len(h.get('sources', [])) == 2
              for h in search11['wd14_hits']),
      'search_tags: dead-end query surfaces wd14_hits')

# 12. get_tag_profile: live distribution profile of a big tag. The sample
#     must be a real random draw (no rating filter, self/meta excluded) and
#     both frequency-ordered singles and lift-ranked pairs must come back.
prof = payload(12)
allt = prof.get('co_tags', []) + prof.get('ubiquitous', [])
check(prof.get('name') == 'arm_up' and prof.get('sample', 0) >= 100
      and prof.get('wd14', {}).get('status') == 'live',
      'get_tag_profile: arm_up sampled live')
check(bool(prof.get('ubiquitous')) and prof['ubiquitous'][0].get('freq', 0) >= 0.5,
      'get_tag_profile: corpus constants split into ubiquitous')
check(bool(prof.get('co_tags')) and prof['co_tags'][0].get('freq', 0) >= 0.1
      and all(e.get('category') != 5 for e in allt)
      and not any(e.get('tag') == 'arm_up' for e in allt),
      'get_tag_profile: freq-ordered co_tags, meta and self excluded')
check(bool(prof.get('top_pairs'))
      and all(isinstance(p.get('lift'), (int, float)) for p in prof['top_pairs']),
      'get_tag_profile: lift-ranked combinations present')

# 13. get_tags_info: batch exact lookup in one request — live name, 0-post
#     tagger-era placeholder row, and a genuinely missing name, all in one
#     response (search[name_normalize] comma list, verified live).
batch = payload(13)
found13 = {t.get('name'): t for t in batch.get('found', [])}
missing13 = {m.get('name'): m for m in batch.get('missing', [])}
check(found13.get('blue_hair', {}).get('post_count', 0) > 0
      and found13['blue_hair'].get('wd14', {}).get('status') == 'live',
      'get_tags_info: live name found with wd14 live')
check(found13.get('gold_footwear', {}).get('post_count') == 0
      and found13['gold_footwear'].get('wd14', {}).get('status') == 'tagger_era'
      and len(found13['gold_footwear'].get('wd14', {}).get('sources', [])) == 2,
      'get_tags_info: 0-post placeholder found, classified tagger_era + both sources')
check('not_a_real_tag_xyz' in missing13
      and missing13['not_a_real_tag_xyz'].get('wd14', {}).get('status') == 'unknown',
      'get_tags_info: absent name listed under missing with wd14 unknown')

# 14. get_tag_profile pair mode: exactly "<tag> <with>" (no order:, which
#     would 422 against the anonymous 2-tag cap), the 50 most recent joint
#     posts, both query tags excluded from every ranked section.
prof14 = payload(14)
ranked14 = (prof14.get('ubiquitous', []) + prof14.get('co_tags', [])
            + prof14.get('top_pairs', []))
ranked_names14 = [e.get('tag', e.get('pair', '')) for e in ranked14]
check(prof14.get('order') == 'recent'
      and prof14.get('with', {}).get('tag') == 'holding_weapon'
      and prof14.get('with', {}).get('post_count', 0) > 0,
      'get_tag_profile: pair mode reports order=recent + with descriptor')
check(prof14.get('sample', 0) >= 10
      and not any('arm_up' in n or 'holding_weapon' in n for n in ranked_names14),
      'get_tag_profile: pair mode excludes both query tags from rankings')
check(bool(prof14.get('ratings')),
      'get_tag_profile: pair mode reports the ratings mix')

# 15. get_tag_profile rating filter: the exempt rating: metatag narrows the
#     sample without counting against the tag cap.
prof15 = payload(15)
check(prof15.get('rating') == 'g' and prof15.get('ratings') == {'g': 1.0}
      and prof15.get('sample', 0) >= 10,
      'get_tag_profile: rating:g sample is all-g')

# 16. search_tags category filter: the API wants numeric ids; the server maps
#     the name so every row comes back a character (4).
tags16 = payload(16).get('tags')
check(bool(tags16) and all(t.get('category') == 4 for t in tags16),
      'search_tags: category=character returns only category-4 rows')

# 17. get_tag_info renamed_to: sailor_suit keeps a 0-post placeholder row and
#     an active alias; the consequent rides the row.
info17 = payload(17)
check(info17.get('name') == 'sailor_suit' and info17.get('post_count') == 0
      and info17.get('renamed_to') == 'sailor',
      'get_tag_info: 0-post renamed name carries renamed_to')

# 18. search_posts limit cap: rejected locally before any request.
err18 = payload(18)
check(err18.get('error') == 'post_search_failed'
      and 'too many posts requested' in err18.get('message', ''),
      'search_posts: limit over 20 rejected locally')

print()
if failures:
    sys.exit('[integration] %d check(s) FAILED' % len(failures))
print('[integration] all checks passed')
PYEOF
