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

	"danbooru-tag-mcp/internal/cache"
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

// defaultCacheTTLs maps cacheable Danbooru endpoints to their fresh-entry
// TTLs. /posts.json is deliberately absent: order:random sampling semantics
// and the profile's whole-population claims make replayed post pages lies,
// so posts never touch the cache Store at all.
var defaultCacheTTLs = map[string]time.Duration{
	"/related_tag.json": 30 * 24 * time.Hour,
	"/wiki_pages.json":  7 * 24 * time.Hour,
	"/tag_aliases.json": 7 * 24 * time.Hour,
	"/tags.json":        24 * time.Hour,
}

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
	// it across cache reads, sleeps, or network execution. Every other
	// field is fixed after construction; tests only retune knobs (reqGap,
	// backoffs, TTLs) before first use, never concurrently.
	lastReq time.Time
	mu      sync.Mutex

	store     *cache.Store // nil = no caching (unit-test default)
	cacheTTLs map[string]time.Duration
	backoffs  []time.Duration // wait before retry N; shortened in tests
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

// WithCache enables the persistent response cache rooted at dir (entries
// under dir/v1/). When the location cannot be opened the client keeps
// running cache-less with one stderr warning — a broken cache must never
// break requests.
func WithCache(dir string) Option {
	return func(c *Client) {
		store, err := cache.Open(dir)
		if err != nil {
			log.Printf("[WARN] cache disabled: %v", err)
			return
		}
		c.store = store
	}
}

