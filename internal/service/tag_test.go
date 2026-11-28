package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"danbooru-tag-mcp/internal/vocab"
)

type mockFetcher struct {
	tagsResp    []byte
	exactResp   []byte
	namesResp   []byte
	relatedResp []byte
	aliasResp   []byte
	wikiResp    []byte
	postsResp   []byte
	countResp   []byte // raw /counts/posts.json body

	relatedCalls int   // number of FetchRelated invocations
	relatedErr   error // returned by FetchRelated instead of a body

	aliasCalls int   // number of FetchAlias invocations
	aliasErr   error // returned by FetchAlias instead of a body

	countCalls int   // number of FetchPostCount invocations
	countErr   error // returned by FetchPostCount instead of a body

	exactCalls   int    // number of FetchTagExact invocations
	lastExactArg string // name handed to the last FetchTagExact

	lastTagsArg      string
	lastTagsCategory int
	lastNamesArg     []string
	lastWikiTitle    string
	lastWikiOther    string
	lastPostsArg     string
	lastPostsLim     int
	postsCalls       int // number of FetchPosts invocations
	lastCountArg     string
}

func (m *mockFetcher) FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool, category int) ([]byte, error) {
	m.lastTagsArg = namePattern
	m.lastTagsCategory = category
	return m.tagsResp, nil
}

func (m *mockFetcher) FetchTagExact(ctx context.Context, name string) ([]byte, error) {
	m.exactCalls++
	m.lastExactArg = name
	return m.exactResp, nil
}

func (m *mockFetcher) FetchTagsByNames(ctx context.Context, names []string) ([]byte, error) {
	m.lastNamesArg = names
	return m.namesResp, nil
}

func (m *mockFetcher) FetchRelated(ctx context.Context, tag string) ([]byte, error) {
	m.relatedCalls++
	if m.relatedErr != nil {
		return nil, m.relatedErr
	}
	return m.relatedResp, nil
}

func (m *mockFetcher) FetchAlias(ctx context.Context, name string) ([]byte, error) {
	m.aliasCalls++
	if m.aliasErr != nil {
		return nil, m.aliasErr
	}
	return m.aliasResp, nil
}

func (m *mockFetcher) FetchWiki(ctx context.Context, title, otherNames string, limit int) ([]byte, error) {
	m.lastWikiTitle = title
	m.lastWikiOther = otherNames
	return m.wikiResp, nil
}

func (m *mockFetcher) FetchPosts(ctx context.Context, tags string, limit int) ([]byte, error) {
	m.postsCalls++
	m.lastPostsArg = tags
	m.lastPostsLim = limit
	return m.postsResp, nil
}

func (m *mockFetcher) FetchPostCount(ctx context.Context, tags string) ([]byte, error) {
	m.countCalls++
	m.lastCountArg = tags
	return m.countResp, m.countErr
}

