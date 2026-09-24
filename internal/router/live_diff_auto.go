package router

import (
	"context"
	"encoding/json"
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
	notice       func(string, string)
	events       *liveDiffBroker
	enabled      atomic.Bool
	mu           sync.Mutex
	requested    bool
	workspace    string
	scope        liveDiffScope
	scopeBytes   int
	turn         liveDiffTurn
	turnComplete bool
	changed      chan struct{}

	// The agents pane shares this launcher; its ownership lives in the collector.
	activityRequested  bool
	stopped            bool // The launcher exited; later requests cannot be served.
	activityConnection func() liveDiffConnection
	activityFailed     func()
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
	var pane, activity, roster liveDiffPane
	diffTried, activityTried := false, false
	diffOpen, activityOpen := false, false
	below := func(open bool, lifetime liveDiffPane) string {
		if open {
			return lifetime.id
		}
		return ""
	}
	defer func() {
		// The event stream ends the viewer even if pane cleanup is unavailable.
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
		for _, id := range []string{roster.id, activity.id, pane.id} {
			if id == "" {
				continue
			}
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			cmd := exec.CommandContext(closeCtx, "herdr", "pane", "close", id)
			cmd.WaitDelay = time.Second
			_ = cmd.Run()
			cancel()
		}
		a.mu.Lock()
		a.stopped = true
		unserved := a.activityRequested && !activityOpen
		a.mu.Unlock()
		if unserved && a.activityFailed != nil {
			a.activityFailed()
		}
	}()
	prepare := func() bool {
		if directory != "" {
			return true
		}
		var err error
		directory, err = os.MkdirTemp("", "mekugi-live-diff-")
		if err != nil {
			directory = ""
			return false
		}
		executable := filepath.Join(directory, "mekugi-live-diff")
		if pinRunningExecutable(executable) != nil {
			return false
		}
		pane.executable, activity.executable, roster.executable = executable, executable, executable
		return true
	}
	writeSession := func(name string, connection liveDiffConnection) (string, bool) {
		data, err := json.Marshal(connection)
		path := filepath.Join(directory, name)
		return path, err == nil && os.WriteFile(path, data, 0600) == nil
	}
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
		if a.scope.Workspaces == nil && !a.activityRequested {
			a.mu.Unlock()
			return
		}
		workspace, activityRequested := a.workspace, a.activityRequested
		requested := a.requested && a.scope.Workspaces != nil
		a.mu.Unlock()
		// Each pane fails alone: a failed launch leaves the other one running, and
		// a created but unplaced pane keeps its ID for shutdown cleanup.
		if requested && workspace != "" && !diffTried {
			diffTried = true
			var ok bool
			if prepare() {
				pane.sessionFile, ok = writeSession("session.json", a.events.descriptor())
			}
			if ok {
				launch, stop := context.WithTimeout(ctx, 5*time.Second)
				diffOpen = splitLiveDiff(launch, workspace, replay, below(activityOpen, activity), &pane) == nil
				stop()
			}
		}
		if activityRequested && workspace != "" && !activityTried {
			activityTried = true
			var ok bool
			if prepare() {
				activity.sessionFile, ok = writeSession("activity.json", a.activityConnection())
			}
			if ok {
				launch, stop := context.WithTimeout(ctx, 5*time.Second)
				activityOpen = splitLiveActivity(launch, workspace, below(diffOpen, pane), &activity) == nil
				stop()
			}
			// The roster is optional: without it the agents pane keeps its own.
			if activityOpen {
				roster.sessionFile = activity.sessionFile
				launch, stop := context.WithTimeout(ctx, 5*time.Second)
				_ = splitLiveRoster(launch, workspace, &roster)
				stop()
			}
			if !activityOpen && a.activityFailed != nil {
				a.activityFailed()
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

// Turn preparation collects scope without opening UI. Only an emitted shell
// call in an observed thread requests the one-shot asynchronous launch.
func (a *autoLiveDiff) requestLaunch(workspace, thread string) {
	if a == nil || !a.enabled.Load() {
		return
	}
	a.mu.Lock()
	if !a.enabled.Load() || a.requested || !a.scope.Workspaces[workspace][thread] {
		a.mu.Unlock()
		return
	}
	a.requested = true
	a.mu.Unlock()
	select {
	case a.changed <- struct{}{}:
	default:
	}
}

// requestActivity asks for the one-shot agents pane. It never blocks and
// reports false when no Herdr pane can be launched.
func (a *autoLiveDiff) requestActivity() bool {
	if a == nil || !a.enabled.Load() || a.activityConnection == nil {
		return false
	}
	a.mu.Lock()
	// Without a root workspace or a running launcher, delivery stays inline.
	if a.activityRequested || a.stopped || a.workspace == "" {
		a.mu.Unlock()
		return false
	}
	a.activityRequested = true
	a.mu.Unlock()
	select {
	case a.changed <- struct{}{}:
	default:
	}
	return true
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
			if a.notice != nil {
				a.notice("live_diff_scope_capacity", "Mekugi disabled automatic live diff because session scope exceeded 1 MiB. Edits and mchanges remain available; restart the router to reset this live-view scope.")
			}
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
