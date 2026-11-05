package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStore_PutGetRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	body := []byte(`[{"id":1,"name":"arm_up"}]`)
	before := time.Now().Add(-time.Second) // stored_at must be at or after this
	if err := s.Put("/tags.json?x=1", body); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, storedAt, ok := s.Get("/tags.json?x=1")
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if string(got) != string(body) {
		t.Errorf("body mismatch: got %q want %q", got, body)
	}
	if storedAt.Before(before) {
		t.Errorf("stored_at %v is older than the Put call", storedAt)
	}
	// A different key must miss: entries are per exact key.
	if _, _, ok := s.Get("/tags.json?x=2"); ok {
		t.Error("different key must miss")
	}
}

func TestStore_PutSameKeyOverwrites(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put("k", []byte("first body")); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := s.Put("k", []byte("second body")); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	got, _, ok := s.Get("k")
	if !ok {
		t.Fatal("expected hit after re-Put")
	}
	if string(got) != "second body" {
		t.Errorf("second Put must replace the body, got %q", got)
	}
	// The rename must replace the entry file in place, never leave a stray
	// second file for the same key.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	jsonFiles := 0
	for _, de := range entries {
		if !de.IsDir() && strings.HasSuffix(de.Name(), ".json") {
			jsonFiles++
		}
	}
	if jsonFiles != 1 {
		t.Errorf("entry directory must hold exactly 1 .json file after re-Put, got %d", jsonFiles)
	}
}

