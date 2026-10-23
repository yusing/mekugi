package launch

import (
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
	for _, member := range n.threads.shared.Descendants(parent) {
		id := member.Thread
		execution.session.RegisterThread(observation.Thread{ID: id, Path: member.Task, Role: member.Role})
		agent := execution.session.Activity().Agent(id)
		agent.ParentThread = member.Parent
		execution.session.Lifecycle(id).Paused = member.Status == control.Paused || member.Status == control.Uncertain
		child, live := children[id]
		if !live {
			execution.session.Lifecycle(id).RestoreFinal(member.Status == control.Completed)
			continue
		}
		agent.SettingsLive = true
		agent.Settings = preferences.Evidence{Model: child.Caption.Model, Effort: child.Caption.Effort, Tier: child.Caption.Tier, TierKnown: child.Caption.Tier != ""}
		agent.ContextKnown, agent.ContextTokens, agent.ContextWindow = child.Caption.ContextKnown, child.Caption.ContextTokens, child.Caption.ContextWindow
	}
	sessionpresentation.ApplyActivity(execution.source, execution.childSource, nil, execution.session.Roster())
	return children
}

// A projection retains the immutable source identities that produced it.
// Unchanged child snapshots must not force the transport to re-encode history.
type nativeChildProjection struct {
	source, main, agents, publication *activity.Publication
	displayed                         sessionpresentation.Thread
	children                          map[string]sessionapi.Descendant
}

func (n *nativeViews) publishChild(v *nativeSessionView, frame *sessionapi.NativeFrame, children map[string]sessionapi.Descendant) {
	if n.threads == nil {
		return
	}
	displayed := frame.View.Displayed
	if displayed.ID == frame.View.Main.ID || displayed == frame.View.SideThread {
		displayed = sessionpresentation.Thread{}
	}
	if v.childThread != displayed.ID {
		v.child.Close()
		v.child, v.childThread = nil, displayed.ID
	}
	child, available := children[displayed.ID]
	if available && v.child == nil {
		var err error
		v.child, err = n.threads.hub.Observe(v.ctx, frame.View.Main.ID, displayed.ID)
		if err != nil {
			frame.View.Loading = err.Error()
		}
	}
	if v.child != nil {
		event, err := v.child.Snapshot()
		if err != nil {
			frame.View.Loading = err.Error()
		}
		if event.Frame.Main != nil {
			available = true
			child.Main, child.Agents, child.Caption = event.Frame.Main, event.Frame.Agents, event.Frame.View.Caption
			child.Session.Workspace = event.Frame.View.Info.Workspace
			frame.AgentOutput = event.Frame.MainOutput
		}
	}
	if available {
		role := frame.View.Caption.Role
		frame.View.Caption = child.Caption
		frame.View.Caption.Role = role
		frame.View.DisplayedWorkspace = child.Session.Workspace
	}
	if frame.Agents == nil {
		return
	}
	previous := v.childProjection
	same := previous.source == frame.Agents && previous.displayed == displayed && previous.main == child.Main && previous.agents == child.Agents && len(previous.children) == len(children)
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
		if row.Agent.Thread == displayed.ID && available {
			runtime, ok = child, true
		}
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
	if available && child.Main != nil {
		changed = true
		publication.Entries = childEntries(child.Main.Entries, displayed.Caller)
		publication.LastSeq = child.Main.LastSeq
	}
	projected := frame.Agents
	if changed {
		projected = &publication
	}
	v.childProjection = nativeChildProjection{source: frame.Agents, main: child.Main, agents: child.Agents, displayed: displayed, children: children, publication: projected}
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
