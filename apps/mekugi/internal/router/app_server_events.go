package router

import (
	"cmp"
	"context"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

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
	agents       []sessionpresentation.ActivityAgent
	transcript   observation.Transcript
	observations *binding.Observation
	metadata     map[string]string                  // Child thread → pending metadata request ID; empty when settled.
	commands     map[[3]string]*appServerCommandRun // Live commands by thread, turn, item.
	activity     *activitymodel.Service
	sessionpresentation.Attachment
	cwd           string
	waits         activitypresentation.Waits
	waitStore     *history.Store
	waitWrites    *persistence.Counter
	waitLimits    history.Limits
	waitThread    string
	waitNamespace string
	waitContext   context.Context

	compactionRollout binding.ThreadInfo // Host-selected Main rollout, without its history.
}

// appServerCommandRun is a started command whose streamed output tail is
// re-sent on the next frame. Completion, not the stream, ends it.
type appServerCommandRun struct {
	segments execTrackPresentation
	entry    sessionpresentation.ActivityEntry
	output   activityui.CommandOutput
	holding  bool // Canonical completion published; terminal output is still rolling.
	// Completed host facts await independent segment attribution.
	completion  *sessionpresentation.ActivityEntry
	completed   observation.Item
	completedAt time.Time
}

func (s *appServerSession) start(thread, cwd string) {
	s.paths = map[string]string{thread: "/root"}
	s.Start(thread)
	s.agents = []sessionpresentation.ActivityAgent{{Thread: thread, Name: "/root", Role: "main"}}
	s.transcript = s.observations.NewTranscript(s.Scope())
	s.metadata = make(map[string]string)
	s.commands = make(map[[3]string]*appServerCommandRun)
	s.waitStore, s.waitContext = nil, nil
	s.waitWrites, s.waitLimits, s.waitThread, s.waitNamespace = nil, history.Limits{}, "", ""
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
		s.agents = append(s.agents, sessionpresentation.ActivityAgent{Thread: info.ID, Name: path})
	}
	s.paths[info.ID] = path
	s.agent(path).Thread = info.ID
	s.agent(path).Role = cmp.Or(info.Role, s.agent(path).Role)
}

func (s *appServerSession) restoreThreadSettings(info binding.ThreadInfo) {
	path := s.paths[info.ID]
	restoreContextUsage(s.agent(path), info)
	if agent := s.agent(path); !agent.SettingsLive {
		settings := rollout.ResumeSettings(info.ID, info.Path, time.Time{})
		if model, ok := settings["model"].(string); ok {
			agent.Settings.Model = model
			agent.Settings.Effort, _ = settings["model_reasoning_effort"].(string)
			if tier, known := settings["service_tier"]; known {
				agent.Settings.Tier, _ = tier.(string)
				agent.Settings.TierKnown = true
			}
		}
	}
}

func (s *appServerSession) agent(path string) *sessionpresentation.ActivityAgent {
	index := slices.IndexFunc(s.agents, func(agent sessionpresentation.ActivityAgent) bool { return agent.Name == path })
	if index < 0 {
		return nil
	}
	agent := &s.agents[index]
	agent.AgentLifecycle = *s.lifecycle(agent.Thread)
	return agent
}

func (s *appServerSession) lifecycle(thread string) *activitypresentation.AgentLifecycle {
	return activitypresentation.Attached[sessionpresentation.ActivityEntry, sessionpresentation.ActivityAgent](s.Scope()).Lifecycle(s.Thread(thread))
}

