package router

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// Streamed output is estimated at this ratio until the provider reports usage.
const activityBytesPerToken = 4

// An initial view attachment that never completes returns activity inline.
const activityPaneLaunchWindow = 15 * time.Second

type activityPaneState int

const (
	activityPaneIdle activityPaneState = iota
	activityPaneClaimed
	activityPaneAttached
	activityPaneReleased
)

// The agents pane owns one root's child activity while it is claimed or
// attached. Events stay in the collector queue until a viewer write flushes,
// so a failed launch or closed viewer falls back to ordinary root drains.
type activityPane struct {
	ctx    context.Context
	launch func() bool

	state      activityPaneState
	root       string
	deadline   time.Time
	generation uint64
	sequence   uint64
	wake       chan struct{}
	announced  bool
	farewell   bool
}

type activityPaneEntry struct {
	Seq      uint64
	Agent    string
	Kind     string
	Text     string
	CallID   string              `json:",omitempty"`
	Filter   *exploreFilterEvent `json:",omitempty"`
	Observed time.Time

	event activityEvent // Original queue entry, requeued if the write fails.
}

type activityPaneAgent struct {
	Name         string
	Role         string    `json:",omitzero"`
	Responding   bool      `json:",omitzero"`
	Final        bool      `json:",omitzero"`
	Started      time.Time `json:",omitzero"`
	LastResponse time.Time `json:",omitzero"`
	Turns        uint64    `json:",omitzero"`
	Cost         float64   `json:",omitzero"`
	CostKnown    bool      `json:",omitzero"`
	CostPartial  bool      `json:",omitzero"`
	// Cumulative provider-reported tokens for the agent's responses.
	InputTokens  uint64 `json:",omitzero"`
	OutputTokens uint64 `json:",omitzero"`
}

type activityPaneEvent struct {
	Kind    string
	Agents  []activityPaneAgent `json:",omitempty"`
	Entries []activityPaneEntry `json:",omitempty"`
}

func newActivityPane(ctx context.Context, launch func() bool) *activityPane {
	return &activityPane{
		ctx: ctx, launch: launch,
		wake: make(chan struct{}, 1),
	}
}

func (a *subagentActivity) attachPane(pane *activityPane) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pane = pane
}

// paneOwnsLocked reports whether the pane currently holds this root's child
// activity. An expired initial attachment window releases ownership lazily.
func (a *subagentActivity) paneOwnsLocked(root string, now time.Time) bool {
	pane := a.pane
	if pane == nil || root == "" || pane.root != root {
		return false
	}
	if pane.state == activityPaneClaimed && !now.Before(pane.deadline) {
		pane.state = activityPaneReleased
	}
	return pane.state == activityPaneClaimed || pane.state == activityPaneAttached
}

// claimPaneLocked requests one launch for the first eligible activity under a root.
// A released pane is not relaunched in the same router session.
func (a *subagentActivity) claimPaneLocked(thread string, now time.Time) {
	pane := a.pane
	if pane == nil || pane.state != activityPaneIdle || pane.launch == nil {
		return
	}
	root := a.rootLocked(thread)
	if root == "" {
		return
	}
	if !pane.launch() {
		return
	}
	pane.state, pane.root, pane.deadline = activityPaneClaimed, root, now.Add(activityPaneLaunchWindow)
}

// releasePane returns ownership to inline delivery after a failed launch.
func (a *subagentActivity) releasePane() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pane != nil && a.pane.state != activityPaneIdle {
		a.pane.state = activityPaneReleased
	}
}

func (a *subagentActivity) wakePaneLocked() {
	if a.pane == nil {
		return
	}
	select {
	case a.pane.wake <- struct{}{}:
	default:
	}
}

// paneNoticesLocked returns the one-time root notices for pane ownership changes.
func (a *subagentActivity) paneNoticesLocked(root string) []map[string]json.RawMessage {
	pane := a.pane
	if pane == nil || pane.root != root {
		return nil
	}
	var text, key string
	switch {
	case pane.state == activityPaneAttached && !pane.announced:
		pane.announced = true
		text, key = "Agent activity is shown in the Mekugi agents pane beside this session.", "announce"
	case pane.state == activityPaneReleased && pane.announced && !pane.farewell:
		pane.farewell = true
		text, key = "The Mekugi agents pane closed; subagent activity resumes here.", "farewell"
	default:
		return nil
	}
	id := commentaryMessageID("activity-pane\x00" + root + "\x00" + key)
	a.copies[id] = struct{}{}
	return []map[string]json.RawMessage{assistantCommentaryMessage(id, text)}
}

// paneAgentsLocked lists the pane root and its observed children in observation order.
func (a *subagentActivity) paneAgentsLocked() []activityPaneAgent {
	var threads []string
	for thread, node := range a.threads {
		if !node.conflicted && a.rootLocked(thread) == a.pane.root {
			threads = append(threads, thread)
		}
	}
	slices.SortFunc(threads, func(x, y string) int { return a.threads[x].order - a.threads[y].order })
	agents := make([]activityPaneAgent, 0, len(threads))
	for _, thread := range threads {
		node := a.threads[thread]
		// Usage and cost come from the canonical per-thread report, the same
		// totals as the Markdown usage report; only streaming is estimated here.
		report, observed := a.usage.snapshot(thread)
		agents = append(agents, activityPaneAgent{
			Name: node.name, Role: node.role, Responding: node.responding > 0, Final: node.final,
			Started: node.started, LastResponse: node.lastResponse, Turns: node.turns,
			Cost:        report.cost.cachedInput + report.cost.uncachedInput + report.cost.output,
			CostKnown:   observed && report.cost.known && !report.Incomplete,
			CostPartial: report.missingUsage != 0,
			InputTokens: report.InputTokens, OutputTokens: report.OutputTokens + node.streamed/activityBytesPerToken,
		})
	}
	return agents
}

