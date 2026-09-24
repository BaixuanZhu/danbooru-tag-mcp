package upgrade

import (
	"archive/zip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	const asset = "danbooru-tag-mcp-windows-amd64.zip"
	tests := []struct {
		name string
		text string
		file string
		want string
		ok   bool
	}{
		{"text mode (two spaces)", "abc123  " + asset + "\n", asset, "abc123", true},
		{"binary mode (*prefix)", "abc123 *" + asset + "\n", asset, "abc123", true},
		{"CRLF line ending", "abc123  " + asset + "\r\n", asset, "abc123", true},
		{"multi-line hit", "dead00  other.zip\nabc123  " + asset + "\n", asset, "abc123", true},
		{"filename mismatch", "abc123  other.zip\n", asset, "", false},
		{"wrong field count", "abc123\n", asset, "", false},
		{"empty text", "", asset, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseChecksum(tt.text, tt.file)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseChecksum = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"0.1.0", "0.2.0", -1},
		{"0.3.0", "0.2.9", 1},
		{"0.2", "0.2.0", 0}, // missing segments count as 0
		{"0.2.0", "0.2", 0},
		{"0.10.0", "0.9.0", 1}, // numeric segment compare, not string compare
		{"0.1.0-dev", "0.2.0", -1},
		{"0.2.0", "0.1.0-dev", 1},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			if got := compareVersions(tt.a, tt.b); got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestExpectedAssetName(t *testing.T) {
	want := "danbooru-tag-mcp-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip"
	if got := expectedAssetName(); got != want {
		t.Errorf("expectedAssetName() = %q, want %q", got, want)
	}
}

func TestCleanupBakAt(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")

	if cleanupBakAt(exe) {
		t.Fatal("no .bak exists, should report false")
	}

	if err := os.WriteFile(exe+".bak", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !cleanupBakAt(exe) {
		t.Fatal("expected .bak to be removed")
	}
	if _, err := os.Stat(exe + ".bak"); !os.IsNotExist(err) {
		t.Fatal(".bak still exists after cleanup")
	}
}

func TestReplaceExeAt(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "app.exe")
	newExe := filepath.Join(dir, "new.exe")
	if err := os.WriteFile(cur, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newExe, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceExeAt(cur, newExe); err != nil {
		t.Fatalf("replaceExeAt failed: %v", err)
	}

	got, err := os.ReadFile(cur)
	if err != nil || string(got) != "new" {
		t.Errorf("current exe content = %q (err: %v), want \"new\"", got, err)
	}
	if _, err := os.Stat(newExe); !os.IsNotExist(err) {
		t.Error("new exe should have been moved away")
	}
	if _, err := os.Stat(cur + ".bak"); !os.IsNotExist(err) {
		t.Error(".bak should be cleaned up after successful replace")
	}
}

func TestReplaceExeAt_CurrentMissing(t *testing.T) {
	dir := t.TempDir()
	newExe := filepath.Join(dir, "new.exe")
	if err := os.WriteFile(newExe, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := replaceExeAt(filepath.Join(dir, "missing.exe"), newExe)
	if err == nil {
		t.Fatal("expected error when current exe does not exist")
	}
	// on failure the new exe must be untouched (rollback semantics)
	if _, statErr := os.Stat(newExe); statErr != nil {
		t.Error("new exe should remain untouched on failure")
	}
}

// writeTestZip builds a zip with the given entries in dir, returns the zip path.
func writeTestZip(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	zipPath := filepath.Join(dir, "test.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return zipPath
}

func TestExtractSingleExe(t *testing.T) {
	dir := t.TempDir()
	zipPath := writeTestZip(t, dir, map[string]string{
		"readme.txt":                   "hi",
		"danbooru-tag-mcp-windows/bin": "skip", // directory prefix should still match by base name
		"danbooru-tag-mcp.exe":         "PAYLOAD",
	})

	out, err := extractSingleExe(zipPath, dir)
	if err != nil {
		t.Fatalf("extractSingleExe failed: %v", err)
	}
	defer os.Remove(out)

	got, err := os.ReadFile(out)
	if err != nil || string(got) != "PAYLOAD" {
		t.Errorf("extracted content = %q (err: %v), want \"PAYLOAD\"", got, err)
	}
}

func TestExtractSingleExe_MissingExe(t *testing.T) {
	dir := t.TempDir()
	zipPath := writeTestZip(t, dir, map[string]string{
		"readme.txt": "hi",
	})

	_, err := extractSingleExe(zipPath, dir)
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("expected 'does not contain' error, got: %v", err)
	}
}

func TestDownloadOnce(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset.zip")
	if err := downloadOnce(srv.URL, dest); err != nil {
		t.Fatalf("downloadOnce failed: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "hello" {
		t.Errorf("downloaded content = %q, want \"hello\"", got)
	}
	if !strings.HasPrefix(gotUA, "danbooru-tag-mcp/") {
		t.Errorf("User-Agent = %q, want danbooru-tag-mcp/* prefix", gotUA)
	}
}

func TestDownloadOnce_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset.zip")
	err := downloadOnce(srv.URL, dest)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got: %v", err)
	}
}
