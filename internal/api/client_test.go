package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(serverURL string) *Client {
	return NewClient(
		WithBaseURL(serverURL),
		WithReqGap(0), // no real throttling in tests
	)
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

func TestFetchTags_BuildsCorrectURL(t *testing.T) {
	var gotMatches, gotOrder, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMatches = r.URL.Query().Get("search[name_matches]")
		gotOrder = r.URL.Query().Get("search[order]")
		gotLimit = r.URL.Query().Get("limit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := newTestClient(srv.URL)
	_, err := cli.FetchTags(context.Background(), "blue_hair", 10, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMatches != "*blue_hair*" || gotOrder != "count" || gotLimit != "10" {
		t.Errorf("unexpected query params: matches=%s, order=%s, limit=%s", gotMatches, gotOrder, gotLimit)
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
