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
	"strings"

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

	// Subcommand routing: no args = MCP server mode (keeps zero-config client
	// compat). The lookup goes through cliCommands — the same table usage()
	// renders — so a command cannot be accepted without being documented.
	if len(os.Args) > 1 {
		cmd, ok := lookupCommand(os.Args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
			usage()
			os.Exit(1)
		}
		if cmd.run != nil {
			cmd.run()
			return
		}
		// A nil run marks server mode: explicit serve falls through to
		// serve(), exactly like no args.
	}

	serve()
}

// cliCommand describes one CLI subcommand. cliCommands drives both the
// router (via lookupCommand) and the Commands section of usage(), so a
// command spelling cannot be accepted without being documented.
type cliCommand struct {
	// name is the canonical spelling accepted on the command line.
	name string
	// aliases are extra accepted spellings, rendered as "Aliases: ..." in
	// the help text.
	aliases []string
	// help holds the description lines: help[0] aligns under the command
	// name, the rest are indented continuation lines. Lines are pre-wrapped
	// to keep the help within 80 columns.
	help []string
	// run is the command action. nil means server mode: main falls through
	// to serve() (serve is the default command, so it shares the no-args
	// path instead of carrying its own closure).
	run func()
}

// cliCommands returns the command table — the single source of truth for
// CLI subcommands: the router resolves os.Args[1] through it and usage()
// renders the Commands section from it, so a command cannot be accepted
// without being documented. Order is the display order in the help output;
// serve stays first because it is the default. It is a function rather than
// a package var to keep the table out of package-level mutable state; every
// call returns a fresh slice. Routing semantics: a nil run falls through to
// serve().
func cliCommands() []cliCommand {
	return []cliCommand{
		{
			name: "serve",
			help: []string{
				"Run the MCP server over stdio. This is the default when no",
				"command is given, and what MCP clients launch.",
			},
		},
		{
			name: "upgrade",
			help: []string{
				"Replace this exe with the latest GitHub Release (SHA256",
				"verified). Takes no arguments; the new version starts on",
				"the next launch.",
			},
			run: upgrade.Run,
		},
		{
			name:    "version",
			aliases: []string{"-v", "--version"},
			help:    []string{"Print the version."},
			run:     func() { fmt.Println("danbooru-tag-mcp", app.Version) },
		},
		{
			name:    "help",
			aliases: []string{"-h", "--help"},
			help:    []string{"Show this help."},
			run:     func() { usage() },
		},
	}
}

// lookupCommand resolves a CLI argument against cliCommands, matching the
// canonical name first and then the aliases. ok is false for anything the
// table does not accept (main then reports an unknown command).
func lookupCommand(arg string) (cliCommand, bool) {
	for _, cmd := range cliCommands() {
		if arg == cmd.name {
			return cmd, true
		}
		for _, a := range cmd.aliases {
			if arg == a {
				return cmd, true
			}
		}
	}
	return cliCommand{}, false
}

// serve starts the MCP stdio server. The version reuses app.Version (ldflags
// injected) so it matches what the version / upgrade subcommands report.
func serve() {
	client := api.NewClient()
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

// usage prints CLI help (only on the explicit help subcommand or after an
// unknown command; never pollutes server-mode stdout). The Commands section
// is rendered from cliCommands so the router and the help cannot drift; the
// remaining sections are static. Column widths (17-char command field,
// 25-char description before "Aliases:", 19/38-space continuations) are the
// pinned layout of the help text.
func usage() {
	fmt.Print(`danbooru-tag-mcp - Danbooru tag lookup MCP server

Usage:
  danbooru-tag-mcp [command]

Commands:
`)
	for _, cmd := range cliCommands() {
		for i, line := range cmd.help {
			if i > 0 {
				fmt.Printf("                   %s\n", line)
				continue
			}
			if len(cmd.aliases) > 0 {
				fmt.Printf("  %-17s%-25sAliases: %s\n",
					cmd.name, line, strings.Join(cmd.aliases, ", "))
			} else {
				fmt.Printf("  %-17s%s\n", cmd.name, line)
			}
		}
	}
	fmt.Print(`
MCP client config:
  { "command": "danbooru-tag-mcp", "args": [] }
  The first run adds its own directory to the user PATH, silently.

Environment variables:
  DANBOORU_LOGIN / DANBOORU_API_KEY   Danbooru API credentials. With both set
                                      requests are authenticated; otherwise
                                      the client is anonymous.
  DANBOORU_REQ_GAP_MS                 Minimum spacing between requests in ms.
                                      Default 1500 anonymous, 500 with
                                      credentials. 0 = no gap; an invalid
                                      value keeps the default.
  DANBOORU_MCP_NO_BOOTSTRAP           Skip the PATH registration at startup
                                      when non-empty.

Exit codes:
  0  success (upgrade also exits 0 when already up to date)
  1  unknown command, a failed upgrade, or a server that could not start
`)
}
