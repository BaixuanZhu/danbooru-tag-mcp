package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Real wiki bodies fetched live on 2026-11-03 from
// danbooru.donmai.us/wiki_pages.json, trimmed to the body field only. They
// pin the deterministic extraction: every assertion below was checked against
// the live pages. Line endings are \n here; TestWiki_CRLFBody pins that the
// \r\n form live bodies actually use extracts identically.
const wikiBodyArmUp = `A single arm is raised up, and the hand is around eye level or above.

If both arms are raised, use [[arms up]] instead. If the arm is reaching outwards, away from the body, use [[outstretched arm]] instead. If the hand is below eye level, use [[hand up]] instead. If the hand is touching the head, use [[hand on own head]] or [[hand on own face]] instead.

h4. Examples

* !post #11522437
* !post #11495343
* !post #10733108: [[Arm on own head]]
* !post #11569077: [[Arm above head]]

h4. See also

* [[arms up]]
* [[arm above head]]
* [[arm behind head]]
* [[hand on own face]]
* [[hand on own head]]
* [[hand up]]
* [[outstretched arm]]
* [[raised fist]]
* [[stretching]]
* [[Tag group:Posture]]`

const wikiBodySailor = `Also known as a seaman, mariner, or seafarer, a person who navigates water-borne vessels or assists as a crew member in their operation and maintenance.

This tag should only apply if a character is wearing a sailor's uniform (not to be confused with [[serafuku]]).

h4. Example

* !post #6513432
* !post #10627053

h4. See also

* [[naval working uniform]]
* [[pirate costume]]
* [[List of Uniforms]]`

const wikiBody1girl = `An image containing one female character.

Only living characters who are visibly present count towards this tag. Inanimate objects, such as [[doll]]s, [[figure]]s, [[painting (object)|]]s, [[poster (object)|]]s, etc, or offscreen characters who aren't visibly present, don't count towards this tag.

Male characters may also be present in addition to the female character. See [[solo]] for images containing a single character.

h4. Examples

* !post #8659492: [[1girl]] [[solo]]
* !post #5501779: [[1girl]] with [[1boy]]
* !post #10804213: [[1girl]] [[multiple_views]]

h4. See also

* [[tag group:character count]]
* [[1boy]]
* [[1other]]
* [[2girls]]
* [[3girls]]
* [[4girls]]
* [[5girls]]
* [[6+girls]]
* [[multiple girls]]
* [[multiple views]]
* [[solo]] / [[solo focus]]`

const wikiBodyHighres = `An image at least 1600 pixels wide or 1200 pixels tall. Approximately equivalent to {{mpixels:>=1.9}}.

This tag is [[help:autotags|automatically]] added to images.

h4. See also

* [[lowres]] (500x500 or smaller)
* [[highres]] (at least 1600x1200)
* [[absurdres]] (at least 3200x2400)
* [[incredibly absurdres]] (any dimension over 10000)
* [[Tag Group:Image Composition]]`

