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

	"github.com/yusing/mekugi/internal/pathdisplay"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// appServerSession adapts app-server notifications for every thread of the
// session into the roster, Activity entries, and live edit cards. Router
// response interception is no longer the source of any of them; the router
// still owns captured changes, journals and cost.
type appServerSession struct {
	seq       uint64
	paths     map[string]string // Thread → canonical agent path.
	agents    []activityPaneAgent
	reasoning map[[3]string]string       // Summary text by thread, turn, item.
	patches   map[string]liveDiffPreview // Live edit card, by fileChange item.
	messages  map[string]activityPaneEntry
	finals    map[string]bool // The thread's current turn already sent its answer.
	cwd       string
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
	Source         jsontext.Value         `json:"source"`
	CreatedAt      int64                  `json:"createdAt"`
	UpdatedAt      int64                  `json:"updatedAt"`
	Turns          []appServerHistoryTurn `json:"turns"`
}

type appServerTokenUsage struct {
	InputTokens  uint64 `json:"inputTokens"`
	OutputTokens uint64 `json:"outputTokens"`
}

type appServerEvent struct {
	ThreadID   string                `json:"threadId"`
	TurnID     string                `json:"turnId"`
	ItemID     string                `json:"itemId"`
	Delta      string                `json:"delta"`
	Item       appServerItem         `json:"item"`
	Changes    []appServerFileChange `json:"changes"`
	Thread     appServerThreadInfo   `json:"thread"`
	TokenUsage struct {
		Total appServerTokenUsage `json:"total"`
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
	s.patches = make(map[string]liveDiffPreview)
	s.messages = make(map[string]activityPaneEntry)
	s.finals = make(map[string]bool)
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
	s.agent(path).Role = cmp.Or(info.AgentRole, spawn.AgentRole)
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
func (u *appServerUI) sessionEvent(m appServerMessage) (bool, error) {
	s := &u.session
	if s.paths == nil {
		return false, nil
	}
	switch m.Method {
	case "thread/started", "thread/tokenUsage/updated", "item/fileChange/patchUpdated", "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded",
		"turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta":
	default:
		return false, nil
	}
	var p appServerEvent
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return true, fmt.Errorf("%s: %w", m.Method, err)
	}
	main := p.ThreadID == u.thread
	var entries []activityPaneEntry
	now := time.Now()
	switch m.Method {
	case "thread/started":
		info := p.Thread
		if old := s.paths[info.ID]; info.ID == "" || old != "" && old != appServerPlaceholder(info.ID) {
			return true, nil
		}
		s.registerThread(info)

	case "thread/tokenUsage/updated":
		agent := s.agent(s.path(p.ThreadID))
		agent.InputTokens, agent.OutputTokens = p.TokenUsage.Total.InputTokens, p.TokenUsage.Total.OutputTokens
		u.observeCost(p.ThreadID, agent)
	case "turn/started":
		agent := s.agent(s.path(p.ThreadID))
		agent.Responding, agent.Final = true, false
		agent.Turns++
		s.finals[p.ThreadID] = false
		delete(s.messages, p.ThreadID)
	case "turn/completed":
		agent := s.agent(s.path(p.ThreadID))
		agent.Responding, agent.LastResponse = false, now
		agent.Final = p.Turn.Status == "completed"
		u.observeCost(p.ThreadID, agent)
		// A child whose provider omits the answer phase still finishes with
		// its last message; Main shows it as the child's answer.
		if last, ok := s.messages[p.ThreadID]; ok && !main && !s.finals[p.ThreadID] {
			last.Seq, last.Kind = s.next(), "final"
			entries = append(entries, last)
		}
		delete(s.messages, p.ThreadID)
	case "item/fileChange/patchUpdated":
		u.patch(p.ThreadID, p.ItemID, p.Changes, false)
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
		entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: s.path(p.ThreadID), Kind: "reasoning", Text: text, CallID: p.ItemID, Observed: now,
			native: &liveActivityNativeItem{thread: p.ThreadID, turn: p.TurnID, item: p.ItemID, phase: "summary"}})
	case "item/started", "item/completed", "item/agentMessage/delta":
		item := p.Item
		id := cmp.Or(p.ItemID, item.ID)
		native := &liveActivityNativeItem{thread: p.ThreadID, turn: p.TurnID, item: id, phase: m.Method}
		agent := s.path(p.ThreadID)
		switch item.Type {
		case "reasoning":
			if text := strings.Join(item.Summary, "\n\n"); strings.TrimSpace(text) != "" {
				s.reasoning[[3]string{p.ThreadID, p.TurnID, id}] = text
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "reasoning", Text: text, CallID: id, Observed: now, native: native})
			}
		case "commandExecution":
			entry := activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerCommandText(item, s.cwd), CallID: id, Observed: now, native: native}
			entries = append(entries, entry)
			if m.Method == "item/completed" && item.ExitCode != nil && *item.ExitCode != 0 {
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: id, Observed: now})
			}
		case "fileChange":
			if len(item.Changes) > 0 {
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: agent, Kind: "tool", Text: appServerEditText(item, s.cwd), CallID: id, Observed: now, native: native})
			}
			if m.Method == "item/started" && len(item.Changes) > 0 {
				u.patch(p.ThreadID, id, item.Changes, true)
			} else if m.Method == "item/completed" {
				u.finishPatch(id, item.Status)
			}
		case "collabAgentToolCall":
			if m.Method == "item/completed" {
				entries = append(entries, s.collab(item, id, now)...)
			}
		case "agentMessage":
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
			text := "Started"
			if item.Model != "" {
				text += " · " + commentaryCode(item.Model)
				if item.ReasoningEffort != "" {
					text += " " + commentaryCode(item.ReasoningEffort)
				}
			}
			entry := activityPaneEntry{Seq: s.next(), Agent: to, Kind: "start", Text: text, Observed: now}
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
				entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: from, Kind: "reply", Observed: now,
					Text: "[" + commentaryCode(from) + " -> " + commentaryCode(to) + "] Message sent:\n" + item.Prompt})
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
	agent.CostKnown = observed && report.cost.known && !report.Incomplete
	agent.CostPartial = report.missingUsage != 0
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
		command := appServerDisplayCommand(item.Command)
		if display, ok := toolActivityReads(command); ok {
			return display
		}
		fence := "```"
		for strings.Contains(command, fence) {
			fence += "`"
		}
		return "Run\n" + fence + "bash\n" + command + "\n" + fence
	}
	return strings.Join(parts, "\n\n")
}

