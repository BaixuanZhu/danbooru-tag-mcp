# danbooru-tag-mcp

[English](README.md) | **简体中文**

[![CI](https://github.com/BaixuanZhu/danbooru-tag-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/BaixuanZhu/danbooru-tag-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BaixuanZhu/danbooru-tag-mcp)](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases/latest)
[![Platform](https://img.shields.io/badge/platform-Windows%2010%2F11-0078D4?logo=windows&logoColor=white)](#安装)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/github/license/BaixuanZhu/danbooru-tag-mcp)](LICENSE)

面向本地 AI 出图的 Danbooru 标签查询 [MCP](https://modelcontextprotocol.io) 服务器。客户端在写提示词之前先去查，而不是凭记忆拼：准确名称、别名、分类、帖子数、共现统计与 wiki 规则，查完再落笔。

支持 Windows 10/11 的 x64 与 ARM64。匿名即可访问，第一次查询之前不需要申请任何 API key。

## 快速开始

按当前用户安装（Windows 10/11，不需要管理员权限）：

```powershell
iwr -useb "https://raw.githubusercontent.com/BaixuanZhu/danbooru-tag-mcp/main/install.ps1" | iex
```

让 MCP 客户端指向这条命令：

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

就这些。客户端下次启动会话时会自动加载下面这八个工具。

## 客户端拿到什么

以下为真实返回，为篇幅做了删减。帖子数每天变动，这一组抓取于 2026-11-12。

`search_tags` 搜 *blue hair*，返回真实存在的名称，模型不用再猜 `bluehair`：

```json
{"tags": [
  {"id": 10953, "name": "blue_hair", "category": 0, "post_count": 1234156},
  {"id": 545704, "name": "blue_hairband", "category": 0, "post_count": 33332},
  {"id": 1564040, "name": "blue_hair-chan_(ramchi)", "category": 4, "post_count": 120}
]}
```

`get_tag_info` 查 `blue_hair`，给出帖子数，以及 WD14 tagger 是否认识这个名字：

```json
{"id": 10953, "name": "blue_hair", "category": 0, "post_count": 1234156,
 "wd14": {"status": "live", "sources": [
   {"vocab": "wd-eva02-large-tagger-v3", "count": 704952},
   {"vocab": "wd-v1-4-moat-tagger-v2", "count": 564360}]}}
```

`get_tag_profile` 看 `arm_up`，全年龄样本 200 帖，回答这个标签实际会画出什么：按样本内频率排序的共现标签，以及哪些组合是它特有而非仅仅常见。

```json
{"name": "arm_up", "post_count": 314588, "rating": "g", "sample": 200,
 "ratings": {"g": 1},
 "ubiquitous": [
   {"tag": "smile", "category": 0, "freq": 0.51},
   {"tag": "solo", "category": 0, "freq": 0.51}],
 "co_tags": [
   {"tag": "looking_at_viewer", "category": 0, "freq": 0.5},
   {"tag": "long_sleeves", "category": 0, "freq": 0.48},
   {"tag": "1girl", "category": 0, "freq": 0.46},
   {"tag": "open_mouth", "category": 0, "freq": 0.45}],
 "top_pairs": [
   {"pair": "pokemon + pokemon_(creature)", "freq": 0.05, "lift": 16.7},
   {"pair": "japanese_clothes + kimono", "freq": 0.05, "lift": 15.4}]}
```

完整画像返回 30 个共现标签与 10 组排名组合。每个字段的含义见[工具](#工具)一节。

## 功能

- **8 个 MCP 工具** —— 标签搜索、精确查询与批量查询、别名解析、相关标签、wiki 页、帖子搜索、分布画像。
- **批量查询** —— 一次调用解析 20 个精确名称，原本要 20 次；结果拆成 found 与 missing，每个名称都带 WD14 判定。
- **分布画像** —— 一个标签实际会画出什么：按样本内频率排序的共现标签，`1girl`/`solo` 这类语料常量单独归入 `ubiquitous`，按 lift 排名的组合，以及样本的分级构成。传 `with` 则改为画像一对标签，回答 `arm_up` 配 `holding_weapon` 会画出什么。
- **结构化 wiki 页** —— 把 DText 正文拆成 `summary`、`intro_tags`、`rules`、`see_also`、`example_posts`，「if X, use Y instead」这类规则直接以数据形式给出，不用再解析散文。
- **Tagger 时代名称识别** —— 每个名称都与锁定版本的 WD14 词表比对，判定为 `live`、`tagger_era`（上游已改名，附规范名）或 `unknown`，LoRA 卡片的标注不会再卡在「tag not found」上。
- **帖子标签列表** —— 每篇帖子都带回按分类拆开的完整标签列表，翻几篇就能看出哪些标签天然一起出现。默认不做分级过滤，只要全年龄就加 `rating:g`。
- **装上就不用管** —— 落盘缓存，Danbooru 故障时继续用过期数据兜底；`upgrade` 校验 SHA256 后原子替换二进制；安装器按用户安装并注册 PATH；请求间隔 1.5 秒（有凭据时 500 毫秒），保证匿名访问也站得住。

返回的就是纯标签串，不绑定任何模型，可以直接粘进 ComfyUI、Stable Diffusion 或 Illustrious 的提示词框。

## 安装

需要 Windows 10/11，x64 或 ARM64。三种方式都按当前用户安装，不需要管理员权限，安装目录会写进用户 PATH，装完直接敲 `danbooru-tag-mcp` 就能用。

### 方式一：安装向导（推荐）

从[最新发布](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases/latest)下载 `danbooru-tag-mcp-windows-<arch>-setup.exe` 双击运行，装到 `%LOCALAPPDATA%\Programs\danbooru-tag-mcp`。

### 方式二：一行 PowerShell

```powershell
iwr -useb "https://raw.githubusercontent.com/BaixuanZhu/danbooru-tag-mcp/main/install.ps1" | iex
```

指定安装目录：

```powershell
.\install.ps1 -InstallDir "D:\tools\danbooru-tag-mcp"
```

### 方式三：便携 zip

从[发布页](https://github.com/BaixuanZhu/danbooru-tag-mcp/releases)下载 `danbooru-tag-mcp-windows-<arch>.zip`，把 `danbooru-tag-mcp.exe` 解压到任意位置，运行一次。首次运行会静默把自身目录加入用户 PATH，重复执行无副作用。

## 配置 MCP 客户端

让 MCP 客户端（Claude Desktop、ZCode 等）指向这条命令：

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

能直接写命令名，是因为安装目录已在用户 PATH 上；也可以填完整 exe 路径。

### 用插件代替手写 JSON（ZCode / Claude Code）

把这个仓库加为插件市场，安装 `danbooru-tag-mcp` 插件。`danbooru-tags` 的服务器定义随插件下发，不用手改 JSON。插件只负责接线：上面的安装步骤要先做完，确保 CLI 真的在 PATH 上。

- **ZCode**：插件市场 → 添加 → `BaixuanZhu/danbooru-tag-mcp` → 安装 `danbooru-tag-mcp` 插件。
- **Claude Code**：`/plugin install danbooru-tag-mcp --marketplace BaixuanZhu/danbooru-tag-mcp`（一步完成，需要 Claude Code 2.1.275+），或先用 `claude plugin marketplace add BaixuanZhu/danbooru-tag-mcp` 添加市场再从那里安装。

`danbooru-tag-mcp upgrade` 独立于插件更新 CLI。详见 [plugins/danbooru-tag-mcp/README.md](plugins/danbooru-tag-mcp/README.md)。

## 工具

| 工具 | 作用 |
|------|------|
| `search_tags` | 按关键词搜索标签，按帖子数排序 |
| `get_tag_info` | 单个标签的精确信息，附带该名称的 WD14 判定 |
| `get_tags_info` | 同上，一次最多 20 个名称 |
| `get_related_tags` | 与指定标签共现的标签 |
| `get_tag_alias` | 把别名或拼写错误解析到规范标签 |
| `get_tag_wiki` | 标签 wiki 页，输出为结构化字段 |
| `search_posts` | 按标签组合搜索帖子，带回标签列表 |
| `get_tag_profile` | 从帖子样本看一个标签实际会画出什么 |

<details>
<summary>八个工具的参数与返回字段</summary>

#### `search_tags`

`query`（必填，例如 `blue hair`）· `limit`（10）· `category`（`general`/`artist`/`copyright`/`character`/`meta`）· `min_posts`（0，低于阈值的标签会被丢弃）· `refresh`（false）

当没有任何结果带帖子时，返回里会多出 `wd14_hits`，即词表中包含该查询词的 WD14 tagger 名称。

#### `get_tag_info`

`name`（必填，例如 `blue_hair`）· `refresh`（false）

返回分类与帖子数，外加 `wd14` 判定：`live`（当前 Danbooru 上存在）、`tagger_era`（仅存在于 WD14 词表，多半已改名，规范名在 `renamed_to` 里）或 `unknown`（两边都没有）。0 帖且存在生效别名的名称，也会返回 `renamed_to`。

#### `get_tags_info`

`names`（必填，例如 `["blue_hair","1girl"]`，最多 20）· `refresh`（false）

返回 `found`（字段与 `get_tag_info` 相同，0 帖行同样带 `renamed_to`）与 `missing`（Danbooru 上不存在，但仍带各自的 `wd14` 判定）。

#### `get_related_tags`

`tag`（必填）· `limit`（10）· `refresh`（false）

`highres` 这类 meta 分类标签会作为噪声过滤掉。

#### `get_tag_alias`

`name`（必填，例如 `sailor_suit`）· `refresh`（false）

返回 `null` 表示输入本身已是规范标签。

#### `get_tag_wiki`

`title` 或 `other_names`（二选一必填）· `limit`（5）· `include_description`（false）· `refresh`（false）

返回 `summary`（首段）、`intro_tags`（定义段的链接目标）、`rules`（消歧与用法句子）、`see_also`、`example_posts`，以及原始 `linked_tags` 并集与多语言 `other_names`。原始 DText 正文不返回；`include_description` 会把引言散文作为纯文本附上，用于角色外观描述。

#### `search_posts`

`tags`（必填，空格分隔）· `limit`（5，最多 20）· `tags_mode`（默认 `full`；`none` 去掉每帖的标签列表，`diff` 只保留查询之外的标签）

默认不加分级过滤；要收窄就加 `rating:g/s/q/e` 元标签。每篇帖子带 `score`、`fav_count`、`large_file_url`，以及按分类拆开的 `tags`：`copyright` / `artist` / `character` / `general` / `meta`。

#### `get_tag_profile`

`tag`（必填，例如 `arm_up`）· `sample`（200，范围 20–200）· `with` · `rating`（`g`/`s`/`q`/`e`）· `order`（默认随机；`score` / `favcount` 切到热帖视角，存在偏差）

从该标签的帖子中采样一页，返回按样本内频率排序的 `co_tags`、语料常量 `ubiquitous`（`freq >= 0.5`）、按 lift 排名的 `top_pairs` 组合，以及 `ratings` 分级构成。随机采样覆盖整体分布；`rating` 过滤样本；`with` 改为画像一对标签最近 200 篇共同出现的帖子。

</details>

六个标签工具（`search_tags`、`get_tag_info`、`get_tags_info`、`get_related_tags`、`get_tag_alias`、`get_tag_wiki`）走[响应缓存](#响应缓存)：TTL 内重复调用跳过网络往返，Danbooru 故障时返回上一次缓存的答案而不是报错，并标记 `stale: true` 与 `cached_at`（缓存写入时间）。它们的 `refresh` 参数（默认 false）可对单次调用绕过缓存、重新拉取。帖子搜索与画像从不缓存，因此没有 `refresh`。

## 响应缓存

重复查询由落盘缓存作答，不再打 Danbooru：

| 数据 | TTL |
|------|-----|
| 相关标签（`get_related_tags`） | 30 天 |
| wiki 页、别名 | 7 天 |
| 标签搜索（`search_tags`） | 24 小时 |
| 精确标签查询（`get_tag_info`、`get_tags_info`） | 7 天 |
| 帖子（`search_posts`、`get_tag_profile`） | 从不缓存 |

- 位置：`%LOCALAPPDATA%\danbooru-tag-mcp\cache`，按用户隔离，重启后保留，上限 2000 条 / 64 MB，超限按最旧优先淘汰。里面只有 API 响应体，随时可以删除；卸载程序会一并清掉。
- 命中新鲜缓存时，返回与网络响应逐字节一致，不加任何标记。
- Danbooru 不可达（重试之后）时，返回过期的缓存答案而不是报错，并在受影响的工具响应上标记 `stale: true` 与 `cached_at`（缓存写入时间）。
- 空结果（`[]`）从不缓存，Danbooru 一收录新标签就能立刻查到。
- 精确标签查询会把标签的身份行保留 7 天（名称与分类几乎不变，和 wiki 页同理），因此 `post_count` 最多可能滞后线上 7 天。需要当前数值做判断时用 `refresh: true`。
- `refresh: true` 重新拉取，并重置该条目的 TTL 计时。
- 设 `DANBOORU_MCP_NO_CACHE=1` 可完全关闭缓存，排查问题时有用。

## 命令行

```
danbooru-tag-mcp            启动 MCP 服务器（MCP 客户端启动的就是它）
danbooru-tag-mcp upgrade    通过 GitHub Release 自更新（SHA256 校验）
danbooru-tag-mcp version    打印版本
danbooru-tag-mcp help       显示帮助
```

## 环境变量

- `DANBOORU_LOGIN` —— Danbooru 账号名，你登录站点用的那个。可选。
- `DANBOORU_API_KEY` —— Danbooru API key，在个人资料的 API key 区块生成。可选。两项都设置时，每个请求都以你的账号认证发出（更高的速率上限与内容访问权限）；否则客户端为匿名，Danbooru 施加更低的限额。
- `DANBOORU_REQ_GAP_MS` —— Danbooru 请求之间的最小间隔，毫秒。`0` 关闭间隔；非法值会回落到默认并给出警告。默认 1500（匿名），两项凭据都设置时为 500。
- `DANBOORU_MCP_NO_BOOTSTRAP` —— 非空时跳过启动时的 PATH 注册。
- `DANBOORU_MCP_NO_CACHE` —— 非空时关闭[响应缓存](#响应缓存)。

## 从源码构建

需要 Go 1.26+ 与 GNU Make（Git Bash 自带）：

```bash
make test        # 单元测试
make integration # 对线上 Danbooru API 的联网冒烟测试（约 1 分钟）
make build       # 开发构建 -> dist/<arch>/（已禁用 bootstrap）
make release     # 便携 zip + checksums.txt
make installer   # NSIS 安装 exe（需要 makensis：scoop install nsis）
```

发布是自动化的：推送 `v*` 标签后，GitHub Actions 会跑测试，并为两种架构发布 setup exe、便携 zip 与 `checksums.txt`。

## 校验下载

二进制没有 Authenticode 签名，完整性依赖可复现构建、SHA-256 校验与构建溯源证明。

```bash
sha256sum -c --ignore-missing checksums.txt   # 对照清单校验完整性
gh attestation verify --repo BaixuanZhu/danbooru-tag-mcp danbooru-tag-mcp.exe   # 校验来源证明
```

[构建如何保持字节级可复现，以及如何自行复现一个（英文）→](docs/reproducible-builds.md)
