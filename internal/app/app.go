// Package app provides cross-package shared infrastructure: version number,
// error exit, User-Agent, the upstream-failure and tag-limit sentinels, and
// the startup bootstrap (PATH injection) switch. Centralizing these here
// avoids import cycles between env / upgrade / api / tools, etc.
package app

import (
	"errors"
	"fmt"
	"os"
)

// ErrUpstreamUnavailable is matched (errors.Is) by every error that means
// Danbooru could not serve a request even after retries: a transport
// failure, a 5xx, or a 429/421. The api layer raises it; the tools layer
// turns it into one uniform result on every tool.
var ErrUpstreamUnavailable = errors.New("danbooru unavailable")

// ErrTagLimitExceeded is matched (errors.Is) by the error for a 422 whose body
// carries Danbooru's PostQuery::TagLimitError: the query names more terms
// (tags plus counting metatags) than the account's level allows. It is a
// definitive answer from a reachable upstream — never an outage — so it
// carries no upstream marker and its text stays the plain shrunken 422. The
// api layer raises it; the service layer branches on it to fall back from a
// refused direct query to the local scan. It lives here, next to
// ErrUpstreamUnavailable, because the service must not import internal/api.
var ErrTagLimitExceeded = errors.New("danbooru tag limit exceeded")

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
