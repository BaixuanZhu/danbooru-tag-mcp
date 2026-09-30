// Package vocab embeds pinned snapshots of the WD14 tagger vocabularies
// (SmilingWolf's selected_tags.csv) and answers exact and substring lookups
// against them. Those vocabularies are the tag universes of the taggers that
// captioned most LoRA training data: a name present here but absent from
// current Danbooru is a tagger-era name (likely renamed since), not a
// made-up word. See data/README.md for provenance.
package vocab

import (
	_ "embed"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed data/wd-v1-4-moat-tagger-v2.csv
var moatV2CSV []byte

//go:embed data/wd-eva02-large-tagger-v3.csv
var eva02V3CSV []byte

// Vocabulary identifiers, identical to the Hugging Face dataset names.
const (
	MoatV2  = "wd-v1-4-moat-tagger-v2"
	Eva02V3 = "wd-eva02-large-tagger-v3"
)

// Source is one vocabulary's snapshot record of a tag name.
type Source struct {
	Vocab string `json:"vocab"`
	Count int    `json:"count"`
}

// Hit is a tag name carried by at least one vocabulary.
type Hit struct {
	Name     string   `json:"name"`
	Category int      `json:"category"` // 0=general, 1=artist, 3=copyright, 4=character, 5=meta, 9=tagger rating bucket
	Sources  []Source `json:"sources"`
}

// VocabData is one CSV payload tagged with its vocabulary identifier.
type VocabData struct {
	Name string
	CSV  []byte
}

// Store is the merged, read-only view of one or more vocabularies; all
// methods are safe for concurrent use.
type Store struct {
	byName map[string]*Hit
	names  []string // all names, sorted, for substring scans
}

// NewStore parses and merges CSV payloads in "tag_id,name,category,count"
// format (header row skipped). A name carried by several vocabularies gets
// one Source per vocabulary, in the order the vocabularies were given.
func NewStore(vocabs ...VocabData) *Store {
	s := &Store{byName: make(map[string]*Hit, 11000)}
	for _, v := range vocabs {
		for _, rec := range parseCSV(v.CSV) {
			name := normalizeName(rec.name)
			if name == "" {
				continue
			}
			if hit, ok := s.byName[name]; ok {
				hit.Sources = append(hit.Sources, Source{Vocab: v.Name, Count: rec.count})
				continue
			}
			s.byName[name] = &Hit{
				Name:     name,
				Category: rec.category,
				Sources:  []Source{{Vocab: v.Name, Count: rec.count}},
			}
		}
	}
	s.names = make([]string, 0, len(s.byName))
	for name := range s.byName {
		s.names = append(s.names, name)
	}
	sort.Strings(s.names)
	return s
}

// csvRecord is one parsed data row.
type csvRecord struct {
	name     string
	category int
	count    int
}

// parseCSV splits a WD14 selected_tags.csv payload into records. The header
// row and any malformed line fail the integer parse and are skipped; tag
// names never contain commas, so a plain split is exact.
func parseCSV(data []byte) []csvRecord {
	var recs []csvRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 4 {
			continue
		}
		category, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		count, err := strconv.Atoi(fields[3])
		if err != nil {
			continue
		}
		recs = append(recs, csvRecord{name: fields[1], category: category, count: count})
	}
	return recs
}

// normalizeName lowercases and maps caption-style spaces to Danbooru
// underscores, so lookups accept both "barefoot sandals" and
// "barefoot_sandals".
func normalizeName(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_")
}

// Lookup reports the vocabulary record for an exact name. ok is false when
// no pinned vocabulary carries the name. A nil Store answers miss, so
// callers can run with WD14 enrichment disabled.
func (s *Store) Lookup(name string) (Hit, bool) {
	if s == nil {
		return Hit{}, false
	}
	hit, ok := s.byName[normalizeName(name)]
	if !ok {
		return Hit{}, false
	}
	return *hit, true
}

// Substring returns up to limit vocabulary names containing the query
// substring (normalized), best snapshot count first; alphabetical order
// breaks ties deterministically. This mirrors search_tags' wildcard
// semantics for its empty-result fallback.
func (s *Store) Substring(query string, limit int) []Hit {
	if s == nil || limit <= 0 {
		return nil
	}
	q := normalizeName(query)
	if q == "" {
		return nil
	}
	var hits []Hit
	for _, name := range s.names {
		if strings.Contains(name, q) {
			hits = append(hits, *s.byName[name])
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		return bestCount(hits[i]) > bestCount(hits[j])
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// bestCount is a hit's largest snapshot count across vocabularies.
func bestCount(h Hit) int {
	best := 0
	for _, src := range h.Sources {
		if src.Count > best {
			best = src.Count
		}
	}
	return best
}

var (
	defaultOnce  sync.Once
	defaultStore *Store
)

// Default is the store over both embedded snapshots, parsed on first use so
// CLI subcommands (version, upgrade) never pay the parse cost.
func Default() *Store {
	defaultOnce.Do(func() {
		defaultStore = NewStore(
			VocabData{Name: MoatV2, CSV: moatV2CSV},
			VocabData{Name: Eva02V3, CSV: eva02V3CSV},
		)
	})
	return defaultStore
}
