package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"danbooru-tag-mcp/internal/service"
	"danbooru-tag-mcp/internal/tools"
	"danbooru-tag-mcp/internal/vocab"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// chainPost is one post of the fake listing, carrying every field the
// service consumes.
type chainPost struct {
	ID       int    `json:"id"`
	Rating   string `json:"rating"`
	Score    int    `json:"score"`
	FavCount int    `json:"fav_count"`
	Preview  string `json:"preview_file_url"`
	Large    string `json:"large_file_url"`
	General  string `json:"tag_string_general"`
}

// chainURL builds the fake CDN URL of a post's file.
func chainURL(kind string, id int) string {
	return fmt.Sprintf("https://cdn.donmai.us/%s/%d.jpg", kind, id)
}

// chainPost1..4 are the corpus, four posts over four tags:
//
//	id 1: 1girl arm_up sword solo   id 2: 1girl arm_up solo
//	id 3: arm_up sword solo         id 4: 1girl arm_up sword
//
// so "1girl arm_up sword" holds posts 1 and 4, and its scan pair "arm_up
// sword" lists 1, 3 and 4 — one post the local filter has to drop.
var chainPosts = []chainPost{
	{ID: 1, Rating: "g", Score: 10, FavCount: 2, Preview: chainURL("preview", 1), Large: chainURL("large", 1), General: "1girl arm_up sword solo"},
	{ID: 2, Rating: "s", Score: 8, FavCount: 1, Preview: chainURL("preview", 2), Large: chainURL("large", 2), General: "1girl arm_up solo"},
	{ID: 3, Rating: "q", Score: 6, FavCount: 0, Preview: chainURL("preview", 3), Large: chainURL("large", 3), General: "arm_up sword solo"},
	{ID: 4, Rating: "e", Score: 4, FavCount: 3, Preview: chainURL("preview", 4), Large: chainURL("large", 4), General: "1girl arm_up sword"},
}

// chainPostWant is one corpus post as the tools layer marshals it (the
// historical shape: every category list, full tags mode).
func chainPostWant(p chainPost) string {
	general := strings.Split(p.General, " ")
	quoted := make([]string, 0, len(general))
	for _, name := range general {
		quoted = append(quoted, `"`+name+`"`)
	}
	return fmt.Sprintf(`{"id":%d,"rating":%q,"score":%d,"fav_count":%d,"tags":{"copyright":[],"artist":[],"character":[],"general":[%s],"meta":[]},"preview_file_url":%q,"large_file_url":%q}`,
		p.ID, p.Rating, p.Score, p.FavCount, strings.Join(quoted, ","), p.Preview, p.Large)
}

// chainRow is one row of the tag table behind /tags.json.
type chainRow struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Category  int    `json:"category"`
	PostCount int    `json:"post_count"`
}

// chainRows is the tag table: the live post counts of the four tags, which
// make arm_up and sword the rarest two of any query over them.
var chainRows = map[string]int{"1girl": 8717103, "arm_up": 317380, "sword": 385398, "solo": 6000000}

// fakeAnswer, when set on a fakeDanbooru, replaces the 200 answer of every
// request the account's limit would accept — the way another 422 or an
// outage hits a query that is within the limit. A request over the limit is
// always refused with the tag-limit 422.
type fakeAnswer struct {
	status      int
	contentType string
	body        string
}

// fakeTerms counts the terms Danbooru counts for a query: every token but
// the free metatags (rating:, status:, id:).
func fakeTerms(tags string) int {
	n := 0
	for _, tok := range strings.Fields(tags) {
		free := false
		for _, key := range []string{"rating:", "status:", "id:"} {
			if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(tok, "-")), key) {
				free = true
			}
		}
		if !free {
			n++
		}
	}
	return n
}

// fakeDanbooru is the slice of Danbooru a search_posts call reaches, behind
// one httptest server. It enforces the term rule — more terms than the
// account's limit answers the live tag-limit 422 — and otherwise answers
// from the corpus above: /posts.json lists the posts carrying every positive
// tag of the query, /tags.json resolves names to rows, /counts/posts.json
// counts the same listing. It records the tags of every query so a test can
// prove what reached the wire and in which order.
type fakeDanbooru struct {
	limit int // terms the account answers as-is

	// answer, when set, replaces the 200 of every request within the limit.
	answer *fakeAnswer

	t *testing.T

	mu      sync.Mutex
	posts   []string // tags of every /posts.json request, in order
	lookups []string // search[name_normalize] of every /tags.json request
	counts  []string // tags of every /counts/posts.json request
}

