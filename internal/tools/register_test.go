package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
		{"search_posts", SearchPostsTool, "count_only"},
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

func (s *brokenService) SearchPosts(ctx context.Context, tags string, limit int, tagsMode string) (*service.PostsResult, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) CountPosts(ctx context.Context, tags string) (*service.PostCount, error) {
	return nil, s.upstreamErr()
}

func (s *brokenService) Profile(ctx context.Context, tag string, sample int, opts service.ProfileOptions) (*service.TagProfile, error) {
	return nil, s.upstreamErr()
}

// postsMockService is a mock TagService for the search_posts handler: its
// SearchPosts answers from the canned posts/err fields and records the call,
// its CountPosts from count/countErr the same way. Every other method keeps
// brokenService's upstream failure; the tests that use this mock never call
// them. The two methods count their calls separately, so a test can prove
// which mode the handler routed a call to.
type postsMockService struct {
	brokenService

	posts *service.PostsResult
	err   error

	count    *service.PostCount
	countErr error

	calls    int
	gotTags  string
	gotLimit int
	gotMode  string

	countCalls   int
	gotCountTags string
}

// SearchPosts records the call and answers from the canned fields.
func (s *postsMockService) SearchPosts(ctx context.Context, tags string, limit int, tagsMode string) (*service.PostsResult, error) {
	s.calls++
	s.gotTags, s.gotLimit, s.gotMode = tags, limit, tagsMode
	return s.posts, s.err
}

// CountPosts records the call and answers from the canned fields.
func (s *postsMockService) CountPosts(ctx context.Context, tags string) (*service.PostCount, error) {
	s.countCalls++
	s.gotCountTags = tags
	return s.count, s.countErr
}

