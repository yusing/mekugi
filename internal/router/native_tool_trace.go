package router

import (
	"bufio"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Native tracing is an observation channel, never a tool executor. Only the
// private directory supplied to this Codex launch is read. Completed receipts
// are persisted with change evidence before this disposable trace is removed.
type nativeToolTrace struct {
	directory string
	mu        sync.Mutex
	bundles   map[string]*nativeTraceBundle
	disabled  bool
}

type nativeTraceBundle struct {
	offset int64
	seq    uint64
	err    error
	bytes  int
	cells  map[string]*nativeTraceCell
	calls  map[string]*nativeTraceTool
	agents map[string]nativeAgentResult
}

type nativeAgentResult struct {
	thread, parent, turn, agent, state, reason string
	previous                                   []string
}

type nativeTraceCell struct {
	thread, callID string
	source         [32]byte
	ended          bool
	tools          []*nativeTraceTool
}

// nativeToolResult is durable host evidence, independent of model-printed text.
type nativeToolResult struct {
	CallID   string
	Tool     string
	Status   string
	ExitCode *int `json:",omitempty"`
}

type nativeTraceTool struct {
	nativeToolResult
	input         string
	command       execCommandInput
	environmentID jsontext.Value
	session       string
	terminal      bool
	stdinPoll     bool
	timeoutMS     *uint64 // Original invocation input, never persisted.
}

type nativeTraceRef struct {
	Path string `json:"path"`
}
type nativeTraceEvent struct {
	Version int    `json:"schema_version"`
	Seq     uint64 `json:"seq"`
	Thread  string `json:"thread_id"`
	Payload struct {
		Type      string         `json:"type"`
		Cell      string         `json:"runtime_cell_id"`
		Call      string         `json:"model_visible_call_id"`
		Source    string         `json:"source_js"`
		Tool      string         `json:"tool_call_id"`
		Status    string         `json:"status"`
		Child     string         `json:"child_thread_id"`
		Parent    string         `json:"parent_thread_id"`
		Turn      string         `json:"child_codex_turn_id"`
		HostTurn  string         `json:"codex_turn_id"`
		Carried   nativeTraceRef `json:"carried_payload"`
		Requester struct {
			Type string `json:"type"`
			Cell string `json:"runtime_cell_id"`
		} `json:"requester"`
		Invocation nativeTraceRef `json:"invocation_payload"`
		Result     nativeTraceRef `json:"result_payload"`
		Runtime    nativeTraceRef `json:"runtime_payload"`
	} `json:"payload"`
}

const nativeTraceReadLimit = 32 << 20

func nativeTraceSource(source string) [32]byte {
	if first, rest, ok := strings.Cut(source, "\n"); ok && strings.HasPrefix(strings.TrimSpace(first), "// @exec:") {
		source = rest
	}
	return sha256.Sum256([]byte(strings.TrimSpace(source)))
}

func readNativeTracePayload(directory string, ref nativeTraceRef, value any) error {
	// Native payload references are relative, single files under payloads/.
	if filepath.Dir(ref.Path) != "payloads" || filepath.Base(ref.Path) == "." {
		return errors.New("invalid native trace payload reference")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(ref.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return errors.New("native trace payload unavailable or too large")
	}
	f, err := root.Open(ref.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 8<<20 {
		return errors.New("native trace payload too large")
	}
	return json.Unmarshal(b, value)
}

func (b *nativeTraceBundle) update(directory string) error {
	if b.err != nil {
		return b.err
	}
	f, err := os.Open(filepath.Join(directory, "trace.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < b.offset {
		return errors.New("native trace was replaced or truncated")
	}
	if _, err = f.Seek(b.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(io.LimitReader(f, nativeTraceReadLimit+1))
	read := 0
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return nil
		} // Never consume an unfinished event.
		if err != nil {
			return err
		}
		read += len(line)
		if read > nativeTraceReadLimit {
			return errors.New("native trace read limit exceeded")
		}
		var event nativeTraceEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return err
		}
		if event.Version != 1 || event.Seq != b.seq+1 {
			return errors.New("unsupported or discontinuous native trace")
		}
		if err := b.event(directory, event); err != nil {
			return err
		}
		b.offset += int64(len(line))
		b.seq = event.Seq
	}
}

func (b *nativeTraceBundle) event(directory string, event nativeTraceEvent) error {
	p := event.Payload
	key := event.Thread + "\x00" + p.Cell
	switch p.Type {
	case "codex_turn_started":
		if event.Thread == "" || p.HostTurn == "" {
			return nil
		}
		if b.agents == nil {
			b.agents = make(map[string]nativeAgentResult)
		}
		if _, known := b.agents[event.Thread]; !known && len(b.agents) >= 4096 {
			return nil
		}
		prior := b.agents[event.Thread]
		previous := prior.previous
		if prior.turn != "" && prior.turn != p.HostTurn {
			if len(previous) >= 4096 {
				return nil
			}
			previous = append(previous, prior.turn)
			b.bytes += len(prior.turn)
		}
		b.agents[event.Thread] = nativeAgentResult{thread: event.Thread, turn: p.HostTurn, state: "working", previous: previous}
	case "agent_result_observed":
		if p.Child != event.Thread || p.Child == "" || p.Parent == "" || p.Turn == "" {
			return nil
		}
		prior, exists := b.agents[p.Child]
		if !exists || prior.turn != p.Turn {
			return nil
		}
		var result struct {
			Agent  string         `json:"child_agent_path"`
			Status jsontext.Value `json:"status"`
		}
		if readNativeTracePayload(directory, p.Carried, &result) != nil {
			return nil
		}
		state, reason := "", ""
		var status map[string]jsontext.Value
		if json.Unmarshal(result.Status, &status) == nil && len(status) == 1 {
			if value, ok := status["completed"]; ok && (value.Kind() == '"' || value.Kind() == 'n') {
				state = "done"
			} else if value, ok := status["errored"]; ok && value.Kind() == '"' {
				state, reason = "blocked", "Host child turn errored"
			}
		} else {
			switch string(result.Status) {
			case `"interrupted"`, `"shutdown"`, `"not_found"`:
				state, reason = "blocked", "Host child turn "+strings.Trim(string(result.Status), `"`)
			}
		}
		if state == "" {
			return nil
		}
		next := nativeAgentResult{thread: p.Child, parent: p.Parent, turn: p.Turn, agent: result.Agent, state: state, reason: reason, previous: prior.previous}
		if prior.state != "working" && (prior.parent != next.parent || prior.agent != next.agent || prior.state != next.state || prior.reason != next.reason) {
			next.state, next.parent, next.agent, next.reason = "working", "", "", ""
		}
		b.bytes += len(p.Child) + len(p.Parent) + len(p.Turn) + len(result.Agent)
		b.agents[p.Child] = next
	case "code_cell_started":
		if len(b.cells) >= 4096 || b.cells[key] != nil {
			return errors.New("native trace cell inventory unavailable")
		}
		b.cells[key] = &nativeTraceCell{thread: event.Thread, callID: p.Call, source: nativeTraceSource(p.Source)}
	case "code_cell_ended":
		if cell := b.cells[key]; cell != nil {
			cell.ended = true
		}
	case "tool_call_started":
		if p.Requester.Type != "code_cell" {
			return nil
		}
		cell := b.cells[event.Thread+"\x00"+p.Requester.Cell]
		if cell == nil {
			return errors.New("native trace missing parent cell")
		}
		if len(b.calls) >= 16384 || b.calls[event.Thread+"\x00"+p.Tool] != nil {
			return errors.New("native trace tool inventory unavailable")
		}
		var invocation struct {
			Name      string `json:"tool_name"`
			Namespace string `json:"tool_namespace"`
			Payload   struct {
				Type      string `json:"type"`
				Input     string `json:"input"`
				Arguments string `json:"arguments"`
			} `json:"payload"`
		}
		if err := readNativeTracePayload(directory, p.Invocation, &invocation); err != nil {
			return err
		}
		tool := &nativeTraceTool{CallID: p.Tool, Tool: invocation.Name, input: invocation.Payload.Input}
		if invocation.Namespace != "" && invocation.Namespace != "functions" {
			tool.Tool = invocation.Namespace + "." + invocation.Name
		}
		if tool.Tool == "exec_command" {
			var args struct {
				Command       string         `json:"cmd"`
				Workdir       string         `json:"workdir"`
				Shell         string         `json:"shell"`
				EnvironmentID jsontext.Value `json:"environment_id"`
				TimeoutMS     jsontext.Value `json:"timeout_ms"`
			}
			if err := json.Unmarshal([]byte(invocation.Payload.Arguments), &args); err != nil {
				return err
			}
			tool.command = execCommandInput{Command: args.Command, Workdir: args.Workdir, Shell: args.Shell}
			tool.environmentID = args.EnvironmentID
			// Optional display metadata must not invalidate execution evidence.
			if len(args.TimeoutMS) > 0 {
				var timeout *uint64
				if json.Unmarshal(args.TimeoutMS, &timeout) == nil {
					tool.timeoutMS = timeout
				}
			}
		}
		if tool.Tool == "write_stdin" {
			var args struct {
				Chars   string         `json:"chars"`
				Session jsontext.Value `json:"session_id"`
			}
			tool.stdinPoll = json.Unmarshal([]byte(invocation.Payload.Arguments), &args) == nil && args.Chars == ""
			tool.session = strings.Trim(string(args.Session), "\"")
		}
		cell.tools = append(cell.tools, tool)
		b.calls[event.Thread+"\x00"+p.Tool] = tool
		b.bytes += len(tool.input) + len(tool.command.Command) + len(tool.command.Workdir) + len(tool.command.Shell) + len(tool.environmentID) + len(tool.CallID) + 256
		if b.bytes > 64<<20 {
			return errors.New("native trace evidence cache limit exceeded")
		}
	case "tool_call_ended":
		tool := b.calls[event.Thread+"\x00"+p.Tool]
		if tool == nil {
			return nil
		}
		tool.Status = p.Status
		if p.Status == "failed" || p.Status == "cancelled" {
			tool.terminal = true
			return nil
		}
		if p.Status != "completed" {
			return nil
		}
		if tool.Tool == "apply_patch" {
			tool.terminal = true
			return nil
		}
		if tool.Tool != "exec_command" && tool.Tool != "write_stdin" {
			tool.terminal = true
			return nil
		}
		var result struct {
			Type  string `json:"type"`
			Value struct {
				Exit    *int           `json:"exit_code"`
				Session jsontext.Value `json:"session_id"`
			} `json:"value"`
		}
		if err := readNativeTracePayload(directory, p.Result, &result); err != nil {
			return err
		}
		if result.Type != "code_mode_response" {
			return errors.New("native trace result kind mismatch")
		}
		if result.Value.Exit != nil {
			tool.ExitCode = result.Value.Exit
			tool.terminal = true
		}
		if session := strings.Trim(string(result.Value.Session), "\""); session != "" && session != "null" {
			tool.session = session
		}
		if tool.ExitCode != nil && *tool.ExitCode != 0 {
			tool.Status = "failed"
		}
		if tool.Tool == "write_stdin" && tool.terminal && tool.session != "" {
			// A terminal poll resolves earlier yielded observations of this
			// same host process, without completing other threads or sessions.
			for key, prior := range b.calls {
				if strings.HasPrefix(key, event.Thread+"\x00") && !prior.terminal &&
					prior.Status == "completed" && prior.session == tool.session {
					prior.ExitCode, prior.Status, prior.terminal = tool.ExitCode, tool.Status, true
				}
			}
		}
	case "tool_call_runtime_ended":
		tool := b.calls[event.Thread+"\x00"+p.Tool]
		if tool == nil || tool.Tool != "exec_command" {
			return nil
		}
		var result struct {
			Exit *int `json:"exit_code"`
		}
		if err := readNativeTracePayload(directory, p.Runtime, &result); err != nil {
			return err
		}
		if result.Exit != nil {
			tool.ExitCode = result.Exit
			tool.terminal = true
			tool.Status = p.Status
			if *result.Exit != 0 {
				tool.Status = "failed"
			}
		}
	}
	return nil
}

func (t *nativeToolTrace) readAgentResults() []nativeAgentResult {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	results := make(map[string]nativeAgentResult)
	ambiguous := make(map[string]bool)
	for _, bundle := range t.readBundles() {
		for key, result := range bundle.agents {
			if _, exists := results[key]; exists {
				ambiguous[key] = true
			}
			results[key] = result
		}
	}
	var found []nativeAgentResult
	for key, result := range results {
		if !ambiguous[key] && result.state != "" {
			result.previous = append([]string(nil), result.previous...)
			found = append(found, result)
		}
	}
	return found
}

// readCell returns a copy; concurrent requests never share mutable outcomes.
// Missing/corrupt/bounded-out evidence is unavailable, never a successful call.
func (t *nativeToolTrace) readCell(thread, callID, source string) *nativeTraceCell {
	if t == nil || thread == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var found *nativeTraceCell
	for _, b := range t.readBundles() {
		for _, cell := range b.cells {
			if cell.thread != thread || cell.callID != callID || cell.source != nativeTraceSource(source) {
				continue
			}
			if found != nil {
				return nil
			}
			copy := *cell
			copy.tools = nil
			for _, tool := range cell.tools {
				clone := *tool
				clone.environmentID = append(jsontext.Value(nil), tool.environmentID...)
				copy.tools = append(copy.tools, &clone)
			}
			found = &copy
		}
	}
	return found
}

// readBundles refreshes the existing bounded inventory with the trace lock held.
func (t *nativeToolTrace) readBundles() []*nativeTraceBundle {
	if t.disabled {
		return nil
	}
	directory, err := os.Open(t.directory)
	if err != nil {
		return nil
	}
	entries, err := directory.ReadDir(65)
	directory.Close()
	if err != nil && err != io.EOF || len(entries) > 64 {
		return nil
	}
	if t.bundles == nil {
		t.bundles = make(map[string]*nativeTraceBundle)
	}
	var bundles []*nativeTraceBundle
	bytes := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "trace-") {
			continue
		}
		b := t.bundles[entry.Name()]
		if b == nil {
			b = &nativeTraceBundle{cells: make(map[string]*nativeTraceCell), calls: make(map[string]*nativeTraceTool)}
			t.bundles[entry.Name()] = b
		}
		b.err = b.update(filepath.Join(t.directory, entry.Name()))
		bytes += b.bytes
		if bytes > 64<<20 {
			t.disabled = true
			t.bundles = nil
			return nil
		}
		if b.err != nil {
			continue
		}
		bundles = append(bundles, b)
	}
	return bundles
}

