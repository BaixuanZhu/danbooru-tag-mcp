#!/usr/bin/env bash
# Online integration smoke test for danbooru-tag-mcp.
#
# Runs the real binary over stdio and calls every MCP tool once against the
# live Danbooru API (anonymous), asserting response shapes. This is the
# canary for upstream API drift: when /related_tag.json was revamped in 2024
# the fixture-based unit tests stayed green — only a live call catches that.
#
# Isolation: DANBOORU_MCP_NO_BOOTSTRAP=1 is exported unconditionally below,
# so the test binary never writes the user PATH or registry, even when a
# release-flavored exe is passed in. The only file written is a temp output
# file, removed on exit; nothing outside the repo is modified.
#
# Usage:
#   bash scripts/integration-test.sh [exe]
#   Default exe: dist/amd64/danbooru-tag-mcp.exe (build it first: make build).
#   stdin hold-open time is tunable via DANBOORU_MCP_IT_SLEEP (default 45s).

set -euo pipefail

EXE="${1:-dist/amd64/danbooru-tag-mcp.exe}"
SLEEP="${DANBOORU_MCP_IT_SLEEP:-45}"

if [ ! -f "$EXE" ]; then
  echo "[integration] exe not found: $EXE (build it first: make build)" >&2
  exit 1
fi
if ! command -v python >/dev/null 2>&1; then
  echo "[integration] python is required for the JSON assertions" >&2
  exit 1
fi

# Hard isolation: never touch the machine PATH/registry, whatever the flavor
# of the binary under test.
export DANBOORU_MCP_NO_BOOTSTRAP=1

OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT

echo "[integration] exe: $EXE (holding stdin open ${SLEEP}s for 9 throttled calls)"

req() { printf '%s\n' "$1"; }

{
  req '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"integration","version":"0.0.1"}}}'
  req '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  req '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
  req '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_tags","arguments":{"query":"blue_hair","limit":3}}}'
  req '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_tag_info","arguments":{"name":"blue_hair"}}}'
  req '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_related_tags","arguments":{"tag":"blue_hair","limit":3}}}'
  req '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"get_tag_alias","arguments":{"name":"sailor_suit"}}}'
  req '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_tag_wiki","arguments":{"title":"firefly_(honkai:_star_rail)"}}}'
  req '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"search_posts","arguments":{"tags":"1girl","limit":2}}}'
  req '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"search_posts","arguments":{"tags":"1girl blue_hair long_hair"}}}'
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

# 2. all six tools registered
names = sorted(t['name'] for t in resp.get(2, {}).get('result', {}).get('tools', []))
expected = sorted(['search_tags', 'get_tag_info', 'get_related_tags',
                   'get_tag_alias', 'get_tag_wiki', 'search_posts'])
check(names == expected, 'tools/list: exactly the six tools')

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
      and isinstance(related[0].get('similarity'), float),
      'get_related_tags: entries carry tag + similarity')

# 6. get_tag_alias
alias = payload(6).get('alias')
check(bool(alias) and alias.get('consequent') == 'sailor',
      'get_tag_alias: sailor_suit resolves to sailor')

# 7. get_tag_wiki
pages = payload(7).get('wiki_pages')
check(bool(pages) and len(pages[0].get('linked_tags', [])) > 0,
      'get_tag_wiki: linked_tags extracted from body')

# 8. search_posts default rating
posts = payload(8).get('posts')
check(bool(posts) and all(p.get('rating') == 'e' for p in posts),
      'search_posts: default rating filter is explicit')

# 9. tag-count pre-validation (rejected locally, no network round trip)
err = payload(9)
check(err.get('error') == 'post_search_failed'
      and 'too many content tags' in err.get('message', ''),
      'search_posts: over-limit query rejected locally')

print()
if failures:
    sys.exit('[integration] %d check(s) FAILED' % len(failures))
print('[integration] all checks passed')
PYEOF
