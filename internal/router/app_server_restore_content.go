package router

import (
	"cmp"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const appServerRestoreThreadLimit = 128

type appServerActivityRestore struct {
	root     appServerThreadInfo
	threads  map[string]appServerThreadInfo
	order    []string
	archived bool
	cursors  map[string]bool
	next     int
	pages    int
	listed   bool
}

func (u *appServerUI) restorePaneContent(root appServerThreadInfo) error {
	u.restoring = &appServerActivityRestore{root: root, threads: make(map[string]appServerThreadInfo), cursors: make(map[string]bool)}
	u.restoreDiffThread(root, true)
	u.status = "Restoring roster and Activity…"
	return u.restoreList("")
}

func (u *appServerUI) restoreDiffThread(info appServerThreadInfo, root bool) {
	if u.shell == nil || u.shell.auto == nil {
		return
	}
	u.shell.auto.includeThread(info.Cwd, info.ID, root)
	if root {
		u.shell.diff.workspace = info.Cwd
	}
}

func (u *appServerUI) restoreList(cursor string) error {
	if u.restoring.pages >= 8 {
		u.restoreContentNotice("Roster pagination exceeded the restoration limit.")
		return u.readRestoredChildren()
	}
	params := map[string]any{
		"ancestorThreadId": u.thread, "modelProviders": []string{},
		"sourceKinds": []string{"subAgentThreadSpawn"}, "limit": 100,
		"archived": u.restoring.archived,
	}
	if cursor != "" {
		params["cursor"] = cursor
	}
	return u.request("thread/list", params)
}

func (u *appServerUI) restoreContentNotice(message string) {
	u.agents.status = "History incomplete"
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: message, Observed: time.Now()}}})
	// Notices advance Main independently of the Activity sequence.
	u.session.seq = max(u.session.seq, u.view.lastSeq)
}

