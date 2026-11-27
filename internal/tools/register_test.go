package tools

import (
	"context"
	"encoding/json"
	"testing"

	"danbooru-tag-mcp/internal/app"
	"danbooru-tag-mcp/internal/service"
	"danbooru-tag-mcp/internal/vocab"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestErrResp_Format(t *testing.T) {
	raw := errResp("tag_not_found", "tag 'sample' does not exist")

	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("failed to parse errResp JSON: %v", err)
	}

	if parsed["error"] != "tag_not_found" {
		t.Errorf("expected error field 'tag_not_found', got '%s'", parsed["error"])
	}
	if parsed["message"] != "tag 'sample' does not exist" {
		t.Errorf("expected message field 'tag 'sample' does not exist', got '%s'", parsed["message"])
	}
}

func TestHasLiveTag(t *testing.T) {
	tests := []struct {
		name string
		tags []service.Tag
		live bool
	}{
		{"no results", nil, false},
		{"only zero-count placeholders", []service.Tag{{Name: "gold_footwear", PostCount: 0}}, false},
		{"one live among placeholders", []service.Tag{{PostCount: 0}, {PostCount: 12}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasLiveTag(tt.tags); got != tt.live {
				t.Errorf("hasLiveTag(%v) = %v, want %v", tt.tags, got, tt.live)
			}
		})
	}
}

func TestToolSchemas_HaveRequired(t *testing.T) {
	tests := []struct {
		name        string
		tool        mcp.Tool
		expectedReq string
	}{
		{"search_tags", SearchTagsTool, "query"},
		{"get_tag_info", GetTagInfoTool, "name"},
		{"get_tags_info", GetTagsInfoTool, "names"},
		{"get_related_tags", GetRelatedTagsTool, "tag"},
		{"get_tag_alias", GetTagAliasTool, "name"},
		{"search_posts", SearchPostsTool, "tags"},
		{"get_tag_profile", GetTagProfileTool, "tag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := false
			for _, field := range tt.tool.InputSchema.Required {
				if field == tt.expectedReq {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("tool %s input schema missing required field %q, got: %+v", tt.name, tt.expectedReq, tt.tool.InputSchema.Required)
			}
		})
	}
}

func TestToolSchemas_PropertiesExist(t *testing.T) {
	tests := []struct {
		name     string
		tool     mcp.Tool
		property string
	}{
		{"search_tags", SearchTagsTool, "limit"},
		{"search_tags", SearchTagsTool, "category"},
		{"search_tags", SearchTagsTool, "min_posts"},
		{"get_tags_info", GetTagsInfoTool, "names"},
		{"get_related_tags", GetRelatedTagsTool, "limit"},
		{"get_tag_wiki", GetTagWikiTool, "other_names"},
		{"get_tag_wiki", GetTagWikiTool, "include_description"},
		{"search_posts", SearchPostsTool, "limit"},
		{"search_posts", SearchPostsTool, "tags_mode"},
		{"get_tag_profile", GetTagProfileTool, "sample"},
		{"get_tag_profile", GetTagProfileTool, "with"},
		{"get_tag_profile", GetTagProfileTool, "rating"},
		{"get_tag_profile", GetTagProfileTool, "order"},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.property, func(t *testing.T) {
			if _, exists := tt.tool.InputSchema.Properties[tt.property]; !exists {
				t.Errorf("tool %s missing expected property %s", tt.name, tt.property)
			}
		})
	}
}

func TestSearchPostsTool_TagsModeEnum(t *testing.T) {
	// The enum is the contract: only full/none/diff are valid modes, and
	// there is no default so the absent param means full server-side.
	prop, exists := SearchPostsTool.InputSchema.Properties["tags_mode"]
	if !exists {
		t.Fatal("search_posts missing tags_mode property")
	}
	schema, ok := prop.(map[string]any)
	if !ok {
		t.Fatalf("tags_mode property is not an object, got: %#v", prop)
	}
	enum, ok := schema["enum"].([]string)
	if !ok {
		t.Fatalf("tags_mode enum missing or wrong type, got: %#v", schema["enum"])
	}
	want := []string{"full", "none", "diff"}
	if len(enum) != len(want) {
		t.Fatalf("tags_mode enum = %v, want %v", enum, want)
	}
	for i, v := range want {
		if enum[i] != v {
			t.Errorf("tags_mode enum[%d] = %q, want %q", i, enum[i], v)
		}
	}
}

