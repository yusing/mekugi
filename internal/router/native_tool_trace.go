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
	input    string
	command  execCommandInput
	session  string
	terminal bool
}

type nativeTraceRef struct {
	Path string `json:"path"`
}
type nativeTraceEvent struct {
	Version int    `json:"schema_version"`
	Seq     uint64 `json:"seq"`
	Thread  string `json:"thread_id"`
	Payload struct {
		Type      string `json:"type"`
		Cell      string `json:"runtime_cell_id"`
		Call      string `json:"model_visible_call_id"`
		Source    string `json:"source_js"`
		Tool      string `json:"tool_call_id"`
		Status    string `json:"status"`
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
				Command string `json:"cmd"`
				Workdir string `json:"workdir"`
				Shell   string `json:"shell"`
			}
			if err := json.Unmarshal([]byte(invocation.Payload.Arguments), &args); err != nil {
				return err
			}
			tool.command = execCommandInput{Command: args.Command, Workdir: args.Workdir, Shell: args.Shell}
		}
		cell.tools = append(cell.tools, tool)
		b.calls[event.Thread+"\x00"+p.Tool] = tool
		b.bytes += len(tool.input) + len(tool.command.Command) + len(tool.command.Workdir) + len(tool.command.Shell) + len(tool.CallID) + 256
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
		if tool.Tool != "exec_command" {
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
		tool.session = strings.Trim(string(result.Value.Session), "\"")
		if tool.session == "null" {
			tool.session = ""
		}
		if tool.ExitCode != nil && *tool.ExitCode != 0 {
			tool.Status = "failed"
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

// readCell returns a copy; concurrent requests never share mutable outcomes.
// Missing/corrupt/bounded-out evidence is unavailable, never a successful call.
func (t *nativeToolTrace) readCell(thread, callID, source string) *nativeTraceCell {
	if t == nil || thread == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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
	var found *nativeTraceCell
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
				copy.tools = append(copy.tools, &clone)
			}
			found = &copy
		}
	}
	return found
}

func (c *nativeTraceCell) patch(input string) (nativeToolResult, bool) {
	if c == nil || !c.ended {
		return nativeToolResult{}, false
	}
	var found *nativeTraceTool
	for _, tool := range c.tools {
		if tool.Tool != "apply_patch" || tool.input != input {
			continue
		}
		if found != nil || !tool.terminal {
			return nativeToolResult{}, false
		}
		found = tool
	}
	if found == nil {
		return nativeToolResult{}, false
	}
	return found.nativeToolResult, true
}

func (c *nativeTraceCell) pending() bool {
	if c == nil {
		return false
	}
	for _, tool := range c.tools {
		if tool.Tool == "exec_command" && !tool.terminal && tool.session != "" {
			return true
		}
	}
	return !c.ended
}

func (c *nativeTraceCell) commands(commands []execCommandInput, workspace string) ([]nativeToolResult, bool) {
	if c == nil || !c.ended {
		return nil, false
	}
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
		command := normalize(tool.command)
		if command.Shell == "" {
			for candidate, remaining := range want {
				if remaining > 0 && candidate.Command == command.Command && candidate.Workdir == command.Workdir {
					command.Shell = candidate.Shell
					break
				}
			}
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
