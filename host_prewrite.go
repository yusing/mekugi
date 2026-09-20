package mekugi

import "context"

type preWriteObserverKey struct{}

// WithPreWriteObserver attaches a display-only observer to ApplyForHostAt.
// The observer runs synchronously after successful evaluation and formatting,
// before any writes, including for no-op evaluations with an empty slice.
// It receives an isolated review slice, not an approval request or a guarantee
// that the subsequent commit will succeed. The callback must be bounded and
// must not mutate the filesystem or other execution state. Cancellation is
// checked again after it returns. Translation and preview do not invoke it.
func WithPreWriteObserver(ctx context.Context, observer func([]ReviewFile)) context.Context {
	return context.WithValue(ctx, preWriteObserverKey{}, observer)
}
