// Package api provides a rate-limited HTTP client for the Danbooru REST API.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"danbooru-tag-mcp/internal/app"
)

const (
	defaultBaseURL   = "https://danbooru.donmai.us"
	anonymousReqGap  = 1500 * time.Millisecond
	credentialedGap  = 500 * time.Millisecond
	defaultUserAgent = "danbooru-mcp/0.1"
	maxResponseBody  = 5 * 1024 * 1024 // 5MB
)

// Retry and error-shrink knobs. Attempts per request: 1 + maxRetries.
const (
	maxRetries        = 2
	firstRetryWait    = 1 * time.Second
	secondRetryWait   = 3 * time.Second
	retryAfterCap     = 30 * time.Second
	maxErrorBodyBytes = 300
	htmlFallbackBytes = 100
)

// errResponseBodyTooLarge marks the 5MB cap tripping, which a retry cannot
// fix (the same body would come back), so it is fatal for the retry loop.
var errResponseBodyTooLarge = errors.New("response size exceeded 5MB limit")

type Client struct {
	httpClient *http.Client
	baseURL    string
	login      string
	apiKey     string
	userAgent  string
	reqGap     time.Duration

	// mu guards ONLY lastReq (the rate-limit accounting) — Get never holds
	// it across sleeps or network execution. Every other field is fixed
	// after construction; tests only retune knobs (reqGap, backoffs) before
	// first use, never concurrently.
	lastReq time.Time
	mu      sync.Mutex

	backoffs []time.Duration // wait before retry N; shortened in tests
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
	login := os.Getenv("DANBOORU_LOGIN")
	apiKey := os.Getenv("DANBOORU_API_KEY")
	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    defaultBaseURL,
		login:      login,
		apiKey:     apiKey,
		userAgent:  defaultUserAgent,
		reqGap:     resolveReqGap(login != "" && apiKey != ""),
		backoffs:   []time.Duration{firstRetryWait, secondRetryWait},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// resolveReqGap picks the default request gap tier: DANBOORU_REQ_GAP_MS
// when set and parseable (0 valid), else 500ms when both credentials are
// present, else the anonymous 1500ms. Invalid or negative values warn once
// and keep the tier default.
func resolveReqGap(credentialed bool) time.Duration {
	if raw, ok := os.LookupEnv("DANBOORU_REQ_GAP_MS"); ok {
		if ms, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
		log.Printf("[WARN] invalid DANBOORU_REQ_GAP_MS %q, keeping the default request gap", raw)
	}
	if credentialed {
		return credentialedGap
	}
	return anonymousReqGap
}

// Get is the single choke point for all outbound requests: rate-limit wait,
// context cancellation, UA and credential injection, a 5MB body cap, bounded
// retry on transient failures (5xx, network errors, 429), and shrunken error
// text for non-2xx responses. A request that still fails after retries comes
// back wrapped in *upstreamError (errors.Is(err, app.ErrUpstreamUnavailable)
// is true); a 4xx is a definitive answer from a reachable upstream and
// surfaces unwrapped after one attempt. Concurrency contract: the rate limit
// keeps its accounting under c.mu but sleeps unlocked (waitTurn); network
// execution (retries, backoffs, Retry-After waits) also runs unlocked.
func (c *Client) Get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if params == nil {
		params = make(url.Values)
	}

	// Rate limiting: interruptible via context timeout/cancel.
	if err := c.waitTurn(ctx); err != nil {
		return nil, err
	}

	// Credentials are injected into a copy: mutating the caller's map
	// would leak them into the next call when params are reused.
	if c.login != "" && c.apiKey != "" {
		signed := make(url.Values, len(params)+2)
		for k, v := range params {
			signed[k] = v
		}
		signed.Set("login", c.login)
		signed.Set("api_key", c.apiKey)
		params = signed
	}

	reqURL := fmt.Sprintf("%s%s", c.baseURL, path)
	if encoded := params.Encode(); encoded != "" {
		reqURL = fmt.Sprintf("%s?%s", reqURL, encoded)
	}

	// Network execution runs with NO lock held: one caller's retries,
	// backoff sleeps, and Retry-After waits must not block another
	// caller's rate-limit accounting.
	return c.doWithRetry(ctx, reqURL)
}

