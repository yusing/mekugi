package router

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// A private router includes only threads it has prepared, not every session that
// ever edited the workspace. Membership and connection capabilities expire with it.
const maxLiveDiffScopeBytes = 1 << 20

type liveDiffScope struct {
	Workspaces map[string]map[string]bool
}

type autoLiveDiff struct {
	events     *liveDiffBroker
	enabled    atomic.Bool
	mu         sync.Mutex
	workspace  string
	scope      liveDiffScope
	scopeBytes int
	changed    chan struct{}
}

func newAutoLiveDiff(ctx context.Context, replay string) (*autoLiveDiff, func()) {
	ctx, cancel := context.WithCancel(ctx)
	a := &autoLiveDiff{
		events:     newLiveDiffBroker(ctx),
		scope:      liveDiffScope{Workspaces: make(map[string]map[string]bool)},
		scopeBytes: len(`{"Workspaces":{}}`),
		changed:    make(chan struct{}, 1),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.run(ctx, replay)
	}()
	return a, func() { cancel(); <-done }
}

func (a *autoLiveDiff) run(ctx context.Context, replay string) {
	var directory string
	var pane liveDiffPane
	defer func() {
		// The event stream ends the viewer even if pane cleanup is unavailable.
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
		if pane.id != "" {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(closeCtx, "herdr", "pane", "close", pane.id)
			cmd.WaitDelay = time.Second
			_ = cmd.Run()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.changed:
		}
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		if a.scope.Workspaces == nil {
			a.mu.Unlock()
			return
		}
		workspace := a.workspace
		data, err := json.Marshal(a.events.descriptor())
		a.mu.Unlock()
		if workspace == "" || err != nil {
			continue
		}
		first := directory == ""
		if first {
			directory, err = os.MkdirTemp("", "mekugi-live-diff-")
			if err != nil {
				return
			}
			pane.sessionFile = filepath.Join(directory, "session.json")
		}
		if first {
			if err := os.WriteFile(pane.sessionFile, data, 0600); err != nil {
				return
			}
			launch, stop := context.WithTimeout(ctx, 5*time.Second)
			err = splitLiveDiff(launch, workspace, replay, io.Discard, &pane)
			stop()
			if err != nil {
				return
			}
		}
	}
}

func (a *autoLiveDiff) enable() {
	if a == nil || os.Getenv("HERDR_ENV") != "1" {
		return
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		return
	}
	a.enabled.Store(true)
}

func (a *autoLiveDiff) observe(workspace, thread string, metadata codexTurnMetadata) {
	if a == nil || !a.enabled.Load() || workspace == "" || thread == "" ||
		metadata.activityIdentityInvalid || metadata.RequestKind != "turn" {
		return
	}
	workspaceJSON, _ := json.Marshal(workspace)
	threadJSON, _ := json.Marshal(thread)
	a.mu.Lock()
	if !a.enabled.Load() {
		a.mu.Unlock()
		return
	}
	rootSelected := a.workspace == "" && metadata.SubagentKind == ""
	threads := a.scope.Workspaces[workspace]
	if !threads[thread] {
		added := len(threadJSON) + len(":true")
		if len(threads) > 0 {
			added++ // comma between thread entries
		}
		if threads == nil {
			added += len(workspaceJSON) + len(":{}")
			if len(a.scope.Workspaces) > 0 {
				added++ // comma between workspace entries
			}
		}
		if a.scopeBytes+added > maxLiveDiffScopeBytes {
			// Do not publish a partial scope or retain unbounded auxiliary state.
			a.enabled.Store(false)
			a.scope.Workspaces = nil
		} else {
			if threads == nil {
				threads = make(map[string]bool)
				a.scope.Workspaces[workspace] = threads
			}
			threads[thread] = true
			a.scopeBytes += added
		}
	} else if !rootSelected {
		a.mu.Unlock()
		return
	}
	a.events.setScope(a.scope)
	if rootSelected && a.enabled.Load() {
		a.workspace = workspace
	}
	a.mu.Unlock()
	select {
	case a.changed <- struct{}{}:
	default:
	}
}
