package router

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/modules/activity/waittargets"

	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/transcript"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/libraries/pathdisplay"
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
	state        scope.Store
	threadStates map[string]*scope.Store
	cwd          string
	waits        map[[3]string][]waittargets.Target // Start-time targets by thread, turn, item.
	waitStore    *mekugiReplayStore
	waitContext  context.Context

	compactionRollout appServerThreadInfo // Host-selected Main rollout, without its history.
}

// appServerCommandRun is a started command whose streamed output tail is
// re-sent on the next frame. Completion, not the stream, ends it.
type appServerCommandRun struct {
	entry  activityPaneEntry
	output activityui.OutputTail
	dirty  bool
	// Instant operations, such as reads, show their output at once; other
	// commands roll a burst through.
	instant bool
	done    []activityPaneEntry // Completion, held until streamed output has rolled through.
	// A tracked command's completion waits for its segment report to end.
	completion  *activityPaneEntry
	completed   appServerItem
	completedAt time.Time
}

type appServerFileChange struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
	Kind struct {
		Type      string `json:"type"`
		MovePath  string `json:"move_path"`
		MovePath2 string `json:"movePath"`
	} `json:"kind"`
}

type appServerCommandAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Path    string `json:"path"`
	Query   string `json:"query"`
}

type appServerThreadInfo struct {
	Name           string                 `json:"name"`
	ID             string                 `json:"id"`
	ParentThreadID string                 `json:"parentThreadId"`
	AgentNickname  string                 `json:"agentNickname"`
	AgentRole      string                 `json:"agentRole"`
	Cwd            string                 `json:"cwd"`
	Path           string                 `json:"path"`
	Source         jsontext.Value         `json:"source"`
	CreatedAt      int64                  `json:"createdAt"`
	UpdatedAt      int64                  `json:"updatedAt"`
	HistoryMode    string                 `json:"historyMode"`
	Turns          []appServerHistoryTurn `json:"turns"`
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
	text   observation.TextEvent `json:"-"`
	Status struct {
		Type string `json:"type"`
	} `json:"status"`
	ThreadID   string              `json:"threadId"`
	TurnID     string              `json:"turnId"`
	ItemID     string              `json:"itemId"`
	Delta      string              `json:"delta"`
	ProcessID  string              `json:"processId"`
	Stdin      string              `json:"stdin"`
	Item       appServerItem       `json:"item"`
	Run        *appserver.HookRun  `json:"run,omitempty"`
	Thread     appServerThreadInfo `json:"thread"`
	TokenUsage struct {
		Total              appServerTokenUsage  `json:"total"`
		Last               *appServerTokenUsage `json:"last"`
		ModelContextWindow uint64               `json:"modelContextWindow"`
	} `json:"tokenUsage"`
	Turn appServerTurn `json:"turn"`
}

func (s *appServerSession) start(thread, cwd string) {
	s.paths = map[string]string{thread: "/root"}
	s.threadStates = nil
	s.agents = []activityPaneAgent{{thread: thread, Name: "/root", Role: "main"}}
	s.transcript = new(transcript.State)
	if s.observations != nil {
		s.transcript = s.observations.NewTranscript(&s.state)
	}
	s.metadata = make(map[string]string)
	s.commands = make(map[[3]string]*appServerCommandRun)
	s.waits = make(map[[3]string][]waittargets.Target)
	s.waitStore, s.waitContext = nil, nil
	s.cwd = cwd
}