func TestStore_CorruptFileIsMiss(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put("k", []byte(`{}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := os.WriteFile(s.path("k"), []byte("not json at all"), 0o644); err != nil {
		t.Fatalf("corrupt the entry: %v", err)
	}
	if _, _, ok := s.Get("k"); ok {
		t.Error("corrupt entry must report a miss, never a panic or a hit")
	}
}

func TestStore_Base64GarbageIsMiss(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Valid JSON envelope, undecodable body: still a miss.
	bad := `{"stored_at":"2026-01-01T00:00:00Z","body":"!!!not-base64!!!"}`
	if err := os.WriteFile(s.path("k"), []byte(bad), 0o644); err != nil {
		t.Fatalf("plant the entry: %v", err)
	}
	if _, _, ok := s.Get("k"); ok {
		t.Error("entry with undecodable body must miss")
	}
}

func TestStore_BadTimestampIsMiss(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bad := `{"stored_at":"yesterday","body":"eA=="}`
	if err := os.WriteFile(s.path("k"), []byte(bad), 0o644); err != nil {
		t.Fatalf("plant the entry: %v", err)
	}
	if _, _, ok := s.Get("k"); ok {
		t.Error("entry with unparseable stored_at must miss")
	}
}

func TestOpen_CreatesMissingDirTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "danbooru-tag-mcp", "cache")
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open on a missing tree must create it, got: %v", err)
	}
	if err := s.Put("k", []byte("v")); err != nil {
		t.Fatalf("Put into the created tree: %v", err)
	}
	if _, _, ok := s.Get("k"); !ok {
		t.Error("expected hit after writing into the freshly created tree")
	}
}

func TestOpen_EmptyDirIsError(t *testing.T) {
	if _, err := Open("   "); err == nil {
		t.Error("empty directory must be rejected, not silently used")
	}
}

func TestStore_PersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	keys := []string{
		"/tags.json?search[name]=arm_up",
		"/tags.json?search[name]=solo",
		"/wiki_pages.json?title=arm_up",
	}
	before := time.Now().Add(-time.Second)
	for i, key := range keys {
		if err := s.Put(key, []byte(fmt.Sprintf("body-%d", i))); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	// Overwrite one key before closing: what persists must be the LAST
	// write, not the first.
	if err := s.Put(keys[1], []byte("body-1-replaced")); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	after := time.Now().Add(time.Second)

	// Drop the in-memory handle and reopen the same directory: every hit
	// below must be served from disk, not memory.
	s = nil
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	want := []string{"body-0", "body-1-replaced", "body-2"}
	for i, key := range keys {
		got, storedAt, ok := reopened.Get(key)
		if !ok {
			t.Errorf("key %s must survive the reopen", key)
			continue
		}
		if string(got) != want[i] {
			t.Errorf("key %s body after reopen: got %q want %q", key, got, want[i])
		}
		if storedAt.Before(before) || storedAt.After(after) {
			t.Errorf("key %s stored_at %v is outside the original Put window [%v, %v]",
				key, storedAt, before, after)
		}
	}
}

func TestStore_ConcurrentAccessIsSafe(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const (
		goroutines = 16
		iterations = 12
		keyCount   = 8
	)
	// Bodies come from a pure formula (padding varies the size), so the set
	// of bodies ever written per key is known up front for the final check.
	bodyFor := func(g, i int) string {
		return fmt.Sprintf("body-g%d-i%d.%s", g, i, strings.Repeat("p", (g*13+i)%64))
	}
	valid := make([]map[string]bool, keyCount)
	for g := 0; g < goroutines; g++ {
		for i := 0; i < iterations; i++ {
			k := (g + i) % keyCount
			if valid[k] == nil {
				valid[k] = make(map[string]bool)
			}
			valid[k][bodyFor(g, i)] = true
		}
	}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("k%d", (g+i)%keyCount)
				// Put errors are not asserted: the guard rail is no panic
				// and no data race, not write success. Which write lands is
				// checked once, after the race, below.
				_ = s.Put(key, []byte(bodyFor(g, i)))
				_, _, _ = s.Get(key)
			}
		}(g)
	}
	// One goroutine keeps reopening the same directory concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, err := Open(dir); err != nil {
				t.Errorf("concurrent Open must keep succeeding: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	// Final reads must hit and return one of the bodies written for that key.
	for k := 0; k < keyCount; k++ {
		key := fmt.Sprintf("k%d", k)
		body, _, ok := s.Get(key)
		if !ok {
			t.Errorf("key %s must hold at least one written body after the race", key)
			continue
		}
		if !valid[k][string(body)] {
			t.Errorf("key %s served an unknown body %q", key, body)
		}
	}
}

// backdate rewrites an entry file's mtime so eviction order is
// deterministic without waiting on real clock spacing.
func backdate(t *testing.T, s *Store, key string, age time.Duration) {
	t.Helper()
	p := s.path(key)
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(p, stamp, stamp); err != nil {
		t.Fatalf("Chtimes %s: %v", p, err)
	}
}

func TestSweep_EntryCapEvictsOldestFirst(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.maxEntries = 3
	for i, key := range []string{"k1", "k2", "k3", "k4", "k5"} {
		if err := s.Put(key, []byte("payload")); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
		// Age earlier entries far into the past so the sweep order is the
		// insertion order, not filesystem timing noise.
		backdate(t, s, key, time.Duration(100-i)*time.Minute)
	}
	// k1 and k2 were the oldest when k4/k5 landed, so the cap of 3 keeps
	// k3, k4, k5.
	for _, miss := range []string{"k1", "k2"} {
		if _, _, ok := s.Get(miss); ok {
			t.Errorf("%s must have been evicted", miss)
		}
	}
	for _, hit := range []string{"k3", "k4", "k5"} {
		if _, _, ok := s.Get(hit); !ok {
			t.Errorf("%s must have survived the sweep", hit)
		}
	}
}

func TestSweep_ByteCapEvictsOldestFirst(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put("k1", []byte("payload")); err != nil {
		t.Fatalf("Put k1: %v", err)
	}
	// Cap the store at exactly one entry file's worth of bytes, then add a
	// newer entry of the same size: the older one must go.
	info, err := os.Stat(s.path("k1"))
	if err != nil {
		t.Fatalf("Stat k1: %v", err)
	}
	s.maxBytes = info.Size()
	backdate(t, s, "k1", time.Hour)
	if err := s.Put("k2", []byte("payload")); err != nil {
		t.Fatalf("Put k2: %v", err)
	}
	if _, _, ok := s.Get("k1"); ok {
		t.Error("k1 must have been evicted to satisfy the byte cap")
	}
	if _, _, ok := s.Get("k2"); !ok {
		t.Error("k2 must have survived the sweep")
	}
}

func TestSweep_NoopWhenUnderCaps(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Put(string(rune('a'+i)), []byte("payload")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("default caps must not evict 5 entries, got %d files", len(entries))
	}
}

func TestSweep_RemovesLeftoverTempFiles(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Plant a temp file exactly like a crashed write would leave behind.
	tmpPath := filepath.Join(s.dir, "tmp-123456789")
	if err := os.WriteFile(tmpPath, []byte("orphaned write"), 0o644); err != nil {
		t.Fatalf("plant the temp file: %v", err)
	}
	// Any later Put sweeps; the garbage must go even though the store is
	// far under both caps.
	if err := s.Put("k", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("leftover tmp file must be removed by the sweep, stat error: %v", err)
	}
	if _, _, ok := s.Get("k"); !ok {
		t.Error("the real entry must survive the sweep")
	}
}
