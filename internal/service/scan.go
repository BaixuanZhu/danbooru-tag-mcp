package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"danbooru-tag-mcp/internal/app"
)

// Scan knobs. All three are constants on purpose: five pages answer the
// typical three-tag query, and a caller-selectable scan size would cost
// context in every session for a rare case.
const (
	// scanPageSize is the page size of every scan request. 200 is the page
	// cap /posts.json enforces for every account (verified live 2026-11-28),
	// so a full page is exactly 200 posts and a shorter one ends the listing.
	scanPageSize = 200

	// maxScanPages bounds one scan: at most 1,000 distinct posts examined per
	// call, five page requests after the one batch lookup.
	maxScanPages = 5

	// scanPairSize is how many tags the scan sends upstream when the term
	// budget allows: the two rarest. Each counting metatag takes one away.
	scanPairSize = 2
)

// metatagCapsListing is the key of the metatag that replaces a listing by a
// random sample of at most N posts (verified live 2026-11-28: "arm_up
// random:5" with limit=200 answers 5 posts). Over such a listing a short page
// does not mean the listing ended, so a scan could not tell a whole answer
// from a cut-off one; it refuses the query instead. limit: caps nothing on
// the JSON API (the same probe with limit:5 answered 200 posts), so it rides
// like any other free metatag.
const metatagCapsListing = "random"

// scanTag is one positive tag with what the batch lookup said about it.
type scanTag struct {
	name  string // sent upstream and matched under this name: the row's canonical name, else the lowercased token
	posts int    // the row's post_count; 0 when Danbooru carries no row
	row   *Tag   // the lookup's row, nil when there is none
}

// scanPlan is one scan with every token resolved: which tags go upstream,
// which are filtered locally, and the exact query each page request carries.
// It belongs to one call; nothing of it is kept on the service.
type scanPlan struct {
	upstream string // the query sent for every page

	// query and passThrough are what rebuildUpstream reads: the parsed query,
	// and the indexes of its colon tokens Danbooru has no row for, which ride
	// the upstream query verbatim like the metatags do.
	query       *postQuery
	passThrough map[int]bool

	pair     []scanTag // the tags sent upstream, in query order
	byRarity []scanTag // the same tags, rarest first: the order narrowing drops them in
	mustHave []string  // local filters: the tags a kept post must carry (lowercase)
	mustNot  []string  // local filters: the tags a kept post must not carry (lowercase)
}

// rebuildUpstream sets the query every page request carries: the pair's
// canonical names, then the metatags and pass-through tokens in their
// original order, each verbatim. The service neither injects nor rewrites a
// metatag, so this is the caller's query minus the tags filtered locally.
func (p *scanPlan) rebuildUpstream() {
	parts := make([]string, 0, len(p.pair)+len(p.query.tokens))
	for _, t := range p.pair {
		parts = append(parts, t.name)
	}
	for i, tok := range p.query.tokens {
		if tok.class == tokenMetatag || p.passThrough[i] {
			parts = append(parts, tok.raw)
		}
	}
	p.upstream = strings.Join(parts, " ")
}

// diffExclusions returns every positive tag of the scan, lowercase: what
// diff mode subtracts from each post's tag lists. The pair's tags are in
// every kept post by construction and the local ones by the filter, so the
// query's own tags never show in a diff answer.
func (p *scanPlan) diffExclusions() map[string]bool {
	out := make(map[string]bool, len(p.pair)+len(p.mustHave))
	for _, t := range p.pair {
		out[strings.ToLower(t.name)] = true
	}
	for _, name := range p.mustHave {
		out[name] = true
	}
	return out
}

