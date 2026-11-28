// Package service implements Danbooru tag/post business logic on top of the
// api layer: tag search/info, related tags, and safe post search.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"danbooru-tag-mcp/internal/vocab"
)

type Tag struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Category  int    `json:"category"` // 0=general, 1=artist, 3=copyright, 4=character, 5=meta
	PostCount int    `json:"post_count"`
}

// RelatedTag is one co-occurring tag from /related_tag.json, with the
// similarity metrics Danbooru reports for the pair and, whenever the
// whole-site post count is available, the tag's lift.
type RelatedTag struct {
	Tag        string   `json:"tag"`
	Category   int      `json:"category"`       // 0=general, 1=artist, 3=copyright, 4=character, 5=meta
	PostCount  int      `json:"post_count"`     // total posts carrying this related tag
	Similarity float64  `json:"similarity"`     // cosine similarity to the query tag (0-1)
	Frequency  float64  `json:"frequency"`      // share of query-tag posts that also carry this tag (0-1)
	Lift       *float64 `json:"lift,omitempty"` // frequency x site-wide post count / post_count, 2 decimals; see relatedLift
}

// TagAlias maps a user-typed alias (antecedent) to the canonical Danbooru tag
// (consequent) it was merged into.
type TagAlias struct {
	Antecedent string `json:"antecedent"`
	Consequent string `json:"consequent"`
}

