package router

import (
	"cmp"
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// appServerSession adapts app-server notifications for every thread of the
// session into the roster and Activity entries. Captured edits and previews
// retain the shared router owners, as does cost.
type appServerSession struct {
	seq          uint64
	paths        map[string]string // Thread → canonical agent path.
	agents       []activityPaneAgent
	transcript   observation.Transcript
	observations *binding.Observation
	metadata     map[string]string                  // Child thread → pending metadata request ID; empty when settled.
	commands     map[[3]string]*appServerCommandRun // Live commands by thread, turn, item.
	activity     *activitymodel.Service
	sessionpresentation.Attachment
	cwd         string
	waits       activitypresentation.Waits
	waitStore   *mekugiReplayStore
	waitContext context.Context

	compactionRollout binding.ThreadInfo // Host-selected Main rollout, without its history.
}

// appServerCommandRun is a started command whose streamed output tail is
// re-sent on the next frame. Completion, not the stream, ends it.
type appServerCommandRun struct {
	segments execTrackPresentation
	entry    activityPaneEntry
	output   activityui.OutputTail
	dirty    bool
	// Instant operations, such as reads, show their output at once; other
	// commands roll a burst through.
	instant bool
	done    []activityPaneEntry // Completion, held until streamed output has rolled through.
	// A tracked command's completion waits for its segment report to end.
	completion  *activityPaneEntry
	completed   observation.Item
	completedAt time.Time
}

type appServerTokenUsage struct {
	TotalTokens           uint64 `json:"totalTokens"`
	InputTokens           uint64 `json:"inputTokens"`
	OutputTokens          uint64 `json:"outputTokens"`
	CachedInputTokens     uint64 `json:"cachedInputTokens"`
	ReasoningOutputTokens uint64 `json:"reasoningOutputTokens"`
}

type appServerTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type appServerEvent struct {
	command *binding.CommandObservation `json:"-"`
	text    observation.TextEvent       `json:"-"`
	Status  struct {
		Type string `json:"type"`
	} `json:"status"`
	ThreadID   string             `json:"threadId"`
	TurnID     string             `json:"turnId"`
	ItemID     string             `json:"itemId"`
	Delta      string             `json:"delta"`
	ProcessID  string             `json:"processId"`
	Stdin      string             `json:"stdin"`
	Item       binding.ThreadItem `json:"item"`
	Run        *appserver.HookRun `json:"run,omitempty"`
	Thread     binding.ThreadInfo `json:"thread"`
	TokenUsage struct {
		Total              appServerTokenUsage  `json:"total"`
		Last               *appServerTokenUsage `json:"last"`
		ModelContextWindow uint64               `json:"modelContextWindow"`
	} `json:"tokenUsage"`
	Turn appServerTurn `json:"turn"`
}

func (p appServerEvent) observation(method string, at time.Time) observation.Event {
	e := observation.Event{At: at, Kind: observation.Kind(method), ThreadID: p.ThreadID, TurnID: p.TurnID,
		ItemID: cmp.Or(p.ItemID, p.Item.ID), Delta: p.Delta, Item: p.Item.Observation(),
		ThreadStatus: p.Status.Type, ProcessID: p.ProcessID, Stdin: p.Stdin,
		TurnStatus: p.Turn.Status, Context: observation.Context{Window: p.TokenUsage.ModelContextWindow},
	}
	if p.Thread.ID != "" {
		e.Thread = p.Thread.Observation()
	}
	if p.TokenUsage.Last != nil {
		e.Context.Tokens, e.Context.Known = p.TokenUsage.Last.TotalTokens, true
	}
	if method == "turn/started" || method == "turn/completed" {
		e.TurnID = p.Turn.ID
	}
	return e
}

func (s *appServerSession) start(thread, cwd string) {
	s.paths = map[string]string{thread: "/root"}
	s.Start(thread)
	s.agents = []activityPaneAgent{{thread: thread, Name: "/root", Role: "main"}}
	s.transcript = s.observations.NewTranscript(s.Scope())
	s.metadata = make(map[string]string)
	s.commands = make(map[[3]string]*appServerCommandRun)
	s.waitStore, s.waitContext = nil, nil
	s.cwd = cwd
}

func (s *appServerSession) registerThread(info binding.ThreadInfo) {
	s.registerThreadFacts(info.Observation())
	s.restoreThreadSettings(info)
}

// registerThreadFacts admits supplied metadata without reading retained state.
func (s *appServerSession) registerThreadFacts(info observation.Thread) {
	if info.ID != "" {
		s.Thread(info.ID)
	}
	path := info.Path
	old := s.paths[info.ID]
	if path == "" && old != appServerPlaceholder(info.ID) {
		path = old
	}
	if path == "" && info.Nickname != "" {
		path = "/root/" + info.Nickname
	}
	path = cmp.Or(path, appServerPlaceholder(info.ID))
	if old := s.paths[info.ID]; old != "" {
		s.agent(old).Name = path
	} else {
		s.agents = append(s.agents, activityPaneAgent{thread: info.ID, Name: path})
	}
	s.paths[info.ID] = path
	s.agent(path).thread = info.ID
	s.agent(path).Role = cmp.Or(info.Role, s.agent(path).Role)
}

func (s *appServerSession) restoreThreadSettings(info binding.ThreadInfo) {
	path := s.paths[info.ID]
	restoreContextUsage(s.agent(path), info)
	if agent := s.agent(path); !agent.settingsLive {
		settings := rollout.ResumeSettings(info.ID, info.Path, time.Time{})
		if model, ok := settings["model"].(string); ok {
			agent.settings.Model = model
			agent.settings.Effort, _ = settings["model_reasoning_effort"].(string)
			if tier, known := settings["service_tier"]; known {
				agent.settings.Tier, _ = tier.(string)
				agent.settings.TierKnown = true
			}
		}
	}
}

func (s *appServerSession) agent(path string) *activityPaneAgent {
	index := slices.IndexFunc(s.agents, func(agent activityPaneAgent) bool { return agent.Name == path })
	if index < 0 {
		return nil
	}
	return &s.agents[index]
}

// path names a thread by its canonical agent path. A thread seen before its
// thread/started notification keeps a stable placeholder name.
func (s *appServerSession) path(thread string) string {
	if path := s.paths[thread]; path != "" {
		return path
	}
	path := appServerPlaceholder(thread)
	s.paths[thread] = path
	s.agents = append(s.agents, activityPaneAgent{thread: thread, Name: path})
	return path
}

func appServerPlaceholder(thread string) string {
	return "/root/" + thread[:min(8, len(thread))]
}

func (s *appServerSession) next() uint64 {
	s.seq++
	return s.seq
}

// sessionEvent owns live enrichment and effects around shared presentation.
func (u *appServerUI) sessionEvent(method string, p appServerEvent) (bool, error) {
	s := &u.session
	if s.paths == nil {
		return false, nil
	}
	if method == "hook/started" || method == "hook/completed" {
		return true, u.hookEvent(method, p)
	}
	if method == "item/started" {
		u.observeShellItem(p.ThreadID, p.Item)
	}
	if command := p.command; command != nil {
		key := command.Key
		if method == "item/completed" {
			u.execTrack.Completed(key)
		}
		switch {
		case method == "item/started":
			if script, ok := binding.CommandScript(command.Command); ok {
				u.execTrack.Start(key, script)
			}
		case method == "item/completed" && (u.session.commands[key] == nil || !u.execTrack.Tracking(key)):
			u.execTrack.Forget(key)
		}
	}

	if h := u.childHistory(p.ThreadID); h != nil {
		h.state.ObserveLive()
	}
	if p.ThreadID != "" && p.ThreadID != u.Thread {
		if err := u.requestThreadMetadata(p.ThreadID); err != nil {
			return true, err
		}
	}

	key := [3]string{p.ThreadID, p.TurnID, cmp.Or(p.ItemID, p.Item.ID)}
	event := p.observation(method, u.now())
	facts := sessionEventFacts{tracked: u.execTrack.Tracking(key), wait: u.readWaitEvidence(event.Item, key)}
	if p.Item.Type == "commandExecution" && u.proxy != nil {
		if ms, known := u.proxy.nativeTrace.CommandTimeout(p.ThreadID, key[2]); known {
			facts.commandTimeout = strconv.FormatUint(ms/1000, 10)
			if fraction := ms % 1000; fraction != 0 {
				facts.commandTimeout += "." + strings.TrimRight(fmt.Sprintf("%03d", fraction), "0")
			}
			facts.commandTimeout += "s"
		}
	}
	if method == "turn/completed" {
		u.endTurnPreviews(p.ThreadID, p.Turn.ID)
	}
	result := u.projectSessionEvent(event, facts)
	if result.started && u.proxy != nil {
		u.proxy.review.Preview().SetTurn(p.ThreadID, p.Turn.ID, true)
	}
	if method == "thread/started" && p.Thread.ID != "" && p.Thread.ID != u.Thread {
		s.restoreThreadSettings(p.Thread)
	}
	if method == "thread/tokenUsage/updated" || method == "turn/completed" {
		if method == "thread/tokenUsage/updated" && p.ThreadID == u.Thread {
			u.exitUsage = p.TokenUsage.Total
		}
		u.observeCost(p.ThreadID, s.agent(s.path(p.ThreadID)))
	}
	if p.Item.Type == "subAgentActivity" && p.Item.AgentThreadID != "" && p.Item.AgentThreadID != u.Thread {
		if err := u.requestThreadMetadata(p.Item.AgentThreadID); err != nil {
			return true, err
		}
	}
	u.retainWaitProgress(key, result.wait)
	if u.proxy != nil {
		for _, thread := range result.spawned {
			u.proxy.usage.MarkNew(thread)
		}
	}
	if result.publish {
		u.applyActivity(result.entries, slices.Clone(s.agents))
	}
	return result.handled, nil
}

type sessionEventFacts struct {
	commandTimeout string
	tracked        bool
	wait           waitEvidence
}

type sessionProjection struct {
	entries                   []activityPaneEntry
	wait                      *activitypresentation.WaitProgress
	spawned                   []string
	handled, publish, started bool
}

// projectSessionEvent applies admitted observations through the existing model
// owners. It performs no host requests, storage operations or execution tracking;
// the source adapter supplies enrichment and publishes the returned entries.
func (u *appServerUI) projectSessionEvent(p observation.Event, facts sessionEventFacts) sessionProjection {
	s := &u.session
	method := string(p.Kind)
	text := binding.RetainedTranscript(p)
	u.observeProgress(p)
	main := p.ThreadID == u.Thread
	result := sessionProjection{publish: true}
	var entries []activityPaneEntry
	now := p.At
	switch method {
	case "thread/started":
		info := p.Thread
		if info.ID == "" || info.ID == u.Thread {
			return sessionProjection{handled: true}
		}
		old := s.path(info.ID)
		s.registerThreadFacts(info)
		u.renameThreadActivity(old, s.paths[info.ID])

	case "thread/status/changed":
		agent := s.agent(s.path(p.ThreadID))
		switch p.ThreadStatus {
		case "active":
			agent.ObserveActive(true, now)
		case "idle", "notLoaded", "systemError":
			agent.ObserveActive(false, now)
		}
	case "thread/tokenUsage/updated":
		agent := s.agent(s.path(p.ThreadID))
		agent.LastResponse = now // Usage arrives once per provider response.
		agent.ContextWindow, agent.ContextKnown, agent.ContextTokens = p.Context.Window, p.Context.Known, p.Context.Tokens
	case "turn/started", "turn/completed":
		agent := s.agent(s.path(p.ThreadID))
		active := u.Turn
		if !main {
			active = agent.turn
		}
		updates := s.ObserveTurn(sessionpresentation.TurnModels{
			Transcript: s.transcript, Waits: &s.waits, Agent: &agent.AgentLifecycle, Selected: active,
		}, sessionpresentation.TurnObservation{
			Kind: observation.Kind(method), Thread: p.ThreadID, Turn: p.TurnID,
			Status: p.TurnStatus, At: now,
		})
		if method == "turn/started" {
			if !updates.Admitted {
				return sessionProjection{handled: true}
			}
			result.started = true
			if !main {
				agent.turn = p.TurnID
			}
		} else {
			for _, progress := range updates.Waits {
				value := progress.Value
				entries = append(entries, activityPaneEntry{
					Seq: s.next(), Agent: s.path(p.ThreadID), Kind: "progress",
					Text: (activityui.Block{Value: value}).ProgressText(), Observed: now,
					native: &liveActivityNativeItem{thread: p.ThreadID, turn: p.TurnID, item: progress.ID, phase: method, wait: &value, live: value.Live},
				})
			}
			entries = append(entries, u.transcriptUpdates(updates.Text, true)...)
			if updates.Current {
				agent.turn = ""
			}
		}

	case "item/commandExecution/outputDelta":
		// Bounded as it arrives; the next frame shows it, so bursts cost one render.
		if run := s.commands[[3]string{p.ThreadID, p.TurnID, p.ItemID}]; run != nil && run.done == nil {
			run.output.Write(p.Delta)
			run.entry.native.invocation.Write(p.Delta)
			run.dirty = true
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded":
		entries = append(entries, u.transcriptUpdates(s.observeTranscript(text, main), true)...)

	case "item/started", "item/completed", "item/agentMessage/delta":
		item := p.Item
		id := cmp.Or(p.ItemID, item.ID)
		native := &liveActivityNativeItem{thread: p.ThreadID, turn: p.TurnID, item: id, phase: method, live: true, command: item.Command, status: item.Status, duration: appServerDuration(item), workdir: appServerCommandWorkdir(item, s.cwd)}
		agent := s.path(p.ThreadID)
		if method != "item/completed" && item.Type != "reasoning" && item.Type != "userMessage" && s.Transcript(p.ThreadID, p.TurnID).Turn != nil {
			if dropped := u.transcriptUpdates(s.transcript.OtherItemStarted(p.ThreadID, s.Thread(p.ThreadID)), false); len(dropped) != 0 {
				entries = append(entries, dropped...)
			}
		}

		if text, wait, handled, progress := s.itemProgress(item, p.ThreadID, p.TurnID, id, method, true, facts.wait); handled {
			result.wait = progress
			if text != "" {
				native.wait = wait
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "progress", Text: text, Observed: now, native: native})
			}
			break
		}
		switch item.Type {
		case "subAgentActivity":
			if item.AgentThreadID != "" && item.AgentThreadID != u.Thread {
				old := s.path(item.AgentThreadID)
				if item.AgentPath != "" {
					s.agent(old).Name = item.AgentPath
					s.paths[item.AgentThreadID] = item.AgentPath
					u.renameThreadActivity(old, item.AgentPath)
				}
			}
		case "reasoning":
			entries = append(entries, u.transcriptUpdates(s.observeTranscript(text, main), true)...)

		case "commandExecution", "webSearch":
			native.commandTimeout = facts.commandTimeout
			native.commandCwd = appServerCommandDirectory(item, s.cwd)
			native.searchResults = appServerSearchResults(item)
			entry := activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerToolText(item, s.cwd), CallID: id, Observed: now, native: native}
			key := [3]string{p.ThreadID, p.TurnID, id}
			if method != "item/completed" {
				if item.Type == "commandExecution" {
					native.running, native.commandStarted = true, now
					if run := s.commands[key]; run == nil {
						native.invocation = activitypresentation.NewInvocation(appServerCommandSource(item.Command, native.commandCwd), now, s.activity.Outputs(s.Scope()))
						native.output = native.invocation.Output()
						s.commands[key] = &appServerCommandRun{entry: entry, instant: instantOperations(entry.Text)}
					} else {
						native.invocation = run.entry.native.invocation
						native.output = native.invocation.Output()
						native.commandStarted = native.invocation.Snapshot().Started
						native.commandTimeout = cmp.Or(native.commandTimeout, run.entry.native.commandTimeout)
					}
				}
				entries = append(entries, entry)
				break
			}
			if item.Type == "commandExecution" {
				native.commandEnded = now
				if run := s.commands[key]; run != nil {
					native.commandStarted = run.entry.native.commandStarted
					native.commandTimeout = cmp.Or(native.commandTimeout, run.entry.native.commandTimeout)
				}
			}
			s.completeCommand(native, item)
			if run := s.commands[key]; run != nil && facts.tracked {
				run.completion, run.completed, run.completedAt = &entry, item, now
				break
			}
			done := s.commandDone(entry, item, now)
			// A burst that arrived just before completion, as from a command
			// that prints only when it exits, rolls through before the final
			// tail replaces it.
			if run := s.commands[key]; run != nil && !run.instant && run.output.Pending() > 0 {
				run.done = done
				break
			}
			delete(s.commands, key)
			entries = append(entries, done...)
		case "imageView":
			entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: "View " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(s.cwd, item.Path)), CallID: id, Observed: now, native: native})
		case "fileChange":
			if len(item.Changes) > 0 {
				native.editPages = appServerEditPages(item, s.cwd, method)
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerEditText(item, s.cwd), CallID: id, Observed: now, native: native})
			}
		case "collabAgentToolCall":
			if method == "item/completed" {
				deliveries := s.collab(item, id, now, activitypresentation.ObservedCollaboration)
				entries = append(entries, deliveries...)
				for _, entry := range deliveries {
					if entry.start != nil {
						result.spawned = append(result.spawned, s.agent(entry.Agent).thread)
					}
				}
			}
		case "agentMessage":
			if main {
				return sessionProjection{entries: entries, publish: len(entries) != 0}
			}
			entries = append(entries, u.transcriptUpdates(s.observeTranscript(text, false), true)...)

		default:
			if main {
				return sessionProjection{entries: entries, publish: len(entries) != 0} // Main's messages belong to the transcript handler.
			}
			return sessionProjection{entries: entries, publish: len(entries) != 0, handled: true}
		}
	}
	result.entries = entries
	result.handled = !main || !strings.HasPrefix(method, "turn/")
	return result
}

