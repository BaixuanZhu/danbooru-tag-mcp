package app

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Bootstrap controls whether startup performs the silent bootstrap
// (registering the exe directory into the user PATH).
// Source default "on": binaries from plain go build / go install behave like
// release builds. Local dev builds get -X danbooru-tag-mcp/internal/app.Bootstrap=off
// injected by the Makefile so dist/ artifacts do not pollute the system
// environment (an ldflags-injected var, same convention as Version).
var Bootstrap = "on"

// BootstrapEnabled reports whether this run should perform the startup
// bootstrap (called from the main startup flow).
func BootstrapEnabled() bool {
	exe, err := os.Executable()
	if err != nil {
		exe = "" // on failure don't block; treat as non-Temp
	}
	return bootstrapEnabled(exe, os.Getenv("DANBOORU_MCP_NO_BOOTSTRAP"), os.TempDir())
}

// bootstrapEnabled is the bootstrap decision logic. Any hit disables it:
//  1. DANBOORU_MCP_NO_BOOTSTRAP env var non-empty (explicit switch, any build)
//  2. Bootstrap=off injected via ldflags (make build dev artifacts)
//  3. The exe itself lives under the system Temp dir (go run's go-build temp
//     binary, verification copies in Temp) — registering an ephemeral location
//     into PATH is guaranteed to become a dead link
//
// Takes explicit params instead of reading globals (except the Bootstrap var,
// same convention as Version) for table-driven tests.
func bootstrapEnabled(exe, noBootEnv, tempDir string) bool {
	if strings.TrimSpace(noBootEnv) != "" {
		return false
	}
	if Bootstrap == "off" {
		return false
	}
	if exe == "" {
		return true
	}
	return !isUnderTemp(exe, tempDir)
}

// isUnderTemp reports whether exe is located under tempDir (tempDir itself
// counts). Pure path comparison; both args are normalized first. Takes
// explicit params for table-driven tests.
func isUnderTemp(exe, tempDir string) bool {
	e := normalizePath(exe)
	t := normalizePath(tempDir)
	return e == t || strings.HasPrefix(e, t+`\`)
}

// normalizePath normalizes a Windows path for comparison: best-effort 8.3
// short-name expansion + Clean + lowercase. If expansion fails (path does not
// exist), falls back to the Clean+lowercase original (comparison may still
// mismatch; the caller handles the fallout).
func normalizePath(p string) string {
	if lp := longPathName(p); lp != "" {
		p = lp
	}
	return strings.ToLower(filepath.Clean(p))
}

// longPathName calls Win32 GetLongPathName to expand 8.3 short names
// (e.g. ZBXCOM~1 -> zbxComputer). os.TempDir() may return the short form while
// os.Executable() returns the long form (or vice versa); comparing without
// expansion mismatches. Returns "" on failure (path does not exist etc.),
// and the caller falls back to the original value.
func longPathName(p string) string {
	p16, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 4096)
	n, err := syscall.GetLongPathName(p16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}
