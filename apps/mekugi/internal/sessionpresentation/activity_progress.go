package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_progress.go:23:26,58:102@7166de95.
import (
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/activity/waittargets"
)

// WaitEvidence is admitted storage input, not a retained-store capability.
type WaitEvidence struct {
	Targets []waittargets.Target
	Found   bool
}

func (s *Attachment) ProjectProgress(waits *activitypresentation.Waits, item observation.Item, thread, turn, id, phase string, observed bool, evidence WaitEvidence) (string, *activitypresentation.Value, bool, *activitypresentation.WaitProgress) {
	if item.Type == "contextCompaction" {
		if phase == "item/completed" {
			return "Context compacted", nil, true, nil
		}
		return "", nil, true, nil
	}
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" {
		return "", nil, false, nil
	}
	in := activitypresentation.WaitObservation{
		Item:  item,
		Phase: phase, Observed: observed && thread != "" && turn != "" && id != "", Caller: s.Caller(thread),
		Snapshot: evidence.Targets, SnapshotFound: evidence.Found,
	}
	in.Item.ID = id
	for _, agent := range s.Roster() {
		in.Agents = append(in.Agents, activitypresentation.WaitAgent{Thread: agent.Thread, Name: agent.Name, Responding: agent.Responding})
	}
	progress := waits.Observe(s.Transcript(thread, turn).Turn, in)
	return progress.Value.ProgressText(), &progress.Value, true, &progress
}

// Main progress consumes Activity's authoritative retained item evidence. The
// application adapts host facts; session presentation owns precedence and keys.
func (m *Model) ObserveActivityProgress(p observation.Event, main, selected string, source *ActivitySource) {
	method := string(p.Kind)
	if method == "item/commandExecution/terminalInteraction" {
		m.TerminalInteraction(p.ThreadID, p.TurnID, selected, p.ProcessID, p.Stdin)
		return
	}
	completed := false
	if p.ThreadID == main && method == "item/started" && p.Item.Type == "contextCompaction" {
		for _, entry := range source.Model.Entries {
			native := entry.Source.Native
			if native != nil && native.Thread == p.ThreadID && native.Turn == p.TurnID && native.Item == p.Item.ID && native.Phase == "item/completed" {
				completed = true
				break
			}
		}
	}
	m.ObserveProgress(p, selected, completed)
}
