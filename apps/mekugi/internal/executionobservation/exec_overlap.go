package executionobservation

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	changes "github.com/yusing/mekugi/modules/changehistory/changes"

	segments "github.com/yusing/mekugi/modules/commandsegments/segments"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

const maxExecWindows = 1024

// execWindow is the observation window of one writer call: from forwarding
// the call to observing its terminal result. The registry is in memory, so
// after a restart, records carry no overlap tags for windows opened before it.
type execWindow struct {
	intervalMu sync.Mutex
	owner      *Windows
	scope      changes.Scope
	cuts       []execIntervalCut
	ref        string
	roots      []string
	thread     string
	turn       string
	// group identifies the response that emitted the call.
	group  string
	paths  []string
	opened time.Time
	closed time.Time
	// session names the host session or cell that keeps a yielded window open.
	session       string
	background    bool
	previewCancel context.CancelFunc
	tracking      *execPreviewTrack
	// created keeps a completed window's mktemp reports for retried
	// finalization after its tracking, with retained output, is released.
	created []segmenttransport.Message
	// claims are snapshot states that overlapping calls already recorded.
	// This window's snapshot comparison starts from them.
	claims changecapture.Claims
}

type Windows struct {
	local  windowRegistry
	shared *windowRegistry
	// Reports is supplied during invocation setup, before observations begin.
	Reports *segments.Observer
}

type windowRegistry struct {
	mu         sync.Mutex
	windows    []*execWindow
	finalizing map[string]*workspaceFinalization
}

type workspaceFinalization struct {
	mu    sync.Mutex
	users int
}

// ForRuntime shares overlap exclusions, interval boundaries and snapshot claims
// with a descendant while keeping its command-report observer runtime-local.
func (r *Windows) ForRuntime() *Windows {
	if r == nil {
		return &Windows{}
	}
	return &Windows{shared: r.state()}
}

func (r *Windows) state() *windowRegistry {
	if r.shared != nil {
		return r.shared
	}
	return &r.local
}

// serialize coordinates close/compare/claim across runtime-local checkpoint
// owners. Entries live only while a comparison is running or waiting.
func (r *Windows) serialize(workspace string) func() {
	if r == nil {
		return func() {}
	}
	state := r.state()
	state.mu.Lock()
	if state.finalizing == nil {
		state.finalizing = make(map[string]*workspaceFinalization)
	}
	lock := state.finalizing[workspace]
	if lock == nil {
		lock = &workspaceFinalization{}
		state.finalizing[workspace] = lock
	}
	lock.users++
	state.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		state.mu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(state.finalizing, workspace)
		}
		state.mu.Unlock()
	}
}

// Retire ends this runtime's observation windows after its processes and
// observations have stopped. It does not invent terminal results or captures.
func (r *Windows) Retire() {
	if r == nil {
		return
	}
	state := r.state()
	state.mu.Lock()
	var refs []string
	for _, window := range state.windows {
		if window.owner == r && window.closed.IsZero() {
			refs = append(refs, window.ref)
		}
	}
	state.mu.Unlock()
	r.close(refs...)
}

// Only windows sharing a writer serialize interval publication. Never hold the
// registry mutex across storage, so unrelated lifecycle delivery stays runnable.
func (r *Windows) lockIntervals(refs []string) func() {
	if r == nil {
		return func() {}
	}
	refs = slices.Sorted(slices.Values(refs))
	r.state().mu.Lock()
	var windows []*execWindow
	for _, ref := range slices.Compact(refs) {
		if window := r.find(ref); window != nil {
			windows = append(windows, window)
		}
	}
	r.state().mu.Unlock()
	for _, window := range windows {
		window.intervalMu.Lock()
	}
	return sync.OnceFunc(func() {
		for _, window := range slices.Backward(windows) {
			window.intervalMu.Unlock()
		}
	})
}

func (r *Windows) find(ref string) *execWindow {
	for _, window := range r.state().windows {
		if window.ref == ref {
			return window
		}
	}
	return nil
}

// open admits a window once. Only an admitted window tracks its commands'
// reports, so a repeated open cannot make the first window's matches ambiguous.
func (r *Windows) open(window *execWindow, commands ...commandanalysis.Command) {
	if r == nil {
		return
	}
	r.state().mu.Lock()
	defer r.state().mu.Unlock()
	if r.find(window.ref) != nil {
		return
	}
	window.tracking = newExecPreviewTrack(r.Reports, window.thread, window.turn, commands)
	window.owner = r
	window.opened = time.Now()
	r.state().windows = append(r.state().windows, window)
	r.prune()
}

// prune drops closed windows that no open window can still overlap.
func (r *Windows) prune() {
	oldest := time.Now()
	for _, window := range r.state().windows {
		if window.closed.IsZero() && window.opened.Before(oldest) {
			oldest = window.opened
		}
	}
	r.state().windows = slices.DeleteFunc(r.state().windows, func(window *execWindow) bool {
		return !window.closed.IsZero() && window.closed.Before(oldest)
	})
	if len(r.state().windows) > maxExecWindows {
		for _, window := range r.state().windows[:len(r.state().windows)-maxExecWindows] {
			if window.tracking != nil {
				window.tracking.Close()
			}
			if window.previewCancel != nil {
				window.previewCancel()
			}
		}
		r.state().windows = r.state().windows[len(r.state().windows)-maxExecWindows:]
	}
}

