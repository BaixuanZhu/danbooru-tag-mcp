// Package service implements Danbooru tag/post business logic on top of the
// api layer: tag search/info, related tags, and safe post search.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"danbooru-tag-mcp/internal/vocab"
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

// TagAlias maps a user-typed alias (antecedent) to the canonical Danbooru tag
// (consequent) it was merged into.
type TagAlias struct {
	Antecedent string `json:"antecedent"`
	Consequent string `json:"consequent"`
}

// WikiPage is a Danbooru wiki entry for a tag, with the [[tag]] links
// extracted from the DText body (appearance traits, related concepts).
type WikiPage struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	OtherNames []string `json:"other_names"`
	LinkedTags []string `json:"linked_tags"`
}

// PostTags is one post's tag list split by Danbooru category (sidebar order),
// so consumers can pick identity tags and content tags without re-parsing.
type PostTags struct {
	Copyright []string `json:"copyright"`
	Artist    []string `json:"artist"`
	Character []string `json:"character"`
	General   []string `json:"general"`
	Meta      []string `json:"meta"`
}

// Post is a trimmed search result entry: identity, rating, the full tag list
// by category, and a preview URL for a quick visual check.
type Post struct {
	ID         int      `json:"id"`
	Rating     string   `json:"rating"`
	Tags       PostTags `json:"tags"`
	PreviewURL string   `json:"preview_file_url"`
}

// TagFetcher decouples the service from the concrete api.Client for mock-based unit tests
type TagFetcher interface {
	FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool) ([]byte, error)
	FetchTagExact(ctx context.Context, name string) ([]byte, error)
	FetchRelated(ctx context.Context, tag string) ([]byte, error)
	FetchAlias(ctx context.Context, name string) ([]byte, error)
	FetchWiki(ctx context.Context, title, otherNames string, limit int) ([]byte, error)
	FetchPosts(ctx context.Context, tags string, limit int) ([]byte, error)
}

// WD14 status values: how a name stands relative to current Danbooru and
// the pinned WD14 tagger vocabularies (internal/vocab).
const (
	wd14Live      = "live"       // current Danbooru carries the name
	wd14TaggerEra = "tagger_era" // only the tagger vocabularies carry it (likely renamed on Danbooru)
	wd14Unknown   = "unknown"    // neither side carries it (treat as a made-up word)
)

// WD14Status classifies a name against the pinned WD14 tagger vocabularies.
// It is computed locally, so it is available even when the Danbooru API
// request itself fails.
type WD14Status struct {
	Status  string         `json:"status"`
	Sources []vocab.Source `json:"sources,omitempty"` // snapshot record per vocabulary, when any carries the name
}

// TagInfo is get_tag_info's payload: the live tag row (embedded, so the
// found-response shape stays flat) plus the WD14 verdict.
type TagInfo struct {
	Tag
	WD14 WD14Status `json:"wd14"`
}

// NotFoundError reports that current Danbooru carries no exact match for
// the name; the WD14 verdict for it is still computable locally.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string { return "tag not found: " + e.Name }

type TagService struct {
	fetcher TagFetcher
	vocab   *vocab.Store // nil disables WD14 enrichment
}

func NewTagService(fetcher TagFetcher, vd *vocab.Store) *TagService {
	return &TagService{fetcher: fetcher, vocab: vd}
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

// Info resolves a name to its exact tag row plus the WD14 verdict. A
// *NotFoundError means current Danbooru no longer carries the name — the
// caller can still classify it locally via WD14Info.
func (s *TagService) Info(ctx context.Context, name string) (*TagInfo, error) {
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
		return nil, &NotFoundError{Name: cleanName}
	}
	// A returned row with 0 posts is a placeholder (renamed-away antecedent,
	// unused tag — verified live: barefoot_sandals, gold_footwear and even
	// painted_toenails all return rows). It does not make the name live.
	return &TagInfo{Tag: tags[0], WD14: s.wd14Status(cleanName, tags[0].PostCount > 0)}, nil
}

// WD14Info classifies a name the caller already found absent from current
// Danbooru: tagger_era when a pinned tagger vocabulary carries it, unknown
// otherwise. Local computation, no request.
func (s *TagService) WD14Info(name string) WD14Status {
	return s.wd14Status(normalizeTag(name), false)
}

// TaggerVocabHits returns tagger vocabulary names containing the query
// substring, best snapshot count first — the search_tags fallback when
// Danbooru returns nothing. Local computation, no request.
func (s *TagService) TaggerVocabHits(query string, limit int) []vocab.Hit {
	return s.vocab.Substring(query, limit)
}