// takePane removes ready pane-owned events for the current viewer generation.
func (a *subagentActivity) takePane(generation uint64) ([]activityPaneEntry, []activityPaneAgent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pane := a.pane
	if a.closed || pane == nil || pane.generation != generation || pane.state != activityPaneAttached {
		return nil, nil, false
	}
	a.expireLocked(time.Now())
	var entries []activityPaneEntry
	kept := a.events[:0]
	for _, event := range a.events {
		if a.rootLocked(event.thread) != pane.root {
			kept = append(kept, event)
			continue
		}
		pane.sequence++
		entry := activityPaneEntry{
			Seq: pane.sequence, Agent: a.threads[event.thread].name, Kind: event.kind,
			Text: event.raw, Observed: event.observed, event: event,
		}
		entry.CallID = event.callID
		entry.Filter = event.filter

		entries = append(entries, entry)
	}
	clear(a.events[len(kept):])
	a.events = kept
	agents := a.paneAgentsLocked()
	return entries, agents, true
}

// restorePane requeues entries whose viewer write failed, ahead of later events.
func (a *subagentActivity) restorePane(entries []activityPaneEntry) {
	if len(entries) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	restored := make([]activityEvent, 0, len(entries)+len(a.events))
	for _, entry := range entries {
		event := entry.event
		// A newer queued operation already replaced this one.
		if (event.kind == "operation" || event.kind == "reasoning") && slices.ContainsFunc(a.events, func(e activityEvent) bool {
			return e.thread == event.thread && e.kind == event.kind && (event.kind != "reasoning" || event.source == e.source)
		}) {
			continue
		}
		restored = append(restored, event)
	}
	a.events = append(restored, a.events...)
	a.wakePaneLocked()
}

func (a *subagentActivity) subscribePane() (uint64, activityPaneEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pane := a.pane
	if a.closed || pane == nil || !a.paneOwnsLocked(pane.root, time.Now()) {
		return 0, activityPaneEvent{}, false
	}
	pane.generation++
	pane.state = activityPaneAttached
	return pane.generation, a.paneSnapshotLocked(), true
}

func (a *subagentActivity) paneSnapshotLocked() activityPaneEvent {
	return activityPaneEvent{Kind: "snapshot", Agents: a.paneAgentsLocked()}
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
		a.claimPaneLocked(thread, time.Now())
		a.wakePaneLocked()
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
		a.wakePaneLocked()
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
		a.wakePaneLocked()
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

// markFinal records a plaintext FINAL_ANSWER under the recipient's root. Its
// body is pane-only; native Codex already delivers the substantive completion.
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
	var threads []string
	for thread, node := range a.threads {
		if node.child && !node.conflicted && node.name == final.sender && a.rootLocked(thread) == root {
			node.final = true
			threads = append(threads, thread)
			a.wakePaneLocked()
		}
	}
	a.mu.Unlock()
	body := final.text
	if len(body) > maxCommentaryPublicationBytes/2 {
		body = strings.ToValidUTF8(body[:maxCommentaryPublicationBytes/2], "") + "\n… (full answer in Codex completion)"
	}
	for _, thread := range threads {
		a.collect(thread, final.source, "final", body)
	}
}

// divertRootReplies moves envelopes addressed to a pane-owned root into the
// pane queue. They return to inline delivery if the pane is released.
func (a *subagentActivity) divertRootReplies(root string, messages []map[string]json.RawMessage, senders []string) []map[string]json.RawMessage {
	if a == nil || len(messages) == 0 {
		return messages
	}
	a.mu.Lock()
	owned := a.paneOwnsLocked(root, time.Now())
	a.mu.Unlock()
	var kept []map[string]json.RawMessage
	for i, message := range messages {
		thread := a.childThread(root, senders[i])
		// A retried request must not repeat a reply the collector already holds.
		if !owned {
			if !a.collected(thread, jsonString(message, "id")) {
				kept = append(kept, message)
			}
			continue
		}
		var content []struct {
			Text string `json:"text"`
		}
		if thread == "" || json.Unmarshal(message["content"], &content) != nil || len(content) != 1 {
			kept = append(kept, message)
			continue
		}
		a.collect(thread, jsonString(message, "id"), "reply", content[0].Text)
	}
	return kept
}

func (a *subagentActivity) collected(thread, source string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	node := a.threads[thread]
	if node == nil || source == "" {
		return false
	}
	_, exists := node.seen[commentaryMessageID(source)]
	return exists
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

// activityPaneText removes the collector's author prefix; the pane shows the
// agent separately. Directed envelope headers remain part of the text.
func activityPaneText(name, text string) string {
	if hasCommentaryAuthor(text, name) {
		return strings.TrimPrefix(text, "["+commentaryCode(name)+"] ")
	}
	return text
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
	a.wakePaneLocked()
}
