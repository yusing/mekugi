package router

import (
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type nativeTraceFixture struct {
	t            *testing.T
	root, bundle string
	seq, payload int
}

func newNativeTraceFixture(t *testing.T) *nativeTraceFixture {
	t.Helper()
	root := t.TempDir()
	bundle := filepath.Join(root, "trace-test")
	if err := os.MkdirAll(filepath.Join(bundle, "payloads"), 0700); err != nil {
		t.Fatal(err)
	}
	return &nativeTraceFixture{t: t, root: root, bundle: bundle}
}

func (f *nativeTraceFixture) ref(value any) map[string]string {
	f.t.Helper()
	f.payload++
	path := fmt.Sprintf("payloads/%d.json", f.payload)
	b, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bundle, path), b, 0600); err != nil {
		f.t.Fatal(err)
	}
	return map[string]string{"path": path}
}

func (f *nativeTraceFixture) event(thread string, payload map[string]any) {
	f.t.Helper()
	f.seq++
	b, err := json.Marshal(map[string]any{"schema_version": 1, "seq": f.seq, "thread_id": thread, "payload": payload})
	if err != nil {
		f.t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(f.bundle, "trace.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append(b, '\n')); err != nil {
		f.t.Fatal(err)
	}
}

func (f *nativeTraceFixture) start(thread, cell, call, source string) {
	f.event(thread, map[string]any{"type": "code_cell_started", "runtime_cell_id": cell, "model_visible_call_id": call, "source_js": source})
}

func (f *nativeTraceFixture) tool(thread, cell, id, name string, input any) {
	kind, key := "function", "arguments"
	if name == "apply_patch" {
		kind, key = "custom", "input"
	}
	ref := f.ref(map[string]any{"tool_name": name, "tool_namespace": nil, "payload": map[string]any{"type": kind, key: input}})
	f.event(thread, map[string]any{"type": "tool_call_started", "tool_call_id": id, "requester": map[string]string{"type": "code_cell", "runtime_cell_id": cell}, "invocation_payload": ref})
}

func (f *nativeTraceFixture) result(thread, id, status string, value any) {
	f.event(thread, map[string]any{"type": "tool_call_ended", "tool_call_id": id, "status": status, "result_payload": f.ref(map[string]any{"type": "code_mode_response", "value": value})})
}

func (f *nativeTraceFixture) end(thread, cell string) {
	f.event(thread, map[string]any{"type": "code_cell_ended", "runtime_cell_id": cell, "status": "completed"})
}

func TestNativeTraceConfirmsEachToolWithoutPrintedOutput(t *testing.T) {
	f := newNativeTraceFixture(t)
	trace := &nativeToolTrace{directory: f.root}
	f.start("author", "1", "outer", "source")
	f.tool("author", "1", "patch", "apply_patch", "patch input")
	f.result("author", "patch", "completed", map[string]any{})
	f.tool("author", "1", "command", "exec_command", `{"cmd":"printf changed > target"}`)
	f.result("author", "command", "completed", map[string]any{"exit_code": 0})
	if cell := trace.readCell("author", "outer", "source"); cell == nil || !cell.pending() {
		t.Fatal("unfinished cell was confirmed")
	}
	f.end("author", "1")
	cell := trace.readCell("author", "outer", "// @exec: {\"yield_time_ms\": 1000}\nsource")
	patch, ok := cell.patch("patch input")
	if !ok || patch.Status != "completed" || patch.CallID != "patch" {
		t.Fatalf("patch receipt = %+v, %t", patch, ok)
	}
	results, ok := cell.commands([]execCommandInput{{Command: "printf changed > target"}}, "/work")
	if !ok || len(results) != 1 || results[0].ExitCode == nil || *results[0].ExitCode != 0 {
		t.Fatalf("command receipts = %+v, %t", results, ok)
	}
	if _, ok := cell.patch("skipped patch"); ok {
		t.Fatal("unexecuted literal patch confirmed")
	}
	if trace.readCell("reviewer", "outer", "source") != nil || trace.readCell("author", "outer", "different source") != nil {
		t.Fatal("cross-thread or mismatched source receipt")
	}
	// A new reader can recover native facts; persisted change records need not
	// retain this disposable trace after finalization.
	reopened := &nativeToolTrace{directory: f.root}
	if _, ok := reopened.readCell("author", "outer", "source").patch("patch input"); !ok {
		t.Fatal("could not reread host evidence")
	}
}

func TestNativeTraceFailuresAndYieldAreNotSuccess(t *testing.T) {
	f := newNativeTraceFixture(t)
	trace := &nativeToolTrace{directory: f.root}
	f.start("author", "1", "outer", "source")
	f.tool("author", "1", "bad-patch", "apply_patch", "bad input")
	f.result("author", "bad-patch", "failed", nil)
	f.tool("author", "1", "nonzero", "exec_command", `{"cmd":"exit 7"}`)
	f.result("author", "nonzero", "completed", map[string]any{"exit_code": 7})
	f.tool("author", "1", "yielded", "exec_command", `{"cmd":"sleep 1"}`)
	f.result("author", "yielded", "completed", map[string]any{"session_id": 123})
	f.end("author", "1")
	cell := trace.readCell("author", "outer", "source")
	if !cell.pending() {
		t.Fatal("yielded process treated as finished")
	}
	if patch, ok := cell.patch("bad input"); !ok || patch.Status != "failed" {
		t.Fatal("caught patch failure lost")
	}
	commands := []execCommandInput{{Command: "exit 7"}, {Command: "sleep 1"}}
	if _, ok := cell.commands(commands, "/work"); ok {
		t.Fatal("pending process confirmed")
	}
	f.event("author", map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "yielded", "status": "completed", "runtime_payload": f.ref(map[string]any{"exit_code": 0})})
	cell = trace.readCell("author", "outer", "source")
	results, ok := cell.commands(commands, "/work")
	if !ok || cell.pending() || len(results) != 2 || results[0].Status != "failed" || *results[0].ExitCode != 7 || *results[1].ExitCode != 0 {
		t.Fatalf("terminal results = %+v, %t", results, ok)
	}
}