// WikiPage is a Danbooru wiki entry distilled into structured fields: the
// DText body is parsed once by deterministic extraction and dropped (no
// raw-body fallback, by design decision). Lists are always non-nil so they
// marshal as [] rather than null.
type WikiPage struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`               // first intro paragraph, links flattened
	Description  string   `json:"description,omitempty"` // full intro prose; only with include_description
	IntroTags    []string `json:"intro_tags"`            // link targets in the intro (definition section)
	Rules        []string `json:"rules"`                 // directive sentences (disambiguation, usage)
	SeeAlso      []string `json:"see_also"`              // link targets under a See also heading
	ExamplePosts []int    `json:"example_posts"`         // !post ids illustrating the tag
	LinkedTags   []string `json:"linked_tags"`           // union of all link targets (unchanged shape)
	OtherNames   []string `json:"other_names"`
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

// Post is a trimmed search result entry: identity, rating, popularity
// counters, the full tag list by category, and image URLs (preview plus the
// large version, which is big enough to actually inspect). Tags is nil
// (omitted from JSON) in the none tags mode; every other mode carries it.
type Post struct {
	ID           int       `json:"id"`
	Rating       string    `json:"rating"`
	Score        int       `json:"score"`
	FavCount     int       `json:"fav_count"`
	Tags         *PostTags `json:"tags,omitempty"`
	PreviewURL   string    `json:"preview_file_url"`
	LargeFileURL string    `json:"large_file_url"` // empty when no large version exists
}

// TagFetcher decouples the service from the concrete api.Client for
// mock-based unit tests. Fetch methods return the raw Danbooru JSON body.
type TagFetcher interface {
	// category is a Danbooru category id filter; -1 means no filter.
	FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool, category int) ([]byte, error)
	FetchTagExact(ctx context.Context, name string) ([]byte, error)
	FetchTagsByNames(ctx context.Context, names []string) ([]byte, error)
	FetchRelated(ctx context.Context, tag string) ([]byte, error)
	FetchAlias(ctx context.Context, name string) ([]byte, error)
	FetchWiki(ctx context.Context, title, otherNames string, limit int) ([]byte, error)
	FetchPosts(ctx context.Context, tags string, limit int) ([]byte, error)
	// FetchPostCount fetches the raw /counts/posts.json body for tags; an
	// empty tags counts every post on the site.
	FetchPostCount(ctx context.Context, tags string) ([]byte, error)
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
// found-response shape stays flat) plus the WD14 verdict, and RenamedTo on
// 0-post placeholder rows that an active alias points elsewhere.
type TagInfo struct {
	Tag
	WD14      WD14Status `json:"wd14"`
	RenamedTo string     `json:"renamed_to,omitempty"`
}

// SearchResult is search_tags' payload: the matched tag rows plus the
// optional WD14 tagger-vocabulary fallback hits (filled by the tools layer
// when no row carries posts).
type SearchResult struct {
	Tags     []Tag       `json:"tags"`
	WD14Hits []vocab.Hit `json:"wd14_hits,omitempty"`
}

// RelatedResult is get_related_tags' payload: the co-occurring tags, ranked
// by lift. Related is always non-nil, so an empty answer marshals as []
// rather than null.
type RelatedResult struct {
	Related []RelatedTag `json:"related"`
}

// AliasResult is get_tag_alias' payload: Alias is null when the input is
// already canonical.
type AliasResult struct {
	Alias *TagAlias `json:"alias"`
}

// WikiResult is get_tag_wiki' payload: the distilled wiki pages.
type WikiResult struct {
	Pages []WikiPage `json:"wiki_pages"`
}

// PostsResult is search_posts' payload: the matched posts plus the optional
// space-split diagnostic. Posts is always non-nil so an empty answer
// marshals as [] rather than null.
type PostsResult struct {
	Posts        []Post   `json:"posts"`
	PossibleTags []string `json:"possible_tags,omitempty"` // vocabulary names two adjacent query tokens form; only on an empty answer
}

// NotFoundError reports that current Danbooru carries no exact match for
// the name; the WD14 verdict for it is still computable locally.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string { return "tag not found: " + e.Name }

// TagService turns the raw Danbooru JSON bodies a TagFetcher returns into
// the tools' payloads, enriching names with the local WD14 vocabularies. It
// is safe for concurrent use (mcp-go runs tool calls on concurrent workers):
// its only state that changes after construction is the whole-site count
// memo, guarded by siteMu.
type TagService struct {
	fetcher TagFetcher
	vocab   *vocab.Store // nil disables WD14 enrichment

	// The whole-site post count every lift of get_related_tags divides by,
	// memoized per process (see sitePostCount). siteMu is held across the
	// request, so concurrent callers share one fetch instead of each sending
	// their own.
	siteMu    sync.Mutex
	sitePosts int // memoized whole-site post count; 0 = nothing memoized
}

func NewTagService(fetcher TagFetcher, vd *vocab.Store) *TagService {
	return &TagService{fetcher: fetcher, vocab: vd}
}

func normalizeTag(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "_")
}

// categoryIDs maps the human category names to Danbooru's numeric ids.
// The API only accepts numeric search[category] values (verified live:
// "character" returns empty while 4 works).
var categoryIDs = map[string]int{
	"general":   0,
	"artist":    1,
	"copyright": 3,
	"character": 4,
	"meta":      5,
}

// CategoryID resolves a category name (general|artist|copyright|character|meta,
// case-insensitive) to Danbooru's numeric id. The second return is false for
// unknown names, so callers reject them before any request.
func CategoryID(name string) (int, bool) {
	id, ok := categoryIDs[strings.ToLower(strings.TrimSpace(name))]
	return id, ok
}

// Search finds tags by keyword, ordered by post count. category is a Danbooru
// category id filter (-1 = no filter); the tools layer maps names to ids.
// minPosts drops rows below the threshold after the fetch (0 keeps every
// row, the pre-filter behavior), so the result may be shorter than limit —
// including empty, which the caller treats as a dead-end search.
func (s *TagService) Search(ctx context.Context, query string, limit, category, minPosts int) (*SearchResult, error) {
	cleanQuery := normalizeTag(query)
	res, err := s.fetcher.FetchTags(ctx, cleanQuery, limit, true, category)
	if err != nil {
		return nil, err
	}

	var tags []Tag
	if err := json.Unmarshal(res, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse tags response: %w", err)
	}
	if minPosts > 0 {
		kept := tags[:0]
		for _, t := range tags {
			if t.PostCount >= minPosts {
				kept = append(kept, t)
			}
		}
		tags = kept
	}
	return &SearchResult{Tags: tags}, nil
}

// Info resolves a name to its exact tag row plus the WD14 verdict. A
// *NotFoundError means current Danbooru no longer carries the name — the
// caller can still classify it locally via WD14Info.
func (s *TagService) Info(ctx context.Context, name string) (*TagInfo, error) {
	cleanName := normalizeTag(name)
	res, err := s.fetcher.FetchTagExact(ctx, cleanName)
	if err != nil {
		return nil, err
	}

	var tags []Tag
	if err := json.Unmarshal(res, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse tag info response: %w", err)
	}
	if len(tags) == 0 {
		return nil, &NotFoundError{Name: cleanName}
	}
	// A returned row with 0 posts is a placeholder (renamed-away antecedent,
	// unused tag — verified live: barefoot_sandals, gold_footwear and even
	// painted_toenails all return rows). It does not make the name live.
	info := &TagInfo{Tag: tags[0], WD14: s.wd14Status(cleanName, tags[0].PostCount > 0)}
	s.resolveRenamedTo(ctx, info)
	return info, nil
}

// resolveRenamedTo fills RenamedTo on 0-post placeholder rows by looking up
// an active alias (verified live: active antecedents keep a 0-post row). The
// alias lookup is an enhancement, so its failure (network, 5xx, unparsable
// body) never turns a found row into an error. Skipped entirely for live rows,
// keeping the common path at one request.
func (s *TagService) resolveRenamedTo(ctx context.Context, info *TagInfo) {
	if info.PostCount != 0 {
		return
	}
	if res, err := s.Alias(ctx, info.Name); err == nil && res.Alias != nil {
		info.RenamedTo = res.Alias.Consequent
	}
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

// danbooruRelatedResponse is the top level of /related_tag.json. PostCount is
// the query tag's own post count (0 when the tag is not found).
type danbooruRelatedResponse struct {
	Query       string                 `json:"query"`
	PostCount   int                    `json:"post_count"`
	RelatedTags []danbooruRelatedEntry `json:"related_tags"`
}

// danbooruCategoryMeta is the category id of meta tags (highres, absurdres,
// commentary, ...) as returned by tags.json / related_tag.json.
const danbooruCategoryMeta = 5

// Lift-ranking knobs for get_related_tags.
const (
	// relatedSupportFloor is the minimum support (posts behind an entry) for
	// the entry to be ranked by lift; below it the entry is sunk to the end
	// of the answer in upstream order instead of dropped. Live data shows
	// 14-17-post companions of mid-size tags are real, while 1-2-post overlaps
	// of tiny tags are not evidence. The same number get_tag_profile keeps as
	// its minimum pair count (profileMinPairCount).
	relatedSupportFloor = 5

	// relatedSampleCap is the number of query-tag posts Danbooru estimates
	// an entry's frequency from, so an entry's support is
	// frequency x min(query posts, relatedSampleCap): the exact joint count
	// while the query tag has at most this many posts.
	relatedSampleCap = 5000
)

// Related answers get_related_tags: the tags co-occurring with tag, ranked
// by lift. The query tag itself and meta-category entries are removed first.
//
// Lift (see relatedLift) needs the whole-site post count, requested once per
// process (sitePostCount) and only once there is something to rank. While it
// is unavailable the answer degrades to exactly the pre-lift payload:
// upstream order, no lift key. limit (max entries; 0 or less keeps all) is
// applied after the ranking, so it keeps the top-lift entries rather than
// the most frequent ones.
func (s *TagService) Related(ctx context.Context, tag string, limit int) (*RelatedResult, error) {
	cleanTag := normalizeTag(tag)
	res, err := s.fetcher.FetchRelated(ctx, cleanTag)
	if err != nil {
		return nil, err
	}

	var resp danbooruRelatedResponse
	if err := json.Unmarshal(res, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse related tags: %w", err)
	}

	// Non-nil from the start, so an empty answer marshals as [] not null.
	candidates := make([]RelatedTag, 0, len(resp.RelatedTags))
	for _, entry := range resp.RelatedTags {
		// The API lists the query tag itself first (similarity 1.0); skip it
		// so the limit is spent on actual co-occurring tags.
		if entry.Tag.Name == resp.Query {
			continue
		}
		// Meta tags (highres, absurdres, ...) co-occur with nearly every
		// upload and are prompt-irrelevant noise; drop them.
		if entry.Tag.Category == danbooruCategoryMeta {
			continue
		}
		candidates = append(candidates, RelatedTag{
			Tag:        entry.Tag.Name,
			Category:   entry.Tag.Category,
			PostCount:  entry.Tag.PostCount,
			Similarity: entry.Similarity,
			Frequency:  entry.Frequency,
		})
	}
	if len(candidates) == 0 {
		// Nothing to rank, so the whole-site count is not requested.
		return &RelatedResult{Related: candidates}, nil
	}

	// A failed count never fails the call: the candidates go out unranked.
	if sitePosts, ok := s.sitePostCount(ctx); ok {
		candidates = rankRelatedByLift(candidates, resp.PostCount, sitePosts)
	}

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return &RelatedResult{Related: candidates}, nil
}

// sitePostCount returns the whole-site post count (the N of the lift
// formula) and whether it is available. The count is requested at most once
// per process: siteMu is held across the request, so concurrent callers wait
// for the one in flight instead of each sending their own, and a success is
// memoized. Only a positive integer is a success. A request error of any
// class, an unparsable body and a non-positive value all answer
// (0, false) and are not memoized, so the next call tries again: a transient
// failure at startup never disables lift ranking for the life of the
// process. The memo lives in memory only; nothing is written to disk.
func (s *TagService) sitePostCount(ctx context.Context) (int, bool) {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if s.sitePosts > 0 {
		return s.sitePosts, true
	}
	res, err := s.fetcher.FetchPostCount(ctx, "")
	if err != nil {
		return 0, false
	}
	total, err := parsePostCount(res)
	if err != nil || total <= 0 {
		return 0, false
	}
	s.sitePosts = total
	return total, true
}

// relatedLift computes lift(B|A) = frequency x N / B, rounded to two
// decimals: how many times more often the tag co-occurs with the query tag
// than its share of the site predicts. 1 means "as often as chance", near
// which corpus constants such as 1girl and solo sit, while tag-specific
// companions (arm_up -> arm_behind_head) reach far above. nil when the lift
// cannot be computed — a 0-post or 0-frequency entry, which cannot occur
// with today's upstream logic and is guarded defensively — or when there is
// no site count.
func relatedLift(frequency float64, postCount, sitePosts int) *float64 {
	if postCount <= 0 || frequency <= 0 || sitePosts <= 0 {
		return nil
	}
	lift := math.Round(frequency*float64(sitePosts)/float64(postCount)*100) / 100
	return &lift
}

// relatedLiftHigher reports whether a ranks before b: by lift descending,
// with an entry that has no lift after every entry that has one.
func relatedLiftHigher(a, b RelatedTag) bool {
	switch {
	case a.Lift == nil:
		return false
	case b.Lift == nil:
		return true
	default:
		return *a.Lift > *b.Lift
	}
}

// rankRelatedByLift attaches its lift to every candidate and orders them.
// candidates arrive in upstream order (frequency descending) with the query
// tag and meta entries already removed; queryPosts is the query tag's own
// post count (A) and sitePosts the whole-site count (N). The result is a new
// slice; candidates is left untouched.
//
// Entries whose support (frequency x min(A, relatedSampleCap)) reaches
// relatedSupportFloor come first, by lift descending. The sort is stable, so
// equal lifts keep upstream frequency order, the better-supported estimate
// first. The rest follow in upstream order: sunk, never dropped. A missing
// or zero A makes every support 0, which leaves the whole list in upstream
// order with lift attached — the safe degradation.
func rankRelatedByLift(candidates []RelatedTag, queryPosts, sitePosts int) []RelatedTag {
	sample := math.Min(float64(queryPosts), relatedSampleCap)
	ranked := make([]RelatedTag, 0, len(candidates))
	sunk := make([]RelatedTag, 0)
	for _, c := range candidates {
		c.Lift = relatedLift(c.Frequency, c.PostCount, sitePosts)
		if c.Frequency*sample >= relatedSupportFloor {
			ranked = append(ranked, c)
		} else {
			sunk = append(sunk, c)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return relatedLiftHigher(ranked[i], ranked[j])
	})
	return append(ranked, sunk...)
}

// Alias resolves a user-typed alias to its canonical Danbooru tag via
// /tag_aliases.json. The result's Alias field is nil when no active alias
// exists, meaning the input is likely already canonical (null in JSON).
func (s *TagService) Alias(ctx context.Context, name string) (*AliasResult, error) {
	cleanName := normalizeTag(name)
	res, err := s.fetcher.FetchAlias(ctx, cleanName)
	if err != nil {
		return nil, err
	}

	var aliases []danbooruAliasResponse
	if err := json.Unmarshal(res, &aliases); err != nil {
		return nil, fmt.Errorf("failed to parse tag alias response: %w", err)
	}
	result := &AliasResult{}
	if len(aliases) > 0 {
		result.Alias = &TagAlias{Antecedent: aliases[0].AntecedentName, Consequent: aliases[0].ConsequentName}
	}
	return result, nil
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
// substring when title is empty, and distills each DText body into the
// structured WikiPage fields. includeDescription additionally returns the
// intro prose as plain text (character appearance descriptions); the raw
// body is never returned.
func (s *TagService) Wiki(ctx context.Context, title, otherNames string, limit int, includeDescription bool) (*WikiResult, error) {
	cleanTitle := normalizeTag(title)
	cleanOther := ""
	if cleanTitle == "" {
		cleanOther = strings.TrimSpace(otherNames)
	}

	res, err := s.fetcher.FetchWiki(ctx, cleanTitle, cleanOther, limit)
	if err != nil {
		return nil, err
	}

	var pages []danbooruWikiResponse
	if err := json.Unmarshal(res, &pages); err != nil {
		return nil, fmt.Errorf("failed to parse wiki response: %w", err)
	}

	results := make([]WikiPage, 0, len(pages))
	for _, p := range pages {
		other := p.OtherNames
		if other == nil {
			other = []string{}
		}
		results = append(results, extractWikiPage(p.Title, p.Body, other, includeDescription))
	}
	return &WikiResult{Pages: results}, nil
}

// Wiki extraction caps: bounded output even for the longest bodies
// (p99 wiki body is ~5 KB, verified live).
const (
	maxWikiRules        = 6 // directive sentences per page
	maxWikiRuleLen      = 500
	maxWikiExamplePosts = 20
)

// wikiHeadingRe matches DText h1.-h6. headings at line starts.
var wikiHeadingRe = regexp.MustCompile(`(?m)^h[1-6]\.[ \t]*(.*)$`)

// wikiLinkFullRe matches a DText link with both parts: [[target|display]].
var wikiLinkFullRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]*))?\]\]`)

