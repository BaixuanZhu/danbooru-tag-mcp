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

	SearchPostsTool = mcp.NewTool("search_posts",
		mcp.WithDescription("Search posts by tag combination (safe content only)"),
		mcp.WithString("tags",
			mcp.Required(),
			mcp.Description("space-separated tags"),
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

	// tool 4: search_posts
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
