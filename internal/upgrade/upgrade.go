// Package upgrade checks GitHub Releases and updates danbooru-tag-mcp itself
// (invoked by the `danbooru-tag-mcp upgrade` subcommand, CLI mode only — the
// MCP server mode is unaffected).
//
// Mechanism: call the GitHub API /repos/{owner}/{repo}/releases/latest for the
// latest release, compare tag_name against the current version, download the
// agreed asset (zip containing a single exe), verify SHA256 (checksums.txt),
// then extract and replace the current exe.
// Windows cannot overwrite a running exe but can rename it: old exe -> .bak,
// new exe moved into place; if the .bak cannot be deleted because the old
// process still holds it, CleanupStaleBak removes it on the next startup.
//
// Deployment notes (when publishing a release):
//   - tags use the v0.2.0 format
//   - upload asset: danbooru-tag-mcp-windows-amd64.zip (zip contains a single danbooru-tag-mcp.exe)
//   - upload checksums.txt (sha256sum format, shared by upgrade and install.ps1)
package upgrade

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"danbooru-tag-mcp/internal/app"
)

// githubRepo is the GitHub repository publishing danbooru-tag-mcp (owner/repo format).
const githubRepo = "BaixuanZhu/danbooru-tag-mcp"

// exeName is the agreed executable file name inside the zip (matches BINARY in the Makefile).
const exeName = "danbooru-tag-mcp.exe"

// downloadClient downloads release assets without an overall timeout:
// timeout control is left to the transfer itself so slow networks don't kill large downloads.
var downloadClient = &http.Client{Timeout: 0}

// githubRelease mirrors the GitHub API releases/latest response (only the needed fields)
type githubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// expectedAssetName returns the release asset file name agreed for this platform.
// Full name = danbooru-tag-mcp-{GOOS}-{GOARCH}.zip, e.g. danbooru-tag-mcp-windows-amd64.zip
func expectedAssetName() string {
	return fmt.Sprintf("danbooru-tag-mcp-%s-%s.zip", runtime.GOOS, runtime.GOARCH)
}

// Run checks for and installs the latest version.
func Run() {
	fmt.Printf("🔍 Checking the latest release of %s...\n", githubRepo)
	rel, err := fetchLatestRelease()
	if err != nil {
		app.Fail("failed to check for updates: " + err.Error())
	}

	latestVersion := strings.TrimPrefix(rel.TagName, "v")
	fmt.Printf("   Latest: %s (current: %s)\n", latestVersion, app.Version)

	// Semantic comparison against the local version prevents a dev/patched
	// build from being silently downgraded by an older release
	switch compareVersions(app.Version, latestVersion) {
	case 0:
		fmt.Println("✅ Already up to date.")
		return
	case 1:
		fmt.Println("⚠️  Local version is newer than the latest release, skipping update.")
		return
	}

	// Find the asset matching the current platform
	assetURL := ""
	want := expectedAssetName()
	for _, a := range rel.Assets {
		if a.Name == want {
			assetURL = a.BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		app.Fail(fmt.Sprintf("asset %s not found in the latest release.\n   Download it manually from %s.", want, rel.HTMLURL))
	}

	fmt.Printf("⬇️  Downloading %s ...\n", want)
	tmpZip := filepath.Join(os.TempDir(), "danbooru-mcp-upgrade-"+latestVersion+".zip")
	if err := downloadFile(assetURL, tmpZip, 3); err != nil {
		app.Fail("download failed: " + err.Error())
	}
	defer os.Remove(tmpZip)

	// SHA256 verification: take the expected hash from the release's checksums.txt
	// asset and compare it against the downloaded file. If an old release has no
	// checksums.txt, print a warning and continue (backwards compatible).
	if expectedHash, ok := fetchExpectedChecksum(rel, want); ok {
		fmt.Print("🔐 Verifying SHA256... ")
		got, err := fileSHA256(tmpZip)
		if err != nil {
			app.Fail("failed to compute SHA256 of the download: " + err.Error())
		}
		if got != expectedHash {
			os.Remove(tmpZip)
			app.Fail(fmt.Sprintf("checksum mismatch\n   want: %s\n   got:  %s", expectedHash, got))
		}
		fmt.Println("OK")
	} else {
		fmt.Println("⚠️  Release has no checksums.txt, skipping integrity check.")
	}

	fmt.Print("📂 Extracting... ")
	// Extract into the current exe's directory to avoid a cross-volume rename
	// failure (os.Rename uses MoveFileEx, which cannot cross volumes)
	selfExe, err := os.Executable()
	if err != nil {
		app.Fail("failed to locate the current exe: " + err.Error())
	}
	selfDir := filepath.Dir(selfExe)
	tmpExe, err := extractSingleExe(tmpZip, selfDir)
	if err != nil {
		app.Fail("extract failed: " + err.Error())
	}
	defer os.Remove(tmpExe)
	fmt.Println("done")

	fmt.Print("🔄 Replacing binary... ")
	if err := replaceSelf(tmpExe); err != nil {
		app.Fail("replace failed: " + err.Error())
	}
	fmt.Println("done")

	fmt.Printf("\n✅ Updated to %s\n", latestVersion)
	fmt.Println("   The new version takes effect on next launch (MCP client reconnect).")
}

// fetchLatestRelease calls the GitHub API for the latest release
func fetchLatestRelease() (*githubRelease, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", app.UserAgent())
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var rel githubRelease
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("failed to parse the GitHub response: %w", err)
	}
	return &rel, nil
}

