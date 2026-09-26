package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"
)

func (a *subagentActivity) authorizePane(w http.ResponseWriter, r *http.Request) bool {
	connection := a.paneDescriptor()
	if connection.Token == "" || r.Header.Get("Authorization") != "Bearer "+connection.Token {
		http.Error(w, "invalid live activity capability", http.StatusUnauthorized)
		return false
	}
	return true
}

func activityPaneWriter(w http.ResponseWriter) func(activityPaneEvent) error {
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	return func(event activityPaneEvent) error {
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
}

func (a *subagentActivity) serveActivityPane(w http.ResponseWriter, r *http.Request) {
	if !a.authorizePane(w, r) {
		return
	}
	generation, snapshot, ok := a.subscribePane()
	if !ok {
		http.Error(w, "live activity pane is not active", http.StatusGone)
		return
	}
	defer a.releasePane()
	a.mu.Lock()
	pane := a.pane
	a.mu.Unlock()
	write := activityPaneWriter(w)
	if write(snapshot) != nil {
		return
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	// Streamed token estimates change without waking the pane.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := snapshot.Agents
	deliver := func() bool {
		entries, agents, current := a.takePane(generation)
		if !current {
			return false
		}
		for len(entries) > 0 {
			// A single entry per event bounds transport without a stored size field.
			event := activityPaneEvent{Kind: "entries", Agents: agents, Entries: entries[:1]}
			if write(event) != nil {
				a.restorePane(entries)
				return false
			}
			entries = entries[1:]
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
		case <-tick.C:
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

// Legacy transport adapters are fixtures for collector ownership and delivery tests.
// Native production delivery uses app-server notifications instead.
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
		entry.assignment = event.assignment

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
