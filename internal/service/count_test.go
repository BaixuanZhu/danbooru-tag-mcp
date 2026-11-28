package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"danbooru-tag-mcp/internal/app"
	"danbooru-tag-mcp/internal/vocab"
)

// timedOutMessage is the exact text of the count-timeout error; agents match
// on it, so it must not drift.
const timedOutMessage = "danbooru could not count this query (count timed out); simplify the query or retry later"

// upstreamMarkedError stands in for the api layer's upstream marker the way
// the tools tests do: Error() is the shrunken cause, Is matches only the
// app.ErrUpstreamUnavailable sentinel.
type upstreamMarkedError struct{}

func (e *upstreamMarkedError) Error() string        { return "danbooru 503 (Downbooru)" }
func (e *upstreamMarkedError) Is(target error) bool { return target == app.ErrUpstreamUnavailable }

// TestCountPosts_PayloadBytesIsQueryThenTotal pins the count-mode payload:
// {"query":<tags as sent, trimmed>,"total":<n>} with query first, answered
// by exactly one count request and no posts request — listing posts is the
// cost count mode exists to avoid.
func TestCountPosts_PayloadBytesIsQueryThenTotal(t *testing.T) {
	mock := &mockFetcher{countResp: []byte(`{"counts":{"posts":317377}}`)}
	svc := NewTagService(mock, vocab.Default())

	res, err := svc.CountPosts(context.Background(), "  arm_up  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"query":"arm_up","total":317377}`; string(raw) != want {
		t.Errorf("payload:\n got: %s\nwant: %s", raw, want)
	}
	if mock.countCalls != 1 {
		t.Errorf("a count must cost exactly one count request, got %d", mock.countCalls)
	}
	if mock.lastCountArg != "arm_up" {
		t.Errorf("the trimmed query must reach the count request, got %q", mock.lastCountArg)
	}
	if mock.postsCalls != 0 {
		t.Errorf("count mode must not list posts, got %d posts requests", mock.postsCalls)
	}
}

// TestCountPosts_ZeroIsACount pins that a 0 from Danbooru is the answer, not
// a failure: an unknown tag counts as 0 (verified live), and the 0 reaches
// the payload. Only an explicit null means "could not count".
func TestCountPosts_ZeroIsACount(t *testing.T) {
	mock := &mockFetcher{countResp: []byte(`{"counts":{"posts":0}}`)}
	svc := NewTagService(mock, nil)

	res, err := svc.CountPosts(context.Background(), "not_a_real_tag_xyz")
	if err != nil {
		t.Fatalf("a zero count is a valid answer, got: %v", err)
	}
	if res.Query != "not_a_real_tag_xyz" || res.Total != 0 {
		t.Errorf("expected {not_a_real_tag_xyz 0}, got %+v", res)
	}
	// The zero must survive marshaling: an omitempty tag on Total would drop
	// it and leave a payload with no answer in it.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"query":"not_a_real_tag_xyz","total":0}`; string(raw) != want {
		t.Errorf("payload:\n got: %s\nwant: %s", raw, want)
	}
}