// roster publishes detached lifecycle values alongside the existing host and
// usage metadata. Mutating a published value cannot change thread state.
func (s *appServerSession) roster() []sessionpresentation.ActivityAgent {
	for i := range s.agents {
		s.agents[i].AgentLifecycle = *s.lifecycle(s.agents[i].Thread)
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
	s.agents = append(s.agents, sessionpresentation.ActivityAgent{Thread: thread, Name: path})
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
			if script, ok := shellinstrumentation.CommandScript(command.Command); ok {
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
	if p.Item.Type == "commandExecution" && u.nativeTrace != nil {
		if ms, known := u.nativeTrace.CommandTimeout(p.ThreadID, key[2]); known {
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
	if result.started && u.review != nil {
		u.review.Preview().SetTurn(p.ThreadID, p.Turn.ID, true)
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
	if u.usage != nil {
		for _, thread := range result.spawned {
			u.usage.MarkNew(thread)
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
	entries                   []sessionpresentation.ActivityEntry
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
	var entries []sessionpresentation.ActivityEntry
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
			active = agent.Turn
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
				agent.Turn = p.TurnID
			}
		} else {
			for _, progress := range updates.Waits {
				value := progress.Value
				entries = append(entries, sessionpresentation.ActivityEntry{
					Seq: s.next(), Agent: s.path(p.ThreadID), Kind: "progress",
					Text: (activityui.Block{Value: value}).ProgressText(), Observed: now,
					Native: &sessionpresentation.NativeItem{Thread: p.ThreadID, Turn: p.TurnID, Item: progress.ID, Phase: method, Wait: &value, Live: value.Live},
				})
			}
			entries = append(entries, u.transcriptUpdates(updates.Text, true)...)
			if updates.Current {
				agent.Turn = ""
			}
		}

	case "item/commandExecution/outputDelta":
		// Bounded as it arrives; the next frame shows it, so bursts cost one render.
		if run := s.commands[[3]string{p.ThreadID, p.TurnID, p.ItemID}]; run != nil && !run.holding && run.completion == nil {
			run.output.Write(p.Delta)
			run.entry.Native.Invocation.Write(p.Delta)
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded":
		entries = append(entries, u.transcriptUpdates(s.observeTranscript(text, main), true)...)

	case "item/started", "item/completed", "item/agentMessage/delta":
		item := p.Item
		id := cmp.Or(p.ItemID, item.ID)
		native := &sessionpresentation.NativeItem{Thread: p.ThreadID, Turn: p.TurnID, Item: id, Phase: method, Live: true, Command: item.Command, Status: item.Status, Duration: appServerDuration(item), Workdir: appServerCommandWorkdir(item, s.cwd)}
		agent := s.path(p.ThreadID)
		if method != "item/completed" && item.Type != "reasoning" && item.Type != "userMessage" && s.Transcript(p.ThreadID, p.TurnID).Turn != nil {
			if dropped := u.transcriptUpdates(s.transcript.OtherItemStarted(p.ThreadID, s.Thread(p.ThreadID)), false); len(dropped) != 0 {
				entries = append(entries, dropped...)
			}
		}

		if text, wait, handled, progress := s.itemProgress(item, p.ThreadID, p.TurnID, id, method, true, facts.wait); handled {
			result.wait = progress
			if text != "" {
				native.Wait = wait
				entries = append(entries, sessionpresentation.ActivityEntry{Seq: s.next(), Agent: agent, Kind: "progress", Text: text, Observed: now, Native: native})
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
			native.CommandTimeout = facts.commandTimeout
			native.CommandCwd = appServerCommandDirectory(item, s.cwd)
			native.SearchResults = appServerSearchResults(item)
			entry := sessionpresentation.ActivityEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerToolText(item, s.cwd), CallID: id, Observed: now, Native: native}
			key := [3]string{p.ThreadID, p.TurnID, id}
			if method != "item/completed" {
				if item.Type == "commandExecution" {
					native.Running, native.CommandStarted = true, now
					if run := s.commands[key]; run == nil {
						native.Invocation = activitypresentation.NewInvocation(sessionpresentation.CommandSource(item.Command, native.CommandCwd), now, s.activity.Outputs(s.Scope()))
						native.Output = native.Invocation.Output()
						s.commands[key] = &appServerCommandRun{entry: entry, output: activityui.NewCommandOutput(instantOperations(entry.Text))}
					} else {
						native.Invocation = run.entry.Native.Invocation
						native.Output = native.Invocation.Output()
						native.CommandStarted = native.Invocation.Snapshot().Started
						native.CommandTimeout = cmp.Or(native.CommandTimeout, run.entry.Native.CommandTimeout)
					}
				}
				entries = append(entries, entry)
				break
			}
			if item.Type == "commandExecution" {
				native.CommandEnded = now
				if run := s.commands[key]; run != nil {
					native.CommandStarted = run.entry.Native.CommandStarted
					native.CommandTimeout = cmp.Or(native.CommandTimeout, run.entry.Native.CommandTimeout)
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
			entries = append(entries, sessionpresentation.ActivityEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: "View " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(s.cwd, item.Path)), CallID: id, Observed: now, Native: native})
		case "fileChange":
			if len(item.Changes) > 0 {
				native.EditPages = activitypresentation.HostEditPages(item, s.cwd, method)
				entries = append(entries, sessionpresentation.ActivityEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: activitypresentation.HostEditText(item, s.cwd), CallID: id, Observed: now, Native: native})
			}
		case "collabAgentToolCall":
			if method == "item/completed" {
				deliveries := s.collab(item, id, now, activitypresentation.ObservedCollaboration)
				entries = append(entries, deliveries...)
				for _, entry := range deliveries {
					if entry.Start != nil {
						result.spawned = append(result.spawned, s.agent(entry.Agent).Thread)
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
func (s *appServerSession) commandDone(entry sessionpresentation.ActivityEntry, item observation.Item, now time.Time) []sessionpresentation.ActivityEntry {
	appServerCommandOutput(&entry, item, now)
	done := []sessionpresentation.ActivityEntry{entry}
	if item.ExitCode != nil && *item.ExitCode != 0 {
		exit := sessionpresentation.ActivityEntry{Seq: s.next(), Agent: entry.Agent, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: entry.CallID, Observed: now}
		exit.OutputTail, exit.OutputOmit = appServerOutputTail(item.AggregatedOutput)
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
	var entries []sessionpresentation.ActivityEntry
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
	slices.SortFunc(entries, func(a, b sessionpresentation.ActivityEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	for i := range entries {
		entries[i].Seq = s.next()
	}
	u.applyActivity(entries, nil)
	u.dirty = true
}

// collab binds neutral delivery facts to existing caller labels and feed
// identities. The live adapter applies accounting after this projection.
func (s *appServerSession) collab(item observation.Item, id string, now time.Time, origin activitypresentation.CollaborationOrigin) []sessionpresentation.ActivityEntry {
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
	var entries []sessionpresentation.ActivityEntry
	for _, d := range deliveries {
		to := s.path(d.To)
		entry := sessionpresentation.ActivityEntry{Seq: s.next(), Agent: to, Kind: d.Kind, Observed: now}
		if d.Kind == "error" {
			entry.Text = d.Text
		} else {
			if d.AssignmentID != "" {
				entry.Assignment = &sessionpresentation.ActivityAssignment{ID: d.AssignmentID, From: from, To: to, Text: d.Text}
			}
			switch d.Kind {
			case "start":
				if agent := s.agent(to); agent.Settings.Model == "" {
					agent.Settings.Model, agent.Settings.Effort = d.Model, d.Effort
				}
				entry.Start = &sessionpresentation.ActivityStart{Model: d.Model}
				if d.Model != "" {
					entry.Start.Effort = d.Effort
				}
			case "assignment":
				entry.Text = d.Text
			case "reply":
				entry.Agent, entry.Text = from, d.Text
				entry.Message = &sessionpresentation.ActivityMessage{From: from, To: to, Text: d.Text}
			}
		}
		entry.Native = &sessionpresentation.NativeItem{Thread: d.From, Turn: "collaboration", Item: d.ID, Phase: "item/completed"}
		entries = append(entries, entry)
	}
	return entries
}

// observeCost reads all consumption metrics from the same canonical owner.
// App-server context and exit usage are separate, never priced or added here.
func (u *appServerUI) observeCost(thread string, agent *sessionpresentation.ActivityAgent) {
	if u.usage == nil {
		return
	}
	report, observed := u.usage.Snapshot(thread)
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
	if u.usage != nil {
		u.usage.Restore(info.ID, len(info.Turns) > 0)
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
		return activitypresentation.ShellSource(frontends.ShellDisplaySource(sessionpresentation.DisplayCommand(item.Command)), cwd)
	}
	return strings.Join(parts, "\n\n")
}

// completeCommand adapts a host result into the shared invocation owner before
// terminal or report settlement. History has no revived start/end clock.
func (s *appServerSession) completeCommand(native *sessionpresentation.NativeItem, item observation.Item) {
	if item.Type != "commandExecution" {
		return
	}
	native.Exit = item.ExitCode // Displayed outcome, separate from queued visual completion.
	if run := s.commands[[3]string{native.Thread, native.Turn, native.Item}]; run != nil {
		native.Invocation = run.entry.Native.Invocation
	}
	source := sessionpresentation.CommandSource(item.Command, native.CommandCwd)
	if native.Invocation == nil {
		native.Invocation = activitypresentation.NewInvocation(source, native.CommandStarted, s.activity.Outputs(s.Scope()))
	}
	native.Invocation.Complete(activitypresentation.CommandCompletion{
		Source: source, At: native.CommandEnded, Duration: appServerDuration(item), Aggregate: item.AggregatedOutput, Exit: item.ExitCode,
	})
	native.Output = native.Invocation.Output()
}

// appServerCommandOutput keeps aggregate output even when the host reports no
// exit. Known failures carry their tail on the separate exit entry. Only confirmed
// success opens the settlement window or starts restored output collapsed.
func appServerCommandOutput(entry *sessionpresentation.ActivityEntry, item observation.Item, settled time.Time) {
	if item.Type != "commandExecution" || item.ExitCode != nil && *item.ExitCode != 0 {
		return
	}
	entry.OutputTail, entry.OutputOmit = appServerOutputTail(item.AggregatedOutput)
	if item.ExitCode == nil {
		return
	}
	entry.Completed = settled
	entry.Native.Changes, entry.Native.Commit = commandOutputRows(item)
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
