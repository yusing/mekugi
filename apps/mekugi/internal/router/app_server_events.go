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
	output   activityui.CommandOutput
	holding  bool // Canonical completion published; terminal output is still rolling.
	// Completed host facts await independent segment attribution.
	completion  *activityPaneEntry
	completed   observation.Item
	completedAt time.Time
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
	agent := &s.agents[index]
	agent.AgentLifecycle = *s.lifecycle(agent.thread)
	return agent
}

func (s *appServerSession) lifecycle(thread string) *activitypresentation.AgentLifecycle {
	return activitypresentation.Attached[activityPaneEntry, activityPaneAgent](s.Scope()).Lifecycle(s.Thread(thread))
}

// roster publishes detached lifecycle values alongside the existing host and
// usage metadata. Mutating a published value cannot change thread state.
func (s *appServerSession) roster() []activityPaneAgent {
	for i := range s.agents {
		s.agents[i].AgentLifecycle = *s.lifecycle(s.agents[i].thread)
	}
	return slices.Clone(s.agents)
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
func (u *appServerUI) sessionEvent(method string, p binding.Notification) (bool, error) {
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
	if command := p.Command; command != nil {
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
	event := p.Observation(method, u.now())
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
		u.applyActivity(result.entries, s.roster())
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
		s.path(p.ThreadID)
		agent := s.lifecycle(p.ThreadID)
		switch p.ThreadStatus {
		case "active":
			agent.ObserveActive(true, now)
		case "idle", "notLoaded", "systemError":
			agent.ObserveActive(false, now)
		}
	case "thread/tokenUsage/updated":
		agent := s.agent(s.path(p.ThreadID))
		s.lifecycle(p.ThreadID).Response(now)
		agent.ContextWindow, agent.ContextKnown, agent.ContextTokens = p.Context.Window, p.Context.Known, p.Context.Tokens
	case "turn/started", "turn/completed":
		agent := s.agent(s.path(p.ThreadID))
		active := u.Turn
		if !main {
			active = agent.turn
		}
		updates := s.ObserveTurn(sessionpresentation.TurnModels{
			Transcript: s.transcript, Waits: &s.waits, Agent: s.lifecycle(p.ThreadID), Selected: active,
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
		if run := s.commands[[3]string{p.ThreadID, p.TurnID, p.ItemID}]; run != nil && !run.holding && run.completion == nil {
			run.output.Write(p.Delta)
			run.entry.native.invocation.Write(p.Delta)
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
						s.commands[key] = &appServerCommandRun{entry: entry, output: activityui.NewCommandOutput(instantOperations(entry.Text))}
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
				u.holdTrackedCommand(&entry)
				entries = append(entries, s.commandDone(entry, item, now)...)
				break
			}
			done := s.commandDone(entry, item, now)
			// A burst that arrived just before completion, as from a command
			// that prints only when it exits, rolls through before the final
			// tail replaces it.
			if run := s.commands[key]; run != nil && run.output.Pending() {
				run.holding = true
				u.holdCommand(run.entry, activityui.CommandFrame{Output: run.output.Snapshot()})
				entries = append(entries, done...)
				break
			}
			if run := s.commands[key]; run != nil {
				u.releaseCommand(run.entry)
			}
			delete(s.commands, key)
			entries = append(entries, done...)
		case "imageView":
			entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: "View " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(s.cwd, item.Path)), CallID: id, Observed: now, native: native})
		case "fileChange":
			if len(item.Changes) > 0 {
				native.editPages = activitypresentation.HostEditPages(item, s.cwd, method)
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: activitypresentation.HostEditText(item, s.cwd), CallID: id, Observed: now, native: native})
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

// flushStreamOutput advances terminal frames and admits changed segment facts.
// Reveal-only updates never replace neutral command records. Late output cannot
// reopen a completed invocation.
func (u *appServerUI) flushStreamOutput() {
	u.flushSummaryOutput()
	s := &u.session
	var entries []activityPaneEntry
	for key, run := range s.commands {
		tracked, handled := u.flushTrackedCommand(key, run)
		entries = append(entries, tracked...)
		if handled {
			continue
		}
		frame, changed := run.output.Advance()
		if !changed {
			continue
		}
		u.holdCommand(run.entry, activityui.CommandFrame{Output: frame})
		if run.holding && !frame.Pending {
			u.releaseCommand(run.entry)
			delete(s.commands, key)
		}
		u.dirty = true
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
