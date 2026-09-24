// Package service implements Danbooru tag/post business logic on top of the
// api layer: tag search/info, related tags, and safe post search.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Tag struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Category  int    `json:"category"` // 0=general, 1=artist, 3=copyright, 4=character, 5=meta
	PostCount int    `json:"post_count"`
}

type RelatedTag struct {
	Tag     string `json:"tag"`
	Overlap int    `json:"overlap"`
}

type Post struct {
	ID         int    `json:"id"`
	Rating     string `json:"rating"`
	PreviewURL string `json:"preview_file_url"`
}

// TagFetcher decouples the service from the concrete api.Client for mock-based unit tests
type TagFetcher interface {
	FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool) ([]byte, error)
	FetchTagExact(ctx context.Context, name string) ([]byte, error)
	FetchRelated(ctx context.Context, tag string) ([]byte, error)
	FetchPosts(ctx context.Context, tags string, limit int) ([]byte, error)
}

type TagService struct {
	fetcher TagFetcher
}

func NewTagService(fetcher TagFetcher) *TagService {
	return &TagService{fetcher: fetcher}
}

func normalizeTag(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "_")
}

func (s *TagService) Search(ctx context.Context, query string, limit int) ([]Tag, error) {
	cleanQuery := normalizeTag(query)
	body, err := s.fetcher.FetchTags(ctx, cleanQuery, limit, true)
	if err != nil {
		return nil, err
	}

	var tags []Tag
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse tags response: %w", err)
	}
	return tags, nil
}

func (s *TagService) Info(ctx context.Context, name string) (*Tag, error) {
	cleanName := normalizeTag(name)
	body, err := s.fetcher.FetchTagExact(ctx, cleanName)
	if err != nil {
		return nil, err
	}

	var tags []Tag
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse tag info response: %w", err)
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("tag not found: %s", cleanName)
	}
	return &tags[0], nil
}

type danbooruRelatedResponse struct {
	Query       string              `json:"query"`
	RelatedTags [][]json.RawMessage `json:"related_tags"`
}

func (s *TagService) Related(ctx context.Context, tag string, limit int) ([]RelatedTag, error) {
	cleanTag := normalizeTag(tag)
	body, err := s.fetcher.FetchRelated(ctx, cleanTag)
	if err != nil {
		return nil, err
	}

	var resp danbooruRelatedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse related tags: %w", err)
	}

	var results []RelatedTag
	for _, pair := range resp.RelatedTags {
		if len(pair) < 2 {
			continue
		}
		var tagName string
		var overlap int
		if err := json.Unmarshal(pair[0], &tagName); err != nil {
			continue
		}
		if err := json.Unmarshal(pair[1], &overlap); err != nil {
			continue
		}
		results = append(results, RelatedTag{Tag: tagName, Overlap: overlap})
	}

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// SearchPosts core business rule: force safe content by appending rating:general
func (s *TagService) SearchPosts(ctx context.Context, tags string, limit int) ([]Post, error) {
	safeTags := strings.TrimSpace(tags)
	if safeTags == "" {
		safeTags = "rating:general"
	} else {
		safeTags = fmt.Sprintf("%s rating:general", safeTags)
	}

	body, err := s.fetcher.FetchPosts(ctx, safeTags, limit)
	if err != nil {
		return nil, err
	}

	var posts []Post
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("failed to parse posts: %w", err)
	}
	return posts, nil
}
