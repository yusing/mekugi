package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

const liveActivityEventsPath = "/internal/live-activity"

const (
	// A launch that never connects, or a viewer that stays disconnected past the
	// reconnect window, returns ownership to inline root delivery.
	activityPaneLaunchWindow = 15 * time.Second
	activityPaneGraceWindow  = 5 * time.Second
	activityPaneHistory      = 256
	// Batches and the reconnect snapshot stay well inside one event line.
	activityPaneLineBudget = maxLiveDiffEventBytes / 2
)

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
	ctx        context.Context
	launch     func() bool
	connection liveDiffConnection

	state      activityPaneState
	root       string
	deadline   time.Time
	generation uint64
	sequence   uint64
	history    []activityPaneEntry
	historyLen int // Encoded bytes of history.
	wake       chan struct{}
	announced  bool
	farewell   bool
}

type activityPaneEntry struct {
	Seq      uint64
	Agent    string
	Kind     string
	Text     string
	Observed time.Time

	event activityEvent // Original queue entry, requeued if the write fails.
	size  int           // Encoded bytes.
}

type activityPaneAgent struct {
	Name       string
	Responding bool `json:",omitzero"`
	Final      bool `json:",omitzero"`
}

type activityPaneEvent struct {
	Kind    string
	Agents  []activityPaneAgent `json:",omitempty"`
	Entries []activityPaneEntry `json:",omitempty"`
}

func newActivityPane(ctx context.Context, launch func() bool) *activityPane {
	return &activityPane{
		ctx: ctx, launch: launch, connection: liveDiffConnection{Token: rand.Text()},
		wake: make(chan struct{}, 1),
	}
}

func (a *subagentActivity) attachPane(pane *activityPane) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pane = pane
}

func (a *subagentActivity) setPaneEndpoint(endpoint string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pane != nil {
		a.pane.connection.Endpoint = endpoint
	}
}

func (a *subagentActivity) paneDescriptor() liveDiffConnection {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pane == nil {
		return liveDiffConnection{}
	}
	return a.pane.connection
}

// paneOwnsLocked reports whether the pane currently holds this root's child
// activity. Expired launch and reconnect windows release ownership lazily.
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