// downloadFile downloads url to dest, retrying up to retries times on failure (linear backoff).
func downloadFile(url, dest string, retries int) error {
	var lastErr error
	for i := 0; i <= retries; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * time.Second)
		}
		lastErr = downloadOnce(url, dest)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// downloadOnce is a single download attempt: streamed to disk, non-200 counts as failure.
func downloadOnce(url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", app.UserAgent())

	resp, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// extractSingleExe extracts exeName from the zip into a temp file in dir and
// returns its path. Placing the temp file in dir (usually the current exe's
// directory) guarantees the later os.Rename stays on the same volume —
// Windows MoveFileEx degrades to copy+delete across volumes, which fails for
// a running exe.
func extractSingleExe(zipPath, dir string) (string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	for _, f := range reader.File {
		if filepath.Base(f.Name) != exeName {
			continue
		}
		out, err := os.CreateTemp(dir, "danbooru-mcp-new-*.exe")
		if err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return "", err
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			os.Remove(out.Name())
			return "", err
		}
		rc.Close()
		out.Close()
		return out.Name(), nil
	}
	return "", fmt.Errorf("zip does not contain %s", exeName)
}

// replaceSelf replaces the currently running danbooru-tag-mcp with the new exe.
// The standard Windows approach: rename the old exe to .bak (a running exe
// cannot be overwritten but can be renamed), then move the new exe into place.
func replaceSelf(newExePath string) error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to locate the current exe: %w", err)
	}
	currentExe, _ = filepath.Abs(currentExe)
	return replaceExeAt(currentExe, newExePath)
}

// replaceExeAt atomically replaces currentExe with newExe (via a .bak hop).
// Split out with explicit paths so table-driven tests can use fake exes in a
// temp directory.
func replaceExeAt(currentExe, newExePath string) error {
	bakPath := currentExe + ".bak"
	os.Remove(bakPath) // clean up a possibly stale .bak

	// 1. Rename the current exe to .bak
	if err := os.Rename(currentExe, bakPath); err != nil {
		return fmt.Errorf("failed to rename the old exe (file may be in use): %w", err)
	}
	// 2. Move the new exe into place
	if err := os.Rename(newExePath, currentExe); err != nil {
		os.Rename(bakPath, currentExe) // roll back
		return fmt.Errorf("failed to move the new exe: %w", err)
	}
	// 3. Try to delete the .bak (may fail, error ignored)
	os.Remove(bakPath)
	return nil
}

