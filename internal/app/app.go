// Package app provides cross-package shared infrastructure: version number,
// error exit, User-Agent, and the startup bootstrap (PATH injection) switch.
// Centralizing these here avoids import cycles between env / upgrade, etc.
package app

import (
	"fmt"
	"os"
)

// Version is the version of danbooru-tag-mcp itself.
// Injected via ldflags at build time (see Makefile: -X danbooru-tag-mcp/internal/app.Version=...);
// local go run / go builds without injection fall back to this default.
var Version = "0.1.0-dev"

// Fail prints an error message to stderr and exits with code 1.
// Used by CLI subcommands on unrecoverable errors.
func Fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// UserAgent returns the unified HTTP User-Agent string.
// Referenced by the upgrade package for GitHub API calls and asset downloads,
// keeping the reported version consistent.
func UserAgent() string {
	return fmt.Sprintf("danbooru-tag-mcp/%s", Version)
}
