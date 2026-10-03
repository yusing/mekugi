//go:build unix

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// One real billed prompt through the pinned authenticated runtime. The second
// launch resumes in a fresh OS/UI process and sends no additional model prompt.
func TestNativeRuntimeJournalClaudePTYLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for real Claude Journal PTY acceptance")
	}
	nativeAcceptanceSettingsUnchanged(t)
	version, err := exec.Command("claude", "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "2.1.288 (Claude Code)" {
		t.Fatal("native acceptance requires installed Claude 2.1.288")
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal("run make test-claude before native Journal PTY acceptance")
	}
	packageData, err := os.ReadFile("../claude/bridge/node_modules/@anthropic-ai/claude-agent-sdk/package.json")
	var sdk struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(packageData, &sdk) != nil || sdk.Version != "0.3.288" {
		t.Fatal("native acceptance requires built SDK 0.3.288")
	}
	workspace, state := t.TempDir(), t.TempDir()
	first := startNativeJournalClaudePTY(t, workspace, state, bridge, "", "first")
	first.await("native Journal ready", func(s string) bool { return strings.Contains(s, "Ready") })
	operations := []map[string]any{
		{"op": "add", "kind": "context", "title": "JPT_CONTEXT", "body": "Preserve JPT_MEMORY_CEDAR"},
		{"op": "add", "kind": "task", "title": "JPT_TASK_ACTIVE", "state": "working"},
	}
	for i := range 40 {
		operations = append(operations, map[string]any{"op": "log", "p": "/2", "text": fmt.Sprintf("JPT_NOTE_%03d retained fact", i)})
	}
	args, err := json.Marshal(map[string]any{"journal": operations})
	if err != nil {
		t.Fatal(err)
	}
	first.paste("This is an authorized isolated native companion acceptance. Invoke mcp__mekugi__journal_batch exactly once with these exact JSON arguments: " + string(args) + ". Native ToolSearch is permitted only if needed to discover this deferred MCP tool. Do not invoke journal_read or any other tool, command, skill, or subagent. Do not mark the task done or retry tools. Then reply only JPT_NATIVE_ANSWER. The authored task intentionally remains working.")
	// Preserve the intentionally working task without dispatching another turn
	// while testing navigation. Enter the draft before completion: the native
	// completion frame may already display the countdown instead of Ready.
	first.keys("JPT_UNSENT_DRAFT")
	first.await("native completion with user draft cancelling continuation", func(s string) bool {
		return strings.Contains(s, "Ready") && strings.Contains(s, "JPT_NATIVE_ANSWER") && strings.Contains(s, "0/1 done") &&
			strings.Contains(s, "JPT_UNSENT_DRAFT") && strings.Contains(s, "Journal continuation cancelled")
	})
	first.evidence("main-plan")
	first.keys("\x02" + "5")
	first.await("shared Journal with retained working task", func(s string) bool {
		pane := nativeJournalPTYPane(s)
		return strings.Contains(pane, "JPT_CONTEXT") && strings.Contains(pane, "JPT_TASK_ACTIVE") && strings.Contains(pane, "◐") && strings.Contains(s, "0/1 done")
	})
	first.evidence("journal-wide")
	// Oversized task children are initially fitted/collapsed. Select the task
	// and explicitly expand it, rather than depending on automatic fit policy.
	first.keys("gj\x1b[C")
	first.await("expanded native Journal notes", func(s string) bool { return strings.Contains(nativeJournalPTYPane(s), "JPT_NOTE_000") })
	mainBefore := nativeJournalPTYMain(first.screen.String())
	first.keys("\x1b[6~\x1b[6~")
	first.await("independent Journal page down", func(s string) bool {
		pane := nativeJournalPTYPane(s)
		return strings.Contains(pane, "JPT_NOTE_") && !strings.Contains(pane, "JPT_NOTE_000")
	})
	if nativeJournalPTYMain(first.screen.String()) != mainBefore {
		t.Fatal("Journal scrolling changed Main viewport")
	}
	first.evidence("journal-scrolled")
	first.resize(72, 22)
	first.await("narrow resized Journal retains selected viewport", func(s string) bool {
		return strings.HasPrefix(s, "┌ 5 Journal") && !strings.Contains(s, "┌ 1 Main") && strings.Contains(nativeJournalPTYPane(s), "JPT_NOTE_")
	})
	first.evidence("journal-narrow")
	first.resize(120, 30)
	first.await("wide resized Journal", func(s string) bool {
		return strings.Contains(s, "┌ 1 Main") && strings.Contains(s, "┌ 5 Journal") && strings.Contains(nativeJournalPTYPane(s), "JPT_NOTE_")
	})
	first.evidence("journal-resized-wide")
	first.keys("\x02" + "1" + "\x03") // Clear the unsent draft before /quit.
	a := first.stopJournal()
	assertNativeJournalPTYState(t, a)
	if !nativeJournalPTYExecutionMatches(a) {
		t.Fatalf("native journal execution incomplete or unexpected tools: %+v", a)
	}
	second := startNativeJournalClaudePTY(t, workspace, state, bridge, a.Session, "resume")
	if second.cmd.Process.Pid == first.cmd.Process.Pid {
		t.Fatal("resume reused the UI process")
	}
	second.await("fresh native history before any next prompt", func(s string) bool { return strings.Contains(s, "Ready") && strings.Contains(s, "JPT_NATIVE_ANSWER") })
	second.keys("\x02" + "5")
	second.await("fresh UI restored Journal and plan before next prompt", func(s string) bool {
		pane := nativeJournalPTYPane(s)
		return strings.Contains(pane, "JPT_CONTEXT") && strings.Contains(pane, "JPT_TASK_ACTIVE") && strings.Contains(pane, "◐") && strings.Contains(s, "0/1 done")
	})
	second.evidence("restored-before-prompt")
	second.keys("gj\x1b[C")
	second.await("fresh UI restored retained notes", func(s string) bool { return strings.Contains(nativeJournalPTYPane(s), "JPT_NOTE_000") })
	second.evidence("restored-notes")
	b := second.stopJournal()
	assertNativeJournalPTYState(t, b)
	if b.Session != a.Session || b.Done != 0 || len(b.Tools) != 0 || len(b.Results) != 0 || b.Failed || !b.History {
		t.Fatalf("fresh UI replayed tools or lost native history: %+v", b)
	}
	if b.Sequence != a.Sequence || b.Receipts != a.Receipts || b.TaskPath != a.TaskPath {
		t.Fatalf("resume mutated authored journal or receipt identity: first=%+v resume=%+v", a, b)
	}
	t.Logf("CLI 2.1.288 / SDK 0.3.288: one real prompt; UI PIDs %d and %d; exact native journal batch persisted %d notes; user draft cancelled continuation; fresh UI restored working task before any next prompt; zero resumed tools", first.cmd.Process.Pid, second.cmd.Process.Pid, a.Notes)
}