const wikiBodyFirefly = `A playable character from [[Honkai: Star Rail]], she is a member of the [[Stellaron Hunters (honkai: star rail)|]]. Voiced by [[Kusunoki Tomori]] in Japanese and [[Analesa Fisher]] in English.

Known as Hotaru ("Firefly") in Japanese, a name she shares with [[Genshin Impact]]'s [[Lumine (Genshin Impact)|]].

She has [[long hair|long]] [[grey hair]] with a light [[blue hair|blue]] [[gradient hair|gradient end]], [[two-tone eyes|two-tone]] [[blue eyes|blue]]-[[purple eyes]], and [[medium breasts|medium]] (sometimes [[large breasts|large]] in fanart) breasts. Her default outfit consists of a [[long sleeves|long-sleeved]] [[white shirt]]/[[white dress|dress]] with an inner layer [[green dress]], [[orange neckerchief]], [[black hairband]], [[black thighhighs]], [[black boots|black]] [[ankle boots]], and a [[single bridal gauntlet]] on her left arm.

h4. Appearance

* !post #7726554: Default (with SAM)
* !post #9556115: [[Firefly (Spring Missive) (honkai: star rail)|Spring Missive]]
* !post #7683192: [[Firefly (AR-26710) (honkai: star rail)|AR-26710]]
* !post #8035938: [[Firefly (Jepella Rebellion) (honkai: star rail)|Jepella Rebellion]]
* !asset #33476671: [[Firefly (Vision) (Honkai: Star Rail)|Vision]]
* !post #9240494: [[Firefly (On the Journey) (honkai: star rail)|On the Journey]]
* !post #9262309: [[Firefly (Impression) (Honkai: Star Rail)|Impression]]
* !asset #48585571: [[Firefly (Little Cat) (honkai: star rail)|Little Cat]]

h4. See also

* [[Sam (Honkai: Star Rail)]]
* [[List of Honkai: Star Rail characters]]

h4. External Links

* "Honkai: Star Rail Wiki: Firefly":https://honkai-star-rail.fandom.com/wiki/Firefly`

// wikiRespBody wraps bodies into the fetcher's JSON response shape.
func wikiRespBody(t *testing.T, title, body string) []byte {
	t.Helper()
	b, err := json.Marshal([]danbooruWikiResponse{{Title: title, Body: body, OtherNames: []string{}}})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func fetchWikiPage(t *testing.T, title, body string, includeDescription bool) WikiPage {
	t.Helper()
	mock := &mockFetcher{wikiResp: wikiRespBody(t, title, body)}
	svc := NewTagService(mock, nil)
	pages, err := svc.Wiki(context.Background(), title, "", 5, includeDescription)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(pages))
	}
	return pages[0]
}

func TestWiki_StructuredFields(t *testing.T) {
	p := fetchWikiPage(t, "arm_up", wikiBodyArmUp, false)

	if want := "A single arm is raised up, and the hand is around eye level or above."; p.Summary != want {
		t.Errorf("summary: expected %q, got %q", want, p.Summary)
	}
	wantIntro := []string{"arms up", "outstretched arm", "hand up", "hand on own head", "hand on own face"}
	if fmt.Sprint(p.IntroTags) != fmt.Sprint(wantIntro) {
		t.Errorf("intro_tags: expected %v, got %v", wantIntro, p.IntroTags)
	}
	wantRules := []string{
		"If both arms are raised, use arms up instead.",
		"If the arm is reaching outwards, away from the body, use outstretched arm instead.",
		"If the hand is below eye level, use hand up instead.",
		"If the hand is touching the head, use hand on own head or hand on own face instead.",
	}
	if fmt.Sprint(p.Rules) != fmt.Sprint(wantRules) {
		t.Errorf("rules: expected %v, got %v", wantRules, p.Rules)
	}
	wantSeeAlso := []string{"arms up", "arm above head", "arm behind head", "hand on own face", "hand on own head", "hand up", "outstretched arm", "raised fist", "stretching"}
	if fmt.Sprint(p.SeeAlso) != fmt.Sprint(wantSeeAlso) {
		t.Errorf("see_also: expected %v, got %v", wantSeeAlso, p.SeeAlso)
	}
	wantPosts := []int{11522437, 11495343, 10733108, 11569077}
	if fmt.Sprint(p.ExamplePosts) != fmt.Sprint(wantPosts) {
		t.Errorf("example_posts: expected %v, got %v", wantPosts, p.ExamplePosts)
	}

	// linked_tags stays the raw union (Examples-section links included).
	for _, want := range []string{"Arm on own head", "Tag group:Posture"} {
		if !contains(p.LinkedTags, want) {
			t.Errorf("linked_tags must keep the raw union incl. %q, got %v", want, p.LinkedTags)
		}
	}

	// Breaking-change pin: the raw body is gone from the payload.
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), `"body"`) {
		t.Errorf("payload must not carry the raw body, got: %s", raw)
	}
	for _, want := range []string{`"summary":`, `"intro_tags":`, `"rules":`, `"see_also":`, `"example_posts":`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %s in payload, got: %s", want, raw)
		}
	}
}