// setSession names the continuation that keeps a yielded call running.
func (r *Windows) setSession(ref, session string) {
	if r == nil {
		return
	}
	r.state().mu.Lock()
	defer r.state().mu.Unlock()
	if window := r.find(ref); window != nil {
		window.session = session
	}
}

// markBackground marks windows of thread that are still open when a request
// for another turn arrives. They stop producing overlap tags.
func (r *Windows) markBackground(thread, turn string) {
	if r == nil || turn == "" {
		return
	}
	r.state().mu.Lock()
	defer r.state().mu.Unlock()
	for _, window := range r.state().windows {
		if window.thread == thread && window.turn != "" && window.turn != turn && window.closed.IsZero() && window.session != "" {
			window.background = true
			if window.previewCancel != nil {
				window.previewCancel()
				window.previewCancel = nil
			}
		}
	}
}

// execWindowView is what finalization learns about concurrent windows.
type execWindowView struct {
	created    []segmenttransport.Message
	overlaps   []string
	excluded   []string
	background []string
	// claims merge the windows' own claims; snapshot reports whether every
	// window is known and not background.
	claims   changecapture.Claims
	snapshot bool
}

func execRootsMeet(a, b []string) bool {
	for _, left := range a {
		for _, right := range b {
			if changes.PathWithin(left, right) || changes.PathWithin(right, left) {
				return true
			}
		}
	}
	return false
}

// close ends the windows of refs and reports the other windows on their
// roots that were open at any time during them. The refs are merged into one
// record, so they are neither overlaps nor exclusions of each other.
func (r *Windows) close(refs ...string) execWindowView {
	return r.view(refs, true)
}

// Intermediate display comparisons use the same isolation as terminal capture,
// without closing the window or claiming durable effects.
func (r *Windows) view(refs []string, complete bool) execWindowView {
	var view execWindowView
	if r == nil {
		return view
	}
	r.state().mu.Lock()
	defer r.state().mu.Unlock()
	now := time.Now()
	var selves []*execWindow
	for _, ref := range refs {
		if window := r.find(ref); window != nil {
			if window.tracking != nil {
				window.created = window.tracking.CreatedPaths()
			}
			view.created = append(view.created, window.created...)
			if complete && window.closed.IsZero() {
				if window.tracking != nil {
					window.tracking.Close()
				}
				window.tracking = nil
				window.closed = now
				if window.previewCancel != nil {
					window.previewCancel()
					window.previewCancel = nil
				}
			}
			selves = append(selves, window)
			view.claims = view.claims.Merge(window.claims)
		}
	}
	// A background window, or one lost to a restart or eviction, may span
	// unrelated work, so it takes no snapshot comparison.
	view.snapshot = len(selves) == len(refs) && !slices.ContainsFunc(selves, func(window *execWindow) bool { return window.background })
	for _, other := range r.state().windows {
		if slices.Contains(refs, other.ref) {
			continue
		}
		for _, self := range selves {
			if self.background {
				continue
			}
			until := self.closed
			if until.IsZero() {
				until = now
			}
			if !execRootsMeet(other.roots, self.roots) || !other.opened.Before(until) ||
				!other.closed.IsZero() && other.closed.Before(self.opened) {
				continue
			}
			if other.background {
				if other.closed.IsZero() {
					view.background = append(view.background, execBackgroundLabel(other.session))
				}
			} else {
				view.overlaps = append(view.overlaps, other.ref)
				view.excluded = append(view.excluded, other.paths...)
			}
			break
		}
	}
	return view
}

// claim hands recorded snapshot states to the still-open windows that
// overlapped refs, so their later comparisons do not record the same effect.
func (r *Windows) claim(refs []string, states changecapture.Claims) {
	if r == nil || len(states) == 0 {
		return
	}
	r.state().mu.Lock()
	defer r.state().mu.Unlock()
	var roots []string
	for _, ref := range refs {
		if window := r.find(ref); window != nil {
			roots = append(roots, window.roots...)
		}
	}
	for _, other := range r.state().windows {
		if slices.Contains(refs, other.ref) || !other.closed.IsZero() || !execRootsMeet(other.roots, roots) {
			continue
		}
		other.claims = other.claims.Merge(states)
	}
}

func execBackgroundLabel(session string) string {
	switch kind, id, _ := strings.Cut(session, ":"); kind {
	case "session":
		return "background session " + id
	case "cell":
		return "background cell " + id
	}
	return "a background command"
}

func (r *Windows) StopTurnPreviews(thread, turn string) {
	if r == nil || turn == "" {
		return
	}
	r.state().mu.Lock()
	defer r.state().mu.Unlock()
	for _, window := range r.state().windows {
		if window.thread == thread && window.turn == turn && window.previewCancel != nil {
			window.previewCancel()
			window.previewCancel = nil
		}
	}
}
