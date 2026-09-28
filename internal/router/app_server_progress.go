package router

import (
	"slices"
	"strings"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type appServerAgentState struct {
	Status string `json:"status"`
}

// History can retain an unfinished wait even in a stopped turn. Preserve its
// item state without reviving work, and allow later live completion to replace it.
func appServerHistoryProgressPhase(item appServerItem) string {
	if item.Status == "inProgress" {
		return "item/started"
	}
	return "item/completed"
}

// appServerProgress owns host progress presentation for live and restored items.
// Only compaction replaces Working; collaboration waits are transcript events.
// Source: codex-rs/tui/src/chatwidget/compaction.rs:6:56@1cc7e236
// and codex-rs/tui/src/multi_agents.rs:375:414@1cc7e236.
func appServerProgress(item appServerItem, method string) (text string, handled bool) {
	switch item.Type {
	case "contextCompaction":
		if method == "item/completed" {
			return "Context compacted", true
		}
		return "", true
	case "collabAgentToolCall":
		if item.Tool != "wait" {
			return "", false
		}
		text = "Waiting for agent"
		if method == "item/completed" {
			text = "Finished waiting"
			if item.Status == "failed" {
				text = "Wait failed"
			}
		}
		receivers := slices.Clone(item.ReceiverThreadIDs)
		var extra []string
		for id := range item.AgentsStates {
			if !slices.Contains(receivers, id) {
				extra = append(extra, id)
			}
		}
		slices.Sort(extra)
		receivers = append(receivers, extra...)
		for i, id := range receivers {
			if name := item.waitNames[id]; name != "" {
				receivers[i] = activityui.AgentDisplayName(name)
			}
			if method == "item/completed" {
				status := item.AgentsStates[id].Status
				if status == "running" {
					status = "Still running"
				}
				if status != "" {
					receivers[i] += ": " + status
				}
			}
		}
		if len(receivers) > 0 {
			text += " · " + strings.Join(receivers, ", ")
		}
		return text, true
	}
	return "", false
}

// Compaction is scoped to the active Main turn and exact item. A completion for
// another item, or activity from a child, cannot clear or replace it.
func (u *appServerUI) observeProgress(method string, p appServerEvent) {
	if p.ThreadID != u.thread {
		return
	}
	switch method {
	case "turn/started":
		u.compacting = nil
		u.polling = nil
	case "turn/completed":
		if p.Turn.ID == u.turn {
			u.compacting = nil
			u.polling = nil
		}
	case "item/commandExecution/terminalInteraction":
		// Source: codex-rs/tui/src/chatwidget/command_lifecycle.rs:76:128@1cc7e236.
		// Empty stdin is a poll, not an ordinary input write. It changes the
		// composer only, so repeated polls never flood the transcript.
		if p.TurnID == u.turn && u.turn != "" && p.ProcessID != "" {
			key := [2]string{p.TurnID, p.ProcessID}
			if p.Stdin == "" {
				u.polling = &key
			} else if u.polling != nil && *u.polling == key {
				u.polling = nil
			}
		}
	case "item/agentMessage/delta", "item/reasoning/summaryTextDelta":
		if p.TurnID == u.turn {
			u.polling = nil
		}
	case "item/started", "item/completed":
		if p.TurnID != u.turn || u.turn == "" {
			return
		}
		if p.Item.Type != "contextCompaction" {
			if method == "item/started" || p.Item.Type == "agentMessage" || p.Item.Type == "commandExecution" && u.polling != nil && u.polling[1] == p.Item.ProcessID {
				u.polling = nil
			}
			return
		}
		u.polling = nil
		key := [2]string{p.TurnID, p.Item.ID}
		if method == "item/started" {
			for _, entry := range u.view.entries {
				if entry.native != nil && entry.native.thread == p.ThreadID && entry.native.turn == p.TurnID && entry.native.item == p.Item.ID && entry.native.phase == "item/completed" {
					return
				}
			}
			u.compacting = &key
		} else if u.compacting != nil && *u.compacting == key {
			u.compacting = nil
		}
	}
}
