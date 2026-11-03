// Package service implements Danbooru tag/post business logic on top of the
// api layer: tag search/info, related tags, and safe post search.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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
// large version, which is big enough to actually inspect).
type Post struct {
	ID           int      `json:"id"`
	Rating       string   `json:"rating"`
	Score        int      `json:"score"`
	FavCount     int      `json:"fav_count"`
	Tags         PostTags `json:"tags"`
	PreviewURL   string   `json:"preview_file_url"`
	LargeFileURL string   `json:"large_file_url"` // empty when no large version exists
}

// TagFetcher decouples the service from the concrete api.Client for mock-based unit tests
type TagFetcher interface {
	// category is a Danbooru category id filter; -1 means no filter.
	FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool, category int) ([]byte, error)
	FetchTagExact(ctx context.Context, name string) ([]byte, error)
	FetchTagsByNames(ctx context.Context, names []string) ([]byte, error)
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
// found-response shape stays flat) plus the WD14 verdict, and RenamedTo on
// 0-post placeholder rows that an active alias points elsewhere.
type TagInfo struct {
	Tag
	WD14      WD14Status `json:"wd14"`
	RenamedTo string     `json:"renamed_to,omitempty"`
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
func (s *TagService) Search(ctx context.Context, query string, limit, category int) ([]Tag, error) {
	cleanQuery := normalizeTag(query)
	body, err := s.fetcher.FetchTags(ctx, cleanQuery, limit, true, category)
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
	if alias, err := s.Alias(ctx, info.Name); err == nil && alias != nil {
		info.RenamedTo = alias.Consequent
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
// substring when title is empty, and distills each DText body into the
// structured WikiPage fields. includeDescription additionally returns the
// intro prose as plain text (character appearance descriptions); the raw
// body is never returned.
func (s *TagService) Wiki(ctx context.Context, title, otherNames string, limit int, includeDescription bool) ([]WikiPage, error) {
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
		other := p.OtherNames
		if other == nil {
			other = []string{}
		}
		results = append(results, extractWikiPage(p.Title, p.Body, other, includeDescription))
	}
	return results, nil
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

// maxPostLimit caps posts per search_posts call: a post carries ~44 tags on
// average (verified live), so 20 posts bound the payload at roughly 20 KB.
const maxPostLimit = 20

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
	if limit > maxPostLimit {
		return nil, fmt.Errorf("too many posts requested: %d (max %d); a post carries its full tag list, so keep pages small", limit, maxPostLimit)
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
			ID:           p.ID,
			Rating:       p.Rating,
			Score:        p.Score,
			FavCount:     p.FavCount,
			Tags:         parsePostTags(p),
			PreviewURL:   p.PreviewFileURL,
			LargeFileURL: p.LargeFileURL,
		})
	}
	return posts, nil
}