// waitTurn blocks until this caller may issue the next outbound request,
// keeping consecutive request starts at least reqGap apart. Accounting
// (reading and stamping lastReq) happens under c.mu; the sleep happens with
// the lock released, so a caller parked here never delays another caller's
// accounting. The re-check loop preserves the global gap when
// several goroutines wake from their sleeps at once: only the one that finds
// the gap elapsed stamps lastReq and proceeds, the rest recompute their
// wait. Context cancellation aborts with ctx.Err().
func (c *Client) waitTurn(ctx context.Context) error {
	for {
		c.mu.Lock()
		elapsed := time.Since(c.lastReq)
		if elapsed >= c.reqGap {
			c.lastReq = time.Now()
			c.mu.Unlock()
			return nil
		}
		wait := c.reqGap - elapsed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// fetchOutcome is one HTTP attempt's raw result before retry
// classification. err carries the transport or body-read error; fatal
// marks failures a retry cannot fix (request build, size cap).
type fetchOutcome struct {
	body        []byte
	status      int
	contentType string
	retryAfter  string
	err         error
	fatal       bool
}

// upstreamError marks a failure worth retrying (5xx, network error,
// 429/421). When retries are exhausted the last error is returned wrapped in
// it, so errors.Is(err, app.ErrUpstreamUnavailable) tells the tools layer to
// surface the uniform upstream-unavailable result: a 4xx is a definitive
// answer from a reachable upstream (bad credentials, unknown query, contract
// change) and never satisfies the sentinel.
type upstreamError struct{ err error }

func (e *upstreamError) Error() string { return e.err.Error() }
func (e *upstreamError) Unwrap() error { return e.err }

// Is matches only the app.ErrUpstreamUnavailable sentinel, keeping
// errors.Is against the wrapped cause working through Unwrap.
func (e *upstreamError) Is(target error) bool { return target == app.ErrUpstreamUnavailable }

// doWithRetry runs the request attempt loop: up to len(backoffs) retries on
// 5xx, network errors, and 429/421, waiting the backoff schedule between
// attempts (a parseable Retry-After, capped, replaces the first wait). Context
// cancellation returns immediately without retrying. Non-retryable 4xx and
// fatal errors return after one attempt. When retries are exhausted on a
// retryable class the last error comes back wrapped in *upstreamError; a 4xx
// and a cancellation surface unwrapped.
func (c *Client) doWithRetry(ctx context.Context, reqURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		out := c.doOnce(ctx, reqURL)
		var retryAfter time.Duration
		switch {
		case out.err != nil:
			if out.fatal || ctx.Err() != nil {
				return nil, out.err
			}
			lastErr = &upstreamError{out.err} // network error: retryable
		case out.status == http.StatusOK:
			return out.body, nil
		case out.status == http.StatusTooManyRequests || out.status == 421:
			retryAfter = retryAfterDuration(out.retryAfter)
			lastErr = &upstreamError{rateLimitedError(out.status, retryAfter)}
		case out.status >= 500:
			lastErr = &upstreamError{httpStatusError(out.status, out.contentType, out.body)}
		default: // non-retryable 4xx
			return nil, httpStatusError(out.status, out.contentType, out.body)
		}
		if attempt >= len(c.backoffs) {
			return nil, lastErr // retries exhausted
		}
		wait := c.backoffs[attempt]
		if attempt == 0 && retryAfter > 0 {
			wait = retryAfter
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// doOnce performs one request/response cycle: context binding, UA
// injection, and the 5MB body cap (overflow is fatal for retries). Every
// error it returns is sanitized (see sanitizeTransportError) — this is the
// single point where transport errors are created, so nothing downstream
// can see a credentialed URL.
func (c *Client) doOnce(ctx context.Context, reqURL string) fetchOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fetchOutcome{err: sanitizeTransportError(err), fatal: true}
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fetchOutcome{err: sanitizeTransportError(err)}
	}
	defer resp.Body.Close()

	// Read at most 5MB + 1 byte to detect overflow
	limitedReader := io.LimitReader(resp.Body, maxResponseBody+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return fetchOutcome{err: sanitizeTransportError(err)}
	}
	if len(body) > maxResponseBody {
		return fetchOutcome{err: sanitizeTransportError(errResponseBodyTooLarge), fatal: true}
	}
	return fetchOutcome{
		body:        body,
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		retryAfter:  resp.Header.Get("Retry-After"),
	}
}

// sanitizeTransportError strips the request URL's query string and userinfo
// from a transport error before it leaves the api layer. The request URL
// carries the injected login/api_key credentials, and error text flows
// verbatim into MCP tool error payloads (agent context, session logs), so a
// timeout or refused connection must not leak them. The rewritten *url.Error
// keeps Op, Err, and the scheme/host/path of the request — the message stays
// diagnosable and errors.Is(err, context.Canceled / DeadlineExceeded) still
// matches through the preserved cause chain. Values that are not *url.Error
// (directly or wrapped) pass through untouched; when the URL string cannot
// be re-parsed, everything after the first '?' is cut so credentials still
// cannot ride the text.
func sanitizeTransportError(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	u, perr := url.Parse(ue.URL)
	if perr != nil {
		if i := strings.IndexByte(ue.URL, '?'); i >= 0 {
			return &url.Error{Op: ue.Op, URL: ue.URL[:i], Err: ue.Err}
		}
		return err
	}
	u.RawQuery = ""
	u.ForceQuery = false
	u.User = nil
	return &url.Error{Op: ue.Op, URL: u.String(), Err: ue.Err}
}

// httpStatusError builds the shrunken error for a non-2xx response: an
// HTML page with a title renders as "danbooru 503 (Downbooru)" (the live
// Downbooru outage shape), everything else as "danbooru <status>:
// <summary>".
func httpStatusError(status int, contentType string, body []byte) error {
	if isHTMLBody(contentType) {
		if title := htmlTitle(body); title != "" {
			return fmt.Errorf("danbooru %d (%s)", status, title)
		}
	}
	return fmt.Errorf("danbooru %d: %s", status, summarizeErrorBody(contentType, body))
}

// rateLimitedError renders the 429/421 error, carrying the parsed
// Retry-After delay when the header was present.
func rateLimitedError(status int, retryAfter time.Duration) error {
	if retryAfter > 0 {
		return fmt.Errorf("rate limited (danbooru %d), retry-after %s", status, retryAfter.Truncate(time.Millisecond))
	}
	return fmt.Errorf("rate limited (danbooru %d)", status)
}

// summarizeErrorBody shrinks a non-2xx response body into a short error
// snippet: an HTML page collapses to its <title> text (the first 100 bytes
// when the page has no parseable title), any other body is truncated to
// 300 bytes. Live evidence: a 503 maintenance page measures 1864-2853
// bytes, which is pure noise inside an error string.
func summarizeErrorBody(contentType string, body []byte) string {
	if isHTMLBody(contentType) {
		if title := htmlTitle(body); title != "" {
			return title
		}
		return truncateBytes(body, htmlFallbackBytes)
	}
	return truncateBytes(body, maxErrorBodyBytes)
}

// isHTMLBody reports whether a Content-Type header names HTML (the
// parameter-bearing "text/html; charset=utf-8" included).
func isHTMLBody(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/html")
}

// htmlTitleRe matches an HTML <title> element; case-insensitive and
// dot-matching because error pages carry attributes and newlines freely.
var htmlTitleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// htmlTitle extracts the trimmed text of the first <title> element, or ""
// when the body has none.
func htmlTitle(body []byte) string {
	m := htmlTitleRe.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// truncateBytes returns at most the first n bytes of body as a string.
func truncateBytes(body []byte, n int) string {
	if len(body) <= n {
		return string(body)
	}
	return string(body[:n])
}

// retryAfterDuration parses a Retry-After header value (delay-seconds or
// HTTP-date) capped at retryAfterCap. Absent, invalid, negative, and
// past-date values yield 0 so the default backoff schedule applies.
func retryAfterDuration(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return capDuration(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return capDuration(time.Until(t))
	}
	return 0
}

// capDuration clamps a positive Retry-After delay at the cap, mapping
// non-positive values to 0.
func capDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > retryAfterCap {
		return retryAfterCap
	}
	return d
}

// FetchTags searches tags by name pattern. category is a Danbooru category
// id; the API rejects names, so -1 (no filter) is the only way to omit it.
func (c *Client) FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool, category int) ([]byte, error) {
	q := make(url.Values)
	q.Set("search[name_matches]", fmt.Sprintf("*%s*", namePattern))
	if orderByCount {
		q.Set("search[order]", "count")
	}
	if category >= 0 {
		q.Set("search[category]", strconv.Itoa(category))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.Get(ctx, "/tags.json", q)
}

// FetchTagExact fetches the exact row for one tag name.
func (c *Client) FetchTagExact(ctx context.Context, name string) ([]byte, error) {
	q := make(url.Values)
	q.Set("search[name]", name)
	q.Set("limit", "1")
	return c.Get(ctx, "/tags.json", q)
}

// FetchTagsByNames fetches the exact rows for multiple tag names in ONE
// request: search[name_normalize] accepts a comma-separated list and
// canonicalizes each entry (case, spacing), so the response carries at most
// one exact row per requested name. limit is set to the name count because
// the default page size would silently truncate longer lists.
func (c *Client) FetchTagsByNames(ctx context.Context, names []string) ([]byte, error) {
	q := make(url.Values)
	q.Set("search[name_normalize]", strings.Join(names, ","))
	q.Set("limit", strconv.Itoa(len(names)))
	return c.Get(ctx, "/tags.json", q)
}

// FetchRelated fetches the co-occurrence table for one tag.
func (c *Client) FetchRelated(ctx context.Context, tag string) ([]byte, error) {
	q := make(url.Values)
	q.Set("query", tag)
	return c.Get(ctx, "/related_tag.json", q)
}

// FetchAlias looks up active tag aliases whose antecedent matches name exactly.
func (c *Client) FetchAlias(ctx context.Context, name string) ([]byte, error) {
	q := make(url.Values)
	q.Set("search[antecedent_name]", name)
	q.Set("search[status]", "active")
	q.Set("limit", "1")
	return c.Get(ctx, "/tag_aliases.json", q)
}

// FetchWiki queries wiki pages by exact title, or by substring match on the
// multilingual other_names list when title is empty. Deleted pages are
// excluded either way.
func (c *Client) FetchWiki(ctx context.Context, title, otherNames string, limit int) ([]byte, error) {
	q := make(url.Values)
	if title != "" {
		q.Set("search[title]", title)
	} else {
		q.Set("search[other_names_match]", "*"+otherNames+"*")
	}
	q.Set("search[is_deleted]", "false")
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.Get(ctx, "/wiki_pages.json", q)
}

// FetchPosts sends tags verbatim; no layer of this server injects a rating
// filter — pinning rating:<x> is the caller's opt-in (see AGENTS.md).
func (c *Client) FetchPosts(ctx context.Context, tags string, limit int) ([]byte, error) {
	q := make(url.Values)
	q.Set("tags", tags)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.Get(ctx, "/posts.json", q)
}

// FetchPostCount asks Danbooru how many posts match tags without fetching
// any post: one request to /counts/posts.json, which answers uncapped totals
// anonymously. The tags go out verbatim like FetchPosts (no layer injects a
// rating filter); an empty tags omits the parameter and counts every post on
// the site. The body is returned raw ({"counts":{"posts":N}}, or null in
// place of N when Danbooru's count timed out) because only the service layer
// can tell that null from a 0.
func (c *Client) FetchPostCount(ctx context.Context, tags string) ([]byte, error) {
	q := make(url.Values)
	if tags != "" {
		q.Set("tags", tags)
	}
	return c.Get(ctx, "/counts/posts.json", q)
}
