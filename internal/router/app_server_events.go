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

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// appServerSession adapts app-server notifications for every thread of the
// session into the roster and Activity entries. Captured edits and previews
// retain the shared router owners, as do journals and cost.
type appServerSession struct {
	seq       uint64
	paths     map[string]string // Thread → canonical agent path.
	agents    []activityPaneAgent
	reasoning map[[3]string]string    // Summary text by thread, turn, item.
	thinking  map[[3]string]time.Time // Start of reasoning still streaming.
	// Thread → thinking block shown from its provider request's start, before
	// any reasoning item exists; the first reasoning item takes it over.
	pendingThinking map[string]pendingThinking
	messages        map[string]activityPaneEntry
	finals          map[string]bool                    // The thread's current turn already sent its answer.
	metadata        map[string]string                  // Child thread → pending metadata request ID; empty when settled.
	commands        map[[3]string]*appServerCommandRun // Live commands by thread, turn, item.
	cwd             string
	waits           map[[3]string][]appServerWaitTarget // Start-time targets by thread, turn, item.
	waitStore       *mekugiReplayStore
	waitContext     context.Context
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
	ID             string                 `json:"id"`
	ParentThreadID string                 `json:"parentThreadId"`
	AgentNickname  string                 `json:"agentNickname"`
	AgentRole      string                 `json:"agentRole"`
	Cwd            string                 `json:"cwd"`
	Path           string                 `json:"path"`
	Source         jsontext.Value         `json:"source"`
	CreatedAt      int64                  `json:"createdAt"`
	UpdatedAt      int64                  `json:"updatedAt"`
	Turns          []appServerHistoryTurn `json:"turns"`
}

type appServerTokenUsage struct {
	TotalTokens           uint64 `json:"totalTokens"`
	InputTokens           uint64 `json:"inputTokens"`
	OutputTokens          uint64 `json:"outputTokens"`
	CachedInputTokens     uint64 `json:"cachedInputTokens"`
	ReasoningOutputTokens uint64 `json:"reasoningOutputTokens"`
}

