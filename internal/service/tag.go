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

// RelatedTag is one co-occurring tag from /related_tag.json, with the
// similarity metrics Danbooru reports for the pair.
type RelatedTag struct {
	Tag        string  `json:"tag"`
	Category   int     `json:"category"`   // 0=general, 1=artist, 3=copyright, 4=character, 5=meta
	PostCount  int     `json:"post_count"` // total posts carrying this related tag
	Similarity float64 `json:"similarity"` // cosine similarity to the query tag (0-1)
	Frequency  float64 `json:"frequency"`  // share of query-tag posts that also carry this tag (0-1)
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

// danbooruRelatedTag is the nested tag object inside a related_tags entry.
type danbooruRelatedTag struct {
	Name      string `json:"name"`
	Category  int    `json:"category"`
	PostCount int    `json:"post_count"`
}

// danbooruRelatedEntry is one element of the related_tags array. Danbooru
// revamped /related_tag.json around 2024: entries stopped being [name, count]
// pairs and became objects with the tag nested under "tag" plus similarity
// metrics (cosine_similarity, frequency, ...).
type danbooruRelatedEntry struct {
	Tag        danbooruRelatedTag `json:"tag"`
	Similarity float64            `json:"cosine_similarity"`
	Frequency  float64            `json:"frequency"`
}

type danbooruRelatedResponse struct {
	Query       string                 `json:"query"`
	RelatedTags []danbooruRelatedEntry `json:"related_tags"`
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
	for _, entry := range resp.RelatedTags {
		// The API lists the query tag itself first (similarity 1.0); skip it
		// so the limit is spent on actual co-occurring tags.
		if entry.Tag.Name == resp.Query {
			continue
		}
		results = append(results, RelatedTag{
			Tag:        entry.Tag.Name,
			Category:   entry.Tag.Category,
			PostCount:  entry.Tag.PostCount,
			Similarity: entry.Similarity,
			Frequency:  entry.Frequency,
		})
	}

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// hasRatingMetatag reports whether the caller already pinned a rating filter
// (e.g. "rating:g") so SearchPosts does not override their choice.
func hasRatingMetatag(tags string) bool {
	for _, field := range strings.Fields(tags) {
		if strings.HasPrefix(field, "rating:") {
			return true
		}
	}
	return false
}

// maxContentTags caps content tags per post search: free/anonymous Danbooru
// accounts allow 2 tags per query (Gold unlocks 6).
const maxContentTags = 2

// countContentTags counts fields that count toward Danbooru's per-account
// tag limit: content tags (no ':') plus order: metatags. Other metatags
// (rating:, status:, id:, ...) are exempt — verified empirically against
// danbooru.donmai.us, which 422s on "a b order:count" but not on
// "a b status:any" for anonymous accounts.
func countContentTags(tags string) int {
	count := 0
	for _, field := range strings.Fields(tags) {
		if !strings.Contains(field, ":") || strings.HasPrefix(field, "order:") {
			count++
		}
	}
	return count
}

// SearchPosts core business rule: default to rating:explicit (R-18 allowed);
// a rating:<x> metatag supplied by the caller is kept as-is.
func (s *TagService) SearchPosts(ctx context.Context, tags string, limit int) ([]Post, error) {
	query := strings.TrimSpace(tags)
	if n := countContentTags(query); n > maxContentTags {
		return nil, fmt.Errorf("too many content tags: %d (max %d, free/anonymous Danbooru limit; rating: and most other metatags do not count, order: does); drop tags or split the query", n, maxContentTags)
	}
	if !hasRatingMetatag(query) {
		if query == "" {
			query = "rating:explicit"
		} else {
			query = fmt.Sprintf("%s rating:explicit", query)
		}
	}

	body, err := s.fetcher.FetchPosts(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	var posts []Post
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("failed to parse posts: %w", err)
	}
	return posts, nil
}
