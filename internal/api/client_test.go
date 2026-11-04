package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"danbooru-tag-mcp/internal/cache"
)

func newTestClient(serverURL string) *Client {
	return NewClient(
		WithBaseURL(serverURL),
		WithReqGap(0), // no real throttling in tests
	)
}

// newFastTestClient disables retry backoff waits too, so retry tests run
// at request speed instead of 1s/3s per retry.
func newFastTestClient(serverURL string) *Client {
	cli := newTestClient(serverURL)
	cli.backoffs = []time.Duration{0, 0}
	return cli
}

// newCachedTestClient is a fast client with the cache rooted at cacheDir.
func newCachedTestClient(t *testing.T, serverURL, cacheDir string) *Client {
	t.Helper()
	cli := newFastTestClient(serverURL)
	WithCache(cacheDir)(cli)
	return cli
}

// countingServer serves body on path and counts requests via hits.
func countingServer(t *testing.T, path, body string, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		atomic.AddInt32(hits, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
}

func TestGet_InjectsAuthParams(t *testing.T) {
	var gotLogin, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLogin = r.URL.Query().Get("login")
		gotAPIKey = r.URL.Query().Get("api_key")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := NewClient(
		WithBaseURL(srv.URL),
		WithReqGap(0),
		WithCredentials("test_user", "test_key"),
	)

	_, err := cli.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotLogin != "test_user" || gotAPIKey != "test_key" {
		t.Errorf("expected auth params test_user/test_key, got %s/%s", gotLogin, gotAPIKey)
	}
}

func TestGet_SetsUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, _ = cli.Get(context.Background(), "/test", nil)

	if gotUA != defaultUserAgent {
		t.Errorf("expected UA %s, got %s", defaultUserAgent, gotUA)
	}
}

func TestGet_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/test", nil)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected rate limited error, got: %v", err)
	}
}

func TestGet_Throttled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(421)
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/test", nil)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected rate limited error on 421, got: %v", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("resource not found"))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/test", nil)
	if err == nil || !strings.Contains(err.Error(), "danbooru 404") {
		t.Fatalf("expected danbooru 404 error, got: %v", err)
	}
}

func TestGet_ResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// write 5MB + 1024 bytes
		w.Write(bytes.Repeat([]byte("a"), maxResponseBody+1024))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/test", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeded 5MB limit") {
		t.Fatalf("expected response size exceeded error, got: %v", err)
	}
}

// --- Step R2: error shrink + bounded retry ---

func TestSummarizeErrorBody_Behavior(t *testing.T) {
	htmlPage := "<!DOCTYPE html><html><head><title>Downbooru</title></head><body>maintenance</body></html>"
	longJSON := `{"error":"` + strings.Repeat("x", 1000) + `"}`
	cases := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{"html collapses to title", "text/html", htmlPage, "Downbooru"},
		{"html with charset still html", "text/html; charset=utf-8", htmlPage, "Downbooru"},
		{"html uppercase markup", "TEXT/HTML", "<HTML><HEAD><TITLE>Maintenance</TITLE></HEAD></HTML>", "Maintenance"},
		{
			"html without title falls back to 100 bytes",
			"text/html",
			"<html><body>" + strings.Repeat("y", 500) + "</body></html>",
			"<html><body>" + strings.Repeat("y", 100-12),
		},
		{"json truncated to 300 bytes", "application/json", longJSON, longJSON[:300]},
		{"short json unchanged", "application/json", `{"a":1}`, `{"a":1}`},
		{"empty body stays empty", "application/json", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeErrorBody(tc.contentType, []byte(tc.body)); got != tc.want {
				t.Errorf("summarizeErrorBody(%q): got %.80q want %.80q", tc.contentType, got, tc.want)
			}
		})
	}
}

func TestRetryAfterDuration_Behavior(t *testing.T) {
	futureDate := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	pastDate := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	cases := []struct {
		name string
		raw  string
		want time.Duration // exact match, except futureDate which is a range
	}{
		{"seconds", "5", 5 * time.Second},
		{"zero is zero", "0", 0},
		{"negative is zero", "-3", 0},
		{"capped at 30s", "3600", retryAfterCap},
		{"garbage is zero", "soon", 0},
		{"empty is zero", "", 0},
		{"past date is zero", pastDate, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryAfterDuration(tc.raw); got != tc.want {
				t.Errorf("retryAfterDuration(%q): got %v want %v", tc.raw, got, tc.want)
			}
		})
	}
	t.Run("http date in the future", func(t *testing.T) {
		got := retryAfterDuration(futureDate)
		if got <= 0 || got > 2*time.Second {
			t.Errorf("retryAfterDuration(future date): got %v, want in (0, 2s]", got)
		}
	})
}