// planScan resolves a query that needs a scan into a scanPlan, refusing it
// locally when no scan could answer it exactly:
//
//   - a wildcard (*) or OR (~) token: only Danbooru can expand it;
//   - a random: metatag: it caps the upstream listing (metatagCapsListing);
//   - counting metatags that leave no term for a tag: the scan's own query
//     obeys the same term budget, so it sends min(scanPairSize, tagLimit -
//     counting metatags) tags and needs at least one;
//   - no tag to send upstream at all.
//
// The first three cost no request. So does the fourth when the query holds
// nothing but exclusions and metatags; when its only candidates are
// unresolved colon tokens, the lookup below is what proves there is no tag,
// and no page is ever requested.
//
// Every content token is then resolved in ONE batch lookup (positives,
// unresolved colon tokens and exclusions alike). A colon token with a row is
// the tag it names, under the row's canonical name; without a row it is a
// pass-through token — an unlisted metatag or a dead name — sent upstream
// verbatim and never filtered on locally, since no post's tag list could
// match it. The k rarest positives (smallest post_count, a missing row
// counting 0; query order breaks ties) become the pair; the other positives
// and every exclusion are local filters, each under its current name
// (currentName).
func (s *TagService) planScan(ctx context.Context, q *postQuery) (*scanPlan, error) {
	if err := refuseUnscannable(q); err != nil {
		return nil, err
	}
	counting := q.countingMetatags()
	k := min(scanPairSize, s.tagLimit-len(counting))
	if k < 1 {
		raws := make([]string, 0, len(counting))
		for _, tok := range counting {
			raws = append(raws, tok.raw)
		}
		return nil, fmt.Errorf("cannot split the query: the metatags %s leave no term for a tag (each costs one of the %d terms the upstream query may have); drop tags or use only free metatags (rating:, status:, id: ...)",
			strings.Join(raws, " "), s.tagLimit)
	}

	content := q.contentTokens()
	hasTag := false
	lookupNames := make([]string, 0, len(content))
	seenName := make(map[string]bool, len(content))
	for _, tok := range content {
		name := strings.ToLower(tok.name())
		if name == "" || seenName[name] {
			continue
		}
		seenName[name] = true
		lookupNames = append(lookupNames, tok.name())
		hasTag = hasTag || !tok.negative()
	}
	if !hasTag {
		return nil, errNoTagToSend
	}

	rows, err := s.lookupTags(ctx, lookupNames)
	if err != nil {
		return nil, err
	}

	plan := &scanPlan{query: q, passThrough: make(map[int]bool)}
	var candidates, local []scanTag
	var negatives []postToken
	resolved := make(map[string]bool, len(lookupNames))
	for i, tok := range q.tokens {
		if !isContent(tok) {
			continue
		}
		key := strings.ToLower(tok.name())
		row := rows[key]
		if tok.class == tokenColon && row == nil {
			plan.passThrough[i] = true // sign included: Danbooru evaluates it, or nothing matches it
			continue
		}
		if tok.negative() {
			negatives = append(negatives, tok)
			continue
		}
		tag := scanTag{row: row, name: key}
		if row != nil {
			tag.name, tag.posts = row.Name, row.PostCount
		}
		if resolved[strings.ToLower(tag.name)] {
			continue // the same tag twice: one entry keeps the pair and the filter honest
		}
		resolved[strings.ToLower(tag.name)] = true
		candidates = append(candidates, tag)
	}
	if len(candidates) == 0 {
		return nil, errNoTagToSend
	}

	// The k rarest positives go upstream; query order breaks ties.
	plan.byRarity = append([]scanTag(nil), candidates...)
	sort.SliceStable(plan.byRarity, func(i, j int) bool { return plan.byRarity[i].posts < plan.byRarity[j].posts })
	plan.byRarity = plan.byRarity[:min(k, len(plan.byRarity))]
	chosen := make(map[string]bool, len(plan.byRarity))
	for _, t := range plan.byRarity {
		chosen[strings.ToLower(t.name)] = true
	}
	for _, t := range candidates {
		if chosen[strings.ToLower(t.name)] {
			plan.pair = append(plan.pair, t)
		} else {
			local = append(local, t)
		}
	}

	for _, t := range local {
		plan.mustHave = append(plan.mustHave, strings.ToLower(s.currentName(ctx, t.row, t.name)))
	}
	seenNegative := make(map[string]bool, len(negatives))
	for _, tok := range negatives {
		key := strings.ToLower(tok.name())
		name := strings.ToLower(s.currentName(ctx, rows[key], key))
		if name == "" || seenNegative[name] {
			continue
		}
		seenNegative[name] = true
		plan.mustNot = append(plan.mustNot, name)
	}
	plan.rebuildUpstream()
	return plan, nil
}

// errNoTagToSend refuses a scan with nothing to send upstream: with only
// exclusions and metatags there is no listing to filter.
var errNoTagToSend = errors.New("cannot split the query: there is no tag to send upstream (only exclusions and metatags); add a tag, or use fewer tags so Danbooru answers the query directly")

// refuseUnscannable returns the local error for a token no scan can honour —
// a wildcard or OR token, or a random: metatag — or nil. It names the token.
func refuseUnscannable(q *postQuery) error {
	for _, tok := range q.contentTokens() {
		if tok.wildcardOrOR() {
			return fmt.Errorf("cannot split the query: '%s' carries a wildcard (*) or OR (~) that only Danbooru can evaluate; drop it, or use fewer tags so Danbooru answers the query directly", tok.raw)
		}
	}
	for _, tok := range q.metatags() {
		if key, _ := metatagKey(tok.raw); key == metatagCapsListing {
			return fmt.Errorf("cannot split the query: the metatag '%s' caps the upstream listing, so a local scan could not tell a whole answer from a cut-off one; drop it, or use fewer tags so Danbooru answers the query directly", tok.raw)
		}
	}
	return nil
}

