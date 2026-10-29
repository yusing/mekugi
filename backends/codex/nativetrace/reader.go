// Package nativetrace decodes bounded observations from an invocation-owned Codex trace.
package nativetrace

import (
	"bufio"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
)

// Source: apps/mekugi/internal/router/native_tool_trace.go:19:597@[d428f9ae] native trace decoding.

// Reader observes the private directory supplied to a Codex invocation. It owns
// no process, trace lifetime, capture policy or publication side effects.
type Reader struct {
	directory string
	mu        sync.Mutex
	bundles   map[string]*bundle
	disabled  bool
	notice    func(string)
	reported  bool
}

// New observes directory without taking ownership of its lifetime.
func New(directory string) *Reader { return &Reader{directory: directory} }

// NewWithNotice reports the first unsupported or discontinuous trace in an invocation.
func NewWithNotice(directory string, notice func(string)) *Reader {
	return &Reader{directory: directory, notice: notice}
}

// Directory is the invocation's trace location, also supplied to the host.
func (t *Reader) Directory() string { return t.directory }

type bundle struct {
	offset int64
	seq    uint64
	err    error
	bytes  int
	cells  map[string]*Cell
	calls  map[string]*tool
}

type traceFormatError struct {
	version       int
	seq, expected uint64
}

func (e *traceFormatError) Error() string {
	return fmt.Sprintf("Native trace version %d is unsupported or discontinuous (sequence %d, expected %d). Nested tool confirmation may be unavailable.", e.version, e.seq, e.expected)
}

// Cell is a detached observation correlated by thread, outer call and source.
// Its methods do not refresh the reader or execute tools.
type Cell struct {
	thread, callID string
	source         [32]byte
	ended          bool
	tools          []*tool
}

type tool struct {
	binding.ToolResult
	input         string
	command       commandanalysis.Command
	environmentID jsontext.Value
	session       string
	terminal      bool
	stdinPoll     bool
	timeoutMS     *uint64 // Original invocation input, never persisted.
}

type payloadRef struct {
	Path string `json:"path"`
}
type event struct {
	Version int    `json:"schema_version"`
	Seq     uint64 `json:"seq"`
	Thread  string `json:"thread_id"`
	Payload struct {
		Type   string `json:"type"`
		Cell   string `json:"runtime_cell_id"`
		Call   string `json:"model_visible_call_id"`
		Source string `json:"source_js"`
		Tool   string `json:"tool_call_id"`
		Status string `json:"status"`
		// Unconsumed agent events still receive envelope field validation.
		Child     string     `json:"child_thread_id"`
		Parent    string     `json:"parent_thread_id"`
		Turn      string     `json:"child_codex_turn_id"`
		HostTurn  string     `json:"codex_turn_id"`
		Carried   payloadRef `json:"carried_payload"`
		Requester struct {
			Type string `json:"type"`
			Cell string `json:"runtime_cell_id"`
		} `json:"requester"`
		Invocation payloadRef `json:"invocation_payload"`
		Result     payloadRef `json:"result_payload"`
		Runtime    payloadRef `json:"runtime_payload"`
	} `json:"payload"`
}

const readLimit = 32 << 20

func sourceHash(source string) [32]byte {
	if first, rest, ok := strings.Cut(source, "\n"); ok && strings.HasPrefix(strings.TrimSpace(first), "// @exec:") {
		source = rest
	}
	return sha256.Sum256([]byte(strings.TrimSpace(source)))
}

func readPayload(directory string, ref payloadRef, value any) error {
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

func (b *bundle) update(directory string) error {
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
	r := bufio.NewReader(io.LimitReader(f, readLimit+1))
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
		if read > readLimit {
			return errors.New("native trace read limit exceeded")
		}
		var envelope struct {
			Version int    `json:"schema_version"`
			Seq     uint64 `json:"seq"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return err
		}
		// Version 2 adds trace events without changing the cell/tool evidence
		// consumed here. Keep sequence validation across unconsumed events.
		if (envelope.Version != 1 && envelope.Version != 2) || envelope.Seq != b.seq+1 {
			return &traceFormatError{envelope.Version, envelope.Seq, b.seq + 1}
		}
		var event event
		if err := json.Unmarshal(line, &event); err != nil {
			return err
		}
		if err := b.event(directory, event); err != nil {
			return err
		}
		b.offset += int64(len(line))
		b.seq = event.Seq
	}
}

func (b *bundle) event(directory string, event event) error {
	p := event.Payload
	key := event.Thread + "\x00" + p.Cell
	switch p.Type {
	case "code_cell_started":
		if len(b.cells) >= 4096 || b.cells[key] != nil {
			return errors.New("native trace cell inventory unavailable")
		}
		b.cells[key] = &Cell{thread: event.Thread, callID: p.Call, source: sourceHash(p.Source)}
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
		if err := readPayload(directory, p.Invocation, &invocation); err != nil {
			return err
		}
		tool := &tool{CallID: p.Tool, Tool: invocation.Name, input: invocation.Payload.Input}
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
			tool.command = commandanalysis.Command{Command: args.Command, Workdir: args.Workdir, Shell: args.Shell}
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
		if err := readPayload(directory, p.Result, &result); err != nil {
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
		if err := readPayload(directory, p.Runtime, &result); err != nil {
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

// ReadCell returns a copy; concurrent requests never share mutable outcomes.
// Missing/corrupt/bounded-out evidence is unavailable, never a successful call.
func (t *Reader) ReadCell(thread, callID, source string) *Cell {
	if t == nil || thread == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var found *Cell
	for _, b := range t.readBundles() {
		for _, cell := range b.cells {
			if cell.thread != thread || cell.callID != callID || cell.source != sourceHash(source) {
				continue
			}
			if found != nil {
				return nil
			}
			copy := *cell
			copy.tools = nil
			for _, tool := range cell.tools {
				clone := *tool
				if tool.ExitCode != nil {
					clone.ExitCode = new(*tool.ExitCode)
				}
				clone.environmentID = append(jsontext.Value(nil), tool.environmentID...)
				copy.tools = append(copy.tools, &clone)
			}
			found = &copy
		}
	}
	return found
}

// readBundles refreshes the existing bounded inventory with the trace lock held.
func (t *Reader) readBundles() []*bundle {
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
		t.bundles = make(map[string]*bundle)
	}
	var bundles []*bundle
	bytes := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "trace-") {
			continue
		}
		b := t.bundles[entry.Name()]
		if b == nil {
			b = &bundle{cells: make(map[string]*Cell), calls: make(map[string]*tool)}
			t.bundles[entry.Name()] = b
		}
		b.err = b.update(filepath.Join(t.directory, entry.Name()))
		if formatErr, ok := errors.AsType[*traceFormatError](b.err); ok && !t.reported && t.notice != nil {
			t.reported = true
			t.notice(formatErr.Error())
		}
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

// CommandTimeout reads the original input by exact native thread/call identity.
// Missing or ambiguous input has no timeout label, including after fresh resume.
func (t *Reader) CommandTimeout(thread, call string) (uint64, bool) {
	if t == nil || thread == "" || call == "" {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var found *tool
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
