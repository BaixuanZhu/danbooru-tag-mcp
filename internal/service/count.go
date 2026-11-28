package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrPostCountTimedOut reports that Danbooru answered a count request with
// {"counts":{"posts":null}}: its count ran into the statement timeout
// (verified live on "arm_up filetype:png"; expensive metatags cause it). That
// is a definitive answer from a reachable upstream, so the error carries no
// upstream marker. Its text is the whole contract: the handler forwards it
// verbatim and agents match on it.
var ErrPostCountTimedOut = errors.New("danbooru could not count this query (count timed out); simplify the query or retry later")

// PostCount is search_posts' payload in count mode: the query as sent
// (trimmed) and the number of posts Danbooru reports for it. Query comes
// first, so the payload reads as the question and then the answer.
type PostCount struct {
	Query string `json:"query"`
	Total int    `json:"total"`
}

// danbooruCountsResponse is the part of /counts/posts.json the service
// reads. Posts stays raw on purpose: an explicit null (the count timed out)
// and a missing key (shape drift) both read as the zero value of a typed
// field, and the two must map to different errors.
type danbooruCountsResponse struct {
	Counts struct {
		Posts json.RawMessage `json:"posts"`
	} `json:"counts"`
}

// maxCountValueSnippet bounds the unexpected value quoted in the parse
// error: the text lands in agent context, and a drifted response could carry
// a value of any size.
const maxCountValueSnippet = 32

// CountPosts answers how many posts a query matches, without listing any:
// one request to /counts/posts.json. It shares checkPostQuery with
// SearchPosts, so the local tag-count cap and its message are identical and
// fire before any request. limit and tags_mode have no counterpart here
// because nothing is listed.
//
// The tags go out verbatim — no layer injects a rating filter — and metatags
// are not rejected: the endpoint honors them (verified live: the four rating:
// slices of arm_up sum exactly to its unfiltered total). Nothing is stored
// between calls, so a count is always live.
//
// Three outcomes are told apart. An integer is the answer. An explicit null
// means Danbooru's count timed out and becomes ErrPostCountTimedOut: never a
// total of 0, which would read as "no such posts", and never an upstream
// failure, which would tell the agent the whole server is down. Anything else
// is upstream shape drift and reports as a parse error, so drift is never
// dressed up as a retryable timeout. Whatever the api layer reports for the
// request passes through unchanged.
func (s *TagService) CountPosts(ctx context.Context, tags string) (*PostCount, error) {
	query, err := s.checkPostQuery(tags)
	if err != nil {
		return nil, err
	}
	res, err := s.fetcher.FetchPostCount(ctx, query)
	if err != nil {
		return nil, err
	}
	total, err := parsePostCount(res)
	if err != nil {
		return nil, err
	}
	return &PostCount{Query: query, Total: total}, nil
}

// parsePostCount reads the total out of a /counts/posts.json body: an integer
// counts.posts is the answer, the literal null is ErrPostCountTimedOut, and
// every other body is a parse error.
func parsePostCount(body []byte) (int, error) {
	var resp danbooruCountsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("failed to parse post count: %w", err)
	}
	raw := resp.Counts.Posts
	if len(raw) == 0 {
		return 0, errors.New("failed to parse post count: response carries no counts.posts value")
	}
	if string(raw) == "null" {
		return 0, ErrPostCountTimedOut
	}
	total, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("failed to parse post count: counts.posts is not an integer: %s", countValueSnippet(raw))
	}
	return total, nil
}

// countValueSnippet quotes an unexpected counts.posts value for the parse
// error, cut to maxCountValueSnippet bytes.
func countValueSnippet(raw []byte) string {
	if len(raw) <= maxCountValueSnippet {
		return string(raw)
	}
	return string(raw[:maxCountValueSnippet]) + "..."
}