func TestWiki_RulesCaptureDisambiguation(t *testing.T) {
	sailor := fetchWikiPage(t, "sailor", wikiBodySailor, false)
	if len(sailor.Rules) != 1 || !strings.Contains(sailor.Rules[0], "not to be confused with serafuku") {
		t.Errorf("expected the serafuku disambiguation rule, got: %v", sailor.Rules)
	}
	if !strings.HasPrefix(sailor.Summary, "Also known as a seaman") {
		t.Errorf("unexpected sailor summary: %q", sailor.Summary)
	}

	girl := fetchWikiPage(t, "1girl", wikiBody1girl, false)
	// The inanimate-object boundary is the page's one real usage rule; the
	// link-free sentences around it must not qualify.
	found := false
	for _, r := range girl.Rules {
		if strings.Contains(r, "Inanimate objects") && strings.Contains(r, "don't count") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the inanimate-object rule, got: %v", girl.Rules)
	}
}

func TestWiki_CharacterIntroTags(t *testing.T) {
	p := fetchWikiPage(t, "firefly_(honkai:_star_rail)", wikiBodyFirefly, false)

	// The appearance prose carries 20+ link targets in the intro.
	if len(p.IntroTags) < 20 {
		t.Errorf("expected 20+ intro tags from the character page, got %d: %v", len(p.IntroTags), p.IntroTags)
	}
	for _, want := range []string{"grey hair", "blue eyes", "purple eyes", "orange neckerchief", "single bridal gauntlet"} {
		if !contains(p.IntroTags, want) {
			t.Errorf("intro_tags must contain %q, got: %v", want, p.IntroTags)
		}
	}
	wantSeeAlso := []string{"Sam (Honkai: Star Rail)"} // the List-of entry is noise
	if fmt.Sprint(p.SeeAlso) != fmt.Sprint(wantSeeAlso) {
		t.Errorf("see_also: expected %v, got %v", wantSeeAlso, p.SeeAlso)
	}
	// The Appearance section mixes !post and !asset embeds; only posts count.
	wantPosts := []int{7726554, 9556115, 7683192, 8035938, 9240494, 9262309}
	if fmt.Sprint(p.ExamplePosts) != fmt.Sprint(wantPosts) {
		t.Errorf("example_posts: expected %v, got %v", wantPosts, p.ExamplePosts)
	}
	// A character intro is descriptive prose: no directive sentences.
	if len(p.Rules) != 0 {
		t.Errorf("expected no rules from a character page, got: %v", p.Rules)
	}
	if !strings.Contains(p.Summary, "A playable character from Honkai: Star Rail") {
		t.Errorf("unexpected summary: %q", p.Summary)
	}
}

func TestWiki_IncludeDescription(t *testing.T) {
	p := fetchWikiPage(t, "firefly_(honkai:_star_rail)", wikiBodyFirefly, true)
	if p.Description == "" {
		t.Fatalf("include_description must populate description")
	}
	// The appearance prose keeps its relationships: link-flattened text
	// naming hair/eyes/outfit, with paragraph breaks preserved.
	for _, want := range []string{"grey hair with a light blue gradient end", "two-tone blue-purple eyes", "single bridal gauntlet"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description must contain %q, got: %q", want, p.Description)
		}
	}
	if !strings.Contains(p.Description, "\n\n") {
		t.Errorf("description must preserve paragraph breaks, got: %q", p.Description)
	}
	if strings.Contains(p.Description, "[[") {
		t.Errorf("description must be plain text without DText links, got: %q", p.Description)
	}

	bare := fetchWikiPage(t, "firefly_(honkai:_star_rail)", wikiBodyFirefly, false)
	if bare.Description != "" {
		t.Errorf("description must default to omitted, got: %q", bare.Description)
	}
	for _, page := range []WikiPage{p, bare} {
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), `"body"`) {
			t.Errorf("raw body must stay absent, got: %s", raw)
		}
	}
}