// lookupTags resolves names to their rows in ONE batch request
// (FetchTagsByNames: search[name_normalize] canonicalizes case and spacing).
// The rows come back keyed by lowercase row name, so a caller matches its
// tokens case-insensitively; a name Danbooru does not carry has no entry,
// which a nil lookup shows.
func (s *TagService) lookupTags(ctx context.Context, names []string) (map[string]*Tag, error) {
	res, err := s.fetcher.FetchTagsByNames(ctx, names)
	if err != nil {
		return nil, err
	}
	var rows []Tag
	if err := json.Unmarshal(res, &rows); err != nil {
		return nil, fmt.Errorf("failed to parse tags response: %w", err)
	}
	byName := make(map[string]*Tag, len(rows))
	for i := range rows {
		byName[strings.ToLower(rows[i].Name)] = &rows[i]
	}
	return byName, nil
}

// currentName returns the name a local filter must use for a token whose row
// is row (nil when Danbooru carries none) and whose lowercased token is
// fallback. A 0-post row is a renamed-away or dead name, and posts list
// their tags under the current name, so it is resolved through the alias
// endpoint once — the lookup get_tag_info uses (resolveRenamedTo), which
// asks nothing for a live row — and the filter follows the alias. Without
// that, the scan would answer "no matches, complete" for a query Danbooru
// answers. The lookup is an enhancement: any failure (network, 5xx,
// unparsable body, no active alias) falls back to the literal name, which
// simply matches nothing. A live row therefore costs no request.
func (s *TagService) currentName(ctx context.Context, row *Tag, fallback string) string {
	if row == nil {
		return fallback
	}
	info := TagInfo{Tag: *row}
	s.resolveRenamedTo(ctx, &info)
	if info.RenamedTo != "" {
		return info.RenamedTo
	}
	return row.Name
}

// scanOutcome is what one run of the page loop produced.
type scanOutcome struct {
	matches []danbooruPost // the posts passing every local filter, in upstream order
	scanned int            // distinct posts examined
	ended   bool           // the listing ended: a page came back short
}

// scanPages runs the page loop of one plan: pages of scanPageSize, at most
// maxScanPages, each post examined once (order:random re-draws every page, so
// pages overlap and duplicates are skipped by post id). A post is kept iff it
// passes the local filters (postMatches). The loop stops at stopAt matches
// (stopAt <= 0: no such stop), at a short page — the listing ended, every
// post of the upstream query was seen — or at the page cap, in which case
// ended stays false and the matches are a true prefix of the answer. Whatever
// the api layer reports for a page is returned unchanged, a cancellation
// included: nothing is retried here.
func (s *TagService) scanPages(ctx context.Context, plan *scanPlan, stopAt int) (*scanOutcome, error) {
	out := &scanOutcome{matches: []danbooruPost{}}
	seen := make(map[int]bool, scanPageSize*maxScanPages)
	for page := 1; page <= maxScanPages; page++ {
		res, err := s.fetcher.FetchPosts(ctx, plan.upstream, scanPageSize, page)
		if err != nil {
			return nil, err
		}
		posts, err := parsePosts(res)
		if err != nil {
			return nil, err
		}
		for _, p := range posts {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			out.scanned++
			if postMatches(postTagSet(p), plan.mustHave, plan.mustNot) {
				out.matches = append(out.matches, p)
				if stopAt > 0 && len(out.matches) == stopAt {
					return out, nil // the first stopAt posts of the answer, in order
				}
			}
		}
		if len(posts) < scanPageSize {
			out.ended = true
			return out, nil
		}
	}
	return out, nil
}

// scan runs a plan and handles the one thing that can still go wrong after
// the local checks: the account's real tag limit is below the configured one,
// so Danbooru refuses the scan's own query (a classified tag-limit error,
// app.ErrTagLimitExceeded — never a bare "error occurred"). The scan is then
// restarted once with one tag fewer: the dropped tag, the less rare of the
// pair, becomes a local filter under its current name. A second refusal, a
// refusal of a one-tag pair, and every other error — another 422, a
// cancellation, a marked upstream failure — come back untouched. It returns
// the plan that produced the outcome, since narrowing changes it.
func (s *TagService) scan(ctx context.Context, plan *scanPlan, stopAt int) (*scanOutcome, *scanPlan, error) {
	for {
		out, err := s.scanPages(ctx, plan, stopAt)
		if err == nil {
			return out, plan, nil
		}
		if len(plan.pair) < 2 || !errors.Is(err, app.ErrTagLimitExceeded) {
			return nil, nil, err
		}
		plan = s.narrowed(ctx, plan)
	}
}

