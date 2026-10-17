package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

type activityPaneEntry struct {
	Seq         uint64
	Agent       string
	Kind        string
	Text        string
	ErrorDetail string `json:",omitempty"` // Full error evidence; Text is the bounded preview.
	CallID      string `json:",omitempty"`
	Observed    time.Time
	completed   time.Time // Completion after the observed output has been presented.

	native      *liveActivityNativeItem // In-process app-server lifecycle input, not a provider observation.
	assignment  *activityAssignment     // Validated native NEW_TASK, not an ordinary message.
	message     *activityMessage        // A directed message, for a reply entry.
	start       *activityStart          // A child's start, for a start entry.
	activitySeq uint64                  // Main excerpt's exact entry in Activity, never a question target.
	outputTail  []string                // Failed command's sanitized final output lines, on an exit entry.
	outputOmit  int                     // Output lines before outputTail.
}

type activityPaneAgent struct {
	activitypresentation.AgentLifecycle
	thread       string // Stable host identity; caller names remain presentation labels.
	turn         string // Current child turn; Main's identity stays with session control.
	settings     preferences.Evidence
	settingsLive bool
	Name         string
	Role         string `json:",omitzero"`
	// Provider requests forwarded for the agent, shown as T+N.
	Roundtrips  uint64  `json:",omitzero"`
	Cost        float64 `json:",omitzero"`
	CostKnown   bool    `json:",omitzero"`
	CostPartial bool    `json:",omitzero"`
	// Cumulative provider-reported tokens for the agent's responses.
	InputTokens       uint64                     `json:",omitzero"`
	OutputTokens      uint64                     `json:",omitzero"`
	OutputThroughput  inference.OutputThroughput `json:",omitzero"`
	TokensKnown       bool                       `json:",omitzero"`
	UsagePartial      bool                       `json:",omitzero"`
	RoundtripsPartial bool                       `json:",omitzero"`
	// Latest host-reported context, not cumulative provider usage.
	ContextTokens uint64 `json:",omitzero"`
	ContextWindow uint64 `json:",omitzero"`
	ContextKnown  bool   `json:",omitzero"`
}

type activityPaneEvent struct {
	Kind    string
	Agents  []activityPaneAgent `json:",omitempty"`
	Entries []activityPaneEntry `json:",omitempty"`
}

func takeNativeActivity(a *sessioncontrol.Audience, root string) []activityPaneEntry {
	audience, events := a.Take(root)
	var entries []activityPaneEntry
	for _, event := range events {
		entry := activityPaneEntry{Agent: audience.Threads[event.Thread], Kind: event.Kind, Text: event.Text, CallID: event.CallID, Observed: event.Observed}
		if event.Kind == "error" {
			entry.ErrorDetail, entry.Text = event.Text, activityui.ErrorPreview(event.Text)
		}
		if a := event.Assignment; a != nil {
			entry.assignment = &activityAssignment{id: a.ID, from: a.From, to: a.To, text: a.Text, created: a.Created}
		}
		if m := event.Message; m != nil {
			entry.message = &activityMessage{from: m.From, to: m.To, text: m.Text}
		}
		if s := event.Start; s != nil {
			entry.start = &activityStart{model: s.Model, effort: s.Effort, tier: s.Tier}
		}
		if event.Kind == "output_dedupe" {
			entry.native = &liveActivityNativeItem{thread: event.Thread}
		}
		entries = append(entries, entry)
	}
	return entries
}

func (entry activityPaneEntry) ActivityFacts() activitypresentation.Facts {
	facts := activitypresentation.Facts{Seq: entry.Seq, Agent: entry.Agent, Kind: entry.Kind, Text: entry.Text, Observed: entry.Observed, Activity: entry.activitySeq, Completed: entry.completed, ContextBoundary: contextBoundary(entry)}
	if entry.assignment != nil {
		facts.Assignment = true
		facts.AssignmentTo, facts.AssignmentText = entry.assignment.to, entry.assignment.text
	}
	if n := entry.native; n != nil {
		facts.Running, facts.Live, facts.Restored, facts.Unconfirmed = n.running, n.live, !n.live, n.unconfirmed()
		facts.ItemComplete = n.phase == "item/completed"
		facts.Spans, facts.Question = n.spans, n.question
		facts.Suppressed, facts.RosterOnly = n.phase == "queued-summary", n.wait != nil
		facts.OutputComplete = n.exit != nil && *n.exit == 0 || !n.live && len(n.segments) > 0 ||
			n.hook != nil && n.hook.Status == "completed" && !n.hook.HasError
		if n.turn != "" {
			facts.InputGroup = n.thread + "\x00" + n.turn
		}
		if n.output != nil {
			state, _ := n.output.Context(0)
			facts.OutputFailed = state.Exited && state.Exit != 0
		}
	}
	return facts
}
func (agent activityPaneAgent) ActivityAgent() activitypresentation.AgentFacts {
	return activitypresentation.AgentFacts{
		Name: agent.Name, Role: agent.Role, Thread: agent.thread, Responding: agent.Responding, Final: agent.Final,
		Started: agent.Started, LastResponse: agent.LastResponse,
		WorkElapsed: time.Duration(agent.WorkTimer.ElapsedNS), WorkSince: agent.WorkTimer.Since, WorkKnown: agent.WorkTimer.Known,
		Turns: agent.Turns, Roundtrips: agent.Roundtrips, Cost: agent.Cost, CostKnown: agent.CostKnown, CostPartial: agent.CostPartial,
		InputTokens: agent.InputTokens, OutputTokens: agent.OutputTokens, OutputThroughput: agent.OutputThroughput,
		TokensKnown: agent.TokensKnown, UsagePartial: agent.UsagePartial, RoundtripsPartial: agent.RoundtripsPartial,
		ContextTokens: agent.ContextTokens, ContextWindow: agent.ContextWindow, ContextKnown: agent.ContextKnown,
	}
}