// Hide only the host's literal shell command wrapper, never evaluate its words
// or discard surrounding operations. Execution and retained items stay intact.
// Source: codex-rs/shell-command/src/bash.rs:106:121@86be5320 extract_bash_command
// Source: codex-rs/shell-command/src/powershell.rs:43:73@86be5320 extract_powershell_command
// Unlike Codex's argv helper, preserve PowerShell's extra script arguments.
func appServerDisplayCommand(command string) string {
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(program.Stmts) != 1 {
		return command
	}
	stmt := program.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) < 3 {
		return command
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		if !shellCatLiteralParts(word.Parts, false) {
			return command
		}
		// Fields removes shell quoting without consulting the environment or
		// executing substitutions. Reject words that expand to multiple args.
		fields, err := expand.Fields(&expand.Config{}, word)
		if err != nil || len(fields) != 1 {
			return command
		}
		args = append(args, fields[0])
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
		if item.Status == "failed" || item.Status == "declined" {
			text += " · " + item.Status
		}
		parts = append(parts, text)
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

// appServerPatchText rebuilds apply_patch input from streamed file changes so
// the existing patch projection lays them out against the workspace.
func appServerPatchText(changes []appServerFileChange, cwd string, final bool) string {
	var b strings.Builder
	b.WriteString("*** Begin Patch\n")
	for _, change := range changes {
		path := change.Path
		if filepath.IsAbs(path) {
			path = pathdisplay.ForWorkspace(cwd, path)
		}
		switch change.Kind.Type {
		case "add":
			b.WriteString("*** Add File: " + path + "\n")
			if change.Diff != "" {
				for line := range strings.SplitSeq(strings.TrimSuffix(change.Diff, "\n"), "\n") {
					b.WriteString("+" + line + "\n")
				}
			}
		case "delete":
			b.WriteString("*** Delete File: " + path + "\n")
		default:
			b.WriteString("*** Update File: " + path + "\n")
			if move := cmp.Or(change.Kind.MovePath, change.Kind.MovePath2); move != "" {
				b.WriteString("*** Move to: " + pathdisplay.ForWorkspace(cwd, move) + "\n")
			}
			if change.Diff != "" {
				b.WriteString(strings.TrimSuffix(change.Diff, "\n") + "\n")
			}
		}
	}
	if final {
		b.WriteString("*** End Patch\n")
	}
	return b.String()
}

// patch projects a streamed patch into its caller's live card. Projection
// reads the files before Codex applies the patch; afterwards the card keeps
// its last projection.
func (u *appServerUI) patch(thread, item string, changes []appServerFileChange, final bool) {
	s := &u.session
	previous, known := s.patches[item]
	if known && previous.Complete {
		return
	}
	preview := liveDiffPreview{ID: "app-server:" + item, Workspace: s.cwd, Caller: s.path(thread), Thread: thread, Status: liveDiffPreviewEdit, Complete: final,
		Input: appServerPatchText(changes, s.cwd, final)}
	if s.cwd == "" {
		return
	}
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	preview = projectStockPatchPreview(ctx, s.cwd, preview)
	if strings.HasPrefix(preview.Status, liveDiffPreviewUnavailable) && known && len(previous.Files) > 0 {
		preview.Files, preview.Status = previous.Files, liveDiffPreviewEdit // A partial hunk keeps the last good frame.
	}
	s.patches[item] = preview
	u.ensureShell()
	u.shell.preview(preview)
}

func (u *appServerUI) finishPatch(item, status string) {
	s := &u.session
	preview, known := s.patches[item]
	if !known {
		return
	}
	preview.Complete = true
	if status == "failed" || status == "declined" {
		preview.Files, preview.Status = nil, liveDiffPreviewUnavailable+"patch "+status
	}
	s.patches[item] = preview
	u.ensureShell()
	u.shell.preview(preview)
}
