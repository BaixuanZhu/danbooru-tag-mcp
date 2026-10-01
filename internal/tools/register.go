// Package tools registers the MCP tools exposed by the server and adapts
// their arguments/results to the service layer.
package tools

import (
	"context"
	"encoding/json"
	"errors"

	"danbooru-tag-mcp/internal/service"
	"danbooru-tag-mcp/internal/vocab"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type TagService interface {
	Search(ctx context.Context, query string, limit int) ([]service.Tag, error)
	Info(ctx context.Context, name string) (*service.TagInfo, error)
	WD14Info(name string) service.WD14Status
	TaggerVocabHits(query string, limit int) []vocab.Hit
	Related(ctx context.Context, tag string, limit int) ([]service.RelatedTag, error)
	Alias(ctx context.Context, name string) (*service.TagAlias, error)
	Wiki(ctx context.Context, title, otherNames string, limit int) ([]service.WikiPage, error)
	SearchPosts(ctx context.Context, tags string, limit int) ([]service.Post, error)
	Profile(ctx context.Context, tag string, sample int) (*service.TagProfile, error)
}

// wd14HitsLimit caps the search_tags fallback: enough nearby candidates to
// act on, few enough not to bloat the empty-result payload.
const wd14HitsLimit = 8

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
		mcp.WithDescription("Search Danbooru tags by keyword, ordered by post count. When no result has posts, adds wd14_hits (WD14 tagger vocab names containing the query)"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("keyword, e.g. 'blue hair'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max number of tags to return (default: 10)"),
		),
	)

	GetTagInfoTool = mcp.NewTool("get_tag_info",
		mcp.WithDescription("Get exact info for a Danbooru tag. wd14 field: live (current Danbooru), tagger_era (WD14 tagger vocab only, likely renamed), unknown (neither)"),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("exact tag name, e.g. 'blue_hair'"),
		),
	)

	GetRelatedTagsTool = mcp.NewTool("get_related_tags",
		mcp.WithDescription("Get co-occurring tags for a given tag (meta-category tags excluded)"),
		mcp.WithString("tag",
			mcp.Required(),
			mcp.Description("tag to find co-occurring tags for"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max related tags to return (default: 10)"),
		),
	)

	GetTagAliasTool = mcp.NewTool("get_tag_alias",
		mcp.WithDescription("Resolve a tag alias or misspelling to its canonical Danbooru tag; null means the input is already canonical"),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("alias or candidate tag, e.g. 'sailor_suit'"),
		),
	)

	GetTagWikiTool = mcp.NewTool("get_tag_wiki",
		mcp.WithDescription("Get a tag's wiki page: body, multilingual other_names, and linked_tags (appearance traits). Pass title (exact canonical tag) or other_names (multilingual alias substring)"),
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
		mcp.WithDescription("Search posts by tags. Defaults to rating:explicit (R-18); a rating:g/s/q/e metatag in tags overrides it (g=all-ages, s=swimwear/borderline, q=suggestive, e=R-18). Max 2 content tags per query (order: counts; other metatags don't). Each post carries its tag list split by category (copyright/artist/character/general/meta)"),
		mcp.WithString("tags",
			mcp.Required(),
			mcp.Description("space-separated tags"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max posts to return (default: 5)"),
		),
	)

	GetTagProfileTool = mcp.NewTool("get_tag_profile",
		mcp.WithDescription("Distribution profile of one tag from a random post sample (default 200): co_tags ordered by in-sample frequency (meta tags and the tag itself excluded), ubiquitous lists corpus-constant tags (freq >= 0.5), top_pairs characteristic combinations ranked by lift (co-occurrence above chance). Shows what a big tag actually renders as"),
		mcp.WithString("tag",
			mcp.Required(),
			mcp.Description("exact tag name, e.g. 'arm_up'"),
		),
		mcp.WithNumber("sample",
			mcp.Description("posts to sample (default: 200, range 20-200)"),
		),
	)
)

// hasLiveTag reports whether any search result carries posts. Danbooru keeps
// 0-post placeholder rows (renamed-away antecedents, unused tags), so a
// result list without a single live tag is the real dead end.
func hasLiveTag(tags []service.Tag) bool {
	for _, t := range tags {
		if t.PostCount > 0 {
			return true
		}
	}
	return false
}

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
		payload := map[string]any{"tags": tags}
		if !hasLiveTag(tags) {
			// Dead end made actionable: nearby WD14 tagger-vocab names.
			if hits := svc.TaggerVocabHits(query, wd14HitsLimit); len(hits) > 0 {
				payload["wd14_hits"] = hits
			}
		}
		return jsonResult(payload)
	})

	// tool 2: get_tag_info
	s.AddTool(GetTagInfoTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		name := getStringArg(args, "name")
		if name == "" {
			return mcp.NewToolResultText(errResp("tag_not_found", "name parameter is required")), nil
		}

		info, err := svc.Info(ctx, name)
		if err != nil {
			var nf *service.NotFoundError
			if errors.As(err, &nf) {
				// Deliver the local WD14 verdict with the not-found error,
				// so a tagger-era name stays actionable instead of dead-ending.
				b, _ := json.Marshal(map[string]any{
					"error":   "tag_not_found",
					"message": nf.Error(),
					"wd14":    svc.WD14Info(name),
				})
				return mcp.NewToolResultText(string(b)), nil
			}
			return mcp.NewToolResultText(errResp("tag_not_found", err.Error())), nil
		}
		return jsonResult(info)
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

	// tool 7: get_tag_profile
	s.AddTool(GetTagProfileTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		tag := getStringArg(args, "tag")
		if tag == "" {
			return mcp.NewToolResultText(errResp("profile_failed", "tag parameter is required")), nil
		}
		sample := getIntArg(args, "sample", 200)

		profile, err := svc.Profile(ctx, tag, sample)
		if err != nil {
			var nf *service.NotFoundError
			if errors.As(err, &nf) {
				// Same story as get_tag_info: the local WD14 verdict rides
				// the not-found error so renamed names stay actionable.
				b, _ := json.Marshal(map[string]any{
					"error":   "tag_not_found",
					"message": nf.Error(),
					"wd14":    svc.WD14Info(tag),
				})
				return mcp.NewToolResultText(string(b)), nil
			}
			return mcp.NewToolResultText(errResp("profile_failed", err.Error())), nil
		}
		return jsonResult(profile)
	})
}