// claimPaneLocked requests one launch for the first child activity under a root.
// A released pane is not relaunched in the same router session.
func (a *subagentActivity) claimPaneLocked(thread string, now time.Time) {
	pane := a.pane
	if pane == nil || pane.state != activityPaneIdle || pane.launch == nil {
		return
	}
	root := a.rootLocked(thread)
	if root == "" || root == thread {
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
		text, key = "Subagent activity is shown in the Mekugi agents pane beside this session.", "announce"
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

// paneAgentsLocked lists the pane root's observed children in observation order.
func (a *subagentActivity) paneAgentsLocked() []activityPaneAgent {
	var nodes []*activityThread
	for thread, node := range a.threads {
		if node.child && !node.conflicted && a.rootLocked(thread) == a.pane.root {
			nodes = append(nodes, node)
		}
	}
	slices.SortFunc(nodes, func(x, y *activityThread) int { return x.order - y.order })
	agents := make([]activityPaneAgent, 0, len(nodes))
	for _, node := range nodes {
		agents = append(agents, activityPaneAgent{Name: node.name, Responding: node.responding > 0, Final: node.final})
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
		// Escaping can expand text up to sixfold, so budget the encoded form.
		data, _ := json.Marshal(entry)
		entry.size = len(data) + 1
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
		if event.kind == "operation" && slices.ContainsFunc(a.events, func(e activityEvent) bool {
			return e.thread == event.thread && e.kind == event.kind
		}) {
			continue
		}
		restored = append(restored, event)
	}
	a.events = append(restored, a.events...)
	a.wakePaneLocked()
}

func (a *subagentActivity) recordPaneHistory(entries []activityPaneEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pane == nil {
		return
	}
	pane := a.pane
	for _, entry := range entries {
		entry.event = activityEvent{}
		pane.history = append(pane.history, entry)
		pane.historyLen += entry.size
	}
	drop := 0
	for drop < len(pane.history) && (len(pane.history)-drop > activityPaneHistory || pane.historyLen > activityPaneLineBudget) {
		pane.historyLen -= pane.history[drop].size
		drop++
	}
	pane.history = slices.Delete(pane.history, 0, drop)
}

// subscribePane attaches a viewer and returns its generation, roster, and history.
func (a *subagentActivity) subscribePane() (uint64, []activityPaneAgent, []activityPaneEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pane := a.pane
	if a.closed || pane == nil || !a.paneOwnsLocked(pane.root, time.Now()) {
		return 0, nil, nil, false
	}
	pane.generation++
	pane.state = activityPaneAttached
	return pane.generation, a.paneAgentsLocked(), slices.Clone(pane.history), true
}

// detachPane starts the reconnect window for the current viewer only.
func (a *subagentActivity) detachPane(generation uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pane := a.pane
	if pane == nil || pane.generation != generation || pane.state != activityPaneAttached {
		return
	}
	pane.state, pane.deadline = activityPaneClaimed, time.Now().Add(activityPaneGraceWindow)
	// A reconnected viewer may be waiting on the shared wake signal.
	a.wakePaneLocked()
}

// beginResponse and endResponse track open provider responses for the roster.
// A new response clears an earlier final-answer marker.
func (a *subagentActivity) beginResponse(thread string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; node != nil && node.child && !a.closed {
		node.responding++
		node.final = false
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
		a.wakePaneLocked()
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

func (a *subagentActivity) serveActivityPane(w http.ResponseWriter, r *http.Request) {
	connection := a.paneDescriptor()
	if connection.Token == "" || r.Header.Get("Authorization") != "Bearer "+connection.Token {
		http.Error(w, "invalid live activity capability", http.StatusUnauthorized)
		return
	}
	generation, agents, history, ok := a.subscribePane()
	if !ok {
		http.Error(w, "live activity pane is not active", http.StatusGone)
		return
	}
	defer a.detachPane(generation)
	a.mu.Lock()
	pane := a.pane
	a.mu.Unlock()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	write := func(event activityPaneEvent) error {
		data, err := json.Marshal(event)
		if err != nil || len(data) > maxLiveDiffEventBytes {
			return errors.New("live activity event exceeds capacity")
		}
		if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return err
		}
		return controller.Flush()
	}
	if write(activityPaneEvent{Kind: "snapshot", Agents: agents, Entries: history}) != nil {
		return
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	var last []activityPaneAgent
	last = agents
	deliver := func() bool {
		entries, agents, current := a.takePane(generation)
		if !current {
			return false
		}
		for len(entries) > 0 {
			// Keep each line within the event limit without splitting an entry.
			batch, size := 0, 0
			for batch < len(entries) && (batch == 0 || size+entries[batch].size < activityPaneLineBudget) {
				size += entries[batch].size
				batch++
			}
			if write(activityPaneEvent{Kind: "entries", Agents: agents, Entries: entries[:batch]}) != nil {
				a.restorePane(entries)
				return false
			}
			a.recordPaneHistory(entries[:batch])
			entries, last = entries[batch:], agents
		}
		if !slices.Equal(last, agents) {
			if write(activityPaneEvent{Kind: "agents", Agents: agents}) != nil {
				return false
			}
			last = agents
		}
		return true
	}
	if !deliver() {
		return
	}
	for {
		select {
		case <-pane.ctx.Done():
			_ = write(activityPaneEvent{Kind: "end"})
			return
		case <-r.Context().Done():
			return
		case <-pane.wake:
			if !deliver() {
				return
			}
		case <-heartbeat.C:
			if !deliver() || write(activityPaneEvent{Kind: "heartbeat"}) != nil {
				return
			}
		}
	}
}

// activityPaneText removes the collector's author prefix; the pane shows the
// agent separately. Directed envelope headers remain part of the text.
func activityPaneText(name, text string) string {
	if hasCommentaryAuthor(text, name) {
		return strings.TrimPrefix(text, "["+commentaryCode(name)+"] ")
	}
	return text
}
