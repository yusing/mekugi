package router

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxExecWindows = 1024

// execWindow is the observation window of one writer call: from forwarding
// the call to observing its terminal result. The registry is in memory, so
// after a restart, records carry no overlap tags for windows opened before it.
type execWindow struct {
	ref    string
	roots  []string
	thread string
	turn   string
	// group identifies the response that emitted the call.
	group  string
	paths  []string
	opened time.Time
	closed time.Time
	// session names the host session or cell that keeps a yielded window open.
	session       string
	background    bool
	previewCancel context.CancelFunc
	// claims are snapshot states that overlapping calls already recorded.
	// This window's snapshot comparison starts from them.
	claims workspaceSnapshotClaims
}

type execWindowRegistry struct {
	mu      sync.Mutex
	windows []*execWindow
	tracker *execTrackHub
}

func (r *execWindowRegistry) find(ref string) *execWindow {
	for _, window := range r.windows {
		if window.ref == ref {
			return window
		}
	}
	return nil
}

func (r *execWindowRegistry) open(window *execWindow) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.find(window.ref) != nil {
		return
	}
	window.opened = time.Now()
	r.windows = append(r.windows, window)
	r.prune()
}

// prune drops closed windows that no open window can still overlap.
func (r *execWindowRegistry) prune() {
	oldest := time.Now()
	for _, window := range r.windows {
		if window.closed.IsZero() && window.opened.Before(oldest) {
			oldest = window.opened
		}
	}
	r.windows = slices.DeleteFunc(r.windows, func(window *execWindow) bool {
		return !window.closed.IsZero() && window.closed.Before(oldest)
	})
	if len(r.windows) > maxExecWindows {
		for _, window := range r.windows[:len(r.windows)-maxExecWindows] {
			if window.previewCancel != nil {
				window.previewCancel()
			}
		}
		r.windows = r.windows[len(r.windows)-maxExecWindows:]
	}
}

// setSession names the continuation that keeps a yielded call running.
func (r *execWindowRegistry) setSession(ref, session string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if window := r.find(ref); window != nil {
		window.session = session
	}
}

// markBackground marks windows of thread that are still open when a request
// for another turn arrives. They stop producing overlap tags.
func (r *execWindowRegistry) markBackground(thread, turn string) {
	if r == nil || turn == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, window := range r.windows {
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
	overlaps   []string
	excluded   []string
	background []string
	// claims merge the windows' own claims; snapshot reports whether every
	// window is known and not background.
	claims   workspaceSnapshotClaims
	snapshot bool
}

func execRootsMeet(a, b []string) bool {
	for _, left := range a {
		for _, right := range b {
			if execPathWithin(left, right) || execPathWithin(right, left) {
				return true
			}
		}
	}
	return false
}

func execPathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// close ends the windows of refs and reports the other windows on their
// roots that were open at any time during them. The refs are merged into one
// record, so they are neither overlaps nor exclusions of each other.
func (r *execWindowRegistry) close(refs ...string) execWindowView {
	var view execWindowView
	if r == nil {
		return view
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var selves []*execWindow
	for _, ref := range refs {
		if window := r.find(ref); window != nil {
			if window.closed.IsZero() {
				window.closed = now
				if window.previewCancel != nil {
					window.previewCancel()
					window.previewCancel = nil
				}
			}
			selves = append(selves, window)
			view.claims = view.claims.merge(window.claims)
		}
	}
	// A background window, or one lost to a restart or eviction, may span
	// unrelated work, so it takes no snapshot comparison.
	view.snapshot = len(selves) == len(refs) && !slices.ContainsFunc(selves, func(window *execWindow) bool { return window.background })
	for _, other := range r.windows {
		if slices.Contains(refs, other.ref) {
			continue
		}
		for _, self := range selves {
			if self.background {
				continue
			}
			if !execRootsMeet(other.roots, self.roots) || !other.opened.Before(self.closed) ||
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
func (r *execWindowRegistry) claim(refs []string, states workspaceSnapshotClaims) {
	if r == nil || len(states) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var roots []string
	for _, ref := range refs {
		if window := r.find(ref); window != nil {
			roots = append(roots, window.roots...)
		}
	}
	for _, other := range r.windows {
		if slices.Contains(refs, other.ref) || !other.closed.IsZero() || !execRootsMeet(other.roots, roots) {
			continue
		}
		other.claims = other.claims.merge(states)
	}
}

// merge adds claims to c, allocating it when needed.
func (c workspaceSnapshotClaims) merge(claims workspaceSnapshotClaims) workspaceSnapshotClaims {
	for domain, states := range claims {
		if c == nil {
			c = make(workspaceSnapshotClaims)
		}
		if c[domain] == nil {
			c[domain] = make(map[string]workspaceSnapshotEntry, len(states))
		}
		maps.Copy(c[domain], states)
	}
	return c
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