func TestSearch_ParsesTags(t *testing.T) {
	mock := &mockFetcher{
		tagsResp: []byte(`[{"id":1,"name":"blue_hair","category":0,"post_count":500}]`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Search(context.Background(), "blue hair", 10, -1, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastTagsArg != "blue_hair" {
		t.Errorf("expected normalized query 'blue_hair', got '%s'", mock.lastTagsArg)
	}
	if len(res.Tags) != 1 || res.Tags[0].Name != "blue_hair" || res.Tags[0].PostCount != 500 {
		t.Errorf("parsed tag mismatch: %+v", res.Tags)
	}
}

func TestSearch_ForwardsCategory(t *testing.T) {
	// The API only accepts numeric ids (verified live: "character" returns
	// empty while 4 works), so the service must forward the id verbatim.
	mock := &mockFetcher{tagsResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	if _, err := svc.Search(context.Background(), "firefly", 10, 4, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastTagsCategory != 4 {
		t.Errorf("expected category 4 forwarded, got %d", mock.lastTagsCategory)
	}
	if _, err := svc.Search(context.Background(), "firefly", 10, -1, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastTagsCategory != -1 {
		t.Errorf("expected category -1 (no filter) forwarded, got %d", mock.lastTagsCategory)
	}
}

func TestSearch_AcceptsColonNames(t *testing.T) {
	// search_tags never treated a colon as metatag syntax: the query reaches
	// the fetcher verbatim, as it does for get_tag_info and get_tag_wiki.
	// get_tag_profile was the one tool refusing such names.
	for _, query := range []string{"firefly_(honkai:_star_rail)", "honkai:_star_rail", ":d"} {
		mock := &mockFetcher{tagsResp: []byte(`[]`)}
		svc := NewTagService(mock, nil)

		if _, err := svc.Search(context.Background(), query, 10, -1, 0); err != nil {
			t.Fatalf("search_tags must accept %q, got: %v", query, err)
		}
		if mock.lastTagsArg != query {
			t.Errorf("query must reach the fetcher verbatim, got %q", mock.lastTagsArg)
		}
	}
}

func TestCategoryID(t *testing.T) {
	for name, want := range map[string]int{
		"general": 0, "artist": 1, "copyright": 3, "character": 4, "meta": 5,
		"Character": 4, " CHARACTER ": 4,
	} {
		got, ok := CategoryID(name)
		if !ok || got != want {
			t.Errorf("CategoryID(%q): expected %d, got %d,%v", name, want, got, ok)
		}
	}
	for _, bad := range []string{"", "style", "circle", "0", "4"} {
		if _, ok := CategoryID(bad); ok {
			t.Errorf("CategoryID(%q) must not resolve", bad)
		}
	}
}

func TestInfo_NotFound(t *testing.T) {
	mock := &mockFetcher{
		exactResp: []byte(`[]`),
	}
	svc := NewTagService(mock, nil)

	_, err := svc.Info(context.Background(), "non_existent_tag")
	var nf *NotFoundError
	if !errors.As(err, &nf) || nf.Name != "non_existent_tag" {
		t.Fatalf("expected *NotFoundError with the queried name, got: %v", err)
	}
}

func TestInfo_WD14SectionRidesLiveRow(t *testing.T) {
	mock := &mockFetcher{
		exactResp: []byte(`[{"id":165438,"name":"anklet","category":0,"post_count":30000}]`),
	}
	svc := NewTagService(mock, vocab.Default())

	info, err := svc.Info(context.Background(), "anklet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.WD14.Status != wd14Live {
		t.Errorf("expected wd14 status live, got %q", info.WD14.Status)
	}
	if len(info.WD14.Sources) != 2 {
		t.Errorf("expected both vocabulary sources for anklet, got: %+v", info.WD14.Sources)
	}

	// The tag row must stay flat: found responses keep their old shape and
	// only gain the wd14 key.
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("failed to marshal info: %v", err)
	}
	for _, want := range []string{`"name":"anklet"`, `"post_count":30000`, `"wd14"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %s in payload, got: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"Tag"`) {
		t.Errorf("tag row must be flat, not nested, got: %s", raw)
	}
}

func TestInfo_ZeroCountRowIsTaggerEra(t *testing.T) {
	// Verified live: Danbooru returns 0-post placeholder rows for names the
	// taggers knew but Danbooru has since emptied (renamed-away antecedents,
	// unused tags) — they must not classify as live.
	mock := &mockFetcher{
		exactResp: []byte(`[
			{"id":1441951,"name":"gold_footwear","category":0,"post_count":0},
			{"id":604897,"name":"painted_toenails","category":0,"post_count":0}
		]`),
	}
	svc := NewTagService(mock, vocab.Default())

	info, err := svc.Info(context.Background(), "gold_footwear")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.WD14.Status != wd14TaggerEra || len(info.WD14.Sources) != 2 {
		t.Errorf("expected tagger_era with both sources, got: %+v", info.WD14)
	}
}

func TestWD14Info_TaggerEraVsUnknown(t *testing.T) {
	svc := NewTagService(&mockFetcher{}, vocab.Default())

	era := svc.WD14Info("barefoot_sandals")
	if era.Status != wd14TaggerEra || len(era.Sources) != 1 ||
		era.Sources[0].Vocab != "wd-v1-4-moat-tagger-v2" || era.Sources[0].Count != 931 {
		t.Errorf("expected tagger_era with the v2 snapshot (931), got: %+v", era)
	}

	unknown := svc.WD14Info("painted_toenails")
	if unknown.Status != wd14Unknown {
		t.Errorf("expected unknown for a made-up word, got %+v", unknown)
	}
	raw, _ := json.Marshal(unknown)
	if strings.Contains(string(raw), "sources") {
		t.Errorf("unknown must omit sources, got: %s", raw)
	}

	// With no vocab store the verdict degrades to unknown instead of panicking.
	bare := NewTagService(&mockFetcher{}, nil)
	if got := bare.WD14Info("barefoot_sandals").Status; got != wd14Unknown {
		t.Errorf("nil vocab store must degrade to unknown, got %q", got)
	}
}

func TestTaggerVocabHits(t *testing.T) {
	svc := NewTagService(&mockFetcher{}, vocab.Default())

	hits := svc.TaggerVocabHits("gold_foot", 8)
	if len(hits) != 1 || hits[0].Name != "gold_footwear" || len(hits[0].Sources) != 2 {
		t.Fatalf("expected merged gold_footwear, got: %+v", hits)
	}
	if got := svc.TaggerVocabHits("gold_foot", 0); got != nil {
		t.Errorf("non-positive limit must yield no hits, got: %+v", got)
	}
	bare := NewTagService(&mockFetcher{}, nil)
	if got := bare.TaggerVocabHits("gold_foot", 8); got != nil {
		t.Errorf("nil vocab store must yield no hits, got: %+v", got)
	}
}

func TestRelated_ParsesCurrentApiFormat(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: []byte(`{
			"query": "blue_hair",
			"post_count": 1207874,
			"tag": {"id": 10953, "name": "blue_hair", "post_count": 1207874, "category": 0},
			"related_tags": [
				{"tag": {"name": "blue_hair", "post_count": 1207874, "category": 0}, "cosine_similarity": 1.0, "frequency": 1.0},
				{"tag": {"name": "aqua_hair", "post_count": 179981, "category": 0}, "cosine_similarity": 0.45, "frequency": 0.11},
				{"tag": {"name": "highres", "post_count": 8211186, "category": 5}, "cosine_similarity": 0.261, "frequency": 0.6828}
			],
			"wiki_page_tags": [{"name": "aqua_hair", "post_count": 179981, "category": 0}]
		}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "blue_hair", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The query tag itself (similarity 1.0) and meta-category entries
	// (highres, category 5) must both be dropped; the limit is spent on
	// content tags only.
	if len(res.Related) != 1 {
		t.Fatalf("expected 1 related tag (self + meta dropped), got %d", len(res.Related))
	}
	if res.Related[0].Tag != "aqua_hair" || res.Related[0].Category != 0 || res.Related[0].PostCount != 179981 {
		t.Errorf("unexpected tag fields: %+v", res.Related[0])
	}
	if res.Related[0].Similarity != 0.45 || res.Related[0].Frequency != 0.11 {
		t.Errorf("unexpected similarity metrics: %+v", res.Related[0])
	}
}

func TestRelated_TruncatesToLimit(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: []byte(`{
			"query": "solo",
			"related_tags": [
				{"tag": {"name": "solo", "post_count": 1000, "category": 0}, "cosine_similarity": 1.0, "frequency": 1.0},
				{"tag": {"name": "1girl", "post_count": 900, "category": 0}, "cosine_similarity": 0.9, "frequency": 0.8},
				{"tag": {"name": "looking_at_viewer", "post_count": 800, "category": 0}, "cosine_similarity": 0.8, "frequency": 0.7},
				{"tag": {"name": "smile", "post_count": 700, "category": 0}, "cosine_similarity": 0.7, "frequency": 0.6}
			]
		}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "solo", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Related) != 2 {
		t.Fatalf("expected 2 tags truncated by limit, got %d", len(res.Related))
	}
	if res.Related[0].Tag != "1girl" || res.Related[1].Tag != "looking_at_viewer" {
		t.Errorf("unexpected content: %+v", res.Related)
	}
}

// relatedEntry renders one related_tags entry of a mock /related_tag.json
// body in the upstream shape (the nested tag row plus the two metrics the
// service reads), so a fixture stays one line per entry.
func relatedEntry(name string, postCount, category int, cosine, frequency float64) string {
	return fmt.Sprintf(`{"tag":{"name":%q,"post_count":%d,"category":%d},"cosine_similarity":%s,"frequency":%s}`,
		name, postCount, category,
		strconv.FormatFloat(cosine, 'g', -1, 64), strconv.FormatFloat(frequency, 'g', -1, 64))
}

// relatedBody renders a mock /related_tag.json body: the query, the query
// tag's own post count (the key is absent when queryPosts is 0) and the
// entries in the order given, which the tests write in upstream order
// (frequency descending).
func relatedBody(query string, queryPosts int, entries ...string) []byte {
	postCount := ""
	if queryPosts > 0 {
		postCount = fmt.Sprintf(`"post_count":%d,`, queryPosts)
	}
	return []byte(fmt.Sprintf(`{"query":%q,%s"tag":null,"related_tags":[%s],"wiki_page_tags":[]}`,
		query, postCount, strings.Join(entries, ",")))
}

// marshalRelated returns the wire bytes of a related answer.
func marshalRelated(t *testing.T, res *RelatedResult) string {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal related result: %v", err)
	}
	return string(raw)
}

// relatedNames lists the tag names of a related answer in answer order.
func relatedNames(res *RelatedResult) []string {
	names := make([]string, 0, len(res.Related))
	for _, r := range res.Related {
		names = append(names, r.Tag)
	}
	return names
}

// relatedLifts renders a related answer for failure messages: one
// "tag:lift" per entry, "-" where an entry carries no lift.
func relatedLifts(res *RelatedResult) []string {
	out := make([]string, 0, len(res.Related))
	for _, r := range res.Related {
		lift := "-"
		if r.Lift != nil {
			lift = strconv.FormatFloat(*r.Lift, 'g', -1, 64)
		}
		out = append(out, r.Tag+":"+lift)
	}
	return out
}

// TestRelated_PayloadBytesWithoutSiteCount pins the wire bytes of a
// non-empty answer when the whole-site count is unavailable (here: the count
// request fails): exactly the pre-lift payload, upstream order and the five
// historical fields, each keeping its name, type and position. The fixture
// leads with the query tag and mixes a meta entry in, so the exact bytes
// also prove both exclusions.
func TestRelated_PayloadBytesWithoutSiteCount(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: relatedBody("arm_up", 10000,
			relatedEntry("arm_up", 10000, 0, 1, 1),
			relatedEntry("1girl", 8800000, 0, 0.9, 0.7),
			relatedEntry("highres", 9000000, 5, 0.8, 0.55),
			relatedEntry("arm_behind_head", 20000, 0, 0.5, 0.25),
			relatedEntry("holding_sword", 40000, 0, 0.4, 0.15),
		),
		countErr: &upstreamMarkedError{},
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "arm_up", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"related":[` +
		`{"tag":"1girl","category":0,"post_count":8800000,"similarity":0.9,"frequency":0.7},` +
		`{"tag":"arm_behind_head","category":0,"post_count":20000,"similarity":0.5,"frequency":0.25},` +
		`{"tag":"holding_sword","category":0,"post_count":40000,"similarity":0.4,"frequency":0.15}` +
		`]}`
	if got := marshalRelated(t, res); got != want {
		t.Errorf("payload:\n got: %s\nwant: %s", got, want)
	}
}

// TestRelated_FetchFailurePropagatesWithoutSiteCount pins the failure path of
// the related request: the error reaches the caller unchanged (the handler
// turns a marked one into the uniform upstream_unavailable result, a parse
// error into the plain related_failed result), nothing is answered, and the
// whole-site count is never requested.
func TestRelated_FetchFailurePropagatesWithoutSiteCount(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		err  error
	}{
		{"upstream-marked request failure", nil, &upstreamMarkedError{}},
		{"unparsable body", []byte(`<html>Downbooru</html>`), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{
				relatedResp: tt.body,
				relatedErr:  tt.err,
				countResp:   []byte(`{"counts":{"posts":10000000}}`),
			}
			svc := NewTagService(mock, nil)

			res, err := svc.Related(context.Background(), "arm_up", 10)
			if res != nil {
				t.Errorf("a failed related request must answer nothing, got %+v", res)
			}
			switch {
			case tt.err != nil && err != tt.err:
				t.Errorf("the request error must reach the caller unchanged, got %v", err)
			case tt.err == nil && (err == nil || !strings.Contains(err.Error(), "failed to parse related tags")):
				t.Errorf("expected the related parse error, got %v", err)
			}
			if mock.relatedCalls != 1 {
				t.Errorf("expected exactly one related request, got %d", mock.relatedCalls)
			}
			if mock.countCalls != 0 {
				t.Errorf("a failed related request must not ask for the whole-site count, got %d request(s)", mock.countCalls)
			}
		})
	}
}

// liftRankingEntries is the hand-built related_tags list of the lift tests,
// in upstream order (frequency descending), for a query tag with 10000
// posts on a site of 10,000,000 posts (the mocks set those counts). Every
// content tag clears the support floor (frequency x 5000 >= 250), so all of
// them are ranked. The trailing comments give
// lift = frequency x N / post_count before and after rounding.
var liftRankingEntries = []string{
	relatedEntry("arm_up", 10000, 0, 1, 1),               // the query tag: excluded
	relatedEntry("1girl", 8800000, 0, 0.9, 0.7),          // 7000000 / 8800000 = 0.795 -> 0.8
	relatedEntry("solo", 6000000, 0, 0.8, 0.6),           // 6000000 / 6000000 = 1
	relatedEntry("highres", 9000000, 5, 0.8, 0.55),       // meta category: excluded
	relatedEntry("tie_lower_freq", 4990000, 0, 0.7, 0.5), // 5000000 / 4990000 = 1.002 -> 1
	relatedEntry("arm_behind_head", 20000, 0, 0.5, 0.25), // 2500000 / 20000 = 125
	relatedEntry("holding_sword", 40000, 0, 0.4, 0.15),   // 1500000 / 40000 = 37.5
	relatedEntry("waving", 50000, 0, 0.3, 0.12),          // 1200000 / 50000 = 24
	relatedEntry("bkub", 30000, 1, 0.2, 0.09),            // 900000 / 30000 = 30 (an artist: kept)
}

// liftRankedOrder is liftRankingEntries the way the lift ranking orders
// them: the answer every lift test expects while the whole-site count is
// available.
var liftRankedOrder = []string{
	"arm_behind_head", "holding_sword", "bkub", "waving", "solo", "tie_lower_freq", "1girl",
}

// liftUpstreamPayload is liftRankingEntries as the pre-lift payload: the
// upstream order, the five historical fields, no lift key. It is the answer
// every lift test expects while the whole-site count is unavailable.
const liftUpstreamPayload = `{"related":[` +
	`{"tag":"1girl","category":0,"post_count":8800000,"similarity":0.9,"frequency":0.7},` +
	`{"tag":"solo","category":0,"post_count":6000000,"similarity":0.8,"frequency":0.6},` +
	`{"tag":"tie_lower_freq","category":0,"post_count":4990000,"similarity":0.7,"frequency":0.5},` +
	`{"tag":"arm_behind_head","category":0,"post_count":20000,"similarity":0.5,"frequency":0.25},` +
	`{"tag":"holding_sword","category":0,"post_count":40000,"similarity":0.4,"frequency":0.15},` +
	`{"tag":"waving","category":0,"post_count":50000,"similarity":0.3,"frequency":0.12},` +
	`{"tag":"bkub","category":1,"post_count":30000,"similarity":0.2,"frequency":0.09}` +
	`]}`

// liftRankedPayload is liftRankingEntries ranked by lift: liftRankedOrder,
// each entry keeping its five historical fields in place with lift last. It
// is the answer the lift tests expect while the whole-site count is
// available and no limit cuts the list.
const liftRankedPayload = `{"related":[` +
	`{"tag":"arm_behind_head","category":0,"post_count":20000,"similarity":0.5,"frequency":0.25,"lift":125},` +
	`{"tag":"holding_sword","category":0,"post_count":40000,"similarity":0.4,"frequency":0.15,"lift":37.5},` +
	`{"tag":"bkub","category":1,"post_count":30000,"similarity":0.2,"frequency":0.09,"lift":30},` +
	`{"tag":"waving","category":0,"post_count":50000,"similarity":0.3,"frequency":0.12,"lift":24},` +
	`{"tag":"solo","category":0,"post_count":6000000,"similarity":0.8,"frequency":0.6,"lift":1},` +
	`{"tag":"tie_lower_freq","category":0,"post_count":4990000,"similarity":0.7,"frequency":0.5,"lift":1},` +
	`{"tag":"1girl","category":0,"post_count":8800000,"similarity":0.9,"frequency":0.7,"lift":0.8}` +
	`]}`

// TestRelated_LiftMathAndRanking pins the lift formula and the ranking it
// drives on liftRankingEntries (query tag 10000 posts, site 10,000,000).
// lift = frequency x N / post_count, rounded to two decimals, descending.
// tie_lower_freq shares solo's rounded lift of 1 although it is the higher
// unrounded value (1.002): its frequency is lower, so the stable sort keeps
// it after solo. limit is applied after the ranking — a limit of 3 keeps the
// three top-lift entries, which sit at upstream positions 6, 7 and 9 — and
// the query tag and the meta entry stay excluded while the artist entry is
// kept.
func TestRelated_LiftMathAndRanking(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: relatedBody("arm_up", 10000, liftRankingEntries...),
		countResp:   []byte(`{"counts":{"posts":10000000}}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "arm_up", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := relatedNames(res); !slices.Equal(got, liftRankedOrder) {
		t.Errorf("ranked order:\n got: %v\nwant: %v", got, liftRankedOrder)
	}
	wantLift := map[string]float64{
		"arm_behind_head": 125, "holding_sword": 37.5,
		"bkub": 30, "waving": 24, "solo": 1, "tie_lower_freq": 1, "1girl": 0.8,
	}
	for _, r := range res.Related {
		if r.Lift == nil {
			t.Errorf("%s: expected a lift, got none", r.Tag)
			continue
		}
		if *r.Lift != wantLift[r.Tag] {
			t.Errorf("%s: lift = %v, want %v", r.Tag, *r.Lift, wantLift[r.Tag])
		}
		if r.Category == danbooruCategoryMeta {
			t.Errorf("%s: a meta entry must stay excluded", r.Tag)
		}
	}
	for _, excluded := range []string{"arm_up", "highres"} {
		if slices.Contains(relatedNames(res), excluded) {
			t.Errorf("%s: the query tag and meta entries must stay excluded", excluded)
		}
	}
	if got := marshalRelated(t, res); got != liftRankedPayload {
		t.Errorf("payload (the five historical fields keep name, type and position; lift is last):\n got: %s\nwant: %s", got, liftRankedPayload)
	}

	// The limit cuts the ranked list, not the upstream one: truncating first
	// would keep the three most frequent entries instead.
	limited, err := svc.Related(context.Background(), "arm_up", 3)
	if err != nil {
		t.Fatalf("limited call: unexpected error: %v", err)
	}
	if got, want := relatedNames(limited), liftRankedOrder[:3]; !slices.Equal(got, want) {
		t.Errorf("limit 3 must keep the three top-lift entries:\n got: %v\nwant: %v", got, want)
	}
	if mock.countCalls != 1 {
		t.Errorf("the second call must answer from the memoized whole-site count, got %d request(s)", mock.countCalls)
	}
}

// TestRelated_NonPositiveLimitKeepsAll pins the meaning of limit at its lower
// end, which the lift ranking keeps: 0 or less is no limit, so every
// candidate is answered — ranked while the whole-site count is available,
// in Danbooru's order while it is not — as it was before the ranking.
func TestRelated_NonPositiveLimitKeepsAll(t *testing.T) {
	tests := []struct {
		name      string
		countErr  error
		wantBytes string
	}{
		{"whole-site count available", nil, liftRankedPayload},
		{"whole-site count unavailable", &upstreamMarkedError{}, liftUpstreamPayload},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, limit := range []int{0, -1} {
				mock := &mockFetcher{
					relatedResp: relatedBody("arm_up", 10000, liftRankingEntries...),
					countErr:    tt.countErr,
					countResp:   []byte(`{"counts":{"posts":10000000}}`),
				}
				svc := NewTagService(mock, nil)

				res, err := svc.Related(context.Background(), "arm_up", limit)
				if err != nil {
					t.Fatalf("limit %d: unexpected error: %v", limit, err)
				}
				if got := marshalRelated(t, res); got != tt.wantBytes {
					t.Errorf("limit %d must keep every entry:\n got: %s\nwant: %s", limit, got, tt.wantBytes)
				}
			}
		})
	}
}

// TestRelatedLift_FormulaRoundingAndGuards table-tests the pure lift helper.
// The value is frequency x N / post_count rounded to two decimals: the
// hundredths digit survives (14.77 is not 14.8, a lift of 0.01 is not 0) and
// a third digit does not (12.3456 is 12.35, rounded, not cut to 12.34). The
// answer is nil — no lift key on the wire — whenever the formula cannot be
// evaluated: a 0-post entry (division by zero), a 0-frequency entry (a lift
// of 0 would sort below real entries) or no site count.
func TestRelatedLift_FormulaRoundingAndGuards(t *testing.T) {
	lift := func(v float64) *float64 { return &v }
	const site = 10000000
	tests := []struct {
		name      string
		frequency float64
		posts     int
		sitePosts int
		want      *float64 // nil = no lift
	}{
		{"whole number", 0.25, 20000, site, lift(125)},
		{"keeps the hundredths digit", 0.1477, 100000, site, lift(14.77)},
		{"hundredths digit next to a nine", 0.1499, 100000, site, lift(14.99)},
		{"third decimal digit is rounded away", 0.123456, 100000, site, lift(12.35)},
		{"below one", 0.7, 8800000, site, lift(0.8)},
		{"small lift is not flattened to zero", 0.000001, 1000, site, lift(0.01)},
		{"0-post entry has no lift", 0.2, 0, site, nil},
		{"negative post count has no lift", 0.2, -5, site, nil},
		{"0-frequency entry has no lift", 0, 1000, site, nil},
		{"no site count", 0.5, 1000, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relatedLift(tt.frequency, tt.posts, tt.sitePosts)
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("relatedLift(%v, %d, %d) = %v, want no lift", tt.frequency, tt.posts, tt.sitePosts, *got)
			case tt.want != nil && got == nil:
				t.Errorf("relatedLift(%v, %d, %d) = no lift, want %v", tt.frequency, tt.posts, tt.sitePosts, *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Errorf("relatedLift(%v, %d, %d) = %v, want %v", tt.frequency, tt.posts, tt.sitePosts, *got, *tt.want)
			}
		})
	}
}

