package router

import (
	"context"
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

func withLiveDiffSources(ctx context.Context) context.Context {
	return context.WithValue(ctx, liveDiffSourcesContext{}, &liveDiffSources{})
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
