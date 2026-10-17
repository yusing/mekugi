package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

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
			parts = append(parts, markdownsyntax.QuoteCode(value))
		}
	}
	return strings.Join(parts, " ")
}

// The native pane keeps every observed NEW_TASK, including follow-ups, keyed by
// the host item and payload. Full-history requests cannot replay its display.
func collectSubagentStart(a *sessioncontrol.Audience, thread string, request *codexresponses.Request, recipient string) {
	if a == nil {
		return
	}
	from := a.ParentName(thread)
	start := nativeSubagentStart(request)
	var items []map[string]jsonv1.RawMessage
	if json.Unmarshal(request.Fields["input"], &items) != nil {
		items = nil
	}
	var assignments []*activityAssignment
	for _, item := range items {
		text, ok := binding.NativeAssignmentText(item, recipient)
		if !ok || strings.TrimSpace(text) == "" {
			continue // Opaque tasks never become an inferred plaintext assignment.
		}
		from := responses.StringField(item, "author")
		id := subagentCommentaryMessageID("assignment\x00" + responses.StringField(item, "id") + "\x00" + from + "\x00" + recipient + "\x00" + text)
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
	var events []activitypresentation.Supplement
	spawn := &activityAssignment{from: from, to: recipient}
	if len(assignments) > 0 {
		spawn = assignments[0]
	}
	if start != nil {
		events = append(events, activitypresentation.Supplement{Thread: thread, Source: "subagent-start\x00" + thread, Kind: "start", Observed: spawn.created, Assignment: supplementAssignment(spawn), Start: &activitypresentation.SupplementStart{Model: start.model, Effort: start.effort, Tier: start.tier}})
	}
	for _, assignment := range assignments {
		events = append(events, activitypresentation.Supplement{Thread: thread, Source: assignment.id, Kind: "assignment", Text: assignment.text, Observed: assignment.created, Assignment: supplementAssignment(assignment)})
	}
	a.Collect(events...)
}

func supplementAssignment(a *activityAssignment) *activitypresentation.SupplementAssignment {
	if a == nil {
		return nil
	}
	return &activitypresentation.SupplementAssignment{ID: a.id, From: a.from, To: a.to, Text: a.text, Created: a.created}
}
func supplementMessage(m *activityMessage) *activitypresentation.SupplementMessage {
	if m == nil {
		return nil
	}
	return &activitypresentation.SupplementMessage{From: m.from, To: m.to, Text: m.text}
}
