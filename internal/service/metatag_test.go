package service

import (
	"strings"
	"testing"
)

func TestIsDanbooruMetatag_OnlyKnownKeysMatch(t *testing.T) {
	// The line between a metatag and a valid name is membership of the
	// pre-colon key in Danbooru's list, not the presence of a colon: names
	// such as honkai:_star_rail start with a word and a colon and are tags.
	tests := []struct {
		name string
		want bool
	}{
		{"rating:g", true},
		{"Rating:G", true},
		{"-rating:e", true},
		{"~order:random", true},
		{"gentags:5", true},
		{"comments:>0", true},
		{"arm_up", false},
		{"honkai:_star_rail", false},
		{"firefly_(honkai:_star_rail)", false},
		{":d", false},
		{"foo:bar", false},
		{"rating", false},
		// Edit-only syntax is not a search metatag; isDanbooruEditMetatag
		// and isSingleTagName handle it.
		{"artist:bkub", false},
		{"gen:1girl", false},
		// Whitespace is not this predicate's rule: the input still begins
		// with a known key. isSingleTagName rejects multi-token input first.
		{"rating:g extra", true},
	}
	for _, tt := range tests {
		if got := isDanbooruMetatag(tt.name); got != tt.want {
			t.Errorf("isDanbooruMetatag(%q): expected %v, got %v", tt.name, tt.want, got)
		}
	}
}

// TestIsDanbooruMetatag_RecognizesEveryListedKey walks the key list itself,
// so a key that is listed but not recognized (a lookup typo, a prefix that is
// not skipped) cannot hide behind a hand-picked table.
func TestIsDanbooruMetatag_RecognizesEveryListedKey(t *testing.T) {
	for _, key := range strings.Fields(danbooruMetatagKeyList) {
		for _, form := range signedForms(key) {
			if !isDanbooruMetatag(form) {
				t.Errorf("isDanbooruMetatag(%q): expected true", form)
			}
		}
	}
}

func TestIsDanbooruEditMetatag_OnlyEditKeysMatch(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"artist:bkub", true},
		{"Character:firefly_(honkai:_star_rail)", true},
		{"gen:1girl", true},
		{"-newpool:pool", true},
		{"~ch:x", true},
		// Search metatags belong to the other predicate.
		{"rating:g", false},
		// Real names, or no key at all.
		{"honkai:_star_rail", false},
		{"firefly_(honkai:_star_rail)", false},
		{"foo:bar", false},
		{"arm_up", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isDanbooruEditMetatag(tt.name); got != tt.want {
			t.Errorf("isDanbooruEditMetatag(%q): expected %v, got %v", tt.name, tt.want, got)
		}
	}
}

// TestIsDanbooruEditMetatag_RecognizesEveryListedKey walks the edit list the
// same way the search list is walked.
func TestIsDanbooruEditMetatag_RecognizesEveryListedKey(t *testing.T) {
	for _, key := range strings.Fields(danbooruEditMetatagKeyList) {
		for _, form := range signedForms(key) {
			if !isDanbooruEditMetatag(form) {
				t.Errorf("isDanbooruEditMetatag(%q): expected true", form)
			}
		}
	}
}

// signedForms returns the four spellings a key must be recognized in: bare,
// negated, tilde-prefixed and upper-case.
func signedForms(key string) []string {
	return []string{
		key + ":x",
		"-" + key + ":x",
		"~" + key + ":x",
		strings.ToUpper(key) + ":x",
	}
}

func TestDanbooruMetatagKeys_AreUniqueLowercase(t *testing.T) {
	assertCleanKeyList(t, danbooruMetatagKeyList, danbooruMetatagKeys)
}

func TestDanbooruEditMetatagKeys_AreUniqueLowercaseAndDisjoint(t *testing.T) {
	assertCleanKeyList(t, danbooruEditMetatagKeyList, danbooruEditMetatagKeys)
	// A key present in both lists is redundant in the edit list: the search
	// predicate already rejects it, and it signals a mis-transcription.
	for key := range danbooruEditMetatagKeys {
		if danbooruMetatagKeys[key] {
			t.Errorf("key %q is in both lists; the edit list holds only what the search list lacks", key)
		}
	}
}

