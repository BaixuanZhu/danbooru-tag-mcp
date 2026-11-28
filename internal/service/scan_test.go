package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"danbooru-tag-mcp/internal/app"
	"danbooru-tag-mcp/internal/vocab"
)

// tagLimitRefused stands in for the api layer's tag-limit classification the
// way upstreamMarkedError stands in for its upstream marker: Error() is the
// shrunken 422 text, Is matches only the app.ErrTagLimitExceeded sentinel.
type tagLimitRefused struct{}

func (e *tagLimitRefused) Error() string {
	return `danbooru 422: {"success":false,"error":"PostQuery::TagLimitError","message":"You cannot search for more than 2 tags at a time."}`
}
func (e *tagLimitRefused) Is(target error) bool { return target == app.ErrTagLimitExceeded }

// scanCall is one recorded posts request.
type scanCall struct {
	tags  string
	limit int
	page  int
}

// scanReply is one scripted /posts.json answer: a body or an error.
type scanReply struct {
	body []byte
	err  error
}

// page scripts a page body.
func page(body []byte) scanReply { return scanReply{body: body} }

// refusedByTagLimit scripts the classified tag-limit refusal.
func refusedByTagLimit() scanReply { return scanReply{err: &tagLimitRefused{}} }

// scanFetcher scripts the three requests a scan can make — the one batch
// lookup, the alias lookups and the page requests — and records every one.
// rows is what the lookup answers. A page request is answered from byPage by
// its page number when that has an entry, otherwise from responses in
// request order (the last one repeats); byPage is what concurrent callers,
// indistinguishable by request order, must use. The other endpoints keep the
// plain mock's behavior. Safe for concurrent callers: the log is guarded.
type scanFetcher struct {
	*mockFetcher

	rows      []Tag
	responses []scanReply
	byPage    map[int]scanReply // page number -> answer
	aliases   map[string]string // antecedent -> consequent of an active alias
	aliasErr  error             // returned by every alias lookup instead of a body
	aliasRaw  []byte            // returned verbatim by every alias lookup when set

	mu       sync.Mutex
	lookups  int
	names    []string
	calls    []scanCall
	aliasFor []string
}

// newScanFetcher builds a scan fetcher whose lookup answers rows and whose
// pages come from responses, in request order.
func newScanFetcher(rows []Tag, responses ...scanReply) *scanFetcher {
	return &scanFetcher{mockFetcher: &mockFetcher{}, rows: rows, responses: responses}
}

// FetchTagsByNames records the lookup and answers the rows.
func (f *scanFetcher) FetchTagsByNames(ctx context.Context, names []string) ([]byte, error) {
	f.mu.Lock()
	f.lookups++
	f.names = append([]string(nil), names...)
	f.mu.Unlock()
	body, err := json.Marshal(f.rows)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// FetchPosts records the call and answers from the script.
func (f *scanFetcher) FetchPosts(ctx context.Context, tags string, limit, page int) ([]byte, error) {
	f.mu.Lock()
	call := len(f.calls)
	f.calls = append(f.calls, scanCall{tags: tags, limit: limit, page: page})
	f.mu.Unlock()
	if r, ok := f.byPage[page]; ok {
		return r.body, r.err
	}
	if len(f.responses) == 0 {
		return []byte(`[]`), nil
	}
	r := f.responses[min(call, len(f.responses)-1)]
	return r.body, r.err
}

// FetchAlias records the lookup and answers the scripted active alias, or
// none, for the name.
func (f *scanFetcher) FetchAlias(ctx context.Context, name string) ([]byte, error) {
	f.mu.Lock()
	f.aliasFor = append(f.aliasFor, name)
	f.mu.Unlock()
	switch {
	case f.aliasErr != nil:
		return nil, f.aliasErr
	case f.aliasRaw != nil:
		return f.aliasRaw, nil
	}
	if consequent, ok := f.aliases[name]; ok {
		return json.Marshal([]danbooruAliasResponse{{AntecedentName: name, ConsequentName: consequent, Status: "active"}})
	}
	return []byte(`[]`), nil
}

// log returns what the fetcher recorded: the lookups with their names, the
// posts requests in order, and the names the alias lookups asked for.
func (f *scanFetcher) log() (lookups int, names []string, calls []scanCall, aliasFor []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups, f.names, append([]scanCall(nil), f.calls...), append([]string(nil), f.aliasFor...)
}

// pages returns the recorded posts requests only.
func (f *scanFetcher) pages() []scanCall {
	_, _, calls, _ := f.log()
	return calls
}

// scanPost renders one light post: its id, its rating and its general tags.
func scanPost(id int, general string) string {
	return fmt.Sprintf(`{"id":%d,"rating":"g","tag_string_general":%q}`, id, general)
}

// scanPage renders a /posts.json body of the given posts; no argument is the
// empty page.
func scanPage(posts ...string) []byte {
	return []byte("[" + strings.Join(posts, ",") + "]")
}

// fullPage renders a full page: n posts numbered from first, all tagged with
// tags.
func fullPage(first, n int, tags string) []byte {
	posts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		posts = append(posts, scanPost(first+i, tags))
	}
	return scanPage(posts...)
}

// postIDs lists the ids of an answer, in answer order.
func postIDs(res *PostsResult) []int {
	ids := make([]int, 0, len(res.Posts))
	for _, p := range res.Posts {
		ids = append(ids, p.ID)
	}
	return ids
}

// scanRows are the rows the scan tests answer the batch lookup with: the live
// post counts of the documented example (2026-11-28). arm_up is the rarest,
// so "1girl arm_up sword" sends "arm_up sword" upstream.
var scanRows = []Tag{
	{Name: "1girl", PostCount: 8717103},
	{Name: "arm_up", PostCount: 317380},
	{Name: "sword", PostCount: 385398},
}

// commonRows is scanRows plus three more common tags, for the queries that
// need more than three positives with a known post count (a token without a
// row counts as 0 posts and would take a pair slot).
var commonRows = append([]Tag{
	{Name: "solo", PostCount: 6000000},
	{Name: "smile", PostCount: 2000000},
	{Name: "long_hair", PostCount: 1500000},
}, scanRows...)

// exampleListing is the upstream listing of "arm_up sword" the example tests
// page through: posts 1, 3 and 5 carry 1girl.
var exampleListing = scanPage(
	scanPost(1, "arm_up sword 1girl solo"),
	scanPost(2, "arm_up sword solo"),
	scanPost(3, "arm_up sword 1girl 1boy"),
	scanPost(4, "arm_up sword"),
	scanPost(5, "arm_up sword 1girl smile"),
)

// pairRequest is the first page request of a scan over scanRows.
func pairRequest(tags string) scanCall { return scanCall{tags: tags, limit: scanPageSize, page: 1} }

// assertRequests fails unless the fetcher saw exactly wantLookups lookups —
// naming names when there is one — and exactly wantCalls posts requests.
func assertRequests(t *testing.T, f *scanFetcher, wantLookups int, names []string, wantCalls ...scanCall) {
	t.Helper()
	lookups, gotNames, calls, _ := f.log()
	if lookups != wantLookups {
		t.Errorf("lookups = %d, want %d", lookups, wantLookups)
	}
	if wantLookups == 1 && !slices.Equal(gotNames, names) {
		t.Errorf("lookup names:\n got: %v\nwant: %v", gotNames, names)
	}
	if !slices.Equal(calls, wantCalls) {
		t.Errorf("posts requests:\n got: %v\nwant: %v", calls, wantCalls)
	}
}

// mustSearchPosts runs one list-mode query and fails the test on an error.
func mustSearchPosts(t *testing.T, svc *TagService, query string, limit int, modes ...string) *PostsResult {
	t.Helper()
	mode := ""
	if len(modes) > 0 {
		mode = modes[0]
	}
	res, err := svc.SearchPosts(context.Background(), query, limit, mode)
	if err != nil {
		t.Fatalf("query %q: unexpected error: %v", query, err)
	}
	return res
}

// mustCountPosts runs one count-mode query and fails the test on an error.
func mustCountPosts(t *testing.T, svc *TagService, query string) *PostCount {
	t.Helper()
	res, err := svc.CountPosts(context.Background(), query)
	if err != nil {
		t.Fatalf("count %q: unexpected error: %v", query, err)
	}
	return res
}

// mustJSON marshals a payload or fails the test.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// assertPayload compares a marshaled payload to the expected wire bytes.
func assertPayload(t *testing.T, got any, want string) {
	t.Helper()
	if raw := mustJSON(t, got); raw != want {
		t.Errorf("payload:\n got: %s\nwant: %s", raw, want)
	}
}

