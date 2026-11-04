// Package cache is a persistent per-key-file response cache. It stores one
// JSON entry per key under <dir>/v1/, writes atomically (temp file +
// rename), and sweeps oldest-by-mtime entries under a dual entry/byte cap.
// It is a stdlib-only leaf package (same discipline as internal/vocab) and
// knows nothing about HTTP or Danbooru: the api layer owns TTLs and the
// stale-fallback policy, this package only stores and returns bytes.
package cache

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Eviction caps, enforced best effort after every write: the oldest
// entries (by file mtime) are removed first until the store is under both.
const (
	defaultMaxEntries = 2000
	defaultMaxBytes   = 64 << 20 // 64MB total on-disk payload
)

// Result carries one response body plus its cache provenance. Stale is
// false on every network path; only the expired-entry fallback sets it,
// with CachedAt naming when that body was stored (zero otherwise).
type Result struct {
	Body     []byte
	Stale    bool
	CachedAt time.Time
}

// entry is the on-disk record: storage time as RFC3339 plus the body
// base64-encoded, so arbitrary bytes survive the JSON round trip.
type entry struct {
	StoredAt string `json:"stored_at"`
	Body     string `json:"body"`
}

// Store is a per-key-file cache rooted at a fixed directory tree. Every
// failure is reported as an error (Get as a miss) — nothing panics, so a
// broken cache can never take a request down with it.
type Store struct {
	dir        string // .../v1, the flat entry directory
	maxEntries int    // sweep threshold: maximum number of entries
	maxBytes   int64  // sweep threshold: maximum total entry file size
}

// Open returns a Store keeping its entries under dir/v1/, creating the
// directory tree when missing. A non-nil error means the location is
// unusable and the caller should run without a cache.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("cache: directory is empty")
	}
	root := filepath.Join(dir, "v1")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("cache: create %s: %w", root, err)
	}
	return &Store{dir: root, maxEntries: defaultMaxEntries, maxBytes: defaultMaxBytes}, nil
}

// path maps a cache key to its entry file. Hashing keeps arbitrary key
// text (raw query strings) off the filesystem and bounds file names.
func (s *Store) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// Get returns the cached body for key and when it was stored. A missing,
// corrupt, or unreadable entry reports a miss (ok=false), never an error:
// the caller treats every failure as "not cached" and goes to the network.
func (s *Store) Get(key string) (body []byte, storedAt time.Time, ok bool) {
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		return nil, time.Time{}, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, time.Time{}, false
	}
	body, err = base64.StdEncoding.DecodeString(e.Body)
	if err != nil {
		return nil, time.Time{}, false
	}
	storedAt, err = time.Parse(time.RFC3339, e.StoredAt)
	if err != nil {
		return nil, time.Time{}, false
	}
	return body, storedAt, true
}

// Put stores body under key atomically: the entry is written to a temp
// file and renamed into place, so readers never observe a half-written
// entry and concurrent writers are last-writer-wins per key. Afterwards a
// best-effort sweep enforces the entry/byte caps; sweep failures are
// ignored, only the write itself can fail.
func (s *Store) Put(key string, body []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("cache: create %s: %w", s.dir, err)
	}
	data, err := json.Marshal(entry{
		// RFC3339Nano: second-resolution timestamps would make short TTLs
		// see every fresh entry as up to 1s older than it is.
		StoredAt: time.Now().UTC().Format(time.RFC3339Nano),
		Body:     base64.StdEncoding.EncodeToString(body),
	})
	if err != nil {
		return fmt.Errorf("cache: encode entry: %w", err)
	}
	dst := s.path(key)
	tmp, err := os.CreateTemp(s.dir, "tmp-*")
	if err != nil {
		return fmt.Errorf("cache: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("cache: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("cache: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("cache: rename into place: %w", err)
	}
	s.sweep()
	return nil
}

// sweep enforces the dual caps: it deletes oldest-by-mtime entries until
// the store holds at most maxEntries entry files totaling at most maxBytes
// bytes. Best effort — unreadable and undeletable files are skipped, and
// leftover temp files are never counted.
func (s *Store) sweep() {
	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	type fileStat struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []fileStat
	var total int64
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		files = append(files, fileStat{
			path:  filepath.Join(s.dir, de.Name()),
			size:  info.Size(),
			mtime: info.ModTime(),
		})
		total += info.Size()
	}
	if len(files) <= s.maxEntries && total <= s.maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	count := len(files)
	for _, f := range files {
		if count <= s.maxEntries && total <= s.maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			count--
			total -= f.size
		}
	}
}
