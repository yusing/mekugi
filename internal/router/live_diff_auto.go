package router

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
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

	// The agents view shares this session; its ownership lives in the collector.
	activityRequested bool
	stopped           bool // The UI session ended; later requests cannot be served.
}

func newAutoLiveDiff(ctx context.Context, replay string) (*autoLiveDiff, func()) {
	ctx, cancel := context.WithCancel(ctx)
	a := &autoLiveDiff{
		events:     newLiveDiffBroker(ctx),
		scope:      liveDiffScope{Workspaces: make(map[string]map[string]bool)},
		scopeBytes: len(`{"Workspaces":{}}`),
		changed:    make(chan struct{}, 1),
	}
	return a, func() {
		cancel()
		a.enabled.Store(false)
		a.mu.Lock()
		a.stopped = true
		a.mu.Unlock()
	}
}

func (a *autoLiveDiff) enable() {
	if a != nil {
		a.enabled.Store(true)
	}
}

// Turn preparation collects scope without opening UI. Only an emitted shell
// call in an observed thread requests the one-shot asynchronous view opening.
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
// reports false when the integrated UI is unavailable.
func (a *autoLiveDiff) requestActivity() bool {
	if a == nil || !a.enabled.Load() {
		return false
	}
	a.mu.Lock()
	// Without a root workspace or an active UI, delivery stays inline.
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
	a.includeThread(workspace, thread, metadata.SubagentKind == "")
}

// includeThread also accepts identities established by observational resume
// history. It changes presentation scope, not turn or execution state.
func (a *autoLiveDiff) includeThread(workspace, thread string, root bool) {
	if a == nil || !a.enabled.Load() || workspace == "" || thread == "" {
		return
	}
	workspaceJSON, _ := json.Marshal(workspace)
	threadJSON, _ := json.Marshal(thread)
	a.mu.Lock()
	if !a.enabled.Load() {
		a.mu.Unlock()
		return
	}
	rootSelected := a.workspace == "" && root
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
