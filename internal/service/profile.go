package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Profile sampling and shaping knobs. A single request is enough: anonymous
// Danbooru caps /posts.json at 200 per page (verified live), and the output
// caps keep the payload comparable to search_tags in context cost.
const (
	profileSampleDefault = 200
	profileSampleMin     = 20
	profileSampleMax     = 200

	// In-sample frequency at which a co-tag is a corpus constant (1girl,
	// solo, long_hair, ...): predictable from the population alone, so it is
	// listed separately instead of flooding co_tags. Verified live on arm_up
	// (309k posts): the raw top 10 is this baseline vocabulary for every
	// single-character tag, not a property of the tag being profiled.
	profileUbiquitousMin = 0.5
	profileMaxUbiquitous = 10
	profileMaxCoTags     = 30

	// Pairs are computed only among co-tags at or above this frequency, and
	// kept only when they co-occur often enough not to be sample noise.
	profileCoreMinFreq  = 0.05
	profileMinPairCount = 5
	profileMaxPairs     = 10
)

// ProfileTag is one co-occurring tag with the share of sampled posts that
// carry it.
type ProfileTag struct {
	Tag      string  `json:"tag"`
	Category int     `json:"category"` // 0=general, 1=artist, 3=copyright, 4=character
	Freq     float64 `json:"freq"`
}

// ProfilePair is a characteristic two-tag combination: how often the pair
// appears together in the sample, and its lift — how many times more often
// than the two singles' frequencies would suggest by chance. High-lift
// pairs are the poses, outfits and compositions a tag actually renders as
// (verified live: arm_up surfaces holding_weapon+sword, crop_top+midriff).
type ProfilePair struct {
	Pair string  `json:"pair"` // "a + b", lexicographically ordered names
	Freq float64 `json:"freq"`
	Lift float64 `json:"lift"`
}

// TagProfile is get_tag_profile's payload: what a tag's posts actually look
// like, estimated from one random page. The tag row and wd14 verdict are
// embedded (flat), so profiling a renamed-away name returns the same story
// get_tag_info tells.
type TagProfile struct {
	TagInfo
	Sample     int           `json:"sample"`     // distinct posts actually sampled
	Ubiquitous []ProfileTag  `json:"ubiquitous"` // corpus constants (freq >= 0.5), freq-ordered
	CoTags     []ProfileTag  `json:"co_tags"`    // the tag's variable content, freq-ordered
	TopPairs   []ProfilePair `json:"top_pairs"`  // combinations ranked by lift
}

// Profile answers "what does this big tag actually render as": it resolves
// the tag row (Info semantics, so the wd14 verdict rides along), samples one
// random page of the tag's posts, and aggregates co-occurring tags by
// in-sample frequency.
//
// The input must be a single exact tag name: the sample query is
// "<tag> order:random" and order: counts toward Danbooru's 2-tag query
// limit (verified live: "arm_up 1boy order:random" 422s), so a second tag
// would fail upstream. sample is clamped to [20, 200] and defaults to 200.
//
// The sample is deliberately NOT rating-filtered (unlike SearchPosts): a
// profile describes the tag's whole population — arm_up is 95% g/s/q, and
// an explicit-only sample would describe a 5% subpopulation.
func (s *TagService) Profile(ctx context.Context, tag string, sample int) (*TagProfile, error) {
	raw := strings.TrimSpace(tag)
	if raw == "" || strings.ContainsAny(raw, " :") {
		return nil, fmt.Errorf("profile takes a single exact tag name (e.g. 'arm_up'); metatags and multi-tag queries are not supported")
	}
	switch {
	case sample <= 0:
		sample = profileSampleDefault
	case sample < profileSampleMin:
		sample = profileSampleMin
	case sample > profileSampleMax:
		sample = profileSampleMax
	}

	info, err := s.Info(ctx, raw)
	if err != nil {
		return nil, err // *NotFoundError propagates; the handler rides the wd14 verdict
	}
	profile := &TagProfile{
		TagInfo:    *info,
		Ubiquitous: []ProfileTag{},
		CoTags:     []ProfileTag{},
		TopPairs:   []ProfilePair{},
	}
	if info.PostCount == 0 {
		// Placeholder row (renamed-away antecedent, unused tag): nothing to
		// sample; the embedded wd14 verdict tells the story.
		return profile, nil
	}

	body, err := s.fetcher.FetchPosts(ctx, raw+" order:random", sample)
	if err != nil {
		return nil, err
	}
	var posts []danbooruPost
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("failed to parse posts: %w", err)
	}
	aggregateProfile(profile, posts)
	return profile, nil
}

