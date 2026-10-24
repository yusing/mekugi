package launch

import (
	"maps"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// descendants borrows publications from the actual runtime owners. Only small
// admitted identity/settings facts enter the parent's existing Activity roster.
func (n *nativeViews) descendants(execution *attachedPresentation, parent string) map[string]sessionapi.Descendant {
	if n.threads == nil || n.threads.hub == nil {
		return nil
	}
	children := n.threads.hub.Descendants(parent)
	liveChildren := make(map[string]sessionapi.Descendant, len(children))
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
			liveChildren[id] = sessionapi.Descendant{Session: sessionapi.Session{Thread: id}}
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
	source, publication *activity.Publication
	children            map[string]sessionapi.Descendant
}

func (n *nativeViews) publishChild(v *nativeSessionView, frame *sessionapi.NativeFrame, children map[string]sessionapi.Descendant) {
	if n.threads == nil {
		return
	}
	frame.ChildThreads = slices.Sorted(maps.Keys(children))
	if frame.Agents == nil {
		return
	}
	previous := v.childProjection
	same := previous.source == frame.Agents && len(previous.children) == len(children)
	if same {
		for id, runtime := range children {
			if previous.children[id].Agents != runtime.Agents {
				same = false
				break
			}
		}
	}
	if same {
		frame.Agents = previous.publication
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
	projected := frame.Agents
	if changed {
		projected = &publication
	}
	v.childProjection = nativeChildProjection{source: frame.Agents, children: children, publication: projected}
	frame.Agents = projected
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
