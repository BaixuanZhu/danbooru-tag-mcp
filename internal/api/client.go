// Package api provides a rate-limited HTTP client for the Danbooru REST API.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	defaultBaseURL   = "https://danbooru.donmai.us"
	defaultReqGap    = 1500 * time.Millisecond
	defaultUserAgent = "danbooru-mcp/0.1"
	maxResponseBody  = 5 * 1024 * 1024 // 5MB
)

type Client struct {
	httpClient *http.Client
	baseURL    string
	login      string
	apiKey     string
	userAgent  string
	reqGap     time.Duration
	lastReq    time.Time
	mu         sync.Mutex
}

type Option func(*Client)

func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = u }
}

func WithReqGap(d time.Duration) Option {
	return func(c *Client) { c.reqGap = d }
}

func WithCredentials(login, apiKey string) Option {
	return func(c *Client) {
		c.login = login
		c.apiKey = apiKey
	}
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) { c.httpClient = httpClient }
}

func NewClient(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    defaultBaseURL,
		login:      os.Getenv("DANBOORU_LOGIN"),
		apiKey:     os.Getenv("DANBOORU_API_KEY"),
		userAgent:  defaultUserAgent,
		reqGap:     defaultReqGap,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get is the single choke point for all outbound requests: rate-limit wait,
// context cancellation, UA and credential injection, and a 5MB body cap.
func (c *Client) Get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Rate limiting: interruptible via context timeout/cancel
	elapsed := time.Since(c.lastReq)
	if elapsed < c.reqGap {
		wait := c.reqGap - elapsed
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	c.lastReq = time.Now()

	if params == nil {
		params = make(url.Values)
	}
	if c.login != "" && c.apiKey != "" {
		params.Set("login", c.login)
		params.Set("api_key", c.apiKey)
	}

	reqURL := fmt.Sprintf("%s%s", c.baseURL, path)
	if encoded := params.Encode(); encoded != "" {
		reqURL = fmt.Sprintf("%s?%s", reqURL, encoded)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read at most 5MB + 1 byte to detect overflow
	limitedReader := io.LimitReader(resp.Body, maxResponseBody+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBody {
		return nil, errors.New("response size exceeded 5MB limit")
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusTooManyRequests, 421:
		return nil, errors.New("rate limited, wait and retry")
	default:
		return nil, fmt.Errorf("danbooru %d: %s", resp.StatusCode, string(body))
	}
}

func (c *Client) FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool) ([]byte, error) {
	q := make(url.Values)
	q.Set("search[name_matches]", fmt.Sprintf("*%s*", namePattern))
	if orderByCount {
		q.Set("search[order]", "count")
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.Get(ctx, "/tags.json", q)
}

func (c *Client) FetchTagExact(ctx context.Context, name string) ([]byte, error) {
	q := make(url.Values)
	q.Set("search[name]", name)
	q.Set("limit", "1")
	return c.Get(ctx, "/tags.json", q)
}

func (c *Client) FetchRelated(ctx context.Context, tag string) ([]byte, error) {
	q := make(url.Values)
	q.Set("query", tag)
	return c.Get(ctx, "/related_tag.json", q)
}

// FetchPosts sends tags as-is; NSFW filtering is injected by the service layer.
func (c *Client) FetchPosts(ctx context.Context, tags string, limit int) ([]byte, error) {
	q := make(url.Values)
	q.Set("tags", tags)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.Get(ctx, "/posts.json", q)
}