func TestNativeTraceRuntimeExitSurvivesYieldedDispatchResult(t *testing.T) {
	f := newNativeTraceFixture(t)
	f.start("t", "1", "outer", "source")
	f.tool("t", "1", "cmd", "exec_command", `{"cmd":"exit 3"}`)
	f.event("t", map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "cmd", "status": "failed", "runtime_payload": f.ref(map[string]any{"exit_code": 3})})
	f.result("t", "cmd", "completed", map[string]any{"session_id": 123})
	f.end("t", "1")
	trace := &nativeToolTrace{directory: f.root}
	results, ok := trace.readCell("t", "outer", "source").commands([]execCommandInput{{Command: "exit 3"}}, "/work")
	if !ok || len(results) != 1 || results[0].Status != "failed" || *results[0].ExitCode != 3 {
		t.Fatalf("lost process exit: %+v, %t", results, ok)
	}
}

func TestNativeTraceMissingAndCorruptEvidenceNeverConfirms(t *testing.T) {
	for _, kind := range []string{"missing result", "sequence gap", "payload escape", "duplicate input"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeTraceFixture(t)
			f.start("t", "1", "outer", "source")
			if kind == "sequence gap" {
				f.seq++
			}
			f.tool("t", "1", "patch", "apply_patch", "input")
			if kind != "missing result" {
				f.result("t", "patch", "completed", map[string]any{})
			}
			if kind == "payload escape" {
				f.event("t", map[string]any{"type": "tool_call_started", "tool_call_id": "other", "requester": map[string]string{"type": "code_cell", "runtime_cell_id": "1"}, "invocation_payload": map[string]string{"path": "../outside.json"}})
			}
			if kind == "duplicate input" {
				f.tool("t", "1", "other", "apply_patch", "input")
				f.result("t", "other", "completed", map[string]any{})
			}
			f.end("t", "1")
			trace := &nativeToolTrace{directory: f.root}
			if _, ok := trace.readCell("t", "outer", "source").patch("input"); ok {
				t.Fatal("ambiguous/missing evidence became success")
			}
		})
	}
}
