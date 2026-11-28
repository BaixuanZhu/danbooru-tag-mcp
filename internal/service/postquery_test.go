package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/app"
)

// tokenSignature renders one classified token the way the tables expect it:
// "+arm_up" a positive, "-arm_up" an exclusion, "m:rating:g" a metatag,
// "c:honkai:_star_rail" an unresolved colon token and "c-:foo:bar" a negated
// one. The raw text is never altered, so the signature also pins that.
func tokenSignature(tok postToken) string {
	switch tok.class {
	case tokenMetatag:
		return "m:" + tok.raw
	case tokenColon:
		if tok.negative() {
			return "c-:" + tok.name()
		}
		return "c:" + tok.raw
	case tokenNegative:
		return "-" + tok.name()
	default:
		return "+" + tok.raw
	}
}

// querySignature renders a whole parsed query, one token signature per
// token, in query order.
func querySignature(query string) string {
	q := parsePostQuery(query)
	sigs := make([]string, 0, len(q.tokens))
	for _, tok := range q.tokens {
		sigs = append(sigs, tokenSignature(tok))
	}
	return strings.Join(sigs, " ")
}

// TestParsePostQuery_ClassifiesEveryTableRow walks the classification table
// of the design: every row, plus the spellings around it. A token is a
// metatag only when its key is listed; any other colon token stays unresolved
// until a lookup; a leading '-' makes a content token an exclusion; wildcard
// and OR tokens are ordinary content tokens here (a scan refuses them later,
// a direct query never looks). Each row also pins the content count and the
// term count the routing reads off the same parse.
func TestParsePostQuery_ClassifiesEveryTableRow(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		want     string
		content  int
		counting int // metatags that cost a term
	}{
		// | tag | positive |
		{"plain tag", "arm_up", "+arm_up", 1, 0},
		{"case is kept as typed", "ARM_UP", "+ARM_UP", 1, 0},
		{"meta-category name is still a tag", "highres", "+highres", 1, 0},

		// | -tag | negative |
		{"exclusion", "-arm_up", "-arm_up", 1, 0},
		{"exclusion keeps its case", "-ARM_UP", "-ARM_UP", 1, 0},

		// | known metatag key | metatag |
		{"metatag", "rating:g", "m:rating:g", 0, 0},
		{"metatag, upper-case key", "Rating:G", "m:Rating:G", 0, 0},
		{"negated metatag is a metatag", "-rating:e", "m:-rating:e", 0, 0},
		{"tilde metatag is a metatag", "~status:deleted", "m:~status:deleted", 0, 0},
		{"counting metatag", "order:score", "m:order:score", 0, 1},
		{"metatag whose value holds a wildcard", "source:*pixiv*", "m:source:*pixiv*", 0, 1},
		{"metatag whose value holds spaces is two tokens", "rating:g extra", "m:rating:g +extra", 1, 0},

		// | other colon token | unresolved: a tag name or an unlisted metatag |
		{"colon tag name", "honkai:_star_rail", "c:honkai:_star_rail", 1, 0},
		{"colon inside a tag name", "firefly_(honkai:_star_rail)", "c:firefly_(honkai:_star_rail)", 1, 0},
		{"tag whose name starts with the colon", ":d", "c::d", 1, 0},
		{"unlisted metatag key", "foo:bar", "c:foo:bar", 1, 0},
		{"negated colon token", "-honkai:_star_rail", "c-:honkai:_star_rail", 1, 0},
		{"tilde colon token keeps its tilde", "~honkai:_star_rail", "c:~honkai:_star_rail", 1, 0},
		{"edit-only key is not a search metatag", "artist:bkub", "c:artist:bkub", 1, 0},
		{"key missing its colon is a tag", "rating", "+rating", 1, 0},

		// | contains * or starts with ~ | content token, refused by a scan only |
		{"wildcard suffix", "arm_up*", "+arm_up*", 1, 0},
		{"wildcard prefix", "*arm_up", "+*arm_up", 1, 0},
		{"OR group", "~arm_up sword", "+~arm_up +sword", 2, 0},
		{"negated wildcard", "-arm_up*", "-arm_up*", 1, 0},
		{"negated OR group", "-~arm_up sword", "-~arm_up +sword", 2, 0},

		// Whole queries: order is kept, every class side by side.
		{"one of each class", "1girl -1boy rating:g order:score honkai:_star_rail foo:bar -foo:bar",
			"+1girl -1boy m:rating:g m:order:score c:honkai:_star_rail c:foo:bar c-:foo:bar", 5, 1},
		{"the documented three-tag query", "1girl arm_up sword", "+1girl +arm_up +sword", 3, 0},
		{"empty query", "", "", 0, 0},
		{"whitespace only", "   \t\n ", "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := parsePostQuery(tt.query)
			if got := querySignature(tt.query); got != tt.want {
				t.Errorf("parsePostQuery(%q):\n got: %s\nwant: %s", tt.query, got, tt.want)
			}
			if got := q.contentCount(); got != tt.content {
				t.Errorf("contentCount(%q) = %d, want %d", tt.query, got, tt.content)
			}
			if got := len(q.countingMetatags()); got != tt.counting {
				t.Errorf("counting metatags of %q = %v, want %d", tt.query, q.countingMetatags(), tt.counting)
			}
			if got, want := q.terms(), tt.content+tt.counting; got != want {
				t.Errorf("terms(%q) = %d, want %d", tt.query, got, want)
			}
			// The raw query is the trimmed input, inner whitespace included:
			// it is what a direct query sends upstream.
			if q.raw != strings.TrimSpace(tt.query) {
				t.Errorf("raw = %q, want the trimmed query %q", q.raw, tt.query)
			}
		})
	}
}

