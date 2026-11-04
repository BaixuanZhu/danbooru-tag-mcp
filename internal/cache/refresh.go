package cache

import "context"

// refreshCtxKey is the private context key carrying the per-call refresh
// request. It never crosses process boundaries: ctx flows tools -> service
// -> api within one call.
type refreshCtxKey struct{}

// WithRefresh returns a context carrying the refresh request: Client.Get
// skips the fresh-entry shortcut, goes to the network (retry rules
// unchanged), and writes a successful response back so the TTL clock
// restarts. A refresh that fails still falls back to a cached entry, marked
// stale. This is the per-call cache bypass behind the tools layer's
// refresh param; no interface signature changes are needed.
func WithRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshCtxKey{}, true)
}

// RefreshRequested reports whether ctx carries the refresh request set by
// WithRefresh. A context that never carried it (or was derived through
// value-free paths) answers false, which is the default cache-on behavior.
func RefreshRequested(ctx context.Context) bool {
	v, ok := ctx.Value(refreshCtxKey{}).(bool)
	return ok && v
}
