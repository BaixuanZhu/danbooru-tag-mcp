// Package tools registers the MCP tools exposed by the server and adapts
// their arguments/results to the service layer.
package tools

import (
	"context"
	"encoding/json"

	"danbooru-tag-mcp/internal/service"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type TagService interface {
	Search(ctx context.Context, query string, limit int) ([]service.Tag, error)
	Info(ctx context.Context, name string) (*service.Tag, error)
	Related(ctx context.Context, tag string, limit int) ([]service.RelatedTag, error)
	Alias(ctx context.Context, name string) (*service.TagAlias, error)
	Wiki(ctx context.Context, title, otherNames string, limit int) ([]service.WikiPage, error)
	SearchPosts(ctx context.Context, tags string, limit int) ([]service.Post, error)
}

func errResp(code, message string) string {
	b, _ := json.Marshal(map[string]any{
		"error":   code,
		"message": message,
	})
	return string(b)
}

func jsonResult(data any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func getStringArg(args map[string]any, key string) string {
	if val, ok := args[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func getIntArg(args map[string]any, key string, defaultVal int) int {
	if val, ok := args[key]; ok {
		switch v := val.(type) {
		case float64:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		}
	}
	return defaultVal
}

// Exported tool instances so register_test.go can statically validate their schemas
var (
	SearchTagsTool = mcp.NewTool("search_tags",
		mcp.WithDescription("Search Danbooru tags by keyword, ordered by post count"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("keyword, e.g. 'blue hair'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max number of tags to return (default: 10)"),
		),
	)

	GetTagInfoTool = mcp.NewTool("get_tag_info",
		mcp.WithDescription("Get exact info for a Danbooru tag"),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("exact tag name, e.g. 'blue_hair'"),
		),
	)

	GetRelatedTagsTool = mcp.NewTool("get_related_tags",
		mcp.WithDescription("Get co-occurring tags for a given tag"),
		mcp.WithString("tag",
			mcp.Required(),
			mcp.Description("tag to find co-occurring tags for"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max related tags to return (default: 10)"),
		),
	)

	GetTagAliasTool = mcp.NewTool("get_tag_alias",
		mcp.WithDescription("Resolve a tag alias, abbreviation or common misspelling to its canonical Danbooru tag. Returns alias: null when the input has no active alias, meaning it is likely already canonical; use the consequent tag in prompts when an alias is returned."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("alias or candidate tag, e.g. 'sailor_suit'"),
		),
	)

	GetTagWikiTool = mcp.NewTool("get_tag_wiki",
		mcp.WithDescription("Get the Danbooru wiki page of a tag: description, DText body, multilingual other_names, and linked_tags (the [[tag]] links extracted from the body, typically the character's appearance traits). Pass either title (exact canonical tag) or other_names (substring match on multilingual aliases, e.g. a Chinese or Japanese character name)."),
		mcp.WithString("title",
			mcp.Description("exact wiki title, i.e. the canonical tag, e.g. 'firefly_(honkai:_star_rail)'"),
		),
		mcp.WithString("other_names",
			mcp.Description("multilingual alias substring, e.g. '流萤' or 'ホタル'; matched with wildcards"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max pages to return when searching by other_names (default: 5)"),
		),
	)

	SearchPostsTool = mcp.NewTool("search_posts",
		mcp.WithDescription("Search posts by tag combination. Defaults to rating:explicit (R-18); pass your own rating metatag (rating:g / rating:s / rating:q / rating:e) in tags to override. Ratings: g=General (all-ages SFW), s=Sensitive (swimwear/underwear, borderline), q=Questionable (suggestive nudity), e=Explicit (R-18). At most 2 tags per query for free accounts, counting content tags and order: metatags (rating: and other metatags do not count); more is rejected with an error."),
		mcp.WithString("tags",
			mcp.Required(),
			mcp.Description("space-separated tags; a rating:g/s/q/e metatag overrides the default rating:explicit"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max posts to return (default: 5)"),
		),
	)
)

func Register(s *server.MCPServer, svc TagService) {
	// tool 1: search_tags
	s.AddTool(SearchTagsTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		query := getStringArg(args, "query")
		if query == "" {
			return mcp.NewToolResultText(errResp("search_failed", "query parameter is required")), nil
		}
		limit := getIntArg(args, "limit", 10)

		tags, err := svc.Search(ctx, query, limit)
		if err != nil {
			return mcp.NewToolResultText(errResp("search_failed", err.Error())), nil
		}
		return jsonResult(map[string]any{"tags": tags})
	})

	// tool 2: get_tag_info
	s.AddTool(GetTagInfoTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		name := getStringArg(args, "name")
		if name == "" {
			return mcp.NewToolResultText(errResp("tag_not_found", "name parameter is required")), nil
		}

		tag, err := svc.Info(ctx, name)
		if err != nil {
			return mcp.NewToolResultText(errResp("tag_not_found", err.Error())), nil
		}
		return jsonResult(tag)
	})

	// tool 3: get_related_tags
	s.AddTool(GetRelatedTagsTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		tag := getStringArg(args, "tag")
		if tag == "" {
			return mcp.NewToolResultText(errResp("related_failed", "tag parameter is required")), nil
		}
		limit := getIntArg(args, "limit", 10)

		related, err := svc.Related(ctx, tag, limit)
		if err != nil {
			return mcp.NewToolResultText(errResp("related_failed", err.Error())), nil
		}
		return jsonResult(map[string]any{"related": related})
	})

	// tool 4: get_tag_alias
	s.AddTool(GetTagAliasTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		name := getStringArg(args, "name")
		if name == "" {
			return mcp.NewToolResultText(errResp("alias_failed", "name parameter is required")), nil
		}

		alias, err := svc.Alias(ctx, name)
		if err != nil {
			return mcp.NewToolResultText(errResp("alias_failed", err.Error())), nil
		}
		return jsonResult(map[string]any{"alias": alias})
	})

	// tool 5: get_tag_wiki
	s.AddTool(GetTagWikiTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		title := getStringArg(args, "title")
		otherNames := getStringArg(args, "other_names")
		if title == "" && otherNames == "" {
			return mcp.NewToolResultText(errResp("wiki_failed", "title or other_names parameter is required")), nil
		}
		limit := getIntArg(args, "limit", 5)

		pages, err := svc.Wiki(ctx, title, otherNames, limit)
		if err != nil {
			return mcp.NewToolResultText(errResp("wiki_failed", err.Error())), nil
		}
		return jsonResult(map[string]any{"wiki_pages": pages})
	})

	// tool 6: search_posts
	s.AddTool(SearchPostsTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		tags := getStringArg(args, "tags")
		if tags == "" {
			return mcp.NewToolResultText(errResp("post_search_failed", "tags parameter is required")), nil
		}
		limit := getIntArg(args, "limit", 5)

		posts, err := svc.SearchPosts(ctx, tags, limit)
		if err != nil {
			return mcp.NewToolResultText(errResp("post_search_failed", err.Error())), nil
		}
		return jsonResult(map[string]any{"posts": posts})
	})
}