// roll reveals one frame's share of pending output, or all of an instant
// operation's.
func (r *appServerCommandRun) roll() bool {
	if r.instant {
		return r.output.Flush()
	}
	return r.output.Roll()
}

// commandDone is a completed command's entry with its output, and a failure's
// exit with the host's combined output tail.
func (s *appServerSession) commandDone(entry activityPaneEntry, item observation.Item, now time.Time) []activityPaneEntry {
	appServerCommandOutput(&entry, item, now)
	done := []activityPaneEntry{entry}
	if item.ExitCode != nil && *item.ExitCode != 0 {
		exit := activityPaneEntry{Seq: s.next(), Agent: entry.Agent, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: entry.CallID, Observed: now}
		exit.outputTail, exit.outputOmit = appServerOutputTail(item.AggregatedOutput)
		done = append(done, exit)
	}
	return done
}

// flushStreamOutput rolls public summaries and re-sends each live command whose output changed since
// the last frame, with its current tail, rolling a burst through a share at a
// time. A held completion follows once nothing is pending. Late output cannot
// reopen a completed command, whose entry it would otherwise replace.
func (u *appServerUI) flushStreamOutput() {
	u.flushSummaryOutput()
	s := &u.session
	var entries []activityPaneEntry
	for key, run := range s.commands {
		if tracked, handled := u.flushTrackedCommand(key, run); handled {
			entries = append(entries, tracked...)
			continue
		}
		if rolled := run.roll(); !run.dirty && !rolled {
			continue
		}
		run.dirty = false
		entry := run.entry
		native := *entry.native
		native.phase = "item/commandExecution/outputDelta"
		// The thread may have been renamed since the command started.
		entry.native, entry.Agent = &native, s.path(native.thread)
		entry.outputTail, entry.outputOmit = run.output.Lines()
		entries = append(entries, entry)
		if run.done != nil && run.output.Pending() == 0 {
			for _, done := range run.done {
				done.Agent = entry.Agent
				if !done.completed.IsZero() {
					done.completed = u.now() // Output settles once it has shown.
				}
				entries = append(entries, done)
			}
			delete(s.commands, key)
		}
	}
	if len(entries) == 0 {
		return
	}
	slices.SortFunc(entries, func(a, b activityPaneEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	for i := range entries {
		entries[i].Seq = s.next()
	}
	u.applyActivity(entries, nil)
	u.dirty = true
}

// collab binds neutral delivery facts to existing caller labels and feed
// identities. The live adapter applies accounting after this projection.
func (s *appServerSession) collab(item observation.Item, id string, now time.Time, origin activitypresentation.CollaborationOrigin) []activityPaneEntry {
	item.ID = id
	deliveries, confirmed := activitypresentation.ProjectCollaboration(item, origin)
	from := ""
	if confirmed {
		// Preserve sender-first observation order, including participants whose
		// blank input or lifecycle call has no delivered message to display.
		from = s.path(item.SenderThreadID)
		for _, receiver := range item.ReceiverThreadIDs {
			s.path(receiver)
		}
	}
	var entries []activityPaneEntry
	for _, d := range deliveries {
		to := s.path(d.To)
		entry := activityPaneEntry{Seq: s.next(), Agent: to, Kind: d.Kind, Observed: now}
		if d.Kind == "error" {
			entry.Text = d.Text
		} else {
			if d.AssignmentID != "" {
				entry.assignment = &activityAssignment{id: d.AssignmentID, from: from, to: to, text: d.Text}
			}
			switch d.Kind {
			case "start":
				if agent := s.agent(to); agent.settings.Model == "" {
					agent.settings.Model, agent.settings.Effort = d.Model, d.Effort
				}
				entry.start = &activityStart{model: d.Model}
				if d.Model != "" {
					entry.start.effort = d.Effort
				}
			case "assignment":
				entry.Text = d.Text
			case "reply":
				entry.Agent, entry.Text = from, d.Text
				entry.message = &activityMessage{from: from, to: to, text: d.Text}
			}
		}
		entry.native = &liveActivityNativeItem{thread: d.From, turn: "collaboration", item: d.ID, phase: "item/completed"}
		entries = append(entries, entry)
	}
	return entries
}

// observeCost reads all consumption metrics from the same canonical owner.
// App-server context and exit usage are separate, never priced or added here.
func (u *appServerUI) observeCost(thread string, agent *activityPaneAgent) {
	if u.proxy == nil || u.proxy.usage == nil {
		return
	}
	report, observed := u.proxy.usage.Snapshot(thread)
	agent.Roundtrips = report.Roundtrips
	agent.OutputThroughput = report.RoundOutput.Throughput
	agent.InputTokens, agent.OutputTokens = report.InputTokens, report.OutputTokens
	agent.TokensKnown = observed && agent.Roundtrips > report.MissingUsage
	agent.Cost = report.Cost.CachedInput + report.Cost.UncachedInput + report.Cost.Output
	agent.CostKnown = agent.TokensKnown && report.Cost.Known
	agent.CostPartial = report.MissingUsage != 0 || report.PriorUnknown
	agent.UsagePartial = agent.CostPartial
	agent.RoundtripsPartial = report.PriorUnknown
}

func (u *appServerUI) restoreUsage(info binding.ThreadInfo) {
	if u.proxy != nil && u.proxy.usage != nil {
		u.proxy.usage.Restore(info.ID, len(info.Turns) > 0)
	}
}

// appServerCommandWorkdir is the display form of the directory the host ran
// a command in, when it is not the workspace its activity is shown against.
// An unknown workspace or directory gets no label.
func appServerCommandWorkdir(item observation.Item, workspace string) string {
	if item.Type != "commandExecution" || !filepath.IsAbs(item.Cwd) || !filepath.IsAbs(workspace) || filepath.Clean(item.Cwd) == filepath.Clean(workspace) {
		return ""
	}
	return pathdisplay.ForWorkspace(workspace, filepath.Clean(item.Cwd))
}

func appServerCommandDirectory(item observation.Item, workspace string) string {
	if filepath.IsAbs(item.Cwd) {
		return item.Cwd
	}
	return workspace
}

// appServerCommandText uses Codex's typed classification, then the shared
// display classifier for frontends that Codex does not recognize. Neither
// classification changes the executed command or retained host item. Paths
// display relative to the directory the command ran in, which
// appServerCommandWorkdir labels when it differs from the workspace.
func appServerCommandText(item observation.Item, cwd string) string {
	cwd = appServerCommandDirectory(item, cwd)
	var parts []string
	for _, action := range item.CommandActions {
		switch action.Type {
		case "read":
			parts = append(parts, "Read "+markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(cwd, action.Path)))
		case "listFiles":
			parts = append(parts, "List "+markdownsyntax.QuoteCode(cmp.Or(pathdisplay.ForWorkspace(cwd, action.Path), ".")))
		case "search":
			text := "Search " + markdownsyntax.QuoteCode(action.Query)
			if action.Path != "" {
				text += " in " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(cwd, action.Path))
			}
			parts = append(parts, text)
		default:
			parts = nil
		}
		if parts == nil {
			break
		}
	}
	if len(parts) == 0 {
		return activitypresentation.ShellSource(toolActivityShellSource(appServerDisplayCommand(item.Command)), cwd)
	}
	return strings.Join(parts, "\n\n")
}

