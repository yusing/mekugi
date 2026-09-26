package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
)

type activityAssignment struct {
	id, from, to, text string
}

// The native pane keeps every observed NEW_TASK, including follow-ups, keyed by
// the host item and payload. Full-history requests cannot replay its display.
// Legacy starts retain their existing bounded assignment excerpt.
func (a *subagentActivity) collectSubagentStart(thread string, request *parsedResponsesRequest, recipient string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	native := a.pane != nil && a.pane.native && a.pane.state == activityPaneAttached && a.rootLocked(thread) == a.pane.root
	from := ""
	if node := a.threads[thread]; node != nil {
		if parent := a.threads[node.parent]; parent != nil {
			from = parent.name
		}
	}
	a.mu.Unlock()
	start := subagentStartCommentary(request, recipient)
	if !native {
		a.collect(thread, "subagent-start\x00"+thread, "start", start)
		return
	}
	heading, _, _ := strings.Cut(start, "\n")
	var items []map[string]jsonv1.RawMessage
	if json.Unmarshal(request.fields["input"], &items) != nil {
		items = nil
	}
	var assignments []*activityAssignment
	for _, item := range items {
		text, ok := journalAssignmentText(item, recipient)
		if !ok || strings.TrimSpace(text) == "" {
			continue // Opaque tasks never become an inferred plaintext assignment.
		}
		from := jsonString(item, "author")
		id := subagentCommentaryMessageID("assignment\x00" + jsonString(item, "id") + "\x00" + from + "\x00" + recipient + "\x00" + text)
		assignments = append(assignments, &activityAssignment{id: id, from: from, to: recipient, text: text})
	}
	// Publish spawn and its first prompt atomically as one navigable event.
	// The collector marks that assignment seen along with the lifecycle start.
	a.mu.Lock()
	defer a.mu.Unlock()
	spawn := &activityAssignment{from: from, to: recipient}
	if len(assignments) > 0 {
		spawn = assignments[0]
	}
	a.collectEventLocked(activityEvent{thread: thread, source: "subagent-start\x00" + thread, kind: "start", text: heading, assignment: spawn})
	for _, assignment := range assignments {
		a.collectEventLocked(activityEvent{thread: thread, source: assignment.id, kind: "assignment", text: assignment.text, assignment: assignment})
	}
}