// TestParsePostQuery_TrimsEndsAndSplitsOnAnyWhitespace pins what "verbatim"
// means for a query: the ends are trimmed, the inner whitespace is kept in
// the raw query that goes upstream, and any whitespace separates tokens.
func TestParsePostQuery_TrimsEndsAndSplitsOnAnyWhitespace(t *testing.T) {
	q := parsePostQuery("  arm_up\t sword\norder:score ")
	if want := "arm_up\t sword\norder:score"; q.raw != want {
		t.Errorf("raw = %q, want %q", q.raw, want)
	}
	if got, want := querySignature("  arm_up\t sword\norder:score "), "+arm_up +sword m:order:score"; got != want {
		t.Errorf("tokens:\n got: %s\nwant: %s", got, want)
	}
}

// TestParsePostQuery_CountingMetatags pins R11: the term count is content
// tokens plus every metatag outside PostQuery::UNLIMITED_METATAGS. order:,
// user:, pool:, fav: and the *_count keys cost a term; rating:, status:, id:
// and the rest of the unlimited list are free, negated or not.
func TestParsePostQuery_CountingMetatags(t *testing.T) {
	free := []string{
		"rating:g", "status:any", "id:>100", "-rating:e", "Rating:G",
		"favcount:>5", "filetype:png", "width:>=1000", "parent:1", "md5:abc",
	}
	for _, metatag := range free {
		q := parsePostQuery("arm_up sword " + metatag)
		if got, want := q.terms(), 2; got != want {
			t.Errorf("%s is free: terms = %d, want %d", metatag, got, want)
		}
		if n := len(q.countingMetatags()); n != 0 {
			t.Errorf("%s is free: counting metatags = %v", metatag, q.countingMetatags())
		}
	}
	counting := []string{
		"order:score", "order:random", "Order:Score", "-order:score",
		"user:albert", "pool:123", "fav:albert", "search:1girl",
		"comment_count:>5", "gentags:5", "approver:albert", "source:*pixiv*",
	}
	for _, metatag := range counting {
		q := parsePostQuery("arm_up sword " + metatag)
		if got, want := q.terms(), 3; got != want {
			t.Errorf("%s counts: terms = %d, want %d", metatag, got, want)
		}
		if got, want := querySignature("arm_up sword "+metatag), "+arm_up +sword m:"+metatag; got != want {
			t.Errorf("%s is still a metatag token:\n got: %s\nwant: %s", metatag, got, want)
		}
	}

	// Free metatags stack without cost; a counting one takes a term each.
	if got, want := parsePostQuery("arm_up rating:g status:any id:>100").terms(), 1; got != want {
		t.Errorf("free metatags stack for free: terms = %d, want %d", got, want)
	}
	if got, want := parsePostQuery("arm_up order:score user:albert pool:1").terms(), 4; got != want {
		t.Errorf("counting metatags take a term each: terms = %d, want %d", got, want)
	}
	// A metatag-only query has no content token.
	if got, want := parsePostQuery("rating:g").terms(), 0; got != want {
		t.Errorf("a free metatag alone: terms = %d, want %d", got, want)
	}
	if got, want := parsePostQuery("order:score").terms(), 1; got != want {
		t.Errorf("a counting metatag alone: terms = %d, want %d", got, want)
	}
}