// assertCleanKeyList guards the transcription of one upstream list: a
// duplicated key is a copy mistake, and a key holding an uppercase letter
// could never match, because lookups lowercase the input.
func assertCleanKeyList(t *testing.T, list string, set map[string]bool) {
	t.Helper()
	seen := make(map[string]bool)
	for _, key := range strings.Fields(list) {
		if seen[key] {
			t.Errorf("key %q is listed more than once", key)
		}
		seen[key] = true
		if key != strings.ToLower(key) {
			t.Errorf("key %q is not lowercase, so it can never match", key)
		}
	}
	if len(seen) != len(set) {
		t.Errorf("expected the set to hold exactly the listed keys, got %d listed vs %d in the set", len(seen), len(set))
	}
}

// TestMetatagKeyLists_MatchUpstreamSnapshot pins both lists to the size of
// upstream at master as read on 2026-11-28, so that an accidentally dropped
// line fails here instead of silently narrowing a predicate. A deliberate
// update of a list changes its count in the same change.
func TestMetatagKeyLists_MatchUpstreamSnapshot(t *testing.T) {
	// PostQueryBuilder::METATAGS: 58 base keys, 19 *_count keys, their 19
	// plural synonyms and the 5 per-category counters.
	if got := len(danbooruMetatagKeys); got != 101 {
		t.Errorf("search metatag list: expected 101 keys (upstream snapshot 2026-11-28), got %d", got)
	}
	// PostEdit::METATAGS holds 24 keys, 11 of them shared with the list above.
	if got := len(danbooruEditMetatagKeys); got != 13 {
		t.Errorf("edit metatag list: expected 13 keys (upstream snapshot 2026-11-28), got %d", got)
	}
}

func TestUnlimitedMetatagKeys_AreUniqueLowercase(t *testing.T) {
	assertCleanKeyList(t, unlimitedMetatagKeyList, unlimitedMetatagKeys)
}

// TestUnlimitedMetatagKeys_AreSearchMetatags keeps the third list inside the
// first: the unlimited list can only exempt metatags Danbooru's post search
// knows, so a key the search list lacks is a transcription error — it would
// exempt nothing and signal a key spelled wrong on one side or the other.
func TestUnlimitedMetatagKeys_AreSearchMetatags(t *testing.T) {
	for key := range unlimitedMetatagKeys {
		if !danbooruMetatagKeys[key] {
			t.Errorf("key %q is not a post-search metatag; the unlimited list only exempts search metatags", key)
		}
	}
}

// TestUnlimitedMetatagKeyList_MatchUpstreamSnapshot pins the list to the size
// of PostQuery::UNLIMITED_METATAGS at master as read on 2026-11-28, the way
// the two other key lists are pinned, so an accidentally dropped line fails
// here instead of silently making a free metatag count. A deliberate update
// changes the count in the same change.
func TestUnlimitedMetatagKeyList_MatchUpstreamSnapshot(t *testing.T) {
	if got := len(unlimitedMetatagKeys); got != 25 {
		t.Errorf("unlimited metatag list: expected 25 keys (upstream snapshot 2026-11-28), got %d", got)
	}
}

// TestUnlimitedMetatagKeyList_IsTheUpstreamText pins the transcription word
// for word. The count alone would let a misspelled key through, and a
// misspelled key here silently turns a free metatag into a counting one.
func TestUnlimitedMetatagKeyList_IsTheUpstreamText(t *testing.T) {
	const upstream = "status rating limit is id date age filesize filetype parent child " +
		"md5 width height duration mpixels ratio score upvote downvotes " +
		"favcount embedded tagcount pixiv_id pixiv"
	if got := strings.Join(strings.Fields(unlimitedMetatagKeyList), " "); got != upstream {
		t.Errorf("unlimited metatag list:\n got: %s\nwant: %s", got, upstream)
	}
}

func TestIsSingleTagName_RejectsMultiTokenAndMetatag(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"arm_up", true},
		{"honkai:_star_rail", true},
		{"firefly_(honkai:_star_rail)", true},
		{":d", true},
		{"foo:bar", true},
		{"", false},
		{"   ", false},
		{"arm_up 1boy", false},
		{"arm_up\t1boy", false},
		{"arm_up\n1boy", false},
		{"rating:g", false},
		{"-rating:e", false},
		{"rating:g extra", false},
		// Edit-only syntax cannot be a tag name either.
		{"artist:bkub", false},
		{"gen:1girl", false},
		{"character:firefly_(honkai:_star_rail)", false},
		{"-newpool:x", false},
	}
	for _, tt := range tests {
		if got := isSingleTagName(tt.raw); got != tt.want {
			t.Errorf("isSingleTagName(%q): expected %v, got %v", tt.raw, tt.want, got)
		}
	}
}
