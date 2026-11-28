package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/vocab"
)

// overLimitMessage is the exact text of the local too-many-tags error for an
// 8-token query, as it reads since the cap became this server's own (6
// content tokens) instead of Danbooru's anonymous limit. Every query that
// names no candidate must keep it byte for byte.
const overLimitMessage = "too many content tags: 8 (max 6); drop tags or split the query"

// overLimitText builds the expected too-many-tags message for n content tags
// followed by the given candidate segments, starting from the pinned 8-token
// text: only the count and the trailing segments differ.
func overLimitText(n int, segments ...string) string {
	msg := strings.Replace(overLimitMessage, "content tags: 8", fmt.Sprintf("content tags: %d", n), 1)
	return msg + strings.Join(segments, "")
}

// searchPostsJSON runs one search_posts query against a mock Danbooru and
// returns the marshaled payload, i.e. the bytes the handler forwards.
func searchPostsJSON(t *testing.T, query string, store *vocab.Store, modes ...string) string {
	t.Helper()
	mode := ""
	if len(modes) > 0 {
		mode = modes[0]
	}
	svc := NewTagService(&mockFetcher{postsResp: []byte(`[]`)}, store)

	res, err := svc.SearchPosts(context.Background(), query, 5, mode)
	if err != nil {
		t.Fatalf("query %q: unexpected error: %v", query, err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("query %q: marshal: %v", query, err)
	}
	return string(raw)
}

// TestSearchPosts_OverLimitMessageIsExact pins the local too-many-tags error
// byte for byte for every query that names no candidate: the 6-tag cap fires
// before any request, and the text must not drift — agents match on it.
func TestSearchPosts_OverLimitMessageIsExact(t *testing.T) {
	tests := []struct {
		name  string
		query string
		store *vocab.Store
	}{
		{"eight made-up tokens", "t1 t2 t3 t4 t5 t6 t7 t8", vocab.Default()},
		{"eight made-up tokens plus a metatag, which does not count", "t1 t2 t3 t4 t5 t6 t7 t8 order:count", vocab.Default()},
		{"space-split pairs, no vocabulary to name them", "blue hair long hair blue eyes red eyes", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{postsResp: []byte(`[]`)}
			svc := NewTagService(mock, tt.store)

			_, err := svc.SearchPosts(context.Background(), tt.query, 5, "")
			if err == nil {
				t.Fatal("expected the too-many-tags error, got nil")
			}
			if err.Error() != overLimitMessage {
				t.Errorf("message:\n got: %s\nwant: %s", err.Error(), overLimitMessage)
			}
		})
	}
}

// TestSearchPosts_EmptyAnswerCostsOneVerbatimRequest pins the request cost
// of an empty answer: exactly one FetchPosts, carrying the query as typed.
// Whatever rides an empty answer is computed locally; a retry with a
// rewritten query would spend a second request and answer a question the
// caller did not ask.
func TestSearchPosts_EmptyAnswerCostsOneVerbatimRequest(t *testing.T) {
	for _, query := range []string{"blue hair", "blue xyzzy"} {
		mock := &mockFetcher{postsResp: []byte(`[]`)}
		svc := NewTagService(mock, vocab.Default())

		if _, err := svc.SearchPosts(context.Background(), query, 5, ""); err != nil {
			t.Fatalf("query %q: unexpected error: %v", query, err)
		}
		if mock.postsCalls != 1 {
			t.Errorf("query %q: expected exactly one posts request, got %d", query, mock.postsCalls)
		}
		if mock.lastPostsArg != query {
			t.Errorf("query %q: the query must go out as typed, got %q", query, mock.lastPostsArg)
		}
		if mock.lastPostsLim != 5 {
			t.Errorf("query %q: limit must be forwarded, got %d", query, mock.lastPostsLim)
		}
	}
}

