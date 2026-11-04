package cache

import (
	"context"
	"testing"
)

func TestRefreshRequested_Behavior(t *testing.T) {
	plain := context.Background()
	if RefreshRequested(plain) {
		t.Error("a plain context must not request a refresh")
	}
	if RefreshRequested(context.WithValue(plain, refreshCtxKey{}, "yes")) {
		t.Error("only boolean values set the flag")
	}
	refreshed := WithRefresh(plain)
	if !RefreshRequested(refreshed) {
		t.Error("WithRefresh must set the flag")
	}
	// The flag survives derivation, so service-layer ctx plumbing keeps it.
	if !RefreshRequested(context.WithoutCancel(refreshed)) {
		t.Error("derived contexts must inherit the refresh request")
	}
}