func NewClient(opts ...Option) *Client {
	login := os.Getenv("DANBOORU_LOGIN")
	apiKey := os.Getenv("DANBOORU_API_KEY")
	ttls := make(map[string]time.Duration, len(defaultCacheTTLs))
	for p, d := range defaultCacheTTLs {
		ttls[p] = d
	}
	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    defaultBaseURL,
		login:      login,
		apiKey:     apiKey,
		userAgent:  defaultUserAgent,
		reqGap:     resolveReqGap(login != "" && apiKey != ""),
		store:      nil,
		cacheTTLs:  ttls,
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

// Get is the single choke point for all outbound requests: cache lookup,
// rate-limit wait, context cancellation, UA and credential injection, a 5MB
// body cap, bounded retry on transient failures (5xx, network errors, 429),
// and stale-cache fallback when retries are exhausted on a retryable failure
// (a 4xx is a definitive answer from a reachable upstream and always
// surfaces). Concurrency contract: the cache read runs before any lock is
// taken (Store is safe for concurrent use, so a fresh cache hit never
// queues behind a caller parked in the rate limiter or the network); the
// rate limit keeps its accounting under c.mu but sleeps unlocked (waitTurn);
// network execution (retries, backoffs, Retry-After waits) and the cache
// write also run unlocked — Store serializes its own writers. A fresh cache
// hit returns before the rate-limit wait and is byte-identical to a network
// response; only the stale fallback sets Result.Stale. A refresh request
// (cache.WithRefresh) skips the fresh-hit shortcut: the network path runs
// unchanged and a successful write restarts the entry's TTL clock.
func (c *Client) Get(ctx context.Context, path string, params url.Values) (cache.Result, error) {
	if params == nil {
		params = make(url.Values)
	}

	// Cache key is computed BEFORE credential injection so credentials
	// never reach disk and anonymous/authenticated callers share entries.
	// params.Encode sorts keys, so the key is deterministic. The read is
	// lock-free on purpose: holding c.mu here would let a slow network
	// caller starve a 1ms cache hit (the lock-granularity regression this
	// layout pins).
	ttl, pathCacheable := c.cacheTTLs[path]
	refresh := cache.RefreshRequested(ctx)
	var cacheKey string
	var staleBody []byte
	var staleAt time.Time
	if pathCacheable && c.store != nil {
		cacheKey = path + "?" + params.Encode()
		if body, storedAt, ok := c.store.Get(cacheKey); ok {
			if time.Since(storedAt) < ttl && !refresh {
				return cache.Result{Body: body}, nil
			}
			// Expired, or a refresh forcing the network path: keep the
			// entry around as the stale fallback, refetch live.
			staleBody, staleAt = body, storedAt
		}
	}

	// Rate limiting: interruptible via context timeout/cancel.
	if err := c.waitTurn(ctx); err != nil {
		return cache.Result{}, err
	}

	// Credentials are injected into a copy: mutating the caller's map
	// would leak them into the next call's cache key when params are
	// reused, splitting the cache by caller.
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
	// caller's cache hit or rate-limit accounting.
	res, err := c.doWithRetry(ctx, reqURL)
	if err != nil {
		// Context cancellation returns immediately: never stale-serve a
		// request the caller abandoned.
		if ctx.Err() != nil {
			return cache.Result{}, err
		}
		// Stale fallback only for unreachable-upstream failures (5xx,
		// network, 429/421, retries exhausted): a 4xx is a definitive
		// answer from a reachable upstream and must surface.
		var te *transientError
		if staleBody != nil && errors.As(err, &te) {
			log.Printf("[WARN] danbooru unreachable (%v), serving stale %s (age %s)", err, path, time.Since(staleAt).Round(time.Second))
			return cache.Result{Body: staleBody, Stale: true, CachedAt: staleAt}, nil
		}
		return cache.Result{}, err
	}

	// Empty-array bodies are dead-end searches: cheap to refetch, and
	// caching them would hide new tags for a whole TTL. Unlocked on
	// purpose: Store.writeMu serializes concurrent writers.
	if pathCacheable && c.store != nil && !isEmptyJSONList(res.Body) {
		if err := c.store.Put(cacheKey, res.Body); err != nil {
			log.Printf("[WARN] cache write failed for %s: %v", path, err)
		}
	}
	return res, nil
}

// waitTurn blocks until this caller may issue the next outbound request,
// keeping consecutive request starts at least reqGap apart. Accounting
// (reading and stamping lastReq) happens under c.mu; the sleep happens with
// the lock released, so a caller parked here never delays another caller's
// accounting or cache hit. The re-check loop preserves the global gap when
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

// isEmptyJSONList reports whether body is exactly "[]" modulo whitespace.
func isEmptyJSONList(body []byte) bool {
	return strings.TrimSpace(string(body)) == "[]"
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

// transientError marks a failure worth retrying (5xx, network error,
// 429/421) — the only class eligible for the stale-cache fallback: a 4xx is
// a definitive answer from a reachable upstream (bad credentials, unknown
// query, contract change), so it must surface instead of replaying old data.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// doWithRetry runs the request attempt loop: up to len(backoffs) retries on
// 5xx, network errors, and 429/421, waiting the backoff schedule between
// attempts (a parseable Retry-After, capped, replaces the first wait). Context
// cancellation returns immediately without retrying. Non-retryable 4xx and
// fatal errors return after one attempt. Retryable failures come back
// wrapped in *transientError so Get can gate the stale fallback on them.
func (c *Client) doWithRetry(ctx context.Context, reqURL string) (cache.Result, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		out := c.doOnce(ctx, reqURL)
		var retryAfter time.Duration
		switch {
		case out.err != nil:
			if out.fatal || ctx.Err() != nil {
				return cache.Result{}, out.err
			}
			lastErr = &transientError{out.err} // network error: retryable
		case out.status == http.StatusOK:
			return cache.Result{Body: out.body}, nil
		case out.status == http.StatusTooManyRequests || out.status == 421:
			retryAfter = retryAfterDuration(out.retryAfter)
			lastErr = &transientError{rateLimitedError(out.status, retryAfter)}
		case out.status >= 500:
			lastErr = &transientError{httpStatusError(out.status, out.contentType, out.body)}
		default: // non-retryable 4xx
			return cache.Result{}, httpStatusError(out.status, out.contentType, out.body)
		}
		if attempt >= len(c.backoffs) {
			return cache.Result{}, lastErr // retries exhausted
		}
		wait := c.backoffs[attempt]
		if attempt == 0 && retryAfter > 0 {
			wait = retryAfter
		}
		select {
		case <-ctx.Done():
			return cache.Result{}, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// doOnce performs one request/response cycle: context binding, UA
// injection, and the 5MB body cap (overflow is fatal for retries).
func (c *Client) doOnce(ctx context.Context, reqURL string) fetchOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fetchOutcome{err: err, fatal: true}
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fetchOutcome{err: err}
	}
	defer resp.Body.Close()

	// Read at most 5MB + 1 byte to detect overflow
	limitedReader := io.LimitReader(resp.Body, maxResponseBody+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return fetchOutcome{err: err}
	}
	if len(body) > maxResponseBody {
		return fetchOutcome{err: errResponseBodyTooLarge, fatal: true}
	}
	return fetchOutcome{
		body:        body,
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		retryAfter:  resp.Header.Get("Retry-After"),
	}
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
func (c *Client) FetchTags(ctx context.Context, namePattern string, limit int, orderByCount bool, category int) (cache.Result, error) {
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
func (c *Client) FetchTagExact(ctx context.Context, name string) (cache.Result, error) {
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
func (c *Client) FetchTagsByNames(ctx context.Context, names []string) (cache.Result, error) {
	q := make(url.Values)
	q.Set("search[name_normalize]", strings.Join(names, ","))
	q.Set("limit", strconv.Itoa(len(names)))
	return c.Get(ctx, "/tags.json", q)
}

// FetchRelated fetches the co-occurrence table for one tag.
func (c *Client) FetchRelated(ctx context.Context, tag string) (cache.Result, error) {
	q := make(url.Values)
	q.Set("query", tag)
	return c.Get(ctx, "/related_tag.json", q)
}

// FetchAlias looks up active tag aliases whose antecedent matches name exactly.
func (c *Client) FetchAlias(ctx context.Context, name string) (cache.Result, error) {
	q := make(url.Values)
	q.Set("search[antecedent_name]", name)
	q.Set("search[status]", "active")
	q.Set("limit", "1")
	return c.Get(ctx, "/tag_aliases.json", q)
}

// FetchWiki queries wiki pages by exact title, or by substring match on the
// multilingual other_names list when title is empty. Deleted pages are
// excluded either way.
func (c *Client) FetchWiki(ctx context.Context, title, otherNames string, limit int) (cache.Result, error) {
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
func (c *Client) FetchPosts(ctx context.Context, tags string, limit int) (cache.Result, error) {
	q := make(url.Values)
	q.Set("tags", tags)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.Get(ctx, "/posts.json", q)
}
