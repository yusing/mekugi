package router

import (
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

// Native MCP reads use the same scoped action and page grouping as the shared
// journal transport. Only an exact authenticated hook receipt changes the row.
func (u *appServerUI) runtimeJournalReadEntry(entry *activityPaneEntry, e session.Event) {
	n := entry.native
	s := u.runtime.observations
	if n.tool != "mcp__mekugi__journal_read" || s == nil || s.journal == nil {
		return
	}
	n.operation, n.journalResults = "", nil
	entry.Text, _ = runtimeToolText(n.tool, n.journalInput, u.session.cwd)
	root := s.journal.rootBinding()
	if root.Session != u.thread || root.Workspace != u.session.cwd {
		return
	}
	ctx, err := s.journal.scope(u.ctx, root)
	if err != nil {
		return
	}
	id := strings.TrimPrefix(e.ID, "history/")
	h, found, err := s.owner.store.lookup(ctx, root.Workspace, journalNativeReceiptID(root, id))
	if err != nil || !found || h.NativeObservation == nil || h.NativeObservation.Call == nil {
		return
	}
	call := h.NativeObservation.Call
	if call.Binding.Runtime != root.Runtime || call.Binding.Workspace != root.Workspace || call.Binding.Session != root.Session ||
		call.Binding.Branch != "" || call.ID != id || call.Tool != n.tool || h.ExecutingThread != observationThread(call.Binding) {
		return
	}
	if call.Binding.Agent == "" {
		if e.Caller != "" {
			return
		}
	} else if u.runtimeCallerLane(e.Caller) != runtimeTaskLane(call.Binding.Agent) {
		return
	}
	actual := *call
	actual.Input = n.journalInput
	if !sameObservationCall(call, &actual) {
		return
	}
	var request journalReadRequest
	if json.Unmarshal([]byte(call.Input), &request, json.RejectUnknownMembers(true)) != nil {
		return
	}
	request.Op = "read"
	if call.Binding.Agent != "" && request.View == "" {
		request.View = "own"
	}
	action, _ := request.action()
	n.operation = appServerCommandText(appServerItem{CommandActions: []appServerCommandAction{action}}, "")
	entry.Text = n.operation
	if e.Kind != "tool_result" || e.Failed {
		return
	}
	var result struct {
		Nodes      []journalNode `json:"nodes"`
		Incomplete bool          `json:"incomplete"`
		Count      *int          `json:"count"`
	}
	if json.Unmarshal([]byte(e.Text), &result) != nil {
		return
	}
	if result.Incomplete {
		if result.Count != nil && *result.Count >= 0 {
			n.journalResults = result.Count
		}
	} else if result.Nodes != nil {
		n.journalResults = new(journalNodeCount(result.Nodes))
	}
}
