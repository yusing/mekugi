package router

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"
)

// Presentation state is independent of executable-call recovery. Identities and
// delivered IDs remain available until shutdown. Bounded live queues drop only
// auxiliary updates with a notice, without disabling later updates or exposing old commentary.
type subagentActivity struct {
	notice  func(string, string)
	mu      sync.Mutex
	threads map[string]*activityThread
	copies  map[string]struct{}
	events  []activityEvent
	closed  bool
	order   int
	pane    *activityPane
}

type activityThread struct {
	parent, name          string
	child, conflicted     bool
	seen                  map[string]struct{}
	order, responding     int
	final                 bool
	started, lastResponse time.Time
	turns                 uint64
	cost                  tokenCost
	paneVisible           bool // Root joins the roster after its first pane-only filter event.
	// Provider-reported usage summed over this thread's responses, and the
	// visible delta bytes streamed since the last report.
	inputTokens, outputTokens, streamed uint64
}

type activityEvent struct {
	thread, source, kind, text string
	callID                     string
	raw                        string // Unattributed text for the agents pane.
	observed                   time.Time
	filter                     *exploreFilterEvent
}

func newSubagentActivity() *subagentActivity {
	return &subagentActivity{threads: make(map[string]*activityThread), copies: make(map[string]struct{})}
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
	a.threads[thread] = &activityThread{parent: parent, name: name, child: child, seen: make(map[string]struct{}), order: a.order, started: time.Now(), cost: tokenCost{known: true}}
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
// Keep local runtime authors and replay provenance intact; suppress root copies.
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

func (a *subagentActivity) collect(thread, source, kind, text string) {
	a.collectEvent(activityEvent{thread: thread, source: source, kind: kind, text: text})
}

func (a *subagentActivity) collectEvent(event activityEvent) {
	thread, source, kind, text := event.thread, event.source, event.kind, event.text
	if a == nil || source == "" || len(source) > maxCommentaryPublicationBytes || strings.TrimSpace(text) == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	node := a.threads[thread]
	if a.closed || node == nil || !node.child && kind != "output_filter" || node.conflicted {
		return
	}
	callID := event.callID
	if kind == "tool" {
		callID, _ = strings.CutPrefix(source, "tool-call\x00")
	} else if kind == "exit" {
		callID, _ = strings.CutPrefix(source, "tool-exit\x00")
	}
	source = commentaryMessageID(source)
	if _, exists := node.seen[source]; exists {
		return
	}
	header, _, directed := strings.Cut(text, "] ")
	raw := activityPaneText(node.name, text)
	directed = directed && strings.HasPrefix(header, "[") &&
		(strings.HasPrefix(header, "["+commentaryCode(node.name)+" -> ") ||
			strings.HasSuffix(header, " -> "+commentaryCode(node.name)))
	if kind == "operation" || !directed {
		text = attributedCommentary(node.name, text)
	}
	if len(text) > maxCommentaryPublicationBytes {
		return
	}
	now := time.Now()
	a.expireLocked(now)
	// Keep the latest ordinary operation, but preserve distinct notices. Appending
	// the replacement after notices preserves each child's observation order.
	if kind == "operation" {
		a.events = slices.DeleteFunc(a.events, func(e activityEvent) bool { return e.thread == thread && e.kind == kind })
	}
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
	node.paneVisible = true
	event.source, event.callID, event.text, event.raw, event.observed = source, callID, text, raw, now
	a.events = append(a.events, event)
	a.claimPaneLocked(thread, now)
	if a.paneOwnsLocked(a.rootLocked(thread), now) {
		a.wakePaneLocked()
	}
}

func (a *subagentActivity) expireLocked(now time.Time) {
	a.events = slices.DeleteFunc(a.events, func(e activityEvent) bool { return now.Sub(e.observed) >= commentaryRouteTTL })
}

func (a *subagentActivity) drain(root string, started time.Time, budget int) []map[string]json.RawMessage {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	node := a.threads[root]
	if node == nil || node.child || a.rootLocked(root) != root {
		return nil
	}
	now := time.Now()
	a.expireLocked(now)
	// Pane-owned child activity stays queued for the viewer; the root receives
	// only one-time ownership notices until the pane is released.
	owned := a.paneOwnsLocked(root, now)
	messages := a.paneNoticesLocked(root)
	if owned {
		return messages
	}
	for _, message := range messages {
		budget -= len(message["content"])
	}
	kept := a.events[:0]
	blocked := make(map[string]bool)
	for index := 0; index < len(a.events); index++ {
		event := a.events[index]
		if a.rootLocked(event.thread) != root {
			kept = append(kept, event)
			continue
		}
		if event.kind == "final" || event.kind == "exit" || event.kind == "output_filter" {
			continue // Final is native; exit and filter metrics are pane-only.
		}
		text := event.text
		author := "[" + commentaryCode(a.threads[event.thread].name) + "] "
		// Omit oversized events rather than blocking later activity until expiry.
		if len(text) > maxCommentaryPublicationBytes {
			continue
		}
		if blocked[event.thread] || len(text) > budget {
			blocked[event.thread] = true
			kept = append(kept, event)
			continue
		}
		// Group ready calls by author without waiting for more activity.
		if event.kind == "tool" {
			grouped := strings.TrimPrefix(event.text, author)
			text = toolActivityGroup(author, grouped)
			if len(text) > budget || len(text) > maxCommentaryPublicationBytes {
				text = event.text
			}
			for index+1 < len(a.events) {
				next := a.events[index+1]
				if next.thread != event.thread || next.kind != "tool" || next.observed.Before(started) != event.observed.Before(started) {
					break
				}
				combined := grouped + "\n\n" + strings.TrimPrefix(next.text, author)
				rendered := toolActivityGroup(author, combined)
				if len(rendered) > budget || len(rendered) > maxCommentaryPublicationBytes {
					break
				}
				text = rendered
				grouped = combined
				index++
			}
		}
		id := commentaryMessageID("root-copy\x00" + root + "\x00" + event.thread + "\x00" + event.source)
		a.copies[id] = struct{}{}
		messages = append(messages, assistantCommentaryMessage(id, text))
		budget -= len(text)
	}
	clear(a.events[len(kept):])
	a.events = kept
	return messages
}

// Root copies can be inherited by a newly forked child before its first request
// establishes ancestry. Exact generated IDs are user-only in every replay, even
// though delivery itself always requires an observed root relationship.
func (a *subagentActivity) stripInput(fields map[string]json.RawMessage) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.copies) == 0 {
		return
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(fields["input"], &items) != nil {
		return
	}
	original := len(items)
	items = slices.DeleteFunc(items, func(item map[string]json.RawMessage) bool {
		_, owned := a.copies[jsonString(item, "id")]
		return owned && jsonString(item, "type") == "message" && jsonString(item, "role") == "assistant"
	})
	if len(items) != original {
		fields["input"] = mustMarshalJSON(items)
	}
}

func (t *mekugiResponseTransform) drainActivity() []map[string]json.RawMessage {
	messages := t.proxy.activity.drain(t.threadID, t.activityStarted, maxCommentaryPublicationBytes-t.activityBytes)
	for _, message := range messages {
		t.featureTrace.record("commentary", "router_activity", "render", "prepared", "", jsonString(message, "id"))
		var content []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(message["content"], &content) == nil && len(content) == 1 {
			t.activityBytes += len(content[0].Text)
		}
	}
	return messages
}

func (a *subagentActivity) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	clear(a.threads)
	clear(a.copies)
	a.events = nil
}