// TestCountPosts_NoRatingInjection mirrors TestSearchPosts_NoRatingInjection:
// no layer may pick a rating on the caller's behalf. The query must reach
// the count request verbatim, so an unfiltered query counts the tag's whole
// population, and a caller-supplied rating metatag is kept as typed.
func TestCountPosts_NoRatingInjection(t *testing.T) {
	mock := &mockFetcher{countResp: []byte(`{"counts":{"posts":317377}}`)}
	svc := NewTagService(mock, nil)

	if _, err := svc.CountPosts(context.Background(), "arm_up"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastCountArg != "arm_up" {
		t.Errorf("query must be sent verbatim with no rating appended, got %q", mock.lastCountArg)
	}

	if _, err := svc.CountPosts(context.Background(), "arm_up rating:g"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastCountArg != "arm_up rating:g" {
		t.Errorf("caller rating must be kept verbatim, got %q", mock.lastCountArg)
	}
}

// TestCountPosts_SharesTheTagCapWithSearchPosts pins that count mode applies
// the post search's local tag-count rule and nothing else. Danbooru enforces
// the same 2-content-tag limit on /counts/posts.json (verified live: three
// content tags answer 422 on both endpoints), so every query must get the
// same verdict from both modes — the identical message, space-split hint
// included — before any request. Metatags are not rejected: the endpoint
// honors rating:, status: and negation (verified live).
func TestCountPosts_SharesTheTagCapWithSearchPosts(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		store  *vocab.Store
		reject string // expected error text; "" means the query is accepted
	}{
		{name: "single tag", query: "arm_up"},
		{name: "two content tags", query: "arm_up holding_weapon"},
		{name: "exempt metatags do not count", query: "1girl blue_hair status:any"},
		{name: "metatag-only query", query: "rating:g"},
		{name: "negation", query: "arm_up -sword"},
		{name: "order: counts toward the cap", query: "1girl blue_hair order:count", reject: overLimitMessage},
		{name: "three content tags", query: "1girl blue_hair long_hair", reject: overLimitMessage},
		{
			name:   "space-split pair keeps its hint",
			query:  "blue hair order:random",
			store:  vocab.Default(),
			reject: overLimitMessage + "; 'blue hair' may be one tag: blue_hair",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			countMock := &mockFetcher{countResp: []byte(`{"counts":{"posts":1}}`)}
			_, countErr := NewTagService(countMock, tt.store).CountPosts(context.Background(), tt.query)
			postsMock := &mockFetcher{postsResp: []byte(`[]`)}
			_, postsErr := NewTagService(postsMock, tt.store).SearchPosts(context.Background(), tt.query, 5, "")

			if tt.reject == "" {
				if countErr != nil || postsErr != nil {
					t.Fatalf("both modes must accept the query, got count: %v, posts: %v", countErr, postsErr)
				}
				if countMock.countCalls != 1 || postsMock.postsCalls != 1 {
					t.Errorf("each mode must spend exactly one request, got count %d, posts %d", countMock.countCalls, postsMock.postsCalls)
				}
				if countMock.lastCountArg != tt.query {
					t.Errorf("the query must reach the count request as typed, got %q", countMock.lastCountArg)
				}
				if countMock.postsCalls != 0 || postsMock.countCalls != 0 {
					t.Errorf("count mode must not list posts and post mode must not count, got %d and %d", countMock.postsCalls, postsMock.countCalls)
				}
				return
			}
			if countErr == nil || postsErr == nil {
				t.Fatalf("both modes must reject the query, got count: %v, posts: %v", countErr, postsErr)
			}
			if countErr.Error() != tt.reject || postsErr.Error() != tt.reject {
				t.Errorf("both modes must reject with the identical message:\ncount: %s\nposts: %s\nwant:  %s", countErr, postsErr, tt.reject)
			}
			if countMock.countCalls != 0 || postsMock.postsCalls != 0 {
				t.Errorf("an over-limit query must be rejected before any request, got count %d, posts %d", countMock.countCalls, postsMock.postsCalls)
			}
		})
	}
}

// TestCountPosts_NullCountIsTheTimedOutError pins the mapping of Danbooru's
// {"counts":{"posts":null}} (verified live on "arm_up filetype:png"): its
// count timed out, which is a definitive answer from a reachable upstream.
// It becomes the sentinel error with its exact text — never a total of 0,
// which would read as "no such posts", and never an upstream failure, which
// would tell the agent the whole server is down.
func TestCountPosts_NullCountIsTheTimedOutError(t *testing.T) {
	for _, body := range []string{
		`{"counts":{"posts":null}}`,         // the live shape
		`{ "counts" : { "posts" : null } }`, // whitespace around the literal
	} {
		mock := &mockFetcher{countResp: []byte(body)}
		svc := NewTagService(mock, nil)

		res, err := svc.CountPosts(context.Background(), "arm_up filetype:png")
		if err == nil {
			t.Fatalf("body %s: a null count must be an error, got %+v", body, res)
		}
		if res != nil {
			t.Errorf("body %s: a failed count must carry no payload, got %+v", body, res)
		}
		if !errors.Is(err, ErrPostCountTimedOut) {
			t.Errorf("body %s: expected ErrPostCountTimedOut, got: %v", body, err)
		}
		if err.Error() != timedOutMessage {
			t.Errorf("body %s: message:\n got: %s\nwant: %s", body, err, timedOutMessage)
		}
		if errors.Is(err, app.ErrUpstreamUnavailable) {
			t.Errorf("body %s: a timed-out count is not an outage and must not carry the upstream marker", body)
		}
		if mock.countCalls != 1 {
			t.Errorf("body %s: the null count must cost exactly one request, got %d", body, mock.countCalls)
		}
	}
}