func TestGet_Retries5xxThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("transient"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	res, err := cli.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("expected the retry to recover, got: %v", err)
	}
	if string(res.Body) != "ok" {
		t.Errorf("unexpected body: %q", res.Body)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("expected 2 requests (1 fail + 1 retry), got %d", n)
	}
}

func TestGet_RetriesExhaustedErrors(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("still down"))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/test", nil)
	if err == nil || !strings.Contains(err.Error(), "danbooru 500") {
		t.Fatalf("expected danbooru 500 error after retries, got: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1+maxRetries {
		t.Errorf("expected %d attempts (1 + %d retries), got %d", 1+maxRetries, maxRetries, n)
	}
}

func TestGet_ContextCancelAbortsWithoutRetry(t *testing.T) {
	var hits int32
	var once sync.Once
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		once.Do(func() { close(started) })
		<-r.Context().Done() // hold the request until the client gives up
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := cli.Get(ctx, "/test", nil)
	<-started
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline error, got: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("cancellation must not retry: expected 1 request, got %d", n)
	}
}

func TestGet_HTMLErrorCollapsedToTitle(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		// Live outage shape: a maintenance page measured at 1864+ bytes.
		w.Write([]byte("<!DOCTYPE html><html><head><title>Downbooru</title></head><body>" + strings.Repeat("maintenance padding ", 100) + "</body></html>"))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/test", nil)
	if err == nil || err.Error() != "danbooru 503 (Downbooru)" {
		t.Fatalf("expected 'danbooru 503 (Downbooru)', got: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1+maxRetries {
		t.Errorf("5xx must be retried before erroring: expected %d attempts, got %d", 1+maxRetries, n)
	}
}

func TestGet_ErrorBodyTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/test", nil)
	if err == nil {
		t.Fatal("expected 422 error")
	}
	msg := err.Error()
	if !strings.Contains(msg, strings.Repeat("x", maxErrorBodyBytes)) {
		t.Errorf("error must carry the first %d bytes", maxErrorBodyBytes)
	}
	if strings.Contains(msg, strings.Repeat("x", maxErrorBodyBytes+1)) {
		t.Errorf("error must not carry more than %d body bytes: %d chars", maxErrorBodyBytes, len(msg))
	}
}

func TestGet_RetryAfterHonored(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL) // zeroed backoffs: only Retry-After can delay
	start := time.Now()
	_, err := cli.Get(context.Background(), "/test", nil)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected rate limited error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "retry-after 1s") {
		t.Errorf("error must mention the Retry-After delay: %v", err)
	}
	// The first backoff is replaced by Retry-After: 1s, so a sub-second
	// total proves the header was ignored... and >= 1s proves it was used.
	if elapsed < time.Second {
		t.Errorf("Retry-After must gate the first retry: elapsed %v < 1s", elapsed)
	}
	if n := atomic.LoadInt32(&hits); n != 1+maxRetries {
		t.Errorf("429 must be retried: expected %d attempts, got %d", 1+maxRetries, n)
	}
}

// --- Step R4b: request gap tiers ---

func TestNewClient_ReqGapResolution(t *testing.T) {
	cases := []struct {
		name            string
		login, key, gap string
		want            time.Duration
	}{
		{"anonymous default", "", "", "", anonymousReqGap},
		{"credential tier", "u", "k", "", credentialedGap},
		{"env override wins over tier", "u", "k", "250", 250 * time.Millisecond},
		{"env zero is valid", "u", "k", "0", 0},
		{"env invalid keeps tier", "u", "k", "abc", credentialedGap},
		{"env negative keeps tier", "", "", "-5", anonymousReqGap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DANBOORU_LOGIN", tc.login)
			t.Setenv("DANBOORU_API_KEY", tc.key)
			t.Setenv("DANBOORU_REQ_GAP_MS", tc.gap)
			if got := NewClient().reqGap; got != tc.want {
				t.Errorf("reqGap: got %v want %v", got, tc.want)
			}
		})
	}
}

// --- Step R1 core: response cache with stale fallback ---

