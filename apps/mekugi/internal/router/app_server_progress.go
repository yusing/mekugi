package router

import (
	"slices"

	"github.com/yusing/mekugi/contracts/observation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
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

// Main progress consumes Activity's authoritative retained item evidence. The
// application adapts host facts; session presentation owns precedence and keys.
func (u *appServerUI) observeProgress(method string, p appServerEvent) {
	m := u.sessionPresentation()
	if method == "item/commandExecution/terminalInteraction" {
		m.TerminalInteraction(p.ThreadID, p.TurnID, u.Turn, p.ProcessID, p.Stdin)
		return
	}
	completed := false
	if p.ThreadID == u.Thread && method == "item/started" && p.Item.Type == "contextCompaction" {
		for _, entry := range u.view.Model.Entries {
			native := entry.Source.native
			if native != nil && native.thread == p.ThreadID && native.turn == p.TurnID && native.item == p.Item.ID && native.phase == "item/completed" {
				completed = true
				break
			}
		}
	}
	turn := p.TurnID
	if method == "turn/started" || method == "turn/completed" {
		turn = p.Turn.ID
	}
	m.ObserveProgress(observation.Event{Kind: observation.Kind(method), ThreadID: p.ThreadID, TurnID: turn,
		Item: observation.Item{ID: p.Item.ID, Type: p.Item.Type, ProcessID: p.Item.ProcessID}}, u.Turn, completed)
}
