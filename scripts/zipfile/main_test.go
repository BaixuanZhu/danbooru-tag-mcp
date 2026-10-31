package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteZip_DeterministicBytes(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("fake exe bytes \x00\x01\x02 danbooru-tag-mcp")
	if err := os.WriteFile(filepath.Join(dir, "danbooru-tag-mcp.exe"), payload, 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	// Two runs over the same input must be byte-identical, and touching the
	// source file between runs must not change a single byte of the zip.
	var first, second []byte
	for i := 0; i < 2; i++ {
		out := filepath.Join(dir, "out.zip")
		_ = os.Remove(out)
		if err := writeZip(out, 1700000000, []string{filepath.Join(dir, "danbooru-tag-mcp.exe")}); err != nil {
			t.Fatalf("writeZip run %d: %v", i+1, err)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("failed to read zip: %v", err)
		}
		if i == 0 {
			first = b
			future := time.Now().Add(time.Hour)
			if err := os.Chtimes(filepath.Join(dir, "danbooru-tag-mcp.exe"), future, future); err != nil {
				t.Fatalf("failed to touch fixture: %v", err)
			}
		} else {
			second = b
		}
	}
	if !bytes.Equal(first, second) {
		t.Error("same inputs must produce byte-identical zips regardless of file mtimes")
	}
}

func TestWriteZip_EntryMetadataPinned(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "danbooru-tag-mcp.exe"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}
	out := filepath.Join(dir, "out.zip")
	if err := writeZip(out, 1700000000, []string{filepath.Join(dir, "danbooru-tag-mcp.exe")}); err != nil {
		t.Fatalf("writeZip: %v", err)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("failed to open zip: %v", err)
	}
	defer zr.Close()

	if len(zr.File) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(zr.File))
	}
	entry := zr.File[0]
	if entry.Name != "danbooru-tag-mcp.exe" {
		t.Errorf("entry must sit at the zip root under its base name, got %q", entry.Name)
	}
	if want := time.Unix(1700000000, 0).UTC(); !entry.Modified.Equal(want) {
		t.Errorf("entry mtime must be pinned to the epoch, got %v, want %v", entry.Modified, want)
	}
	if entry.Method != zip.Deflate {
		t.Errorf("entry must use Deflate, got method %d", entry.Method)
	}
}
