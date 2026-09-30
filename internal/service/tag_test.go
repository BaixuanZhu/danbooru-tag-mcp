package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/vocab"
)

type mockFetcher struct {
	tagsResp    []byte
	exactResp   []byte
	relatedResp []byte
	aliasResp   []byte
	wikiResp    []byte
	postsResp   []byte

	lastTagsArg   string
	lastWikiTitle string
	lastWikiOther string
	lastPostsArg  string
}

func (m *mockFetcher) FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool) ([]byte, error) {
	m.lastTagsArg = namePattern
	return m.tagsResp, nil
}

func (m *mockFetcher) FetchTagExact(ctx context.Context, name string) ([]byte, error) {
	return m.exactResp, nil
}

func (m *mockFetcher) FetchRelated(ctx context.Context, tag string) ([]byte, error) {
	return m.relatedResp, nil
}

func (m *mockFetcher) FetchAlias(ctx context.Context, name string) ([]byte, error) {
	return m.aliasResp, nil
}

func (m *mockFetcher) FetchWiki(ctx context.Context, title, otherNames string, limit int) ([]byte, error) {
	m.lastWikiTitle = title
	m.lastWikiOther = otherNames
	return m.wikiResp, nil
}

func (m *mockFetcher) FetchPosts(ctx context.Context, tags string, limit int) ([]byte, error) {
	m.lastPostsArg = tags
	return m.postsResp, nil
}

func TestSearch_ParsesTags(t *testing.T) {
	mock := &mockFetcher{
		tagsResp: []byte(`[{"id":1,"name":"blue_hair","category":0,"post_count":500}]`),
	}
	svc := NewTagService(mock, nil)

	tags, err := svc.Search(context.Background(), "blue hair", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastTagsArg != "blue_hair" {
		t.Errorf("expected normalized query 'blue_hair', got '%s'", mock.lastTagsArg)
	}
	if len(tags) != 1 || tags[0].Name != "blue_hair" || tags[0].PostCount != 500 {
		t.Errorf("parsed tag mismatch: %+v", tags)
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
		era.Sources[0].Vocab != vocab.MoatV2 || era.Sources[0].Count != 931 {
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

	rel, err := svc.Related(context.Background(), "blue_hair", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The query tag itself (similarity 1.0) and meta-category entries
	// (highres, category 5) must both be dropped; the limit is spent on
	// content tags only.
	if len(rel) != 1 {
		t.Fatalf("expected 1 related tag (self + meta dropped), got %d", len(rel))
	}
	if rel[0].Tag != "aqua_hair" || rel[0].Category != 0 || rel[0].PostCount != 179981 {
		t.Errorf("unexpected tag fields: %+v", rel[0])
	}
	if rel[0].Similarity != 0.45 || rel[0].Frequency != 0.11 {
		t.Errorf("unexpected similarity metrics: %+v", rel[0])
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

	rel, err := svc.Related(context.Background(), "solo", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rel) != 2 {
		t.Fatalf("expected 2 tags truncated by limit, got %d", len(rel))
	}
	if rel[0].Tag != "1girl" || rel[1].Tag != "looking_at_viewer" {
		t.Errorf("unexpected content: %+v", rel)
	}
}

func TestAlias_ParsesMapping(t *testing.T) {
	mock := &mockFetcher{
		aliasResp: []byte(`[{"id":7315,"antecedent_name":"sailor_suit","consequent_name":"sailor","status":"active"}]`),
	}
	svc := NewTagService(mock, nil)

	alias, err := svc.Alias(context.Background(), "sailor suit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alias == nil || alias.Antecedent != "sailor_suit" || alias.Consequent != "sailor" {
		t.Fatalf("unexpected alias: %+v", alias)
	}
}

func TestAlias_NoActiveAliasReturnsNull(t *testing.T) {
	mock := &mockFetcher{aliasResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	alias, err := svc.Alias(context.Background(), "blue_hair")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alias != nil {
		t.Fatalf("expected nil alias for canonical input, got: %+v", alias)
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

	pages, err := svc.Wiki(context.Background(), "firefly_(honkai:_star_rail)", "", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(pages))
	}
	p := pages[0]
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

	if _, err := svc.Wiki(context.Background(), "", "流萤", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastWikiTitle != "" || mock.lastWikiOther != "流萤" {
		t.Errorf("other-names mode must pass the alias only, got title=%q other=%q", mock.lastWikiTitle, mock.lastWikiOther)
	}
}

func TestSearchPosts_DefaultsToExplicitRating(t *testing.T) {
	mock := &mockFetcher{
		postsResp: []byte(`[{"id":100,"rating":"e","preview_file_url":"https://danbooru.donmai.us/sample.jpg"}]`),
	}
	svc := NewTagService(mock, nil)

	posts, err := svc.SearchPosts(context.Background(), "cat_ears 1girl", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(mock.lastPostsArg, "rating:explicit") {
		t.Errorf("expected tags to include rating:explicit, got: %s", mock.lastPostsArg)
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

	posts, err := svc.SearchPosts(context.Background(), "touhou", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	tags := posts[0].Tags
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

	_, err := svc.SearchPosts(context.Background(), "1girl blue_hair long_hair", 5)
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
	if _, err := svc.SearchPosts(context.Background(), "1girl blue_hair status:any", 5); err != nil {
		t.Fatalf("unexpected error for exempt metatag: %v", err)
	}
	if !strings.Contains(mock.lastPostsArg, "status:any") {
		t.Errorf("metatag must be passed through, got: %s", mock.lastPostsArg)
	}

	// order: counts toward the limit; 2 content tags + order: must be rejected locally.
	mock.lastPostsArg = ""
	if _, err := svc.SearchPosts(context.Background(), "1girl blue_hair order:count", 5); err == nil {
		t.Fatalf("expected too-many-tags error for order: metatag, got nil")
	}
	if mock.lastPostsArg != "" {
		t.Errorf("no request should be sent on validation failure, got: %s", mock.lastPostsArg)
	}
}

func TestSearchPosts_KeepsCallerRating(t *testing.T) {
	mock := &mockFetcher{postsResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	if _, err := svc.SearchPosts(context.Background(), "cat_ears rating:general", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(mock.lastPostsArg, "rating:explicit") {
		t.Errorf("caller rating must not be overridden, got: %s", mock.lastPostsArg)
	}
	if !strings.Contains(mock.lastPostsArg, "rating:general") {
		t.Errorf("caller rating must be kept, got: %s", mock.lastPostsArg)
	}
}
