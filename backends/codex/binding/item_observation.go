package binding

import (
	"cmp"
	"slices"

	"github.com/yusing/mekugi/contracts/observation"
)

// Observation supplies public item facts without changing the native source.
// Variant-specific private reasoning remains in the original host payload.
func (i ThreadItem) Observation() observation.Item {
	out := observation.Item{
		UserShell: i.Type == "commandExecution" && i.Source == "userShell",
		ID:        i.ID, Type: i.Type, Status: i.Status, Delivery: i.Delivery, Phase: i.Phase,
		Cwd: i.Cwd, Path: i.Path, Command: i.Command, ProcessID: i.ProcessID,
		ClientID:   i.ClientID,
		Tool:       i.Tool,
		DurationMS: i.DurationMS, AggregatedOutput: i.AggregatedOutput, ExitCode: i.ExitCode,
		Summary: slices.Clone(i.Summary), Query: i.Query,
	}
	if i.Type == "agentMessage" {
		out.Text = i.Text
	}
	if i.Type == "mcpToolCall" {
		out.Server = i.Server
		out.Arguments, out.Result, out.Error = slices.Clone(i.Arguments), slices.Clone(i.Result), slices.Clone(i.Error)
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
	return observation.Thread{ID: t.ID, Nickname: t.AgentNickname, Role: t.AgentRole}
}
