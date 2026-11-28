package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"danbooru-tag-mcp/internal/app"
	"danbooru-tag-mcp/internal/service"
	"danbooru-tag-mcp/internal/tools"
	"danbooru-tag-mcp/internal/vocab"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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
	body, err := cli.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("expected the retry to recover, got: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("unexpected body: %q", body)
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

// TestGet_TransportErrorsOmitCredentials pins the credential-hygiene rule
// for transport failures: with credentials configured, an error from a
// request that fails mid-flight must carry neither the login nor the api
// key nor any query string, while staying useful (scheme, host, path,
// cause) and keeping errors.Is(err, context.DeadlineExceeded) true.
func TestGet_TransportErrorsOmitCredentials(t *testing.T) {
	var hits int32
	var once sync.Once
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		once.Do(func() { close(started) })
		<-r.Context().Done() // hold the request until the client gives up
	}))
	defer srv.Close()

	const (
		probeLogin = "probe_login"
		probeKey   = "probe_api_key_0123456789"
	)
	cli := NewClient(
		WithBaseURL(srv.URL),
		WithReqGap(0),
		WithCredentials(probeLogin, probeKey),
	)
	cli.backoffs = []time.Duration{0, 0}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	params := url.Values{"limit": {"1"}, "search[name]": {"probe_tag"}}
	_, err := cli.Get(ctx, "/tags.json", params)
	<-started
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error identity lost: expected context.DeadlineExceeded, got: %v", err)
	}
	msg := err.Error()
	if strings.Contains(msg, probeLogin) {
		t.Errorf("error text leaks the login: %v", err)
	}
	if strings.Contains(msg, probeKey) {
		t.Errorf("error text leaks the api key: %v", err)
	}
	if strings.Contains(msg, "limit=1") || strings.Contains(msg, "search%5Bname%5D") {
		t.Errorf("error text carries a query string: %v", err)
	}
	if !strings.Contains(msg, "/tags.json") {
		t.Errorf("error text lost the request path: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Fatalf("expected a *url.Error, got %T", err)
	}
	if strings.Contains(ue.URL, "?") {
		t.Errorf("error URL must carry no query string, got %q", ue.URL)
	}
	if !strings.HasPrefix(ue.URL, srv.URL+"/tags.json") {
		t.Errorf("error URL lost scheme/host/path, got %q", ue.URL)
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

func TestGet_ConcurrentRequestsPreserveRateGap(t *testing.T) {
	// waitTurn moved the rate-limit sleep outside c.mu; the re-check loop
	// must still keep every consecutive pair of outbound request starts
	// (observed as server arrivals) at least reqGap apart.
	const (
		n      = 8
		reqGap = 50 * time.Millisecond
		slack  = 10 * time.Millisecond // loopback scheduling jitter allowance
	)
	var mu sync.Mutex
	arrivals := make([]time.Time, 0, n)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	cli.reqGap = reqGap

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := url.Values{"search[name]": []string{fmt.Sprintf("tag_%d", i)}}
			if _, err := cli.Get(context.Background(), "/tags.json", q); err != nil {
				t.Errorf("Get %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(arrivals) != n {
		t.Fatalf("expected %d server arrivals, got %d", n, len(arrivals))
	}
	sort.Slice(arrivals, func(i, j int) bool { return arrivals[i].Before(arrivals[j]) })
	for i := 1; i < len(arrivals); i++ {
		if gap := arrivals[i].Sub(arrivals[i-1]); gap < reqGap-slack {
			t.Errorf("concurrent requests broke the rate gap: arrivals %d->%d only %v apart (want >= %v)", i-1, i, gap, reqGap-slack)
		}
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

// TestGet_NonRetryable4xxReturnsAfterOneAttempt pins the invariant the old
// stale-serve test carried: a definitive 4xx is an answer from a reachable
// upstream (bad query, revoked credentials), so it surfaces after exactly
// one attempt with its shrunken error text — no retry, no masking.
func TestGet_NonRetryable4xxReturnsAfterOneAttempt(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"query rejected"}`))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	_, err := cli.Get(context.Background(), "/tags.json", url.Values{"search[name]": {"arm_up"}})
	if err == nil {
		t.Fatal("expected the 4xx to surface")
	}
	if !strings.Contains(err.Error(), "danbooru 422") {
		t.Errorf("expected the shrunken danbooru 422 error, got: %v", err)
	}
	if errors.Is(err, app.ErrUpstreamUnavailable) {
		t.Error("a 4xx is definitive and must not carry the upstream-unavailable marker")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("a 4xx must not retry: expected 1 attempt, got %d", n)
	}
}

// TestGet_UpstreamFailuresAreMarked pins the marker contract: 5xx, 429 and
// refused connections (each after the retry budget is spent) satisfy
// errors.Is(err, app.ErrUpstreamUnavailable), while a definitive 4xx and a
// caller cancellation do not.
func TestGet_UpstreamFailuresAreMarked(t *testing.T) {
	t.Run("5xx after retries", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("still down"))
		}))
		defer srv.Close()

		cli := newFastTestClient(srv.URL)
		_, err := cli.Get(context.Background(), "/test", nil)
		if err == nil || !strings.Contains(err.Error(), "danbooru 500") {
			t.Fatalf("expected the shrunken 500 error, got: %v", err)
		}
		if !errors.Is(err, app.ErrUpstreamUnavailable) {
			t.Errorf("retries-exhausted 5xx must carry the upstream marker, got: %v", err)
		}
	})

	t.Run("429 after retries", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		// Zeroed backoffs: only the Retry-After header can delay, keeping
		// the marker assertion at ~1s.
		cli := newFastTestClient(srv.URL)
		_, err := cli.Get(context.Background(), "/test", nil)
		if err == nil || !strings.Contains(err.Error(), "rate limited") {
			t.Fatalf("expected the rate-limited error, got: %v", err)
		}
		if !errors.Is(err, app.ErrUpstreamUnavailable) {
			t.Errorf("retries-exhausted 429 must carry the upstream marker, got: %v", err)
		}
	})

	t.Run("refused connection", func(t *testing.T) {
		// A closed port: the OS refuses instantly, so no sleeps are needed.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		ln.Close()

		cli := newTestClient("http://" + addr)
		cli.backoffs = []time.Duration{0, 0}
		_, err = cli.Get(context.Background(), "/test", nil)
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if !errors.Is(err, app.ErrUpstreamUnavailable) {
			t.Errorf("a refused connection must carry the upstream marker, got: %v", err)
		}
	})

	t.Run("4xx unmarked", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("resource not found"))
		}))
		defer srv.Close()

		cli := newFastTestClient(srv.URL)
		_, err := cli.Get(context.Background(), "/test", nil)
		if err == nil || !strings.Contains(err.Error(), "danbooru 404") {
			t.Fatalf("expected the shrunken 404 error, got: %v", err)
		}
		if errors.Is(err, app.ErrUpstreamUnavailable) {
			t.Errorf("a definitive 4xx must not carry the upstream marker, got: %v", err)
		}
	})

	t.Run("cancellation unmarked", func(t *testing.T) {
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
		if errors.Is(err, app.ErrUpstreamUnavailable) {
			t.Errorf("a caller cancellation must not carry the upstream marker, got: %v", err)
		}
	})
}

// TestChain_UpstreamFailureReachesTheClient runs the whole api -> service ->
// tools chain against one local server: during a 503 outage the real
// handlers return the marked upstream-unavailable result; for a genuine
// not-found (healthy upstream, empty row list) get_tag_info still answers
// the unmarked tag_not_found payload with its wd14 verdict.
func TestChain_UpstreamFailureReachesTheClient(t *testing.T) {
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("<html><head><title>Downbooru</title></head></html>"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	svc := service.NewTagService(cli, vocab.Default())
	s := server.NewMCPServer("test", "0.0.0.0", server.WithToolCapabilities(true))
	tools.Register(s, svc)

	mcpCli, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("in-process client: %v", err)
	}
	defer mcpCli.Close()
	if err := mcpCli.Start(context.Background()); err != nil {
		t.Fatalf("client start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "chain-test", Version: "0.0.1"}
	if _, err := mcpCli.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := mcpCli.CallTool(context.Background(), req)
		if err != nil {
			t.Fatalf("CallTool %s: %v", name, err)
		}
		return res
	}
	textOf := func(res *mcp.CallToolResult) string {
		t.Helper()
		if len(res.Content) != 1 {
			t.Fatalf("expected 1 content item, got %d", len(res.Content))
		}
		tc, ok := res.Content[0].(mcp.TextContent)
		if !ok {
			t.Fatalf("expected text content, got %T", res.Content[0])
		}
		return tc.Text
	}

	// Outage: the marked result must reach the MCP client.
	down.Store(true)
	res := call("get_tag_info", map[string]any{"name": "blue_hair"})
	if !res.IsError {
		t.Fatalf("the outage must be flagged at the protocol level, got: %+v", res)
	}
	var marked map[string]string
	if err := json.Unmarshal([]byte(textOf(res)), &marked); err != nil {
		t.Fatalf("marked payload is not JSON: %v (%s)", err, textOf(res))
	}
	if marked["error"] != "upstream_unavailable" {
		t.Errorf("error code = %q, want upstream_unavailable", marked["error"])
	}
	if !strings.Contains(marked["message"], "danbooru 503 (Downbooru)") ||
		!strings.Contains(marked["message"], "All tools of this server are affected") {
		t.Errorf("message must carry the cause and the server-wide note, got: %q", marked["message"])
	}
	if strings.Contains(marked["message"], "tag_not_found") {
		t.Errorf("an upstream failure must never be reported as tag_not_found, got: %q", marked["message"])
	}

	// Healthy upstream, absent name: the unmarked not-found payload survives.
	down.Store(false)
	res = call("get_tag_info", map[string]any{"name": "gold_footwear"})
	if res.IsError {
		t.Fatalf("a genuine not-found must stay an unmarked result, got IsError: %+v", res)
	}
	var nf map[string]any
	if err := json.Unmarshal([]byte(textOf(res)), &nf); err != nil {
		t.Fatalf("not-found payload is not JSON: %v (%s)", err, textOf(res))
	}
	if nf["error"] != "tag_not_found" {
		t.Errorf("error code = %v, want tag_not_found", nf["error"])
	}
	if nf["message"] != "tag not found: gold_footwear" {
		t.Errorf("message = %v, want the NotFoundError text", nf["message"])
	}
	wd14, ok := nf["wd14"].(map[string]any)
	if !ok || wd14["status"] != "tagger_era" {
		t.Errorf("the wd14 verdict must ride the not-found payload, got: %v", nf["wd14"])
	}
}

// TestFetchPostCount_BuildsCorrectURL pins the one request count mode needs:
// GET /counts/posts.json carrying the tags as typed and nothing else — no
// rating filter, no limit. An empty tags omits the parameter, which asks for
// the whole-site count.
func TestFetchPostCount_BuildsCorrectURL(t *testing.T) {
	tests := []struct {
		name      string
		tags      string
		wantQuery string // url.Values.Encode of the query the server must see
	}{
		{"no filter is injected", "arm_up", "tags=arm_up"},
		{"tags go out as typed", "arm_up rating:g", "tags=arm_up+rating%3Ag"},
		{"empty tags omit the parameter", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotMethod, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				// The whole query is compared, which pins "tags and nothing
				// else". Credentials are stripped first: when the shell
				// carries DANBOORU_LOGIN / DANBOORU_API_KEY, NewClient injects
				// them, and the comparison would fail and print them.
				q := r.URL.Query()
				q.Del("login")
				q.Del("api_key")
				gotQuery = q.Encode()
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"counts":{"posts":317377}}`))
			}))
			defer srv.Close()

			cli := newTestClient(srv.URL)
			if _, err := cli.FetchPostCount(context.Background(), tt.tags); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotPath != "/counts/posts.json" {
				t.Errorf("expected /counts/posts.json, got %q", gotPath)
			}
			if gotMethod != http.MethodGet {
				t.Errorf("expected GET, got %s", gotMethod)
			}
			if gotQuery != tt.wantQuery {
				t.Errorf("query = %q, want %q", gotQuery, tt.wantQuery)
			}
		})
	}
}

