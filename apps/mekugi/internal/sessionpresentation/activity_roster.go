package sessionpresentation

import (
	"cmp"

	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Activity borrows the existing feature owner in this attachment's scope.
func (a *Attachment) Activity() *activitypresentation.Session[ActivityEntry, ActivityAgent] {
	return activitypresentation.Attached[ActivityEntry, ActivityAgent](a.Scope())
}

// RegisterThread admits supplied metadata without reading retained state.
// Source: apps/mekugi/internal/router/app_server_events.go:91:112@29a3a244.
func (a *Attachment) RegisterThread(info observation.Thread) {
	if info.ID != "" {
		a.Thread(info.ID)
	}
	old := a.KnownCaller(info.ID)
	name := info.Path
	if name == "" && old != Placeholder(info.ID) {
		name = old
	}
	if name == "" && info.Nickname != "" {
		name = "/root/" + info.Nickname
	}
	name = cmp.Or(name, Placeholder(info.ID))
	state := a.Activity()
	if agent := state.Agent(info.ID); agent != nil {
		agent.Name, agent.Role = name, cmp.Or(info.Role, agent.Role)
		agent.NonAttachable = agent.NonAttachable || info.NonAttachable
	} else {
		state.AddAgent(ActivityAgent{Thread: info.ID, Name: name, Role: info.Role, NonAttachable: info.NonAttachable})
	}
}

func (a *Attachment) Agent(name string) *ActivityAgent {
	agent := a.Activity().AgentNamed(name)
	if agent != nil {
		agent.AgentLifecycle = *a.Lifecycle(agent.Thread)
	}
	return agent
}

func (a *Attachment) Lifecycle(thread string) *activitypresentation.AgentLifecycle {
	return a.Activity().Lifecycle(a.Thread(thread))
}

// Roster publishes detached lifecycle values with the existing host metadata.
func (a *Attachment) Roster() []ActivityAgent {
	agents := a.Activity().Agents()
	for i := range agents {
		agents[i].AgentLifecycle = *a.Lifecycle(agents[i].Thread)
	}
	return agents
}

func (a *Attachment) KnownCaller(thread string) string {
	if agent := a.Activity().Agent(thread); agent != nil {
		return agent.Name
	}
	return ""
}

// Caller preserves first-observation ordering before metadata arrives.
func (a *Attachment) Caller(thread string) string {
	if name := a.KnownCaller(thread); name != "" {
		return name
	}
	name := Placeholder(thread)
	a.Activity().AddAgent(ActivityAgent{Thread: thread, Name: name})
	return name
}

func Placeholder(thread string) string { return "/root/" + thread[:min(8, len(thread))] }
