package router

import (
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/scope"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

const (
	maxActivityMetadataBytes   = 16 << 10
	maxActivityEventsPerThread = 64
)

// Presentation state is independent of executable-call recovery. Identities and
// delivered IDs remain available until shutdown. Bounded live queues drop only
// auxiliary updates with a notice, without disabling later updates or replaying expired activity.
type subagentActivity struct {
	notice      func(string, string)
	mu          sync.Mutex
	threads     map[string]*activityThread
	closed      bool
	invocation  scope.Store
	supplements *activitypresentation.Supplements
}

type activityThread struct {
	parent, name      string
	child, conflicted bool
	state             scope.Store
}

type activityToolRef struct{ thread, call string }

type activityRequestStart struct {
	thread string
	at     time.Time
}

type activityEvent struct {
	thread, source, kind, text string
	callID                     string
	observed                   time.Time
	assignment                 *activityAssignment
	message                    *activityMessage
	start                      *activityStart
}

func newSubagentActivity() *subagentActivity {
	a := &subagentActivity{threads: make(map[string]*activityThread)}
	a.supplements = activitypresentation.RequestSupplements(&a.invocation)
	return a
}

func (a *subagentActivity) observe(thread, parent, name string, child bool) bool {
	if a == nil || thread == "" {
		return false
	}
	if len(thread)+len(parent)+len(name) > maxActivityMetadataBytes || strings.ContainsAny(name, "\r\n\x00") ||
		child && (parent == "" || parent == thread || !strings.HasPrefix(name, "/root/")) ||
		!child && (parent != "" || name != "/root") {
		a.invalidate(thread)
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

func (a *subagentActivity) rootLocked(thread string) string {
	for range len(a.threads) {
		node := a.threads[thread]
		if node == nil || node.conflicted {
			return ""
		}
		if !node.child {
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
func (a *subagentActivity) invalidate(thread string) {
	if a == nil || thread == "" || len(thread) > maxActivityMetadataBytes {
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

func (a *subagentActivity) collectEvent(event activityEvent) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.collectEventLocked(event)
}

func (a *subagentActivity) collectEventLocked(event activityEvent) {
	node := a.threads[event.thread]
	if a.closed || node == nil || node.conflicted {
		return
	}
	if a.supplements.Collect(&node.state, event.supplement(), time.Now()) && a.notice != nil {
		a.notice("activity_capacity", "Mekugi omitted child-activity updates because its queue is full (1,024 total, 64 per child). Child execution and results are unchanged; updates resume when the queue drains.")
	}
}

func (event activityEvent) supplement() activitypresentation.Supplement {
	value := activitypresentation.Supplement{Thread: event.thread, Source: event.source, Kind: event.kind, Text: event.text, CallID: event.callID, Observed: event.observed}
	if a := event.assignment; a != nil {
		value.Assignment = &activitypresentation.SupplementAssignment{ID: a.id, From: a.from, To: a.to, Text: a.text, Created: a.created}
	}
	if m := event.message; m != nil {
		value.Message = &activitypresentation.SupplementMessage{From: m.from, To: m.to, Text: m.text}
	}
	if s := event.start; s != nil {
		value.Start = &activitypresentation.SupplementStart{Model: s.model, Effort: s.effort, Tier: s.tier}
	}
	return value
}

func (a *subagentActivity) markRestored(thread string, entry activityPaneEntry) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.supplements.Restore((activityEvent{thread: thread, kind: entry.Kind, assignment: entry.assignment, message: entry.message}).supplement())
}

// audienceLocked preserves unknown versus invalid ancestry for notice admission.
func (a *subagentActivity) audienceLocked(root string) (activitypresentation.Audience, bool) {
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

func (a *subagentActivity) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	clear(a.threads)
	a.supplements.Clear()
}