// completeCommand adapts a host result into the shared invocation owner before
// terminal or report settlement. History has no revived start/end clock.
func (s *appServerSession) completeCommand(native *liveActivityNativeItem, item observation.Item) {
	if item.Type != "commandExecution" {
		return
	}
	native.exit = item.ExitCode // Displayed outcome, separate from queued visual completion.
	if run := s.commands[[3]string{native.thread, native.turn, native.item}]; run != nil {
		native.invocation = run.entry.native.invocation
	}
	source := appServerCommandSource(item.Command, native.commandCwd)
	if native.invocation == nil {
		native.invocation = activitypresentation.NewInvocation(source, native.commandStarted, s.activity.Outputs(s.Scope()))
	}
	native.invocation.Complete(activitypresentation.CommandCompletion{
		Source: source, At: native.commandEnded, Duration: appServerDuration(item), Aggregate: item.AggregatedOutput, Exit: item.ExitCode,
	})
	native.output = native.invocation.Output()
}

// appServerCommandOutput keeps aggregate output even when the host reports no
// exit. Known failures carry their tail on the separate exit entry. Only confirmed
// success opens the settlement window or starts restored output collapsed.
func appServerCommandOutput(entry *activityPaneEntry, item observation.Item, settled time.Time) {
	if item.Type != "commandExecution" || item.ExitCode != nil && *item.ExitCode != 0 {
		return
	}
	entry.outputTail, entry.outputOmit = appServerOutputTail(item.AggregatedOutput)
	if item.ExitCode == nil {
		return
	}
	entry.completed = settled
	entry.native.changes, entry.native.commit = commandOutputRows(item)
}