// wikiExamplePostRe matches DText example embeds: !post #<id>. !asset embeds
// are not posts and never match.
var wikiExamplePostRe = regexp.MustCompile(`!post #(\d+)`)

// wikiParagraphSplitRe splits prose on blank lines (DText paragraphs).
var wikiParagraphSplitRe = regexp.MustCompile(`\n[ \t]*\n`)

// wikiSection is one heading-delimited chunk of a wiki body; heading is ""
// for the intro (everything before the first heading).
type wikiSection struct {
	heading string
	body    string
}

// splitWikiSections splits a DText body on h1.-h6. headings. The first
// section (empty heading) is the intro: the definition and rules paragraphs
// before any section structure.
func splitWikiSections(body string) []wikiSection {
	matches := wikiHeadingRe.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return []wikiSection{{heading: "", body: body}}
	}
	sections := []wikiSection{{heading: "", body: body[:matches[0][0]]}}
	for i, m := range matches {
		heading := strings.TrimSpace(body[m[2]:m[3]])
		start, end := m[1], len(body)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		sections = append(sections, wikiSection{heading: heading, body: body[start:end]})
	}
	return sections
}

// flattenWikiLinks replaces [[target|display]] links with their display text
// (the target itself when the display part is absent or blank), so prose
// reads as plain text.
func flattenWikiLinks(s string) string {
	return wikiLinkFullRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := wikiLinkFullRe.FindStringSubmatch(m)
		if display := strings.TrimSpace(sub[2]); display != "" {
			return display
		}
		return strings.TrimSpace(sub[1])
	})
}

