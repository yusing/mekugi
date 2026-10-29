package sessionpresentation

import (
	"fmt"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/activity/coordination"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
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
	target = pathdisplay.AgentCanonicalName(target)
	if c.Failure != "" {
		entry.Kind, entry.Text = "error", strings.TrimSpace(c.Action+" "+target)+" failed: "+c.Failure
		return entry
	}
	if c.Confirmed && (c.Kind == "start" || c.Kind == "assignment" || c.Kind == "reply") {
		from := entry.Agent
		// The host root is this runtime, which may itself be a delegate.
		// Keep its local alias until presentation supplies its admitted caller.
		if from == "/root" {
			from = "Main"
		}
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
	entry.Native.Wait = &activitypresentation.Value{Kind: "progress", From: entry.Agent}
	entry.Text = strings.TrimSpace(c.Action+" "+target) + " · " + c.State
	if c.Action == "Spawn" && c.State == "pending" {
		entry.Text = "Spawning " + target
	}
	if c.Confirmed {
		switch c.Action {
		case "Wait":
			wait := &activitypresentation.Value{Kind: "progress", From: entry.Agent, Body: "Waiting for"}
			if c.State != "waiting" && c.State != "scheduled" {
				wait.Body = "Finished waiting for"
				wait.WaitReason = c.State
			}
			if c.State == "completed" && c.EventKind == "message" {
				wait.WaitReason = "message received"
			}
			for _, target := range c.Targets {
				if caller := s.KnownCaller(target.Name); caller != "" {
					target.Name = caller
				}
				wait.WaitTargets = append(wait.WaitTargets, activitypresentation.WaitTarget{Name: target.Name, Status: target.Status})
			}
			if (c.State == "waiting" || c.State == "scheduled") && c.TimeoutMS != nil {
				wait.WaitTimeout = (time.Duration(*c.TimeoutMS) * time.Millisecond).String()
			}
			entry.Text = wait.ProgressText()
			entry.Native.Wait = wait
		case "Interrupt":
			entry.Text = "Interrupt requested for " + target
			entry.Native.InterruptTarget, entry.Native.InterruptID = target, c.ID
		case "List agents":
			entry.Text = listed(c.Count, "agent")
		case "List models":
			entry.Text = listed(c.Count, "model")
		case "Inspect":
			inspected := &activitypresentation.Value{Kind: "progress", From: entry.Agent, Body: "Inspected", WaitTargets: []activitypresentation.WaitTarget{{Name: target}}}
			entry.Text, entry.Native.Wait = inspected.ProgressText(), inspected
		}
	}
	if len(entry.Native.Wait.WaitTargets) == 0 {
		entry.Native.Wait.Body = entry.Text
	}
	return entry
}

func listed(count int, noun string) string {
	if count != 1 {
		noun += "s"
	}
	return fmt.Sprintf("Listed %d %s", count, noun)
}

func settledInterrupt(target, status string) string {
	if status == "interrupted" {
		return "Interrupted " + target
	}
	return "Interrupt settled for " + target + " · " + status
}

// SetInterruptions also settles non-parent requesters and outcomes observed
// before their request rows. New rows use this same immutable publication.
func (s *ActivitySource) SetInterruptions(publication *control.Interruptions) {
	if s.interruptions == publication {
		return
	}
	s.interruptions = publication
	for i, previous := range s.Entries {
		settled := s.settleInterrupt(previous.Source)
		if settled.Native != previous.Source.Native {
			s.Model.Replace(i, settled, ActivityContent(settled).Values())
		}
	}
}

func (s *ActivitySource) settleInterrupt(entry ActivityEntry) ActivityEntry {
	if entry.Native == nil || entry.Native.InterruptID == "" {
		return entry
	}
	if outcome := s.interruptions.Outcome(entry.Native.Thread, entry.Native.InterruptID); outcome != "" {
		entry.Native = new(*entry.Native)
		entry.Text = settledInterrupt(entry.Native.InterruptTarget, string(outcome))
		if entry.Native.Wait != nil {
			entry.Native.Wait = new(*entry.Native.Wait)
			entry.Native.Wait.Body = entry.Text
		}
		entry.Native.InterruptID = ""
	}
	return entry
}
