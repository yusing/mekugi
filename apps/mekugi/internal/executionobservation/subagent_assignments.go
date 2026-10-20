// Source: apps/mekugi/internal/router/native_activity_assignments.go@[d6415ec1].
package executionobservation

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Each observed assignment retains its host identity and authored timestamp.
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
	var assignments []*activitypresentation.SupplementAssignment
	for _, item := range items {
		text, ok := binding.NativeAssignmentText(item, recipient)
		if !ok || strings.TrimSpace(text) == "" {
			continue // Opaque tasks never become an inferred plaintext assignment.
		}
		from := responses.StringField(item, "author")
		id := binding.SubagentMessageID("assignment\x00" + responses.StringField(item, "id") + "\x00" + from + "\x00" + recipient + "\x00" + text)
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
		assignments = append(assignments, &activitypresentation.SupplementAssignment{ID: id, From: from, To: recipient, Text: text, Created: created})
	}
	// Publish spawn and its first prompt atomically as one navigable event.
	// The collector marks that assignment seen along with the lifecycle start.
	var events []activitypresentation.Supplement
	spawn := &activitypresentation.SupplementAssignment{From: from, To: recipient}
	if len(assignments) > 0 {
		spawn = assignments[0]
	}
	if start != nil {
		events = append(events, activitypresentation.Supplement{Thread: thread, Source: "subagent-start\x00" + thread, Kind: "start", Observed: spawn.Created, Assignment: spawn, Start: &activitypresentation.SupplementStart{Model: start.Model, Effort: start.Effort, Tier: start.Tier}})
	}
	for _, assignment := range assignments {
		events = append(events, activitypresentation.Supplement{Thread: thread, Source: assignment.ID, Kind: "assignment", Text: assignment.Text, Observed: assignment.Created, Assignment: assignment})
	}
	a.Collect(events...)
}
