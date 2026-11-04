package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"danbooru-tag-mcp/internal/cache"
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

	// stale makes every fetch report a stale-marked result (CachedAt
	// = staleAt), pinning how provenance flows into response marks.
	stale   bool
	staleAt time.Time

	aliasCalls int   // number of FetchAlias invocations
	aliasErr   error // returned by FetchAlias instead of a body

	lastTagsArg      string
	lastTagsCategory int
	lastNamesArg     []string
	lastWikiTitle    string
	lastWikiOther    string
	lastPostsArg     string
	lastPostsLim     int
}

// result wraps a canned body with the mock's provenance settings.
func (m *mockFetcher) result(body []byte) cache.Result {
	return cache.Result{Body: body, Stale: m.stale, CachedAt: m.staleAt}
}

func (m *mockFetcher) FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool, category int) (cache.Result, error) {
	m.lastTagsArg = namePattern
	m.lastTagsCategory = category
	return m.result(m.tagsResp), nil
}

func (m *mockFetcher) FetchTagExact(ctx context.Context, name string) (cache.Result, error) {
	return m.result(m.exactResp), nil
}

func (m *mockFetcher) FetchTagsByNames(ctx context.Context, names []string) (cache.Result, error) {
	m.lastNamesArg = names
	return m.result(m.namesResp), nil
}

func (m *mockFetcher) FetchRelated(ctx context.Context, tag string) (cache.Result, error) {
	return m.result(m.relatedResp), nil
}

func (m *mockFetcher) FetchAlias(ctx context.Context, name string) (cache.Result, error) {
	m.aliasCalls++
	if m.aliasErr != nil {
		return cache.Result{}, m.aliasErr
	}
	return m.result(m.aliasResp), nil
}

func (m *mockFetcher) FetchWiki(ctx context.Context, title, otherNames string, limit int) (cache.Result, error) {
	m.lastWikiTitle = title
	m.lastWikiOther = otherNames
	return m.result(m.wikiResp), nil
}

func (m *mockFetcher) FetchPosts(ctx context.Context, tags string, limit int) (cache.Result, error) {
	m.lastPostsArg = tags
	m.lastPostsLim = limit
	return m.result(m.postsResp), nil
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

	posts, err := svc.SearchPosts(context.Background(), "touhou", 1, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := posts[0]
	if p.Score != 42 || p.FavCount != 7 {
		t.Errorf("score/fav_count not copied: %+v", p)
	}
	if p.LargeFileURL != "https://cdn.donmai.us/large/x.jpg" {
		t.Errorf("large_file_url not copied: %q", p.LargeFileURL)
	}
	raw, _ := json.Marshal(posts)
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

	posts, err := svc.SearchPosts(context.Background(), "cat_ears 1girl", 5, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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

	posts, err := svc.SearchPosts(context.Background(), "touhou", 1, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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

// --- Step R1 visible half: stale marks on cacheable responses ---

func TestCacheableResponses_StaleMarksMarshaled(t *testing.T) {
	// One canned body per path, live rows so no alias fetch intervenes.
	bodies := func(m *mockFetcher) {
		m.tagsResp = []byte(`[{"id":1,"name":"blue_hair","category":0,"post_count":1200000}]`)
		m.exactResp = []byte(`[{"id":1,"name":"blue_hair","category":0,"post_count":1200000}]`)
		m.namesResp = []byte(`[{"id":1,"name":"blue_hair","category":0,"post_count":1200000}]`)
		m.relatedResp = []byte(`{"query":"blue_hair","related_tags":[]}`)
		m.aliasResp = []byte(`[{"antecedent_name":"sailor_suit","consequent_name":"sailor","status":"active"}]`)
		m.wikiResp = []byte(`[{"title":"arm_up","body":"Body.","other_names":[],"is_deleted":false}]`)
	}
	cases := []struct {
		name string
		call func(svc *TagService) (any, error)
	}{
		{"search_tags", func(svc *TagService) (any, error) { return svc.Search(context.Background(), "blue hair", 10, -1, 0) }},
		{"get_tag_info", func(svc *TagService) (any, error) { return svc.Info(context.Background(), "blue_hair") }},
		{"get_tags_info", func(svc *TagService) (any, error) { return svc.Infos(context.Background(), []string{"blue_hair"}) }},
		{"get_related_tags", func(svc *TagService) (any, error) { return svc.Related(context.Background(), "blue_hair", 10) }},
		{"get_tag_alias", func(svc *TagService) (any, error) { return svc.Alias(context.Background(), "sailor_suit") }},
		{"get_tag_wiki", func(svc *TagService) (any, error) { return svc.Wiki(context.Background(), "arm_up", "", 5, false) }},
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh network/cache path: both markers absent (the transparent
			// fresh-hit contract).
			freshMock := &mockFetcher{}
			bodies(freshMock)
			got, err := tc.call(NewTagService(freshMock, nil))
			if err != nil {
				t.Fatalf("fresh path: %v", err)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), `"stale":`) || strings.Contains(string(raw), `"cached_at":`) {
				t.Errorf("fresh response must omit both markers, got: %s", raw)
			}

			// Stale fallback: both markers present with the entry's storage
			// time in RFC3339.
			staleMock := &mockFetcher{stale: true, staleAt: at}
			bodies(staleMock)
			got, err = tc.call(NewTagService(staleMock, nil))
			if err != nil {
				t.Fatalf("stale path: %v", err)
			}
			raw, _ = json.Marshal(got)
			for _, want := range []string{`"stale":true`, `"cached_at":"2026-11-01T12:00:00Z"`} {
				if !strings.Contains(string(raw), want) {
					t.Errorf("stale response must carry %s, got: %s", want, raw)
				}
			}
		})
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
		posts, err := svc.SearchPosts(context.Background(), "touhou", 1, mode)
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		raw, err := json.Marshal(posts)
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

	posts, err := svc.SearchPosts(context.Background(), "touhou", 1, "none")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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
	posts, err := svc.SearchPosts(context.Background(), "Arm_Up rating:g", 1, "diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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
