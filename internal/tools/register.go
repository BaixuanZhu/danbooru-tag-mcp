// Package tools registers the MCP tools exposed by the server and adapts
// their arguments/results to the service layer.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"danbooru-tag-mcp/internal/cache"
	"danbooru-tag-mcp/internal/service"
	"danbooru-tag-mcp/internal/vocab"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type TagService interface {
	Search(ctx context.Context, query string, limit, category, minPosts int) (*service.SearchResult, error)
	Info(ctx context.Context, name string) (*service.TagInfo, error)
	Infos(ctx context.Context, names []string) (*service.TagsInfo, error)
	WD14Info(name string) service.WD14Status
	TaggerVocabHits(query string, limit int) []vocab.Hit
	Related(ctx context.Context, tag string, limit int) (*service.RelatedResult, error)
	Alias(ctx context.Context, name string) (*service.AliasResult, error)
	Wiki(ctx context.Context, title, otherNames string, limit int, includeDescription bool) (*service.WikiResult, error)
	SearchPosts(ctx context.Context, tags string, limit int, tagsMode string) ([]service.Post, error)
	Profile(ctx context.Context, tag string, sample int, opts service.ProfileOptions) (*service.TagProfile, error)
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

// getBoolArg extracts a boolean argument; anything absent or non-boolean is
// false, so the option stays opt-in.
func getBoolArg(args map[string]any, key string) bool {
	if val, ok := args[key]; ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return false
}

// withRefresh wraps ctx with the cache refresh request when the call opted
// in: the api layer then bypasses the fresh-hit shortcut and re-fetches
// live. Absent/false returns ctx unchanged (default cache-on behavior).
func withRefresh(ctx context.Context, args map[string]any) context.Context {
	if getBoolArg(args, "refresh") {
		return cache.WithRefresh(ctx)
	}
	return ctx
}

// getStringSliceArg extracts a string-array argument. MCP clients deliver
// JSON arrays as []any; non-string entries are skipped.
func getStringSliceArg(args map[string]any, key string) []string {
	val, ok := args[key]
	if !ok {
		return nil
	}
	arr, ok := val.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Exported tool instances so register_test.go can statically validate their schemas
var (
	SearchTagsTool = mcp.NewTool("search_tags",
		mcp.WithDescription("Search Danbooru tags by keyword, ordered by post count. When no result has posts, adds wd14_hits (WD14 tagger vocab names containing the query). category filters by general|artist|copyright|character|meta. min_posts filters out tags below the threshold (may return fewer than limit). Serves expired cached data marked stale+cached_at when Danbooru is unreachable; refresh:true re-fetches live and updates the cache"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("keyword, e.g. 'blue hair'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max number of tags to return (default: 10)"),
		),
		mcp.WithString("category",
			mcp.Description("filter by category: general|artist|copyright|character|meta"),
		),
		mcp.WithNumber("min_posts",
			mcp.Description("drop tags with fewer posts (default: 0 keeps all)"),
		),
		mcp.WithBoolean("refresh",
			mcp.Description("bypass the cache and re-fetch live"),
		),
	)

	GetTagInfoTool = mcp.NewTool("get_tag_info",
		mcp.WithDescription("Get exact info for a Danbooru tag. wd14 field: live (current Danbooru), tagger_era (WD14 tagger vocab only, likely renamed), unknown (neither). Adds renamed_to when a 0-post name has an active alias. Serves expired cached data marked stale+cached_at when Danbooru is unreachable; refresh:true re-fetches live and updates the cache"),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("exact tag name, e.g. 'blue_hair'"),
		),
		mcp.WithBoolean("refresh",
			mcp.Description("bypass the cache and re-fetch live"),
		),
	)

	GetRelatedTagsTool = mcp.NewTool("get_related_tags",
		mcp.WithDescription("Get co-occurring tags for a given tag (meta-category tags excluded). Serves expired cached data marked stale+cached_at when Danbooru is unreachable; refresh:true re-fetches live and updates the cache"),
		mcp.WithString("tag",
			mcp.Required(),
			mcp.Description("tag to find co-occurring tags for"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max related tags to return (default: 10)"),
		),
		mcp.WithBoolean("refresh",
			mcp.Description("bypass the cache and re-fetch live"),
		),
	)

	GetTagAliasTool = mcp.NewTool("get_tag_alias",
		mcp.WithDescription("Resolve a tag alias or misspelling to its canonical Danbooru tag; null means the input is already canonical. Serves expired cached data marked stale+cached_at when Danbooru is unreachable; refresh:true re-fetches live and updates the cache"),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("alias or candidate tag, e.g. 'sailor_suit'"),
		),
		mcp.WithBoolean("refresh",
			mcp.Description("bypass the cache and re-fetch live"),
		),
	)

	GetTagWikiTool = mcp.NewTool("get_tag_wiki",
		mcp.WithDescription("Get a tag's wiki page as structured fields: summary, intro_tags, rules, see_also, example_posts, linked_tags, other_names. include_description also returns the intro prose. Pass title (exact canonical tag) or other_names (multilingual alias substring). Serves expired cached data marked stale+cached_at when Danbooru is unreachable; refresh:true re-fetches live and updates the cache"),
		mcp.WithString("title",
			mcp.Description("exact wiki title, i.e. the canonical tag, e.g. 'firefly_(honkai:_star_rail)'"),
		),
		mcp.WithString("other_names",
			mcp.Description("multilingual alias substring, e.g. '流萤' or 'ホタル'; matched with wildcards"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max pages to return when searching by other_names (default: 5)"),
		),
		mcp.WithBoolean("include_description",
			mcp.Description("also return the intro prose as plain text (character appearance descriptions)"),
		),
		mcp.WithBoolean("refresh",
			mcp.Description("bypass the cache and re-fetch live"),
		),
	)

	SearchPostsTool = mcp.NewTool("search_posts",
		mcp.WithDescription("Search posts by tags; no rating filter is applied, add a rating:g/s/q/e metatag to narrow (g=all-ages, s=swimwear/borderline, q=suggestive, e=R-18). Max 2 content tags per query (order: counts; other metatags don't). Each post carries score, fav_count, large_file_url, and its tag list split by category (copyright/artist/character/general/meta). tags_mode=none drops tag lists; diff keeps only tags not already in the query"),
		mcp.WithString("tags",
			mcp.Required(),
			mcp.Description("space-separated tags"),
		),
		mcp.WithNumber("limit",
			mcp.Description("max posts to return (default: 5, max: 20)"),
		),
		mcp.WithString("tags_mode",
			mcp.Enum("full", "none", "diff"),
			mcp.Description("tag-list mode: full (default), none (drop tag lists), diff (drop tags already in the query)"),
		),
	)

	GetTagProfileTool = mcp.NewTool("get_tag_profile",
		mcp.WithDescription("Distribution profile of one tag from a post sample (default 200): co_tags by in-sample frequency, ubiquitous (freq >= 0.5), top_pairs by lift, ratings mix. order: random (default), score, favcount (top-post biased). rating: g/s/q/e filter. with: second tag; profiles the 200 most recent joint posts instead"),
		mcp.WithString("tag",
			mcp.Required(),
			mcp.Description("exact tag name, e.g. 'arm_up'"),
		),
		mcp.WithNumber("sample",
			mcp.Description("posts to sample (default: 200, range 20-200)"),
		),
		mcp.WithString("with",
			mcp.Description("second exact tag; profiles the 200 most recent joint posts (order forced to recent)"),
		),
		mcp.WithString("rating",
			mcp.Description("filter sampled posts by rating: g|s|q|e"),
		),
		mcp.WithString("order",
			mcp.Description("single-tag sample ordering: random (default), score, favcount (top-post biased); not allowed with with"),
		),
	)

	GetTagsInfoTool = mcp.NewTool("get_tags_info",
		mcp.WithDescription("Batch exact info for up to 20 Danbooru tag names in one request (cheaper than repeated get_tag_info calls). 0-post rows also resolve renamed_to (one extra request each). Returns found tags (name, category, post_count, wd14 verdict: live/tagger_era/unknown) and missing names with their wd14 verdict. Serves expired cached data marked stale+cached_at when Danbooru is unreachable; refresh:true re-fetches live and updates the cache"),
		mcp.WithArray("names",
			mcp.Required(),
			mcp.MaxItems(20),
			mcp.Items(map[string]any{"type": "string"}),
			mcp.Description("exact tag names, e.g. ['blue_hair','1girl']"),
		),
		mcp.WithBoolean("refresh",
			mcp.Description("bypass the cache and re-fetch live"),
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
		category := -1
		if cat := getStringArg(args, "category"); cat != "" {
			id, ok := service.CategoryID(cat)
			if !ok {
				return mcp.NewToolResultText(errResp("search_failed", fmt.Sprintf("unknown category %q: use general, artist, copyright, character or meta", cat))), nil
			}
			category = id
		}
		ctx = withRefresh(ctx, args)

		res, err := svc.Search(ctx, query, limit, category, getIntArg(args, "min_posts", 0))
		if err != nil {
			return mcp.NewToolResultText(errResp("search_failed", err.Error())), nil
		}
		if !hasLiveTag(res.Tags) {
			// Dead end made actionable: nearby WD14 tagger-vocab names.
			if hits := svc.TaggerVocabHits(query, wd14HitsLimit); len(hits) > 0 {
				res.WD14Hits = hits
			}
		}
		return jsonResult(res)
	})

	// tool 2: get_tag_info
	s.AddTool(GetTagInfoTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		name := getStringArg(args, "name")
		if name == "" {
			return mcp.NewToolResultText(errResp("tag_not_found", "name parameter is required")), nil
		}
		ctx = withRefresh(ctx, args)

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
		ctx = withRefresh(ctx, args)

		res, err := svc.Related(ctx, tag, limit)
		if err != nil {
			return mcp.NewToolResultText(errResp("related_failed", err.Error())), nil
		}
		return jsonResult(res)
	})

	// tool 4: get_tag_alias
	s.AddTool(GetTagAliasTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		name := getStringArg(args, "name")
		if name == "" {
			return mcp.NewToolResultText(errResp("alias_failed", "name parameter is required")), nil
		}
		ctx = withRefresh(ctx, args)

		res, err := svc.Alias(ctx, name)
		if err != nil {
			return mcp.NewToolResultText(errResp("alias_failed", err.Error())), nil
		}
		return jsonResult(res)
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
		ctx = withRefresh(ctx, args)

		res, err := svc.Wiki(ctx, title, otherNames, limit, getBoolArg(args, "include_description"))
		if err != nil {
			return mcp.NewToolResultText(errResp("wiki_failed", err.Error())), nil
		}
		return jsonResult(res)
	})

	// tool 6: search_posts
	s.AddTool(SearchPostsTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		tags := getStringArg(args, "tags")
		if tags == "" {
			return mcp.NewToolResultText(errResp("post_search_failed", "tags parameter is required")), nil
		}
		limit := getIntArg(args, "limit", 5)

		posts, err := svc.SearchPosts(ctx, tags, limit, getStringArg(args, "tags_mode"))
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
		opts := service.ProfileOptions{
			With:   getStringArg(args, "with"),
			Rating: getStringArg(args, "rating"),
			Order:  getStringArg(args, "order"),
		}

		profile, err := svc.Profile(ctx, tag, sample, opts)
		if err != nil {
			var nf *service.NotFoundError
			if errors.As(err, &nf) {
				// Same story as get_tag_info: the local WD14 verdict rides
				// the not-found error so renamed names stay actionable. The
				// verdict is computed for the actually missing name, which in
				// pair mode may be the with tag rather than the primary.
				b, _ := json.Marshal(map[string]any{
					"error":   "tag_not_found",
					"message": nf.Error(),
					"wd14":    svc.WD14Info(nf.Name),
				})
				return mcp.NewToolResultText(string(b)), nil
			}
			return mcp.NewToolResultText(errResp("profile_failed", err.Error())), nil
		}
		return jsonResult(profile)
	})

	// tool 8: get_tags_info
	s.AddTool(GetTagsInfoTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		names := getStringSliceArg(args, "names")
		if len(names) == 0 {
			return mcp.NewToolResultText(errResp("tags_info_failed", "names parameter is required (array of tag names)")), nil
		}
		ctx = withRefresh(ctx, args)

		info, err := svc.Infos(ctx, names)
		if err != nil {
			return mcp.NewToolResultText(errResp("tags_info_failed", err.Error())), nil
		}
		return jsonResult(info)
	})
}