// TestSearchPosts_OverLimitCostsNoRequest pins the request cost of the
// over-limit path: a query above the 6-tag cap is rejected locally, before
// any request of any kind — no page, no name lookup — whether or not it
// names a candidate.
func TestSearchPosts_OverLimitCostsNoRequest(t *testing.T) {
	tests := []struct {
		name  string
		query string
		store *vocab.Store
	}{
		{"space-split pairs", "blue hair long hair blue eyes red eyes", vocab.Default()},
		{"no candidate", "t1 t2 t3 t4 t5 t6 t7 t8", vocab.Default()},
		{"nil vocabulary", "blue hair long hair blue eyes red eyes", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{postsResp: []byte(`[]`)}
			svc := NewTagService(mock, tt.store)

			if _, err := svc.SearchPosts(context.Background(), tt.query, 5, ""); err == nil {
				t.Fatal("expected the too-many-tags error, got nil")
			}
			if mock.postsCalls != 0 || mock.lastNamesArg != nil || mock.aliasCalls != 0 {
				t.Errorf("an over-limit query must be rejected before any request, got %d page(s), lookup %v, %d alias lookup(s)",
					mock.postsCalls, mock.lastNamesArg, mock.aliasCalls)
			}
		})
	}
}

// pairTestStore is a synthetic vocabulary carrying exactly the names the
// candidate table needs, so the logic tests do not depend on the pinned
// snapshots. The colon-bearing name is there for the classification rule: a
// token is skipped only when its key is a known metatag, so a space-split
// colon-bearing tag name is a candidate like any other — but only a join the
// store really carries can prove it.
var pairTestStore = vocab.NewStore(vocab.VocabData{Name: "test", CSV: []byte(
	"tag_id,name,category,count\n" +
		"1,blue_hair,0,100\n" +
		"2,long_hair,0,100\n" +
		"3,gold_footwear,0,100\n" +
		"4,red_eyes,0,100\n" +
		"5,kafka_(honkai:_star_rail),4,100\n")})

// TestTagPairCandidates_DetectionRules pins the candidate rule on a
// synthetic vocabulary: adjacent plain tokens only, joined name must be
// carried, query order, deduped, capped, and never a known metatag.
func TestTagPairCandidates_DetectionRules(t *testing.T) {
	pair := func(pairText, joins string) tagPairCandidate {
		return tagPairCandidate{Pair: pairText, Joins: joins}
	}
	tests := []struct {
		name  string
		query string
		store *vocab.Store
		want  []tagPairCandidate
	}{
		{"empty query", "", pairTestStore, nil},
		{"single token", "blue", pairTestStore, nil},
		{
			name:  "adjacent pair joining to a vocabulary name",
			query: "blue hair",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("blue hair", "blue_hair")},
		},
		{"pair joining to nothing", "blue xyzzy", pairTestStore, nil},
		{"reversed pair is a different name", "hair blue", pairTestStore, nil},
		{
			name:  "only adjacent tokens are joined",
			query: "blue long hair",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("long hair", "long_hair")},
		},
		{
			name:  "query order",
			query: "gold footwear blue hair",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("gold footwear", "gold_footwear"), pair("blue hair", "blue_hair")},
		},
		{"a metatag is never joined", "blue rating:g hair", pairTestStore, nil},
		{
			// The key before the colon is not a known metatag, so this is a
			// real tag name split by a space — exactly the dead end the
			// diagnostic exists for. (Until the classification became exact,
			// any colon suppressed the candidate.)
			name:  "a colon-bearing tag name split by a space is a candidate",
			query: "kafka_(honkai: star_rail)",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("kafka_(honkai: star_rail)", "kafka_(honkai:_star_rail)")},
		},
		{
			name:  "a metatag does not hide the next pair",
			query: "rating:g blue hair",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("blue hair", "blue_hair")},
		},
		{
			name:  "order: is a metatag",
			query: "blue hair order:random",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("blue hair", "blue_hair")},
		},
		{
			name:  "repeated pair is named once, first pair text kept",
			query: "blue hair Blue Hair",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("blue hair", "blue_hair")},
		},
		{
			name:  "typed pair text, lowercase join",
			query: "Blue Hair",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("Blue Hair", "blue_hair")},
		},
		{
			name:  "any whitespace separates tokens",
			query: "blue\tHair\nlong hair",
			store: pairTestStore,
			want:  []tagPairCandidate{pair("blue Hair", "blue_hair"), pair("long hair", "long_hair")},
		},
		{
			name:  "capped at three",
			query: "blue hair long hair gold footwear red eyes",
			store: pairTestStore,
			want: []tagPairCandidate{
				pair("blue hair", "blue_hair"),
				pair("long hair", "long_hair"),
				pair("gold footwear", "gold_footwear"),
			},
		},
		{"nil store yields none", "blue hair", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tagPairCandidates(tt.query, tt.store); !slices.Equal(got, tt.want) {
				t.Errorf("tagPairCandidates(%q):\n got: %+v\nwant: %+v", tt.query, got, tt.want)
			}
		})
	}
}

