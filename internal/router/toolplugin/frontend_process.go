package toolplugin

import "context"

type hostProcessGroupKey struct{}
type frontendOrphanCleanupKey struct{}

// WithHostProcessGroup keeps executable frontend descendants in Codex's
// command group so stock cancellation can terminate the whole invocation.
func WithHostProcessGroup(ctx context.Context) context.Context {
	return context.WithValue(ctx, hostProcessGroupKey{}, true)
}

func hostProcessGroupOwned(ctx context.Context) bool {
	owned, _ := ctx.Value(hostProcessGroupKey{}).(bool)
	return owned
}

// EnableFrontendOrphanCleanup is for a dedicated authenticated frontend
// process, not a router or test process that owns unrelated child commands.
func EnableFrontendOrphanCleanup(ctx context.Context) (context.Context, error) {
	if err := enableFrontendSubreaper(); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, frontendOrphanCleanupKey{}, true), nil
}
