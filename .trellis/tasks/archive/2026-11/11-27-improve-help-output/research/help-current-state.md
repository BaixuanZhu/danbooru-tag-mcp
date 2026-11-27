# `danbooru-tag-mcp help` — current state

Date: 2026-11-27. Captured from the installed 0.6.0 exe with
`DANBOORU_MCP_NO_BOOTSTRAP=1`; `usage()` at `main` (acd06f9) is identical.

## Output (exit 0, stdout only, 31 lines)

```
danbooru-tag-mcp - Danbooru tag lookup MCP server

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
```

## Router (`main.go:51-70`)

| Argument | Action | Exit code |
|---|---|---|
| none, `serve` | `serve()` | never returns while serving; 1 via `log.Fatalf` if the stdio server fails (`main.go:89`) |
| `upgrade` | `upgrade.Run()` | 0 or 1 |
| `version`, `-v`, `--version` | prints `danbooru-tag-mcp <version>` | 0 |
| `help`, `-h`, `--help` | `usage()` | 0 |
| anything else | `unknown command: X` on stderr, then `usage()` on stdout | 1 |

Before routing, every start runs `env.EnsureUserPath()` unless bootstrap is
disabled, and `upgrade.CleanupStaleBak()`.

## `upgrade` (`internal/upgrade/upgrade.go:66-148`)

- Reads no arguments and no flags.
- stdout: checking, latest vs current, downloading, progress, `OK`, `done`,
  `Updated to <version>`, and the reminder that the new version takes
  effect on the next launch (MCP client reconnect).
- Exit 0 outcomes: updated; `Already up to date.`; `Local version is newer
  than the latest release, skipping update.`
- Exit 1 through `app.Fail` (message on stderr, `internal/app/app.go:18-21`):
  release check failed, asset missing in the release, download failed,
  checksum computation failed, checksum mismatch, current exe not located,
  extract failed, replace failed.
- When the release has no `checksums.txt` it prints a warning and skips
  the integrity check.

## Startup PATH registration

`internal/env/env.go:176-194`, `internal/app/bootstrap.go`.

- On every start of a release build, appends the exe's directory to the
  user PATH when it is missing. Silent: failures are ignored, nothing is
  printed, and the process never exits because of it.
- Skipped when `DANBOORU_MCP_NO_BOOTSTRAP` is non-empty, when the build
  injects `Bootstrap=off` (`make build`), or when the exe lives under the
  system temp directory.

## Environment variables the binary reads

| Variable | Where | Effect |
|---|---|---|
| `DANBOORU_LOGIN`, `DANBOORU_API_KEY` | `internal/api/client.go:135-136` | both non-empty: authenticated requests and the 500 ms gap tier; otherwise anonymous |
| `DANBOORU_REQ_GAP_MS` | `client.go:163` | overrides the request gap; `0` = no gap; invalid or negative: one warning, tier default kept |
| `DANBOORU_MCP_NO_BOOTSTRAP` | `internal/app/bootstrap.go:25` | skips the PATH registration |
| `DANBOORU_MCP_NO_CACHE` | `main.go:99` | disables the response cache (leaves with task `11-27-remove-response-cache`) |
| `LOCALAPPDATA` | `main.go:102` | cache directory only (same task) |

## Mirrors and tests

- `README.md:177-184` "CLI" block: four commands, no aliases, no `serve`.
- `usage()` has no test; package `main` has no test file.
- `install.ps1:16` and the NSIS header carry their own usage comments for
  script maintainers; they are unrelated to the exe output.

## Why the Install section is useless

The text is only reachable by running the installed binary. It restates a
subset of the README's install options and drifts on its own.