// isNoiseWikiLink reports whether a link target is navigational noise rather
// than a usable tag reference (tag group indexes, help pages, list-of pages).
func isNoiseWikiLink(target string) bool {
	lower := strings.ToLower(strings.TrimSpace(target))
	for _, prefix := range []string{"tag_group:", "tag group:", "help:", "list of", "list_of"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// extractWikiTagTargets returns the unique link targets of a DText section in
// order of first appearance, dropping navigational noise. Raw target text
// (spaces, title case) matches the linked_tags convention.
func extractWikiTagTargets(section string) []string {
	seen := make(map[string]bool)
	out := []string{}
	for _, m := range wikiLinkRe.FindAllStringSubmatch(section, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] || isNoiseWikiLink(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// splitWikiParagraphs splits a section into its non-empty blank-line-separated
// paragraphs.
func splitWikiParagraphs(section string) []string {
	parts := wikiParagraphSplitRe.Split(section, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// flattenWikiParagraph flattens one paragraph to a single line of plain text.
func flattenWikiParagraph(p string) string {
	return strings.Join(strings.Fields(flattenWikiLinks(p)), " ")
}

// firstWikiParagraph returns the first non-empty paragraph of a section as
// plain text, or "" when there is none.
func firstWikiParagraph(section string) string {
	for _, p := range splitWikiParagraphs(section) {
		if flat := flattenWikiParagraph(p); flat != "" {
			return flat
		}
	}
	return ""
}

// wikiDescription renders the full intro as plain prose: links flattened to
// display text, paragraph breaks preserved.
func wikiDescription(intro string) string {
	paras := splitWikiParagraphs(intro)
	out := make([]string, 0, len(paras))
	for _, p := range paras {
		if flat := flattenWikiParagraph(p); flat != "" {
			out = append(out, flat)
		}
	}
	return strings.Join(out, "\n\n")
}

// splitSentences splits a paragraph into sentences on ". " boundaries (a
// period at the end of input or followed by whitespace). Naive but
// deterministic for wiki prose.
func splitSentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '.' {
			continue
		}
		if i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// wikiRuleKeywords are directive markers that qualify a link-carrying
// sentence as a usage rule ("use X instead", "not to be confused with Y",
// "should only apply if ..."). A keyword heuristic, not a parser: sentences
// without a link or without a keyword are dropped by design.
var wikiRuleKeywords = []string{
	"instead", "not to be confused", "should", "use ", "only ", "do not",
	"don't", "aren't", "avoid", "overlap", "subjective", "note", "caution", "see ",
}

// wikiRuleSkipHeadings are heading prefixes whose sections never yield rules
// (examples, cross references, external links). "example" also covers the
// singular "Example" heading (verified live on the sailor wiki).
var wikiRuleSkipHeadings = []string{"example", "see also", "external link"}

// hasWikiRuleKeyword reports whether the sentence carries a directive marker.
func hasWikiRuleKeyword(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, kw := range wikiRuleKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// extractWikiRules collects the directive sentences of the intro and of every
// section except examples/see-also/external-links. A sentence qualifies when
// it carries at least one [[link]] and a directive keyword; links are
// flattened to display text and the result is deduped and capped.
func extractWikiRules(sections []wikiSection) []string {
	rules := []string{}
	seen := make(map[string]bool)
	for _, sec := range sections {
		heading := strings.ToLower(strings.TrimSpace(sec.heading))
		skip := false
		for _, prefix := range wikiRuleSkipHeadings {
			if strings.HasPrefix(heading, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		for _, para := range splitWikiParagraphs(sec.body) {
			for _, sent := range splitSentences(para) {
				if !wikiLinkRe.MatchString(sent) {
					continue
				}
				flat := flattenWikiParagraph(sent)
				if flat == "" || len(flat) > maxWikiRuleLen || seen[flat] || len(rules) >= maxWikiRules {
					continue
				}
				if hasWikiRuleKeyword(flat) {
					seen[flat] = true
					rules = append(rules, flat)
				}
			}
		}
	}
	return rules
}

// extractWikiSeeAlso returns the unique link targets under any heading
// containing "see also" (case-insensitive), noise-filtered.
func extractWikiSeeAlso(sections []wikiSection) []string {
	var bodies []string
	for _, sec := range sections {
		if strings.Contains(strings.ToLower(sec.heading), "see also") {
			bodies = append(bodies, sec.body)
		}
	}
	if len(bodies) == 0 {
		return []string{}
	}
	return extractWikiTagTargets(strings.Join(bodies, "\n"))
}

// extractWikiExamplePosts returns the unique !post #<id> ids of a body in
// order of first appearance, capped.
func extractWikiExamplePosts(body string) []int {
	seen := make(map[int]bool)
	out := []int{}
	for _, m := range wikiExamplePostRe.FindAllStringSubmatch(body, -1) {
		id, err := strconv.Atoi(m[1])
		if err != nil || seen[id] {
			continue
		}
		if len(out) == maxWikiExamplePosts {
			break
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// extractWikiPage runs the deterministic extraction over one wiki body.
// Line endings are normalized first: live bodies carry \r\n (verified on the
// sampled pages), and every downstream rule assumes \n paragraphs.
func extractWikiPage(title, body string, otherNames []string, includeDescription bool) WikiPage {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	sections := splitWikiSections(body)
	intro := sections[0]
	summary := firstWikiParagraph(intro.body)
	if summary == "" {
		// Intro is empty (the body opens with a heading): fall back to the
		// first paragraph anywhere in the page.
		summary = firstWikiParagraph(body)
	}
	linked := extractLinkedTags(body)
	if linked == nil {
		linked = []string{} // empty bodies must marshal as [], not null
	}
	page := WikiPage{
		Title:        title,
		Summary:      summary,
		IntroTags:    extractWikiTagTargets(intro.body),
		Rules:        extractWikiRules(sections),
		SeeAlso:      extractWikiSeeAlso(sections),
		ExamplePosts: extractWikiExamplePosts(body),
		LinkedTags:   linked,
		OtherNames:   otherNames,
	}
	if includeDescription {
		page.Description = wikiDescription(intro.body)
	}
	return page
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

// maxPossibleTags caps the space-split candidates one search_posts answer
// names: the diagnostic points at the likely cause, it is not a suggestion
// list.
const maxPossibleTags = 3

// maxPostLimit caps posts per search_posts call: a post carries ~44 tags on
// average (verified live), so 20 posts bound the payload at roughly 20 KB.
const maxPostLimit = 20

// isMetatagToken reports whether one space-separated query field is a
// metatag (contains ':': rating:g, order:random, status:deleted, ...)
// rather than a literal tag name.
//
// Deliberately broader than isDanbooruMetatag (known keys only), and it
// stays that way: here the verdict exempts a token from the 2-tag cap, and
// exempting is the safe side: an unlisted metatag must never be counted as
// a content tag. The two predicates differ on purpose.
func isMetatagToken(field string) bool {
	return strings.Contains(field, ":")
}

// countContentTags counts fields that count toward Danbooru's per-account
// tag limit: content tags (no ':') plus order: metatags. Other metatags
// (rating:, status:, id:, ...) are exempt — verified empirically against
// danbooru.donmai.us, which 422s on "a b order:count" but not on
// "a b status:any" for anonymous accounts.
func countContentTags(tags string) int {
	count := 0
	for _, field := range strings.Fields(tags) {
		if !isMetatagToken(field) || strings.HasPrefix(field, "order:") {
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
	Score              int    `json:"score"`
	FavCount           int    `json:"fav_count"`
	PreviewFileURL     string `json:"preview_file_url"`
	LargeFileURL       string `json:"large_file_url"`
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

// minus returns the category lists with the excluded (lowercase) names
// removed from every category; used by diff mode to drop the query's own
// tags from each post.
func (t PostTags) minus(exclude map[string]bool) PostTags {
	if len(exclude) == 0 {
		return t
	}
	filter := func(names []string) []string {
		kept := names[:0]
		for _, n := range names {
			if !exclude[strings.ToLower(n)] {
				kept = append(kept, n)
			}
		}
		return kept
	}
	t.Copyright = filter(t.Copyright)
	t.Artist = filter(t.Artist)
	t.Character = filter(t.Character)
	t.General = filter(t.General)
	t.Meta = filter(t.Meta)
	return t
}

// Tags-list modes for SearchPosts (validated pre-flight, empty = full).
const (
	tagsModeFull = "full" // every per-post category list, the historical shape
	tagsModeNone = "none" // omit the per-post tag lists entirely
	tagsModeDiff = "diff" // category lists minus the query's own content tags
)

// tagsModes is the valid-mode set for SearchPosts.
var tagsModes = map[string]bool{tagsModeFull: true, tagsModeNone: true, tagsModeDiff: true}

// queryContentTags returns the lowercase content-tag names of a posts query:
// space-split tokens that are not metatags (any ':' token — rating:g,
// order:random, status:deleted — never names a literal tag, so diff mode
// excludes them from subtraction). This is the same metatag predicate
// countContentTags applies, minus its order: special case: order: counts
// toward the 2-tag cap but is not a subtractable name.
func queryContentTags(query string) map[string]bool {
	out := make(map[string]bool)
	for _, tok := range strings.Fields(query) {
		if !isMetatagToken(tok) {
			out[strings.ToLower(tok)] = true
		}
	}
	return out
}

// tagPairCandidate is one space-split suspect in a posts query: two adjacent
// plain tokens whose underscore join is a name the tagger vocabularies
// carry.
type tagPairCandidate struct {
	Pair  string // the two tokens as typed, one space between: "blue hair"
	Joins string // the vocabulary name they form: "blue_hair"
}

// tagPairCandidates finds the space-split suspects of a posts query. The
// query is split on whitespace because that is Danbooru's multi-tag syntax,
// so a space inside one tag name ("blue hair" meaning blue_hair) becomes two
// near-empty tokens and an empty answer with no hint. A candidate is a pair
// of adjacent tokens, neither metatag-looking (the tag count's predicate,
// isMetatagToken), whose lowercase underscore join the store carries.
// Candidates keep query order, are deduped by the joined name and capped at
// maxPossibleTags; a nil store yields none. Local computation, no request —
// and deliberately no check that the tokens really have no posts, which
// would cost requests.
func tagPairCandidates(query string, store *vocab.Store) []tagPairCandidate {
	tokens := strings.Fields(query)
	seen := make(map[string]bool)
	var out []tagPairCandidate
	for i := 0; i+1 < len(tokens); i++ {
		a, b := tokens[i], tokens[i+1]
		if isMetatagToken(a) || isMetatagToken(b) {
			continue
		}
		joined := strings.ToLower(a) + "_" + strings.ToLower(b)
		if seen[joined] {
			continue
		}
		if _, ok := store.Lookup(joined); !ok {
			continue
		}
		seen[joined] = true
		out = append(out, tagPairCandidate{Pair: a + " " + b, Joins: joined})
		if len(out) == maxPossibleTags {
			break
		}
	}
	return out
}

// tagPairHint renders candidates as error-message segments, one per
// candidate ("; 'blue hair' may be one tag: blue_hair"), so the
// too-many-tags error can name the space-split pair that burned the tag
// slots. No candidate yields "", which keeps that error byte-identical.
func tagPairHint(candidates []tagPairCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range candidates {
		fmt.Fprintf(&b, "; '%s' may be one tag: %s", c.Pair, c.Joins)
	}
	return b.String()
}

// checkPostQuery validates a posts query against Danbooru's per-account tag
// limit before any request and returns it trimmed, exactly as it is sent.
// SearchPosts and CountPosts share this one function so the two modes cannot
// drift: the limit is the same on /posts.json and /counts/posts.json
// (verified live: three content tags answer 422 on both), so an over-limit
// query fails locally with the same message, space-split hint included.
func (s *TagService) checkPostQuery(tags string) (string, error) {
	query := strings.TrimSpace(tags)
	if n := countContentTags(query); n > maxContentTags {
		return "", fmt.Errorf("too many content tags: %d (max %d, free/anonymous Danbooru limit; rating: and most other metatags do not count, order: does); drop tags or split the query%s",
			n, maxContentTags, tagPairHint(tagPairCandidates(query, s.vocab)))
	}
	return query, nil
}

// SearchPosts sends the tags out verbatim: no layer injects a rating
// filter. The caller pins rating:<x> when they want one (rating: metatags
// are exempt from the tag-count limit), so an unfiltered query describes
// the tag's whole population — the same whole-population rule
// get_tag_profile samples by. tagsMode reshapes only the response: full
// (default, the historical shape) keeps every per-post category list, none
// omits the tag lists, diff keeps only tags not already in the query.
//
// A space inside one tag name is the multi-tag syntax, so "blue hair" asks
// Danbooru for two near-empty tokens and comes back empty. That dead end is
// reported, not repaired: when the answer has no posts and an adjacent token
// pair joins into a tagger-vocabulary name (tagPairCandidates), PossibleTags
// names the candidates, and the local too-many-tags error gains a segment
// per candidate. The query is never retried or rewritten; the caller
// decides. Both diagnostics are local, cost no request, and are omitted
// when there is no candidate, so every other payload stays byte-identical.
func (s *TagService) SearchPosts(ctx context.Context, tags string, limit int, tagsMode string) (*PostsResult, error) {
	query, err := s.checkPostQuery(tags)
	if err != nil {
		return nil, err
	}
	if limit > maxPostLimit {
		return nil, fmt.Errorf("too many posts requested: %d (max %d); a post carries its full tag list, so keep pages small", limit, maxPostLimit)
	}
	mode := strings.ToLower(strings.TrimSpace(tagsMode))
	if mode == "" {
		mode = tagsModeFull
	}
	if !tagsModes[mode] {
		return nil, fmt.Errorf("invalid tags mode %q: use full, none or diff", tagsMode)
	}

	res, err := s.fetcher.FetchPosts(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	var rawPosts []danbooruPost
	if err := json.Unmarshal(res, &rawPosts); err != nil {
		return nil, fmt.Errorf("failed to parse posts: %w", err)
	}
	var exclude map[string]bool
	if mode == tagsModeDiff {
		exclude = queryContentTags(query)
	}
	result := &PostsResult{Posts: make([]Post, 0, len(rawPosts))}
	for _, p := range rawPosts {
		post := Post{
			ID:           p.ID,
			Rating:       p.Rating,
			Score:        p.Score,
			FavCount:     p.FavCount,
			PreviewURL:   p.PreviewFileURL,
			LargeFileURL: p.LargeFileURL,
		}
		if mode != tagsModeNone {
			pt := parsePostTags(p)
			if mode == tagsModeDiff {
				pt = pt.minus(exclude)
			}
			post.Tags = &pt
		}
		result.Posts = append(result.Posts, post)
	}
	if len(result.Posts) == 0 {
		// Local lookup, run only on the empty answer it explains.
		for _, c := range tagPairCandidates(query, s.vocab) {
			result.PossibleTags = append(result.PossibleTags, c.Joins)
		}
	}
	return result, nil
}
