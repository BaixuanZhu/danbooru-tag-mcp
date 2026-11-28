package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/app"
)

// goldenUsage pins the exact help text the binary must print. It mirrors the
// task PRD's target wording byte-for-byte; changing it (or usage()) is a
// deliberate, reviewed edit, not an accident.
const goldenUsage = `danbooru-tag-mcp - Danbooru tag lookup MCP server

Usage:
  danbooru-tag-mcp [command]

Commands:
  serve            Run the MCP server over stdio. This is the default when no
                   command is given, and what MCP clients launch.
  upgrade          Replace this exe with the latest GitHub Release (SHA256
                   verified). Takes no arguments; the new version starts on
                   the next launch.
  version          Print the version.       Aliases: -v, --version
  help             Show this help.          Aliases: -h, --help

MCP client config:
  { "command": "danbooru-tag-mcp", "args": [] }
  The first run adds its own directory to the user PATH, silently.

Environment variables:
  DANBOORU_LOGIN / DANBOORU_API_KEY   Danbooru API credentials. With both set
                                      requests are authenticated and
                                      search_posts tries up to 6 tags directly
                                      (anonymous: 2); otherwise the client is
                                      anonymous.
  DANBOORU_REQ_GAP_MS                 Minimum spacing between requests in ms.
                                      Default 1500 anonymous, 500 with
                                      credentials. 0 = no gap; an invalid
                                      value keeps the default.
  DANBOORU_MCP_NO_BOOTSTRAP           Skip the PATH registration at startup
                                      when non-empty.

Exit codes:
  0  success (upgrade also exits 0 when already up to date)
  1  unknown command, a failed upgrade, or a server that could not start
`

// captureStdout runs fn with os.Stdout redirected into a pipe and returns
// what it printed. fmt.Print* resolve os.Stdout at call time, so the swap
// is observed by everything fn prints.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}

// TestUsage_MatchesGolden pins the full help output byte-for-byte.
func TestUsage_MatchesGolden(t *testing.T) {
	if got, want := captureStdout(t, usage), goldenUsage; got != want {
		t.Errorf("usage() drifted from the pinned help text.\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

// TestUsage_DocumentsEveryCommand is the router/help drift guard: every name
// and alias in cliCommands must appear in the rendered help (nothing the
// router accepts may go undocumented), and the removed Install / Modes
// sections must stay removed.
func TestUsage_DocumentsEveryCommand(t *testing.T) {
	out := captureStdout(t, usage)
	for _, banned := range []string{"Install", "Modes"} {
		if strings.Contains(out, banned) {
			t.Errorf("usage() mentions %q; that section was removed deliberately", banned)
		}
	}
	for _, cmd := range cliCommands() {
		if !strings.Contains(out, "  "+cmd.name) {
			t.Errorf("usage() does not show command %q", cmd.name)
		}
		if len(cmd.aliases) == 0 {
			continue
		}
		want := "Aliases: " + strings.Join(cmd.aliases, ", ")
		if !strings.Contains(out, want) {
			t.Errorf("usage() does not list aliases of %q as %q", cmd.name, want)
		}
	}
}

// TestLookupCommand_TableDrivesRouter pins the routing semantics: every name
// and alias in the table resolves to its own command, unknown spellings are
// rejected, and only serve carries a nil run (the fall-through to serve()).
func TestLookupCommand_TableDrivesRouter(t *testing.T) {
	for _, cmd := range cliCommands() {
		got, ok := lookupCommand(cmd.name)
		if !ok || got.name != cmd.name {
			t.Errorf("lookupCommand(%q) = %q, %v; want command %q", cmd.name, got.name, ok, cmd.name)
		}
		for _, a := range cmd.aliases {
			got, ok := lookupCommand(a)
			if !ok || got.name != cmd.name {
				t.Errorf("lookupCommand(%q) = %q, %v; want command %q", a, got.name, ok, cmd.name)
			}
		}
		wantNil := cmd.name == "serve" // only serve falls through to serve()
		if (cmd.run == nil) != wantNil {
			t.Errorf("command %q has run == nil: %v; want %v", cmd.name, cmd.run == nil, wantNil)
		}
	}
	for _, unknown := range []string{"", "frobnicate", "Serve", "VERSION", "--HELP"} {
		if got, ok := lookupCommand(unknown); ok {
			t.Errorf("lookupCommand(%q) accepted unknown spelling as %q", unknown, got.name)
		}
	}
}

// TestVersionRun_KeepsOutputShape pins the version command's one-line
// `danbooru-tag-mcp <version>` output so a table refactor cannot change it.
func TestVersionRun_KeepsOutputShape(t *testing.T) {
	cmd, ok := lookupCommand("version")
	if !ok || cmd.run == nil {
		t.Fatalf("version command not routable through the table")
	}
	want := "danbooru-tag-mcp " + app.Version + "\n"
	if got := captureStdout(t, cmd.run); got != want {
		t.Errorf("version output = %q; want %q", got, want)
	}
}
