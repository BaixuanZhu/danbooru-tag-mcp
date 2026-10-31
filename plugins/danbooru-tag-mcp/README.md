# danbooru-tag-mcp (ZCode / Claude Code plugin)

Installs the `danbooru-tags` MCP server into ZCode or Claude Code with zero
manual client configuration: the plugin ships the MCP server definition
(`.mcp.json`), so enabling the plugin is all it takes.

## Prerequisite: the CLI must be on PATH

This plugin wires the configuration only — it does not download or carry the
binary. Install the `danbooru-tag-mcp` CLI first (Windows 10/11 x64 / ARM64)
via any route from the
[project README](https://github.com/BaixuanZhu/danbooru-tag-mcp#install)
(setup wizard, one-line PowerShell, or portable zip), then verify in a new
terminal:

```powershell
danbooru-tag-mcp version
```

The MCP server launches the bare command `danbooru-tag-mcp`, resolved through
your PATH. Keep the CLI updated with `danbooru-tag-mcp upgrade`; the plugin
never needs to change for CLI updates.

## Install

After the CLI is on PATH, install the plugin from this repository's
marketplace:

- **Claude Code**: `/plugin install danbooru-tag-mcp --marketplace BaixuanZhu/danbooru-tag-mcp`
  (or `claude plugin marketplace add BaixuanZhu/danbooru-tag-mcp` first, then
  install).
- **ZCode**: Plugin Marketplace → Add → `BaixuanZhu/danbooru-tag-mcp` →
  install the `danbooru-tag-mcp` plugin.

## What you get

The 8 Danbooru tag lookup tools (`search_tags`, `get_tag_info`,
`get_tags_info`, `get_related_tags`, `get_tag_alias`, `get_tag_wiki`,
`search_posts`, `get_tag_profile`) — see the
[tool reference](https://github.com/BaixuanZhu/danbooru-tag-mcp#tools)
for parameters and output shapes.

## Credentials (optional)

Anonymous access works out of the box. To authenticate (higher rate limits),
set `DANBOORU_LOGIN` and `DANBOORU_API_KEY` as **user-level environment
variables** (`setx` or System Properties), then restart the client — the
server inherits them from the environment.

Do not add these variables as empty placeholders to the MCP server's `env`
block: an explicitly declared (even empty) value overrides the inherited
environment, silently downgrading an authenticated setup to anonymous.

## Platform

Windows only — the CLI builds and the installer channels are Windows x64 /
ARM64.