func TestDefaultCacheTTLs_Pinned(t *testing.T) {
	for path, want := range map[string]time.Duration{
		"/related_tag.json": 30 * 24 * time.Hour,
		"/wiki_pages.json":  7 * 24 * time.Hour,
		"/tag_aliases.json": 7 * 24 * time.Hour,
		"/tags.json":        24 * time.Hour,
	} {
		if got := defaultCacheTTLs[path]; got != want {
			t.Errorf("TTL for %s: got %v want %v", path, got, want)
		}
	}
	if _, ok := defaultCacheTTLs["/posts.json"]; ok {
		t.Error("/posts.json must never have a TTL entry")
	}
}

func TestGet_CacheFreshHitIsTransparent(t *testing.T) {
	var hits int32
	body := `[{"id":1,"name":"arm_up"}]`
	srv := countingServer(t, "/tags.json", body, &hits)
	defer srv.Close()

	cli := newCachedTestClient(t, srv.URL, t.TempDir())
	q := url.Values{"search[name]": []string{"arm_up"}}

	res1, err := cli.Get(context.Background(), "/tags.json", q)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	res2, err := cli.Get(context.Background(), "/tags.json", q)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("fresh hit must not hit the network: expected 1 request, got %d", n)
	}
	if string(res2.Body) != string(res1.Body) || string(res2.Body) != body {
		t.Errorf("cached body must be byte-identical: got %q want %q", res2.Body, body)
	}
	if res2.Stale || !res2.CachedAt.IsZero() {
		t.Errorf("fresh hit must carry no markers: Stale=%v CachedAt=%v", res2.Stale, res2.CachedAt)
	}
}

func TestGet_CacheHitSkipsRateLimitWait(t *testing.T) {
	var hits int32
	srv := countingServer(t, "/tags.json", `[{"id":1}]`, &hits)
	defer srv.Close()

	cli := newCachedTestClient(t, srv.URL, t.TempDir())
	q := url.Values{"search[name]": []string{"arm_up"}}
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}
	// A network path would now stall for an hour in the rate limiter, and
	// a canceled context would abort inside it. Only the pre-rate-limit
	// cache short-circuit can answer — deterministic, no timing.
	cli.reqGap = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := cli.Get(ctx, "/tags.json", q)
	if err != nil {
		t.Fatalf("canceled ctx must still be served from the cache: %v", err)
	}
	if string(res.Body) != `[{"id":1}]` {
		t.Errorf("unexpected cached body: %q", res.Body)
	}
}

func TestGet_CacheKeyIgnoresCredentials(t *testing.T) {
	var hits int32
	srv := countingServer(t, "/tags.json", "shared", &hits)
	defer srv.Close()

	dir := t.TempDir()
	alice := NewClient(WithBaseURL(srv.URL), WithReqGap(0), WithCache(dir), WithCredentials("alice", "k1"))
	bob := NewClient(WithBaseURL(srv.URL), WithReqGap(0), WithCache(dir), WithCredentials("bob", "k2"))
	q := url.Values{"search[name]": []string{"arm_up"}}

	if _, err := alice.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("alice Get: %v", err)
	}
	res, err := bob.Get(context.Background(), "/tags.json", q)
	if err != nil {
		t.Fatalf("bob Get: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("credentials must not split the cache key: expected 1 request, got %d", n)
	}
	if string(res.Body) != "shared" {
		t.Errorf("bob must see alice's cached body, got %q", res.Body)
	}
}

func TestGet_CacheTTLExpiryRefetches(t *testing.T) {
	var hits int32
	srv := countingServer(t, "/tags.json", `[{"id":1}]`, &hits)
	defer srv.Close()

	cli := newCachedTestClient(t, srv.URL, t.TempDir())
	cli.cacheTTLs["/tags.json"] = 20 * time.Millisecond
	q := url.Values{"search[name]": []string{"arm_up"}}

	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	time.Sleep(40 * time.Millisecond) // entry expires
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("expired entry must refetch: expected 2 requests, got %d", n)
	}
	// The refetch re-cached the entry, so the clock restarted.
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("third Get: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("re-cached entry must be served fresh: expected 2 requests, got %d", n)
	}
}