// inProcessClient wires a real MCP server around svc and returns a started
// in-process client, so handler tests see exactly what an MCP client gets.
func inProcessClient(t *testing.T, svc TagService) *client.Client {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0.0", server.WithToolCapabilities(true))
	Register(s, svc)

	cli, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("in-process client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if err := cli.Start(context.Background()); err != nil {
		t.Fatalf("client start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "handler-test", Version: "0.0.1"}
	if _, err := cli.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return cli
}

// callText calls one tool and returns its result together with the text of
// its single content item, i.e. the wire payload an MCP client reads.
func callText(t *testing.T, cli *client.Client, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := cli.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: expected 1 content item, got %d", name, len(res.Content))
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("%s: expected text content, got %T", name, res.Content[0])
	}
	return res, tc.Text
}

// TestSearchPostsHandler_PayloadBytesUnchanged pins the wire bytes the
// search_posts handler must keep whatever the service learns later: a valid
// query answers {"posts":[...]} and nothing else, a dead query answers the
// bare {"posts":[]} (never null, no diagnostic key), and a validation error
// stays the plain post_search_failed result. The marked
// upstream_unavailable result would tell the agent to stop calling this
// server, which is the wrong lesson for a malformed query. Each call must
// reach the service exactly once, with the tags as typed.
func TestSearchPostsHandler_PayloadBytesUnchanged(t *testing.T) {
	checkListModeBytes(t, nil)
}

// TestSearchPostsHandler_CountOnlyFalseKeepsListModeBytes pins the other
// side of the count-mode switch: an explicit count_only:false is list mode,
// so the pinned answers must come back byte for byte. Count mode is opt-in;
// a client that sends the flag as false must never be switched over.
func TestSearchPostsHandler_CountOnlyFalseKeepsListModeBytes(t *testing.T) {
	checkListModeBytes(t, map[string]any{"count_only": false})
}

// checkListModeBytes runs the pinned list-mode payloads of the search_posts
// handler, with extraArgs added to every call: one table serves the bare
// call and any call that carries an argument list mode must ignore.
func checkListModeBytes(t *testing.T, extraArgs map[string]any) {
	t.Helper()
	fullPost := service.Post{
		ID:       900,
		Rating:   "e",
		Score:    42,
		FavCount: 7,
		Tags: &service.PostTags{
			Copyright: []string{"touhou"},
			Artist:    []string{"kantoku"},
			Character: []string{"hakurei_reimu"},
			General:   []string{"1girl", "solo"},
			Meta:      []string{"highres"},
		},
		PreviewURL:   "https://cdn.donmai.us/preview/x.jpg",
		LargeFileURL: "https://cdn.donmai.us/large/x.jpg",
	}
	overLimit := errors.New("too many content tags: 3 (max 2, free/anonymous Danbooru limit; rating: and most other metatags do not count, order: does); drop tags or split the query")

	tests := []struct {
		name string
		svc  *postsMockService
		args map[string]any
		want string
	}{
		{
			name: "non-empty result carries posts only",
			svc:  &postsMockService{posts: &service.PostsResult{Posts: []service.Post{fullPost}}},
			args: map[string]any{"tags": "touhou"},
			want: `{"posts":[{"id":900,"rating":"e","score":42,"fav_count":7,"tags":{"copyright":["touhou"],"artist":["kantoku"],"character":["hakurei_reimu"],"general":["1girl","solo"],"meta":["highres"]},"preview_file_url":"https://cdn.donmai.us/preview/x.jpg","large_file_url":"https://cdn.donmai.us/large/x.jpg"}]}`,
		},
		{
			name: "dead query is a bare empty array",
			svc:  &postsMockService{posts: &service.PostsResult{Posts: []service.Post{}}},
			args: map[string]any{"tags": "not_a_real_tag_xyz"},
			want: `{"posts":[]}`,
		},
		{
			// The real service appends a hint to this message only for a
			// query that names a candidate, so the query is one that names
			// none: the fixture mirrors a combination the chain produces.
			name: "over-limit query is the plain validation result",
			svc:  &postsMockService{err: overLimit},
			args: map[string]any{"tags": "1girl blue_hair long_hair"},
			want: `{"error":"post_search_failed","message":"` + overLimit.Error() + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := make(map[string]any, len(tt.args)+len(extraArgs))
			for k, v := range tt.args {
				args[k] = v
			}
			for k, v := range extraArgs {
				args[k] = v
			}
			res, text := callText(t, inProcessClient(t, tt.svc), "search_posts", args)
			if text != tt.want {
				t.Errorf("payload:\n got: %s\nwant: %s", text, tt.want)
			}
			if res.IsError {
				t.Errorf("a successful or validation result must not be flagged at the protocol level, got: %+v", res)
			}
			if strings.Contains(text, "upstream_unavailable") {
				t.Errorf("a validation error must never be marked upstream_unavailable, got: %s", text)
			}
			if tt.svc.calls != 1 {
				t.Errorf("the handler must call the service exactly once (no retry), got %d", tt.svc.calls)
			}
			if tt.svc.countCalls != 0 {
				t.Errorf("list mode must never ask for a count, got %d CountPosts call(s)", tt.svc.countCalls)
			}
			if tt.svc.gotTags != tt.args["tags"] {
				t.Errorf("tags must reach the service as typed, got %q", tt.svc.gotTags)
			}
		})
	}
}