type appServerEvent struct {
	ThreadID   string              `json:"threadId"`
	TurnID     string              `json:"turnId"`
	ItemID     string              `json:"itemId"`
	Delta      string              `json:"delta"`
	ProcessID  string              `json:"processId"`
	Stdin      string              `json:"stdin"`
	Item       appServerItem       `json:"item"`
	Thread     appServerThreadInfo `json:"thread"`
	TokenUsage struct {
		Total              appServerTokenUsage  `json:"total"`
		Last               *appServerTokenUsage `json:"last"`
		ModelContextWindow uint64               `json:"modelContextWindow"`
	} `json:"tokenUsage"`
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

func (s *appServerSession) start(thread, cwd string) {
	s.paths = map[string]string{thread: "/root"}
	s.agents = []activityPaneAgent{{Name: "/root", Role: "main", Started: time.Now()}}
	s.reasoning = make(map[[3]string]string)
	s.thinking = make(map[[3]string]time.Time)
	s.pendingThinking = make(map[string]pendingThinking)
	s.messages = make(map[string]activityPaneEntry)
	s.finals = make(map[string]bool)
	s.metadata = make(map[string]string)
	s.commands = make(map[[3]string]*appServerCommandRun)
	s.waits = make(map[[3]string][]appServerWaitTarget)
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
		s.agents = append(s.agents, activityPaneAgent{Name: path, Started: time.Now()})
	}
	s.paths[info.ID] = path
	s.agent(path).Role = cmp.Or(info.AgentRole, spawn.AgentRole, s.agent(path).Role)
	restoreContextUsage(s.agent(path), info)
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
	s.agents = append(s.agents, activityPaneAgent{Name: path, Started: time.Now()})
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
func (u *appServerUI) sessionEvent(m appserver.Message) (bool, error) {
	s := &u.session
	if s.paths == nil {
		return false, nil
	}
	switch m.Method {
	case "thread/started", "thread/tokenUsage/updated", "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded",
		"turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta", "item/commandExecution/terminalInteraction",
		"item/commandExecution/outputDelta":
	default:
		return false, nil
	}
	var p appServerEvent
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return true, fmt.Errorf("%s: %w", m.Method, err)
	}
	u.observeProgress(m.Method, p)
	if m.Method == "item/started" {
		u.observeShellItem(p.ThreadID, p.Item)
	}
	if p.Item.Type == "commandExecution" {
		// Every command is offered for matching, including ones Activity hides,
		// so their shells never wait for an item that will not arrive.
		key := [3]string{p.ThreadID, p.TurnID, cmp.Or(p.ItemID, p.Item.ID)}
		switch {
		case m.Method == "item/started":
			u.execTrack.start(key, p.Item.Command)
		case m.Method == "item/completed" && (u.session.commands[key] == nil || !u.execTrack.tracking(key)):
			// Only a command Activity presents holds its report until it ends.
			u.execTrack.finish(key)
		}
	}
	main := p.ThreadID == u.thread
	if p.ThreadID != "" && !main {
		if err := u.requestThreadMetadata(p.ThreadID); err != nil {
			return true, err
		}
	}
	var entries []activityPaneEntry
	now := time.Now()
	switch m.Method {
	case "thread/started":
		info := p.Thread
		if info.ID == "" || info.ID == u.thread {
			return true, nil
		}
		old := s.path(info.ID)
		s.registerThread(info)
		u.renameThreadActivity(old, s.paths[info.ID])

	case "thread/tokenUsage/updated":
		if main {
			u.exitUsage = p.TokenUsage.Total
		}
		agent := s.agent(s.path(p.ThreadID))
		agent.LastResponse = now // Usage arrives once per provider response.
		agent.InputTokens, agent.OutputTokens = p.TokenUsage.Total.InputTokens, p.TokenUsage.Total.OutputTokens
		agent.ContextWindow = p.TokenUsage.ModelContextWindow
		agent.ContextKnown = p.TokenUsage.Last != nil
		agent.ContextTokens = 0
		if p.TokenUsage.Last != nil {
			agent.ContextTokens = p.TokenUsage.Last.TotalTokens
		}
		u.observeCost(p.ThreadID, agent)
	case "turn/started":
		agent := s.agent(s.path(p.ThreadID))
		agent.Responding, agent.Final = true, false
		agent.Turns++
		s.finals[p.ThreadID] = false
		delete(s.messages, p.ThreadID)
	case "turn/completed":
		for key := range s.waits {
			if key[0] == p.ThreadID && key[1] == p.Turn.ID {
				delete(s.waits, key)
			}
		}
		entries = append(entries, s.endThinking(p.ThreadID, now)...)
		agent := s.agent(s.path(p.ThreadID))
		agent.Responding, agent.LastResponse = false, now
		agent.Final = p.Turn.Status == "completed"
		u.observeCost(p.ThreadID, agent)
		// A child whose provider omits the answer phase still finishes with
		// its last message; Main shows it as the child's answer.
		if last, ok := s.messages[p.ThreadID]; ok && !main && !s.finals[p.ThreadID] && p.Turn.Status == "completed" && last.native.turn == p.Turn.ID {
			last.Seq, last.Kind = s.next(), "final"
			native := *last.native
			native.phase = "item/completed"
			last.native = &native
			entries = append(entries, last)
		}
		delete(s.messages, p.ThreadID)
	case "item/commandExecution/outputDelta":
		// Bounded as it arrives; the next frame shows it, so bursts cost one render.
		if run := s.commands[[3]string{p.ThreadID, p.TurnID, p.ItemID}]; run != nil && run.done == nil {
			run.output.Write(p.Delta)
			run.dirty = true
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded":
		key := [3]string{p.ThreadID, p.TurnID, p.ItemID}
		text := s.reasoning[key]
		if m.Method == "item/reasoning/summaryPartAdded" {
			if text != "" {
				s.reasoning[key] = text + "\n\n"
			}
			break
		}
		text += p.Delta
		s.reasoning[key] = text
		native := &liveActivityNativeItem{thread: p.ThreadID, turn: p.TurnID, item: p.ItemID, phase: "summary"}
		if _, ok := s.thinking[key]; !ok {
			s.thinking[key] = now
			if pending, ok := s.pendingThinking[p.ThreadID]; ok {
				delete(s.pendingThinking, p.ThreadID)
				s.thinking[key], native.replaces = pending.at, pending.item
			}
		}
		entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: s.path(p.ThreadID), Kind: "reasoning", Text: text, CallID: p.ItemID, Observed: now, native: native})
	case "item/started", "item/completed", "item/agentMessage/delta":
		item := p.Item
		id := cmp.Or(p.ItemID, item.ID)
		item = u.waitItem(item, p.ThreadID, p.TurnID, id, m.Method == "item/started")
		native := &liveActivityNativeItem{thread: p.ThreadID, turn: p.TurnID, item: id, phase: m.Method, live: true}
		agent := s.path(p.ThreadID)
		if m.Method != "item/completed" && item.Type != "reasoning" && item.Type != "userMessage" {
			// Other output started first: that request streamed no reasoning.
			// A completion can be the previous request's command, notified late.
			// Applied now: the item's own handling may return without applying.
			if dropped := s.dropThinking(p.ThreadID); len(dropped) != 0 {
				u.applyActivity(dropped, nil)
			}
		}
		if text, wait, handled := appServerProgress(item, m.Method); handled {
			if text != "" {
				native.wait = wait
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "progress", Text: text, Observed: now, native: native})
			}
			break
		}
		switch item.Type {
		case "subAgentActivity":
			if item.AgentThreadID != "" && item.AgentThreadID != u.thread {
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
			key := [3]string{p.ThreadID, p.TurnID, id}
			text := strings.Join(item.Summary, "\n\n")
			if m.Method == "item/completed" {
				native.collapseAt = now.Add(activityui.ThinkingLinger)
				if pending, ok := s.pendingThinking[p.ThreadID]; ok && s.thinking[key].IsZero() && strings.TrimSpace(text) != "" {
					// A summary delivered only at completion still takes over the block.
					delete(s.pendingThinking, p.ThreadID)
					s.thinking[key], native.replaces = pending.at, pending.item
				}
				if started, ok := s.thinking[key]; ok {
					delete(s.thinking, key)
					native.thought = now.Sub(started)
					// A route whose history cannot replay it completes the
					// streamed text without a summary.
					if strings.TrimSpace(text) == "" {
						text = s.reasoning[key]
					}
				}
			}
			if strings.TrimSpace(text) != "" {
				s.reasoning[key] = text
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "reasoning", Text: text, CallID: id, Observed: now, native: native})
			}
		case "commandExecution", "webSearch":
			if u.internalJournalCommand(p.ThreadID, item) {
				break
			}
			native.searchResults = appServerSearchResults(item)
			entry := activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerToolText(item, s.cwd), CallID: id, Observed: now, native: native}
			key := [3]string{p.ThreadID, p.TurnID, id}
			if m.Method != "item/completed" {
				if item.Type == "commandExecution" {
					native.running = true
					if s.commands[key] == nil {
						s.commands[key] = &appServerCommandRun{entry: entry, instant: instantOperations(entry.Text)}
					}
				}
				entries = append(entries, entry)
				break
			}
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
			entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: "View " + commentaryCode(pathdisplay.ForWorkspace(s.cwd, item.Path)), CallID: id, Observed: now, native: native})
		case "fileChange":
			if len(item.Changes) > 0 {
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerEditText(item, s.cwd), CallID: id, Observed: now, native: native})
			}
		case "collabAgentToolCall":
			if m.Method == "item/completed" && (item.Status == "" || item.Status == "completed") {
				entries = append(entries, s.collab(item, id, now)...)
			}
		case "agentMessage":
			if item.Delivery == "async" && len(item.Questions) > 0 {
				return !main, nil
			}
			if main {
				return false, nil // Main's messages belong to the transcript handler.
			}
			if m.Method != "item/completed" || strings.TrimSpace(item.Text) == "" {
				return true, nil
			}
			native.phase = "message" // A later answer promotion may replace it.
			entry := activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "text", Text: item.Text, Observed: now, native: native}
			if item.Phase == "final_answer" || item.Phase == "finalAnswer" {
				entry.Kind = "final"
				native.phase = "item/completed"
				s.finals[p.ThreadID] = true
			}
			s.messages[p.ThreadID] = entry
			entries = append(entries, entry)
		default:
			if main {
				return false, nil // Main's messages belong to the transcript handler.
			}
			return true, nil
		}
	}
	u.applyActivity(entries, slices.Clone(s.agents))
	// Main's turn lifecycle also drives the composer state.
	return !main || !strings.HasPrefix(m.Method, "turn/"), nil
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
	appServerSucceededOutput(&entry, item, now)
	done := []activityPaneEntry{entry}
	if item.ExitCode != nil && *item.ExitCode != 0 {
		exit := activityPaneEntry{Seq: s.next(), Agent: entry.Agent, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: entry.CallID, Observed: now}
		exit.outputTail, exit.outputOmit = appServerOutputTail(item.AggregatedOutput)
		done = append(done, exit)
	}
	return done
}