// TestFetchPostCount_ReturnsBodyVerbatim pins that the body reaches the
// service untouched: the service tells an explicit null (Danbooru's count
// timed out) from a 0 by the raw bytes, so the api layer must not parse,
// rewrite or trim the payload.
func TestFetchPostCount_ReturnsBodyVerbatim(t *testing.T) {
	for _, body := range []string{`{"counts":{"posts":null}}`, `{"counts":{"posts":0}}`, `{"counts":{"posts":317377}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(body))
		}))
		defer srv.Close()

		cli := newTestClient(srv.URL)
		got, err := cli.FetchPostCount(context.Background(), "arm_up")
		if err != nil {
			t.Fatalf("body %s: unexpected error: %v", body, err)
		}
		if string(got) != body {
			t.Errorf("body %s: must come back verbatim, got %s", body, got)
		}
	}
}

// TestFetchPostCount_UpstreamFailureIsMarked pins R4 for the counts request:
// it goes through Client.Get like every other request, so a 503 is retried
// and, once the retries are spent, comes back carrying the upstream marker
// with the shrunken cause — the handler turns that into the uniform
// upstream_unavailable result.
func TestFetchPostCount_UpstreamFailureIsMarked(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("<html><head><title>Downbooru</title></head><body>" + strings.Repeat("maintenance padding ", 100) + "</body></html>"))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	_, err := cli.FetchPostCount(context.Background(), "arm_up")
	if err == nil || err.Error() != "danbooru 503 (Downbooru)" {
		t.Fatalf("expected 'danbooru 503 (Downbooru)', got: %v", err)
	}
	if !errors.Is(err, app.ErrUpstreamUnavailable) {
		t.Errorf("a retries-exhausted 503 must carry the upstream marker, got: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1+maxRetries {
		t.Errorf("5xx must be retried before erroring: expected %d attempts, got %d", 1+maxRetries, n)
	}
}

// TestFetchPostCount_422IsUnmarkedSingleAttempt pins that a 422 on the counts
// request (an over-limit query the local check did not catch) is a
// definitive answer: one attempt, shrunken text, no upstream marker.
func TestFetchPostCount_422IsUnmarkedSingleAttempt(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"TagLimitError: tag limit exceeded"}`))
	}))
	defer srv.Close()

	cli := newFastTestClient(srv.URL)
	_, err := cli.FetchPostCount(context.Background(), "1girl blue_hair long_hair")
	if err == nil {
		t.Fatal("expected the 422 to surface")
	}
	if !strings.Contains(err.Error(), "danbooru 422") {
		t.Errorf("expected the shrunken danbooru 422 error, got: %v", err)
	}
	if errors.Is(err, app.ErrUpstreamUnavailable) {
		t.Error("a 4xx is definitive and must not carry the upstream-unavailable marker")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("a 4xx must not retry: expected 1 attempt, got %d", n)
	}
}