// upstreamMockError is the failing mock's answer: the shrunken cause text
// marked as an upstream failure the same way the api layer's upstreamError
// marks real ones (Error() is the cause alone; Is matches only the
// app.ErrUpstreamUnavailable sentinel).
type upstreamMockError struct{}

func (e *upstreamMockError) Error() string        { return "danbooru 503 (Downbooru)" }
func (e *upstreamMockError) Is(target error) bool { return target == app.ErrUpstreamUnavailable }

// brokenService is a mock TagService whose every request-bearing method
// fails with the upstream-marked error, simulating a Danbooru outage.
type brokenService struct{}

func (s *brokenService) upstreamErr() error { return &upstreamMockError{} }

func (s *brokenService) Search(ctx context.Context, query string, limit, category, minPosts int) (*service.SearchResult, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) Info(ctx context.Context, name string) (*service.TagInfo, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) Infos(ctx context.Context, names []string) (*service.TagsInfo, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) WD14Info(name string) service.WD14Status {
	return service.WD14Status{Status: "unknown"}
}

func (s *brokenService) TaggerVocabHits(query string, limit int) []vocab.Hit { return nil }

func (s *brokenService) Related(ctx context.Context, tag string, limit int) (*service.RelatedResult, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) Alias(ctx context.Context, name string) (*service.AliasResult, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) Wiki(ctx context.Context, title, otherNames string, limit int, includeDescription bool) (*service.WikiResult, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) SearchPosts(ctx context.Context, tags string, limit int, tagsMode string) ([]service.Post, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) Profile(ctx context.Context, tag string, sample int, opts service.ProfileOptions) (*service.TagProfile, error) {
	return nil, s.upstreamErr()
}

// TestHandlers_UpstreamFailureIsUniform pins PRD R10: during an outage every
// one of the eight tools answers the same protocol-level marked result with
// the same upstream_unavailable code and message — so an agent needs a
// single rule (stop calling this server) instead of eight per-tool codes.
func TestHandlers_UpstreamFailureIsUniform(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0.0", server.WithToolCapabilities(true))
	Register(s, &brokenService{})

	cli, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("in-process client: %v", err)
	}
	defer cli.Close()
	if err := cli.Start(context.Background()); err != nil {
		t.Fatalf("client start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "uniform-test", Version: "0.0.1"}
	if _, err := cli.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	const wantMessage = "Danbooru is not answering right now: danbooru 503 (Downbooru). All tools of this server are affected; retry later."
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"search_tags", map[string]any{"query": "blue_hair"}},
		{"get_tag_info", map[string]any{"name": "blue_hair"}},
		{"get_tags_info", map[string]any{"names": []any{"blue_hair"}}},
		{"get_related_tags", map[string]any{"tag": "blue_hair"}},
		{"get_tag_alias", map[string]any{"name": "blue_hair"}},
		{"get_tag_wiki", map[string]any{"title": "blue_hair"}},
		{"search_posts", map[string]any{"tags": "blue_hair"}},
		{"get_tag_profile", map[string]any{"tag": "blue_hair"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Name = tt.name
			req.Params.Arguments = tt.args
			res, err := cli.CallTool(context.Background(), req)
			if err != nil {
				t.Fatalf("CallTool %s: %v", tt.name, err)
			}
			if !res.IsError {
				t.Fatalf("%s must flag the outage at the protocol level (IsError), got: %+v", tt.name, res)
			}
			if len(res.Content) != 1 {
				t.Fatalf("%s: expected 1 content item, got %d", tt.name, len(res.Content))
			}
			text, ok := res.Content[0].(mcp.TextContent)
			if !ok {
				t.Fatalf("%s: expected text content, got %T", tt.name, res.Content[0])
			}
			if tt.name == "search_tags" {
				t.Logf("sample payload: %s", text.Text)
			}
			var parsed map[string]string
			if err := json.Unmarshal([]byte(text.Text), &parsed); err != nil {
				t.Fatalf("%s: payload is not JSON: %v (%s)", tt.name, err, text.Text)
			}
			if parsed["error"] != "upstream_unavailable" {
				t.Errorf("%s: error code = %q, want upstream_unavailable", tt.name, parsed["error"])
			}
			if parsed["message"] != wantMessage {
				t.Errorf("%s: message = %q, want %q", tt.name, parsed["message"], wantMessage)
			}
		})
	}
}