func TestGet_PostsNeverCached(t *testing.T) {
	var hits int32
	srv := countingServer(t, "/posts.json", `[{"id":1}]`, &hits)
	defer srv.Close()

	dir := t.TempDir()
	cli := newCachedTestClient(t, srv.URL, dir)
	q := url.Values{"tags": []string{"arm_up"}}
	for i := 0; i < 2; i++ {
		if _, err := cli.Get(context.Background(), "/posts.json", q); err != nil {
			t.Fatalf("Get %d: %v", i+1, err)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("posts must always go to the network: expected 2 requests, got %d", n)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "v1"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("/posts.json must never write a cache entry, found %d files", len(entries))
	}
}

func TestGet_EmptyArrayBodyNotCached(t *testing.T) {
	var hits int32
	srv := countingServer(t, "/tags.json", " [] ", &hits)
	defer srv.Close()

	dir := t.TempDir()
	cli := newCachedTestClient(t, srv.URL, dir)
	q := url.Values{"search[name]": []string{"no_such_tag"}}
	for i := 0; i < 2; i++ {
		if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
			t.Fatalf("Get %d: %v", i+1, err)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("dead-end searches must refetch: expected 2 requests, got %d", n)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "v1"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("empty-array bodies must never be cached, found %d files", len(entries))
	}
}

func TestGet_StaleServeAfterRetriesExhausted(t *testing.T) {
	var hits int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path == "/tags.json" {
			if down.Load() {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("<html><head><title>Downbooru</title></head></html>"))
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":1,"name":"arm_up"}]`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("<html><head><title>Downbooru</title></head></html>"))
	}))
	defer srv.Close()

	cli := newCachedTestClient(t, srv.URL, t.TempDir())
	cli.cacheTTLs["/tags.json"] = 20 * time.Millisecond
	q := url.Values{"search[name]": []string{"arm_up"}}
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}
	time.Sleep(40 * time.Millisecond) // entry expires
	down.Store(true)

	res, err := cli.Get(context.Background(), "/tags.json", q)
	if err != nil {
		t.Fatalf("expired entry must be served when retries exhaust, got: %v", err)
	}
	if !res.Stale {
		t.Error("fallback result must be marked Stale")
	}
	if res.CachedAt.IsZero() {
		t.Error("fallback result must carry CachedAt")
	}
	if string(res.Body) != `[{"id":1,"name":"arm_up"}]` {
		t.Errorf("stale body must be the cached one, got %q", res.Body)
	}
	// 1 priming request + 3 attempts (1 + 2 retries) during the outage.
	if n := atomic.LoadInt32(&hits); n != 4 {
		t.Errorf("expected retries before the stale fallback (4 requests total), got %d", n)
	}

	// No cached entry for this path: the shrunken error is the answer.
	_, err = cli.Get(context.Background(), "/tag_aliases.json", url.Values{"search[antecedent_name]": []string{"x"}})
	if err == nil || err.Error() != "danbooru 503 (Downbooru)" {
		t.Fatalf("cache miss during outage must yield the shrunken error, got: %v", err)
	}
}

func TestGet_NonRetryableErrorDoesNotStaleServe(t *testing.T) {
	// A 4xx is a definitive answer from a reachable upstream (bad query,
	// revoked credentials): an expired entry must NOT shadow it — only
	// unreachable-upstream failures fall back to stale data.
	var hits int32
	var reject atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if reject.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"error":"query rejected"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":1,"name":"arm_up"}]`))
	}))
	defer srv.Close()

	cli := newCachedTestClient(t, srv.URL, t.TempDir())
	cli.cacheTTLs["/tags.json"] = 20 * time.Millisecond
	q := url.Values{"search[name]": []string{"arm_up"}}
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}
	time.Sleep(40 * time.Millisecond) // entry expires
	reject.Store(true)

	res, err := cli.Get(context.Background(), "/tags.json", q)
	if err == nil || !strings.Contains(err.Error(), "danbooru 422") {
		t.Fatalf("the 4xx must surface, got result=%+v err=%v", res, err)
	}
	if res.Stale {
		t.Error("a 4xx must never be answered with stale cache data")
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("4xx must not retry: expected 2 requests total, got %d", n)
	}
}

