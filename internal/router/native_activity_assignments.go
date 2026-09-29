package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
	"time"
)

type activityAssignment struct {
	id, from, to, text string
	created            time.Time
}

// activityMessage is a message one agent delivered to another. Its text is
// the payload alone; an opaque payload leaves it empty.
type activityMessage struct{ from, to, text string }

// activityStart names the model settings a child actually ran with.
type activityStart struct{ model, effort, tier string }

// label shows the settings as code spans; any may be unknown.
func (s *activityStart) label() string {
	var parts []string
	for _, value := range []string{s.model, s.effort, s.tier} {
		if value != "" {
			parts = append(parts, commentaryCode(value))
		}
	}
	return strings.Join(parts, " ")
}

// The native pane keeps every observed NEW_TASK, including follow-ups, keyed by
// the host item and payload. Full-history requests cannot replay its display.
func (a *subagentActivity) collectSubagentStart(thread string, request *parsedResponsesRequest, recipient string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	from := ""
	if node := a.threads[thread]; node != nil {
		if parent := a.threads[node.parent]; parent != nil {
			from = parent.name
		}
	}
	a.mu.Unlock()
	start := nativeSubagentStart(request)
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
		var metadata struct {
			Created jsonv1.Number `json:"create_time"`
		}
		// Codex stamps authored messages before recording them in history.
		// Replayed input retains that timestamp; request arrival is not send time.
		_ = json.Unmarshal(item["internal_chat_message_metadata_passthrough"], &metadata)
		var created time.Time
		if elapsed, err := time.ParseDuration(metadata.Created.String() + "s"); err == nil {
			created = time.Unix(0, int64(elapsed))
		}
		assignments = append(assignments, &activityAssignment{id: id, from: from, to: recipient, text: text, created: created})
	}
	// Publish spawn and its first prompt atomically as one navigable event.
	// The collector marks that assignment seen along with the lifecycle start.
	a.mu.Lock()
	defer a.mu.Unlock()
	spawn := &activityAssignment{from: from, to: recipient}
	if len(assignments) > 0 {
		spawn = assignments[0]
	}
	if start != nil {
		a.collectEventLocked(activityEvent{thread: thread, source: "subagent-start\x00" + thread, kind: "start", observed: spawn.created, assignment: spawn, start: start})
	}
	for _, assignment := range assignments {
		a.collectEventLocked(activityEvent{thread: thread, source: assignment.id, kind: "assignment", text: assignment.text, observed: assignment.created, assignment: assignment})
	}
}