// TestSearchPostsHandler_CountModePayloadBytes pins the wire bytes of the
// count-mode switch. count_only routes the call to CountPosts — never to
// SearchPosts — and marshals its answer as is: {"query":...,"total":...}.
// The post-mode arguments are neither read nor validated on that path, so a
// limit over the cap and an unknown tags_mode still get their count. Errors
// keep failResult's mapping: a timed-out count and an over-limit query are
// the plain post_search_failed result, an outage the uniform marked result.
func TestSearchPostsHandler_CountModePayloadBytes(t *testing.T) {
	overLimit := errors.New("too many content tags: 3 (max 2, free/anonymous Danbooru limit; rating: and most other metatags do not count, order: does); drop tags or split the query")
	count := &service.PostCount{Query: "blue_hair", Total: 317377}

	tests := []struct {
		name        string
		svc         *postsMockService
		args        map[string]any
		want        string
		wantIsError bool
	}{
		{
			name: "count answers query then total",
			svc:  &postsMockService{count: count},
			args: map[string]any{"tags": "blue_hair", "count_only": true},
			want: `{"query":"blue_hair","total":317377}`,
		},
		{
			// No post is listed, so the post-mode arguments are nothing to
			// validate: the handler must not even look at them.
			name: "post-mode arguments are not validated",
			svc:  &postsMockService{count: count},
			args: map[string]any{"tags": "blue_hair", "count_only": true, "limit": 21, "tags_mode": "bogus"},
			want: `{"query":"blue_hair","total":317377}`,
		},
		{
			name: "timed-out count is an unmarked tool error",
			svc:  &postsMockService{countErr: service.ErrPostCountTimedOut},
			args: map[string]any{"tags": "arm_up filetype:png", "count_only": true},
			want: `{"error":"post_search_failed","message":"danbooru could not count this query (count timed out); simplify the query or retry later"}`,
		},
		{
			name: "over-limit query is the plain validation result",
			svc:  &postsMockService{countErr: overLimit},
			args: map[string]any{"tags": "1girl blue_hair long_hair", "count_only": true},
			want: `{"error":"post_search_failed","message":"` + overLimit.Error() + `"}`,
		},
		{
			// SearchPosts answers fine here, so the marked result can only
			// come from CountPosts: this row proves the routing as well.
			name:        "outage is the uniform marked result",
			svc:         &postsMockService{posts: &service.PostsResult{Posts: []service.Post{}}, countErr: &upstreamMockError{}},
			args:        map[string]any{"tags": "blue_hair", "count_only": true},
			want:        `{"error":"upstream_unavailable","message":"Danbooru is not answering right now: danbooru 503 (Downbooru). All tools of this server are affected; retry later."}`,
			wantIsError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, text := callText(t, inProcessClient(t, tt.svc), "search_posts", tt.args)
			if text != tt.want {
				t.Errorf("payload:\n got: %s\nwant: %s", text, tt.want)
			}
			if res.IsError != tt.wantIsError {
				t.Errorf("IsError = %v, want %v", res.IsError, tt.wantIsError)
			}
			if !tt.wantIsError && strings.Contains(text, "upstream_unavailable") {
				t.Errorf("a non-outage result must never be marked upstream_unavailable, got: %s", text)
			}
			if tt.svc.calls != 0 {
				t.Errorf("count mode must never list posts: SearchPosts was called %d time(s)", tt.svc.calls)
			}
			if tt.svc.countCalls != 1 {
				t.Errorf("the handler must call CountPosts exactly once (no retry), got %d", tt.svc.countCalls)
			}
			if tt.svc.gotCountTags != tt.args["tags"] {
				t.Errorf("tags must reach CountPosts as typed, got %q", tt.svc.gotCountTags)
			}
		})
	}
}

// TestSearchPostsHandler_CountOnlyEmptyTagsIsStillRejected pins that count
// mode keeps the handler's own required-argument check: tags is required in
// both modes, and the rejection costs no service call at all.
func TestSearchPostsHandler_CountOnlyEmptyTagsIsStillRejected(t *testing.T) {
	svc := &postsMockService{count: &service.PostCount{Query: "", Total: 12592609}}

	res, text := callText(t, inProcessClient(t, svc), "search_posts", map[string]any{"tags": "", "count_only": true})
	if want := `{"error":"post_search_failed","message":"tags parameter is required"}`; text != want {
		t.Errorf("payload:\n got: %s\nwant: %s", text, want)
	}
	if res.IsError {
		t.Errorf("a validation error must not be flagged at the protocol level, got: %+v", res)
	}
	if svc.calls != 0 || svc.countCalls != 0 {
		t.Errorf("the rejection must happen before the service, got %d SearchPosts and %d CountPosts call(s)", svc.calls, svc.countCalls)
	}
}

