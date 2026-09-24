package service

import (
	"context"
	"strings"
	"testing"
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
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

	_, err := svc.Info(context.Background(), "non_existent_tag")
	if err == nil || !strings.Contains(err.Error(), "tag not found") {
		t.Fatalf("expected 'tag not found' error, got: %v", err)
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
				{"tag": {"name": "highres", "post_count": 8211186, "category": 5}, "cosine_similarity": 0.261, "frequency": 0.6828}
			],
			"wiki_page_tags": [{"name": "aqua_hair", "post_count": 179981, "category": 0}]
		}`),
	}
	svc := NewTagService(mock)

	rel, err := svc.Related(context.Background(), "blue_hair", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rel) != 1 {
		t.Fatalf("expected 1 related tag (query tag itself skipped), got %d", len(rel))
	}
	if rel[0].Tag != "highres" || rel[0].Category != 5 || rel[0].PostCount != 8211186 {
		t.Errorf("unexpected tag fields: %+v", rel[0])
	}
	if rel[0].Similarity != 0.261 || rel[0].Frequency != 0.6828 {
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
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

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
}

func TestSearchPosts_RejectsTooManyContentTags(t *testing.T) {
	mock := &mockFetcher{postsResp: []byte(`[]`)}
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

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
	svc := NewTagService(mock)

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