// TestParsePostQuery_TermCountFollowsTheWholeKeyList walks both key lists, so
// the verdict cannot hide behind hand-picked examples: every search metatag
// key costs a term exactly when it is outside the unlimited list.
func TestParsePostQuery_TermCountFollowsTheWholeKeyList(t *testing.T) {
	for key := range danbooruMetatagKeys {
		for _, form := range signedForms(key) {
			q := parsePostQuery(form)
			if len(q.tokens) != 1 || q.tokens[0].class != tokenMetatag {
				t.Errorf("%q must parse as one metatag token, got %s", form, querySignature(form))
				continue
			}
			wantCounting := 0
			if !unlimitedMetatagKeys[key] {
				wantCounting = 1
			}
			if got := q.terms(); got != wantCounting {
				t.Errorf("%q: terms = %d, want %d (unlimited = %v)", form, got, wantCounting, unlimitedMetatagKeys[key])
			}
		}
	}
}

// TestParsePostQuery_CapCountsEveryContentToken pins what the local cap
// counts, before any lookup: six content tokens pass and the seventh is
// refused with the historical message shape; exclusions and unresolved colon
// tokens are content tokens; metatags, counting or not, never count toward
// it; and the hint is appended verbatim.
func TestParsePostQuery_CapCountsEveryContentToken(t *testing.T) {
	const six = "a1 a2 a3 a4 a5 a6"
	if err := capViolation(parsePostQuery(six), ""); err != nil {
		t.Errorf("six content tokens must pass the cap, got: %v", err)
	}
	if err := capViolation(parsePostQuery(six+" rating:g order:score user:x"), ""); err != nil {
		t.Errorf("metatags must not count toward the cap, got: %v", err)
	}
	if err := capViolation(parsePostQuery(""), ""); err != nil {
		t.Errorf("an empty query must pass the cap, got: %v", err)
	}

	tests := []struct {
		name  string
		query string
		wantN int
	}{
		{"seven plain tags", six + " a7", 7},
		{"six tags and an exclusion", six + " -a7", 7},
		{"six tags and an unresolved colon token", six + " foo:bar", 7},
		{"six tags and a colon tag name", six + " firefly_(honkai:_star_rail)", 7},
		{"six tags, a metatag and a wildcard token", six + " rating:g a7*", 7},
		{"a long space-split query", "blue hair long hair blue eyes red eyes", 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := capViolation(parsePostQuery(tt.query), "")
			if err == nil {
				t.Fatalf("expected the cap error, got nil")
			}
			want := tooManyTagsMessage(tt.wantN)
			if err.Error() != want {
				t.Errorf("message:\n got: %s\nwant: %s", err, want)
			}
			if errors.Is(err, app.ErrUpstreamUnavailable) {
				t.Errorf("the cap error must not carry the upstream marker, got: %v", err)
			}
		})
	}

	// The hint rides the end of the message exactly as before.
	err := capViolation(parsePostQuery("blue hair long hair blue eyes red eyes"),
		"; 'blue hair' may be one tag: blue_hair; 'long hair' may be one tag: long_hair; 'blue eyes' may be one tag: blue_eyes")
	if want := tooManyTagsMessage(8) + "; 'blue hair' may be one tag: blue_hair; 'long hair' may be one tag: long_hair; 'blue eyes' may be one tag: blue_eyes"; err == nil || err.Error() != want {
		t.Errorf("message with hint:\n got: %v\nwant: %s", err, want)
	}
}

// tooManyTagsMessage is the exact text of the local cap error for n content
// tokens; the space-split hint, when any, follows it. The number is the only
// part that varies and the maximum is the constant the service enforces.
func tooManyTagsMessage(n int) string {
	return fmt.Sprintf("too many content tags: %d (max %d); drop tags or split the query", n, MaxQueryTags)
}