func TestGet_StoreFailureFallsThroughToNetwork(t *testing.T) {
	var hits int32
	srv := countingServer(t, "/tags.json", `[{"id":1}]`, &hits)
	defer srv.Close()

	// A regular file where the cache directory should be: Open fails, the
	// client warns and keeps serving from the network.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("plant the blocker: %v", err)
	}
	cli := NewClient(WithBaseURL(srv.URL), WithReqGap(0), WithCache(blocker))
	cli.backoffs = []time.Duration{0, 0}
	if cli.store != nil {
		t.Fatal("WithCache must disable the store when Open fails")
	}
	q := url.Values{"search[name]": []string{"arm_up"}}
	for i := 0; i < 2; i++ {
		res, err := cli.Get(context.Background(), "/tags.json", q)
		if err != nil {
			t.Fatalf("Get %d must succeed without a cache: %v", i+1, err)
		}
		if string(res.Body) != `[{"id":1}]` {
			t.Errorf("unexpected body: %q", res.Body)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("cache-less client must always fetch: expected 2 requests, got %d", n)
	}
}

// --- Step R1 refresh control ---

func TestGet_RefreshBypassesFreshHitAndBumpsStoredAt(t *testing.T) {
	var hits int32
	srv := countingServer(t, "/tags.json", `[{"id":1}]`, &hits)
	defer srv.Close()

	cli := newCachedTestClient(t, srv.URL, t.TempDir())
	q := url.Values{"search[name]": []string{"arm_up"}}
	key := "/tags.json?" + q.Encode()

	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}
	_, at1, ok := cli.store.Get(key)
	if !ok {
		t.Fatal("primed entry missing")
	}

	// A plain second call is a fresh hit: no network, no markers.
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("plain second Get: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("fresh hit must stay off the network: expected 1 request, got %d", n)
	}

	// The refresh request bypasses the fresh hit and rewrites the entry, so
	// the TTL clock (stored_at) restarts.
	time.Sleep(20 * time.Millisecond) // separate the two stored_at timestamps
	res, err := cli.Get(cache.WithRefresh(context.Background()), "/tags.json", q)
	if err != nil {
		t.Fatalf("refresh Get: %v", err)
	}
	if res.Stale || !res.CachedAt.IsZero() {
		t.Errorf("a served refresh must carry no markers: Stale=%v CachedAt=%v", res.Stale, res.CachedAt)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("refresh must bypass the fresh hit: expected 2 requests, got %d", n)
	}
	_, at2, ok := cli.store.Get(key)
	if !ok {
		t.Fatal("refreshed entry missing")
	}
	if !at2.After(at1) {
		t.Errorf("refresh must restart the TTL clock: stored_at %v not after %v", at2, at1)
	}

	// The refreshed entry is fresh again: a plain call stays off the network.
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("post-refresh plain Get: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("refreshed entry must serve fresh: expected 2 requests, got %d", n)
	}
}

func TestGet_RefreshFailureStillStaleServes(t *testing.T) {
	var hits int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if down.Load() {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("<html><head><title>Downbooru</title></head></html>"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":1,"name":"arm_up"}]`))
	}))
	defer srv.Close()

	cli := newCachedTestClient(t, srv.URL, t.TempDir())
	q := url.Values{"search[name]": []string{"arm_up"}}
	if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}
	down.Store(true)

	// The entry is still FRESH, but a refresh that exhausts its retries must
	// still fall back to it, marked stale.
	res, err := cli.Get(cache.WithRefresh(context.Background()), "/tags.json", q)
	if err != nil {
		t.Fatalf("failed refresh must stale-serve, got: %v", err)
	}
	if !res.Stale || res.CachedAt.IsZero() {
		t.Errorf("fallback must be marked stale with cached_at: Stale=%v CachedAt=%v", res.Stale, res.CachedAt)
	}
	if string(res.Body) != `[{"id":1,"name":"arm_up"}]` {
		t.Errorf("stale body must be the cached one, got %q", res.Body)
	}
	// 1 priming request + 3 attempts (1 + 2 retries) during the outage.
	if n := atomic.LoadInt32(&hits); n != 4 {
		t.Errorf("expected retries before the stale fallback (4 requests total), got %d", n)
	}
}