// coTagCount is the running tally for one co-occurring tag.
type coTagCount struct {
	count    int
	category int
}

// coTagRow is one ranked singles entry before frequency rounding.
type coTagRow struct {
	name     string
	count    int
	category int
}

// pairRow is one counted combination before output rounding.
type pairRow struct {
	a, b  string
	name  string
	count int
	lift  float64
}

// taggedName is a tag name with its Danbooru category.
type taggedName struct {
	name     string
	category int
}

// contentTags lists a post's non-meta tags with their categories, minus the
// profiled tag itself.
func contentTags(post danbooruPost, self string) []taggedName {
	t := parsePostTags(post)
	groups := []struct {
		names    []string
		category int
	}{
		{t.General, 0}, {t.Artist, 1}, {t.Copyright, 3}, {t.Character, 4},
	}
	var out []taggedName
	for _, g := range groups {
		for _, name := range g.names {
			if name != self {
				out = append(out, taggedName{name: name, category: g.category})
			}
		}
	}
	return out
}

// aggregateProfile folds the sampled posts into the profile in place:
// singles are split into ubiquitous constants vs freq-ordered co_tags, and
// combinations are ranked by lift. order:random pages are independent
// draws, so posts are deduplicated by id before counting.
func aggregateProfile(p *TagProfile, posts []danbooruPost) {
	seen := make(map[int]bool, len(posts))
	distinct := make([]danbooruPost, 0, len(posts))
	for _, post := range posts {
		if seen[post.ID] {
			continue
		}
		seen[post.ID] = true
		distinct = append(distinct, post)
	}
	n := len(distinct)
	if n == 0 {
		return
	}
	p.Sample = n
	self := p.Name

	counts := make(map[string]coTagCount)
	for _, post := range distinct {
		for _, tn := range contentTags(post, self) {
			c := counts[tn.name]
			c.count++
			c.category = tn.category
			counts[tn.name] = c
		}
	}

	rows := make([]coTagRow, 0, len(counts))
	for name, c := range counts {
		rows = append(rows, coTagRow{name: name, count: c.count, category: c.category})
	}
	// Count desc, name asc: deterministic ordering for reproducible profiles.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})
	for _, r := range rows {
		freq := float64(r.count) / float64(n)
		entry := ProfileTag{Tag: r.name, Category: r.category, Freq: roundFreq(r.count, n)}
		if freq >= profileUbiquitousMin {
			if len(p.Ubiquitous) < profileMaxUbiquitous {
				p.Ubiquitous = append(p.Ubiquitous, entry)
			}
			continue
		}
		if len(p.CoTags) < profileMaxCoTags {
			p.CoTags = append(p.CoTags, entry)
		}
	}

	// Combinations: rank by lift (joint frequency over the product of the
	// singles frequencies) so corpus-baseline pairs like 1girl+solo sink and
	// tag-specific poses float to the top.
	core := make(map[string]bool, len(rows))
	for _, r := range rows {
		if float64(r.count)/float64(n) >= profileCoreMinFreq {
			core[r.name] = true
		}
	}
	pairCounts := make(map[string]pairRow)
	for _, post := range distinct {
		var present []string
		for _, tn := range contentTags(post, self) {
			if core[tn.name] {
				present = append(present, tn.name)
			}
		}
		sort.Strings(present)
		for i := 0; i < len(present); i++ {
			for j := i + 1; j < len(present); j++ {
				key := present[i] + "+" + present[j]
				pr := pairCounts[key]
				if pr.count == 0 {
					pr.a, pr.b = present[i], present[j]
					pr.name = present[i] + " + " + present[j]
				}
				pr.count++
				pairCounts[key] = pr
			}
		}
	}
	pairs := make([]pairRow, 0, len(pairCounts))
	for _, pr := range pairCounts {
		if pr.count < profileMinPairCount {
			continue
		}
		pr.lift = float64(pr.count*n) / float64(counts[pr.a].count*counts[pr.b].count)
		pairs = append(pairs, pr)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].lift != pairs[j].lift {
			return pairs[i].lift > pairs[j].lift
		}
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].name < pairs[j].name
	})
	for _, pr := range pairs {
		if len(p.TopPairs) == profileMaxPairs {
			break
		}
		p.TopPairs = append(p.TopPairs, ProfilePair{
			Pair: pr.name,
			Freq: roundFreq(pr.count, n),
			Lift: math.Round(pr.lift*10) / 10,
		})
	}
}

// roundFreq rounds an in-sample share to two decimals for stable output.
func roundFreq(count, n int) float64 {
	return math.Round(float64(count)/float64(n)*100) / 100
}
