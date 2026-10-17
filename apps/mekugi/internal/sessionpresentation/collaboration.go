package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_events.go:413:458@7166de95.
import (
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// collab binds neutral delivery facts to existing caller labels and feed
// identities. The live adapter applies accounting after this projection.
func (s *Attachment) Collaboration(item observation.Item, id string, now time.Time, origin activitypresentation.CollaborationOrigin) []ActivityEntry {
	item.ID = id
	deliveries, confirmed := activitypresentation.ProjectCollaboration(item, origin)
	from := ""
	if confirmed {
		// Preserve sender-first observation order, including participants whose
		// blank input or lifecycle call has no delivered message to display.
		from = s.Caller(item.SenderThreadID)
		for _, receiver := range item.ReceiverThreadIDs {
			s.Caller(receiver)
		}
	}
	var entries []ActivityEntry
	for _, d := range deliveries {
		to := s.Caller(d.To)
		entry := ActivityEntry{Seq: s.Activity().NextSequence(), Agent: to, Kind: d.Kind, Observed: now}
		if d.Kind == "error" {
			entry.Text = d.Text
		} else {
			if d.AssignmentID != "" {
				entry.Assignment = &ActivityAssignment{ID: d.AssignmentID, From: from, To: to, Text: d.Text}
			}
			switch d.Kind {
			case "start":
				if agent := s.Agent(to); agent.Settings.Model == "" {
					agent.Settings.Model, agent.Settings.Effort = d.Model, d.Effort
				}
				entry.Start = &ActivityStart{Model: d.Model}
				if d.Model != "" {
					entry.Start.Effort = d.Effort
				}
			case "assignment":
				entry.Text = d.Text
			case "reply":
				entry.Agent, entry.Text = from, d.Text
				entry.Message = &ActivityMessage{From: from, To: to, Text: d.Text}
			}
		}
		entry.Native = &NativeItem{Thread: d.From, Turn: "collaboration", Item: d.ID, Phase: "item/completed"}
		entries = append(entries, entry)
	}
	return entries
}
