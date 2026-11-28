package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/vocab"
)

// directPathFixture is the one post the direct-path pins answer with: every
// field the service consumes, so the pinned bytes prove the whole projection.
// Its general tags name two of the direct queries, which lets diff mode show
// its subtraction in the same table.
const directPathFixture = `{
	"id": 900, "rating": "e", "score": 42, "fav_count": 7,
	"preview_file_url": "https://cdn.donmai.us/preview/x.jpg",
	"large_file_url": "https://cdn.donmai.us/large/x.jpg",
	"tag_string_copyright": "touhou",
	"tag_string_artist": "kantoku",
	"tag_string_character": "hakurei_reimu",
	"tag_string_general": "arm_up sword solo",
	"tag_string_meta": "highres"
}`

// directPathPost is directPathFixture after the service's projection.
const directPathPost = `{"id":900,"rating":"e","score":42,"fav_count":7,"tags":{"copyright":["touhou"],"artist":["kantoku"],"character":["hakurei_reimu"],"general":["arm_up","sword","solo"],"meta":["highres"]},"preview_file_url":"https://cdn.donmai.us/preview/x.jpg","large_file_url":"https://cdn.donmai.us/large/x.jpg"}`

// directQueries are the posts queries an anonymous client sends to Danbooru
// as-is: at most two terms, a term being a content token or a metatag that
// counts (order:, user:, pool:, ...). The free metatags (rating:, status:,
// id:, ...) ride at no cost, and an unknown-key colon token (foo:bar) is a
// content token. Every row is a direct query under the old 2-tag cap too,
// which is what makes the pins below comparable across the change.
var directQueries = []struct {
	name  string
	query string
}{
	{"single tag", "touhou"},
	{"two content tags", "arm_up sword"},
	{"tag and an exclusion", "arm_up -1boy"},
	{"free metatag rides at no cost", "arm_up rating:g"},
	{"several free metatags ride at no cost", "arm_up status:any id:>100"},
	{"counting metatag takes the second slot", "arm_up order:score"},
	{"content tags plus a free metatag", "arm_up sword status:any"},
	{"unknown-key colon token is a content token", "arm_up foo:bar"},
}

// scanFieldKeys are the JSON keys only a split (scanned) answer carries.
var scanFieldKeys = []string{`"upstream_query"`, `"scanned"`, `"complete"`}

// assertNoScanFields fails the test when a marshaled payload carries any of
// the scan fields: a direct answer must stay byte-identical to the pre-scan
// shape.
func assertNoScanFields(t *testing.T, payload string) {
	t.Helper()
	for _, key := range scanFieldKeys {
		if strings.Contains(payload, key) {
			t.Errorf("a direct answer must not carry %s, got: %s", key, payload)
		}
	}
}

