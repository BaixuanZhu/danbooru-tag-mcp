package service

import (
	"context"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/vocab"
)

func TestInfos_Behavior(t *testing.T) {
	tests := []struct {
		name string
		// input names passed to Infos
		in []string
		// raw /tags.json rows the fetcher returns
		resp string
		// expected found names, in order
		wantFound []string
		// expected missing names, in order
		wantMissing []string
		// names sent to the fetcher (normalized + deduped), nil to skip check
		wantFetch []string
	}{
		{
			name: "mixed found and missing, input order kept",
			in:   []string{"blue_hair", "not_a_real_tag_xyz", "gold_footwear"},
			resp: `[
				{"id":1,"name":"blue_hair","category":0,"post_count":1200000},
				{"id":2,"name":"gold_footwear","category":0,"post_count":0}
			]`,
			wantFound:   []string{"blue_hair", "gold_footwear"},
			wantMissing: []string{"not_a_real_tag_xyz"},
			wantFetch:   []string{"blue_hair", "not_a_real_tag_xyz", "gold_footwear"},
		},
		{
			name:        "all missing is a valid answer, not an error",
			in:          []string{"fake_one", "fake_two"},
			resp:        `[]`,
			wantFound:   []string{},
			wantMissing: []string{"fake_one", "fake_two"},
		},
		{
			name:        "names are normalized and deduped before the single request",
			in:          []string{"blue hair", " blue_hair ", "solo"},
			resp:        `[{"id":1,"name":"blue_hair","category":0,"post_count":1}]`,
			wantFound:   []string{"blue_hair"},
			wantMissing: []string{"solo"},
			wantFetch:   []string{"blue_hair", "solo"},
		},
		{
			name: "case drift between request and row still matches",
			in:   []string{"Blue_Hair"},
			resp: `[{"id":1,"name":"blue_hair","category":0,"post_count":1}]`,
			// Found carries the canonical API row, not the raw input form.
			wantFound: []string{"blue_hair"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{namesResp: []byte(tt.resp)}
			svc := NewTagService(mock, nil)

			got, err := svc.Infos(context.Background(), tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Found) != len(tt.wantFound) {
				t.Fatalf("found %d tags, want %d: %+v", len(got.Found), len(tt.wantFound), got.Found)
			}
			for i, want := range tt.wantFound {
				if got.Found[i].Name != want {
					t.Errorf("found[%d] = %q, want %q", i, got.Found[i].Name, want)
				}
			}
			if len(got.Missing) != len(tt.wantMissing) {
				t.Fatalf("missing %d names, want %d: %+v", len(got.Missing), len(tt.wantMissing), got.Missing)
			}
			for i, want := range tt.wantMissing {
				if got.Missing[i].Name != want {
					t.Errorf("missing[%d] = %q, want %q", i, got.Missing[i].Name, want)
				}
			}
			if tt.wantFetch != nil {
				if strings.Join(mock.lastNamesArg, ",") != strings.Join(tt.wantFetch, ",") {
					t.Errorf("fetched %v, want %v", mock.lastNamesArg, tt.wantFetch)
				}
			}
		})
	}
}

func TestInfos_WD14Verdicts(t *testing.T) {
	// Same verdict rules as Info: a 0-post placeholder row is found but
	// tagger_era; a genuinely missing name degrades to unknown or tagger_era
	// depending on the vocabularies.
	mock := &mockFetcher{namesResp: []byte(`[
		{"id":1,"name":"blue_hair","category":0,"post_count":1200000},
		{"id":2,"name":"gold_footwear","category":0,"post_count":0}
	]`)}
	svc := NewTagService(mock, vocab.Default())

	got, err := svc.Infos(context.Background(), []string{"blue_hair", "gold_footwear", "barefoot_sandals", "painted_toenails"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byName := map[string]TagInfo{}
	for _, f := range got.Found {
		byName[f.Name] = f
	}
	if s := byName["blue_hair"].WD14.Status; s != wd14Live {
		t.Errorf("blue_hair wd14 = %q, want live", s)
	}
	if s := byName["gold_footwear"].WD14.Status; s != wd14TaggerEra {
		t.Errorf("gold_footwear wd14 = %q, want tagger_era (0-post placeholder)", s)
	}

	missing := map[string]MissingTag{}
	for _, m := range got.Missing {
		missing[m.Name] = m
	}
	// barefoot_sandals is absent from Danbooru but carried by a vocabulary.
	if s := missing["barefoot_sandals"].WD14.Status; s != wd14TaggerEra {
		t.Errorf("barefoot_sandals wd14 = %q, want tagger_era", s)
	}
	// painted_toenails is carried by neither side (verified live).
	if s := missing["painted_toenails"].WD14.Status; s != wd14Unknown {
		t.Errorf("painted_toenails wd14 = %q, want unknown", s)
	}

	// nil vocab store must degrade verdicts instead of panicking.
	bare := NewTagService(&mockFetcher{namesResp: []byte(`[]`)}, nil)
	got2, err := bare.Infos(context.Background(), []string{"blue_hair"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s := got2.Missing[0].WD14.Status; s != wd14Unknown {
		t.Errorf("nil vocab store must degrade to unknown, got %q", s)
	}
}

func TestInfos_RejectsBadInputBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"empty list", nil, "no usable tag names"},
		{"only blanks", []string{"  ", ""}, "no usable tag names"},
		{"over the cap", make([]string, maxBatchNames+1), "too many names"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "over the cap" {
				for i := range tt.names {
					tt.names[i] = "tag_" + strings.Repeat("x", i+1)
				}
			}
			mock := &mockFetcher{}
			svc := NewTagService(mock, nil)

			_, err := svc.Infos(context.Background(), tt.names)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got: %v", tt.want, err)
			}
			if mock.lastNamesArg != nil {
				t.Errorf("validation failure must not trigger a request, got: %v", mock.lastNamesArg)
			}
		})
	}
}
