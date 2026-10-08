package router

import (
	"slices"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type appServerAgentState struct {
	Status string `json:"status"`
}

// History can retain an unfinished item in a stopped turn. The owning turn
// ends its waiting presentation without claiming a successful tool result.
func appServerHistoryProgressPhase(item appServerItem, turnStatus string) string {
	if item.Status == "inProgress" {
		if item.Type == "collabAgentToolCall" && item.Tool == "wait" && (turnStatus == "completed" || turnStatus == "interrupted" || turnStatus == "failed") {
			return "turn/completed"
		}
		return "item/started"
	}
	return "item/completed"
}

// appServerProgress owns host progress presentation for live and restored items.
// Only compaction replaces Working; collaboration waits are roster-only status.
// Source: codex-rs/tui/src/chatwidget/compaction.rs:6:56@1cc7e236
// and codex-rs/tui/src/multi_agents.rs:375:414@1cc7e236.
func appServerProgress(item appServerItem, method string) (text string, wait *activityui.Block, handled bool) {
	switch item.Type {
	case "contextCompaction":
		if method == "item/completed" {
			return "Context compacted", nil, true
		}
		return "", nil, true
	}
	if block := appServerWaitProgress(item, method); block != nil {
		return block.ProgressText(), block, true
	}
	return "", nil, false
}

func (u *appServerUI) progress(item appServerItem, method, thread, turn string) (string, *activityui.Block, bool) {
	text, wait, handled := appServerProgress(item, method)
	if item.Type == "contextCompaction" && text != "" {
		answered := u.journalCompactionAnswered(thread, turn, item.ID)
		if !answered {
			u.bindRolloutCompaction(thread, turn, item.ID)
			answered = u.journalCompactionAnswered(thread, turn, item.ID)
		}
		if u.journalResetEvent(thread, turn) {
			return "", wait, handled // The slice driver's durable journal event owns this reset.
		}
		if answered || u.journalResetCompleted(thread, turn) {
			text = "Context reset from journal"
		} else if u.proxy != nil && u.proxy.journalCompaction == "auto" {
			text = "Context reset"
		}
	}
	return text, wait, handled
}

// Standalone journal answers have no slice note. Present their exact host receipt
// as a journal row, not a second progress/commentary surface.
func (u *appServerUI) progressEntry(entry activityPaneEntry) activityPaneEntry {
	if entry.Text == "Context reset from journal" {
		entry.Kind = "journal_event"
		entry.journalEvent = &journalEvent{Op: "reset", ResetTurn: entry.native.turn,
			Fields: journalNode{Kind: "note", Title: "↻ Context reset from journal · 0 provider tokens"}}
		entry.native.recovery = u.progressRecovery(entry.Text, entry.native.thread, entry.native.turn, entry.native.item)
	}
	return entry
}

func (u *appServerUI) progressRecovery(text, thread, turn, item string) string {
	if text != "Context reset from journal" || thread != u.thread {
		return ""
	}
	if u.proxy != nil && u.proxy.replayStore != nil {
		for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
			if sink != nil && sink.thread == thread {
				if message, err := u.proxy.replayStore.compactionRecovery(u.ctx, sink.workspace, thread, turn, item); err == nil && message != "" {
					return message
				}
			}
		}
	}
	if d := u.reset; d != nil && d.thread == thread && d.compactTurn == turn && d.proxy != nil && d.proxy.replayStore != nil {
		d.proxy.replayStore.bindStandaloneCompactionItem(u.ctx, d.workspace, thread, turn, item)
		if message, err := d.proxy.replayStore.compactionRecovery(u.ctx, d.workspace, thread, turn, item); err == nil && message != "" {
			return message
		}
	}
	return journalRecoveryUnavailable
}

const journalRecoveryUnavailable = "The exact model-visible recovery message is unavailable for this reset. It was not retained or its retained evidence is no longer readable."

// Durable reset notes already own a workspace; do not search another journal
// namespace for a turn with the same name.
func (u *appServerUI) journalResetRecovery(workspace, thread, turn string) string {
	if thread != u.thread {
		return ""
	}
	p := u.proxy
	if p == nil && u.reset != nil && u.reset.thread == thread && u.reset.workspace == workspace {
		p = u.reset.proxy
	}
	if p != nil && p.replayStore != nil {
		if message, err := p.replayStore.compactionRecovery(u.ctx, workspace, thread, turn, ""); err == nil && message != "" {
			return message
		}
	}
	return journalRecoveryUnavailable
}

func appServerWaitProgress(item appServerItem, method string) *activityui.Block {
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" {
		return nil
	}
	text := "Waiting for agent"
	if method == "item/completed" || method == "turn/completed" {
		text = "Finished waiting"
		if item.Status == "inProgress" {
			text = "Wait ended"
		}
		if item.Status == "failed" {
			text = "Wait failed"
		}
	}
	block := &activityui.Block{Kind: "progress", Body: text}
	receivers := slices.Clone(item.ReceiverThreadIDs)
	var extra []string
	for id := range item.AgentsStates {
		if !slices.Contains(receivers, id) {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	for _, id := range append(receivers, extra...) {
		name := item.waitNames[id]
		if name == "" {
			name = id
		}
		status := ""
		if method == "item/completed" {
			status = item.AgentsStates[id].Status
			if status == "running" {
				status = "Still running"
			}
		}
		block.WaitTargets = append(block.WaitTargets, activityui.WaitTarget{Name: name, Status: status})
	}
	return block
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
