package projection

import (
	"context"
	"errors"
	"reflect"
	"sync"
)

// A preview projects from the workspace as it was before its call ran. Codex
// executes a call once its input completes, while the final projection may
// still be pacing; re-reading then would diff the edit against itself. Each
// preview keeps the first successful read of a path, per reader.
type liveDiffSources struct {
	mu    sync.Mutex
	files map[liveDiffSourceKey]liveDiffSource
	// Captured-only projections must not learn their baseline after execution.
	capturedOnly bool
}

type liveDiffSourceKey struct {
	reader uintptr
	path   string
}

type liveDiffSource struct {
	content string
	exists  bool
}

type liveDiffSourcesContext struct{}

// WithSources keeps the first successful baseline per path and reader. Captured-only
// contexts reject uncaptured reads, so observation cannot learn a post-call baseline.
func WithSources(ctx context.Context, capturedOnly bool) context.Context {
	return context.WithValue(ctx, liveDiffSourcesContext{}, &liveDiffSources{capturedOnly: capturedOnly})
}

// Prime supplies a captured baseline without replacing an earlier successful read.
func Prime(ctx context.Context, read func(string) (string, bool, error), path, content string, exists bool) {
	sources, _ := ctx.Value(liveDiffSourcesContext{}).(*liveDiffSources)
	if sources == nil || path == "" {
		return
	}
	sources.mu.Lock()
	defer sources.mu.Unlock()
	if sources.files == nil {
		sources.files = make(map[liveDiffSourceKey]liveDiffSource)
	}
	key := liveDiffSourceKey{reflect.ValueOf(read).Pointer(), path}
	if _, cached := sources.files[key]; !cached {
		sources.files[key] = liveDiffSource{content, exists}
	}
}

func liveDiffSourceRead(ctx context.Context, path string, read func(string) (string, bool, error)) (string, bool, error) {
	sources, _ := ctx.Value(liveDiffSourcesContext{}).(*liveDiffSources)
	if sources == nil {
		return read(path)
	}
	key := liveDiffSourceKey{reflect.ValueOf(read).Pointer(), path}
	sources.mu.Lock()
	source, cached := sources.files[key]
	sources.mu.Unlock()
	if cached {
		return source.content, source.exists, nil
	}
	if sources.capturedOnly {
		return "", false, errors.New("preview source was not captured")
	}
	content, exists, err := read(path)
	if err != nil {
		return content, exists, err // A failed read is retried on the next frame.
	}
	sources.mu.Lock()
	defer sources.mu.Unlock()
	if sources.files == nil {
		sources.files = make(map[liveDiffSourceKey]liveDiffSource)
	}
	if source, cached := sources.files[key]; cached {
		return source.content, source.exists, nil
	}
	sources.files[key] = liveDiffSource{content, exists}
	return content, exists, nil
}