type nativeJournalPTYResult struct {
	Session                             string
	Tools                               map[string]string
	Results                             map[string]int
	Done, Receipts, Notes, Items        int
	Sequence                            uint64
	Failed, History, Answer, NotesValid bool
	TaskPath, TaskState, ContextBody    string
}

func assertNativeJournalPTYState(t *testing.T, r nativeJournalPTYResult) {
	t.Helper()
	if r.Session == "" || r.TaskPath != "/2" || r.TaskState != "working" || r.ContextBody != "Preserve JPT_MEMORY_CEDAR" || r.Notes != 40 || !r.NotesValid || r.Items != 42 || r.Receipts != 1 {
		t.Fatalf("durable native Journal missing or fabricated task completion: %+v", r)
	}
}

func TestNativeRuntimeJournalClaudePTYProcess(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" || os.Getenv("MEKUGI_NATIVE_JOURNAL_PTY_CHILD") != "1" {
		t.Skip("private subprocess entry point for native Journal PTY acceptance")
	}
	shutdown, stopSignals := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(shutdown, 3*time.Minute)
	defer cancel()
	workspace := os.Getenv("MEKUGI_NATIVE_JOURNAL_PTY_WORKSPACE")
	service, err := StartObservationService(ctx, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	client, err := claude.Start(ctx, "node", os.Getenv("MEKUGI_NATIVE_JOURNAL_PTY_BRIDGE"), claude.Config{
		Cwd: workspace, Executable: executable, Resume: os.Getenv("MEKUGI_NATIVE_JOURNAL_PTY_RESUME"), Model: "haiku",
		Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
	forwardCtx, stopForwarding := context.WithCancel(ctx)
	defer stopForwarding()
	result := nativeJournalPTYResult{Tools: make(map[string]string), Results: make(map[string]int)}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer close(observed.events)
		for e := range client.Events() {
			if e.Kind == "session" {
				result.Session = e.SessionID
			}
			if e.Historical && e.Kind == "message" && strings.TrimSpace(e.Text) == "JPT_NATIVE_ANSWER" {
				result.History = true
			}
			if !e.Historical {
				switch e.Kind {
				case "tool":
					result.Tools[e.ID] = e.Role
				case "tool_result":
					result.Results[e.ID]++
					result.Failed = result.Failed || e.Failed
				case "done":
					result.Done++
					result.Failed = result.Failed || e.Failed
				case "message":
					result.Answer = result.Answer || strings.TrimSpace(e.Text) == "JPT_NATIVE_ANSWER"
				case "error":
					result.Failed = true
				}
			}
			select {
			case observed.events <- e:
			case <-forwardCtx.Done():
				return
			}
		}
	}()
	err = RunNativeSession(ctx, observed, "Claude Code", workspace, os.Stdin, os.Stdout, service)
	stopForwarding()
	closeErr := client.Close()
	cancel()
	<-joined
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	binding := ObservationBinding{Runtime: "claude", Workspace: workspace, Session: result.Session}
	journal, found, err := readThreadJournal(service.owner.store, workspace, observationThread(binding))
	if err != nil || !found {
		t.Fatalf("retained Journal unavailable: %v", err)
	}
	result.Sequence = journal.Sequence
	result.Receipts = len(journal.Receipts)
	result.Items = len(journal.Items)
	notes := make(map[string]bool)
	for _, item := range journal.Items {
		switch item.Title {
		case "JPT_CONTEXT":
			result.ContextBody = item.Body
		case "JPT_TASK_ACTIVE":
			result.TaskPath, result.TaskState = item.Path, item.State
		}
		if item.Kind == "note" && strings.HasPrefix(item.Title, "JPT_NOTE_") {
			result.Notes++
			if strings.HasPrefix(item.Path, "/2/") {
				notes[item.Title] = true
			}
		}
	}
	result.NotesValid = len(notes) == 40
	for i := range 40 {
		result.NotesValid = result.NotesValid && notes[fmt.Sprintf("JPT_NOTE_%03d retained fact", i)]
	}
	data, err := json.Marshal(&result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("MEKUGI_NATIVE_JOURNAL_PTY_RESULT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

type nativeJournalClaudePTY struct {
	*nativeClaudePTY
	permissionSelected bool
	permissionAnswered bool
}

func startNativeJournalClaudePTY(t *testing.T, workspace, state, bridge, resume, phase string) *nativeJournalClaudePTY {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	result := filepath.Join(t.TempDir(), "native-journal-pty-result.json")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeRuntimeJournalClaudePTYProcess$", "-test.timeout=190s")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 8 * time.Second
	cmd.Env = append(os.Environ(), "MEKUGI_NATIVE_JOURNAL_PTY_CHILD=1", "MEKUGI_NATIVE_JOURNAL_PTY_WORKSPACE="+workspace,
		"MEKUGI_NATIVE_JOURNAL_PTY_BRIDGE="+bridge, "MEKUGI_NATIVE_JOURNAL_PTY_RESUME="+resume, "MEKUGI_NATIVE_JOURNAL_PTY_RESULT="+result, "XDG_STATE_HOME="+state)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 30})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &nativeClaudePTY{t: t, ctx: ctx, cancel: cancel, cmd: cmd, master: master, frames: make(chan []byte, 128), done: make(chan error, 1), screen: vt.NewEmulator(120, 30), resultPath: result, workspace: workspace, phase: phase}
	go func() { _, _ = io.Copy(master, s.screen) }()
	go func() { s.done <- cmd.Wait() }()
	go func() {
		defer close(s.frames)
		buf := make([]byte, 65536)
		var pending []byte
		for {
			n, err := master.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				end := bytes.Index(pending, []byte("\x1b[?2026l"))
				if end < 0 {
					break
				}
				end += len("\x1b[?2026l")
				select {
				case s.frames <- bytes.Clone(pending[:end]):
				case <-ctx.Done():
					return
				}
				pending = pending[end:]
			}
			if err != nil {
				if len(pending) > 0 {
					select {
					case s.frames <- bytes.Clone(pending):
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		master.Close()
		s.screen.Close()
		if !s.stopped {
			<-s.done
		}
	})
	return &nativeJournalClaudePTY{nativeClaudePTY: s}
}
func (s *nativeJournalClaudePTY) stopJournal() nativeJournalPTYResult {
	s.t.Helper()
	s.keys("\x02" + "1")
	s.paste("/quit")
	select {
	case err := <-s.done:
		s.stopped = true
		if err != nil {
			for frame := range s.frames {
				_, _ = s.screen.Write(frame)
			}
			s.t.Fatalf("native Journal UI process failed: %v\n%s", err, s.screen.String())
		}
	case <-s.ctx.Done():
		s.t.Fatal("native Journal UI did not exit")
	}
	var result nativeJournalPTYResult
	data, err := os.ReadFile(s.resultPath)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		s.t.Fatal(err)
	}
	return result
}
func (s *nativeJournalClaudePTY) evidence(name string) {
	s.t.Helper()
	dir := os.Getenv("MEKUGI_NATIVE_CLAUDE_JOURNAL_PTY_EVIDENCE")
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		s.t.Fatal("native Journal PTY evidence directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.t.Fatal(err)
	}
	frame := strings.ReplaceAll(s.screen.String(), s.workspace, "<workspace>")
	if err := os.WriteFile(filepath.Join(dir, "native-runtime-journal-pty-"+s.phase+"-"+name+".txt"), []byte(frame+"\n"), 0600); err != nil {
		s.t.Fatal(err)
	}
}
func nativeJournalPTYPane(frame string) string { return nativeJournalPTYColumns(frame, true) }
func nativeJournalPTYMain(frame string) string { return nativeJournalPTYColumns(frame, false) }
func nativeJournalPTYColumns(frame string, journal bool) string {
	lines := strings.Split(frame, "\n")
	start := strings.Index(lines[0], "┌ 5 Journal")
	if start < 0 {
		return ""
	}
	col := len([]rune(lines[0][:start]))
	for i, line := range lines[:len(lines)-2] {
		cells := []rune(line)
		if journal {
			lines[i] = string(cells[min(col, len(cells)):])
		} else {
			lines[i] = string(cells[:min(col, len(cells))])
		}
	}
	return strings.Join(lines[:len(lines)-2], "\n")
}

// The ordinary user permission decision is sent through the shared terminal UI,
// not through an SDK permission-mode override or an observer-generated result.
func (s *nativeJournalClaudePTY) await(label string, match func(string) bool) {
	s.t.Helper()
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	for {
		select {
		case frame, ok := <-s.frames:
			if !ok {
				s.t.Fatalf("Journal PTY closed while awaiting %s\n%s", label, s.screen.String())
			}
			if _, err := s.screen.Write(frame); err != nil {
				s.t.Fatal(err)
			}
			visible := s.screen.String()
			// Long native tool names do not fit the dock header, and long
			// input scrolls the record header offscreen. Match the actual
			// waiting dock instead, then confirm its selected choice in a
			// subsequent rendered frame before submitting it.
			if !s.permissionSelected && nativeJournalPTYPermissionDock(visible, false) {
				s.keys("\x02" + "1" + "1")
				s.permissionSelected = true
			} else if s.permissionSelected && !s.permissionAnswered && nativeJournalPTYPermissionDock(visible, true) {
				s.keys("\r")
				s.permissionAnswered = true
			}
			if match(visible) {
				return
			}
		case <-timer.C:
			s.evidence("timeout")
			s.t.Fatalf("missing %s\n%s", label, s.screen.String())
		case <-s.ctx.Done():
			s.evidence("timeout")
			s.t.Fatalf("Journal PTY timed out awaiting %s\n%s", label, s.screen.String())
		}
	}
}

// Validate the exact navigation and pane-extraction assertions offline. This
// synthetic harness check does not replace the opt-in native acceptance above.
func TestNativeRuntimeJournalPTYNavigationHarness(t *testing.T) {
	u, s, b, c := nativeRuntimeJournalFixture(t)
	u.view.clock = u.clock
	ops := []map[string]any{{"op": "add", "kind": "context", "title": "JPT_CONTEXT"}, {"op": "add", "kind": "task", "title": "JPT_TASK_ACTIVE", "state": "working"}}
	for i := range 40 {
		ops = append(ops, map[string]any{"op": "log", "p": "/2", "text": fmt.Sprintf("JPT_NOTE_%03d retained fact", i)})
	}
	data, err := json.Marshal(map[string]any{"journal": ops})
	if err != nil {
		t.Fatal(err)
	}
	input := string(data)
	runtimeJournalReceipt(t, s, b, c, "batch", "journal_batch", input)
	runtimeJournalInvoke(t, s, c, "batch", "journal_batch", input)
	u.runtimeJournalPending()
	runtimeKeys(t, u, "\x02"+"5")
	frame := runtimeFrame(t, u, 120, 30)
	if !strings.Contains(nativeJournalPTYPane(frame), "JPT_TASK_ACTIVE") || !strings.Contains(frame, "0/1 done") {
		t.Fatalf("missing Journal/plan:\n%s", frame)
	}
	runtimeKeys(t, u, "gj\x1b[C")
	frame = runtimeFrame(t, u, 120, 30)
	if !strings.Contains(nativeJournalPTYPane(frame), "JPT_NOTE_000") {
		t.Fatalf("expansion did not show first note:\n%s", frame)
	}
	main := nativeJournalPTYMain(frame)
	runtimeKeys(t, u, "\x1b[6~\x1b[6~")
	frame = runtimeFrame(t, u, 120, 30)
	if strings.Contains(nativeJournalPTYPane(frame), "JPT_NOTE_000") || !strings.Contains(nativeJournalPTYPane(frame), "JPT_NOTE_") || nativeJournalPTYMain(frame) != main {
		t.Fatalf("Journal page down or Main isolation failed:\n%s", frame)
	}
	frame = runtimeFrame(t, u, 72, 22)
	if !strings.HasPrefix(frame, "┌ 5 Journal") || !strings.Contains(nativeJournalPTYPane(frame), "JPT_NOTE_") {
		t.Fatalf("narrow Journal assertion failed:\n%s", frame)
	}
	frame = runtimeFrame(t, u, 120, 30)
	if !strings.Contains(frame, "┌ 1 Main") || !strings.Contains(nativeJournalPTYPane(frame), "JPT_NOTE_") {
		t.Fatalf("wide resize assertion failed:\n%s", frame)
	}
}

// Match the active question dock, not a transcript record or optional header.
func nativeJournalPTYPermissionDock(frame string, allow bool) bool {
	choice := "› 2. Deny"
	if allow {
		choice = "› 1. Allow once"
	}
	return strings.Contains(frame, "? 1 of 1") && strings.Contains(frame, "waiting") && strings.Contains(frame, "answering") && strings.Contains(frame, choice)
}

func TestNativeRuntimeJournalPTYPermissionHarness(t *testing.T) {
	u, f := runtimeTestUI(t)
	u.view.clock = u.clock
	// Reproduce the paid frame: a long MCP name omits the dock header,
	// and a long description pushes its transcript record offscreen.
	description := strings.Repeat(`{"op":"log","p":"/2","text":"retained fact"},`, 40)
	if err := u.runtimeEvent(session.Event{Kind: "prompt", Prompt: &session.Prompt{ID: "journal-permission", Tool: "mcp__mekugi__journal_batch", Description: description}}); err != nil {
		t.Fatal(err)
	}
	frame := runtimeFrame(t, u, 120, 30)
	if strings.Contains(frame, "Permission") || strings.Contains(frame, "journal_batch") {
		t.Fatalf("fixture did not reproduce missing optional tool header:\n%s", frame)
	}
	if !nativeJournalPTYPermissionDock(frame, false) {
		t.Fatalf("waiting default-Deny dock not detected:\n%s", frame)
	}
	runtimeKeys(t, u, "\x02"+"1"+"1")
	if len(f.decisions) != 0 {
		t.Fatal("selection submitted permission before rendered confirmation")
	}
	frame = runtimeFrame(t, u, 120, 30)
	if !nativeJournalPTYPermissionDock(frame, true) {
		t.Fatalf("terminal keys did not select explicit Allow once:\n%s", frame)
	}
	runtimeKeys(t, u, "\r")
	if len(f.decisions) != 1 || f.decisions[0].ID != "journal-permission" || !f.decisions[0].Allow || len(f.sent) != 0 {
		t.Fatalf("explicit terminal Allow once did not reach native client: decisions=%+v model sends=%v", f.decisions, f.sent)
	}
	if u.questions.active != nil || !u.questions.calls[0].resolved || u.questions.calls[0].questions[0].outcome != "answered" {
		t.Fatal("native permission dock remained pending after Allow once")
	}
}

// Deferred MCP discovery is a native runtime call, not a journal mutation.
// Every distinct native call still needs its terminal result, and exactly one
// journal mutation is permitted irrespective of the number of discovery calls.
func nativeJournalPTYExecutionMatches(r nativeJournalPTYResult) bool {
	if r.Done != 1 || len(r.Results) != len(r.Tools) || r.Failed || !r.Answer {
		return false
	}
	batches := 0
	for id, tool := range r.Tools {
		if id == "" || r.Results[id] != 1 {
			return false
		}
		switch tool {
		case "mcp__mekugi__journal_batch":
			batches++
		case "ToolSearch":
		default:
			return false
		}
	}
	return batches == 1
}

func TestNativeRuntimeJournalPTYExecutionHarness(t *testing.T) {
	cases := []struct {
		name    string
		tools   map[string]string
		results map[string]int
		valid   bool
	}{
		{"eager MCP", map[string]string{"batch": "mcp__mekugi__journal_batch"}, map[string]int{"batch": 1}, true},
		{"deferred MCP", map[string]string{"search": "ToolSearch", "batch": "mcp__mekugi__journal_batch"}, map[string]int{"search": 1, "batch": 1}, true},
		{"discovery calls", map[string]string{"search1": "ToolSearch", "search2": "ToolSearch", "batch": "mcp__mekugi__journal_batch"}, map[string]int{"search1": 1, "search2": 1, "batch": 1}, true},
		{"missing batch", map[string]string{"search": "ToolSearch"}, map[string]int{"search": 1}, false},
		{"duplicate batch", map[string]string{"batch1": "mcp__mekugi__journal_batch", "batch2": "mcp__mekugi__journal_batch"}, map[string]int{"batch1": 1, "batch2": 1}, false},
		{"extra read", map[string]string{"batch": "mcp__mekugi__journal_batch", "read": "mcp__mekugi__journal_read"}, map[string]int{"batch": 1, "read": 1}, false},
		{"extra native effect", map[string]string{"batch": "mcp__mekugi__journal_batch", "write": "Write"}, map[string]int{"batch": 1, "write": 1}, false},
		{"missing discovery result", map[string]string{"search": "ToolSearch", "batch": "mcp__mekugi__journal_batch"}, map[string]int{"batch": 1}, false},
		{"duplicate result", map[string]string{"batch": "mcp__mekugi__journal_batch"}, map[string]int{"batch": 2}, false},
		{"duplicate discovery hides missing journal result", map[string]string{"search": "ToolSearch", "batch": "mcp__mekugi__journal_batch"}, map[string]int{"search": 2}, false},
		{"duplicate discovery alongside journal result", map[string]string{"search": "ToolSearch", "batch": "mcp__mekugi__journal_batch"}, map[string]int{"search": 2, "batch": 1}, false},
		{"unmatched result replaces missing journal result", map[string]string{"search": "ToolSearch", "batch": "mcp__mekugi__journal_batch"}, map[string]int{"search": 1, "unknown": 1}, false},
		{"extra unmatched result", map[string]string{"batch": "mcp__mekugi__journal_batch"}, map[string]int{"batch": 1, "unknown": 1}, false},
		{"empty identity", map[string]string{"": "mcp__mekugi__journal_batch"}, map[string]int{"": 1}, false},
		{"no calls", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := nativeJournalPTYResult{Tools: tc.tools, Results: tc.results, Done: 1, Answer: true}
			if got := nativeJournalPTYExecutionMatches(r); got != tc.valid {
				t.Fatalf("execution match=%v want %v: %+v", got, tc.valid, r)
			}
		})
	}
	for _, r := range []nativeJournalPTYResult{
		{Tools: map[string]string{"batch": "mcp__mekugi__journal_batch"}, Results: map[string]int{"batch": 1}, Done: 1, Answer: true, Failed: true},
		{Tools: map[string]string{"batch": "mcp__mekugi__journal_batch"}, Results: map[string]int{"batch": 1}, Answer: true},
		{Tools: map[string]string{"batch": "mcp__mekugi__journal_batch"}, Results: map[string]int{"batch": 1}, Done: 1},
	} {
		if nativeJournalPTYExecutionMatches(r) {
			t.Fatalf("incomplete host outcome accepted: %+v", r)
		}
	}
}
