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

	p, err := svc.Profile(context.Background(), "arm_up", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Whole-population sample: "<tag> order:random" and nothing else — the
	// search_posts rating:explicit default must NOT leak in here.
	if mock.lastPostsArg != "arm_up order:random" {
		t.Errorf("sample query must be exactly '<tag> order:random', got: %s", mock.lastPostsArg)
	}
	if p.Sample != 4 {
		t.Errorf("expected sample 4, got %d", p.Sample)
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

	p, err := svc.Profile(context.Background(), "t", 20)
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
		_, err := svc.Profile(context.Background(), bad, 200)
		if err == nil || !strings.Contains(err.Error(), "single exact tag name") {
			t.Errorf("expected single-tag rejection for %q, got: %v", bad, err)
		}
	}
}

func TestProfile_NotFoundPropagates(t *testing.T) {
	mock := &mockFetcher{exactResp: []byte(`[]`)}
	svc := NewTagService(mock, nil)

	_, err := svc.Profile(context.Background(), "no_such_tag", 200)
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

	p, err := svc.Profile(context.Background(), "gold_footwear", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastPostsArg != "" {
		t.Errorf("0-post tag must not trigger a posts request, got: %s", mock.lastPostsArg)
	}
	if p.Sample != 0 || p.WD14.Status != wd14TaggerEra {
		t.Errorf("expected empty profile with tagger_era verdict, got: sample=%d wd14=%+v", p.Sample, p.WD14)
	}

	// Empty buckets must marshal as [], never null.
	raw, _ := json.Marshal(p)
	for _, want := range []string{`"sample":0`, `"co_tags":[]`, `"top_pairs":[]`} {
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

		if _, err := svc.Profile(context.Background(), "arm_up", tt.in); err != nil {
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

	p, err := svc.Profile(context.Background(), "arm_up", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Sample != 1 {
		t.Fatalf("expected deduplicated sample of 1, got %d", p.Sample)
	}
	if len(p.Ubiquitous) != 1 || p.Ubiquitous[0].Tag != "1girl" || p.Ubiquitous[0].Freq != 1 {
		t.Errorf("expected 1girl at freq 1.0 in ubiquitous, got: %+v", p.Ubiquitous)
	}
}