// newChainClient wires the real api -> service -> tools chain to a test
// server and returns a started in-process MCP client, so a test sees exactly
// what an MCP client gets from every layer combined. Retry backoffs are
// zeroed so an outage costs no real sleeps.
func newChainClient(t *testing.T, handler http.Handler) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc := service.NewTagService(newFastTestClient(srv.URL), vocab.Default())
	s := server.NewMCPServer("test", "0.0.0.0", server.WithToolCapabilities(true))
	tools.Register(s, svc)

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
	initReq.Params.ClientInfo = mcp.Implementation{Name: "chain-test", Version: "0.0.1"}
	if _, err := cli.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return cli
}

// callToolText calls one tool on cli and returns its result together with
// the text of its single content item, i.e. the wire payload.
func callToolText(t *testing.T, cli *client.Client, name string, args map[string]any) (*mcp.CallToolResult, string) {
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

// TestChain_CountModeReachesTheClient runs the whole api -> service -> tools
// chain for search_posts count mode against one local server that answers
// /counts/posts.json from switchable knobs and /posts.json with a plain
// page. It pins what the layers contribute together: a live count arrives as
// {"query":...,"total":...}; a null count as the unmarked post_search_failed
// result; a 503 on the counts request as the uniform marked
// upstream_unavailable result after the retry budget; a 422 unmarked after
// one attempt. /posts.json is never touched: count mode has no fallback to
// listing posts, whatever goes wrong with the count.
func TestChain_CountModeReachesTheClient(t *testing.T) {
	var mu sync.Mutex // guards the knobs between calls
	countStatus, countContentType, countBody := http.StatusOK, "application/json", `{"counts":{"posts":317377}}`
	var countHits, postsHits int32
	cli := newChainClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/counts/posts.json":
			atomic.AddInt32(&countHits, 1)
			mu.Lock()
			status, contentType, body := countStatus, countContentType, countBody
			mu.Unlock()
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(status)
			w.Write([]byte(body))
		case "/posts.json":
			atomic.AddInt32(&postsHits, 1)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serveCount := func(status int, contentType, body string) {
		mu.Lock()
		defer mu.Unlock()
		countStatus, countContentType, countBody = status, contentType, body
	}
	countArgs := map[string]any{"tags": "arm_up", "count_only": true}

	// A live count: the marshaled PostCount, query first.
	res, text := callToolText(t, cli, "search_posts", countArgs)
	if want := `{"query":"arm_up","total":317377}`; text != want {
		t.Errorf("live count:\n got: %s\nwant: %s", text, want)
	}
	if res.IsError {
		t.Errorf("a live count must not be flagged at the protocol level, got: %+v", res)
	}

	// A null count: definitive, so the tool's own code as an unmarked result.
	serveCount(http.StatusOK, "application/json", `{"counts":{"posts":null}}`)
	before := atomic.LoadInt32(&countHits)
	res, text = callToolText(t, cli, "search_posts", countArgs)
	want := `{"error":"post_search_failed","message":"danbooru could not count this query (count timed out); simplify the query or retry later"}`
	if text != want {
		t.Errorf("null count:\n got: %s\nwant: %s", text, want)
	}
	if res.IsError {
		t.Errorf("a null count is not a protocol-level error, got: %+v", res)
	}
	if got := atomic.LoadInt32(&countHits) - before; got != 1 {
		t.Errorf("a null count must cost one request, got %d", got)
	}

	// A 422 on the counts request: one attempt, unmarked, shrunken text.
	serveCount(http.StatusUnprocessableEntity, "application/json", `{"error":"TagLimitError: tag limit exceeded"}`)
	before = atomic.LoadInt32(&countHits)
	res, text = callToolText(t, cli, "search_posts", countArgs)
	if res.IsError {
		t.Errorf("a 422 is definitive and must stay unmarked, got: %+v", res)
	}
	if !strings.HasPrefix(text, `{"error":"post_search_failed","message":"danbooru 422`) || strings.Contains(text, "upstream_unavailable") {
		t.Errorf("a 422 must be the plain post_search_failed result, got: %s", text)
	}
	if got := atomic.LoadInt32(&countHits) - before; got != 1 {
		t.Errorf("a 422 must cost one attempt, got %d", got)
	}

	// An outage on the counts request: retried, then the uniform marked result.
	serveCount(http.StatusServiceUnavailable, "text/html", `<html><head><title>Downbooru</title></head></html>`)
	before = atomic.LoadInt32(&countHits)
	res, text = callToolText(t, cli, "search_posts", countArgs)
	if !res.IsError {
		t.Errorf("the outage must be flagged at the protocol level, got: %+v", res)
	}
	want = `{"error":"upstream_unavailable","message":"Danbooru is not answering right now: danbooru 503 (Downbooru). All tools of this server are affected; retry later."}`
	if text != want {
		t.Errorf("outage:\n got: %s\nwant: %s", text, want)
	}
	if got := atomic.LoadInt32(&countHits) - before; got != 1+maxRetries {
		t.Errorf("a 5xx must be retried before the marked result, expected %d attempts, got %d", 1+maxRetries, got)
	}

	if n := atomic.LoadInt32(&postsHits); n != 0 {
		t.Errorf("count mode must never list posts, got %d /posts.json requests", n)
	}
}
