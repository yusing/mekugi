package router

import (
	"time"
)

// The only activity frontend is the native app-server UI.
type activityPane struct{ root string }

type activityPaneEntry struct {
	Seq      uint64
	Agent    string
	Kind     string
	Text     string
	CallID   string              `json:",omitempty"`
	Filter   *exploreFilterEvent `json:",omitempty"`
	Observed time.Time

	native       *liveActivityNativeItem // In-process app-server lifecycle input, not a provider observation.
	journal      *journalItem            // Native presentation keeps IDs/questions separate from rendered text.
	journalItems []journalItem           // One terminal delivery uses Activity's existing grouped result renderer.
	assignment   *activityAssignment     // Validated native NEW_TASK, not an ordinary message.
	activitySeq  uint64                  // Main excerpt's exact entry in Activity, never a question target.
	outputTail   []string                // Failed command's sanitized final output lines, on an exit entry.
	outputOmit   int                     // Output lines before outputTail.
}

type activityPaneAgent struct {
	Name         string
	Role         string    `json:",omitzero"`
	Responding   bool      `json:",omitzero"`
	Final        bool      `json:",omitzero"`
	Started      time.Time `json:",omitzero"`
	LastResponse time.Time `json:",omitzero"`
	Turns        uint64    `json:",omitzero"`
	// Provider requests forwarded for the agent, shown as T+N.
	Roundtrips  uint64  `json:",omitzero"`
	Cost        float64 `json:",omitzero"`
	CostKnown   bool    `json:",omitzero"`
	CostPartial bool    `json:",omitzero"`
	// Cumulative provider-reported tokens for the agent's responses.
	InputTokens  uint64 `json:",omitzero"`
	OutputTokens uint64 `json:",omitzero"`
	// Latest host-reported context, not cumulative provider usage.
	ContextTokens uint64 `json:",omitzero"`
	ContextWindow uint64 `json:",omitzero"`
	ContextKnown  bool   `json:",omitzero"`
}

type activityPaneEvent struct {
	Kind    string
	Agents  []activityPaneAgent `json:",omitempty"`
	Entries []activityPaneEntry `json:",omitempty"`
}

func (a *subagentActivity) releasePane() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pane = nil
}

// Main already knows its app-server thread before the first provider request.
// The native frontend binds supplemental observations to its active root.
// App-server notifications supply the live tool and lifecycle display.
func (a *subagentActivity) attachNativePane(root string) {
	a.observe(root, "", "/root", false)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pane = &activityPane{root: root}
}

// beginResponse and endResponse track open provider responses for the roster.
// A new response clears an earlier final-answer marker.
func (a *subagentActivity) beginResponse(thread string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; node != nil && !node.conflicted && !a.closed {
		node.responding++
		node.turns++
		node.final = false
	}
}

func (a *subagentActivity) endResponse(thread string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; node != nil && node.responding > 0 && !a.closed {
		node.responding--
		node.lastResponse = time.Now()
		// A response that ended without usage leaves no estimate behind.
		if node.responding == 0 {
			node.streamed = 0
		}
	}
}

// syncUsage drops the streamed estimate once the canonical usage report
// includes the response.
func (a *subagentActivity) syncUsage(thread string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; node != nil && !node.conflicted && !a.closed {
		node.streamed = 0
	}
}

// streamOutput adds visible streamed delta bytes to an agent's output estimate.
// The pane picks it up on its next tick rather than waking per delta.
func (a *subagentActivity) streamOutput(thread string, bytes int) {
	if a == nil || bytes == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; node != nil && !node.conflicted && !a.closed {
		node.streamed += uint64(bytes)
	}
}

// markFinal records the observed plaintext FINAL_ANSWER marker under its root.
// App-server delivers the substantive completion body.
func (a *subagentActivity) markFinal(recipientThread string, final subagentFinal) {
	if a == nil {
		return
	}
	a.mu.Lock()
	root := a.rootLocked(recipientThread)
	if a.closed || root == "" {
		a.mu.Unlock()
		return
	}
	for thread, node := range a.threads {
		if node.child && !node.conflicted && node.name == final.sender && a.rootLocked(thread) == root {
			node.final = true
		}
	}
	a.mu.Unlock()
}

func (a *subagentActivity) childThread(root, name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for thread, node := range a.threads {
		if node.child && !node.conflicted && node.name == name && a.rootLocked(thread) == root {
			return thread
		}
	}
	return ""
}

func (a *subagentActivity) syncPaneRoles(parent string, roles map[string]journalSpawnRole) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, node := range a.threads {
		if node.parent != parent || node.conflicted {
			continue
		}
		// Missing or conflicting evidence leaves the role blank; the roster omits it.
		node.role = ""
		if evidence, ok := roles[node.name]; ok && !evidence.Conflicted {
			node.role = evidence.Role
		}
	}
}

func nativeObservedActivity(kind string) bool {
	return kind == "output_filter" || kind == "reply" || kind == "start" || kind == "assignment"
}

// takeNativeActivity drains router-owned annotations and authenticated input
// messages that V2 app-server does not expose. Lifecycle stays app-server owned.
func (a *subagentActivity) takeNativeActivity(root string) []activityPaneEntry {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.pane == nil || a.pane.root != root {
		return nil
	}
	a.expireLocked(time.Now())
	var entries []activityPaneEntry
	kept := a.events[:0]
	for _, event := range a.events {
		node := a.threads[event.thread]
		if !nativeObservedActivity(event.kind) || node == nil || node.conflicted || a.rootLocked(event.thread) != root {
			kept = append(kept, event)
			continue
		}
		entries = append(entries, activityPaneEntry{Agent: node.name, Kind: event.kind, Text: event.raw, CallID: event.callID, Filter: event.filter, Observed: event.observed, assignment: event.assignment})
	}
	clear(a.events[len(kept):])
	a.events = kept
	return entries
}