// TestTagPairCandidates_EmbeddedVocabularyCarriesTheDocumentedExample pins
// the data fact the documented example and the online smoke rest on: the
// embedded snapshots carry blue_hair and neither carries bare blue or hair,
// so "blue hair" yields exactly one candidate and no token is a name itself.
func TestTagPairCandidates_EmbeddedVocabularyCarriesTheDocumentedExample(t *testing.T) {
	store := vocab.Default()
	if _, ok := store.Lookup("blue_hair"); !ok {
		t.Fatal("blue_hair must be carried by the embedded vocabularies")
	}
	for _, bare := range []string{"blue", "hair"} {
		if _, ok := store.Lookup(bare); ok {
			t.Errorf("bare %q must not be a vocabulary name: the example would then name a token, not a join", bare)
		}
	}

	want := []tagPairCandidate{{Pair: "blue hair", Joins: "blue_hair"}}
	if got := tagPairCandidates("blue hair", store); !slices.Equal(got, want) {
		t.Errorf("tagPairCandidates(\"blue hair\") = %+v, want %+v", got, want)
	}
}

// TestSearchPosts_SpaceSplitPairNamedOnEmptyAnswer pins the diagnostic on
// the silent dead end: "blue hair" is two 0-post tokens (a space inside one
// tag name is the multi-tag syntax), so the live answer is empty, and the
// payload must name the vocabulary-backed join — posts first, then
// possible_tags. A colon-bearing tag name split by a space is the same dead
// end: the key before the colon is not a known metatag, so nothing suppresses
// the candidate.
func TestSearchPosts_SpaceSplitPairNamedOnEmptyAnswer(t *testing.T) {
	// Both embedded snapshots carry blue_hair and neither carries bare blue
	// or hair (verified live), so the pair yields exactly one candidate.
	if got, want := searchPostsJSON(t, "blue hair", vocab.Default()), `{"posts":[],"possible_tags":["blue_hair"]}`; got != want {
		t.Errorf("payload:\n got: %s\nwant: %s", got, want)
	}
	if got, want := searchPostsJSON(t, "kafka_(honkai: star_rail)", pairTestStore), `{"posts":[],"possible_tags":["kafka_(honkai:_star_rail)"]}`; got != want {
		t.Errorf("payload:\n got: %s\nwant: %s", got, want)
	}
}

// TestSearchPosts_SpaceSplitDiagnosticIndependentOfTagsMode pins that
// tags_mode reshapes only the per-post tag lists: with no post to reshape,
// every mode answers the same diagnostic bytes.
func TestSearchPosts_SpaceSplitDiagnosticIndependentOfTagsMode(t *testing.T) {
	const want = `{"posts":[],"possible_tags":["blue_hair"]}`
	for _, mode := range []string{"", "full", "none", "diff"} {
		if got := searchPostsJSON(t, "blue hair", vocab.Default(), mode); got != want {
			t.Errorf("mode %q: payload:\n got: %s\nwant: %s", mode, got, want)
		}
	}
}

