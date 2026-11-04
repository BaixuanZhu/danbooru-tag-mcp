package tools

import (
	"encoding/json"
	"testing"

	"danbooru-tag-mcp/internal/service"

	"github.com/mark3labs/mcp-go/mcp"
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
		{"search_tags", SearchTagsTool, "refresh"},
		{"get_tag_info", GetTagInfoTool, "refresh"},
		{"get_tags_info", GetTagsInfoTool, "names"},
		{"get_tags_info", GetTagsInfoTool, "refresh"},
		{"get_related_tags", GetRelatedTagsTool, "limit"},
		{"get_related_tags", GetRelatedTagsTool, "refresh"},
		{"get_tag_alias", GetTagAliasTool, "refresh"},
		{"get_tag_wiki", GetTagWikiTool, "other_names"},
		{"get_tag_wiki", GetTagWikiTool, "include_description"},
		{"get_tag_wiki", GetTagWikiTool, "refresh"},
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

func TestToolSchemas_NoRefreshOnUncachedTools(t *testing.T) {
	// Posts and profile responses are never cached, so the refresh bypass
	// (and the stale markers it pairs with) have nothing to act on there.
	for _, tt := range []struct {
		name string
		tool mcp.Tool
	}{
		{"search_posts", SearchPostsTool},
		{"get_tag_profile", GetTagProfileTool},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, exists := tt.tool.InputSchema.Properties["refresh"]; exists {
				t.Errorf("tool %s must not expose a refresh param", tt.name)
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