// TestCountPosts_UnexpectedShapeIsAParseErrorNotATimeout pins the other side
// of the mapping: anything that is not an integer and not an explicit null
// is upstream shape drift and reports as a parse error. Drift must never be
// reported as a timed-out count (the message tells the agent to retry later,
// which would not help) nor as an outage, and the message stays short even
// when the unexpected value is huge.
func TestCountPosts_UnexpectedShapeIsAParseErrorNotATimeout(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing counts.posts", `{}`},
		{"empty counts", `{"counts":{}}`},
		{"null counts", `{"counts":null}`},
		{"renamed key", `{"counts":{"post_count":317377}}`},
		{"quoted number", `{"counts":{"posts":"317377"}}`},
		{"fractional number", `{"counts":{"posts":317377.5}}`},
		{"boolean", `{"counts":{"posts":true}}`},
		{"object", `{"counts":{"posts":{"total":317377}}}`},
		{"array body", `[]`},
		{"empty body", ``},
		{"HTML maintenance page", `<html><head><title>Downbooru</title></head></html>`},
		{"truncated JSON", `{"counts":{"posts":31737`},
		{"oversized value", `{"counts":{"posts":"` + strings.Repeat("x", 100000) + `"}}`},
		// A body that is not JSON at all takes the other parse-error branch.
		{"oversized unparsable body", "<html>" + strings.Repeat("x", 100000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{countResp: []byte(tt.body)}
			svc := NewTagService(mock, nil)

			res, err := svc.CountPosts(context.Background(), "arm_up")
			if err == nil {
				t.Fatalf("an unexpected shape must be an error, got %+v", res)
			}
			if res != nil {
				t.Errorf("a failed count must carry no payload, got %+v", res)
			}
			if errors.Is(err, ErrPostCountTimedOut) {
				t.Errorf("shape drift must not be reported as a timed-out count, got: %v", err)
			}
			if errors.Is(err, app.ErrUpstreamUnavailable) {
				t.Errorf("shape drift is not an outage and must not carry the upstream marker, got: %v", err)
			}
			if !strings.HasPrefix(err.Error(), "failed to parse post count: ") {
				t.Errorf("expected a parse error, got: %v", err)
			}
			if len(err.Error()) > 200 {
				t.Errorf("the message must stay short regardless of the value, got %d bytes: %.120q...", len(err.Error()), err.Error())
			}
			if mock.countCalls != 1 {
				t.Errorf("expected exactly one request, got %d", mock.countCalls)
			}
		})
	}
}

// TestCountPosts_ParseErrorQuotesAtMost32Bytes pins how much of an unexpected
// counts.posts value the parse error may quote: 32 bytes and an ellipsis,
// because the text lands in agent context and a drifted response can carry a
// value of any size. The expectation is a literal rather than
// maxCountValueSnippet, so moving the cap in either direction goes red.
func TestCountPosts_ParseErrorQuotesAtMost32Bytes(t *testing.T) {
	mock := &mockFetcher{countResp: []byte(`{"counts":{"posts":"` + strings.Repeat("x", 100000) + `"}}`)}
	svc := NewTagService(mock, nil)

	_, err := svc.CountPosts(context.Background(), "arm_up")
	if err == nil {
		t.Fatal("a non-integer count must be a parse error, got nil")
	}
	// The value is a JSON string, so its first 32 bytes are the opening
	// quote and 31 of the x's.
	want := "failed to parse post count: counts.posts is not an integer: \"" + strings.Repeat("x", 31) + "..."
	if err.Error() != want {
		t.Errorf("message (%d bytes):\n got: %.100q...\nwant: %.100q...", len(err.Error()), err.Error(), want)
	}
}

// TestCountPosts_FetchErrorsPassThroughUnchanged pins that whatever the api
// layer reports for the count request reaches the handler as is: an
// upstream-marked failure keeps its marker (the handler turns it into the
// uniform result) and a definitive 4xx stays a plain error. CountPosts adds
// no wrapping, no fallback and no second request — a failed count must never
// degrade into listing posts.
func TestCountPosts_FetchErrorsPassThroughUnchanged(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		marked bool
	}{
		{"upstream failure keeps its marker", &upstreamMarkedError{}, true},
		{"definitive 4xx stays unmarked", errors.New(`danbooru 422: {"error":"Tag limit exceeded"}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockFetcher{countErr: tt.err}
			svc := NewTagService(mock, nil)

			res, err := svc.CountPosts(context.Background(), "arm_up")
			if err == nil || res != nil {
				t.Fatalf("the fetch error must surface, got res %+v, err %v", res, err)
			}
			if err != tt.err {
				t.Errorf("the fetch error must pass through unchanged, got: %v (want %v)", err, tt.err)
			}
			if errors.Is(err, app.ErrUpstreamUnavailable) != tt.marked {
				t.Errorf("upstream marker = %v, want %v (err: %v)", errors.Is(err, app.ErrUpstreamUnavailable), tt.marked, err)
			}
			if mock.countCalls != 1 {
				t.Errorf("a failed count must not be retried at this layer, got %d requests", mock.countCalls)
			}
			if mock.postsCalls != 0 {
				t.Errorf("a failed count must not fall back to listing posts, got %d posts requests", mock.postsCalls)
			}
		})
	}
}

// TestCountPosts_IsAlwaysLive pins R2: nothing is stored between calls. The
// response cache left in 0.7.0 and must not return through count mode, where
// a stale total is indistinguishable from a fresh one.
func TestCountPosts_IsAlwaysLive(t *testing.T) {
	mock := &mockFetcher{countResp: []byte(`{"counts":{"posts":317377}}`)}
	svc := NewTagService(mock, nil)

	first, err := svc.CountPosts(context.Background(), "arm_up")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mock.countResp = []byte(`{"counts":{"posts":317378}}`)
	second, err := svc.CountPosts(context.Background(), "arm_up")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.countCalls != 2 {
		t.Errorf("every call must reach the upstream, got %d requests for 2 calls", mock.countCalls)
	}
	if first.Total != 317377 || second.Total != 317378 {
		t.Errorf("the second call must reflect the new upstream answer, got %d then %d", first.Total, second.Total)
	}
}