type pendingThinking struct {
	item string // Placeholder item ID, never a host item.
	at   time.Time
}

// beginThinking shows a thinking block from a provider request's start, as
// grok-build does, so the wait for the first reasoning delta is not silent.
// Reasoning still streaming for the thread keeps its own block.
func (s *appServerSession) beginThinking(thread string, at time.Time) []activityPaneEntry {
	if s.pendingThinking == nil {
		return nil
	}
	for key := range s.thinking {
		if key[0] == thread {
			return nil
		}
	}
	entries := s.dropThinking(thread)
	item := "thinking:" + strconv.FormatUint(s.next(), 10)
	s.pendingThinking[thread] = pendingThinking{item: item, at: at}
	return append(entries, activityPaneEntry{Seq: s.next(), Agent: s.path(thread), Kind: "reasoning", CallID: item, Observed: at,
		native: &liveActivityNativeItem{thread: thread, item: item, phase: "summary", live: true}})
}

// dropThinking removes a request's thinking block no reasoning took over,
// as grok-build removes an empty block rather than showing "Thought".
func (s *appServerSession) dropThinking(thread string) []activityPaneEntry {
	pending, ok := s.pendingThinking[thread]
	if !ok {
		return nil
	}
	delete(s.pendingThinking, thread)
	return []activityPaneEntry{{Seq: s.next(), Agent: s.path(thread), Kind: "reasoning", CallID: pending.item,
		native: &liveActivityNativeItem{thread: thread, item: pending.item, phase: "discarded"}}}
}