// TestRelated_SiteCountFetchedOnce pins the whole-site count memo (R2): the
// count is requested for the first answer that has entries, with empty tags
// (the whole site), and never again in the process — the related request
// itself stays live on every call.
func TestRelated_SiteCountFetchedOnce(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: relatedBody("arm_up", 10000, liftRankingEntries...),
		countResp:   []byte(`{"counts":{"posts":10000000}}`),
	}
	svc := NewTagService(mock, nil)

	for call := 1; call <= 2; call++ {
		res, err := svc.Related(context.Background(), "arm_up", 10)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", call, err)
		}
		if len(res.Related) == 0 || res.Related[0].Tag != "arm_behind_head" || res.Related[0].Lift == nil {
			t.Fatalf("call %d: expected a ranked answer carrying lift, got %+v", call, res.Related)
		}
	}
	if mock.countCalls != 1 {
		t.Errorf("the whole-site count must be requested once per process, got %d request(s)", mock.countCalls)
	}
	if mock.lastCountArg != "" {
		t.Errorf("the whole-site count must be requested with empty tags, got %q", mock.lastCountArg)
	}
	if mock.relatedCalls != 2 {
		t.Errorf("every call must make its own related request, got %d", mock.relatedCalls)
	}
}

// TestRelated_SiteCountFailureDegradesAndRetries pins R3 and the failure
// matrix: when the whole-site count cannot be had, the call still answers,
// in exactly the pre-lift shape (upstream order, no lift key), and every
// failure class behaves the same way. A failure is never memoized: the next
// call requests the count again, and a count that then succeeds turns the
// lift on.
func TestRelated_SiteCountFailureDegradesAndRetries(t *testing.T) {
	tests := []struct {
		name      string
		countErr  error
		countResp []byte
	}{
		{"upstream-marked request failure", &upstreamMarkedError{}, nil},
		{"unmarked request failure (definitive 4xx)", errors.New("danbooru 404: not found"), nil},
		{"caller cancellation", context.Canceled, nil},
		{"empty body", nil, nil},
		{"unparsable body", nil, []byte(`<html>Downbooru</html>`)},
		{"timed-out count (null)", nil, []byte(`{"counts":{"posts":null}}`)},
		{"zero count", nil, []byte(`{"counts":{"posts":0}}`)},
		{"negative count", nil, []byte(`{"counts":{"posts":-5}}`)},
		{"not an integer", nil, []byte(`{"counts":{"posts":12.5}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{
				relatedResp: relatedBody("arm_up", 10000, liftRankingEntries...),
				countErr:    tt.countErr,
				countResp:   tt.countResp,
			}
			svc := NewTagService(mock, nil)

			res, err := svc.Related(context.Background(), "arm_up", 100)
			if err != nil {
				t.Fatalf("a failed whole-site count must never fail the call, got: %v", err)
			}
			if got := marshalRelated(t, res); got != liftUpstreamPayload {
				t.Errorf("degraded payload (upstream order, no lift):\n got: %s\nwant: %s", got, liftUpstreamPayload)
			}
			if mock.countCalls != 1 {
				t.Errorf("the call must try the whole-site count once, got %d request(s)", mock.countCalls)
			}

			// The failure was not memoized: the next call asks again.
			mock.countErr, mock.countResp = nil, []byte(`{"counts":{"posts":10000000}}`)
			res, err = svc.Related(context.Background(), "arm_up", 100)
			if err != nil {
				t.Fatalf("retry call: unexpected error: %v", err)
			}
			if mock.countCalls != 2 {
				t.Errorf("a failed whole-site count must be requested again on the next call, got %d request(s) in total", mock.countCalls)
			}
			if got := relatedNames(res); !slices.Equal(got, liftRankedOrder) || res.Related[0].Lift == nil {
				t.Errorf("a count that succeeds on retry must turn the lift ranking on, got %v", relatedLifts(res))
			}
		})
	}
}

// TestRelated_EmptyResultSkipsSiteCount pins the empty answer: when nothing
// is left after the exclusions (a tag Danbooru does not know, or one whose
// only company is itself and meta tags), the answer is the bare
// {"related":[]} — an empty list, never null — and the whole-site count is
// not requested for it.
func TestRelated_EmptyResultSkipsSiteCount(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"not-found shape", []byte(`{"query":"not_a_real_tag_xyz","post_count":0,"tag":null,"related_tags":[],"wiki_page_tags":[]}`)},
		{"only the query tag and a meta entry", relatedBody("arm_up", 10000,
			relatedEntry("arm_up", 10000, 0, 1, 1),
			relatedEntry("highres", 9000000, 5, 0.8, 0.55),
		)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{relatedResp: tt.body, countResp: []byte(`{"counts":{"posts":10000000}}`)}
			svc := NewTagService(mock, nil)

			res, err := svc.Related(context.Background(), "arm_up", 10)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res == nil {
				t.Fatal("an empty answer is a valid answer, got nil")
			}
			if got := marshalRelated(t, res); got != `{"related":[]}` {
				t.Errorf("empty answer:\n got: %s\nwant: {\"related\":[]}", got)
			}
			if mock.countCalls != 0 {
				t.Errorf("an empty answer must not request the whole-site count, got %d request(s)", mock.countCalls)
			}
		})
	}
}

// TestRelated_SupportFloorSinksLowOverlapEntries pins the support floor: an
// entry backed by fewer than 5 posts (support = frequency x min(query
// posts, 5000); exact here, the query tag has 40 posts) is not trusted to
// rank, however high its lift. Such entries are sunk after every ranked one
// — in upstream order among themselves, not in lift order — and never
// dropped: they still carry their lift and a larger limit still reaches
// them. An entry sitting exactly on the floor is ranked.
func TestRelated_SupportFloorSinksLowOverlapEntries(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: relatedBody("warming_hands", 40,
			relatedEntry("warming_hands", 40, 0, 1, 1),                       // the query tag: excluded
			relatedEntry("kerosene_heater", 400, 0, 0.4, 0.4),                // support 16, lift 1000
			relatedEntry("breathing_on_another's_hands", 700, 0, 0.35, 0.35), // support 14, lift 500
			relatedEntry("breathing_on_hands", 3000, 0, 0.3, 0.3),            // support 12, lift 100
			relatedEntry("joint_five", 500, 0, 0.25, 0.125),                  // support 5 (the floor), lift 250
			relatedEntry("rare_pose_a", 25, 0, 0.1, 0.1),                     // support 4, lift 4000: sunk
			relatedEntry("rare_pose_b", 12, 0, 0.05, 0.075),                  // support 3, lift 6250: sunk
			relatedEntry("rare_pose_c", 50, 0, 0.05, 0.05),                   // support 2, lift 1000: sunk
		),
		countResp: []byte(`{"counts":{"posts":1000000}}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "warming_hands", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantOrder := []string{
		"kerosene_heater", "breathing_on_another's_hands", "joint_five", "breathing_on_hands",
		"rare_pose_a", "rare_pose_b", "rare_pose_c",
	}
	if got := relatedNames(res); !slices.Equal(got, wantOrder) {
		t.Errorf("ranked entries first, below-floor entries after them in upstream order:\n got: %v\nwant: %v", got, wantOrder)
	}
	wantLift := map[string]float64{
		"kerosene_heater": 1000, "breathing_on_another's_hands": 500, "joint_five": 250,
		"breathing_on_hands": 100, "rare_pose_a": 4000, "rare_pose_b": 6250, "rare_pose_c": 1000,
	}
	for _, r := range res.Related {
		if r.Lift == nil {
			t.Errorf("%s: every entry carries its lift, sunk ones included, got none", r.Tag)
			continue
		}
		if *r.Lift != wantLift[r.Tag] {
			t.Errorf("%s: lift = %v, want %v", r.Tag, *r.Lift, wantLift[r.Tag])
		}
	}

	// Sunk, not dropped: a smaller limit still reaches the sunk entries.
	limited, err := svc.Related(context.Background(), "warming_hands", 5)
	if err != nil {
		t.Fatalf("limited call: unexpected error: %v", err)
	}
	if got, want := relatedNames(limited), wantOrder[:5]; !slices.Equal(got, want) {
		t.Errorf("limit 5:\n got: %v\nwant: %v", got, want)
	}
}

// TestRelated_SupportIsCappedAtTheSampleSize pins the min(A, 5000) of the
// support: Danbooru estimates every frequency of a query tag with more than
// 5000 posts from 5000 of them, so an entry's support is frequency x 5000,
// never frequency x the query tag's real post count (20000 here).
// on_cap_floor sits exactly on the floor (0.001 x 5000 = 5) and is ranked.
// Measured against 20000 posts the two entries below it would clear the
// floor too and, having the higher lifts, take the top: they must stay
// sunk, in upstream order, and still carry their lift.
func TestRelated_SupportIsCappedAtTheSampleSize(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: relatedBody("arm_up", 20000,
			relatedEntry("arm_up", 20000, 0, 1, 1),                      // the query tag: excluded
			relatedEntry("ranked_low_lift", 10000, 0, 0.01, 0.002),      // support 10, lift 0.2
			relatedEntry("on_cap_floor", 100, 0, 0.005, 0.001),          // support 5 (the floor), lift 10
			relatedEntry("just_below_cap_floor", 30, 0, 0.0045, 0.0009), // support 4.5, lift 30: sunk
			relatedEntry("below_cap_floor", 50, 0, 0.004, 0.0008),       // support 4, lift 16: sunk
		),
		countResp: []byte(`{"counts":{"posts":1000000}}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "arm_up", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"related":[` +
		`{"tag":"on_cap_floor","category":0,"post_count":100,"similarity":0.005,"frequency":0.001,"lift":10},` +
		`{"tag":"ranked_low_lift","category":0,"post_count":10000,"similarity":0.01,"frequency":0.002,"lift":0.2},` +
		`{"tag":"just_below_cap_floor","category":0,"post_count":30,"similarity":0.0045,"frequency":0.0009,"lift":30},` +
		`{"tag":"below_cap_floor","category":0,"post_count":50,"similarity":0.004,"frequency":0.0008,"lift":16}` +
		`]}`
	if got := marshalRelated(t, res); got != want {
		t.Errorf("support is frequency x min(query posts, 5000):\n got: %s\nwant: %s", got, want)
	}
}

// TestRelated_ZeroPostCountKeepsUpstreamOrder pins the safe degradation when
// the query tag's own count cannot support a ranking: a missing or zero
// top-level post_count makes every support 0, so no entry reaches the floor
// and the list stays in upstream order. The whole-site count is still had,
// so every entry carries its lift. A query tag too small for any entry to
// reach the floor (4 posts) answers the same way.
func TestRelated_ZeroPostCountKeepsUpstreamOrder(t *testing.T) {
	entries := []string{
		relatedEntry("arm_up", 0, 0, 1, 1),                   // the query tag: excluded
		relatedEntry("1girl", 8800000, 0, 0.9, 0.7),          // lift 0.8
		relatedEntry("solo", 6000000, 0, 0.8, 0.6),           // lift 1
		relatedEntry("arm_behind_head", 20000, 0, 0.5, 0.25), // lift 125: the highest, must not float up
	}
	tests := []struct {
		name string
		body []byte
	}{
		{"post_count key absent", []byte(fmt.Sprintf(`{"query":"arm_up","tag":null,"related_tags":[%s]}`, strings.Join(entries, ",")))},
		{"post_count is zero", relatedBody("arm_up", 0, entries...)},
		{"query tag too small for the floor", relatedBody("arm_up", 4, entries...)},
	}
	wantOrder := []string{"1girl", "solo", "arm_behind_head"}
	wantLift := []float64{0.8, 1, 125}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{relatedResp: tt.body, countResp: []byte(`{"counts":{"posts":10000000}}`)}
			svc := NewTagService(mock, nil)

			res, err := svc.Related(context.Background(), "arm_up", 100)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := relatedNames(res); !slices.Equal(got, wantOrder) {
				t.Errorf("no re-ranking without a usable query count:\n got: %v\nwant: %v", got, wantOrder)
			}
			if mock.countCalls != 1 {
				t.Errorf("the whole-site count is still requested, got %d request(s)", mock.countCalls)
			}
			for i, r := range res.Related {
				if r.Lift == nil || *r.Lift != wantLift[i] {
					t.Errorf("%s: lift = %v, want %v (attached, only the ranking is off)", r.Tag, r.Lift, wantLift[i])
				}
			}
		})
	}
}

// TestRelated_UncomputableLiftIsOmitted pins the defensive rule for entries
// lift cannot be computed for: a 0-post or 0-frequency entry carries no lift
// key rather than a wrong number. Neither occurs with today's upstream
// logic. A 0-post entry that is well supported stays in the ranked group,
// after every entry that has a lift; a 0-frequency entry is below the floor
// like any other.
func TestRelated_UncomputableLiftIsOmitted(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: relatedBody("arm_up", 1000,
			relatedEntry("arm_up", 1000, 0, 1, 1),     // the query tag: excluded
			relatedEntry("live_a", 1000, 0, 0.5, 0.5), // lift 500
			relatedEntry("dead_tag", 0, 0, 0.3, 0.2),  // 0 posts: no lift; support 200
			relatedEntry("live_b", 2000, 0, 0.2, 0.1), // lift 50
			relatedEntry("zero_freq", 500, 0, 0, 0),   // 0 frequency: no lift; support 0
		),
		countResp: []byte(`{"counts":{"posts":1000000}}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "arm_up", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"related":[` +
		`{"tag":"live_a","category":0,"post_count":1000,"similarity":0.5,"frequency":0.5,"lift":500},` +
		`{"tag":"live_b","category":0,"post_count":2000,"similarity":0.2,"frequency":0.1,"lift":50},` +
		`{"tag":"dead_tag","category":0,"post_count":0,"similarity":0.3,"frequency":0.2},` +
		`{"tag":"zero_freq","category":0,"post_count":500,"similarity":0,"frequency":0}` +
		`]}`
	if got := marshalRelated(t, res); got != want {
		t.Errorf("payload:\n got: %s\nwant: %s", got, want)
	}
}

// TestRelated_LiftTieBlockKeepsUpstreamOrder pins the answer for a long
// block of equal lifts: 40 entries share one lift (100) and strictly
// decrease in frequency, so upstream order and expected answer are both
// tie_00, tie_01, ... An all-equal block is already sorted, and Go's
// unstable sort leaves a sorted slice untouched, so this case cannot tell a
// stable sort from an unstable one; the alternating case below can.
func TestRelated_LiftTieBlockKeepsUpstreamOrder(t *testing.T) {
	const tied = 40
	// post_count = frequency x 100000 keeps lift = frequency x 10000000 /
	// post_count at 100 for every entry.
	entries := make([]string, 0, tied+1)
	want := make([]string, 0, tied)
	entries = append(entries, relatedEntry("arm_up", 10000, 0, 1, 1)) // the query tag: excluded
	for i := 0; i < tied; i++ {
		postCount := 50000 - 500*i
		name := fmt.Sprintf("tie_%02d", i)
		entries = append(entries, relatedEntry(name, postCount, 0, 0.1, float64(postCount)/100000))
		want = append(want, name)
	}
	mock := &mockFetcher{
		relatedResp: relatedBody("arm_up", 10000, entries...),
		countResp:   []byte(`{"counts":{"posts":10000000}}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "arm_up", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := relatedNames(res); !slices.Equal(got, want) {
		t.Errorf("equal lifts keep upstream (frequency) order:\n got: %v\nwant: %v", got, want)
	}
	for _, r := range res.Related {
		if r.Lift == nil || *r.Lift != 100 {
			t.Errorf("every entry of the block carries lift 100, got %v", relatedLifts(res))
			break
		}
	}
}

// TestRelated_EqualLiftsKeepUpstreamOrderAfterSwaps pins the stable sort on
// an input an unstable sort scrambles: 80 entries whose lifts alternate
// between 10 and 3 upstream, so sorting has to move every other entry and
// must still keep each lift group in upstream order. (The all-equal block
// above is already sorted, so an unstable sort never touches it; verified
// by mutation: sort.Slice fails here and passes there.)
func TestRelated_EqualLiftsKeepUpstreamOrderAfterSwaps(t *testing.T) {
	const (
		entries = 80
		site    = 10000000
		base    = 20000 // post counts base+i: every entry stays far above the support floor
	)
	upstream := make([]string, 1, entries+1)
	upstream[0] = relatedEntry("arm_up", 10000, 0, 1, 1) // the query tag: excluded
	var want []string
	for _, parity := range []int{0, 1} { // answer order: the lift-10 group, then lift-3
		for i := parity; i < entries; i += 2 {
			want = append(want, fmt.Sprintf("alt_%02d", i))
		}
	}
	for i := 0; i < entries; i++ {
		lift, posts := 3.0, base+i
		if i%2 == 0 {
			lift = 10
		}
		upstream = append(upstream, relatedEntry(fmt.Sprintf("alt_%02d", i), posts, 0, 0.1, lift*float64(posts)/site))
	}
	mock := &mockFetcher{
		relatedResp: relatedBody("arm_up", 10000, upstream...),
		countResp:   []byte(`{"counts":{"posts":10000000}}`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Related(context.Background(), "arm_up", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := relatedNames(res); !slices.Equal(got, want) {
		t.Errorf("equal lifts keep upstream order even when the sort moves entries:\n got: %v\nwant: %v", got, want)
	}
	for i, r := range res.Related {
		wantLift := 3.0
		if i < entries/2 {
			wantLift = 10
		}
		if r.Lift == nil || *r.Lift != wantLift {
			t.Errorf("position %d (%s): lift = %v, want %v", i, r.Tag, r.Lift, wantLift)
		}
	}
}

// countedCountFetcher wraps the mock for the test that calls Related from
// several goroutines at once (the plain mock's counters are not
// synchronized). It counts its own calls, records how many count requests
// were in flight at the same time, and holds each one open for hold, so
// concurrent callers pile up behind the memo.
type countedCountFetcher struct {
	*mockFetcher

	hold time.Duration

	mu          sync.Mutex
	seenRelated int
	seenCount   int
	inFlight    int
	maxInFlight int
}

// FetchRelated counts the call under the lock and answers from the canned body.
func (f *countedCountFetcher) FetchRelated(ctx context.Context, tag string) ([]byte, error) {
	f.mu.Lock()
	f.seenRelated++
	f.mu.Unlock()
	return f.mockFetcher.relatedResp, f.mockFetcher.relatedErr
}

// FetchPostCount counts the call, tracks the in-flight requests, holds the
// request open for hold, then answers from the canned body.
func (f *countedCountFetcher) FetchPostCount(ctx context.Context, tags string) ([]byte, error) {
	f.mu.Lock()
	f.seenCount++
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()
	time.Sleep(f.hold)
	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
	return f.mockFetcher.countResp, f.mockFetcher.countErr
}

// counts returns how many related and count requests were made, and the
// largest number of count requests that ran at the same time.
func (f *countedCountFetcher) counts() (related, count, maxInFlight int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seenRelated, f.seenCount, f.maxInFlight
}

// TestRelated_ConcurrentCallsFetchSiteCountOnce pins the memo under
// concurrency (mcp-go runs tool calls on concurrent workers): the mutex is
// held across the whole-site count request, so callers arriving while it is
// in flight wait for it instead of sending their own. Every caller gets the
// complete ranked answer, exactly one count request is made, and no two
// count requests ever overlap.
func TestRelated_ConcurrentCallsFetchSiteCountOnce(t *testing.T) {
	const callers = 8
	mock := &countedCountFetcher{
		mockFetcher: &mockFetcher{
			relatedResp: relatedBody("arm_up", 10000, liftRankingEntries...),
			countResp:   []byte(`{"counts":{"posts":10000000}}`),
		},
		hold: 20 * time.Millisecond,
	}
	svc := NewTagService(mock, nil)

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*RelatedResult, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.Related(context.Background(), "arm_up", 10)
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
		if got := relatedNames(res); !slices.Equal(got, liftRankedOrder) || slices.ContainsFunc(res.Related, func(r RelatedTag) bool { return r.Lift == nil }) {
			t.Errorf("caller %d: every concurrent answer must be the complete ranked list, got %v", i, relatedLifts(res))
		}
	}
	related, count, maxInFlight := mock.counts()
	if related != callers {
		t.Errorf("every caller must make its own related request, got %d", related)
	}
	if count != 1 {
		t.Errorf("the whole-site count must be requested once, however many callers race for it, got %d", count)
	}
	if maxInFlight != 1 {
		t.Errorf("count requests must never overlap (the memo holds its mutex across the request), saw %d in flight", maxInFlight)
	}
}

func TestAlias_ParsesMapping(t *testing.T) {
	mock := &mockFetcher{
		aliasResp: []byte(`[{"id":7315,"antecedent_name":"sailor_suit","consequent_name":"sailor","status":"active"}]`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Alias(context.Background(), "sailor suit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Alias == nil || res.Alias.Antecedent != "sailor_suit" || res.Alias.Consequent != "sailor" {
		t.Fatalf("unexpected alias: %+v", res.Alias)
	}
}

func TestAlias_NoActiveAliasReturnsNull(t *testing.T) {
	mock := &mockFetcher{aliasResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	res, err := svc.Alias(context.Background(), "blue_hair")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Alias != nil {
		t.Fatalf("expected null alias for canonical input, got: %+v", res.Alias)
	}
}

func TestWiki_ExtractsLinkedTags(t *testing.T) {
	mock := &mockFetcher{
		wikiResp: []byte(`[{
			"id": 118420,
			"title": "firefly_(honkai:_star_rail)",
			"body": "Firefly is a character from [[Honkai: Star Rail]].\n\n=== Appearance ===\n* [[grey_hair]]\n* [[cyan_eyes|cyan eyes]]\n* [[grey_hair]]",
			"other_names": ["ホタル (崩壊:スターレイル)", "流萤 (崩坏:星穹铁道)"],
			"is_deleted": false
		}]`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.Wiki(context.Background(), "firefly_(honkai:_star_rail)", "", 5, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(res.Pages))
	}
	p := res.Pages[0]
	if p.Title != "firefly_(honkai:_star_rail)" {
		t.Errorf("unexpected title: %s", p.Title)
	}
	if len(p.OtherNames) != 2 || p.OtherNames[1] != "流萤 (崩坏:星穹铁道)" {
		t.Errorf("unexpected other_names: %+v", p.OtherNames)
	}
	// Deduplicated, pipe display text stripped, order of first appearance kept.
	want := []string{"Honkai: Star Rail", "grey_hair", "cyan_eyes"}
	if len(p.LinkedTags) != len(want) {
		t.Fatalf("expected linked tags %v, got %v", want, p.LinkedTags)
	}
	for i, tag := range want {
		if p.LinkedTags[i] != tag {
			t.Errorf("linked tag %d: expected %s, got %s", i, tag, p.LinkedTags[i])
		}
	}
	if mock.lastWikiTitle != "firefly_(honkai:_star_rail)" || mock.lastWikiOther != "" {
		t.Errorf("title mode must pass the title only, got title=%q other=%q", mock.lastWikiTitle, mock.lastWikiOther)
	}
}

func TestWiki_OtherNamesMode(t *testing.T) {
	mock := &mockFetcher{wikiResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	if _, err := svc.Wiki(context.Background(), "", "流萤", 5, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastWikiTitle != "" || mock.lastWikiOther != "流萤" {
		t.Errorf("other-names mode must pass the alias only, got title=%q other=%q", mock.lastWikiTitle, mock.lastWikiOther)
	}
}

func TestInfo_RenamedTo(t *testing.T) {
	// Verified live: renamed-away names keep a 0-post placeholder row, and
	// every sampled active alias antecedent has one. The alias consequent is
	// the actionable answer, so it rides the row as renamed_to.
	mock := &mockFetcher{
		exactResp: []byte(`[{"id":26260,"name":"sailor_suit","category":0,"post_count":0}]`),
		aliasResp: []byte(`[{"antecedent_name":"sailor_suit","consequent_name":"sailor","status":"active"}]`),
	}
	svc := NewTagService(mock, nil)

	info, err := svc.Info(context.Background(), "sailor_suit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.RenamedTo != "sailor" {
		t.Errorf("expected renamed_to sailor, got %q", info.RenamedTo)
	}
	raw, _ := json.Marshal(info)
	if !strings.Contains(string(raw), `"renamed_to":"sailor"`) {
		t.Errorf("expected renamed_to in payload, got: %s", raw)
	}
}

func TestInfo_LiveRowSkipsAliasRequest(t *testing.T) {
	// The alias lookup runs only on the 0-post dead end: a live row must not
	// pay the extra request.
	mock := &mockFetcher{
		exactResp: []byte(armUpExactRow),
		aliasResp: []byte(`[{"antecedent_name":"arm_up","consequent_name":"x","status":"active"}]`),
	}
	svc := NewTagService(mock, nil)

	info, err := svc.Info(context.Background(), "arm_up")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.RenamedTo != "" {
		t.Errorf("live row must not carry renamed_to, got %q", info.RenamedTo)
	}
}

func TestInfo_AliasLookupFailureIsNonFatal(t *testing.T) {
	// The alias fetch is an enhancement: a parse failure (here: empty body)
	// must still return the found row, just without renamed_to.
	mock := &mockFetcher{
		exactResp: []byte(`[{"id":26260,"name":"sailor_suit","category":0,"post_count":0}]`),
	}
	svc := NewTagService(mock, nil)

	info, err := svc.Info(context.Background(), "sailor_suit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "sailor_suit" || info.RenamedTo != "" {
		t.Errorf("expected the row without renamed_to, got: %+v", info)
	}
}

func TestSearchPosts_Fields(t *testing.T) {
	// score/fav_count/large_file_url are present on all sampled posts
	// (verified live); the projection must copy them through.
	mock := &mockFetcher{
		postsResp: []byte(`[{
			"id": 900,
			"rating": "g",
			"score": 42,
			"fav_count": 7,
			"preview_file_url": "https://cdn.donmai.us/preview/x.jpg",
			"large_file_url": "https://cdn.donmai.us/large/x.jpg"
		}]`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.SearchPosts(context.Background(), "touhou", 1, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := res.Posts[0]
	if p.Score != 42 || p.FavCount != 7 {
		t.Errorf("score/fav_count not copied: %+v", p)
	}
	if p.LargeFileURL != "https://cdn.donmai.us/large/x.jpg" {
		t.Errorf("large_file_url not copied: %q", p.LargeFileURL)
	}
	raw, _ := json.Marshal(res.Posts)
	for _, want := range []string{`"score":42`, `"fav_count":7`, `"large_file_url":"https://cdn.donmai.us/large/x.jpg"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %s in payload, got: %s", want, raw)
		}
	}
}

func TestSearchPosts_RejectsLimitOverMax(t *testing.T) {
	// 20 posts bound the payload at ~20 KB (a post carries ~44 tags on
	// average, verified live); limit 21 must fail before any request.
	mock := &mockFetcher{postsResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	_, err := svc.SearchPosts(context.Background(), "1girl", maxPostLimit+1, "")
	if err == nil || !strings.Contains(err.Error(), "too many posts requested") {
		t.Fatalf("expected limit rejection, got: %v", err)
	}
	if mock.lastPostsArg != "" {
		t.Errorf("no request should be sent on limit rejection, got: %s", mock.lastPostsArg)
	}
	if _, err := svc.SearchPosts(context.Background(), "1girl", maxPostLimit, ""); err != nil {
		t.Errorf("limit %d must stay allowed, got: %v", maxPostLimit, err)
	}
}

func TestSearchPosts_NoRatingInjection(t *testing.T) {
	// Pinned on purpose: no layer may pick a rating on the caller's behalf.
	// An injected rating:explicit would bias research queries to a ~5%
	// subpopulation (arm_up is 95% g/s/q — verified live) and contradict
	// get_tag_profile's whole-population sample. The query must go out
	// verbatim; callers opt into a rating with an exempt rating: metatag.
	mock := &mockFetcher{
		postsResp: []byte(`[{"id":100,"rating":"e","preview_file_url":"https://danbooru.donmai.us/sample.jpg"}]`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.SearchPosts(context.Background(), "cat_ears 1girl", 5, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	posts := res.Posts
	if mock.lastPostsArg != "cat_ears 1girl" {
		t.Errorf("query must be sent verbatim with no rating appended, got: %s", mock.lastPostsArg)
	}

	// A caller-supplied rating metatag is kept as-is (nothing to override).
	mock.lastPostsArg = ""
	if _, err := svc.SearchPosts(context.Background(), "cat_ears rating:general", 5, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "cat_ears rating:general" {
		t.Errorf("caller rating must be kept verbatim, got: %s", mock.lastPostsArg)
	}

	if len(posts) != 1 || posts[0].ID != 100 || posts[0].Rating != "e" {
		t.Errorf("parsed post mismatch: %+v", posts)
	}

	// Posts without tag_string_* fields must still serialize every category
	// as an empty array, never null.
	raw, err := json.Marshal(posts)
	if err != nil {
		t.Fatalf("failed to marshal posts: %v", err)
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("post JSON must not contain nulls, got: %s", raw)
	}
}

func TestSearchPosts_ParsesCategorizedTags(t *testing.T) {
	mock := &mockFetcher{
		postsResp: []byte(`[{
			"id": 900,
			"rating": "e",
			"preview_file_url": "https://cdn.donmai.us/preview/x.jpg",
			"tag_string_copyright": "touhou",
			"tag_string_artist": "kantoku",
			"tag_string_character": "hakurei_reimu",
			"tag_string_general": "1girl solo long_hair",
			"tag_string_meta": "highres absurdres"
		}]`),
	}
	svc := NewTagService(mock, nil)

	res, err := svc.SearchPosts(context.Background(), "touhou", 1, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	posts := res.Posts
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	tags := *posts[0].Tags
	if len(tags.Copyright) != 1 || tags.Copyright[0] != "touhou" {
		t.Errorf("unexpected copyright tags: %+v", tags.Copyright)
	}
	if len(tags.Artist) != 1 || tags.Artist[0] != "kantoku" {
		t.Errorf("unexpected artist tags: %+v", tags.Artist)
	}
	if len(tags.Character) != 1 || tags.Character[0] != "hakurei_reimu" {
		t.Errorf("unexpected character tags: %+v", tags.Character)
	}
	if len(tags.General) != 3 || tags.General[0] != "1girl" || tags.General[2] != "long_hair" {
		t.Errorf("unexpected general tags: %+v", tags.General)
	}
	if len(tags.Meta) != 2 || tags.Meta[1] != "absurdres" {
		t.Errorf("unexpected meta tags: %+v", tags.Meta)
	}
	if posts[0].PreviewURL != "https://cdn.donmai.us/preview/x.jpg" {
		t.Errorf("unexpected preview URL: %s", posts[0].PreviewURL)
	}
}

func TestSearchPosts_RejectsTooManyContentTags(t *testing.T) {
	mock := &mockFetcher{postsResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	_, err := svc.SearchPosts(context.Background(), "1girl blue_hair long_hair", 5, "")
	if err == nil || !strings.Contains(err.Error(), "too many content tags") {
		t.Fatalf("expected too-many-tags error, got: %v", err)
	}
	if mock.lastPostsArg != "" {
		t.Errorf("no request should be sent on validation failure, got: %s", mock.lastPostsArg)
	}
}

func TestSearchPosts_MetatagCounting(t *testing.T) {
	mock := &mockFetcher{
		postsResp: []byte(`[{"id":101,"rating":"e","preview_file_url":""}]`),
	}
	svc := NewTagService(mock, nil)

	// rating:/status: are exempt from the limit; the query below is 2 content tags.
	if _, err := svc.SearchPosts(context.Background(), "1girl blue_hair status:any", 5, ""); err != nil {
		t.Fatalf("unexpected error for exempt metatag: %v", err)
	}
	if !strings.Contains(mock.lastPostsArg, "status:any") {
		t.Errorf("metatag must be passed through, got: %s", mock.lastPostsArg)
	}

	// order: counts toward the limit; 2 content tags + order: must be rejected locally.
	mock.lastPostsArg = ""
	if _, err := svc.SearchPosts(context.Background(), "1girl blue_hair order:count", 5, ""); err == nil {
		t.Fatalf("expected too-many-tags error for order: metatag, got nil")
	}
	if mock.lastPostsArg != "" {
		t.Errorf("no request should be sent on validation failure, got: %s", mock.lastPostsArg)
	}
}

// --- Step R4a: min_posts ---

func TestSearch_MinPostsFilters(t *testing.T) {
	mock := &mockFetcher{tagsResp: []byte(`[
		{"id":1,"name":"big","category":0,"post_count":500},
		{"id":2,"name":"small","category":0,"post_count":10},
		{"id":3,"name":"dead","category":0,"post_count":0}
	]`)}
	svc := NewTagService(mock, nil)

	// Default 0 must keep every row (the pre-filter behavior).
	res, err := svc.Search(context.Background(), "tag", 10, -1, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Tags) != 3 {
		t.Errorf("min_posts 0 must keep all rows, got %d", len(res.Tags))
	}

	// Threshold filtering: fewer rows than the limit is valid.
	res, err = svc.Search(context.Background(), "tag", 10, -1, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Tags) != 1 || res.Tags[0].Name != "big" {
		t.Errorf("min_posts 50 must keep only big, got: %+v", res.Tags)
	}

	// min_posts 1 also drops 0-post placeholder rows.
	res, err = svc.Search(context.Background(), "tag", 10, -1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Tags) != 2 {
		t.Errorf("min_posts 1 must drop the 0-post placeholder, got: %+v", res.Tags)
	}
}

// --- Step R4c: search_posts tags modes ---

func TestSearchPosts_TagsModeDefaultFullIsByteIdentical(t *testing.T) {
	// Both the default (empty) and explicit full must marshal the exact
	// historical shape: the tag block present with every category list.
	mock := &mockFetcher{postsResp: []byte(`[{
		"id": 900, "rating": "e", "score": 42, "fav_count": 7,
		"preview_file_url": "https://cdn.donmai.us/preview/x.jpg",
		"large_file_url": "https://cdn.danbooru.us/large/x.jpg",
		"tag_string_copyright": "touhou",
		"tag_string_artist": "kantoku",
		"tag_string_character": "hakurei_reimu",
		"tag_string_general": "1girl solo",
		"tag_string_meta": "highres"
	}]`)}
	svc := NewTagService(mock, nil)

	want := `[{"id":900,"rating":"e","score":42,"fav_count":7,"tags":{"copyright":["touhou"],"artist":["kantoku"],"character":["hakurei_reimu"],"general":["1girl","solo"],"meta":["highres"]},"preview_file_url":"https://cdn.donmai.us/preview/x.jpg","large_file_url":"https://cdn.danbooru.us/large/x.jpg"}]`
	for _, mode := range []string{"", "full"} {
		res, err := svc.SearchPosts(context.Background(), "touhou", 1, mode)
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		raw, err := json.Marshal(res.Posts)
		if err != nil {
			t.Fatalf("mode %q: marshal: %v", mode, err)
		}
		if string(raw) != want {
			t.Errorf("mode %q: expected the historical full shape, got: %s", mode, raw)
		}
	}
}

func TestSearchPosts_TagsModeNoneOmitsTagLists(t *testing.T) {
	mock := &mockFetcher{postsResp: []byte(`[{
		"id": 900, "rating": "e", "score": 42, "fav_count": 7,
		"preview_file_url": "https://cdn.donmai.us/preview/x.jpg",
		"large_file_url": "https://cdn.donmai.us/large/x.jpg",
		"tag_string_general": "1girl solo"
	}]`)}
	svc := NewTagService(mock, nil)

	res, err := svc.SearchPosts(context.Background(), "touhou", 1, "none")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	posts := res.Posts
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	if posts[0].Tags != nil {
		t.Errorf("none mode must drop the tag lists, got: %+v", posts[0].Tags)
	}
	// Identity, rating, popularity counters and URLs must survive.
	raw, _ := json.Marshal(posts)
	for _, want := range []string{`"id":900`, `"rating":"e"`, `"score":42`, `"fav_count":7`, `"large_file_url"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("none mode must keep %s, got: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"tags"`) {
		t.Errorf("none mode must omit the tags key entirely, got: %s", raw)
	}
}

func TestSearchPosts_TagsModeDiffSubtractsQueryContentTags(t *testing.T) {
	mock := &mockFetcher{postsResp: []byte(`[{
		"id": 900, "rating": "g",
		"tag_string_copyright": "touhou project",
		"tag_string_character": "hakurei_reimu",
		"tag_string_general": "arm_up 1girl solo",
		"tag_string_meta": "highres"
	}]`)}
	svc := NewTagService(mock, nil)

	// The query mixes case and a metatag: only arm_up is subtracted,
	// rating:g never names a literal tag so it cannot be.
	res, err := svc.SearchPosts(context.Background(), "Arm_Up rating:g", 1, "diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	posts := res.Posts
	if len(posts) != 1 || posts[0].Tags == nil {
		t.Fatalf("diff mode must keep the tag block, got: %+v", posts)
	}
	tags := *posts[0].Tags
	if fmt.Sprint(tags.General) != fmt.Sprint([]string{"1girl", "solo"}) {
		t.Errorf("general must be [1girl solo], got: %v", tags.General)
	}
	if fmt.Sprint(tags.Copyright) != fmt.Sprint([]string{"touhou", "project"}) {
		t.Errorf("copyright must be untouched, got: %v", tags.Copyright)
	}
	if fmt.Sprint(tags.Character) != fmt.Sprint([]string{"hakurei_reimu"}) {
		t.Errorf("character must be untouched, got: %v", tags.Character)
	}
	if fmt.Sprint(tags.Meta) != fmt.Sprint([]string{"highres"}) {
		t.Errorf("meta must be untouched, got: %v", tags.Meta)
	}
}

func TestSearchPosts_TagsModeInvalid(t *testing.T) {
	mock := &mockFetcher{postsResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	_, err := svc.SearchPosts(context.Background(), "1girl", 5, "bogus")
	if err == nil || !strings.Contains(err.Error(), "invalid tags mode") {
		t.Fatalf("expected mode rejection, got: %v", err)
	}
	if mock.lastPostsArg != "" {
		t.Errorf("no request should be sent on validation failure, got: %s", mock.lastPostsArg)
	}
}