func TestWiki_NoiseAndSections(t *testing.T) {
	highres := fetchWikiPage(t, "highres", wikiBodyHighres, false)
	// help: links are navigational, not tags: no intro tags on this page.
	if len(highres.IntroTags) != 0 {
		t.Errorf("help: link must be noise-filtered from intro_tags, got: %v", highres.IntroTags)
	}
	wantSeeAlso := []string{"lowres", "highres", "absurdres", "incredibly absurdres"}
	if fmt.Sprint(highres.SeeAlso) != fmt.Sprint(wantSeeAlso) {
		t.Errorf("see_also: expected %v (Tag Group dropped), got %v", wantSeeAlso, highres.SeeAlso)
	}
	if len(highres.Rules) != 0 || len(highres.ExamplePosts) != 0 {
		t.Errorf("highres has no rules/example posts, got: %v / %v", highres.Rules, highres.ExamplePosts)
	}
	// linked_tags keeps the noise entries (raw union contract).
	if !contains(highres.LinkedTags, "help:autotags") || !contains(highres.LinkedTags, "Tag Group:Image Composition") {
		t.Errorf("linked_tags must keep raw targets incl. noise, got: %v", highres.LinkedTags)
	}

	// External-links sections never fold into see_also.
	body := "Intro about [[thing]].\n\nh4. External Links\n\n* [[external_thing]]\n\nh4. See also\n\n* [[real_thing]]"
	p := fetchWikiPage(t, "external_test", body, false)
	if fmt.Sprint(p.SeeAlso) != fmt.Sprint([]string{"real_thing"}) {
		t.Errorf("external links must not fold into see_also, got: %v", p.SeeAlso)
	}

	// Sailor's See also carries a List-of entry; 1girl's carries a tag group.
	sailor := fetchWikiPage(t, "sailor", wikiBodySailor, false)
	wantSailor := []string{"naval working uniform", "pirate costume"}
	if fmt.Sprint(sailor.SeeAlso) != fmt.Sprint(wantSailor) {
		t.Errorf("see_also: expected %v (List of Uniforms dropped), got %v", wantSailor, sailor.SeeAlso)
	}
	girl := fetchWikiPage(t, "1girl", wikiBody1girl, false)
	if contains(girl.SeeAlso, "tag group:character count") {
		t.Errorf("tag group entries must be dropped from see_also, got: %v", girl.SeeAlso)
	}
}

func TestWiki_EmptyBody(t *testing.T) {
	p := fetchWikiPage(t, "empty_tag", "", false)
	if p.Summary != "" || p.IntroTags == nil || p.Rules == nil || p.SeeAlso == nil || p.ExamplePosts == nil || p.LinkedTags == nil {
		t.Fatalf("empty body must yield empty fields, got: %+v", p)
	}
	raw, _ := json.Marshal(p)
	for _, want := range []string{`"summary":""`, `"intro_tags":[]`, `"rules":[]`, `"see_also":[]`, `"example_posts":[]`, `"linked_tags":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %s in payload, got: %s", want, raw)
		}
	}
}

func TestWiki_CRLFBody(t *testing.T) {
	// Live bodies carry \r\n (verified on the sampled pages); extraction must
	// be identical to the \n fixtures.
	p := fetchWikiPage(t, "arm_up", strings.ReplaceAll(wikiBodyArmUp, "\n", "\r\n"), false)
	if want := "A single arm is raised up, and the hand is around eye level or above."; p.Summary != want {
		t.Errorf("summary with CRLF body: expected %q, got %q", want, p.Summary)
	}
	if len(p.Rules) != 4 || len(p.SeeAlso) != 9 || len(p.ExamplePosts) != 4 {
		t.Errorf("CRLF extraction mismatch: rules=%d see_also=%d posts=%v", len(p.Rules), len(p.SeeAlso), p.ExamplePosts)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