// appServerOutputTail keeps the last non-blank lines of a command's host
// output for display. It never reads beyond the host's aggregate, and counts
// the lines it leaves out.
func appServerOutputTail(output *string) ([]string, int) {
	if output == nil {
		return nil, 0
	}
	return activityui.TailOutput(*output)
}

// Hide only the host's literal shell command wrapper, never evaluate its words
// or discard surrounding operations. Execution and retained items stay intact.
// Source: codex-rs/shell-command/src/bash.rs:106:121@86be5320 extract_bash_command
// Source: codex-rs/shell-command/src/powershell.rs:43:73@86be5320 extract_powershell_command
// Unlike Codex's argv helper, preserve PowerShell's extra script arguments.
func appServerDisplayCommand(command string) string {
	args, ok := binding.CommandArgs(command)
	if !ok {
		return command
	}
	name := filepath.Base(args[0])
	name = strings.TrimSuffix(name, filepath.Ext(name))
	switch name {
	case "bash", "sh":
		if len(args) == 3 && (args[1] == "-lc" || args[1] == "-c") {
			return args[2]
		}
	case "pwsh", "powershell":
		for i := 1; i+1 < len(args); i++ {
			switch strings.ToLower(args[i]) {
			case "-nologo", "-noprofile":
			case "-command", "-c":
				if i+2 == len(args) {
					return args[i+1]
				}
				return command
			default:
				return command
			}
		}
	}
	return command
}