func TestFetchTags_BuildsCorrectURL(t *testing.T) {
	var gotMatches, gotOrder, gotLimit, gotCategory string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMatches = r.URL.Query().Get("search[name_matches]")
		gotOrder = r.URL.Query().Get("search[order]")
		gotLimit = r.URL.Query().Get("limit")
		gotCategory = r.URL.Query().Get("search[category]")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	if _, err := cli.FetchTags(context.Background(), "blue_hair", 10, true, -1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMatches != "*blue_hair*" || gotOrder != "count" || gotLimit != "10" {
		t.Errorf("unexpected query params: matches=%s, order=%s, limit=%s", gotMatches, gotOrder, gotLimit)
	}
	// -1 means no filter: search[category] must stay absent (a literal -1
	// would be rejected by the API).
	if gotCategory != "" {
		t.Errorf("category -1 must omit search[category], got %q", gotCategory)
	}
}

func TestFetchTags_CategoryFilter(t *testing.T) {
	// The API only accepts numeric ids (verified live: "character" returns
	// empty while 4 works), so the client forwards the id verbatim and only
	// when set.
	var gotCategory, gotMatches, gotOrder, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCategory = r.URL.Query().Get("search[category]")
		gotMatches = r.URL.Query().Get("search[name_matches]")
		gotOrder = r.URL.Query().Get("search[order]")
		gotLimit = r.URL.Query().Get("limit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	if _, err := cli.FetchTags(context.Background(), "firefly", 10, true, 4); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCategory != "4" {
		t.Errorf("expected search[category]=4, got %q", gotCategory)
	}
	if gotMatches != "*firefly*" || gotOrder != "count" || gotLimit != "10" {
		t.Errorf("category must not disturb the other params: matches=%s order=%s limit=%s", gotMatches, gotOrder, gotLimit)
	}
}

func TestFetchTagExact_ReturnsSingle(t *testing.T) {
	var gotName, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = r.URL.Query().Get("search[name]")
		gotLimit = r.URL.Query().Get("limit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.FetchTagExact(context.Background(), "solo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotName != "solo" || gotLimit != "1" {
		t.Errorf("unexpected query params: name=%s, limit=%s", gotName, gotLimit)
	}
}

func TestFetchTagsByNames_BuildsQuery(t *testing.T) {
	var gotNames, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNames = r.URL.Query().Get("search[name_normalize]")
		gotLimit = r.URL.Query().Get("limit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.FetchTagsByNames(context.Background(), []string{"blue_hair", "1girl", "solo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// One request for the whole batch: comma-joined name_normalize plus a
	// limit matching the name count, so the default page size cannot
	// truncate the response.
	if gotNames != "blue_hair,1girl,solo" || gotLimit != "3" {
		t.Errorf("unexpected query params: name_normalize=%s, limit=%s", gotNames, gotLimit)
	}
}

func TestFetchAlias_BuildsCorrectURL(t *testing.T) {
	var gotName, gotStatus, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = r.URL.Query().Get("search[antecedent_name]")
		gotStatus = r.URL.Query().Get("search[status]")
		gotLimit = r.URL.Query().Get("limit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.FetchAlias(context.Background(), "sailor_suit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotName != "sailor_suit" || gotStatus != "active" || gotLimit != "1" {
		t.Errorf("unexpected query params: name=%s, status=%s, limit=%s", gotName, gotStatus, gotLimit)
	}
}

func TestFetchWiki_BuildsCorrectURL(t *testing.T) {
	cases := []struct {
		name                 string
		title, otherNames    string
		wantTitle, wantOther string
	}{
		{"by title", "firefly_(honkai:_star_rail)", "", "firefly_(honkai:_star_rail)", ""},
		{"by other names", "", "流萤", "", "*流萤*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotTitle, gotOther, gotDeleted, gotLimit string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotTitle = r.URL.Query().Get("search[title]")
				gotOther = r.URL.Query().Get("search[other_names_match]")
				gotDeleted = r.URL.Query().Get("search[is_deleted]")
				gotLimit = r.URL.Query().Get("limit")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
			}))
			defer srv.Close()

			cli := newTestClient(srv.URL)
			if _, err := cli.FetchWiki(context.Background(), tc.title, tc.otherNames, 5); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotTitle != tc.wantTitle || gotOther != tc.wantOther {
				t.Errorf("unexpected query: title=%q other=%q", gotTitle, gotOther)
			}
			if gotDeleted != "false" || gotLimit != "5" {
				t.Errorf("unexpected is_deleted=%q limit=%q", gotDeleted, gotLimit)
			}
		})
	}
}

func TestFetchPosts_DoesNotAddRatingFilter(t *testing.T) {
	var gotTags string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTags = r.URL.Query().Get("tags")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.FetchPosts(context.Background(), "1girl blue_hair", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(gotTags, "rating:") {
		t.Errorf("api layer must NOT inject rating filter, got tags: %s", gotTags)
	}
}