// assertComplete fails unless complete is present with the wanted value.
func assertComplete(t *testing.T, res *PostsResult, want bool) {
	t.Helper()
	if res.Complete == nil {
		t.Fatalf("a split answer must carry complete, got nil (payload %s)", mustJSON(t, res))
	}
	if *res.Complete != want {
		t.Errorf("complete = %v, want %v", *res.Complete, want)
	}
}

// TestScan_PicksRarestPairAndCanonicalNames pins the pair: the two rarest
// positives — by the row's post_count, ties by query order — go upstream
// under their canonical row names, in query order; every other positive is a
// local filter under its canonical name too, so a tag typed in another case
// still matches. The lookup is one request; pages are of 200 from page 1.
func TestScan_PicksRarestPairAndCanonicalNames(t *testing.T) {
	t.Run("the two rarest tags go upstream", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(exampleListing))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword", 5)
		assertRequests(t, f, 1, []string{"1girl", "arm_up", "sword"}, pairRequest("arm_up sword"))
		if res.UpstreamQuery != "arm_up sword" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "arm_up sword")
		}
		// Posts 2 and 4 lack 1girl: that filter is local.
		if got, want := postIDs(res), []int{1, 3, 5}; !slices.Equal(got, want) {
			t.Errorf("kept posts:\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("the pair keeps query order, not rarity order", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(exampleListing))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl sword arm_up", 5)
		// arm_up (317k) is rarer than sword (385k) but was typed second.
		if res.UpstreamQuery != "sword arm_up" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "sword arm_up")
		}
	})

	t.Run("a tag typed upper-case is canonical in the query and in the filter", func(t *testing.T) {
		// Here arm_up is the common tag, so it is the local filter — and the
		// posts carry it lower-case, as Danbooru does.
		rows := []Tag{
			{Name: "sword", PostCount: 100},
			{Name: "holding_weapon", PostCount: 200},
			{Name: "arm_up", PostCount: 900},
		}
		f := newScanFetcher(rows, page(scanPage(
			scanPost(1, "sword holding_weapon arm_up"),
			scanPost(2, "sword holding_weapon"),
		)))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "sword holding_weapon ARM_UP", 5)
		if res.UpstreamQuery != "sword holding_weapon" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "sword holding_weapon")
		}
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("the filter must match the canonical name arm_up:\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("a tag Danbooru carries no row for is the rarest", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(scanPage()))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up not_a_real_tag_xyz", 5)
		// No row counts as 0 posts, so the missing tag takes a pair slot;
		// Danbooru then answers an empty listing, which is also its answer to
		// the whole query.
		if res.UpstreamQuery != "arm_up not_a_real_tag_xyz" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "arm_up not_a_real_tag_xyz")
		}
		if len(res.Posts) != 0 {
			t.Errorf("an empty listing is an empty answer, got %+v", res.Posts)
		}
		assertComplete(t, res, true)
	})

	t.Run("the same tag twice is sent once", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(exampleListing))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "arm_up arm_up sword 1girl", 5)
		// Both pair slots belong to the two rarest distinct tags; a pair of
		// "arm_up arm_up" would list every arm_up post to filter on sword.
		if res.UpstreamQuery != "arm_up sword" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "arm_up sword")
		}
	})
}

// TestScan_ClassifiesColonTokensByRowPresence pins what a colon token is: a
// tag when Danbooru carries that exact name, sent and filtered under the
// row's name; otherwise a pass-through token that rides the upstream query
// verbatim and is never a local filter — a post's tag list could not match
// it, so filtering on it would drop every post.
func TestScan_ClassifiesColonTokensByRowPresence(t *testing.T) {
	rows := []Tag{
		{Name: "firefly_(honkai:_star_rail)", PostCount: 30000},
		{Name: "1girl", PostCount: 8717103},
		{Name: "solo", PostCount: 6000000},
	}
	listing := scanPage(
		scanPost(1, "firefly_(honkai:_star_rail) solo 1girl"),
		scanPost(2, "firefly_(honkai:_star_rail) solo"),
	)

	t.Run("a colon tag Danbooru carries is a positive tag", func(t *testing.T) {
		f := newScanFetcher(rows, page(listing))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "firefly_(honkai:_star_rail) 1girl solo", 5)
		assertRequests(t, f, 1, []string{"firefly_(honkai:_star_rail)", "1girl", "solo"},
			pairRequest("firefly_(honkai:_star_rail) solo"))
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("a three-positive scan keeps the posts carrying 1girl only:\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("a colon token without a row is passed through, never filtered", func(t *testing.T) {
		f := newScanFetcher(rows, page(scanPage(
			scanPost(1, "solo 1girl"),
			scanPost(2, "solo 1girl smile"),
		)))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "foo:bar 1girl solo", 5)
		assertRequests(t, f, 1, []string{"foo:bar", "1girl", "solo"}, pairRequest("1girl solo foo:bar"))
		if got, want := postIDs(res), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("no post carries foo:bar and none may be dropped for it:\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("a negated colon token follows the same rule", func(t *testing.T) {
		// With a row it is a local exclusion under the row's name...
		f := newScanFetcher(rows, page(scanPage(
			scanPost(1, "solo 1girl"),
			scanPost(2, "solo 1girl firefly_(honkai:_star_rail)"),
		)))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl solo -firefly_(honkai:_star_rail)", 5)
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("the post carrying the excluded tag must go:\n got: %v\nwant: %v", got, want)
		}

		// ...and without one it rides the upstream query, sign included.
		f = newScanFetcher(rows, page(scanPage(scanPost(1, "solo 1girl"))))
		svc = NewTagService(f, nil)
		res = mustSearchPosts(t, svc, "1girl solo -foo:bar", 5)
		if res.UpstreamQuery != "1girl solo -foo:bar" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "1girl solo -foo:bar")
		}
	})
}

// TestPostMatches table-tests the pure local filter: a post is kept iff its
// tag set carries every must-have tag and none of the must-not tags.
func TestPostMatches(t *testing.T) {
	set := map[string]bool{"arm_up": true, "sword": true, "1girl": true}
	tests := []struct {
		name              string
		mustHave, mustNot []string
		want              bool
	}{
		{"no filter keeps everything", nil, nil, true},
		{"present must-have", []string{"1girl"}, nil, true},
		{"absent must-have", []string{"solo"}, nil, false},
		{"one absent among several must-haves", []string{"1girl", "solo"}, nil, false},
		{"absent must-not", []string{"arm_up"}, []string{"solo"}, true},
		{"present must-not", nil, []string{"sword"}, false},
		{"one present among several must-nots", nil, []string{"solo", "1girl"}, false},
		{"both sides satisfied", []string{"arm_up", "1girl"}, []string{"solo", "smile"}, true},
		{"both sides violated", []string{"solo"}, []string{"sword"}, false},
		{"the filter is exact, not a prefix", []string{"arm"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postMatches(set, tt.mustHave, tt.mustNot); got != tt.want {
				t.Errorf("postMatches(%v, %v) = %v, want %v", tt.mustHave, tt.mustNot, got, tt.want)
			}
		})
	}
}