// ServeHTTP answers one request; see fakeDanbooru.
func (f *fakeDanbooru) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch r.URL.Path {
	case "/posts.json":
		tags := q.Get("tags")
		f.mu.Lock()
		f.posts = append(f.posts, tags)
		f.mu.Unlock()
		if fakeTerms(tags) > f.limit {
			f.refuse(w)
			return
		}
		if f.answer != nil {
			f.reply(w, *f.answer)
			return
		}
		body, err := json.Marshal(f.listing(tags))
		if err != nil {
			f.t.Errorf("marshal listing: %v", err)
		}
		f.reply(w, fakeAnswer{status: http.StatusOK, contentType: "application/json", body: string(body)})

	case "/counts/posts.json":
		tags := q.Get("tags")
		f.mu.Lock()
		f.counts = append(f.counts, tags)
		f.mu.Unlock()
		if fakeTerms(tags) > f.limit {
			f.refuse(w)
			return
		}
		if f.answer != nil {
			f.reply(w, *f.answer)
			return
		}
		f.reply(w, fakeAnswer{status: http.StatusOK, contentType: "application/json",
			body: fmt.Sprintf(`{"counts":{"posts":%d}}`, len(f.listing(tags)))})

	case "/tags.json":
		names := strings.Split(q.Get("search[name_normalize]"), ",")
		f.mu.Lock()
		f.lookups = append(f.lookups, q.Get("search[name_normalize]"))
		f.mu.Unlock()
		rows := make([]chainRow, 0, len(names))
		for i, name := range names {
			if count, ok := chainRows[name]; ok {
				rows = append(rows, chainRow{ID: i + 1, Name: name, PostCount: count})
			}
		}
		body, err := json.Marshal(rows)
		if err != nil {
			f.t.Errorf("marshal rows: %v", err)
		}
		f.reply(w, fakeAnswer{status: http.StatusOK, contentType: "application/json", body: string(body)})

	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// listing returns the corpus posts a query holds: those whose tag list
// carries every positive tag of the query, in id order.
func (f *fakeDanbooru) listing(tags string) []chainPost {
	var want []string
	for _, tok := range strings.Fields(tags) {
		if !strings.HasPrefix(tok, "-") && !strings.Contains(tok, ":") {
			want = append(want, tok)
		}
	}
	var held []chainPost
	for _, p := range chainPosts {
		tagsOf := strings.Split(p.General, " ")
		has := true
		for _, tag := range want {
			if !slices.Contains(tagsOf, tag) {
				has = false
			}
		}
		if has {
			held = append(held, p)
		}
	}
	return held
}

// refuse answers the live tag-limit 422 of the account.
func (f *fakeDanbooru) refuse(w http.ResponseWriter) {
	f.reply(w, fakeAnswer{status: http.StatusUnprocessableEntity, contentType: "application/json",
		body: fmt.Sprintf(`{"success":false,"error":"PostQuery::TagLimitError","message":"You cannot search for more than %d tags at a time."}`, f.limit)})
}

// reply writes one answer.
func (f *fakeDanbooru) reply(w http.ResponseWriter, a fakeAnswer) {
	w.Header().Set("Content-Type", a.contentType)
	w.WriteHeader(a.status)
	w.Write([]byte(a.body))
}

// requests returns the recorded tags of every /posts.json request, every
// batch lookup and every /counts/posts.json request, each in order.
func (f *fakeDanbooru) requests() (posts, lookups, counts []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.posts), slices.Clone(f.lookups), slices.Clone(f.counts)
}

