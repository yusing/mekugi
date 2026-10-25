package launch

import (
	"maps"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func publishNativeDelivery(attached *attachedPresentation, input control.NativeInput, now time.Time) {
	entry := nativeDeliveryEntry(input, control.MessageItemID(input.ID), now)
	entry.Seq = attached.session.Activity().NextSequence()
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
}

func nativeDeliveryEntry(input control.NativeInput, item string, now time.Time) sessionpresentation.ActivityEntry {
	entry := sessionpresentation.ActivityEntry{Agent: input.Sender, Kind: "final", Text: input.Message, Observed: now, Completed: now,
		Native: &sessionpresentation.NativeItem{Thread: input.Thread, Item: item, Phase: "item/completed"}}
	if input.Kind == "MESSAGE" {
		entry.Kind = "reply"
		entry.Message = &sessionpresentation.ActivityMessage{From: input.Sender, To: "Main", Text: input.Message}
	}
	return entry
}

// descendants borrows publications from the actual runtime owners. Only small
// admitted identity/settings facts enter the parent's existing Activity roster.
func (n *nativeViews) descendants(execution *attachedPresentation, parent string) map[string]sessionapi.Frame {
	if n.threads == nil || n.threads.hub == nil {
		return nil
	}
	children := n.threads.hub.Descendants(parent)
	liveChildren := make(map[string]sessionapi.Frame, len(children))
	for _, member := range n.threads.shared.Descendants(parent) {
		id := member.Thread
		execution.session.RegisterThread(observation.Thread{ID: id, Path: member.Task, Role: member.Role})
		agent := execution.session.Activity().Agent(id)
		agent.ParentThread = member.Parent
		lifecycle := execution.session.Lifecycle(id)
		lifecycle.Paused = member.Status == control.Paused || member.Status == control.Uncertain
		lifecycle.Responding = member.Status == control.Working || member.Status == control.Pending
		lifecycle.Waiting = member.Status == control.Waiting
		lifecycle.Final = member.Status == control.Completed
		child, live := children[id]
		if !live {
			liveChildren[id] = sessionapi.Frame{Session: sessionapi.Session{Thread: id}}
			execution.session.Lifecycle(id).RestoreFinal(member.Status == control.Completed)
			continue
		}
		liveChildren[id] = child
		agent.SettingsLive = true
		agent.Settings = preferences.Evidence{Model: child.Caption.Model, Effort: child.Caption.Effort, Tier: child.Caption.Tier, TierKnown: child.Caption.Tier != ""}
		agent.ContextKnown, agent.ContextTokens, agent.ContextWindow = child.Caption.ContextKnown, child.Caption.ContextTokens, child.Caption.ContextWindow
	}
	sessionpresentation.ApplyActivity(execution.source, execution.childSource, nil, execution.session.Roster())
	return liveChildren
}

// A projection retains the immutable source identities that produced it.
// Unchanged child snapshots must not force the transport to re-encode history.
type nativeChildProjection struct {
	source, publication        *activity.Publication
	children                   map[string]sessionapi.Frame
	reviewSource, review, diff *changes.ReviewView
}

func (n *nativeViews) publishChild(frame *sessionapi.NativeFrame, children map[string]sessionapi.Frame) {
	if n.threads == nil {
		return
	}
	frame.ChildThreads = slices.Sorted(maps.Keys(children))
	if frame.Agents == nil {
		return
	}
	previous := n.childProjection
	reviewSame := sameReview(previous.reviewSource, frame.Review) && len(previous.children) == len(children)
	same := previous.source == frame.Agents && len(previous.children) == len(children)
	for id, runtime := range children {
		old, exists := previous.children[id]
		same = same && exists && old.Agents == runtime.Agents
		reviewSame = reviewSame && exists && sameReview(old.Review, runtime.Review)
	}
	if same && reviewSame {
		frame.Agents, frame.Review = previous.publication, previous.diff
		return
	}
	publication := *frame.Agents
	changed := false
	for i := range publication.Roster {
		row := &publication.Roster[i]
		runtime, ok := children[row.Agent.Thread]
		if !ok || runtime.Agents == nil {
			continue
		}
		for _, own := range runtime.Agents.Roster {
			if own.Agent.Thread != row.Agent.Thread {
				continue
			}
			if !changed {
				publication.Roster = slices.Clone(publication.Roster)
				changed = true
			}
			row = &publication.Roster[i]
			name, role := row.Agent.Name, row.Agent.Role
			row.Agent = own.Agent
			row.Agent.Name, row.Agent.Role = name, role
			row.Agent.Summary = childEntries(own.Agent.Summary, name)
			break
		}
	}
	// All runtimes publish through the same saved-review owner contract. Compose
	// their admitted immutable captures once, not counters or a second ledger.
	review, diff := previous.review, previous.diff
	if !reviewSame && frame.Review != nil {
		combined := *frame.Review
		var chunks []changes.Chunk
		seen := make(map[string]bool)
		add := func(view *changes.ReviewView) {
			if view == nil {
				return
			}
			for _, file := range view.Files {
				for _, chunk := range file.Chunks {
					key := chunk.Workspace + "\x00" + chunk.Key
					if !seen[key] {
						seen[key] = true
						chunks = append(chunks, chunk)
					}
				}
			}
			if combined.Failure == "" {
				combined.Failure = view.Failure
			}
		}
		add(frame.Review)
		for _, id := range frame.ChildThreads {
			add(children[id].Review)
		}
		if len(children) > 0 {
			combined.Publication = combined.Publication.With(changes.GroupCaptures(chunks))
		}
		combined.Revision = previous.reviewRevision() + 1
		review = &combined
		pane := combined
		pane.Publication = pane.Publication.ForDiff(pane.Workspace)
		diff = &pane
	}
	if frame.Review == nil {
		review, diff = nil, nil
	}
	if review != nil && len(children) > 0 {
		publication.LineCounts, publication.NetCounts = review.Counts.Callers, review.Counts.Net
		changed = true
	}
	projected := frame.Agents
	if changed {
		projected = &publication
	}
	n.childProjection = nativeChildProjection{source: frame.Agents, children: children, publication: projected, reviewSource: frame.Review, review: review, diff: diff}
	frame.Agents, frame.Review = projected, diff
}

// Child source identities stay unchanged so output/detail reads resolve at the
// original owner. Only the viewing parent's caller labels are projected.
func childEntries(entries []activity.Entry, caller string) []activity.Entry {
	entries = slices.Clone(entries)
	for i := range entries {
		entry := &entries[i]
		if entry.Agent == "Main" || entry.Agent == "/root" {
			entry.Agent = caller
		}
		entry.Values = activity.CloneValues(entry.Values)
		var relabel func([]activity.Value)
		relabel = func(values []activity.Value) {
			for j := range values {
				value := &values[j]
				for _, label := range []*string{&value.Owner, &value.From} {
					if *label == "Main" || *label == "/root" {
						*label = caller
					}
				}
				// Recipients are global logical task paths. Only the renderer's
				// explicit Main alias denotes this independently hosted child.
				if value.To == "Main" {
					value.To = caller
				}
				relabel(value.Members)
				relabel(value.RequestedOperations)
			}
		}
		relabel(entry.Values)
	}
	return entries
}

func sameReview(a, b *changes.ReviewView) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Revision == b.Revision && a.Workspace == b.Workspace && a.Coverage == b.Coverage && a.Failure == b.Failure
}

func (p nativeChildProjection) reviewRevision() uint64 {
	if p.review == nil {
		return 0
	}
	return p.review.Revision
}
