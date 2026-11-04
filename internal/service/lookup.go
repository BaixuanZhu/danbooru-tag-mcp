package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// maxBatchNames caps the get_tags_info batch: enough to vet a whole
// candidate prompt list in one call, few enough to keep both the request
// URL and the response (fixed-size rows only) small.
const maxBatchNames = 20

// MissingTag reports a name current Danbooru does not carry, with the local
// WD14 verdict so a tagger-era name stays actionable — the batch counterpart
// of get_tag_info's not-found rider.
type MissingTag struct {
	Name string     `json:"name"`
	WD14 WD14Status `json:"wd14"`
}

// TagsInfo is get_tags_info's payload: exact rows for the names Danbooru
// carries (input order preserved) plus the names it does not, each entry
// carrying a WD14 verdict.
type TagsInfo struct {
	Found   []TagInfo    `json:"found"`
	Missing []MissingTag `json:"missing,omitempty"`
	CacheMarks
}

// Infos resolves up to maxBatchNames exact tag names in ONE Danbooru request
// (search[name_normalize] takes a comma-separated list — verified live,
// search[name] does not). Names are normalized and deduped; 0-post rows
// count as found (placeholder semantics shared with Info) and resolve
// renamed_to via one extra alias request each (resolveRenamedTo: live rows
// skip it, alias failures are non-fatal); everything else lands in Missing
// with its local WD14 verdict. An all-missing input is a valid answer, not
// an error.
func (s *TagService) Infos(ctx context.Context, names []string) (*TagsInfo, error) {
	clean := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		c := normalizeTag(n)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		clean = append(clean, c)
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("no usable tag names in request")
	}
	if len(clean) > maxBatchNames {
		return nil, fmt.Errorf("too many names: %d (max %d); split the call into batches", len(clean), maxBatchNames)
	}

	res, err := s.fetcher.FetchTagsByNames(ctx, clean)
	if err != nil {
		return nil, err
	}
	var rows []Tag
	if err := json.Unmarshal(res.Body, &rows); err != nil {
		return nil, fmt.Errorf("failed to parse tags info response: %w", err)
	}

	// The API canonicalizes case while normalizeTag does not, so match rows
	// case-insensitively instead of reporting "Blue_Hair" missing.
	byName := make(map[string]Tag, len(rows))
	for _, r := range rows {
		byName[strings.ToLower(r.Name)] = r
	}

	result := &TagsInfo{Found: make([]TagInfo, 0, len(clean)), CacheMarks: marksFrom(res)}
	for _, name := range clean {
		row, ok := byName[strings.ToLower(name)]
		if !ok {
			result.Missing = append(result.Missing, MissingTag{Name: name, WD14: s.wd14Status(name, false)})
			continue
		}
		result.Found = append(result.Found, TagInfo{Tag: row, WD14: s.wd14Status(name, row.PostCount > 0)})
	}
	// Dead-end enhancement shared with Info: renamed-away antecedents keep a
	// 0-post row, and their alias consequent is the actionable answer.
	for i := range result.Found {
		s.resolveRenamedTo(ctx, &result.Found[i])
	}
	return result, nil
}