func (s *appServerSession) registerThread(info appServerThreadInfo) {
	var source struct {
		SubAgent struct {
			ThreadSpawn struct {
				AgentPath string `json:"agent_path"`
				AgentRole string `json:"agent_role"`
			} `json:"thread_spawn"`
		} `json:"subAgent"`
	}
	_ = json.Unmarshal(info.Source, &source)
	spawn := source.SubAgent.ThreadSpawn
	path := spawn.AgentPath
	old := s.paths[info.ID]
	if path == "" && old != appServerPlaceholder(info.ID) {
		path = old
	}
	if path == "" && info.AgentNickname != "" {
		path = "/root/" + info.AgentNickname
	}
	path = cmp.Or(path, appServerPlaceholder(info.ID))
	if old := s.paths[info.ID]; old != "" {
		s.agent(old).Name = path
	} else {
		s.agents = append(s.agents, activityPaneAgent{thread: info.ID, Name: path})
	}
	s.paths[info.ID] = path
	s.agent(path).thread = info.ID
	s.agent(path).Role = cmp.Or(info.AgentRole, spawn.AgentRole, s.agent(path).Role)
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

// sessionEvent handles what the session adapter owns. Main-thread turn
// lifecycle and Main's own messages continue to the transcript handler.
func (u *appServerUI) sessionEvent(method string, p appServerEvent) (bool, error) {
	s := &u.session
	if s.paths == nil {
		return false, nil
	}
	if method == "hook/started" || method == "hook/completed" {
		return true, u.hookEvent(method, p)
	}
	u.observeProgress(method, p)
	if method == "item/started" {
		u.observeShellItem(p.ThreadID, p.Item)
	}
	if p.Item.Type == "commandExecution" {
		// Every command is offered for matching, including ones Activity hides,
		// so their shells never wait for an item that will not arrive.
		key := [3]string{p.ThreadID, p.TurnID, cmp.Or(p.ItemID, p.Item.ID)}
		if method == "item/completed" {
			u.execTrack.completed(key)
		}
		switch {
		case method == "item/started":
			u.execTrack.start(key, p.Item.Command)
		case method == "item/completed" && (u.session.commands[key] == nil || !u.execTrack.tracking(key)):
			// Only a command Activity presents holds its report until it ends.
			u.execTrack.finish(key)
		}
	}
	main := p.ThreadID == u.Thread
	if h := u.childHistory(p.ThreadID); h != nil {
		h.state.ObserveLive()
	}
	if p.ThreadID != "" && !main {
		if err := u.requestThreadMetadata(p.ThreadID); err != nil {
			return true, err
		}
	}
	var entries []activityPaneEntry
	now := u.now()
	switch method {
	case "thread/started":
		info := p.Thread
		if info.ID == "" || info.ID == u.Thread {
			return true, nil
		}
		old := s.path(info.ID)
		s.registerThread(info)
		u.renameThreadActivity(old, s.paths[info.ID])

	case "thread/status/changed":
		agent := s.agent(s.path(p.ThreadID))
		switch p.Status.Type {
		case "active":
			// Status alone does not establish the first turn's start.
			if !agent.Started.IsZero() {
				agent.WorkTimer.update(true, now)
				agent.Responding, agent.Final = true, false
			}
		case "idle", "notLoaded", "systemError":
			agent.WorkTimer.update(false, now)
			agent.Responding = false
		}
	case "thread/tokenUsage/updated":
		if main {
			u.exitUsage = p.TokenUsage.Total
		}
		agent := s.agent(s.path(p.ThreadID))
		agent.LastResponse = now // Usage arrives once per provider response.
		agent.ContextWindow = p.TokenUsage.ModelContextWindow
		agent.ContextKnown = p.TokenUsage.Last != nil
		agent.ContextTokens = 0
		if p.TokenUsage.Last != nil {
			agent.ContextTokens = p.TokenUsage.Last.TotalTokens
		}
		u.observeCost(p.ThreadID, agent)
	case "turn/started":
		if u.proxy != nil {
			u.proxy.review.Preview().SetTurn(p.ThreadID, p.Turn.ID, true)
		}
		agent := s.agent(s.path(p.ThreadID))
		agent.WorkTimer.update(true, now)
		agent.Responding, agent.Final = true, false
		if !main {
			agent.turn = p.Turn.ID
		}
		if agent.Started.IsZero() {
			agent.Started = now
		}
		agent.Turns++
		s.transcript.TurnStarted(p.ThreadID)
	case "turn/completed":
		u.endTurnPreviews(p.ThreadID, p.Turn.ID)
		entries = append(entries, u.endTurnWaits(p.ThreadID, p.Turn.ID, now)...)
		for key := range s.waits {
			if key[0] == p.ThreadID && key[1] == p.Turn.ID {
				delete(s.waits, key)
			}
		}
		agent := s.agent(s.path(p.ThreadID))
		active := u.Turn
		if !main {
			active = agent.turn
		}
		current := active == "" || active == p.Turn.ID
		entries = append(entries, u.transcriptUpdates(s.transcript.TurnCompleted(p.ThreadID, p.Turn.ID, p.Turn.Status, now, current), true)...)
		if current {
			agent.WorkTimer.update(false, now)
			agent.Responding, agent.LastResponse = false, now
			agent.Final, agent.turn = p.Turn.Status == "completed", ""
		}
		u.observeCost(p.ThreadID, agent)

	case "item/commandExecution/outputDelta":
		// Bounded as it arrives; the next frame shows it, so bursts cost one render.
		if run := s.commands[[3]string{p.ThreadID, p.TurnID, p.ItemID}]; run != nil && run.done == nil {
			run.output.Write(p.Delta)
			if output := run.entry.native.output; output != nil {
				output.Write(p.Delta)
			}
			run.dirty = true
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded":
		entries = append(entries, u.transcriptUpdates(u.session.transcript.Observe(p.text, main), true)...)

	case "item/started", "item/completed", "item/agentMessage/delta":
		item := p.Item
		id := cmp.Or(p.ItemID, item.ID)
		item = u.waitItem(item, p.ThreadID, p.TurnID, id, method == "item/started")
		native := &liveActivityNativeItem{thread: p.ThreadID, turn: p.TurnID, item: id, phase: method, live: true, command: item.Command, status: item.Status, duration: appServerDuration(item), workdir: appServerCommandWorkdir(item, s.cwd)}
		agent := s.path(p.ThreadID)
		if method != "item/completed" && item.Type != "reasoning" && item.Type != "userMessage" {
			if dropped := u.transcriptUpdates(s.transcript.OtherItemStarted(p.ThreadID), false); len(dropped) != 0 {
				u.applyActivity(dropped, nil)
			}
		}

		if text, wait, handled := appServerProgress(item, method); handled {
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
				if err := u.requestThreadMetadata(item.AgentThreadID); err != nil {
					return true, err
				}
			}
		case "reasoning":
			entries = append(entries, u.transcriptUpdates(u.session.transcript.Observe(p.text, main), true)...)

		case "commandExecution", "webSearch":
			if item.Type == "commandExecution" && u.proxy != nil {
				if ms, known := u.proxy.nativeTrace.CommandTimeout(p.ThreadID, id); known {
					native.commandTimeout = strconv.FormatUint(ms/1000, 10)
					if fraction := ms % 1000; fraction != 0 {
						native.commandTimeout += "." + strings.TrimRight(fmt.Sprintf("%03d", fraction), "0")
					}
					native.commandTimeout += "s"
				}
			}
			native.commandCwd = appServerCommandDirectory(item, s.cwd)
			native.searchResults = appServerSearchResults(item)
			entry := activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerToolText(item, s.cwd), CallID: id, Observed: now, native: native}
			key := [3]string{p.ThreadID, p.TurnID, id}
			if method != "item/completed" {
				if item.Type == "commandExecution" {
					native.running, native.commandStarted = true, now
					if run := s.commands[key]; run == nil {
						if outputs := s.activity.Outputs(&s.state); outputs != nil {
							native.output = outputs.New()
						}
						s.commands[key] = &appServerCommandRun{entry: entry, instant: instantOperations(entry.Text)}
					} else {
						native.output = run.entry.native.output
						native.commandStarted = run.entry.native.commandStarted
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
			s.retainOutput(native, item)
			if run := s.commands[key]; run != nil && u.execTrack.tracking(key) {
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
			if method == "item/completed" && (item.Status == "" || item.Status == "completed") {
				if item.Tool == "spawnAgent" && u.proxy != nil {
					for _, child := range item.ReceiverThreadIDs {
						u.proxy.usage.MarkNew(child)
					}
				}
				entries = append(entries, s.collab(item, id, now)...)
			}
		case "agentMessage":
			if main {
				return false, nil
			}
			entries = append(entries, u.transcriptUpdates(u.session.transcript.Observe(p.text, false), true)...)

		default:
			if main {
				return false, nil // Main's messages belong to the transcript handler.
			}
			return true, nil
		}
	}
	u.applyActivity(entries, slices.Clone(s.agents))
	// Main's turn lifecycle also drives the composer state.
	return !main || !strings.HasPrefix(method, "turn/"), nil
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
func (s *appServerSession) commandDone(entry activityPaneEntry, item appServerItem, now time.Time) []activityPaneEntry {
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

// collab turns one completed collaboration call into the assignment, message
// or spawn it performed. Waits, listings and lifecycle calls add nothing.
func (s *appServerSession) collab(item appServerItem, id string, now time.Time) []activityPaneEntry {
	from := s.path(item.SenderThreadID)
	var entries []activityPaneEntry
	for _, receiver := range item.ReceiverThreadIDs {
		first := len(entries)
		to := s.path(receiver)
		switch item.Tool {
		case "spawnAgent":
			if agent := s.agent(to); agent.settings.Model == "" {
				agent.settings.Model, agent.settings.Effort = item.Model, item.ReasoningEffort
			}
			start := &activityStart{model: item.Model}
			if item.Model != "" {
				start.effort = item.ReasoningEffort
			}
			entry := activityPaneEntry{Seq: s.next(), Agent: to, Kind: "start", Observed: now, start: start}
			if strings.TrimSpace(item.Prompt) != "" {
				entry.assignment = &activityAssignment{id: id, from: from, to: to, text: item.Prompt}
			}
			entries = append(entries, entry)
		case "sendInput", "followupTask":
			if strings.TrimSpace(item.Prompt) != "" {
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: to, Kind: "assignment", Text: item.Prompt, Observed: now,
					assignment: &activityAssignment{id: id + "\x00" + receiver, from: from, to: to, text: item.Prompt}})
			}
		case "sendMessage":
			if strings.TrimSpace(item.Prompt) != "" {
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: from, Kind: "reply", Text: item.Prompt, Observed: now,
					message: &activityMessage{from: from, to: to, text: item.Prompt}})
			}
		}
		for i := first; i < len(entries); i++ {
			entries[i].native = &liveActivityNativeItem{thread: item.SenderThreadID, turn: "collaboration", item: id + "\x00" + receiver, phase: "item/completed"}
		}
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

func (u *appServerUI) restoreUsage(info appServerThreadInfo) {
	if u.proxy != nil && u.proxy.usage != nil {
		u.proxy.usage.Restore(info.ID, len(info.Turns) > 0)
	}
}

// appServerCommandWorkdir is the display form of the directory the host ran
// a command in, when it is not the workspace its activity is shown against.
// An unknown workspace or directory gets no label.
func appServerCommandWorkdir(item appServerItem, workspace string) string {
	if item.Type != "commandExecution" || !filepath.IsAbs(item.Cwd) || !filepath.IsAbs(workspace) || filepath.Clean(item.Cwd) == filepath.Clean(workspace) {
		return ""
	}
	return pathdisplay.ForWorkspace(workspace, filepath.Clean(item.Cwd))
}

func appServerCommandDirectory(item appServerItem, workspace string) string {
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
func appServerCommandText(item appServerItem, cwd string) string {
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

// retainOutput keeps a completed command's host output for the output dialog,
// continuing what its stream retained.
func (s *appServerSession) retainOutput(native *liveActivityNativeItem, item appServerItem) {
	if item.Type != "commandExecution" {
		return
	}
	native.exit = item.ExitCode
	if s.activity == nil {
		return
	}
	if run := s.commands[[3]string{native.thread, native.turn, native.item}]; run != nil {
		native.output = run.entry.native.output
	}
	if native.output == nil {
		native.output = s.activity.Outputs(&s.state).New()
	}
	native.output.Finish(item.AggregatedOutput, item.ExitCode)
}

// appServerCommandOutput keeps aggregate output even when the host reports no
// exit. Known failures carry their tail on the separate exit entry. Only confirmed
// success opens the settlement window or starts restored output collapsed.
func appServerCommandOutput(entry *activityPaneEntry, item appServerItem, settled time.Time) {
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
	args, ok := appServerCommandArgs(command)
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

// appServerShellScript is the script of a host Bash or sh command, exactly as the
// shell receives it with -c or -lc.
func appServerShellScript(command string) (string, bool) {
	args, ok := appServerCommandArgs(command)
	if !ok || len(args) != 3 || args[1] != "-lc" && args[1] != "-c" {
		return "", false
	}
	name := filepath.Base(args[0])
	name = strings.TrimSuffix(name, filepath.Ext(name))
	return shellinstrumentation.ShOriginal(args[2]), name == "bash" || name == "sh" || name == "dash"
}

// appServerCommandArgs unquotes a host command made only of literal words.
func appServerCommandArgs(command string) ([]string, bool) {
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil, false
	}
	stmt := program.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) < 3 {
		return nil, false
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		if !shellsyntax.LiteralParts(word.Parts, false) {
			return nil, false
		}
		// Fields removes shell quoting without consulting the environment or
		// executing substitutions. Reject words that expand to multiple args.
		fields, err := expand.Fields(&expand.Config{}, word)
		if err != nil || len(fields) != 1 {
			return nil, false
		}
		args = append(args, fields[0])
	}
	return args, true
}

// appServerEditText is one Activity operation per changed file, with its
// line counts taken from the patch Codex applied.
func appServerEditText(item appServerItem, cwd string) string {
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
		if moved := cmp.Or(change.Kind.MovePath, change.Kind.MovePath2); moved != "" {
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
func appServerEditPages(item appServerItem, cwd, phase string) []activityui.Block {
	if phase != "item/completed" || item.Status != "" && item.Status != "completed" {
		return nil
	}
	var pages []activityui.Block
	for _, change := range item.Changes {
		path, diff := pathdisplay.ForWorkspace(cwd, change.Path), change.Diff
		syntaxPath := ""
		if moved := cmp.Or(change.Kind.MovePath, change.Kind.MovePath2); moved != "" {
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
		if cmp.Or(change.Kind.MovePath, change.Kind.MovePath2) != "" {
			verb = "Move"
		}
		pages = append(pages, activityui.Block{Kind: "op", Verb: verb, Path: path, SyntaxPath: syntaxPath, Code: diff, Lang: lang, Fenced: true})
	}
	return pages
}

func appServerChangeCounts(change appServerFileChange) (added, removed int) {
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
