package binding

import (
	"cmp"
	json "encoding/json/v2"
	"slices"

	"github.com/yusing/mekugi/contracts/observation"
)

// Observation supplies public item facts without changing the native source.
// Variant-specific private reasoning remains in the original host payload.
func (i ThreadItem) Observation() observation.Item {
	out := observation.Item{
		ID: i.ID, Type: i.Type, Status: i.Status, Delivery: i.Delivery, Phase: i.Phase,
		Cwd: i.Cwd, Kind: i.Kind, Path: i.Path, Command: i.Command, ProcessID: i.ProcessID,
		ClientID: i.ClientID, AgentThreadID: i.AgentThreadID, AgentPath: i.AgentPath,
		Tool: i.Tool, SenderThreadID: i.SenderThreadID, ReceiverThreadIDs: slices.Clone(i.ReceiverThreadIDs),
		Prompt: i.Prompt, Model: i.Model, ReasoningEffort: i.ReasoningEffort,
		DurationMS: i.DurationMS, AggregatedOutput: i.AggregatedOutput, ExitCode: i.ExitCode,
		Summary: slices.Clone(i.Summary), Query: i.Query,
	}
	if i.Type == "agentMessage" {
		out.Text = i.Text
	}
	if i.Type == "userMessage" {
		out.Content = slices.Clone(i.Content)
	}
	for _, q := range i.Questions {
		out.Questions = append(out.Questions, observation.Question{ID: q.ID, Header: q.Header, Question: q.Question, Title: q.Title, IsSecret: q.IsSecret, Options: slices.Clone(q.Options)})
	}
	for _, c := range i.Changes {
		out.Changes = append(out.Changes, c.Observation())
	}
	for _, a := range i.CommandActions {
		out.CommandActions = append(out.CommandActions, observation.CommandAction{Type: a.Type, Command: a.Command, Path: a.Path, Query: a.Query})
	}
	if a := i.Action; a != nil {
		out.Action = &observation.WebSearchAction{Type: a.Type, Query: a.Query, Queries: slices.Clone(a.Queries), URL: a.URL, Pattern: a.Pattern}
	}
	if i.Results != nil {
		out.Results = new(len(i.Results))
	}
	if i.AgentsStates != nil {
		out.AgentsStates = make(map[string]observation.AgentState, len(i.AgentsStates))
		for id, state := range i.AgentsStates {
			out.AgentsStates[id] = observation.AgentState{Status: state.Status}
		}
	}
	return out
}

func (c FileChange) Observation() observation.FileChange {
	out := observation.FileChange{Path: c.Path, Diff: c.Diff}
	out.Kind.Type, out.Kind.MovePath = c.Kind.Type, cmp.Or(c.Kind.MovePath, c.Kind.MovePath2)
	return out
}

// Observation admits supplied lineage labels without opening a rollout.
// Source: apps/mekugi/internal/router/app_server_events.go:125:152@[439670ce].
func (t ThreadInfo) Observation() observation.Thread {
	var source struct {
		SubAgent struct {
			ThreadSpawn struct {
				AgentPath string `json:"agent_path"`
				AgentRole string `json:"agent_role"`
			} `json:"thread_spawn"`
		} `json:"subAgent"`
	}
	_ = json.Unmarshal(t.Source, &source)
	return observation.Thread{ID: t.ID, Nickname: t.AgentNickname, Path: source.SubAgent.ThreadSpawn.AgentPath, Role: cmp.Or(t.AgentRole, source.SubAgent.ThreadSpawn.AgentRole)}
}