// TestSearchPosts_DirectPathByteIdentical pins the bytes that must not
// change when a query fits Danbooru's direct limit. It was written and run
// green on the pre-scan code, so every expectation here is the historical
// payload: posts first, possible_tags on an empty space-split answer, each
// tags_mode's own shape, and never a scan field.
func TestSearchPosts_DirectPathByteIdentical(t *testing.T) {
	const (
		answered = `{"posts":[` + directPathPost + `]}`
		empty    = `{"posts":[]}`
	)
	tests := []struct {
		name  string
		query string
		mode  string
		store *vocab.Store
		body  string
		want  string
	}{
		{"single tag", "touhou", "", nil, "[" + directPathFixture + "]", answered},
		{"explicit full is the default", "touhou", "full", nil, "[" + directPathFixture + "]", answered},
		{"two content tags, empty answer", "arm_up sword", "", nil, `[]`, empty},
		{"tag and an exclusion", "arm_up -1boy", "", nil, "[" + directPathFixture + "]", answered},
		{"free metatag", "arm_up rating:g", "", nil, "[" + directPathFixture + "]", answered},
		{"counting metatag", "arm_up order:score", "", nil, "[" + directPathFixture + "]", answered},
		{"colon token", "arm_up foo:bar", "", nil, "[" + directPathFixture + "]", answered},
		{
			name:  "none mode drops the tag lists only",
			query: "arm_up sword",
			mode:  "none",
			body:  "[" + directPathFixture + "]",
			want:  `{"posts":[{"id":900,"rating":"e","score":42,"fav_count":7,"preview_file_url":"https://cdn.donmai.us/preview/x.jpg","large_file_url":"https://cdn.donmai.us/large/x.jpg"}]}`,
		},
		{
			name:  "diff mode subtracts the query's content tags",
			query: "arm_up sword",
			mode:  "diff",
			body:  "[" + directPathFixture + "]",
			want:  `{"posts":[{"id":900,"rating":"e","score":42,"fav_count":7,"tags":{"copyright":["touhou"],"artist":["kantoku"],"character":["hakurei_reimu"],"general":["solo"],"meta":["highres"]},"preview_file_url":"https://cdn.donmai.us/preview/x.jpg","large_file_url":"https://cdn.donmai.us/large/x.jpg"}]}`,
		},
		{
			name:  "space-split diagnostic rides the empty answer",
			query: "blue hair",
			store: vocab.Default(),
			body:  `[]`,
			want:  `{"posts":[],"possible_tags":["blue_hair"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{postsResp: []byte(tt.body)}
			svc := NewTagService(mock, tt.store)

			res, err := svc.SearchPosts(context.Background(), tt.query, 7, tt.mode)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("payload:\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestSearchPosts_DirectQueryCostsOneVerbatimRequest pins the cost and the
// shape of every direct query in every tags_mode: exactly one posts request
// carrying the query as typed and the limit as given — no name lookup, no
// alias request, no count, no scan — and a payload without the scan fields.
func TestSearchPosts_DirectQueryCostsOneVerbatimRequest(t *testing.T) {
	for _, q := range directQueries {
		for _, mode := range []string{"", "full", "none", "diff"} {
			t.Run(q.name+"/mode="+mode, func(t *testing.T) {
				mock := &mockFetcher{postsResp: []byte("[" + directPathFixture + "]")}
				svc := NewTagService(mock, nil)

				res, err := svc.SearchPosts(context.Background(), q.query, 7, mode)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if mock.postsCalls != 1 {
					t.Errorf("a direct query must cost exactly one posts request, got %d", mock.postsCalls)
				}
				if mock.lastPostsArg != q.query {
					t.Errorf("the query must go out verbatim, got %q", mock.lastPostsArg)
				}
				if mock.lastPostsLim != 7 {
					t.Errorf("limit must be forwarded, got %d", mock.lastPostsLim)
				}
				if mock.lastPostsPage != 1 {
					t.Errorf("a direct query asks for the first page, got page %d", mock.lastPostsPage)
				}
				if mock.lastNamesArg != nil || mock.aliasCalls != 0 || mock.countCalls != 0 {
					t.Errorf("a direct query must look up, alias-resolve and count nothing, got names=%v alias=%d count=%d",
						mock.lastNamesArg, mock.aliasCalls, mock.countCalls)
				}
				got, err := json.Marshal(res)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				assertNoScanFields(t, string(got))
			})
		}
	}
}

// TestCountPosts_DirectPathByteIdentical is the count-mode twin of the list
// pin: a query within the direct limit costs exactly one count request with
// the tags verbatim, and the payload stays {"query":...,"total":...} — no
// scan fields, no posts request, no lookup.
func TestCountPosts_DirectPathByteIdentical(t *testing.T) {
	for _, q := range append(directQueries, struct{ name, query string }{"space-split pair", "blue hair"}) {
		t.Run(q.name, func(t *testing.T) {
			mock := &mockFetcher{countResp: []byte(`{"counts":{"posts":317377}}`)}
			svc := NewTagService(mock, vocab.Default())

			res, err := svc.CountPosts(context.Background(), q.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mock.countCalls != 1 {
				t.Errorf("a direct count must cost exactly one count request, got %d", mock.countCalls)
			}
			if mock.lastCountArg != q.query {
				t.Errorf("the query must go out verbatim, got %q", mock.lastCountArg)
			}
			if mock.postsCalls != 0 || mock.lastNamesArg != nil || mock.aliasCalls != 0 {
				t.Errorf("a direct count must not list posts or look anything up, got posts=%d names=%v alias=%d",
					mock.postsCalls, mock.lastNamesArg, mock.aliasCalls)
			}
			got, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// json.Marshal escapes '>' as \u003e; the handler marshals the
			// same way, so this is the wire bytes.
			if want := `{"query":"` + strings.ReplaceAll(q.query, ">", `\u003e`) + `","total":317377}`; string(got) != want {
				t.Errorf("payload:\n got: %s\nwant: %s", got, want)
			}
		})
	}
}
