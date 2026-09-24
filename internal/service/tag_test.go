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
	postsResp   []byte

	lastTagsArg  string
	lastPostsArg string
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

func TestRelated_TruncatesToLimit(t *testing.T) {
	mock := &mockFetcher{
		relatedResp: []byte(`{
			"query": "solo",
			"related_tags": [
				["solo", 1000],
				["1girl", 900],
				["looking_at_viewer", 800]
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
	if rel[0].Tag != "solo" || rel[1].Tag != "1girl" {
		t.Errorf("unexpected content: %+v", rel)
	}
}

func TestSearchPosts_AppendsRatingSafe(t *testing.T) {
	mock := &mockFetcher{
		postsResp: []byte(`[{"id":100,"rating":"g","preview_file_url":"https://danbooru.donmai.us/sample.jpg"}]`),
	}
	svc := NewTagService(mock)

	posts, err := svc.SearchPosts(context.Background(), "cat_ears 1girl", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(mock.lastPostsArg, "rating:general") {
		t.Errorf("expected tags to include rating:general, got: %s", mock.lastPostsArg)
	}
	if len(posts) != 1 || posts[0].ID != 100 || posts[0].Rating != "g" {
		t.Errorf("parsed post mismatch: %+v", posts)
	}
}
