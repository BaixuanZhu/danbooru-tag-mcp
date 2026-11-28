package service

import "strings"

// danbooruMetatagKeyList mirrors PostQueryBuilder::METATAGS in
// app/logical/post_query_builder.rb of github.com/danbooru/danbooru (read
// 2026-11-28: 101 keys): every key Danbooru recognizes in a post query,
// grouped as upstream builds it: the base keys, then the *_count keys,
// their plural synonyms (each *_count key minus the suffix, pluralized)
// and the per-category *tags counters.
//
// This list is the exact line between a post-search metatag and a plain
// token, and one half of what a tag name may not begin with. Danbooru's
// TagNameValidator (app/logical/tag_name_validator.rb) refuses to create a
// tag whose name begins with one of these keys followed by a colon, so a
// name that does not match can be a real tag, colon included:
// honkai:_star_rail and firefly_(honkai:_star_rail) are tags because the
// text before their first colon is not a key. The validator's other half
// is danbooruEditMetatagKeyList. A key added upstream after this snapshot
// is missed, which costs one lookup and a not-found, never a wrong sample,
// since profile queries are built from resolved row names, not from the
// caller's text.
const danbooruMetatagKeyList = `user approver commenter comm noter noteupdater artcomm commentaryupdater
flagger appealer upvote downvote fav ordvote ordfav favgroup ordfavgroup reacted pool
ordpool note comment commentary id rating source status filetype
disapproved parent child search embedded md5 pixelhash width height mpixels ratio
score upvotes downvotes favcount filesize date age order limit tagcount pixiv_id pixiv
unaliased exif duration random is has ai
comment_count deleted_comment_count active_comment_count
note_count deleted_note_count active_note_count
flag_count
child_count deleted_child_count active_child_count
pool_count deleted_pool_count active_pool_count series_pool_count collection_pool_count
appeal_count approval_count replacement_count disapproval_count
comments deleted_comments active_comments
notes deleted_notes active_notes
flags
children deleted_children active_children
pools deleted_pools active_pools series_pools collection_pools
appeals approvals replacements disapprovals
gentags arttags copytags chartags metatags`

// danbooruEditMetatagKeyList is the other half of TagNameValidator: the 13
// keys of PostEdit::METATAGS (app/logical/post_edit.rb, read 2026-11-28)
// that the list above does not carry. They are edit syntax, not search
// syntax: the prefix an edit uses to file a tag under a category
// (artist:bkub), in its long form and as the short forms of
// TagCategory.mapping (art copy char ch co gen), plus the two keys that act
// on a post only after it is saved. A post query does not know them, so
// isDanbooruMetatag reports false for them; they only fence tag names
// (isSingleTagName), and nothing valid is lost: Danbooru carries no tag
// with posts under any of them (probed live 2026-11-28).
const danbooruEditMetatagKeyList = `artist copyright character general meta
art copy char ch co gen
newpool newfavgroup`

// keySet turns one space-separated key list into a lookup set.
func keySet(list string) map[string]bool {
	set := make(map[string]bool)
	for _, key := range strings.Fields(list) {
		set[key] = true
	}
	return set
}

// danbooruMetatagKeys is danbooruMetatagKeyList as a lookup set.
var danbooruMetatagKeys = keySet(danbooruMetatagKeyList)

// danbooruEditMetatagKeys is danbooruEditMetatagKeyList as a lookup set.
var danbooruEditMetatagKeys = keySet(danbooruEditMetatagKeyList)

// metatagKey returns the lowercased text before name's first colon, after
// skipping one leading '-' or '~'. found is false when name has no colon.
func metatagKey(name string) (key string, found bool) {
	if len(name) > 0 && (name[0] == '-' || name[0] == '~') {
		name = name[1:]
	}
	key, _, found = strings.Cut(name, ":")
	return strings.ToLower(key), found
}

// isDanbooruMetatag reports whether name begins with a Danbooru post-search
// metatag: one of danbooruMetatagKeys, a colon, then the value (rating:g,
// Order:Random). A single leading '-' or '~' is skipped first, so a negated
// metatag such as -rating:e is caught as well. Whitespace is deliberately
// not this predicate's concern: "rating:g extra" begins with a known key
// and reports true, so a caller that needs a single token establishes that
// first (isSingleTagName).
func isDanbooruMetatag(name string) bool {
	key, found := metatagKey(name)
	return found && danbooruMetatagKeys[key]
}

// isDanbooruEditMetatag reports whether name begins with one of
// danbooruEditMetatagKeys and a colon, with the same sign and case handling
// as isDanbooruMetatag: edit-only syntax such as artist:bkub or gen:1girl,
// which cannot be a tag name but is not a search metatag either.
func isDanbooruEditMetatag(name string) bool {
	key, found := metatagKey(name)
	return found && danbooruEditMetatagKeys[key]
}

// isSingleTagName reports whether raw (already trimmed) can be one exact tag
// name: not blank, a single whitespace-separated token, and not reserved by
// Danbooru's TagNameValidator, which refuses both the search keys and the
// edit keys as the beginning of a tag name. Danbooru names never contain
// whitespace (it is the query's tag separator), so "arm_up 1boy" is a
// multi-tag query, not a name that could be normalized into arm_up_1boy (a
// lookup that would only answer a misleading not-found). Any whitespace
// counts, not only the space.
func isSingleTagName(raw string) bool {
	if raw == "" || len(strings.Fields(raw)) != 1 {
		return false
	}
	return !isDanbooruMetatag(raw) && !isDanbooruEditMetatag(raw)
}