// narrowed returns the plan with the less rare pair tag moved to the local
// filters; the pair has at least two tags. The upstream query shrinks with
// it, since it is rebuilt from the remaining pair.
func (s *TagService) narrowed(ctx context.Context, plan *scanPlan) *scanPlan {
	dropped := plan.byRarity[len(plan.byRarity)-1]
	byRarity := plan.byRarity[:len(plan.byRarity)-1]
	pair := make([]scanTag, 0, len(byRarity))
	for _, t := range plan.pair {
		if !strings.EqualFold(t.name, dropped.name) {
			pair = append(pair, t)
		}
	}
	mustHave := make([]string, 0, len(plan.mustHave)+1)
	mustHave = append(mustHave, plan.mustHave...)
	// The dropped tag keeps its row, so a 0-post one is alias-resolved like
	// any other local filter.
	mustHave = append(mustHave, strings.ToLower(s.currentName(ctx, dropped.row, dropped.name)))
	narrowed := &scanPlan{
		query:       plan.query,
		passThrough: plan.passThrough,
		pair:        pair,
		byRarity:    byRarity,
		mustHave:    mustHave,
		mustNot:     plan.mustNot,
	}
	narrowed.rebuildUpstream()
	return narrowed
}

// postTagSet is one post's tag names, lowercased: the union of its five
// tag_string_* fields. Posts carry their tags in canonical form, and the
// lowercase keeps the comparison canonical whatever a filter was typed as.
func postTagSet(p danbooruPost) map[string]bool {
	t := parsePostTags(p)
	set := make(map[string]bool, len(t.Copyright)+len(t.Artist)+len(t.Character)+len(t.General)+len(t.Meta))
	for _, group := range [][]string{t.Copyright, t.Artist, t.Character, t.General, t.Meta} {
		for _, name := range group {
			set[strings.ToLower(name)] = true
		}
	}
	return set
}

// postMatches reports whether a post passes the local filters of a scan:
// every must-have tag is in its tag set and none of the must-not tags is.
// Pure, so the filter is table-testable on its own.
func postMatches(tagSet map[string]bool, mustHave, mustNot []string) bool {
	for _, name := range mustHave {
		if !tagSet[name] {
			return false
		}
	}
	for _, name := range mustNot {
		if tagSet[name] {
			return false
		}
	}
	return true
}

// scanStopAt is how many matches the list mode of a scan collects before it
// stops. A non-positive limit omits the limit parameter on a direct query,
// where Danbooru answers its default page of 20 — the same number that caps
// search_posts — so the scan stops there: the same posts, and a payload no
// larger than a direct answer's.
func scanStopAt(limit int) int {
	if limit <= 0 {
		return maxPostLimit
	}
	return limit
}

// searchPostsByScan answers a query Danbooru will not take as-is by scanning
// a wider listing and filtering it locally (planScan, scan). The three scan
// fields ride the answer and only such an answer: upstream_query is the query
// actually sent, scanned the number of distinct posts examined, and complete
// tells whether the posts are the whole answer — true when the limit's worth
// of matches was collected or the listing ended, false when the page cap cut
// the scan short and the posts are a true prefix. The space-split diagnostic
// (possible_tags) rides an empty answer exactly as on the direct path.
func (s *TagService) searchPostsByScan(ctx context.Context, q *postQuery, limit int, mode string) (*PostsResult, error) {
	plan, err := s.planScan(ctx, q)
	if err != nil {
		return nil, err
	}
	stopAt := scanStopAt(limit)
	out, plan, err := s.scan(ctx, plan, stopAt)
	if err != nil {
		return nil, err
	}
	complete := out.ended || len(out.matches) >= stopAt
	result := &PostsResult{
		Posts:         shapePosts(out.matches, mode, plan.diffExclusions()),
		UpstreamQuery: plan.upstream,
		Scanned:       out.scanned,
		Complete:      &complete,
	}
	if len(result.Posts) == 0 {
		// Local lookup, run only on the empty answer it explains.
		for _, c := range tagPairCandidates(q.raw, s.vocab) {
			result.PossibleTags = append(result.PossibleTags, c.Joins)
		}
	}
	return result, nil
}

// countPostsByScan is count mode over a scan: the same scan without the
// limit stop, answering how many posts passed the local filters. The total is
// exact exactly when the listing ended (complete); otherwise it is a lower
// bound and complete says so.
func (s *TagService) countPostsByScan(ctx context.Context, q *postQuery) (*PostCount, error) {
	plan, err := s.planScan(ctx, q)
	if err != nil {
		return nil, err
	}
	out, _, err := s.scan(ctx, plan, 0)
	if err != nil {
		return nil, err
	}
	return &PostCount{Query: q.raw, Total: len(out.matches), Scanned: out.scanned, Complete: &out.ended}, nil
}
