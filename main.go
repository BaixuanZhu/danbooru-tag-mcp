// Command danbooru-tag-mcp is a Danbooru tag lookup MCP server (stdio
// transport) that helps local AI image generation pick correct Danbooru tags.
// With no arguments it runs as the MCP server; it also exposes CLI subcommands
// (upgrade / version / help) for the installer and end users.
//
// Usage:
//
//	danbooru-tag-mcp          # MCP server mode (what MCP clients configure)
//	danbooru-tag-mcp upgrade  # self-update via GitHub Release
//
// This file only routes commands and runs startup bootstrap; the MCP
// implementation lives in api/ service/ tools/, self-update and PATH
// injection in internal/.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"danbooru-tag-mcp/internal/api"
	"danbooru-tag-mcp/internal/app"
	"danbooru-tag-mcp/internal/env"
	"danbooru-tag-mcp/internal/service"
	"danbooru-tag-mcp/internal/tools"
	"danbooru-tag-mcp/internal/upgrade"
	"danbooru-tag-mcp/internal/vocab"

	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// All non-protocol logs go to stderr, keeping stdout pure for JSON-RPC transport
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Silent bootstrap: add the exe's own directory to the user PATH (first
	// run, idempotent). This lets MCP client configs use the bare command name
	// "danbooru-tag-mcp". Skipped when (see app.BootstrapEnabled): dev builds
	// (make build injects off), DANBOORU_MCP_NO_BOOTSTRAP non-empty, or the
	// exe is under the system Temp dir (go run temp binary).
	if app.BootstrapEnabled() {
		env.EnsureUserPath()
	}
	// Clean up a .bak left by the last upgrade (undeletable while the old
	// process still holds it; retried on next startup)
	upgrade.CleanupStaleBak()

	// Subcommand routing: no args = MCP server mode (keeps zero-config client compat)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			// explicit server mode, same as no args; falls through to serve()
		case "upgrade":
			upgrade.Run()
			return
		case "version", "-v", "--version":
			fmt.Println("danbooru-tag-mcp", app.Version)
			return
		case "help", "-h", "--help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
			usage()
			os.Exit(1)
		}
	}

	serve()
}

// serve starts the MCP stdio server. The version reuses app.Version (ldflags
// injected) so it matches what the version / upgrade subcommands report.
func serve() {
	client := api.NewClient(clientOptions()...)
	svc := service.NewTagService(client, vocab.Default())

	s := server.NewMCPServer(
		"danbooru-tags",
		app.Version,
		server.WithToolCapabilities(true),
	)
	tools.Register(s, svc)

	log.Println("[INFO] danbooru-mcp server initialized, serving on stdio...")
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("[FATAL] mcp server terminated: %v", err)
	}
}

// clientOptions builds the api client options for server mode: the response
// cache under %LOCALAPPDATA%\danbooru-tag-mcp\cache, skipped entirely (no
// cache) when DANBOORU_MCP_NO_CACHE is non-empty or LOCALAPPDATA is unset.
// WithCache itself degrades to cache-less with a stderr warning when the
// location cannot be opened, so no extra error handling is needed here.
func clientOptions() []api.Option {
	if os.Getenv("DANBOORU_MCP_NO_CACHE") != "" {
		return nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return nil
	}
	return []api.Option{api.WithCache(filepath.Join(base, "danbooru-tag-mcp", "cache"))}
}

// usage prints CLI help (only on the explicit help subcommand; never pollutes
// server-mode stdout)
func usage() {
	fmt.Print(`danbooru-tag-mcp - Danbooru tag lookup MCP server

Usage:
  danbooru-tag-mcp [command]

Modes:
  (no args) or serve   Run the MCP server over stdio for MCP clients.
                       Client config example (JSON): { "command": "danbooru-tag-mcp", "args": [] }
                       The first run silently registers its directory into the user PATH (idempotent).

Commands:
  serve            Run the MCP server over stdio (same as no args)
  upgrade          Check and update itself (via GitHub Release, SHA256 verified)
  version          Print the version
  help             Show this help

Install:
  Double-click installer (GitHub Release page):
    danbooru-tag-mcp-windows-<arch>-setup.exe   (per-user setup wizard)
  PowerShell one-liner (installs to %LOCALAPPDATA%\Programs\danbooru-tag-mcp by default,
  use -InstallDir for another directory; registers the user PATH automatically):
    iwr -useb "https://raw.githubusercontent.com/BaixuanZhu/danbooru-tag-mcp/main/install.ps1" | iex
  Local / custom directory install:
    .\install.ps1 -InstallDir "D:\tools\danbooru-tag-mcp"

Environment variables:
  DANBOORU_LOGIN / DANBOORU_API_KEY   Danbooru API credentials (optional; anonymous access without them)
  DANBOORU_REQ_GAP_MS                 Min spacing between requests in ms (0 = no gap; invalid keeps
                                      the default: 1500 anonymous / 500 with credentials)
  DANBOORU_MCP_NO_BOOTSTRAP           Skip PATH registration at startup when non-empty
  DANBOORU_MCP_NO_CACHE               Disable the response cache when non-empty
`)
}
