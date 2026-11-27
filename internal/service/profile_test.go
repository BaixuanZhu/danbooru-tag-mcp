package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/vocab"
)

const armUpExactRow = `[{"id":475187,"name":"arm_up","category":0,"post_count":309018}]`

func TestProfile_UbiquitousSplitAndExclusions(t *testing.T) {
	mock := &mockFetcher{
		exactResp: []byte(armUpExactRow),
		postsResp: []byte(`[
			{"id":1,"rating":"g","tag_string_general":"arm_up 1girl solo smile","tag_string_meta":"highres"},
			{"id":2,"rating":"g","tag_string_general":"arm_up 1girl solo"},
			{"id":3,"rating":"s","tag_string_general":"arm_up 1girl smile","tag_string_artist":"artist_x"},
			{"id":4,"rating":"g","tag_string_general":"arm_up solo"}
		]`),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "arm_up", 200, ProfileOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Whole-population sample: "<tag> order:random" and nothing else — the
	// search_posts rating:explicit default must NOT leak in here.
	if mock.lastPostsArg != "arm_up order:random" {
		t.Errorf("sample query must be exactly '<tag> order:random', got: %s", mock.lastPostsArg)
	}
	if p.Order != "random" || p.Rating != "" {
		t.Errorf("default profile must echo order=random and no rating, got order=%q rating=%q", p.Order, p.Rating)
	}
	if p.Sample != 4 {
		t.Errorf("expected sample 4, got %d", p.Sample)
	}
	if p.Ratings["g"] != 0.75 || p.Ratings["s"] != 0.25 {
		t.Errorf("expected ratings mix g=0.75 s=0.25, got: %v", p.Ratings)
	}

	// 1girl/solo 3/4=0.75 and smile 2/4=0.5 (boundary inclusive) are corpus
	// constants; artist_x 1/4=0.25 is the tag's variable content.
	if len(p.Ubiquitous) != 3 {
		t.Fatalf("expected 3 ubiquitous tags, got: %+v", p.Ubiquitous)
	}
	for i, want := range []struct {
		tag  string
		freq float64
	}{{"1girl", 0.75}, {"solo", 0.75}, {"smile", 0.5}} {
		if p.Ubiquitous[i].Tag != want.tag || p.Ubiquitous[i].Freq != want.freq {
			t.Errorf("ubiquitous[%d]: expected %s %.2f, got: %+v", i, want.tag, want.freq, p.Ubiquitous[i])
		}
	}
	if len(p.CoTags) != 1 || p.CoTags[0].Tag != "artist_x" || p.CoTags[0].Category != 1 || p.CoTags[0].Freq != 0.25 {
		t.Errorf("unexpected co_tags: %+v", p.CoTags)
	}

	// The profiled tag itself and meta tags must not appear anywhere.
	for _, group := range [][]ProfileTag{p.Ubiquitous, p.CoTags} {
		for _, e := range group {
			if e.Tag == "arm_up" || e.Tag == "highres" || e.Category == 5 {
				t.Errorf("self/meta tag leaked into the profile: %+v", e)
			}
		}
	}

	// With 4 posts no pair can clear the min count of 5.
	if len(p.TopPairs) != 0 {
		t.Errorf("expected no pairs from a tiny sample, got: %+v", p.TopPairs)
	}

	// Embedded tag row stays flat, wd14 rides along (nil vocab => live).
	if p.Name != "arm_up" || p.PostCount != 309018 || p.WD14.Status != wd14Live {
		t.Errorf("unexpected embedded tag row: %+v", p.TagInfo)
	}
}

func TestProfile_PairsLiftOrderingAndCaps(t *testing.T) {
	// 20 posts: "ubiq" in all, "pa"/"pb" together in posts 1-10 (freq 0.5
	// each, pair freq 0.5), and 40 single-appearance c-tags (2 per post).
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 20; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		gen := fmt.Sprintf("t ubiq c%02d c%02d", i*2, i*2+1)
		if i < 10 {
			gen = fmt.Sprintf("t ubiq pa pb c%02d c%02d", i*2, i*2+1)
		}
		fmt.Fprintf(&b, `{"id":%d,"rating":"g","tag_string_general":%q}`, i+1, gen)
	}
	b.WriteString("]")
	mock := &mockFetcher{
		exactResp: []byte(`[{"id":1,"name":"t","category":0,"post_count":1000}]`),
		postsResp: []byte(b.String()),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "t", 20, ProfileOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Sample != 20 {
		t.Fatalf("expected sample 20, got %d", p.Sample)
	}

	// ubiq 20/20, pa 10/20, pb 10/20 land in ubiquitous; the 40 c-tags at
	// 1/20 fill co_tags up to the cap, ties broken alphabetically.
	if len(p.Ubiquitous) != 3 {
		t.Errorf("expected 3 ubiquitous tags, got: %+v", p.Ubiquitous)
	}
	if len(p.CoTags) != profileMaxCoTags {
		t.Fatalf("expected co_tags capped at %d, got %d", profileMaxCoTags, len(p.CoTags))
	}
	if p.CoTags[0].Tag != "c00" || p.CoTags[len(p.CoTags)-1].Tag != "c29" {
		t.Errorf("cap must keep the alphabetical head, got %s..%s", p.CoTags[0].Tag, p.CoTags[len(p.CoTags)-1].Tag)
	}

	// lift(pa+pb) = (10*20)/(10*10) = 2.0 tops the ranking; the baseline
	// pairs against ubiq sit at lift 1.0 and sort behind it by name.
	if len(p.TopPairs) != 3 {
		t.Fatalf("expected 3 pairs, got: %+v", p.TopPairs)
	}
	if p.TopPairs[0].Pair != "pa + pb" || p.TopPairs[0].Freq != 0.5 || p.TopPairs[0].Lift != 2 {
		t.Errorf("top pair must be pa + pb (freq 0.5, lift 2), got: %+v", p.TopPairs[0])
	}
	if p.TopPairs[1].Pair != "pa + ubiq" || p.TopPairs[2].Pair != "pb + ubiq" {
		t.Errorf("unexpected lift-tie order: %+v", p.TopPairs[1:])
	}

	// Single-appearance pairs (count 1 < 5) never surface.
	for _, pr := range p.TopPairs {
		if strings.Contains(pr.Pair, "c0") || strings.Contains(pr.Pair, "c1") || strings.Contains(pr.Pair, "c2") || strings.Contains(pr.Pair, "c3") {
			t.Errorf("noise pair leaked past the min count: %+v", pr)
		}
	}
}

func TestProfile_RejectsQueries(t *testing.T) {
	svc := NewTagService(&mockFetcher{}, nil)

	for _, bad := range []string{"", "arm_up 1boy", "arm_up rating:g", "order:random", "arm_up order:random"} {
		_, err := svc.Profile(context.Background(), bad, 200, ProfileOptions{})
		if err == nil || !strings.Contains(err.Error(), "single exact tag name") {
			t.Errorf("expected single-tag rejection for %q, got: %v", bad, err)
		}
	}
}

func TestProfile_NotFoundPropagates(t *testing.T) {
	mock := &mockFetcher{exactResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	_, err := svc.Profile(context.Background(), "no_such_tag", 200, ProfileOptions{})
	var nf *NotFoundError
	if !errors.As(err, &nf) || nf.Name != "no_such_tag" {
		t.Fatalf("expected *NotFoundError, got: %v", err)
	}
}

func TestProfile_ZeroCountTagSkipsSampling(t *testing.T) {
	mock := &mockFetcher{
		exactResp: []byte(`[{"id":1441951,"name":"gold_footwear","category":0,"post_count":0}]`),
	}
	svc := NewTagService(mock, vocab.Default())

	p, err := svc.Profile(context.Background(), "gold_footwear", 200, ProfileOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "" {
		t.Errorf("0-post tag must not trigger a posts request, got: %s", mock.lastPostsArg)
	}
	if p.Sample != 0 || p.WD14.Status != wd14TaggerEra {
		t.Errorf("expected empty profile with tagger_era verdict, got: sample=%d wd14=%+v", p.Sample, p.WD14)
	}
	if p.Order != "random" {
		t.Errorf("0-post profile must still echo the order, got %q", p.Order)
	}

	// Empty buckets must marshal as [], never null.
	raw, _ := json.Marshal(p)
	for _, want := range []string{`"sample":0`, `"ratings":{}`, `"co_tags":[]`, `"top_pairs":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %s in payload, got: %s", want, raw)
		}
	}
}

func TestProfile_ClampsSample(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, profileSampleDefault},
		{5, profileSampleMin},
		{5000, profileSampleMax},
		{100, 100},
	}
	for _, tt := range tests {
		mock := &mockFetcher{
			exactResp: []byte(armUpExactRow),
			postsResp: []byte(`[{"id":1,"rating":"g","tag_string_general":"arm_up 1girl"}]`),
		}
		svc := NewTagService(mock, nil)

		if _, err := svc.Profile(context.Background(), "arm_up", tt.in, ProfileOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.lastPostsLim != tt.want {
			t.Errorf("sample %d: expected request limit %d, got %d", tt.in, tt.want, mock.lastPostsLim)
		}
	}
}

func TestProfile_DeduplicatesPosts(t *testing.T) {
	// The same post id twice (repeated draw across random pages) must count
	// once: 2 appearances over 1 distinct post is freq 1.0, not 2.0.
	mock := &mockFetcher{
		exactResp: []byte(armUpExactRow),
		postsResp: []byte(`[
			{"id":7,"rating":"g","tag_string_general":"arm_up 1girl"},
			{"id":7,"rating":"g","tag_string_general":"arm_up 1girl"}
		]`),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "arm_up", 200, ProfileOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Sample != 1 {
		t.Fatalf("expected deduplicated sample of 1, got %d", p.Sample)
	}
	if p.Ratings["g"] != 1 {
		t.Errorf("expected ratings g=1.0 over the deduplicated sample, got: %v", p.Ratings)
	}
	if len(p.Ubiquitous) != 1 || p.Ubiquitous[0].Tag != "1girl" || p.Ubiquitous[0].Freq != 1 {
		t.Errorf("expected 1girl at freq 1.0 in ubiquitous, got: %+v", p.Ubiquitous)
	}
}

func TestProfile_PairRecentQueryAndExclusions(t *testing.T) {
	// Pair mode: 2 content tags, no order: (order: would 422 — verified
	// live), so the sample is the most recent joint page.
	mock := &mockFetcher{
		namesResp: []byte(`[
			{"id":475187,"name":"arm_up","category":0,"post_count":309018},
			{"id":9519,"name":"holding_weapon","category":0,"post_count":436404}
		]`),
		postsResp: []byte(`[
			{"id":1,"rating":"g","tag_string_general":"arm_up holding_weapon sword holding_sword"},
			{"id":2,"rating":"s","tag_string_general":"arm_up holding_weapon sword"},
			{"id":3,"rating":"g","tag_string_general":"arm_up holding_weapon 1girl solo"}
		]`),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "arm_up", 50, ProfileOptions{With: "holding_weapon"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Exactly "<tag> <with>" with the canonical row names and no order:.
	if mock.lastPostsArg != "arm_up holding_weapon" {
		t.Errorf("pair query must be exactly '<tag> <with>', got: %s", mock.lastPostsArg)
	}
	if p.Order != "recent" {
		t.Errorf("pair mode must report order=recent, got %q", p.Order)
	}
	if p.With == nil || p.With.Tag != "holding_weapon" || p.With.PostCount != 436404 {
		t.Errorf("unexpected with descriptor: %+v", p.With)
	}
	if p.Name != "arm_up" || p.PostCount != 309018 {
		t.Errorf("embedded row must be the primary tag, got: %+v", p.TagInfo)
	}
	// Both query tags are excluded from every ranked section.
	for _, group := range [][]ProfileTag{p.Ubiquitous, p.CoTags} {
		for _, e := range group {
			if e.Tag == "arm_up" || e.Tag == "holding_weapon" {
				t.Errorf("query tag leaked into ranked sections: %+v", e)
			}
		}
	}
	for _, pr := range p.TopPairs {
		if strings.Contains(pr.Pair, "arm_up") || strings.Contains(pr.Pair, "holding_weapon") {
			t.Errorf("query tag leaked into pairs: %+v", pr)
		}
	}
	// sword 2/3=0.67 crosses the ubiquitous bar; the 1/3 tags are co_tags,
	// ties broken alphabetically.
	if len(p.Ubiquitous) != 1 || p.Ubiquitous[0].Tag != "sword" || p.Ubiquitous[0].Freq != 0.67 {
		t.Errorf("expected sword at 0.67 in ubiquitous, got: %+v", p.Ubiquitous)
	}
	wantCo := fmt.Sprint([]ProfileTag{{Tag: "1girl", Category: 0, Freq: 0.33}, {Tag: "holding_sword", Category: 0, Freq: 0.33}, {Tag: "solo", Category: 0, Freq: 0.33}})
	if fmt.Sprint(p.CoTags) != wantCo {
		t.Errorf("unexpected co_tags, got: %s", fmt.Sprint(p.CoTags))
	}
	if p.Ratings["g"] != 0.67 || p.Ratings["s"] != 0.33 {
		t.Errorf("expected ratings g=0.67 s=0.33, got: %v", p.Ratings)
	}
}

func TestProfile_PairUsesCanonicalRowNames(t *testing.T) {
	// The API canonicalizes case; the pair query must use the row names, not
	// the raw arguments (verified live behavior of Infos).
	mock := &mockFetcher{
		namesResp: []byte(`[
			{"id":1,"name":"arm_up","category":0,"post_count":100},
			{"id":2,"name":"holding_weapon","category":0,"post_count":200}
		]`),
		postsResp: []byte(`[]`),
	}
	svc := NewTagService(mock, nil)

	if _, err := svc.Profile(context.Background(), "ARM_UP", 50, ProfileOptions{With: "Holding_Weapon"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "arm_up holding_weapon" {
		t.Errorf("pair query must use canonical row names, got: %s", mock.lastPostsArg)
	}
	if p, _ := svc.Profile(context.Background(), "ARM_UP", 50, ProfileOptions{With: "Holding_Weapon"}); p.Name != "arm_up" {
		t.Errorf("embedded row must carry the canonical name, got %q", p.Name)
	}
}

func TestProfile_PairRatingFilter(t *testing.T) {
	mock := &mockFetcher{
		namesResp: []byte(`[
			{"id":1,"name":"arm_up","category":0,"post_count":100},
			{"id":2,"name":"holding_weapon","category":0,"post_count":200}
		]`),
		postsResp: []byte(`[{"id":1,"rating":"g","tag_string_general":"arm_up holding_weapon sword"}]`),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "arm_up", 50, ProfileOptions{With: "holding_weapon", Rating: "g"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "arm_up holding_weapon rating:g" {
		t.Errorf("pair rating must compose as an exempt metatag, got: %s", mock.lastPostsArg)
	}
	if p.Rating != "g" || p.Order != "recent" {
		t.Errorf("expected rating=g order=recent, got rating=%q order=%q", p.Rating, p.Order)
	}
}

func TestProfile_SingleRatingFilterAndMix(t *testing.T) {
	mock := &mockFetcher{
		exactResp: []byte(armUpExactRow),
		postsResp: []byte(`[
			{"id":1,"rating":"g","tag_string_general":"arm_up 1girl"},
			{"id":2,"rating":"g","tag_string_general":"arm_up solo"},
			{"id":3,"rating":"e","tag_string_general":"arm_up smile"}
		]`),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "arm_up", 50, ProfileOptions{Rating: "g"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// rating: composes after order: (both exempt/counted as designed).
	if mock.lastPostsArg != "arm_up order:random rating:g" {
		t.Errorf("single rating query must be '<tag> order:random rating:<x>', got: %s", mock.lastPostsArg)
	}
	if p.Rating != "g" {
		t.Errorf("expected rating echo, got %q", p.Rating)
	}
	// Ratings describe the deduped sample whatever it contains.
	if p.Ratings["g"] != 0.67 || p.Ratings["e"] != 0.33 {
		t.Errorf("expected ratings g=0.67 e=0.33, got: %v", p.Ratings)
	}
}

func TestProfile_OrderEcho(t *testing.T) {
	for _, order := range []string{"score", "favcount"} {
		mock := &mockFetcher{
			exactResp: []byte(armUpExactRow),
			postsResp: []byte(`[{"id":1,"rating":"g","tag_string_general":"arm_up 1girl"}]`),
		}
		svc := NewTagService(mock, nil)

		p, err := svc.Profile(context.Background(), "arm_up", 50, ProfileOptions{Order: order})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.lastPostsArg != "arm_up order:"+order {
			t.Errorf("order %s: unexpected query %q", order, mock.lastPostsArg)
		}
		if p.Order != order {
			t.Errorf("order %s: expected echo, got %q", order, p.Order)
		}
	}
	// Case/whitespace are normalized before validation.
	mock := &mockFetcher{exactResp: []byte(armUpExactRow), postsResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)
	if _, err := svc.Profile(context.Background(), "arm_up", 50, ProfileOptions{Order: " Score "}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "arm_up order:score" {
		t.Errorf("order must be lowercased in the query, got: %s", mock.lastPostsArg)
	}
}

func TestProfile_RejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		name string
		opts ProfileOptions
		want string
	}{
		{"with has space", ProfileOptions{With: "two words"}, "single exact tag name"},
		{"with is metatag", ProfileOptions{With: "rating:g"}, "single exact tag name"},
		{"with equals tag", ProfileOptions{With: "arm_up"}, "different tag"},
		{"with equals tag case-insensitive", ProfileOptions{With: "ARM_UP"}, "different tag"},
		{"invalid rating", ProfileOptions{Rating: "x"}, "invalid rating"},
		{"invalid order", ProfileOptions{Order: "count"}, "invalid order"},
		{"order with with", ProfileOptions{With: "holding_weapon", Order: "random"}, "not allowed with with"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockFetcher{}
			svc := NewTagService(mock, nil)
			p, err := svc.Profile(context.Background(), "arm_up", 50, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got: %v (profile: %+v)", tc.want, err, p)
			}
			if mock.lastNamesArg != nil || mock.lastPostsArg != "" {
				t.Errorf("invalid options must fail before any request, got names=%v posts=%s", mock.lastNamesArg, mock.lastPostsArg)
			}
		})
	}
}

func TestProfile_MissingWithIsNotFound(t *testing.T) {
	// The primary resolves but with does not: the error must name the
	// actually missing tag so the handler's wd14 rider covers it.
	mock := &mockFetcher{
		namesResp: []byte(`[{"id":475187,"name":"arm_up","category":0,"post_count":309018}]`),
	}
	svc := NewTagService(mock, nil)

	_, err := svc.Profile(context.Background(), "arm_up", 50, ProfileOptions{With: "no_such_tag"})
	var nf *NotFoundError
	if !errors.As(err, &nf) || nf.Name != "no_such_tag" {
		t.Fatalf("expected *NotFoundError{no_such_tag}, got: %v", err)
	}
}

func TestProfile_ZeroPostWithSkipsSampling(t *testing.T) {
	mock := &mockFetcher{
		namesResp: []byte(`[
			{"id":475187,"name":"arm_up","category":0,"post_count":309018},
			{"id":26260,"name":"dead_tag","category":0,"post_count":0}
		]`),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "arm_up", 50, ProfileOptions{With: "dead_tag"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "" {
		t.Errorf("a 0-post with tag cannot have joint posts; no request expected, got: %s", mock.lastPostsArg)
	}
	if p.With == nil || p.With.PostCount != 0 {
		t.Errorf("with descriptor must carry post_count 0, got: %+v", p.With)
	}
	if p.Order != "recent" || p.Sample != 0 {
		t.Errorf("expected empty recent profile, got order=%q sample=%d", p.Order, p.Sample)
	}
}

func TestProfile_ZeroPostPrimaryResolvesAlias(t *testing.T) {
	// Pair mode applies the same dead-end enhancement as Info: the primary's
	// renamed_to is resolved before reporting the empty profile.
	mock := &mockFetcher{
		namesResp: []byte(`[
			{"id":26260,"name":"sailor_suit","category":0,"post_count":0},
			{"id":1234,"name":"sailor","category":0,"post_count":5000}
		]`),
		aliasResp: []byte(`[{"antecedent_name":"sailor_suit","consequent_name":"sailor","status":"active"}]`),
	}
	svc := NewTagService(mock, nil)

	p, err := svc.Profile(context.Background(), "sailor_suit", 50, ProfileOptions{With: "sailor"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "" {
		t.Errorf("0-post primary cannot have joint posts; no posts request expected, got: %s", mock.lastPostsArg)
	}
	if p.RenamedTo != "sailor" {
		t.Errorf("expected renamed_to sailor on the primary row, got %q", p.RenamedTo)
	}
	if p.With == nil || p.With.Tag != "sailor" || p.With.PostCount != 5000 {
		t.Errorf("unexpected with descriptor: %+v", p.With)
	}
}