// appServerEditText is one Activity operation per changed file, with its
// line counts taken from the patch Codex applied.
func appServerEditText(item observation.Item, cwd string) string {
	var parts []string
	for _, change := range item.Changes {
		added, removed := appServerChangeCounts(change)
		verb := "Edit"
		switch change.Kind.Type {
		case "add":
			verb = "Create"
		case "delete":
			verb = "Delete"
		}
		path := pathdisplay.ForWorkspace(cwd, change.Path)
		if moved := change.Kind.MovePath; moved != "" {
			verb, path = "Move", pathdisplay.Move(cwd, change.Path, moved)
		}
		text := verb + " " + markdownsyntax.QuoteCode(path) + fmt.Sprintf(" · +%d −%d", added, removed)
		switch item.Status {
		case "failed", "declined":
			text += " · " + item.Status
		case "inProgress":
			// Started patches may still await approval; only completion confirms them.
			text += " · pending"
		}
		parts = append(parts, text+" · apply_patch")
	}
	return strings.Join(parts, "\n\n")
}

// appServerEditPages retains host-supplied completed diffs for navigation only.
// It neither captures the filesystem nor creates durable change evidence.
func appServerEditPages(item observation.Item, cwd, phase string) []activitypresentation.Value {
	if phase != "item/completed" || item.Status != "" && item.Status != "completed" {
		return nil
	}
	var pages []activitypresentation.Value
	for _, change := range item.Changes {
		path, diff := pathdisplay.ForWorkspace(cwd, change.Path), change.Diff
		syntaxPath := ""
		if moved := change.Kind.MovePath; moved != "" {
			path = pathdisplay.Move(cwd, change.Path, moved)
			syntaxPath = pathdisplay.ForWorkspace(cwd, moved)
			if body, ok := strings.CutSuffix(diff, "\n\nMoved to: "+moved); ok {
				diff = body + "\n\nMoved to: " + pathdisplay.ForWorkspace(cwd, moved)
			}
			if diff == "" {
				diff = fmt.Sprintf("move %q -> %q\n", pathdisplay.ForWorkspace(cwd, change.Path), pathdisplay.ForWorkspace(cwd, moved))
			}
		}
		if diff == "" {
			continue
		}
		verb, lang := "Edit", "diff"
		switch change.Kind.Type {
		case "add":
			verb, lang = "Create", ""
		case "delete":
			verb, lang = "Delete", ""
		}
		if change.Kind.MovePath != "" {
			verb = "Move"
		}
		pages = append(pages, activitypresentation.Value{Kind: "op", Verb: verb, Path: path, SyntaxPath: syntaxPath, Code: diff, Lang: lang, Fenced: true})
	}
	return pages
}

func appServerChangeCounts(change observation.FileChange) (added, removed int) {
	lines := strings.Split(strings.TrimSuffix(change.Diff, "\n"), "\n")
	if change.Diff == "" {
		return 0, 0
	}
	switch change.Kind.Type {
	case "add":
		return len(lines), 0
	case "delete":
		return 0, len(lines)
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}