// TestSearchPosts_NoSpaceSplitDiagnosticOnDeadQueries pins the other side of
// the contract: a genuinely dead query stays the bare {"posts":[]}, with no
// possible_tags key at all, so the key's presence always means something.
func TestSearchPosts_NoSpaceSplitDiagnosticOnDeadQueries(t *testing.T) {
	tests := []struct {
		name  string
		query string
		store *vocab.Store
	}{
		{"single token", "blue", vocab.Default()},
		{"made-up token", "not_a_real_tag_xyz", vocab.Default()},
		{"pair that joins to nothing", "blue xyzzy", vocab.Default()},
		{"a metatag is never joined", "blue rating:g hair", vocab.Default()},
		{"nil vocabulary store", "blue hair", nil},
		// A query that needs a scan names its candidates too; that answer
		// and its scan fields are pinned by TestScan_EmptyAnswerKeepsPossibleTags.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := searchPostsJSON(t, tt.query, tt.store); got != `{"posts":[]}` {
				t.Errorf("a dead query must stay bare, got: %s", got)
			}
		})
	}
}

// TestSearchPosts_NoSpaceSplitDiagnosticOnNonEmptyAnswer pins that the
// diagnostic is tied to the empty answer, not to the query: the same
// space-split pair answered with posts carries no possible_tags key, so a
// valid query's payload stays byte-identical.
func TestSearchPosts_NoSpaceSplitDiagnosticOnNonEmptyAnswer(t *testing.T) {
	mock := &mockFetcher{postsResp: []byte(`[{"id":100,"rating":"e","preview_file_url":"https://danbooru.donmai.us/sample.jpg"}]`)}
	svc := NewTagService(mock, vocab.Default())

	res, err := svc.SearchPosts(context.Background(), "blue hair", 5, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"posts":[{"id":100,"rating":"e","score":0,"fav_count":0,"tags":{"copyright":[],"artist":[],"character":[],"general":[],"meta":[]},"preview_file_url":"https://danbooru.donmai.us/sample.jpg","large_file_url":""}]}`
	if string(raw) != want {
		t.Errorf("payload:\n got: %s\nwant: %s", raw, want)
	}
}

// TestSearchPosts_OverLimitNamesSpaceSplitCandidates pins the hint on the
// over-limit path: space-split pairs burn tag slots, so a query of seven or
// more tokens can be over the cap although its joined tags would not be. The
// error names each candidate — query order, deduped, at most three — as
// segments appended to the message.
func TestSearchPosts_OverLimitNamesSpaceSplitCandidates(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "one candidate",
			query: "blue hair t1 t2 t3 t4 t5",
			want:  overLimitText(7, "; 'blue hair' may be one tag: blue_hair"),
		},
		{
			name:  "several candidates keep query order",
			query: "blue hair long hair t1 t2 t3",
			want: overLimitText(7,
				"; 'blue hair' may be one tag: blue_hair",
				"; 'long hair' may be one tag: long_hair"),
		},
		{
			name:  "capped at three",
			query: "blue hair long hair blue eyes red eyes",
			want: overLimitText(8,
				"; 'blue hair' may be one tag: blue_hair",
				"; 'long hair' may be one tag: long_hair",
				"; 'blue eyes' may be one tag: blue_eyes"),
		},
		{
			name:  "a repeated pair is named once",
			query: "blue hair Blue Hair t1 t2 t3",
			want:  overLimitText(7, "; 'blue hair' may be one tag: blue_hair"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{postsResp: []byte(`[]`)}
			svc := NewTagService(mock, vocab.Default())

			_, err := svc.SearchPosts(context.Background(), tt.query, 5, "")
			if err == nil {
				t.Fatal("expected the too-many-tags error, got nil")
			}
			if err.Error() != tt.want {
				t.Errorf("message:\n got: %s\nwant: %s", err.Error(), tt.want)
			}
		})
	}
}
