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
