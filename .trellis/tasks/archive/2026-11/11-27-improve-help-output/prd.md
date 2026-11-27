# Make danbooru-tag-mcp help worth reading

## Goal

`danbooru-tag-mcp help` becomes an accurate, complete reference to what the
installed binary does, for someone who already has it. PRD-only task.

User value: today the output spends 8 of its 31 lines on install
instructions that are useless once the binary exists, and its Commands
section documents 4 of the 8 spellings the router accepts, with nothing
about what `upgrade` returns. The maintainer's verdict: "几乎没有价值".

## Background (confirmed facts)

Evidence with line anchors is in `research/help-current-state.md`.

- The output of 0.6.0 (identical at `main`) has six sections: title, Usage,
  Modes, Commands, Install, Environment variables. Exit code 0, everything
  on stdout (`main.go:111-144`).
- The router (`main.go:51-70`) accepts eight spellings: `serve`, `upgrade`,
  `version`, `-v`, `--version`, `help`, `-h`, `--help`. Help lists four.
- "Modes" and "Commands" both describe `serve`.
- Nothing documents what `upgrade` does or returns. It takes no arguments,
  replaces the exe in place, exits 0 for "updated", "already up to date"
  and "local version is newer", and 1 through `app.Fail` for every failure
  (`internal/upgrade/upgrade.go:66-148`).
- Exit codes are undocumented: 0 on success; 1 for an unknown command
  (`main.go:65-67`), a failed upgrade, or a server that could not start
  (`main.go:89`).
- The environment-variable block matches the four variables the binary
  reads. `DANBOORU_MCP_NO_CACHE` leaves with task
  `11-27-remove-response-cache`.
- The README "CLI" block (`README.md:177-184`) lists four commands and no
  aliases; nothing tests `usage()`.

## Requirements

- R1: remove the Install section.
- R2: the Commands section lists every spelling the router accepts, and
  each command says what it does, what it takes, and what comes back. The
  "Modes" section disappears; its default-command note moves to `serve`.
- R3: keep the MCP client config example and the one-line note that the
  first run adds its own directory to the user PATH.
- R4: the Environment variables block lists exactly the variables the
  binary reads, each with its default.
- R5: document the exit codes.
- R6: one table drives both the router lookup and the Commands section, so
  a command cannot be accepted but undocumented. A unit test in package
  `main` walks that table and fails when a name or alias is missing from
  the `usage()` output, or when the word "Install" returns.
- R7: `help`, `-h` and `--help` stay identical, exit 0, and write to stdout
  only. An unknown command still prints its message, then the help, and
  exits 1. Command names and routing semantics do not change.
- R8: the README "CLI" block agrees with the help text.

### Required content

The text below is the specification. Wording may be tightened, but no
listed fact may be dropped.

```
danbooru-tag-mcp - Danbooru tag lookup MCP server

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
```

## Acceptance Criteria

- [ ] `danbooru-tag-mcp help`, `-h` and `--help` print the required content, exit 0, and write nothing to stderr.
- [ ] The output contains no install instructions and no "Modes" section.
- [ ] A unit test in package `main` reads the command table and fails if any name or alias is absent from the `usage()` output, or if "Install" is back.
- [ ] `danbooru-tag-mcp frobnicate` prints `unknown command: frobnicate`, then the help, and exits 1.
- [ ] `danbooru-tag-mcp version` output is unchanged.
- [ ] The environment-variable block matches what the binary reads.
- [ ] The README "CLI" block matches the help text.
- [ ] `gofmt -l main.go` is empty; `go vet ./...` and `make test` pass.

## Technical Notes

- Smallest shape for R6: a `cliCommand` table in `main.go` (name, aliases,
  help lines, action). `usage()` renders the Commands section from it, the
  router resolves the first argument through it, and the test captures the
  output of `usage()` by swapping `os.Stdout`.
- `usage()` stays CLI-mode only. On the server path stdout carries JSON-RPC
  (AGENTS.md, Key conventions).

## Out of Scope

- The version number in the help title (`version` already prints it).
- Listing the eight MCP tools.
- The output or behavior of `upgrade`.
- The usage comments in `install.ps1` and the NSIS script.

## Ordering

Lands after `11-27-remove-response-cache`: both edit `usage()` in
`main.go`, and the environment-variable block must not advertise
`DANBOORU_MCP_NO_CACHE`.