// TestSearchPostsTool_CountOnlyIsAnOptionalBoolean pins count mode's switch:
// a boolean that is never required, so every existing caller keeps list mode.
func TestSearchPostsTool_CountOnlyIsAnOptionalBoolean(t *testing.T) {
	prop, exists := SearchPostsTool.InputSchema.Properties["count_only"]
	if !exists {
		t.Fatal("search_posts missing count_only property")
	}
	schema, ok := prop.(map[string]any)
	if !ok {
		t.Fatalf("count_only property is not an object, got: %#v", prop)
	}
	if schema["type"] != "boolean" {
		t.Errorf("count_only type = %v, want boolean", schema["type"])
	}
	for _, field := range SearchPostsTool.InputSchema.Required {
		if field == "count_only" {
			t.Error("count_only must stay optional: list mode is the default")
		}
	}
}

// TestSearchPostsTool_DescriptionNamesCountOnlyOnce pins R5: count mode
// costs exactly one clause of the tool description, like possible_tags.
func TestSearchPostsTool_DescriptionNamesCountOnlyOnce(t *testing.T) {
	if got := strings.Count(SearchPostsTool.Description, "count_only"); got != 1 {
		t.Errorf("search_posts description must mention count_only exactly once, got %d: %s", got, SearchPostsTool.Description)
	}
}

// TestHandlers_UpstreamFailureIsUniform pins PRD R10: during an outage every
// one of the eight tools answers the same protocol-level marked result with
// the same upstream_unavailable code and message — so an agent needs a
// single rule (stop calling this server) instead of eight per-tool codes.
// search_posts is listed twice: count mode is the same tool on another
// endpoint, and its failures must be just as uniform.
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
		name    string
		args    map[string]any
		subtest string // names the subtest when two rows share a tool
	}{
		{"search_tags", map[string]any{"query": "blue_hair"}, ""},
		{"get_tag_info", map[string]any{"name": "blue_hair"}, ""},
		{"get_tags_info", map[string]any{"names": []any{"blue_hair"}}, ""},
		{"get_related_tags", map[string]any{"tag": "blue_hair"}, ""},
		{"get_tag_alias", map[string]any{"name": "blue_hair"}, ""},
		{"get_tag_wiki", map[string]any{"title": "blue_hair"}, ""},
		{"search_posts", map[string]any{"tags": "blue_hair"}, ""},
		{"search_posts", map[string]any{"tags": "blue_hair", "count_only": true}, "search_posts count mode"},
		{"get_tag_profile", map[string]any{"tag": "blue_hair"}, ""},
	} {
		subtest := tt.subtest
		if subtest == "" {
			subtest = tt.name
		}
		t.Run(subtest, func(t *testing.T) {
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

// TestSearchPostsHandler_PossibleTagsRideThePayload pins the handler side of
// the space-split diagnostic: it marshals the service's result as is — posts
// first, possible_tags after it — so the empty answer reads
// {"posts":[],"possible_tags":["blue_hair"]}.
func TestSearchPostsHandler_PossibleTagsRideThePayload(t *testing.T) {
	svc := &postsMockService{posts: &service.PostsResult{
		Posts:        []service.Post{},
		PossibleTags: []string{"blue_hair"},
	}}

	res, text := callText(t, inProcessClient(t, svc), "search_posts", map[string]any{"tags": "blue hair"})
	if want := `{"posts":[],"possible_tags":["blue_hair"]}`; text != want {
		t.Errorf("payload:\n got: %s\nwant: %s", text, want)
	}
	if res.IsError {
		t.Errorf("a diagnostic is not an error, got: %+v", res)
	}
}

// TestSearchPostsTool_DescriptionNamesPossibleTagsOnce pins that the
// space-split diagnostic costs at most one clause of the tool description:
// every MCP client injects that text into each session, so the field gets
// exactly one mention — enough for an agent to know it exists.
func TestSearchPostsTool_DescriptionNamesPossibleTagsOnce(t *testing.T) {
	if got := strings.Count(SearchPostsTool.Description, "possible_tags"); got != 1 {
		t.Errorf("search_posts description must mention possible_tags exactly once, got %d: %s", got, SearchPostsTool.Description)
	}
}
