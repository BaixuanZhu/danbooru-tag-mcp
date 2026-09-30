package vocab

import "testing"

// Synthetic fixtures mirroring the real snapshot rows used in acceptance
// cases, so logic tests do not depend on the pinned data.
var (
	testV2 = VocabData{Name: MoatV2, CSV: []byte(
		"tag_id,name,category,count\n" +
			"165438,anklet,0,15332\n" +
			"568529,barefoot_sandals,0,931\n" +
			"1441951,gold_footwear,0,714\n")}
	testV3 = VocabData{Name: Eva02V3, CSV: []byte(
		"tag_id,name,category,count\n" +
			"165438,anklet,0,18092\n" +
			"2068563,barefoot_sandals_(jewelry),0,1088\n" +
			"1441951,gold_footwear,0,1225\n")}
)

func TestNewStore_MergesVocabs(t *testing.T) {
	s := NewStore(testV2, testV3)

	hit, ok := s.Lookup("anklet")
	if !ok {
		t.Fatal("expected anklet to hit")
	}
	if len(hit.Sources) != 2 ||
		hit.Sources[0].Vocab != MoatV2 || hit.Sources[0].Count != 15332 ||
		hit.Sources[1].Vocab != Eva02V3 || hit.Sources[1].Count != 18092 {
		t.Errorf("expected one source per vocabulary in order, got: %+v", hit.Sources)
	}

	// A name carried by one vocabulary only still hits, with a single source.
	hit, ok = s.Lookup("barefoot_sandals")
	if !ok || len(hit.Sources) != 1 || hit.Sources[0].Vocab != MoatV2 {
		t.Errorf("expected v2-only hit, got: %+v ok=%v", hit.Sources, ok)
	}
}

func TestLookup_NormalizesAndHandlesNilStore(t *testing.T) {
	s := NewStore(testV3)
	if _, ok := s.Lookup("Barefoot Sandals (jewelry)"); !ok {
		t.Error("caption-style space/case form must normalize to the underscore form")
	}
	if _, ok := s.Lookup("painted_toenails"); ok {
		t.Error("absent name must miss")
	}

	var nilStore *Store
	if _, ok := nilStore.Lookup("anklet"); ok {
		t.Error("nil store must answer miss without panicking")
	}
	if nilStore.Substring("anklet", 8) != nil {
		t.Error("nil store must answer no substring hits without panicking")
	}
}

func TestSubstring_OrdersByCountAndTruncates(t *testing.T) {
	s := NewStore(testV2, testV3)

	hits := s.Substring("gold_footwear", 8)
	if len(hits) != 1 || hits[0].Name != "gold_footwear" || len(hits[0].Sources) != 2 {
		t.Fatalf("expected merged gold_footwear, got: %+v", hits)
	}

	// "foot" matches both barefoot forms and gold_footwear; gold_footwear's
	// best snapshot count (1225) must rank it first, and the limit must cap.
	hits = s.Substring("foot", 1)
	if len(hits) != 1 {
		t.Fatalf("expected limit 1, got %d", len(hits))
	}
	if hits[0].Name != "gold_footwear" {
		t.Errorf("expected best-count name first, got %s", hits[0].Name)
	}
	if s.Substring("nonexistent_substring", 8) != nil {
		t.Error("no matches must yield nil")
	}
}

// TestDefault_PinnedSnapshots pins the vendored snapshots: if a CSV is ever
// re-pinned these counts change, and this test forces a conscious re-check
// (counts verified live against Danbooru and the HF datasets on 2026-09-30).
func TestDefault_PinnedSnapshots(t *testing.T) {
	s := Default()

	hit, ok := s.Lookup("barefoot_sandals")
	if !ok || len(hit.Sources) != 1 || hit.Sources[0].Vocab != MoatV2 || hit.Sources[0].Count != 931 {
		t.Errorf("expected v2-only barefoot_sandals (931), got: %+v ok=%v", hit.Sources, ok)
	}
	hit, ok = s.Lookup("barefoot_sandals_(jewelry)")
	if !ok || len(hit.Sources) != 1 || hit.Sources[0].Vocab != Eva02V3 || hit.Sources[0].Count != 1088 {
		t.Errorf("expected v3-only barefoot_sandals_(jewelry) (1088), got: %+v ok=%v", hit.Sources, ok)
	}
	hit, ok = s.Lookup("gold_footwear")
	if !ok || len(hit.Sources) != 2 {
		t.Errorf("expected gold_footwear in both vocabularies, got: %+v ok=%v", hit.Sources, ok)
	}
	if _, ok := s.Lookup("anklet"); !ok {
		t.Error("expected anklet to hit both vocabularies")
	}
	if _, ok := s.Lookup("painted_toenails"); ok {
		t.Error("painted_toenails is in neither snapshot and must miss")
	}
}
