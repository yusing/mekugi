package router

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// Bound native message display independently of authored journal publications.
// Clipping is explicit in Activity.
const maxNativeActivityMessageBytes = 64 << 10

// Presentation state is independent of executable-call recovery. Identities and
// delivered IDs remain available until shutdown. Bounded live queues drop only
// auxiliary updates with a notice, without disabling later updates or replaying expired activity.
type subagentActivity struct {
	notice  func(string, string)
	mu      sync.Mutex
	threads map[string]*activityThread
	events  []activityEvent
	closed  bool
	order   int
	pane    *activityPane
	// starts are forwarded requests whose provider streams untitled
	// reasoning, awaiting the native UI's pre-delta thinking block.
	starts []activityRequestStart
	// unreturned are nested commands whose Code Mode cell returned nothing,
	// awaiting the native UI's note that the model never saw their output.
	unreturned []activityToolRef
	// usage is the canonical per-thread accounting the roster displays.
	usage *threadUsage
	// restored counts messages that resumed history already shows. A later
	// full-history request carries them again under this router's new
	// identities; each count absorbs one such repeat.
	restored map[string]int
}

type activityThread struct {
	parent, name          string
	role                  string
	child, conflicted     bool
	seen                  map[string]struct{}
	order, responding     int
	final                 bool
	started, lastResponse time.Time
	turns                 uint64
	// Visible delta bytes streamed since the thread's last usage report.
	streamed uint64
}

type activityToolRef struct{ thread, call string }

type activityRequestStart struct {
	thread string
	at     time.Time
}

type activityEvent struct {
	thread, source, kind, text string
	callID                     string
	raw                        string // Unattributed text for the agents pane.
	observed                   time.Time
	queued                     time.Time // Queue retention is independent of original message time.
	assignment                 *activityAssignment
	message                    *activityMessage
	start                      *activityStart
}

func newSubagentActivity() *subagentActivity {
	return &subagentActivity{threads: make(map[string]*activityThread)}
}

func (a *subagentActivity) observe(thread, parent, name string, child bool) bool {
	if a == nil || thread == "" {
		return false
	}
	if len(thread)+len(parent)+len(name) > maxCommentaryPublicationBytes || strings.ContainsAny(name, "\r\n\x00") ||
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
	a.order++
	a.threads[thread] = &activityThread{parent: parent, name: name, child: child, seen: make(map[string]struct{}), order: a.order, started: time.Now()}
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
	if a == nil || thread == "" || len(thread) > maxCommentaryPublicationBytes {
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
	thread, source, kind, text := event.thread, event.source, event.kind, event.text
	// A message may be opaque and a start carries only its settings.
	if source == "" || len(source) > maxCommentaryPublicationBytes || strings.TrimSpace(text) == "" && event.message == nil && event.start == nil {
		return
	}
	node := a.threads[thread]
	if a.closed || node == nil || node.conflicted {
		return
	}
	// The native app-server owns commands, reasoning, authored messages and
	// lifecycle. Only annotations and authenticated input bodies need the router.
	if !nativeObservedActivity(kind) {
		return
	}
	source = commentaryMessageID(source)
	if _, exists := node.seen[source]; exists {
		return
	}
	if key := restoredActivityKey(event, thread); key != "" && a.restored[key] > 0 {
		a.restored[key]--
		node.seen[source] = struct{}{}
		return
	}
	raw := clipNativeActivityMessage(text)
	if event.message != nil {
		message := *event.message
		message.text = clipNativeActivityMessage(message.text)
		event.message = &message
	}
	now := time.Now()
	a.expireLocked(now)
	count := 0
	for _, event := range a.events {
		if event.thread == thread {
			count++
		}
	}
	if len(a.events) >= maxCommentaryEvents || count >= maxCommentaryEventsPerRoute {
		if a.notice != nil {
			a.notice("activity_capacity", "Mekugi omitted child-activity updates because its queue is full (1,024 total, 64 per child). Child execution and results are unchanged; updates resume when the queue drains.")
		}
		return
	}
	node.seen[source] = struct{}{}
	if kind == "start" && event.assignment != nil && event.assignment.id != "" {
		node.seen[commentaryMessageID(event.assignment.id)] = struct{}{}
	}
	event.source, event.text, event.raw, event.queued = source, raw, raw, now
	if event.observed.IsZero() {
		event.observed = now
	}
	a.events = append(a.events, event)
}

func clipNativeActivityMessage(text string) string {
	if len(text) <= maxNativeActivityMessageBytes {
		return text
	}
	const clipped = "\n… (message clipped at the native Activity 64 KiB display limit)"
	return strings.ToValidUTF8(text[:maxNativeActivityMessageBytes-len(clipped)], "") + clipped
}

// markRestored records a message that restored Activity shows and that the
// thread's later requests carry again.
func (a *subagentActivity) markRestored(thread string, entry activityPaneEntry) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.restored == nil {
		a.restored = make(map[string]int)
	}
	event := activityEvent{kind: entry.Kind, assignment: entry.assignment, message: entry.message}
	if event.kind == "start" {
		a.restored[restoredActivityKey(event, thread)]++
		event.kind = "assignment" // Its task is also a restored assignment.
	}
	if key := restoredActivityKey(event, thread); key != "" {
		a.restored[key]++
	}
}

// restoredActivityKey identifies a repeated message by its thread's start or
// by content: request input need not carry the host item IDs that live source
// identities include. Other kinds are never repeated.
func restoredActivityKey(event activityEvent, thread string) string {
	switch {
	case event.kind == "start":
		return "start\x00" + thread
	case event.kind == "assignment" && event.assignment != nil:
		return "assignment\x00" + event.assignment.from + "\x00" + event.assignment.to + "\x00" + event.assignment.text
	case event.kind == "reply" && event.message != nil:
		return "reply\x00" + event.message.from + "\x00" + event.message.to + "\x00" + event.message.text
	}
	return ""
}

func (a *subagentActivity) expireLocked(now time.Time) {
	a.events = slices.DeleteFunc(a.events, func(e activityEvent) bool { return now.Sub(e.queued) >= commentaryRouteTTL })
}

func (a *subagentActivity) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	clear(a.threads)
	a.events = nil
}
