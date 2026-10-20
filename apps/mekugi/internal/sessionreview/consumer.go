package sessionreview

import "context"

// Replace retires both consumer admission and producer membership. A blocked
// read retains its physical loader slot until it actually returns.
func (r *Review) Replace(ctx context.Context) {
	r.Projection.Reset()
	r.Loader.Replace(ctx, r.Projection)
	r.PreviewScope, r.Failure = r.Projection.Scope, ""
	r.ResetScope()
}

// Source: apps/mekugi/internal/router/native_shell.go:152:232@51f24bbd.
// Apply admits preview authority separately from retained hydration. The return
// values identify an admitted event and a new complete publication, respectively.
// The caller serializes this consumer with replacement and loader completion.
func (r *Review) Apply(ctx context.Context, event Event) (bool, bool) {
	if event.Kind == "preview" {
		scope := r.Projection.Scope
		if r.Loader != nil {
			scope = r.PreviewScope
		}
		preview := event.Preview
		return preview != nil && (preview.Workspace == "" || scope.Workspaces[preview.Workspace][preview.Thread]), false
	}
	if event.Kind == "scope" && event.Scope != nil {
		r.PreviewScope = *event.Scope
	}
	if r.Failure != "" {
		return true, false
	}
	if r.Loader != nil {
		if err := r.Loader.Enqueue(event); err != nil {
			r.Failure = err.Error()
			return true, true
		}
		return true, false
	}
	changed, err := r.Projection.Apply(ctx, event)
	if err != nil {
		r.Failure = err.Error()
		return true, true
	}
	return true, changed
}

// Loaded publishes only the current attachment's complete retained result.
func (r *Review) Loaded(result Result) bool {
	accepted, err := r.Loader.Accept(result)
	if err != nil {
		r.Failure = err.Error()
		return true
	}
	if accepted {
		workspace := r.Projection.Workspace
		r.Projection = r.Loader.Projection()
		r.Projection.Workspace = workspace
	}
	return accepted
}

// Ready preserves preview delivery after retained loading fails, without asking
// for a new durable resync barrier after every subsequent preview event.
func (r *Review) Ready() bool {
	return r.Failure != "" || r.Loader == nil || !r.Loader.NeedsResync()
}

// Restoring reports retained evidence still needed by the selected membership.
func (r *Review) Restoring() bool {
	if r.Projection.Retained.Store == nil || r.Failure != "" {
		return false
	}
	if r.Loader.Pending() {
		return true
	}
	for workspace, threads := range r.Scope().Workspaces {
		for thread := range threads {
			if !r.Projection.Scope.Workspaces[workspace][thread] {
				return true
			}
		}
	}
	return false
}