// wd14Status derives the verdict from a vocab lookup plus whether current
// Danbooru carries the name: a hit is live when Danbooru does, tagger_era
// when only the vocabularies do; a miss is live or unknown accordingly.
func (s *TagService) wd14Status(name string, live bool) WD14Status {
	hit, ok := s.vocab.Lookup(name)
	if !ok {
		if live {
			return WD14Status{Status: wd14Live}
		}
		return WD14Status{Status: wd14Unknown}
	}
	if live {
		return WD14Status{Status: wd14Live, Sources: hit.Sources}
	}
	return WD14Status{Status: wd14TaggerEra, Sources: hit.Sources}
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

// danbooruCategoryMeta is the category id of meta tags (highres, absurdres,
// commentary, ...) as returned by tags.json / related_tag.json.
const danbooruCategoryMeta = 5

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
		// Meta tags co-occur with nearly every upload and would fill the
		// top-N with prompt-irrelevant noise; drop them.
		if entry.Tag.Category == danbooruCategoryMeta {
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

// Alias resolves a user-typed alias to its canonical Danbooru tag via
// /tag_aliases.json. Returns nil when no active alias exists, meaning the
// input is likely already canonical.
func (s *TagService) Alias(ctx context.Context, name string) (*TagAlias, error) {
	cleanName := normalizeTag(name)
	body, err := s.fetcher.FetchAlias(ctx, cleanName)
	if err != nil {
		return nil, err
	}

	var aliases []danbooruAliasResponse
	if err := json.Unmarshal(body, &aliases); err != nil {
		return nil, fmt.Errorf("failed to parse tag alias response: %w", err)
	}
	if len(aliases) == 0 {
		return nil, nil
	}
	return &TagAlias{Antecedent: aliases[0].AntecedentName, Consequent: aliases[0].ConsequentName}, nil
}

type danbooruAliasResponse struct {
	AntecedentName string `json:"antecedent_name"`
	ConsequentName string `json:"consequent_name"`
	Status         string `json:"status"`
}

type danbooruWikiResponse struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	OtherNames []string `json:"other_names"`
	IsDeleted  bool     `json:"is_deleted"`
}

// Wiki fetches wiki pages by exact title, or by multilingual other-names
// substring when title is empty. The DText [[tag]] links of each body are
// extracted into LinkedTags for direct prompt assembly.
func (s *TagService) Wiki(ctx context.Context, title, otherNames string, limit int) ([]WikiPage, error) {
	cleanTitle := normalizeTag(title)
	cleanOther := ""
	if cleanTitle == "" {
		cleanOther = strings.TrimSpace(otherNames)
	}

	body, err := s.fetcher.FetchWiki(ctx, cleanTitle, cleanOther, limit)
	if err != nil {
		return nil, err
	}

	var pages []danbooruWikiResponse
	if err := json.Unmarshal(body, &pages); err != nil {
		return nil, fmt.Errorf("failed to parse wiki response: %w", err)
	}

	results := make([]WikiPage, 0, len(pages))
	for _, p := range pages {
		if p.OtherNames == nil {
			p.OtherNames = []string{}
		}
		results = append(results, WikiPage{
			Title:      p.Title,
			Body:       p.Body,
			OtherNames: p.OtherNames,
			LinkedTags: extractLinkedTags(p.Body),
		})
	}
	return results, nil
}

// wikiLinkRe matches DText links: [[tag_name]] or [[tag_name|display text]].
var wikiLinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]*)?\]\]`)

// extractLinkedTags returns the unique [[tag]] link targets of a DText body,
// in order of first appearance.
func extractLinkedTags(body string) []string {
	seen := make(map[string]bool)
	var tags []string
	for _, m := range wikiLinkRe.FindAllStringSubmatch(body, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tags = append(tags, name)
	}
	return tags
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

// danbooruPost is the subset of /posts.json entries the service consumes;
// the tag_string_* fields are space-separated tag names grouped by category.
type danbooruPost struct {
	ID                 int    `json:"id"`
	Rating             string `json:"rating"`
	PreviewFileURL     string `json:"preview_file_url"`
	TagStringCopyright string `json:"tag_string_copyright"`
	TagStringArtist    string `json:"tag_string_artist"`
	TagStringCharacter string `json:"tag_string_character"`
	TagStringGeneral   string `json:"tag_string_general"`
	TagStringMeta      string `json:"tag_string_meta"`
}

// parsePostTags splits the raw tag_string_* fields into per-category lists.
// strings.Fields of an empty string yields an empty non-nil slice, so absent
// categories marshal as [] rather than null.
func parsePostTags(p danbooruPost) PostTags {
	return PostTags{
		Copyright: strings.Fields(p.TagStringCopyright),
		Artist:    strings.Fields(p.TagStringArtist),
		Character: strings.Fields(p.TagStringCharacter),
		General:   strings.Fields(p.TagStringGeneral),
		Meta:      strings.Fields(p.TagStringMeta),
	}
}

// SearchPosts sends the tags out verbatim: no layer injects a rating
// filter. The caller pins rating:<x> when they want one (rating: metatags
// are exempt from the tag-count limit), so an unfiltered query describes
// the tag's whole population — the same whole-population rule
// get_tag_profile samples by.
func (s *TagService) SearchPosts(ctx context.Context, tags string, limit int) ([]Post, error) {
	query := strings.TrimSpace(tags)
	if n := countContentTags(query); n > maxContentTags {
		return nil, fmt.Errorf("too many content tags: %d (max %d, free/anonymous Danbooru limit; rating: and most other metatags do not count, order: does); drop tags or split the query", n, maxContentTags)
	}

	body, err := s.fetcher.FetchPosts(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	var rawPosts []danbooruPost
	if err := json.Unmarshal(body, &rawPosts); err != nil {
		return nil, fmt.Errorf("failed to parse posts: %w", err)
	}
	posts := make([]Post, 0, len(rawPosts))
	for _, p := range rawPosts {
		posts = append(posts, Post{
			ID:         p.ID,
			Rating:     p.Rating,
			Tags:       parsePostTags(p),
			PreviewURL: p.PreviewFileURL,
		})
	}
	return posts, nil
}
