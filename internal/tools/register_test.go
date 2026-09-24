package tools

import (
	"encoding/json"
	"testing"

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

func TestToolSchemas_HaveRequired(t *testing.T) {
	tests := []struct {
		name        string
		tool        mcp.Tool
		expectedReq string
	}{
		{"search_tags", SearchTagsTool, "query"},
		{"get_tag_info", GetTagInfoTool, "name"},
		{"get_related_tags", GetRelatedTagsTool, "tag"},
		{"get_tag_alias", GetTagAliasTool, "name"},
		{"search_posts", SearchPostsTool, "tags"},
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
		{"get_related_tags", GetRelatedTagsTool, "limit"},
		{"get_tag_wiki", GetTagWikiTool, "other_names"},
		{"search_posts", SearchPostsTool, "limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, exists := tt.tool.InputSchema.Properties[tt.property]; !exists {
				t.Errorf("tool %s missing expected property %s", tt.name, tt.property)
			}
		})
	}
}