// endThinking completes reasoning a finished turn never completed, such as
// after an interrupt, so it stops presenting as streaming.
func (s *appServerSession) endThinking(thread string, now time.Time) []activityPaneEntry {
	entries := s.dropThinking(thread)
	var keys [][3]string
	for key := range s.thinking {
		if key[0] == thread {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b [3]string) int { return cmp.Compare(a[1]+"\x00"+a[2], b[1]+"\x00"+b[2]) })
	for _, key := range keys {
		entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: s.path(thread), Kind: "reasoning", Text: s.reasoning[key], CallID: key[2], Observed: now,
			native: &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2], phase: "item/completed", thought: now.Sub(s.thinking[key]), collapseAt: now.Add(activityui.ThinkingLinger)}})
		delete(s.thinking, key)
	}
	return entries
}

// flushCommandOutput re-sends each live command whose output changed since
// the last frame, with its current tail, rolling a burst through a share at a
// time. A held completion follows once nothing is pending. Late output cannot
// reopen a completed command, whose entry it would otherwise replace.
func (u *appServerUI) flushCommandOutput() {
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
				if done.native != nil && !done.native.settled.IsZero() {
					done.native.settled = time.Now() // Output settles once it has shown.
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

// observeCost reads the router's accounting for the thread. App-server token
// counts are shown, never priced a second time.
func (u *appServerUI) observeCost(thread string, agent *activityPaneAgent) {
	if u.proxy == nil || u.proxy.usage == nil {
		return
	}
	report, observed := u.proxy.usage.snapshot(thread)
	agent.Cost = report.cost.cachedInput + report.cost.uncachedInput + report.cost.output
	agent.CostKnown = observed && report.cost.known
	agent.CostPartial = report.missingUsage != 0
	agent.Roundtrips = u.proxy.usage.roundtrips(thread)
}

// appServerCommandText uses Codex's typed classification, then the shared
// display classifier for frontends that Codex does not recognize. Neither
// classification changes the executed command or retained host item.
func appServerCommandText(item appServerItem, cwd string) string {
	var parts []string
	for _, action := range item.CommandActions {
		switch action.Type {
		case "read":
			parts = append(parts, "Read "+commentaryCode(pathdisplay.ForWorkspace(cwd, action.Path)))
		case "listFiles":
			parts = append(parts, "List "+commentaryCode(cmp.Or(pathdisplay.ForWorkspace(cwd, action.Path), ".")))
		case "search":
			text := "Search " + commentaryCode(action.Query)
			if action.Path != "" {
				text += " in " + commentaryCode(pathdisplay.ForWorkspace(cwd, action.Path))
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
		return toolActivityShell(appServerDisplayCommand(item.Command))
	}
	return strings.Join(parts, "\n\n")
}

// appServerSucceededOutput keeps a successful command's output tail on its
// entry, open from settled until its agent's next event; a zero settled time
// starts it collapsed. Failures carry their tail on the exit entry instead.
func appServerSucceededOutput(entry *activityPaneEntry, item appServerItem, settled time.Time) {
	if item.Type != "commandExecution" || item.ExitCode == nil || *item.ExitCode != 0 {
		return
	}
	entry.outputTail, entry.outputOmit = appServerOutputTail(item.AggregatedOutput)
	entry.native.settled, entry.native.collapsed = settled, settled.IsZero()
	entry.native.changes = mchangesOutputRows(item)
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
	case "bash", "zsh", "sh":
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

// appServerShellScript is the script of a host Bash command, exactly as the
// shell receives it with -c or -lc.
func appServerShellScript(command string) (string, bool) {
	args, ok := appServerCommandArgs(command)
	if !ok || len(args) != 3 || args[1] != "-lc" && args[1] != "-c" {
		return "", false
	}
	name := filepath.Base(args[0])
	return args[2], strings.TrimSuffix(name, filepath.Ext(name)) == "bash"
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
		if !shellCatLiteralParts(word.Parts, false) {
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
		text := verb + " " + commentaryCode(pathdisplay.ForWorkspace(cwd, change.Path)) + fmt.Sprintf(" · +%d −%d", added, removed)
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
