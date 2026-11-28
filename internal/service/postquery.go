package service

import (
	"fmt"
	"strings"
)

// MaxQueryTags caps the content tokens one posts query may name, in list and
// in count mode alike. A content token is anything that is not a known
// metatag — a plain tag, a "-tag" exclusion, an unknown-key colon token such
// as firefly_(honkai:_star_rail) or foo:bar — and every one of them counts,
// before any lookup: the cap is checked locally, so it costs no request.
// Above the cap the query is refused locally.
const MaxQueryTags = 6

// anonymousTagLimit is the number of terms anonymous Danbooru answers as-is.
// The verified rule (live and in app/models/user.rb, 2026-11-28): anonymous
// 2 terms, any logged-in level below Gold 3, Gold and above unlimited — a
// term being a tag or a counting metatag. This is the service's default
// direct tag limit. Only an account configured above it — whose real limit
// depends on its level and is not known in advance — gets the tag-limit
// fallback from a refused direct query; for the anonymous limit the number is
// exact, so a refusal of a query within it means the term count itself is
// off, and a scan built on the same count could not repair it.
const anonymousTagLimit = 2

// tokenClass is what one whitespace-separated posts-query token is.
type tokenClass int

const (
	tokenPositive tokenClass = iota // a tag that must be present
	tokenNegative                   // a "-tag" exclusion: a tag that must be absent
	tokenMetatag                    // a known metatag key: rating:g, order:score, -status:deleted
	tokenColon                      // another colon token: a tag name (honkai:_star_rail) or a metatag missing from the key list (foo:bar); only a Danbooru lookup tells which
)

// postToken is one posts-query token with its class. raw is kept as typed: a
// metatag and a pass-through token ride the upstream query verbatim, because
// no layer of this server rewrites metatags.
type postToken struct {
	raw   string
	class tokenClass
}

// name is the tag name a content token carries: the token minus its one
// leading '-'. A metatag has no name; the scan never asks one for it.
func (t postToken) name() string {
	return strings.TrimPrefix(t.raw, "-")
}

// negative reports whether a content token excludes its tag.
func (t postToken) negative() bool {
	return strings.HasPrefix(t.raw, "-")
}

// wildcardOrOR reports whether a content token needs Danbooru to expand it —
// a wildcard anywhere in it, or a leading ~ (OR) — which a post's tag list
// cannot reproduce. A scan that would have to filter on such a token is
// refused; a direct query is untouched.
func (t postToken) wildcardOrOR() bool {
	return strings.Contains(t.raw, "*") || strings.HasPrefix(t.name(), "~")
}

// postQuery is a posts query split into classified tokens. It is computed per
// call and never stored on the service.
type postQuery struct {
	raw    string // the query as sent, trimmed
	tokens []postToken
}

// parsePostQuery classifies every whitespace-separated token of a query. A
// token is a metatag exactly when it begins with a known metatag key
// (isDanbooruMetatag, which skips one leading - or ~); a leading '-' makes a
// content token an exclusion. The verdict never follows from a colon: a colon
// token whose key is not a listed metatag stays unresolved (tokenColon) until
// Danbooru says whether it names a tag, so firefly_(honkai:_star_rail) is
// never mistaken for a metatag and an unlisted future one is never mistaken
// for a tag.
func parsePostQuery(query string) *postQuery {
	q := &postQuery{raw: strings.TrimSpace(query)}
	for _, raw := range strings.Fields(q.raw) {
		tok := postToken{raw: raw, class: tokenPositive}
		switch {
		case isDanbooruMetatag(raw):
			tok.class = tokenMetatag
		case strings.Contains(raw, ":"):
			tok.class = tokenColon
		case strings.HasPrefix(raw, "-"):
			tok.class = tokenNegative
		}
		q.tokens = append(q.tokens, tok)
	}
	return q
}

// select returns the tokens keep accepts, in query order.
func (q *postQuery) selectTokens(keep func(postToken) bool) []postToken {
	out := make([]postToken, 0, len(q.tokens))
	for _, tok := range q.tokens {
		if keep(tok) {
			out = append(out, tok)
		}
	}
	return out
}

// isContent reports whether a token is not a metatag.
func isContent(tok postToken) bool { return tok.class != tokenMetatag }

// contentTokens returns the tokens that are not metatags, in query order.
func (q *postQuery) contentTokens() []postToken {
	return q.selectTokens(isContent)
}

// contentCount returns how many tokens are content tokens: the number the
// local tag cap (MaxQueryTags) applies to.
func (q *postQuery) contentCount() int {
	n := 0
	for _, tok := range q.tokens {
		if isContent(tok) {
			n++
		}
	}
	return n
}

// metatags returns the metatag tokens, in query order.
func (q *postQuery) metatags() []postToken {
	return q.selectTokens(func(tok postToken) bool { return tok.class == tokenMetatag })
}

// countingMetatags returns the metatag tokens that cost a term toward
// Danbooru's tag limit (isCountingMetatag), in query order.
func (q *postQuery) countingMetatags() []postToken {
	return q.selectTokens(func(tok postToken) bool { return tok.class == tokenMetatag && isCountingMetatag(tok) })
}

// terms returns the number of terms Danbooru counts against its tag limit:
// content tokens plus counting metatags. terms <= tagLimit is what decides
// whether a query is sent as-is.
func (q *postQuery) terms() int {
	n := 0
	for _, tok := range q.tokens {
		if isContent(tok) || isCountingMetatag(tok) {
			n++
		}
	}
	return n
}

// isCountingMetatag reports whether a metatag token costs a term toward
// Danbooru's tag limit: every metatag does unless its key is in
// PostQuery::UNLIMITED_METATAGS (unlimitedMetatagKeys). A content token
// reports true, which is why terms can ask every token the same question.
func isCountingMetatag(tok postToken) bool {
	if tok.class != tokenMetatag {
		return true
	}
	key, _ := metatagKey(tok.raw)
	return !unlimitedMetatagKeys[key]
}

// capViolation returns the local error for a query naming more content tokens
// than MaxQueryTags, or nil. hint is appended verbatim (the space-split
// candidates of the query, "" when there are none), so the text keeps the
// shape it had when the cap was Danbooru's anonymous limit.
func capViolation(q *postQuery, hint string) error {
	n := q.contentCount()
	if n <= MaxQueryTags {
		return nil
	}
	return fmt.Errorf("too many content tags: %d (max %d); drop tags or split the query%s", n, MaxQueryTags, hint)
}