// TestPostTagSet pins the tag set the filter reads: the union of the five
// tag_string_* fields, lowercased, meta tags included.
func TestPostTagSet(t *testing.T) {
	var p danbooruPost
	if err := json.Unmarshal([]byte(`{
		"tag_string_copyright": "Touhou",
		"tag_string_artist": "kantoku",
		"tag_string_character": "hakurei_reimu",
		"tag_string_general": "1girl Solo",
		"tag_string_meta": "highres"
	}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"touhou", "kantoku", "hakurei_reimu", "1girl", "solo", "highres"}
	got := postTagSet(p)
	if len(got) != len(want) {
		t.Errorf("postTagSet = %v, want exactly %v", got, want)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("postTagSet lacks %q: %v", name, got)
		}
	}
}

// TestScan_FiltersLocally pins the filter on a whole scan: the remaining
// positive must be present, the exclusion must be absent, the kept posts keep
// the upstream order, and the metatags ride the upstream query verbatim —
// nothing is injected, nothing is rewritten.
func TestScan_FiltersLocally(t *testing.T) {
	f := newScanFetcher(scanRows, page(scanPage(
		scanPost(9, "arm_up sword 1girl"),
		scanPost(4, "arm_up sword"),
		scanPost(7, "arm_up sword 1girl 1boy"),
		scanPost(2, "arm_up sword 1girl"),
	)))
	svc := NewTagService(f, nil)

	res := mustSearchPosts(t, svc, "rating:g 1girl -1boy arm_up sword", 5)
	assertRequests(t, f, 1, []string{"1girl", "1boy", "arm_up", "sword"}, pairRequest("arm_up sword rating:g"))
	if got, want := postIDs(res), []int{9, 2}; !slices.Equal(got, want) {
		t.Errorf("kept posts (1girl present, 1boy absent, upstream order):\n got: %v\nwant: %v", got, want)
	}
}

// TestScan_UpstreamQueryIsPairThenVerbatimTokens pins the shape of
// upstream_query: the pair's canonical names first, then every metatag and
// pass-through token in its original relative order, each exactly as typed.
func TestScan_UpstreamQueryIsPairThenVerbatimTokens(t *testing.T) {
	t.Run("one counting metatag leaves one tag", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(scanPage()))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "order:Score 1girl foo:bar arm_up sword Rating:g", 5)
		if want := "arm_up order:Score foo:bar Rating:g"; res.UpstreamQuery != want {
			t.Errorf("upstream_query:\n got: %q\nwant: %q", res.UpstreamQuery, want)
		}
	})

	t.Run("free metatags leave the pair at two", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(scanPage()))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "status:any 1girl arm_up sword -foo:bar id:<5 rating:g", 5)
		if want := "arm_up sword status:any -foo:bar id:<5 rating:g"; res.UpstreamQuery != want {
			t.Errorf("upstream_query:\n got: %q\nwant: %q", res.UpstreamQuery, want)
		}
	})
}

// TestScan_StopsAtLimitAndAtListingEnd pins the two ways an answer is
// complete, and the request each one costs: the list mode stops as soon as
// the limit's worth of matches is collected — those are the first posts of
// the answer, in order — and a short page ends the listing.
func TestScan_StopsAtLimitAndAtListingEnd(t *testing.T) {
	t.Run("limit reached inside the first page", func(t *testing.T) {
		// A full page whose 1girl posts are ids 1 and 3: nothing after the
		// second match is examined.
		posts := make([]string, 0, scanPageSize)
		for i := 1; i <= scanPageSize; i++ {
			tags := "arm_up sword"
			if i == 1 || i == 3 {
				tags = "arm_up sword 1girl"
			}
			posts = append(posts, scanPost(i, tags))
		}
		f := newScanFetcher(scanRows, page(scanPage(posts...)))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword", 2)
		assertRequests(t, f, 1, []string{"1girl", "arm_up", "sword"}, pairRequest("arm_up sword"))
		if got, want := postIDs(res), []int{1, 3}; !slices.Equal(got, want) {
			t.Errorf("the first two matches, in order:\n got: %v\nwant: %v", got, want)
		}
		if res.Scanned != 3 {
			t.Errorf("the scan must stop at the second match: scanned = %d, want 3", res.Scanned)
		}
		assertComplete(t, res, true)
	})

	t.Run("listing ends under the limit", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(scanPage(
			scanPost(5, "arm_up sword 1girl"),
			scanPost(6, "arm_up sword"),
			scanPost(7, "arm_up sword"),
		)))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword", 5)
		if got, want := postIDs(res), []int{5}; !slices.Equal(got, want) {
			t.Errorf("kept posts:\n got: %v\nwant: %v", got, want)
		}
		if res.Scanned != 3 {
			t.Errorf("scanned = %d, want 3", res.Scanned)
		}
		assertComplete(t, res, true)
		assertRequests(t, f, 1, []string{"1girl", "arm_up", "sword"}, pairRequest("arm_up sword"))
	})

	t.Run("a listing that ends exactly on a page boundary", func(t *testing.T) {
		// 200 posts on page 1 and nothing on page 2: only the empty page
		// proves the listing ended.
		f := newScanFetcher(scanRows, page(fullPage(1, scanPageSize, "arm_up sword")), page(scanPage()))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword", 5)
		if res.Scanned != scanPageSize {
			t.Errorf("scanned = %d, want %d", res.Scanned, scanPageSize)
		}
		assertComplete(t, res, true)
		if calls := f.pages(); len(calls) != 2 || calls[1].page != 2 {
			t.Errorf("the scan must ask for page 2, got %+v", calls)
		}
	})
}

// TestScan_PageCapReportsIncomplete pins the bound: five full pages are
// requested — pages 1 to 5, no sixth — and when they hold fewer matches than
// the limit, the posts are a true prefix of the answer and complete is false.
func TestScan_PageCapReportsIncomplete(t *testing.T) {
	// One 1girl post per full page, at the page's first position.
	responses := make([]scanReply, 0, maxScanPages+1)
	wantCalls := make([]scanCall, 0, maxScanPages)
	for p := 1; p <= maxScanPages; p++ {
		posts := make([]string, 0, scanPageSize)
		for i := 0; i < scanPageSize; i++ {
			tags := "arm_up sword"
			if i == 0 {
				tags = "arm_up sword 1girl"
			}
			posts = append(posts, scanPost((p-1)*scanPageSize+i+1, tags))
		}
		responses = append(responses, page(scanPage(posts...)))
		wantCalls = append(wantCalls, pairRequest("arm_up sword"))
		wantCalls[len(wantCalls)-1].page = p
	}
	// A sixth full page of matches would be served if asked; it must not be.
	responses = append(responses, page(fullPage(1001, scanPageSize, "arm_up sword 1girl")))
	f := newScanFetcher(scanRows, responses...)
	svc := NewTagService(f, nil)

	res := mustSearchPosts(t, svc, "1girl arm_up sword", 20)
	assertRequests(t, f, 1, []string{"1girl", "arm_up", "sword"}, wantCalls...)
	if got, want := postIDs(res), []int{1, 201, 401, 601, 801}; !slices.Equal(got, want) {
		t.Errorf("kept posts, a true prefix of the answer:\n got: %v\nwant: %v", got, want)
	}
	if res.Scanned != scanPageSize*maxScanPages {
		t.Errorf("scanned = %d, want %d", res.Scanned, scanPageSize*maxScanPages)
	}
	assertComplete(t, res, false)
}

// TestScan_DedupesRandomPages pins the dedupe: order:random re-draws every
// page, so the same post can come back twice, and it must be answered and
// counted once. scanned counts the distinct posts. Both pages are full, so
// the listing ends only on the empty third page.
func TestScan_DedupesRandomPages(t *testing.T) {
	// The posts carrying 1girl are ids 1, 2 and 201..218; a post's tags are
	// fixed, whichever draw returns it. Page 1 holds ids 1..200; page 2 is a
	// re-draw that begins with 100 posts of page 1 (1 and 2 among them) and
	// goes on with 201..300; page 3 is empty.
	ids := func(from, to int) []int {
		list := make([]int, 0, to-from+1)
		for id := from; id <= to; id++ {
			list = append(list, id)
		}
		return list
	}
	draw := func(ids ...int) []byte {
		posts := make([]string, 0, len(ids))
		for _, id := range ids {
			tags := "arm_up sword"
			if id <= 2 || (id >= 201 && id <= 218) {
				tags = "arm_up sword 1girl"
			}
			posts = append(posts, scanPost(id, tags))
		}
		return scanPage(posts...)
	}
	responses := []scanReply{
		page(draw(ids(1, 200)...)),
		page(draw(append(ids(1, 100), ids(201, 300)...)...)),
		page(scanPage()),
	}
	wantIDs := append([]int{1, 2}, ids(201, 218)...) // the twenty 1girl posts, first-seen order

	t.Run("list mode answers each post once", func(t *testing.T) {
		f := newScanFetcher(scanRows, responses...)
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword order:random", 20)
		// 1 and 2 lead page 2 again: answering them twice would reach the
		// limit at id 216 and cut the list short.
		if got := postIDs(res); !slices.Equal(got, wantIDs) {
			t.Errorf("each post once, in first-seen order:\n got: %v\nwant: %v", got, wantIDs)
		}
		// Page 1 in full, then page 2 up to the twentieth distinct post.
		if res.Scanned != scanPageSize+18 {
			t.Errorf("scanned counts the distinct posts examined: got %d, want %d", res.Scanned, scanPageSize+18)
		}
		assertComplete(t, res, true)
	})

	t.Run("count mode counts each post once", func(t *testing.T) {
		f := newScanFetcher(scanRows, responses...)
		svc := NewTagService(f, nil)

		res := mustCountPosts(t, svc, "1girl arm_up sword order:random")
		// Counting 1 and 2 twice would answer 22; counting every row of page
		// 2 would answer 400 scanned.
		if res.Total != len(wantIDs) || res.Scanned != 300 {
			t.Errorf("distinct posts only: total = %d, scanned = %d; want %d and 300", res.Total, res.Scanned, len(wantIDs))
		}
		if res.Complete == nil || !*res.Complete {
			t.Errorf("the empty third page ends the listing, so complete, got %v", res.Complete)
		}
	})
}

// TestScan_BudgetShrinksWithCountingMetatags pins R11 on the scan's own
// query: order: costs a term, so an anonymous scan with it sends one tag
// instead of two — the rarest — and the tag it displaces becomes a local
// filter. rating: costs nothing and leaves the pair at two.
func TestScan_BudgetShrinksWithCountingMetatags(t *testing.T) {
	posts := scanPage(
		scanPost(1, "arm_up sword 1girl"),
		scanPost(2, "arm_up solo"),
		scanPost(3, "arm_up 1girl"),
	)

	t.Run("no counting metatag: two tags", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(posts))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword", 5)
		if res.UpstreamQuery != "arm_up sword" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "arm_up sword")
		}
	})

	t.Run("free metatags leave the pair at two", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(posts))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword rating:g status:any", 5)
		if want := "arm_up sword rating:g status:any"; res.UpstreamQuery != want {
			t.Errorf("upstream_query:\n got: %q\nwant: %q", res.UpstreamQuery, want)
		}
	})

	t.Run("one counting metatag leaves room for one tag", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(posts))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword order:score", 5)
		if want := "arm_up order:score"; res.UpstreamQuery != want {
			t.Errorf("upstream_query:\n got: %q\nwant: %q", res.UpstreamQuery, want)
		}
		// sword and 1girl are both local now: post 2 lacks 1girl and post 3
		// lacks sword.
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("the displaced tag is filtered locally:\n got: %v\nwant: %v", got, want)
		}
	})
}

// TestScan_RefusesWhenMetatagsUseTheBudget pins the first local refusal: when
// counting metatags take every term, the scan has no room for a tag and the
// query is refused before any request — no lookup, no page — in both modes,
// with an error that names the metatags and stays unmarked.
func TestScan_RefusesWhenMetatagsUseTheBudget(t *testing.T) {
	const query = "arm_up sword order:score user:albert"
	const want = "cannot split the query: the metatags order:score user:albert leave no term for a tag (each costs one of the 2 terms the upstream query may have); drop tags or use only free metatags (rating:, status:, id: ...)"

	for name, call := range map[string]func(*TagService) error{
		"list mode": func(svc *TagService) error {
			_, err := svc.SearchPosts(context.Background(), query, 5, "")
			return err
		},
		"count mode": func(svc *TagService) error {
			_, err := svc.CountPosts(context.Background(), query)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newScanFetcher(scanRows, page(exampleListing))
			err := call(NewTagService(f, nil))
			if err == nil {
				t.Fatal("expected the budget refusal, got nil")
			}
			if err.Error() != want {
				t.Errorf("message:\n got: %s\nwant: %s", err, want)
			}
			if errors.Is(err, app.ErrUpstreamUnavailable) {
				t.Errorf("a local refusal must not carry the upstream marker, got: %v", err)
			}
			assertRequests(t, f, 0, nil)
			if _, _, _, aliasFor := f.log(); len(aliasFor) != 0 {
				t.Errorf("the refusal must cost no request, got alias lookups %v", aliasFor)
			}
		})
	}

	// Free metatags never use the budget, however many there are: three
	// content tokens need a scan and it still sends two tags.
	f := newScanFetcher(scanRows, page(exampleListing))
	res := mustSearchPosts(t, NewTagService(f, nil), "1girl arm_up sword rating:g status:any id:>100 favcount:>5", 5)
	if want := "arm_up sword rating:g status:any id:>100 favcount:>5"; res.UpstreamQuery != want {
		t.Errorf("upstream_query:\n got: %q\nwant: %q", res.UpstreamQuery, want)
	}

	// An account with a higher limit has a bigger budget: two counting
	// metatags leave room for the pair, where the anonymous budget left none.
	// Five content tokens and two counting metatags are seven terms, past
	// that account's limit of 6, so no direct attempt comes first.
	f = newScanFetcher(commonRows, page(exampleListing))
	res = mustSearchPosts(t, NewTagService(f, nil, WithUpstreamTagLimit(6)), "1girl arm_up sword solo smile order:score user:albert", 5)
	if want := "arm_up sword order:score user:albert"; res.UpstreamQuery != want {
		t.Errorf("credentialed upstream_query:\n got: %q\nwant: %q", res.UpstreamQuery, want)
	}
}

// TestScan_RefusesWithoutPositiveTag pins the second local refusal: a scan
// with nothing to send upstream — only exclusions and metatags — is refused
// with an unmarked error and no page request. When the only candidates are
// unresolved colon tokens, the one lookup is what proves there is no tag;
// otherwise the refusal costs no request at all.
func TestScan_RefusesWithoutPositiveTag(t *testing.T) {
	const want = "cannot split the query: there is no tag to send upstream (only exclusions and metatags); add a tag, or use fewer tags so Danbooru answers the query directly"

	for _, query := range []string{"-sword -1girl -solo", "-sword -1girl order:score", "-sword -1girl -solo rating:g status:any"} {
		for name, call := range map[string]func(*TagService) error{
			"list mode": func(svc *TagService) error {
				_, err := svc.SearchPosts(context.Background(), query, 5, "")
				return err
			},
			"count mode": func(svc *TagService) error {
				_, err := svc.CountPosts(context.Background(), query)
				return err
			},
		} {
			t.Run(query+"/"+name, func(t *testing.T) {
				f := newScanFetcher(scanRows, page(exampleListing))
				err := call(NewTagService(f, nil))
				if err == nil || err.Error() != want {
					t.Fatalf("message:\n got: %v\nwant: %s", err, want)
				}
				if errors.Is(err, app.ErrUpstreamUnavailable) {
					t.Errorf("a local refusal must not carry the upstream marker, got: %v", err)
				}
				assertRequests(t, f, 0, nil)
			})
		}
	}

	// Exclusions alone within the direct limit are sent verbatim: the direct
	// path never looks at what the query contains.
	f := newScanFetcher(scanRows, page(scanPage(scanPost(1, "arm_up"))))
	mustSearchPosts(t, NewTagService(f, nil), "-sword -1girl", 5)
	assertRequests(t, f, 0, nil, scanCall{tags: "-sword -1girl", limit: 5, page: 1})

	// A colon token Danbooru has no row for is not a tag to send: the lookup
	// runs, no page does.
	f = newScanFetcher(scanRows, page(exampleListing))
	_, err := NewTagService(f, nil).SearchPosts(context.Background(), "foo:bar -sword -1girl", 5, "")
	if err == nil || err.Error() != want {
		t.Fatalf("message:\n got: %v\nwant: %s", err, want)
	}
	assertRequests(t, f, 1, []string{"foo:bar", "sword", "1girl"})
}

// TestScan_NarrowsOnTagLimitRefusal pins the fallback inside a scan: a page
// request Danbooru refuses for its tag limit — an account whose real limit is
// below the configured one — restarts the scan once with one tag fewer, the
// less rare of the pair, which becomes a local filter. A second refusal
// returns the error. Nothing else narrows: another 422, an outage, a
// cancellation and an unparsable page come back after their one request. The
// mechanism is scripted on the anonymous service, whose scans skip the direct
// attempt, and once on a credentialed one with a query beyond its limit.
func TestScan_NarrowsOnTagLimitRefusal(t *testing.T) {
	t.Run("one narrowing, then the scan runs", func(t *testing.T) {
		f := newScanFetcher(scanRows, refusedByTagLimit(), page(scanPage(
			scanPost(1, "arm_up sword 1girl"),
			scanPost(2, "arm_up solo"),
			scanPost(3, "arm_up 1girl"),
		)))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "rating:g 1girl arm_up sword", 5)
		want := []scanCall{
			{tags: "arm_up sword rating:g", limit: scanPageSize, page: 1},
			{tags: "arm_up rating:g", limit: scanPageSize, page: 1},
		}
		if got := f.pages(); !slices.Equal(got, want) {
			t.Errorf("posts requests:\n got: %v\nwant: %v", got, want)
		}
		if res.UpstreamQuery != "arm_up rating:g" {
			t.Errorf("upstream_query must name the narrowed query, got %q", res.UpstreamQuery)
		}
		// sword is a local filter now: post 3 lacks it.
		if got, wantIDs := postIDs(res), []int{1}; !slices.Equal(got, wantIDs) {
			t.Errorf("the dropped tag is filtered locally:\n got: %v\nwant: %v", got, wantIDs)
		}
		if res.Scanned != 3 {
			t.Errorf("the restarted scan starts over: scanned = %d, want 3", res.Scanned)
		}
		assertComplete(t, res, true)
		if lookups, _, _, _ := f.log(); lookups != 1 {
			t.Errorf("narrowing reuses the one lookup, got %d", lookups)
		}
	})

	t.Run("a second refusal returns the error", func(t *testing.T) {
		f := newScanFetcher(scanRows, refusedByTagLimit(), refusedByTagLimit(), page(exampleListing))
		svc := NewTagService(f, nil)

		res, err := svc.SearchPosts(context.Background(), "1girl arm_up sword", 5, "")
		if res != nil || err == nil {
			t.Fatalf("the second refusal must surface, got res %+v, err %v", res, err)
		}
		if !errors.Is(err, app.ErrTagLimitExceeded) {
			t.Errorf("the refusal must keep its classification, got: %v", err)
		}
		if calls := f.pages(); len(calls) != 2 {
			t.Errorf("a one-tag pair cannot narrow again: want 2 page requests, got %d (%v)", len(calls), calls)
		}
	})

	t.Run("a one-tag pair is not narrowed", func(t *testing.T) {
		// order:score leaves room for one tag only.
		f := newScanFetcher(scanRows, refusedByTagLimit(), page(exampleListing))
		svc := NewTagService(f, nil)

		_, err := svc.SearchPosts(context.Background(), "1girl arm_up sword order:score", 5, "")
		if err == nil || !errors.Is(err, app.ErrTagLimitExceeded) {
			t.Fatalf("the refusal must surface unchanged, got: %v", err)
		}
		if calls := f.pages(); len(calls) != 1 {
			t.Errorf("want exactly 1 page request, got %d (%v)", len(calls), calls)
		}
	})

	t.Run("a credentialed scan beyond its limit narrows the same way", func(t *testing.T) {
		// Six content tokens (the local cap) and one counting metatag are
		// seven terms, past the account's limit of 6: no direct attempt,
		// straight to the scan, whose pair still has room for two tags.
		rows := append([]Tag{}, scanRows...)
		rows = append(rows,
			Tag{Name: "solo", PostCount: 6000000},
			Tag{Name: "smile", PostCount: 2000000},
			Tag{Name: "long_hair", PostCount: 1500000},
			Tag{Name: "blue_eyes", PostCount: 600000},
		)
		f := newScanFetcher(rows, refusedByTagLimit(), page(scanPage(
			scanPost(1, "arm_up sword 1girl solo smile long_hair"),
			scanPost(2, "arm_up 1girl solo smile long_hair"),
		)))
		svc := NewTagService(f, nil, WithUpstreamTagLimit(6))

		res := mustSearchPosts(t, svc, "1girl arm_up sword solo smile long_hair order:score", 5)
		want := []scanCall{
			{tags: "arm_up sword order:score", limit: scanPageSize, page: 1},
			{tags: "arm_up order:score", limit: scanPageSize, page: 1},
		}
		if got := f.pages(); !slices.Equal(got, want) {
			t.Errorf("posts requests:\n got: %v\nwant: %v", got, want)
		}
		if got, wantIDs := postIDs(res), []int{1}; !slices.Equal(got, wantIDs) {
			t.Errorf("the dropped tag is filtered locally:\n got: %v\nwant: %v", got, wantIDs)
		}
		if res.UpstreamQuery != "arm_up order:score" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "arm_up order:score")
		}
	})

	for _, tt := range []struct {
		name     string
		reply    scanReply
		checkErr func(error) bool
	}{
		{"another 422", scanReply{err: errors.New(`danbooru 422: {"error":"query rejected"}`)},
			func(err error) bool {
				return strings.HasPrefix(err.Error(), `danbooru 422: {"error":"query rejected"}`)
			}},
		{"outage", scanReply{err: &upstreamMarkedError{}},
			func(err error) bool { return errors.Is(err, app.ErrUpstreamUnavailable) }},
		{"cancellation", scanReply{err: context.Canceled},
			func(err error) bool { return errors.Is(err, context.Canceled) }},
		{"unparsable page body", page([]byte(`<html>Downbooru</html>`)),
			func(err error) bool { return strings.HasPrefix(err.Error(), "failed to parse posts") }},
	} {
		t.Run(tt.name+" does not narrow", func(t *testing.T) {
			f := newScanFetcher(scanRows, tt.reply, page(exampleListing))
			svc := NewTagService(f, nil)

			res, err := svc.SearchPosts(context.Background(), "1girl arm_up sword", 5, "")
			if res != nil || err == nil {
				t.Fatalf("the error must surface, got res %+v, err %v", res, err)
			}
			if !tt.checkErr(err) {
				t.Errorf("the error must come back untouched, got: %v", err)
			}
			if errors.Is(err, app.ErrTagLimitExceeded) {
				t.Errorf("only the tag-limit refusal narrows, got: %v", err)
			}
			if calls := f.pages(); len(calls) != 1 {
				t.Errorf("no narrowing: want exactly 1 page request, got %d (%v)", len(calls), calls)
			}
		})
	}
}

// TestScan_RenamedLocalFilterUsesCurrentName pins R12: a local filter whose
// row shows 0 posts is a renamed-away name — posts list their tags under the
// current name — so it is resolved through the alias endpoint once and the
// filter follows the alias. Without it the scan would answer "no matches,
// complete" for a query Danbooru answers.
func TestScan_RenamedLocalFilterUsesCurrentName(t *testing.T) {
	renamedRows := func() []Tag {
		return append([]Tag{
			{Name: "1girl", PostCount: 8717103},
			{Name: "arm_up", PostCount: 317380},
			{Name: "sword", PostCount: 385398},
		}, Tag{Name: "sailor_suit", PostCount: 0})
	}

	t.Run("an exclusion follows the alias", func(t *testing.T) {
		f := newScanFetcher(renamedRows(), page(scanPage(
			scanPost(1, "arm_up sword 1girl"),
			scanPost(2, "arm_up sword 1girl sailor"),
		)))
		f.aliases = map[string]string{"sailor_suit": "sailor"}
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "1girl arm_up sword -sailor_suit", 5)
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("the post tagged sailor is what -sailor_suit excludes:\n got: %v\nwant: %v", got, want)
		}
		if _, _, _, aliasFor := f.log(); !slices.Equal(aliasFor, []string{"sailor_suit"}) {
			t.Errorf("exactly one alias lookup for the renamed filter, got %v", aliasFor)
		}
	})

	t.Run("a positive local filter follows the alias", func(t *testing.T) {
		// order:score leaves room for one tag: old_a, the first 0-post token
		// in query order, which Danbooru resolves upstream itself. old_b
		// becomes the local filter.
		rows := []Tag{
			{Name: "old_a", PostCount: 0},
			{Name: "old_b", PostCount: 0},
			{Name: "live", PostCount: 500},
		}
		f := newScanFetcher(rows, page(scanPage(
			scanPost(1, "new_a new_b live"),
			scanPost(2, "new_a live"),
		)))
		f.aliases = map[string]string{"old_a": "new_a", "old_b": "new_b"}
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "old_a old_b live order:score", 5)
		if res.UpstreamQuery != "old_a order:score" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "old_a order:score")
		}
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("the filter on old_b must match under new_b:\n got: %v\nwant: %v", got, want)
		}
		if _, _, _, aliasFor := f.log(); !slices.Equal(aliasFor, []string{"old_b"}) {
			t.Errorf("only the local filter is resolved, never the pair member, got %v", aliasFor)
		}
	})

	t.Run("a tag narrowed out of the pair follows the alias", func(t *testing.T) {
		rows := []Tag{
			{Name: "old_a", PostCount: 0},
			{Name: "old_b", PostCount: 0},
			{Name: "live", PostCount: 500},
		}
		f := newScanFetcher(rows, refusedByTagLimit(), page(scanPage(
			scanPost(1, "new_a new_b live"),
			scanPost(2, "new_a live"),
		)))
		f.aliases = map[string]string{"old_a": "new_a", "old_b": "new_b"}
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "old_a old_b live", 5)
		if res.UpstreamQuery != "old_a" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "old_a")
		}
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("the narrowed-out old_b must match under new_b:\n got: %v\nwant: %v", got, want)
		}
		if _, _, _, aliasFor := f.log(); !slices.Equal(aliasFor, []string{"old_b"}) {
			t.Errorf("the narrowed-out tag is resolved once, got %v", aliasFor)
		}
	})

	t.Run("a 0-post tag in the pair is left to Danbooru", func(t *testing.T) {
		rows := []Tag{
			{Name: "old_a", PostCount: 0},
			{Name: "live", PostCount: 500},
		}
		f := newScanFetcher(rows, page(scanPage(scanPost(1, "new_a live"))))
		f.aliases = map[string]string{"old_a": "new_a"}
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "old_a live order:score", 5)
		if got, want := postIDs(res), []int{1}; !slices.Equal(got, want) {
			t.Errorf("Danbooru resolves the pair's renamed tag itself:\n got: %v\nwant: %v", got, want)
		}
		if _, _, _, aliasFor := f.log(); len(aliasFor) != 0 {
			t.Errorf("a pair member is never alias-resolved, got %v", aliasFor)
		}
	})

	// The alias lookup is an enhancement, so each way it can fail lands on
	// the same fallback: the scan still answers, under the literal name.
	for name, fail := range map[string]func(f *scanFetcher){
		"a failed alias request":   func(f *scanFetcher) { f.aliasErr = &upstreamMarkedError{} },
		"a failed alias 4xx":       func(f *scanFetcher) { f.aliasErr = errors.New("danbooru 404: not found") },
		"an unparsable alias body": func(f *scanFetcher) { f.aliasRaw = []byte(`<html>`) },
	} {
		t.Run(name+" falls back to the literal name", func(t *testing.T) {
			f := newScanFetcher(renamedRows(), page(scanPage(
				scanPost(1, "arm_up sword 1girl"),
				scanPost(2, "arm_up sword 1girl sailor"),
			)))
			f.aliases = map[string]string{"sailor_suit": "sailor"}
			fail(f)
			svc := NewTagService(f, nil)

			// Its failure never fails the call. The literal name matches
			// nothing, so the exclusion filters nothing — the degraded but
			// honest answer.
			res := mustSearchPosts(t, svc, "1girl arm_up sword -sailor_suit", 5)
			if got, want := postIDs(res), []int{1, 2}; !slices.Equal(got, want) {
				t.Errorf("fallback to the literal name:\n got: %v\nwant: %v", got, want)
			}
			if _, _, _, aliasFor := f.log(); !slices.Equal(aliasFor, []string{"sailor_suit"}) {
				t.Errorf("the alias lookup must be tried once, got %v", aliasFor)
			}
		})
	}

	t.Run("a 0-post row without an active alias is simply dead", func(t *testing.T) {
		// Neither 0-post name has an alias here, so each is asked once and
		// then taken literally: a dead exclusion matches no post's tag, so it
		// excludes nothing. Neither is an error.
		rows := append(renamedRows(), Tag{Name: "dead_tag", PostCount: 0})
		f := newScanFetcher(rows, page(scanPage(
			scanPost(1, "arm_up sword 1girl"),
			scanPost(2, "arm_up sword 1girl sailor"),
		)))
		svc := NewTagService(f, nil)

		res := mustSearchPosts(t, svc, "arm_up sword order:score -sailor_suit -dead_tag", 5)
		if got, want := postIDs(res), []int{1, 2}; !slices.Equal(got, want) {
			t.Errorf("a dead exclusion excludes nothing:\n got: %v\nwant: %v", got, want)
		}
		assertComplete(t, res, true)
		if _, _, _, aliasFor := f.log(); !slices.Equal(aliasFor, []string{"sailor_suit", "dead_tag"}) {
			t.Errorf("each 0-post local filter is resolved once, got %v", aliasFor)
		}
	})
}

// TestScan_NoAliasLookupForPairMembersOrLiveFilters pins the cost of R12:
// the alias endpoint is asked only for a local filter with a 0-post row. The
// common case — every local filter live, a pass-through token, an exclusion
// Danbooru carries no row for, a 0-post tag inside the pair — issues no alias
// request at all.
func TestScan_NoAliasLookupForPairMembersOrLiveFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
		rows  []Tag
	}{
		{"live local filters", "1girl arm_up sword -solo", append([]Tag{}, scanRows...)},
		{"a 0-post tag inside the pair", "old_a live order:score", []Tag{{Name: "old_a", PostCount: 0}, {Name: "live", PostCount: 500}}},
		{"a pass-through token", "foo:bar 1girl solo", scanRows},
		{"an exclusion Danbooru carries no row for", "1girl arm_up sword -1boy", scanRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newScanFetcher(tt.rows, page(exampleListing))
			f.aliases = map[string]string{"old_a": "new_a", "arm_up": "x", "1girl": "x", "solo": "x", "1boy": "x"}
			svc := NewTagService(f, nil)

			mustSearchPosts(t, svc, tt.query, 5)
			lookups, _, _, aliasFor := f.log()
			if lookups != 1 || len(aliasFor) != 0 {
				t.Errorf("one lookup and no alias request, got %d lookup(s) and alias lookups %v", lookups, aliasFor)
			}
		})
	}
}

// TestParsePostQuery_CapFiresBeforeAnyRequest pins the cap end to end: more
// than MaxQueryTags content tokens are refused locally, in both modes, with
// the historical message shape and the space-split hint — no lookup, no
// page, no count.
func TestParsePostQuery_CapFiresBeforeAnyRequest(t *testing.T) {
	queries := []struct {
		name  string
		query string
		want  string
	}{
		{"seven plain tags", "a1 a2 a3 a4 a5 a6 a7", tooManyTagsMessage(7)},
		{"six tags, a metatag and a wildcard token", "a1 a2 a3 a4 a5 a6 rating:g a7*", tooManyTagsMessage(7)},
		{"six tags and an unresolved colon token", "a1 a2 a3 a4 a5 a6 foo:bar", tooManyTagsMessage(7)},
		{
			"eight tokens with three candidates",
			"blue hair long hair blue eyes red eyes",
			tooManyTagsMessage(8) + "; 'blue hair' may be one tag: blue_hair; 'long hair' may be one tag: long_hair; 'blue eyes' may be one tag: blue_eyes",
		},
	}
	for _, q := range queries {
		for name, call := range map[string]func(*TagService) error{
			"list mode": func(svc *TagService) error {
				_, err := svc.SearchPosts(context.Background(), q.query, 5, "")
				return err
			},
			"count mode": func(svc *TagService) error {
				_, err := svc.CountPosts(context.Background(), q.query)
				return err
			},
		} {
			t.Run(q.name+"/"+name, func(t *testing.T) {
				f := newScanFetcher(scanRows, page(exampleListing))
				f.mockFetcher.countResp = []byte(`{"counts":{"posts":317377}}`)
				err := call(NewTagService(f, vocab.Default()))
				if err == nil {
					t.Fatal("expected the cap error, got nil")
				}
				if err.Error() != q.want {
					t.Errorf("message:\n got: %s\nwant: %s", err, q.want)
				}
				if errors.Is(err, app.ErrUpstreamUnavailable) {
					t.Errorf("the cap error must not carry the upstream marker, got: %v", err)
				}
				assertRequests(t, f, 0, nil)
				if f.mockFetcher.countCalls != 0 {
					t.Errorf("the cap must fire before any request, got %d count request(s)", f.mockFetcher.countCalls)
				}
			})
		}
	}

	// The cap is the same for an account with a higher direct limit.
	f := newScanFetcher(scanRows, page(exampleListing))
	if _, err := NewTagService(f, nil, WithUpstreamTagLimit(6)).SearchPosts(context.Background(), "a1 a2 a3 a4 a5 a6 a7", 5, ""); err == nil || err.Error() != tooManyTagsMessage(7) {
		t.Errorf("credentialed cap:\n got: %v\nwant: %s", err, tooManyTagsMessage(7))
	}
	assertRequests(t, f, 0, nil)
}

// TestParsePostQuery_WildcardRefusal pins R9 end to end: a wildcard or OR
// token — and, for the same reason, a random: metatag — is refused only when
// the query needs a scan, by a local unmarked error naming the token, with no
// request. A direct query carries such a token to Danbooru untouched.
func TestParsePostQuery_WildcardRefusal(t *testing.T) {
	refused := []struct {
		name  string
		query string
		token string
	}{
		{"wildcard suffix", "arm_up sword 1girl*", "1girl*"},
		{"wildcard prefix", "arm_up sword *1girl", "*1girl"},
		{"OR group", "arm_up sword ~1girl", "~1girl"},
		{"negated wildcard", "arm_up sword -1girl*", "-1girl*"},
		{"negated OR group", "arm_up sword -~1girl", "-~1girl"},
	}
	for _, tt := range refused {
		want := fmt.Sprintf("cannot split the query: '%s' carries a wildcard (*) or OR (~) that only Danbooru can evaluate; drop it, or use fewer tags so Danbooru answers the query directly", tt.token)
		for name, call := range map[string]func(*TagService) error{
			"list mode": func(svc *TagService) error {
				_, err := svc.SearchPosts(context.Background(), tt.query, 5, "")
				return err
			},
			"count mode": func(svc *TagService) error {
				_, err := svc.CountPosts(context.Background(), tt.query)
				return err
			},
		} {
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				f := newScanFetcher(scanRows, page(exampleListing))
				err := call(NewTagService(f, nil))
				if err == nil {
					t.Fatal("expected the wildcard refusal, got nil")
				}
				if err.Error() != want {
					t.Errorf("message:\n got: %s\nwant: %s", err, want)
				}
				if errors.Is(err, app.ErrUpstreamUnavailable) {
					t.Errorf("a local refusal must not carry the upstream marker, got: %v", err)
				}
				assertRequests(t, f, 0, nil)
			})
		}
	}

	// The random: metatag caps the upstream listing (verified live
	// 2026-11-28), which a scan would misread for the end of it.
	const wantRandom = "cannot split the query: the metatag 'random:20' caps the upstream listing, so a local scan could not tell a whole answer from a cut-off one; drop it, or use fewer tags so Danbooru answers the query directly"
	for name, call := range map[string]func(*TagService) error{
		"list mode": func(svc *TagService) error {
			_, err := svc.SearchPosts(context.Background(), "1girl arm_up sword random:20", 5, "")
			return err
		},
		"count mode": func(svc *TagService) error {
			_, err := svc.CountPosts(context.Background(), "1girl arm_up sword random:20")
			return err
		},
	} {
		t.Run("random:/"+name, func(t *testing.T) {
			f := newScanFetcher(scanRows, page(exampleListing))
			err := call(NewTagService(f, nil))
			if err == nil || err.Error() != wantRandom {
				t.Fatalf("message:\n got: %v\nwant: %s", err, wantRandom)
			}
			assertRequests(t, f, 0, nil)
		})
	}

	// Direct queries are untouched, whatever they carry.
	for _, query := range []string{"arm_up* sword", "~arm_up sword", "-arm_up* sword", "arm_up random:20"} {
		f := newScanFetcher(scanRows, page(scanPage(scanPost(1, "arm_up"))))
		mustSearchPosts(t, NewTagService(f, nil), query, 5)
		assertRequests(t, f, 0, nil, scanCall{tags: query, limit: 5, page: 1})
	}
}

// TestCountPosts_OverScan pins count mode over a scan: the same scan without
// the limit stop. The total is exact, and complete true, exactly when the
// listing ended; at the page cap it is a lower bound and complete is false.
func TestCountPosts_OverScan(t *testing.T) {
	t.Run("exact when the listing ends", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(exampleListing))
		svc := NewTagService(f, nil)

		res := mustCountPosts(t, svc, "1girl arm_up sword")
		assertPayload(t, res, `{"query":"1girl arm_up sword","total":3,"scanned":5,"complete":true}`)
		assertRequests(t, f, 1, []string{"1girl", "arm_up", "sword"}, pairRequest("arm_up sword"))
		if f.mockFetcher.countCalls != 0 {
			t.Errorf("a scanned count must not ask /counts/posts.json, got %d request(s)", f.mockFetcher.countCalls)
		}
	})

	t.Run("a lower bound at the page cap", func(t *testing.T) {
		responses := make([]scanReply, 0, maxScanPages)
		for p := 1; p <= maxScanPages; p++ {
			responses = append(responses, page(fullPage((p-1)*scanPageSize+1, scanPageSize, "arm_up sword")))
		}
		f := newScanFetcher(scanRows, responses...)
		svc := NewTagService(f, nil)

		res := mustCountPosts(t, svc, "1girl arm_up sword")
		// No match in five full pages is a lower bound of 0, not an exact 0.
		assertPayload(t, res, `{"query":"1girl arm_up sword","total":0,"scanned":1000,"complete":false}`)
	})

	t.Run("count mode never stops at a limit", func(t *testing.T) {
		// Every post matches: a list mode would stop at its limit, the count
		// must keep counting to the page cap.
		responses := make([]scanReply, 0, maxScanPages+1)
		for p := 1; p <= maxScanPages+1; p++ {
			responses = append(responses, page(fullPage((p-1)*scanPageSize+1, scanPageSize, "arm_up sword 1girl")))
		}
		f := newScanFetcher(scanRows, responses...)
		svc := NewTagService(f, nil)

		res := mustCountPosts(t, svc, "1girl arm_up sword")
		if res.Total != 1000 || res.Complete == nil || *res.Complete {
			t.Errorf("1,000 matches at the page cap is a lower bound: total = %d, complete = %v", res.Total, res.Complete)
		}
		if calls := f.pages(); len(calls) != maxScanPages {
			t.Errorf("the count must scan every page it may, got %d page request(s)", len(calls))
		}
	})

	t.Run("a direct count refused for the tag limit falls back to the scan", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(exampleListing))
		f.mockFetcher.countErr = &tagLimitRefused{}
		svc := NewTagService(f, nil, WithUpstreamTagLimit(6))

		res := mustCountPosts(t, svc, "1girl arm_up sword")
		assertPayload(t, res, `{"query":"1girl arm_up sword","total":3,"scanned":5,"complete":true}`)
		if f.mockFetcher.countCalls != 1 {
			t.Errorf("exactly one direct attempt, got %d count request(s)", f.mockFetcher.countCalls)
		}
		if f.mockFetcher.lastCountArg != "1girl arm_up sword" {
			t.Errorf("the direct attempt must carry the query verbatim, got %q", f.mockFetcher.lastCountArg)
		}
	})
}

// TestSearchPosts_CredentialedDirectFallback pins the routing of an account
// configured above the anonymous limit: a query within the limit goes out
// once, verbatim; one Danbooru refuses for its tag limit is answered by the
// scan — exactly one direct attempt, then one scan; and no other error does.
// An anonymous client never sends more than two terms as-is.
func TestSearchPosts_CredentialedDirectFallback(t *testing.T) {
	t.Run("within the limit: one direct request, no scan fields", func(t *testing.T) {
		f := newScanFetcher(scanRows, page(scanPage(scanPost(1, "arm_up sword 1girl"))))
		svc := NewTagService(f, nil, WithUpstreamTagLimit(6))

		res := mustSearchPosts(t, svc, "1girl arm_up sword", 5)
		assertRequests(t, f, 0, nil, scanCall{tags: "1girl arm_up sword", limit: 5, page: 1})
		assertNoScanFields(t, mustJSON(t, res))
	})

	t.Run("a tag-limit refusal falls back to the scan", func(t *testing.T) {
		f := newScanFetcher(scanRows, refusedByTagLimit(), page(exampleListing))
		svc := NewTagService(f, nil, WithUpstreamTagLimit(6))

		res := mustSearchPosts(t, svc, "1girl arm_up sword", 2)
		want := []scanCall{
			{tags: "1girl arm_up sword", limit: 2, page: 1}, // the direct attempt, as typed
			pairRequest("arm_up sword"),                     // the scan
		}
		assertRequests(t, f, 1, []string{"1girl", "arm_up", "sword"}, want...)
		if got, wantIDs := postIDs(res), []int{1, 3}; !slices.Equal(got, wantIDs) {
			t.Errorf("the scan's answer with the caller's limit:\n got: %v\nwant: %v", got, wantIDs)
		}
		if res.UpstreamQuery != "arm_up sword" {
			t.Errorf("upstream_query = %q, want %q", res.UpstreamQuery, "arm_up sword")
		}
		assertComplete(t, res, true)
	})

	for _, tt := range []struct {
		name string
		err  error
	}{
		{"another 422", errors.New(`danbooru 422: {"error":"query rejected"}`)},
		{"outage", &upstreamMarkedError{}},
		{"cancellation", context.Canceled},
	} {
		t.Run(tt.name+" comes back untouched, with no scan", func(t *testing.T) {
			f := newScanFetcher(scanRows, scanReply{err: tt.err}, page(exampleListing))
			svc := NewTagService(f, nil, WithUpstreamTagLimit(6))

			res, err := svc.SearchPosts(context.Background(), "1girl arm_up sword", 5, "")
			if res != nil || err != tt.err {
				t.Fatalf("the error must come back untouched and unanswered, got res %+v, err %v (want %v)", res, err, tt.err)
			}
			assertRequests(t, f, 0, nil, scanCall{tags: "1girl arm_up sword", limit: 5, page: 1})
		})
	}

	t.Run("an anonymous client never sends more than two terms as-is", func(t *testing.T) {
		for _, query := range []string{"1girl arm_up sword", "arm_up sword order:score", "arm_up sword user:albert", "blue hair order:random"} {
			f := newScanFetcher(scanRows, page(exampleListing))
			svc := NewTagService(f, nil)

			mustSearchPosts(t, svc, query, 5)
			calls := f.pages()
			if len(calls) == 0 {
				t.Fatalf("query %q: expected a scan", query)
			}
			for _, c := range calls {
				if c.tags == query {
					t.Errorf("query %q must not reach Danbooru as-is, got a request with tags %q", query, c.tags)
				}
			}
		}
	})

	t.Run("an anonymous direct attempt refused for the tag limit is not retried", func(t *testing.T) {
		// Two terms by our count; the anonymous limit is exact, so a refusal
		// means the count is off and the scan — built on the same count —
		// could not repair it. The error surfaces as it always did.
		f := newScanFetcher(scanRows, refusedByTagLimit())
		svc := NewTagService(f, nil)

		res, err := svc.SearchPosts(context.Background(), "arm_up sword", 5, "")
		if res != nil || !errors.Is(err, app.ErrTagLimitExceeded) {
			t.Fatalf("the refusal must surface, got res %+v, err %v", res, err)
		}
		assertRequests(t, f, 0, nil, scanCall{tags: "arm_up sword", limit: 5, page: 1})
	})
}

// TestScan_TagsModesReshapeScanAnswers pins that tags_mode reshapes a split
// answer exactly as it reshapes a direct one — none drops the tag lists, diff
// subtracts the query's own tags under their canonical names (pair and local
// filters alike) — while the scan fields stay.
func TestScan_TagsModesReshapeScanAnswers(t *testing.T) {
	raw := `[{"id":1,"rating":"g","score":3,"fav_count":4,"preview_file_url":"https://cdn.donmai.us/preview/p.jpg","large_file_url":"https://cdn.donmai.us/large/p.jpg",` +
		`"tag_string_character":"hakurei_reimu","tag_string_general":"arm_up sword 1girl solo","tag_string_meta":"highres"}]`
	svc := NewTagService(newScanFetcher(scanRows, page([]byte(raw))), nil)

	res := mustSearchPosts(t, svc, "ARM_UP sword 1girl", 5, "diff")
	assertPayload(t, res, `{"posts":[{"id":1,"rating":"g","score":3,"fav_count":4,"tags":{"copyright":[],"artist":[],"character":["hakurei_reimu"],"general":["solo"],"meta":["highres"]},"preview_file_url":"https://cdn.donmai.us/preview/p.jpg","large_file_url":"https://cdn.donmai.us/large/p.jpg"}],"upstream_query":"arm_up sword","scanned":1,"complete":true}`)

	res = mustSearchPosts(t, svc, "ARM_UP sword 1girl", 5, "none")
	assertPayload(t, res, `{"posts":[{"id":1,"rating":"g","score":3,"fav_count":4,"preview_file_url":"https://cdn.donmai.us/preview/p.jpg","large_file_url":"https://cdn.donmai.us/large/p.jpg"}],"upstream_query":"arm_up sword","scanned":1,"complete":true}`)
}

// TestScan_EmptyAnswerKeepsPossibleTags pins the diagnostic on an empty scan
// answer: "blue hair order:random" used to be rejected by the 2-tag cap; it
// is now a scan — order:random takes one of the two terms, so the single tag
// sent upstream is the 0-post token blue, with hair as a local filter — that
// is empty and complete, and it still names the joined tag. scanned is
// omitted when nothing was scanned.
func TestScan_EmptyAnswerKeepsPossibleTags(t *testing.T) {
	svc := NewTagService(newScanFetcher(nil, page(scanPage())), vocab.Default())
	res := mustSearchPosts(t, svc, "blue hair order:random", 5)
	assertPayload(t, res, `{"posts":[],"possible_tags":["blue_hair"],"upstream_query":"blue order:random","complete":true}`)

	// Count mode answers the same query with the scan fields of its own.
	count := mustCountPosts(t, svc, "blue hair order:random")
	assertPayload(t, count, `{"query":"blue hair order:random","total":0,"complete":true}`)
}

// TestScan_NonPositiveLimitStopsAtTheDefaultPage pins what a limit of zero or
// less means for a scan: a direct query then omits the limit parameter and
// Danbooru answers its default page of 20, so the scan stops at 20 matches.
func TestScan_NonPositiveLimitStopsAtTheDefaultPage(t *testing.T) {
	f := newScanFetcher(scanRows, page(fullPage(1, scanPageSize, "arm_up sword 1girl")))
	svc := NewTagService(f, nil)

	for _, limit := range []int{0, -1} {
		res := mustSearchPosts(t, svc, "1girl arm_up sword", limit)
		if len(res.Posts) != maxPostLimit {
			t.Errorf("limit %d: want %d posts, got %d", limit, maxPostLimit, len(res.Posts))
		}
		if res.Scanned != maxPostLimit {
			t.Errorf("limit %d: scanned = %d, want %d", limit, res.Scanned, maxPostLimit)
		}
		assertComplete(t, res, true)
	}
	for _, c := range f.pages() {
		if c.limit != scanPageSize {
			t.Errorf("a scan page always asks for %d posts, got %+v", scanPageSize, c)
		}
	}
}

// TestWithUpstreamTagLimit pins the option and its default: without it the
// service assumes the anonymous limit of two terms; a higher limit sends
// more terms as-is; a value below one is ignored.
func TestWithUpstreamTagLimit(t *testing.T) {
	// Three terms: a scan by default, direct with a limit of 3 or more.
	tests := []struct {
		name   string
		opts   []Option
		direct bool
	}{
		{"default is the anonymous limit", nil, false},
		{"limit 3 sends three terms as-is", []Option{WithUpstreamTagLimit(3)}, true},
		{"limit 6 sends three terms as-is", []Option{WithUpstreamTagLimit(6)}, true},
		{"a value below one keeps the default", []Option{WithUpstreamTagLimit(0)}, false},
		{"a negative value keeps the default", []Option{WithUpstreamTagLimit(-6)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newScanFetcher(scanRows, page(scanPage(scanPost(1, "arm_up sword 1girl"))))
			res := mustSearchPosts(t, NewTagService(f, nil, tt.opts...), "1girl arm_up sword", 5)

			if tt.direct {
				assertRequests(t, f, 0, nil, scanCall{tags: "1girl arm_up sword", limit: 5, page: 1})
				assertNoScanFields(t, mustJSON(t, res))
			} else {
				assertRequests(t, f, 1, []string{"1girl", "arm_up", "sword"}, pairRequest("arm_up sword"))
				if res.Complete == nil {
					t.Errorf("a split answer carries complete, got %s", mustJSON(t, res))
				}
			}
		})
	}
}

// TestScan_ConcurrentCallsKeepPerCallState pins that a scan keeps all its
// state per call: concurrent scans over one service each get the complete
// answer of their own query, with their own pages, and nothing leaks between
// them (run under the race detector).
func TestScan_ConcurrentCallsKeepPerCallState(t *testing.T) {
	// The pages are scripted by page number, since concurrent callers cannot
	// be told apart by request order. Page 1 holds two matching posts — a
	// short page, so every scan ends there.
	f := newScanFetcher(scanRows)
	f.byPage = map[int]scanReply{1: page(scanPage(
		scanPost(7, "arm_up sword 1girl"),
		scanPost(9, "arm_up sword 1girl"),
	))}
	svc := NewTagService(f, nil)

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*PostsResult, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.SearchPosts(context.Background(), "1girl arm_up sword", 5, "")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: unexpected error: %v", i, err)
		}
	}
	for i, res := range results {
		if res == nil {
			continue
		}
		if got, want := postIDs(res), []int{7, 9}; !slices.Equal(got, want) {
			t.Errorf("caller %d: every concurrent scan must answer on its own, got posts %v (want %v)", i, got, want)
		}
		if res.UpstreamQuery != "arm_up sword" || res.Scanned != 2 || res.Complete == nil || !*res.Complete {
			t.Errorf("caller %d: upstream_query = %q, scanned = %d, complete = %v; want \"arm_up sword\", 2 and true",
				i, res.UpstreamQuery, res.Scanned, res.Complete)
		}
	}
	if lookups, _, calls, _ := f.log(); lookups != callers || len(calls) != callers {
		t.Errorf("every caller must make its own lookup and scan: got %d lookup(s) and %d page request(s), want %d and %d", lookups, len(calls), callers, callers)
	}
}