func (u *appServerUI) restoreActivityResponse(method string, m appServerMessage) error {
	r := u.restoring
	if r == nil {
		return nil
	}
	if m.Error != nil {
		u.restoreContentNotice("Could not restore " + method + " history: " + m.Error.Message)
		if method == "thread/list" {
			return u.readRestoredChildren()
		}
		r.next++
		return u.readNextRestoredChild()
	}
	if method == "thread/list" {
		r.pages++
		var result struct {
			Data       []appServerThreadInfo `json:"data"`
			NextCursor string                `json:"nextCursor"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			u.restoreContentNotice("Invalid roster history: " + err.Error())
			return u.readRestoredChildren()
		}
		for _, info := range result.Data {
			if info.ID == "" || info.ID == u.thread || r.threads[info.ID].ID != "" {
				continue
			}
			if len(r.order) == appServerRestoreThreadLimit {
				u.restoreContentNotice("Activity restoration is limited to 128 child threads; additional history was not loaded.")
				return u.readRestoredChildren()
			}
			r.threads[info.ID] = info
			r.order = append(r.order, info.ID)
		}
		if result.NextCursor != "" {
			if r.cursors[result.NextCursor] {
				u.restoreContentNotice("Roster history returned a repeated pagination cursor.")
				return u.readRestoredChildren()
			}
			r.cursors[result.NextCursor] = true
			return u.restoreList(result.NextCursor)
		}
		if !r.archived {
			r.archived, r.cursors = true, make(map[string]bool)
			return u.restoreList("")
		}
		return u.readRestoredChildren()
	}
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		u.restoreContentNotice("Invalid Activity history: " + err.Error())
	} else if result.Thread.ID != r.order[r.next] {
		u.restoreContentNotice("Activity history returned a different thread identity.")
	} else {
		u.restoreActivityThread(result.Thread)
	}
	r.next++
	return u.readNextRestoredChild()
}

func (u *appServerUI) readRestoredChildren() error {
	r := u.restoring
	r.listed = true
	// Register names before rendering cross-agent assignments and messages.
	for _, id := range r.order {
		info := r.threads[id]
		u.session.registerThread(info)
		u.restoreDiffThread(info, false)
		agent := u.session.agent(u.session.paths[id])
		agent.Started, agent.LastResponse = historyTime(info.CreatedAt), historyTime(info.UpdatedAt)
	}
	for _, turn := range r.root.Turns {
		for _, item := range turn.Items {
			if item.Type == "collabAgentToolCall" {
				item.SenderThreadID = u.thread
				u.applyRestoredActivity(u.restoredCollab(item, historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, r.root.UpdatedAt))))
			}
		}
	}
	r.root.Turns = nil
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(u.session.agents)})
	return u.readNextRestoredChild()
}

func (u *appServerUI) readNextRestoredChild() error {
	r := u.restoring
	if r.next < len(r.order) {
		u.status = fmt.Sprintf("Restoring Activity %d/%d…", r.next+1, len(r.order))
		return u.request("thread/read", map[string]any{"threadId": r.order[r.next], "includeTurns": true})
	}
	return u.finishRestoredContent()
}

// Scope publication and durable hydration are separate events. Keep Enter
// guarded until the controller has consumed the final scope, not just until
// Codex's history replies have arrived.
func (u *appServerUI) finishRestoredContent() error {
	r := u.restoring
	if r == nil || !r.listed || r.next < len(r.order) {
		return nil
	}
	if u.shell != nil && u.shell.auto != nil && u.shell.diffFailure == "" {
		a := u.shell.auto
		a.mu.Lock()
		scope := cloneLiveDiffScope(a.scope)
		a.mu.Unlock()
		for workspace, threads := range scope.Workspaces {
			for thread := range threads {
				if !u.shell.diff.scope.Workspaces[workspace][thread] {
					u.status = "Restoring saved Diff…"
					return nil
				}
			}
		}
	}
	u.restoring = nil
	u.status = "Ready"
	if u.turn != "" {
		u.status = "Working"
	}
	pending := u.resumePending
	u.resumePending = nil
	for _, event := range pending {
		if err := u.message(event); err != nil {
			return err
		}
	}
	return nil
}

func (u *appServerUI) restoredCollab(item appServerItem, observed time.Time) []activityPaneEntry {
	if item.Status == "completed" {
		return u.session.collab(item, item.ID, observed)
	}
	var entries []activityPaneEntry
	for _, receiver := range item.ReceiverThreadIDs {
		entries = append(entries, activityPaneEntry{Seq: u.session.next(), Agent: u.session.path(receiver), Kind: "error", Observed: observed,
			Text:   "Historical " + item.Tool + ": " + cmp.Or(item.Status, "unknown status") + "; delivery not confirmed",
			native: &liveActivityNativeItem{thread: item.SenderThreadID, turn: "collaboration", item: item.ID + "\x00" + receiver, phase: "item/completed"}})
	}
	return entries
}

func historyTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func (u *appServerUI) applyRestoredActivity(entries []activityPaneEntry) {
	for i := range entries {
		if entries[i].Kind == "reply" {
			from, to, _, _, ok := parseLiveActivityEnvelope(entries[i].Text)
			if ok && from == "/root" {
				entries[i].Agent = to
			}
		}
	}
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: entries, Agents: slices.Clone(u.session.agents)})
}

// Observational history never opens previews, resumes children, or fabricates
// a live response. Only a subsequent live notification can mark an agent busy.
func (u *appServerUI) restoreActivityThread(info appServerThreadInfo) {
	s := &u.session
	name := s.paths[info.ID]
	agent := s.agent(name)
	agent.Started, agent.LastResponse = historyTime(info.CreatedAt), historyTime(info.UpdatedAt)
	agent.Turns, agent.Responding = uint64(len(info.Turns)), false
	u.observeCost(info.ID, agent)
	for _, turn := range info.Turns {
		observed := historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, info.UpdatedAt))
		var entries []activityPaneEntry
		lastMessage := -1
		for _, item := range turn.Items {
			entry := activityPaneEntry{Seq: s.next(), Agent: name, Observed: observed, CallID: item.ID,
				native: &liveActivityNativeItem{thread: info.ID, turn: turn.ID, item: item.ID, phase: "item/completed"}}
			switch item.Type {
			case "reasoning":
				entry.Kind, entry.Text = "reasoning", strings.Join(item.Summary, "\n\n")
				if strings.TrimSpace(entry.Text) != "" {
					entries = append(entries, entry)
				}
			case "commandExecution", "fileChange":
				entry.Kind, entry.Text = "tool", appServerCommandText(item, info.Cwd)
				if item.Type == "fileChange" {
					entry.Text = appServerEditText(item, info.Cwd)
				}
				entries = append(entries, entry)
				if item.ExitCode != nil && *item.ExitCode != 0 {
					entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: name, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID, Observed: observed})
				}
			case "agentMessage":
				entry.Kind, entry.Text = "text", item.Text
				if item.Phase == "final_answer" || item.Phase == "finalAnswer" {
					entry.Kind = "final"
				}
				lastMessage = len(entries)
				entries = append(entries, entry)
			case "collabAgentToolCall":
				entries = append(entries, u.restoredCollab(item, observed)...)
			}
		}
		agent = s.agent(name)
		agent.Final = turn.Status == "completed"
		if agent.Final && lastMessage >= 0 {
			entries[lastMessage].Kind = "final"
		}
		if turn.Status == "failed" || turn.Status == "interrupted" || turn.Status == "inProgress" {
			text := "Recorded turn: " + turn.Status + " (history only)"
			if turn.Error != nil {
				text += ": " + turn.Error.Message
			}
			entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: name, Kind: "error", Text: text, Observed: observed})
		}
		u.applyRestoredActivity(entries)
	}
}
