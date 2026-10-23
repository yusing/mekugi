package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_events.go:413:458@7166de95.
import (
	"fmt"
	"strings"
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
		entry := ActivityEntry{Seq: s.Activity().NextSequence(), Observed: now,
			Native: &NativeItem{Thread: d.From, Turn: "collaboration", Item: d.ID, Phase: "item/completed"}}
		if d.Kind == "start" {
			if agent := s.Agent(to); agent.Settings.Model == "" {
				agent.Settings.Model, agent.Settings.Effort = d.Model, d.Effort
			}
		}
		d.From, d.To = from, to
		entries = append(entries, coordinationDelivery(entry, d))
	}
	return entries
}

// CoordinationEntry binds the first-party operation to ordinary agent traffic.
// Host identity remains stable across pending, completed and restored entries.
func (s *Attachment) CoordinationEntry(entry ActivityEntry, item observation.Item) ActivityEntry {
	entry.Kind, entry.Text = "tool", activitypresentation.MCPText(item)
	entry.Native.Status, entry.Native.Duration = item.Status, ItemDuration(item)
	entry.Native.Running = entry.Native.Phase == "item/started" && item.Status == "inProgress"
	c, handled := activitypresentation.ProjectCoordination(item)
	if !handled {
		return entry
	}
	entry.Native.ToolDetail = activitypresentation.MCPDetail(item)
	c.Confirmed = c.Confirmed && entry.Native.Phase == "item/completed"
	target := c.Target
	if name := s.KnownCaller(c.Thread); name != "" {
		target = name
	} else if name := s.KnownCaller(target); name != "" {
		target = name
	}
	if c.Failure != "" {
		entry.Kind, entry.Text = "error", strings.TrimSpace(c.Action+" "+target)+" failed: "+c.Failure
		return entry
	}
	if c.Confirmed && (c.Kind == "start" || c.Kind == "assignment" || c.Kind == "reply") {
		d := activitypresentation.CollaborationDelivery{Kind: c.Kind, From: entry.Agent, To: target, Text: c.Message}
		if (c.Kind == "start" || c.Kind == "assignment") && strings.TrimSpace(c.Message) != "" {
			d.AssignmentID = item.ID
		}
		entry = coordinationDelivery(entry, d)
		if caller := s.KnownCaller(c.Thread); caller != "" && entry.Start != nil {
			settings := s.Agent(caller).Settings
			entry.Start.Model, entry.Start.Effort, entry.Start.Tier = settings.Model, settings.Effort, settings.Tier
		}
		return entry
	}
	entry.Kind = "progress"
	entry.Text = strings.TrimSpace(c.Action+" "+target) + " · " + c.State
	if c.Action == "Spawn" && c.State == "pending" {
		entry.Text = "Spawning " + target
	}
	if c.Confirmed {
		switch c.Action {
		case "Wait":
			entry.Text = map[string]string{"registered": "Wait registered", "completed": "Finished waiting", "timeout": "Wait timed out", "canceled": "Wait canceled", "paused": "Wait paused"}[c.State]
			for i := range c.Targets {
				if caller := s.KnownCaller(c.Targets[i].Name); caller != "" {
					c.Targets[i].Name = caller
				}
			}
			entry.Native.Wait = &activitypresentation.Value{Kind: "progress", Body: entry.Text, WaitTargets: c.Targets}
		case "Interrupt":
			entry.Text = "Interrupt requested for " + target
		case "List agents":
			entry.Text = fmt.Sprintf("Listed %d agents", c.Count)
		}
	}
	return entry
}

// Both host adapters use the same sender/recipient and assignment construction.
func coordinationDelivery(entry ActivityEntry, d activitypresentation.CollaborationDelivery) ActivityEntry {
	entry.Agent, entry.Kind, entry.Text = d.To, d.Kind, ""
	if d.AssignmentID != "" {
		entry.Assignment = &ActivityAssignment{ID: d.AssignmentID, From: d.From, To: d.To, Text: d.Text}
	}
	switch d.Kind {
	case "start":
		entry.Start = &ActivityStart{Model: d.Model}
		if d.Model != "" {
			entry.Start.Effort = d.Effort
		}
	case "assignment", "error":
		entry.Text = d.Text
	case "reply":
		entry.Agent, entry.Text = d.From, d.Text
		entry.Message = &ActivityMessage{From: d.From, To: d.To, Text: d.Text}
	}
	return entry
}
