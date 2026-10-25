package sessionpresentation

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/activity/coordination"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// CoordinationEntry binds the first-party operation to ordinary agent traffic.
// Host identity remains stable across pending, completed and restored entries.
func (s *Attachment) CoordinationEntry(entry ActivityEntry, item observation.Item) ActivityEntry {
	entry.Kind, entry.Text = "tool", activitypresentation.MCPText(item)
	entry.Native.Status, entry.Native.Duration = item.Status, ItemDuration(item)
	entry.Native.Running = entry.Native.Phase == "item/started" && item.Status == "inProgress"
	c, handled := coordination.ProjectCoordination(item)
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
		from := entry.Agent
		entry.Agent, entry.Kind, entry.Text = target, c.Kind, ""
		if (c.Kind == "start" || c.Kind == "assignment") && strings.TrimSpace(c.Message) != "" {
			entry.Assignment = &ActivityAssignment{ID: item.ID, From: from, To: target, Text: c.Message}
		}
		switch c.Kind {
		case "start":
			entry.Start = new(ActivityStart)
			if caller := s.KnownCaller(c.Thread); caller != "" {
				settings := s.Agent(caller).Settings
				entry.Start.Model, entry.Start.Effort, entry.Start.Tier = settings.Model, settings.Effort, settings.Tier
			}
		case "assignment":
			entry.Text = c.Message
		case "reply":
			entry.Agent, entry.Text = from, c.Message
			entry.Message = &ActivityMessage{From: from, To: target, Text: c.Message}
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
			if c.State == "completed" && c.EventKind == "message" {
				entry.Text = "Message received"
			}
			wait := &activitypresentation.Value{Kind: "progress", Body: entry.Text}
			for _, target := range c.Targets {
				if caller := s.KnownCaller(target.Name); caller != "" {
					target.Name = caller
				}
				wait.WaitTargets = append(wait.WaitTargets, activitypresentation.WaitTarget{Name: target.Name, Status: target.Status})
			}
			entry.Native.Wait = wait
		case "Interrupt":
			entry.Text = "Interrupt requested for " + target
		case "List agents":
			entry.Text = fmt.Sprintf("Listed %d agents", c.Count)
		}
	}
	return entry
}