// newTagLimitChainClient wires the real api -> service -> tools chain to a
// fake Danbooru the way main wires it: an authenticated client gets the
// credentialed direct tag limit, an anonymous one the default. Credentials
// are set explicitly both ways, so the shell's DANBOORU_LOGIN /
// DANBOORU_API_KEY can neither turn the anonymous client into a credentialed
// one nor leak into the recorded queries.
func newTagLimitChainClient(t *testing.T, fake *fakeDanbooru, credentialed bool) *client.Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	login, key := "", ""
	var opts []service.Option
	if credentialed {
		login, key = "probe_user", "probe_key"
		opts = append(opts, service.WithUpstreamTagLimit(6))
	}
	apiClient := NewClient(WithBaseURL(srv.URL), WithReqGap(0), WithCredentials(login, key))
	apiClient.backoffs = []time.Duration{0, 0}
	if got := apiClient.Authenticated(); got != credentialed {
		t.Fatalf("Authenticated() = %v, want %v", got, credentialed)
	}
	svc := service.NewTagService(apiClient, vocab.Default(), opts...)

	s := server.NewMCPServer("test", "0.0.0.0", server.WithToolCapabilities(true))
	tools.Register(s, svc)

	cli, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("in-process client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if err := cli.Start(context.Background()); err != nil {
		t.Fatalf("client start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "tag-limit-chain-test", Version: "0.0.1"}
	if _, err := cli.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return cli
}

// TestSearchPosts_TagLimitFallback runs the whole api -> service -> tools
// chain against a fake Danbooru that enforces the term rule, and pins the
// routing of the tag-limit refusal:
//
//   - a credentialed-style account (limit 6) whose real limit is 3 gets its
//     3-tag query answered directly, in one verbatim request; its 4-tag query
//     is tried directly exactly once, refused, and answered by the scan — in
//     list mode and in count mode;
//   - an anonymous client never sends a 3-tag query as-is: the scan answers
//     without a direct attempt, while a 2-tag query still goes out verbatim;
//   - any other error on the direct attempt is returned untouched, with no
//     scan: another 422 stays the plain post_search_failed result, an outage
//     the uniform marked result.
func TestSearchPosts_TagLimitFallback(t *testing.T) {
	const (
		threeTags = "1girl arm_up sword"
		fourTags  = "1girl arm_up sword solo"
	)
	// "1girl arm_up sword" holds posts 1 and 4; with solo only post 1.
	directThree := `{"posts":[` + chainPostWant(chainPosts[0]) + `,` + chainPostWant(chainPosts[3]) + `]}`
	scanThree := `{"posts":[` + chainPostWant(chainPosts[0]) + `,` + chainPostWant(chainPosts[3]) + `],"upstream_query":"arm_up sword","scanned":3,"complete":true}`
	scanFour := `{"posts":[` + chainPostWant(chainPosts[0]) + `],"upstream_query":"arm_up sword","scanned":3,"complete":true}`

	t.Run("credentialed account: direct within the real limit, scan above it", func(t *testing.T) {
		fake := &fakeDanbooru{limit: 3, t: t}
		cli := newTagLimitChainClient(t, fake, true)

		// Three terms are within the account's real limit: one verbatim
		// request, the historical payload, no scan field.
		res, text := callToolText(t, cli, "search_posts", map[string]any{"tags": threeTags})
		if text != directThree {
			t.Errorf("direct answer:\n got: %s\nwant: %s", text, directThree)
		}
		if res.IsError || strings.Contains(text, "upstream_query") {
			t.Errorf("a direct answer is a plain result without scan fields, got IsError %v: %s", res.IsError, text)
		}
		if posts, lookups, counts := fake.requests(); !slices.Equal(posts, []string{threeTags}) || len(lookups) != 0 || len(counts) != 0 {
			t.Errorf("a direct answer costs one verbatim request:\n got: posts %v, lookups %v, counts %v", posts, lookups, counts)
		}

		// Four terms: the direct attempt is refused, then the scan answers.
		// The pair is "arm_up sword" (the rarest two); its listing holds
		// post 3, which lacks 1girl and solo.
		res, text = callToolText(t, cli, "search_posts", map[string]any{"tags": fourTags})
		if text != scanFour {
			t.Errorf("fallback answer:\n got: %s\nwant: %s", text, scanFour)
		}
		if res.IsError || strings.Contains(text, "upstream_unavailable") || strings.Contains(text, "post_search_failed") {
			t.Errorf("a fallback answer is a plain success, got IsError %v: %s", res.IsError, text)
		}
		posts, lookups, _ := fake.requests()
		wantPosts := []string{threeTags, fourTags, "arm_up sword"}
		if !slices.Equal(posts, wantPosts) || !slices.Equal(lookups, []string{"1girl,arm_up,sword,solo"}) {
			t.Errorf("exactly one direct attempt, then one scan:\n got: posts %v, lookups %v\nwant: posts %v and one lookup of the four names", posts, lookups, wantPosts)
		}

		// Count mode falls back the same way, from the counts endpoint.
		res, text = callToolText(t, cli, "search_posts", map[string]any{"tags": fourTags, "count_only": true})
		if want := `{"query":"` + fourTags + `","total":1,"scanned":3,"complete":true}`; text != want {
			t.Errorf("fallback count:\n got: %s\nwant: %s", text, want)
		}
		if res.IsError {
			t.Errorf("a fallback count is a plain result, got: %+v", res)
		}
		posts, _, counts := fake.requests()
		if !slices.Equal(counts, []string{fourTags}) || len(posts) != len(wantPosts)+1 {
			t.Errorf("count mode tries the direct count exactly once and scans once, got counts %v and %d page requests", counts, len(posts))
		}
	})

	t.Run("anonymous client never sends a 3-tag query as-is", func(t *testing.T) {
		fake := &fakeDanbooru{limit: 2, t: t}
		cli := newTagLimitChainClient(t, fake, false)

		res, text := callToolText(t, cli, "search_posts", map[string]any{"tags": threeTags})
		if text != scanThree {
			t.Errorf("anonymous answer:\n got: %s\nwant: %s", text, scanThree)
		}
		if res.IsError {
			t.Errorf("the scan answers a plain result, got: %+v", res)
		}
		posts, lookups, counts := fake.requests()
		if !slices.Equal(posts, []string{"arm_up sword"}) || !slices.Equal(lookups, []string{"1girl,arm_up,sword"}) || len(counts) != 0 {
			t.Errorf("the scan's pair is the only query that reaches Danbooru:\n got: posts %v, lookups %v, counts %v", posts, lookups, counts)
		}

		// Within the anonymous limit a query still goes out verbatim: "arm_up
		// sword" holds posts 1, 3 and 4.
		_, text = callToolText(t, cli, "search_posts", map[string]any{"tags": "arm_up sword"})
		want := `{"posts":[` + chainPostWant(chainPosts[0]) + `,` + chainPostWant(chainPosts[2]) + `,` + chainPostWant(chainPosts[3]) + `]}`
		if text != want {
			t.Errorf("a 2-tag answer:\n got: %s\nwant: %s", text, want)
		}
		posts, lookups, _ = fake.requests()
		if !slices.Equal(posts, []string{"arm_up sword", "arm_up sword"}) || len(lookups) != 1 {
			t.Errorf("the 2-tag query goes out verbatim once, with no further lookup:\n got: posts %v, lookups %v", posts, lookups)
		}
	})

	for _, tt := range []struct {
		name        string
		answer      fakeAnswer
		want        string
		wantIsError bool
	}{
		{
			name:   "another 422",
			answer: fakeAnswer{status: http.StatusUnprocessableEntity, contentType: "application/json", body: `{"error":"query rejected"}`},
			want:   `{"error":"post_search_failed","message":"danbooru 422: {\"error\":\"query rejected\"}"}`,
		},
		{
			name:        "outage",
			answer:      fakeAnswer{status: http.StatusServiceUnavailable, contentType: "text/html", body: `<html><head><title>Downbooru</title></head><body>maintenance</body></html>`},
			want:        `{"error":"upstream_unavailable","message":"Danbooru is not answering right now: danbooru 503 (Downbooru). All tools of this server are affected; retry later."}`,
			wantIsError: true,
		},
	} {
		t.Run(tt.name+" on the direct attempt comes back untouched, with no scan", func(t *testing.T) {
			fake := &fakeDanbooru{limit: 3, t: t, answer: &tt.answer}
			cli := newTagLimitChainClient(t, fake, true)

			// Three terms, within the account's limit: Danbooru fails on a
			// query it would accept, which is no reason to scan.
			res, text := callToolText(t, cli, "search_posts", map[string]any{"tags": threeTags})
			if text != tt.want {
				t.Errorf("payload:\n got: %s\nwant: %s", text, tt.want)
			}
			if res.IsError != tt.wantIsError {
				t.Errorf("IsError = %v, want %v", res.IsError, tt.wantIsError)
			}
			attempts := 1
			if tt.answer.status >= 500 {
				attempts = 1 + maxRetries
			}
			if posts, lookups, _ := fake.requests(); len(posts) != attempts || len(lookups) != 0 || !slices.Equal(posts, slices.Repeat([]string{threeTags}, attempts)) {
				t.Errorf("no scan after this class: want %d attempt(s) of the verbatim query and no lookup, got posts %v and lookups %v", attempts, posts, lookups)
			}
		})
	}
}
