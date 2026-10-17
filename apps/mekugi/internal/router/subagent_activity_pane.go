package router

import (
	"time"

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

func (a *subagentActivity) releasePane() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.supplements.Detach()
}

// Main already knows its app-server thread before the first provider request.
// The native frontend binds supplemental observations to its active root.
// App-server notifications supply the live tool and lifecycle display.
func (a *subagentActivity) attachNativePane(root string) {
	a.observe(root, "", "/root", false)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.supplements.Attach(root)
}

// A thinking response starts Conversation's pre-delta block. Host observations,
// not provider rounds, own the roster lifecycle.
func (a *subagentActivity) beginResponse(thread string, thinking bool, at time.Time) {
	if a == nil || !thinking {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; node != nil && !node.conflicted && !a.closed {
		a.supplements.StartRequest(thread, at)
	}
}

func (a *subagentActivity) takeRequestStarts(root string) []activityRequestStart {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	audience, _ := a.audienceLocked(root)
	var starts []activityRequestStart
	for _, start := range a.supplements.TakeStarts(audience) {
		starts = append(starts, activityRequestStart{thread: start.Thread, at: start.At})
	}
	return starts
}

func (a *subagentActivity) noteUnreturned(thread string, calls []string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.supplements.NoteUnreturned(thread, calls)
	}
}

func (a *subagentActivity) takeUnreturned(root string) []activityToolRef {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	audience, _ := a.audienceLocked(root)
	var refs []activityToolRef
	for _, ref := range a.supplements.TakeUnreturned(audience) {
		refs = append(refs, activityToolRef{thread: ref.Thread, call: ref.Call})
	}
	return refs
}

func (a *subagentActivity) childThread(root, name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for thread, node := range a.threads {
		if node.child && !node.conflicted && node.name == name && a.rootLocked(thread) == root {
			return thread
		}
	}
	return ""
}

// Native observations supply the lifecycle; this adapter delivers supplemental
// facts admitted by the same current application authority.
func (a *subagentActivity) takeNativeActivity(root string) []activityPaneEntry {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	audience, _ := a.audienceLocked(root)
	var entries []activityPaneEntry
	for _, event := range a.supplements.Take(audience, time.Now()) {
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
