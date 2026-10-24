package sessioncontrol

import (
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/scope"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

const MaxActivityMetadataBytes = 16 << 10

// Source: apps/mekugi/internal/router/subagent_activity{,_pane}.go@cf02d9b2.
// Audience authenticates auxiliary delivery independently of presentation.
// Presentation state is independent of executable-call recovery. Identities and
// delivered IDs remain available until shutdown. Bounded live queues drop only
// auxiliary updates with a notice, without disabling later updates or replaying expired activity.
type Audience struct {
	notice        func(string, string)
	mu            sync.Mutex
	threads       map[string]*activityThread
	closed        bool
	invocation    scope.Store
	supplements   *activitypresentation.Supplements
	prepared      []preparedContent
	preparedBytes int
}

type activityThread struct {
	parent, name      string
	child, conflicted bool
	state             scope.Store
}

func NewAudience(notice func(string, string)) *Audience {
	a := &Audience{notice: notice, threads: make(map[string]*activityThread)}
	a.supplements = activitypresentation.RequestSupplements(&a.invocation)
	return a
}

func (a *Audience) Observe(thread, parent, name string, child bool) bool {
	if a == nil || thread == "" {
		return false
	}
	if len(thread)+len(parent)+len(name) > MaxActivityMetadataBytes || strings.ContainsAny(name, "\r\n\x00") ||
		child && (parent == "" || parent == thread || !strings.HasPrefix(name, "/root/")) ||
		!child && (parent != "" || name != "/root") {
		a.Invalidate(thread)
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	if old := a.threads[thread]; old != nil {
		if old.parent != parent || old.name != name || old.child != child {
			old.conflicted = true
		}
		return !old.conflicted
	}
	a.threads[thread] = &activityThread{parent: parent, name: name, child: child}
	return true
}

func (a *Audience) rootLocked(thread string) string {
	for range len(a.threads) {
		node := a.threads[thread]
		if node == nil || node.conflicted {
			return ""
		}
		if !node.child || a.supplements.Attached(thread) {
			return thread
		}
		thread = node.parent
	}
	return "" // Cycles and unknown ancestry never broadcast.
}

// Shell workers share stable thread capabilities across requests. Once an
// accepted request makes that thread's identity ambiguous, later publications
// cannot safely use its earlier ancestry, even after another valid turn.
// Keep local runtime authors and replay provenance intact; suppress ambiguous activity.
func (a *Audience) Invalidate(thread string) {
	if a == nil || thread == "" || len(thread) > MaxActivityMetadataBytes {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	if node := a.threads[thread]; node != nil {
		node.conflicted = true
	} else {
		// An initially ambiguous capability must not acquire ancestry later.
		a.threads[thread] = &activityThread{conflicted: true}
	}
}

// Collect admits a batch under the same authenticated ancestry snapshot.
func (a *Audience) Collect(events ...activitypresentation.Supplement) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, event := range events {
		node := a.threads[event.Thread]
		if a.closed || node == nil || node.conflicted {
			continue
		}
		if a.supplements.Collect(&node.state, event, time.Now()) && a.notice != nil {
			a.notice("activity_capacity", "Mekugi omitted child-activity updates because its queue is full (1,024 total, 64 per child). Child execution and results are unchanged; updates resume when the queue drains.")
		}
	}
}

// audienceLocked preserves unknown versus invalid ancestry for notice admission.
func (a *Audience) audienceLocked(root string) (activitypresentation.Audience, bool) {
	audience := activitypresentation.Audience{Root: root, Threads: make(map[string]string)}
	if a.threads[root] != nil && a.rootLocked(root) != root {
		return audience, false
	}
	for thread, node := range a.threads {
		if a.rootLocked(thread) == root {
			audience.Threads[thread] = node.name
		}
	}
	return audience, true
}

func (a *Audience) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	clear(a.threads)
	a.prepared, a.preparedBytes = nil, 0
	a.supplements.Clear()
}
func (a *Audience) Detach() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.supplements.Detach()
}

// Main already knows its app-server thread before the first provider request.
// The native frontend binds supplemental observations to its active root.
// App-server notifications supply the live tool and lifecycle display.
func (a *Audience) Attach(root string) {
	a.Observe(root, "", "/root", false)
	a.AttachRuntime(root)
}

// AttachRuntime borrows an independent runtime without assigning a root-shaped
// identity. Its admitted request supplies the canonical logical membership.
func (a *Audience) AttachRuntime(root string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.supplements.Attach(root)
}

// A thinking response starts Conversation's pre-delta block. Host observations,
// not provider rounds, own the roster lifecycle.
func (a *Audience) BeginResponse(thread string, thinking bool, at time.Time) {
	if a == nil || !thinking {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; node != nil && !node.conflicted && !a.closed {
		a.supplements.StartRequest(thread, at)
	}
}

func (a *Audience) TakeStarts(root string) []activitypresentation.RequestStart {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	audience, _ := a.audienceLocked(root)
	return a.supplements.TakeStarts(audience)
}

func (a *Audience) NoteUnreturned(thread string, calls []string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.supplements.NoteUnreturned(thread, calls)
	}
}

func (a *Audience) TakeUnreturned(root string) []activitypresentation.Unreturned {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	audience, _ := a.audienceLocked(root)
	return a.supplements.TakeUnreturned(audience)
}

// Snapshot resolves the current audience without exposing its mutable registry.
func (a *Audience) Snapshot(root string) (activitypresentation.Audience, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.audienceLocked(root)
}

func (a *Audience) Take(root string) (activitypresentation.Audience, []activitypresentation.Supplement) {
	if a == nil {
		return activitypresentation.Audience{}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return activitypresentation.Audience{}, nil
	}
	audience, _ := a.audienceLocked(root)
	return audience, a.supplements.Take(audience, time.Now())
}

// Repetition resolves a retained annotation for one arriving/restored source.
// It does not replay the supplement queue or publish unrelated history.
func (a *Audience) Repetition(root, thread, id string) string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || root == "" || a.rootLocked(thread) != root {
		return ""
	}
	node := a.threads[thread]
	return a.supplements.Repetition(root, &node.state, id)
}

// BorrowSide admits the active work-capable side without detaching Main.
func (a *Audience) BorrowSide(root string) {
	if a == nil {
		return
	}
	if root != "" {
		a.Observe(root, "", "/root", false)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.supplements.Borrow(root)
}
