package sessionpresentation

import (
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// Source: apps/mekugi/internal/router/subagent_activity_pane.go:15:32@45fbe279
type ActivityEntry struct {
	ReloadSources []activitypresentation.ReloadSource
	Side          bool `json:"-"` // Invocation-local side projection, retired with its owner.
	Seq           uint64
	Agent         string
	Kind          string
	Text          string
	ErrorDetail   string `json:",omitempty"` // Full error evidence; Text is the bounded preview.
	CallID        string `json:",omitempty"`
	Observed      time.Time
	Completed     time.Time `json:"-"` // Admitted completion; terminal reveal keeps its own clock.

	Native      *NativeItem         `json:"-"` // In-process app-server lifecycle input, not a provider observation.
	Assignment  *ActivityAssignment `json:"-"` // Validated native NEW_TASK, not an ordinary message.
	Message     *ActivityMessage    `json:"-"` // A directed message, for a reply entry.
	Start       *ActivityStart      `json:"-"` // A child's start, for a start entry.
	ActivitySeq uint64              `json:"-"` // Main excerpt's exact entry in Activity, never a question target.
	OutputTail  []string            `json:"-"` // Failed command's sanitized final output lines, on an exit entry.
	OutputOmit  int                 `json:"-"` // Output lines before outputTail.
}

// Source: apps/mekugi/internal/router/subagent_activity_pane.go:34:58@45fbe279
type ActivityAgent struct {
	activitypresentation.AgentLifecycle
	ParentThread    string               `json:"-"` // Host ancestry for delayed metadata admission.
	AudiencePending bool                 `json:"-"` // Activity stays visible while its Main audience is unresolved.
	Isolated        bool                 `json:"-"` // Invocation-local audience, keyed by host thread identity.
	Thread          string               `json:"-"` // Stable host identity; caller names remain presentation labels.
	Turn            string               `json:"-"` // Current child turn; Main's identity stays with session control.
	Settings        preferences.Evidence `json:"-"`
	SettingsLive    bool                 `json:"-"`
	Name            string
	Role            string `json:",omitzero"`
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

// Source: apps/mekugi/internal/router/subagent_activity_pane.go:60:64@45fbe279
type ActivityEvent struct {
	Agents  []ActivityAgent `json:",omitempty"`
	Entries []ActivityEntry `json:",omitempty"`
}

// Source: apps/mekugi/internal/router/subagent_activity_pane.go:91:113@45fbe279
func (entry ActivityEntry) ActivityFacts() activitypresentation.Facts {
	facts := activitypresentation.Facts{Retained: entry.Side, Seq: entry.Seq, Agent: entry.Agent, Kind: entry.Kind, Text: entry.Text, Observed: entry.Observed, Activity: entry.ActivitySeq, Completed: entry.Completed, ContextBoundary: ContextBoundary(entry)}
	// Restored recipient envelopes have no native item, but their body has
	// already been delivered just like a live recipient-input supplement.
	facts.ItemComplete = entry.Native == nil && entry.Kind == "reply" && entry.Message != nil
	if entry.Assignment != nil {
		facts.Assignment = true
		facts.AssignmentTo, facts.AssignmentText = entry.Assignment.To, entry.Assignment.Text
	}
	if n := entry.Native; n != nil {
		if entry.Kind == "reasoning" {
			facts.TextRef = observation.ItemRef{ThreadID: n.Thread, TurnID: n.Turn, ItemID: n.Item}
		}
		facts.Running, facts.Live, facts.Restored, facts.Unconfirmed = n.Running, n.Live, !n.Live, n.Unconfirmed()
		// Recipient-input supplements contain an already-delivered complete
		// message, even when Codex emitted no public tool-completion item.
		facts.ItemComplete = n.Phase == "item/completed" || n.Phase == "supplement" && entry.Kind == "reply" && entry.Message != nil
		facts.Spans, facts.Question = n.Spans, n.Question
		facts.Images = n.Images
		facts.Suppressed, facts.RosterOnly = n.Phase == "queued-summary", n.Wait != nil
		facts.OutputComplete = n.Exit != nil && *n.Exit == 0 || !n.Live && len(n.Segments) > 0 ||
			n.Hook != nil && n.Hook.Status == "completed" && !n.Hook.HasError
		if n.Turn != "" {
			facts.InputGroup = n.Thread + "\x00" + n.Turn
		}
		if n.Output != nil {
			state, _ := n.Output.Context(0)
			facts.OutputFailed = state.Exited && state.Exit != 0
		}
	}
	return facts
}

// Source: apps/mekugi/internal/router/subagent_activity_pane.go:114:124@45fbe279
func (agent ActivityAgent) ActivityAgent() activitypresentation.AgentFacts {
	return activitypresentation.AgentFacts{
		Name: agent.Name, Role: agent.Role, Thread: agent.Thread, Responding: agent.Responding, Final: agent.Final, Waiting: agent.Waiting, Paused: agent.Paused,
		Started: agent.Started, LastResponse: agent.LastResponse,
		WorkElapsed: time.Duration(agent.WorkTimer.ElapsedNS), WorkSince: agent.WorkTimer.Since, WorkKnown: agent.WorkTimer.Known,
		Turns: agent.Turns, Roundtrips: agent.Roundtrips, Cost: agent.Cost, CostKnown: agent.CostKnown, CostPartial: agent.CostPartial,
		InputTokens: agent.InputTokens, OutputTokens: agent.OutputTokens, OutputThroughput: agent.OutputThroughput,
		TokensKnown: agent.TokensKnown, UsagePartial: agent.UsagePartial, RoundtripsPartial: agent.RoundtripsPartial,
		ContextTokens: agent.ContextTokens, ContextWindow: agent.ContextWindow, ContextKnown: agent.ContextKnown,
	}
}
