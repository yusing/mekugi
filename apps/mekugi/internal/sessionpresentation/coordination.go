package sessionpresentation

import (
	json "encoding/json/v2"
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
	if c.Kind == "side-question" {
		from := entry.Agent
		if from == "/root" {
			from = "Main"
		}
		entry.SideQuestion = &activitypresentation.SideQuestion{From: from, To: target, Question: c.Message, Answer: c.Answer, Answered: c.Confirmed}
	}
	if c.Action == "Spawn" && c.State == "pending" {
		entry.Text = "Spawning " + target
	}
	if c.Confirmed {
		switch c.Action {
		case "Compact":
			entry.Text = "Compaction requested"
		case "Wait":
			for i := range c.Targets {
				if caller := s.KnownCaller(c.Targets[i].Name); caller != "" {
					c.Targets[i].Name = caller
				}
			}
			wait := coordinationWait(c)
			wait.From = entry.Agent
			entry.Text, entry.Native.Wait = wait.ProgressText(), wait
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
	if entry.Native.Wait.Body == "" {
		entry.Native.Wait.Body = entry.Text
	}
	return entry
}

func coordinationWait(c coordination.Coordination) *activitypresentation.Value {
	// Waits select all eligible targets; any/all controls the wake condition.
	// Empty acknowledgements do not expose those targets' individual names.
	wait := &activitypresentation.Value{Kind: "progress", Body: "Waiting for all"}
	if len(c.Targets) > 0 {
		wait.Body = "Waiting for"
	}
	if c.State != "waiting" && c.State != "compacting" {
		wait.Body, wait.WaitReason = "Finished waiting for", c.State
	}
	if c.State == "completed" && c.EventKind == "message" {
		wait.WaitReason = "message received"
	}
	for _, target := range c.Targets {
		wait.WaitTargets = append(wait.WaitTargets, activitypresentation.WaitTarget{Name: target.Name, Status: target.Status})
	}
	if (c.State == "waiting" || c.State == "compacting") && c.TimeoutMS != nil {
		wait.WaitTimeout = (time.Duration(*c.TimeoutMS) * time.Millisecond).String()
	}
	return wait
}

// RetainedWaitEntry projects an authenticated wake through the ordinary
// coordination presentation, without restoring or executing the subscription.
func RetainedWaitEntry(thread, agent, name, output string, at time.Time) (ActivityEntry, bool) {
	if name != "wait_agent" {
		return ActivityEntry{}, false
	}
	c := coordination.ProjectWaitOutcome([]byte(output))
	if !c.Confirmed || c.ID == "" {
		return ActivityEntry{}, false
	}
	entry := ActivityEntry{Agent: agent, Observed: at, Completed: at,
		Native: &NativeItem{Thread: thread, Item: "threadcontrol:wait:" + strings.TrimPrefix(c.ID, thread+":"), Phase: "item/completed"}}
	entry.Kind = "progress"
	entry.Native.Wait = coordinationWait(c)
	entry.Native.Wait.From = agent
	entry.Text = entry.Native.Wait.ProgressText()
	return entry, true
}

// CompactionOutcomeEntry reports the reset's outcome independently of a pending
// compound wait. The retained operation identity also deduplicates restoration.
func CompactionOutcomeEntry(thread, agent, id string, result *control.CompactionResult, at time.Time) (ActivityEntry, bool) {
	if id == "" || result == nil {
		return ActivityEntry{}, false
	}
	kind, text := "progress", ""
	switch result.State {
	case "skipped":
		text = "Compaction skipped"
	case "failed":
		kind, text = "error", "Compaction not performed"
	case "canceled":
		text = "Compaction canceled"
	default:
		return ActivityEntry{}, false
	}
	if result.Error != "" {
		text += ": " + result.Error
	}
	return ActivityEntry{Agent: agent, Kind: kind, Text: text, Observed: at, Completed: at,
		Native: &NativeItem{Thread: thread, Item: "threadcontrol:compact:" + id, Phase: "item/completed"}}, true
}

// RetainedCompactionEntry consumes only a host-admitted standalone checkpoint
// output. Its caller retains namespace and client-authored provenance checks.
func RetainedCompactionEntry(thread, agent, name, output string, at time.Time) (ActivityEntry, bool) {
	if name != "compact" && name != "wait_agent" {
		return ActivityEntry{}, false
	}
	var result struct {
		ID         string                    `json:"id"`
		Compaction *control.CompactionResult `json:"compaction"`
	}
	if json.Unmarshal([]byte(output), &result) != nil {
		return ActivityEntry{}, false
	}
	return CompactionOutcomeEntry(thread, agent, result.ID, result.Compaction, at)
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