// fetchExpectedChecksum parses the expected SHA256 of filename from the
// release's checksums.txt asset. checksums.txt uses the GNU coreutils
// sha256sum format: "<hash>  <filename>".
// Returns ("", false) when the checksums.txt asset is missing or the file
// name does not match.
func fetchExpectedChecksum(rel *githubRelease, filename string) (string, bool) {
	var checksumURL string
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			checksumURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumURL == "" {
		return "", false
	}

	tmpChecksum := filepath.Join(os.TempDir(), "danbooru-mcp-checksums.txt")
	if err := downloadFile(checksumURL, tmpChecksum, 3); err != nil {
		return "", false
	}
	defer os.Remove(tmpChecksum)

	data, err := os.ReadFile(tmpChecksum)
	if err != nil {
		return "", false
	}
	return parseChecksum(string(data), filename)
}

// parseChecksum finds the hash for filename in sha256sum-format text.
// GNU coreutils has two formats:
//
//	"<hash>  <filename>"  text mode (two spaces, no asterisk)
//	"<hash> *<filename>"  binary mode (one space + leading asterisk)
//
// The sha256sum bundled with Windows Git Bash defaults to binary mode (a
// leading '*' on the name), so strip a leading '*' before comparing.
// Pure function, for table-driven tests.
func parseChecksum(text, filename string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// split into hash and name
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		hash, name := fields[0], fields[1]
		name = strings.TrimPrefix(name, "*") // tolerate binary-mode '*' prefix
		if name == filename {
			return hash, true
		}
	}
	return "", false
}

// compareVersions compares two version numbers semantically, returning
// -1/0/1 (a<b / a==b / a>b). Inputs should be clean version numbers (no v
// prefix), e.g. "0.2.0"; a non-numeric suffix is allowed (dev builds like
// "0.1.0-dev", suffix segments count as 0). Splits on non-digit characters
// and compares segment by segment; missing segments count as 0.
// Pure function, for table-driven tests.
func compareVersions(a, b string) int {
	pa := versionSegments(a)
	pb := versionSegments(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		va, vb := 0, 0
		if i < len(pa) {
			va = pa[i]
		}
		if i < len(pb) {
			vb = pb[i]
		}
		if va != vb {
			if va < vb {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionSegments splits a version number into digit segments on non-digit
// characters, e.g. "0.2.1" -> [0,2,1]. Invalid segments count as 0. Pure function.
func versionSegments(s string) []int {
	var parts []int
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool {
		return r < '0' || r > '9'
	}) {
		if seg == "" {
			continue
		}
		n, err := strconv.Atoi(seg)
		if err != nil {
			n = 0
		}
		parts = append(parts, n)
	}
	return parts
}

// fileSHA256 computes the SHA256 of a file (lowercase hex).
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CleanupStaleBak removes a leftover danbooru-tag-mcp.exe.bak.
//
// replaceSelf renames the old exe to .bak during an upgrade and deletes it
// immediately on success; but while the old exe is still running the delete
// fails (Windows file lock), so the .bak can linger. This is called on every
// startup (including MCP server mode) to clear .baks left by the last upgrade.
// Failures are silently ignored (a still-locked .bak or missing permissions
// must not block the main flow).
func CleanupStaleBak() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	cleanupBakAt(exePath)
}

// cleanupBakAt removes the .bak belonging to exePath. Pure path manipulation,
// for table-driven tests. Returns whether a file was actually removed
// (a non-existing .bak counts as nothing to clean).
func cleanupBakAt(exePath string) bool {
	bakPath := exePath + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		return false // does not exist, nothing to clean
	}
	return os.Remove(bakPath) == nil
}
