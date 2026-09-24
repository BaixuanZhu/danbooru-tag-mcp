# danbooru-tag-mcp

A Danbooru tag lookup [MCP](https://modelcontextprotocol.io) (Model Context
Protocol) server for local AI image generation. It exposes four lookup tools
over stdio so your AI client can resolve real Danbooru tags — exact names,
aliases, categories, post counts and co-occurrence statistics — before
composing prompts, which noticeably improves tag accuracy in generated images.

## Features

- **4 MCP tools**: tag search, exact tag info, related (co-occurring) tags, and post search
- **SFW by default**: `search_posts` always appends `rating:general`, so image results stay general-rated
- **Self-updating**: `danbooru-tag-mcp upgrade` checks GitHub Releases, verifies SHA256 and replaces the binary atomically
- **Zero-config install**: per-user installer (setup wizard or one-line PowerShell) that registers the install directory into the user PATH automatically
- **Polite API usage**: requests are throttled to one per 1.5 seconds; anonymous access works, credentials raise the limits

## Install

Requires Windows 10/11 on x64 or ARM64.

### Option 1: setup wizard (recommended)

Download `danbooru-tag-mcp-windows-<arch>-setup.exe` from the
[latest release](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases/latest)
and double-click it. This installs per-user to
`%LOCALAPPDATA%\Programs\danbooru-tag-mcp` (no admin rights needed) and
registers the user PATH.

### Option 2: one-line PowerShell

```powershell
iwr -useb "https://raw.githubusercontent.com/BaixuanZhu/danbooru-tag-mcp/main/install.ps1" | iex
```

Custom install directory:

```powershell
.\install.ps1 -InstallDir "D:\tools\danbooru-tag-mcp"
```

### Option 3: portable zip

Download `danbooru-tag-mcp-windows-<arch>.zip` from the
[release page](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases),
extract `danbooru-tag-mcp.exe` anywhere, and run it once — the first run
silently adds its own directory to the user PATH (idempotent).

## Configure your MCP client

Point your MCP client (Claude Desktop, ZCode, etc.) at the command:

```json
{
  "mcpServers": {
    "danbooru-tags": {
      "command": "danbooru-tag-mcp",
      "args": []
    }
  }
}
```

The bare command name works because the install directory is on the user PATH;
alternatively use the full exe path.

## Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `search_tags` | Search tags by keyword, ordered by post count | `query` (required, e.g. `blue hair`), `limit` (default 10) |
| `get_tag_info` | Exact info for one tag (aliases, category, counts) | `name` (required, e.g. `blue_hair`) |
| `get_related_tags` | Tags that co-occur with the given tag | `tag` (required), `limit` (default 10) |
| `search_posts` | Search posts by a tag combination (general rating only) | `tags` (required, space-separated), `limit` (default 5) |

## CLI

```
danbooru-tag-mcp            run the MCP server (what MCP clients launch)
danbooru-tag-mcp upgrade    self-update via GitHub Release (SHA256 verified)
danbooru-tag-mcp version    print the version
danbooru-tag-mcp help       show help
```

## Environment variables

| Variable | Purpose |
|----------|---------|
| `DANBOORU_LOGIN` / `DANBOORU_API_KEY` | Danbooru API credentials (optional; without them the client is anonymous and Danbooru applies lower rate/content limits) |
| `DANBOORU_MCP_NO_BOOTSTRAP` | Skip the startup PATH registration when non-empty |

## Building from source

Requires Go 1.26+ and GNU Make (bundled with Git Bash):

```bash
make test        # unit tests
make build       # dev build -> dist/<arch>/ (bootstrap disabled)
make release     # portable zip + checksums.txt
make installer   # NSIS setup exe (requires makensis: scoop install nsis)
```

Releases are automated: pushing a `v*` tag makes GitHub Actions run the test
suite and publish the setup exes, portable zips, and `checksums.txt` for both
architectures.