// commandTimeout reads the original input by exact native thread/call identity.
// Missing or ambiguous input has no timeout label, including after fresh resume.
func (t *nativeToolTrace) commandTimeout(thread, call string) (uint64, bool) {
	if t == nil || thread == "" || call == "" {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var found *nativeTraceTool
	for _, b := range t.readBundles() {
		if tool := b.calls[thread+"\x00"+call]; tool != nil {
			if found != nil {
				return 0, false
			}
			found = tool
		}
	}
	if found == nil || found.Tool != "exec_command" || found.timeoutMS == nil {
		return 0, false
	}
	return *found.timeoutMS, true
}

// patchCall selects one occurrence, so repeated identical inputs retain their
// own native identities and outcomes in host dispatch order.
func (c *nativeTraceCell) patchCall(input string, occurrence int) *nativeTraceTool {
	if c == nil || occurrence < 0 {
		return nil
	}
	for _, tool := range c.tools {
		if tool.Tool != "apply_patch" || tool.input != input {
			continue
		}
		if occurrence == 0 {
			return tool
		}
		occurrence--
	}
	return nil
}

func (c *nativeTraceCell) patch(input string, occurrence int) (nativeToolResult, bool) {
	tool := c.patchCall(input, occurrence)
	if tool == nil || !c.ended || !tool.terminal {
		return nativeToolResult{}, false
	}
	return tool.nativeToolResult, true
}

// execCommands lists an ended cell's nested exec_command calls.
func (c *nativeTraceCell) execCommands() []string {
	if c == nil || !c.ended {
		return nil
	}
	var calls []string
	for _, tool := range c.tools {
		if tool.Tool == "exec_command" && tool.CallID != "" {
			calls = append(calls, tool.CallID)
		}
	}
	return calls
}

func (c *nativeTraceCell) pending() bool {
	if c == nil {
		return false
	}
	for _, tool := range c.tools {
		if tool.Tool == applyPatchToolName && !tool.terminal {
			return true
		}
		if tool.Tool == "exec_command" && !tool.terminal && tool.session != "" {
			return true
		}
	}
	return !c.ended
}

// commands confirms each observed command by exactly one terminal host call.
// Journal publications the router lowered into history's carrier are not
// observed commands and do not affect the outcome; any unobserved occurrence
// leaves the outcome unconfirmed.
func (c *nativeTraceCell) commands(history *mekugiHistory, workspace string) ([]nativeToolResult, bool) {
	if c == nil || !c.ended || history.ExecObservation == nil {
		return nil, false
	}
	commands := history.ExecObservation.Commands
	normalize := func(command execCommandInput) execCommandInput {
		if command.Workdir == "" {
			command.Workdir = workspace
		} else if !filepath.IsAbs(command.Workdir) {
			command.Workdir = filepath.Join(workspace, command.Workdir)
		}
		command.Workdir = filepath.Clean(command.Workdir)
		return command
	}
	want := make(map[execCommandInput]int)
	for _, command := range commands {
		want[normalize(command)]++
	}
	var results []nativeToolResult
	for _, tool := range c.tools {
		if tool.Tool != "exec_command" {
			continue
		}
		if !execLocalEnvironment(tool.environmentID) {
			return nil, false
		}
		command := normalize(tool.command)
		if command.Shell == "" {
			for candidate, remaining := range want {
				if remaining > 0 && candidate.Command == command.Command && candidate.Workdir == command.Workdir {
					command.Shell = candidate.Shell
					break
				}
			}
		}
		if want[command] == 0 && history.lowersJournalCommand(nativeJournalCommand.FindStringSubmatch(tool.command.Command)) {
			continue
		}
		if !tool.terminal || want[command] == 0 {
			return nil, false
		}
		want[command]--
		results = append(results, tool.nativeToolResult)
	}
	for _, remaining := range want {
		if remaining != 0 {
			return nil, false
		}
	}
	return results, len(results) != 0
}

func (r nativeToolResult) text() string {
	text := "host tool " + r.CallID + " " + r.Tool + ": " + r.Status
	if r.ExitCode != nil {
		text += "; exit " + strconv.Itoa(*r.ExitCode)
	}
	return text
}
