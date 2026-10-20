package executionobservation

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	trace := nativetrace.New(f.root)
	f.start("author", "1", "outer", "source")
	f.tool("author", "1", "patch", "apply_patch", "patch input")
	f.result("author", "patch", "completed", map[string]any{})
	f.tool("author", "1", "command", "exec_command", `{"cmd":"printf changed > target"}`)
	f.result("author", "command", "completed", map[string]any{"exit_code": 0})
	if cell := trace.ReadCell("author", "outer", "source"); cell == nil || !cell.Pending() {
		t.Fatal("unfinished cell was confirmed")
	}
	f.end("author", "1")
	cell := trace.ReadCell("author", "outer", "// @exec: {\"yield_time_ms\": 1000}\nsource")
	patch, ok := cell.Patch("patch input", 0)
	if !ok || patch.Status != "completed" || patch.CallID != "patch" {
		t.Fatalf("patch receipt = %+v, %t", patch, ok)
	}
	results, ok := cell.Commands([]commandanalysis.Command{{Command: "printf changed > target"}}, "/work")
	if !ok || len(results) != 1 || results[0].ExitCode == nil || *results[0].ExitCode != 0 {
		t.Fatalf("command receipts = %+v, %t", results, ok)
	}
	if _, ok := cell.Patch("skipped patch", 0); ok {
		t.Fatal("unexecuted literal patch confirmed")
	}
	if trace.ReadCell("reviewer", "outer", "source") != nil || trace.ReadCell("author", "outer", "different source") != nil {
		t.Fatal("cross-thread or mismatched source receipt")
	}
	// A new reader can recover native facts; persisted change records need not
	// retain this disposable trace after finalization.
	reopened := nativetrace.New(f.root)
	if _, ok := reopened.ReadCell("author", "outer", "source").Patch("patch input", 0); !ok {
		t.Fatal("could not reread host evidence")
	}
}

func TestNativeTraceFailuresAndYieldAreNotSuccess(t *testing.T) {
	f := newNativeTraceFixture(t)
	trace := nativetrace.New(f.root)
	f.start("author", "1", "outer", "source")
	f.tool("author", "1", "bad-patch", "apply_patch", "bad input")
	f.result("author", "bad-patch", "failed", nil)
	f.tool("author", "1", "nonzero", "exec_command", `{"cmd":"exit 7"}`)
	f.result("author", "nonzero", "completed", map[string]any{"exit_code": 7})
	f.tool("author", "1", "yielded", "exec_command", `{"cmd":"sleep 1"}`)
	f.result("author", "yielded", "completed", map[string]any{"session_id": 123})
	f.end("author", "1")
	cell := trace.ReadCell("author", "outer", "source")
	if !cell.Pending() {
		t.Fatal("yielded process treated as finished")
	}
	if patch, ok := cell.Patch("bad input", 0); !ok || patch.Status != "failed" {
		t.Fatal("caught patch failure lost")
	}
	commands := []commandanalysis.Command{{Command: "exit 7"}, {Command: "sleep 1"}}
	if _, ok := cell.Commands(commands, "/work"); ok {
		t.Fatal("pending process confirmed")
	}
	f.event("author", map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "yielded", "status": "completed", "runtime_payload": f.ref(map[string]any{"exit_code": 0})})
	cell = trace.ReadCell("author", "outer", "source")
	results, ok := cell.Commands(commands, "/work")
	if !ok || cell.Pending() || len(results) != 2 || results[0].Status != "failed" || *results[0].ExitCode != 7 || *results[1].ExitCode != 0 {
		t.Fatalf("terminal results = %+v, %t", results, ok)
	}
}

func TestNativeTraceTerminalPollIsThreadScoped(t *testing.T) {
	f := newNativeTraceFixture(t)
	for _, thread := range []string{"author", "other"} {
		f.start(thread, "cell", "outer", "source")
		f.tool(thread, "cell", "command", "exec_command", `{"cmd":"sleep 1"}`)
		f.result(thread, "command", "completed", map[string]any{"session_id": 7})
	}
	f.tool("author", "cell", "poll", "write_stdin", `{"session_id":7,"chars":""}`)
	f.result("author", "poll", "completed", map[string]any{"exit_code": 0})
	f.end("author", "cell")
	f.end("other", "cell")
	trace := nativetrace.New(f.root)
	if cell := trace.ReadCell("author", "outer", "source"); cell == nil || cell.Pending() {
		t.Fatal("terminal poll did not resolve its host process")
	}
	if cell := trace.ReadCell("other", "outer", "source"); cell == nil || !cell.Pending() {
		t.Fatal("terminal poll resolved another thread's process")
	}
}

func TestNativeTracePatchOccurrencesKeepDistinctOutcomes(t *testing.T) {
	f := newNativeTraceFixture(t)
	f.start("author", "runtime", "outer", "source")
	f.tool("author", "runtime", "first", applyPatchToolName, "same input")
	f.result("author", "first", "completed", map[string]any{})
	f.tool("author", "runtime", "second", applyPatchToolName, "same input")
	f.result("author", "second", "failed", nil)
	f.tool("author", "runtime", "third", applyPatchToolName, "same input")
	f.end("author", "runtime")
	cell := nativetrace.New(f.root).ReadCell("author", "outer", "source")
	for index, want := range []binding.ToolResult{{CallID: "first", Status: "completed"}, {CallID: "second", Status: "failed"}} {
		result, ok := cell.Patch("same input", index)
		if !ok || result.CallID != want.CallID || result.Status != want.Status {
			t.Fatalf("occurrence %d: %+v, %v", index, result, ok)
		}
	}
	for _, index := range []int{-1, 2, 3} {
		if _, ok := cell.Patch("same input", index); ok {
			t.Fatalf("pending or absent occurrence %d confirmed", index)
		}
	}
}

func TestNativeTraceRuntimeExitSurvivesYieldedDispatchResult(t *testing.T) {
	f := newNativeTraceFixture(t)
	f.start("t", "1", "outer", "source")
	f.tool("t", "1", "cmd", "exec_command", `{"cmd":"exit 3"}`)
	f.event("t", map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "cmd", "status": "failed", "runtime_payload": f.ref(map[string]any{"exit_code": 3})})
	f.result("t", "cmd", "completed", map[string]any{"session_id": 123})
	f.end("t", "1")
	trace := nativetrace.New(f.root)
	results, ok := trace.ReadCell("t", "outer", "source").Commands([]commandanalysis.Command{{Command: "exit 3"}}, "/work")
	if !ok || len(results) != 1 || results[0].Status != "failed" || *results[0].ExitCode != 3 {
		t.Fatalf("lost process exit: %+v, %t", results, ok)
	}
}

func TestNativeTraceMissingAndCorruptEvidenceNeverConfirms(t *testing.T) {
	for _, kind := range []string{"missing result", "sequence gap", "payload escape"} {
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
			f.end("t", "1")
			trace := nativetrace.New(f.root)
			if _, ok := trace.ReadCell("t", "outer", "source").Patch("input", 0); ok {
				t.Fatal("ambiguous/missing evidence became success")
			}
		})
	}
}

func TestNativeTraceCommandsMatchAuthoredShellCalls(t *testing.T) {
	const edit = "touch file.txt"
	const diagnostic = "printf status"
	const source = "retained source"
	for _, tc := range []struct {
		name     string
		extra    []string
		authored bool
		want     bool
	}{
		{"authored diagnostic command", []string{diagnostic}, true, true},
		{"unrecorded diagnostic command", []string{diagnostic}, false, false},
		{"extra command", []string{"touch other.txt"}, false, false},
		{"repeated command", []string{edit}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := &mekugiHistory{Script: source, CarrierPayload: `const command = "printf status";`,
				ExecObservation: &changecapture.Observation{Commands: []commandanalysis.Command{{Command: edit}}, CodeMode: true}}
			if tc.authored {
				history.ExecObservation.Commands = append(history.ExecObservation.Commands, commandanalysis.Command{Command: diagnostic})
			}
			f := newNativeTraceFixture(t)
			f.start("t", "1", "cell", source)
			for i, command := range append(tc.extra, edit) {
				id := fmt.Sprint(i)
				f.tool("t", "1", id, "exec_command", string(responses.MustMarshalJSON(map[string]any{"cmd": command})))
				f.result("t", id, "completed", map[string]any{"exit_code": 0})
			}
			f.end("t", "1")
			trace := nativetrace.New(f.root)
			results, ok := trace.ReadCell("t", "cell", source).Commands(history.ExecObservation.Commands, "/work")
			if ok != tc.want || ok && len(results) != 2 {
				t.Fatalf("results=%+v confirmed=%v want=%v", results, ok, tc.want)
			}
		})
	}
}

func TestNativeTraceIncrementalBundlesStayIndependent(t *testing.T) {
	first := newNativeTraceFixture(t)
	first.start("author", "cell", "outer", "source")
	first.tool("author", "cell", "patch", "apply_patch", "input")
	first.result("author", "patch", "completed", nil)
	first.end("author", "cell")
	path := filepath.Join(first.bundle, "trace.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, int64(len(data)-1)); err != nil {
		t.Fatal(err)
	}
	reader := nativetrace.New(first.root)
	before := reader.ReadCell("author", "outer", "source")
	if before == nil || before.Ended() || !before.Pending() {
		t.Fatal("unfinished final record completed a cell")
	}
	if _, confirmed := before.Patch("input", 0); confirmed {
		t.Fatal("unfinished cell confirmed its patch")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, confirmed := reader.ReadCell("author", "outer", "source").Patch("input", 0); !confirmed || before.Ended() {
		t.Fatal("incremental completion was lost or changed an earlier observation")
	}

	second := newNativeTraceFixture(t)
	second.start("other", "cell", "outer", "source")
	second.tool("other", "cell", "patch", "apply_patch", "input")
	second.result("other", "patch", "completed", nil)
	second.end("other", "cell")
	bundle := filepath.Join(first.root, "trace-second")
	if err := os.Rename(second.bundle, bundle); err != nil {
		t.Fatal(err)
	}
	second.bundle = bundle
	if _, confirmed := reader.ReadCell("other", "outer", "source").Patch("input", 0); !confirmed {
		t.Fatal("independent bundle sequence or thread identity was lost")
	}
	second.start("author", "duplicate", "outer", "source")
	if reader.ReadCell("author", "outer", "source") != nil {
		t.Fatal("ambiguous outer call acquired evidence")
	}
	second.seq++
	second.end("author", "duplicate")
	if reader.ReadCell("other", "outer", "source") != nil {
		t.Fatal("corrupt bundle retained usable evidence")
	}
	if _, confirmed := reader.ReadCell("author", "outer", "source").Patch("input", 0); !confirmed {
		t.Fatal("corrupt bundle disabled independent valid evidence")
	}
}

func TestNativeTraceUnusedAgentResultsDoNotExhaustToolEvidence(t *testing.T) {
	f := newNativeTraceFixture(t)
	f.event("author", map[string]any{"type": "codex_turn_started", "codex_turn_id": "turn"})
	carried := f.ref(map[string]any{"child_agent_path": strings.Repeat("a", 1<<20), "status": map[string]any{"completed": nil}})
	for range 65 {
		f.event("author", map[string]any{
			"type": "agent_result_observed", "child_thread_id": "author", "parent_thread_id": "parent",
			"child_codex_turn_id": "turn", "carried_payload": carried,
		})
	}
	f.start("author", "cell", "outer", "source")
	f.tool("author", "cell", "patch", "apply_patch", "input")
	f.result("author", "patch", "completed", nil)
	f.end("author", "cell")
	reader := nativetrace.New(f.root)
	if result, confirmed := reader.ReadCell("author", "outer", "source").Patch("input", 0); !confirmed || result.CallID != "patch" {
		t.Fatal("unused agent results exhausted valid tool evidence")
	}
	f.seq++
	f.event("author", map[string]any{"type": "codex_turn_started", "codex_turn_id": "next"})
	if reader.ReadCell("author", "outer", "source") != nil {
		t.Fatal("ignored agent event bypassed sequence validation")
	}
}

func TestNativeTraceRetainedToolEvidenceRemainsBounded(t *testing.T) {
	f := newNativeTraceFixture(t)
	f.start("author", "cell", "outer", "source")
	invocation := f.ref(map[string]any{
		"tool_name": "apply_patch", "payload": map[string]any{"type": "custom", "input": strings.Repeat("p", 1<<20)},
	})
	for index := range 65 {
		id := fmt.Sprint(index)
		f.event("author", map[string]any{
			"type": "tool_call_started", "tool_call_id": id,
			"requester": map[string]string{"type": "code_cell", "runtime_cell_id": "cell"}, "invocation_payload": invocation,
		})
		f.result("author", id, "completed", nil)
	}
	f.end("author", "cell")
	reader := nativetrace.New(f.root)
	for range 2 {
		if reader.ReadCell("author", "outer", "source") != nil {
			t.Fatal("bounded-out tool evidence became available")
		}
	}
}

func TestNativeTraceReturnedExitCodeDoesNotMutateReader(t *testing.T) {
	f := newNativeTraceFixture(t)
	f.start("author", "cell", "outer", "source")
	f.tool("author", "cell", "command", "exec_command", `{"cmd":"exit 7"}`)
	f.result("author", "command", "completed", map[string]any{"exit_code": 7})
	f.end("author", "cell")
	reader := nativetrace.New(f.root)
	commands := []commandanalysis.Command{{Command: "exit 7"}}
	results, confirmed := reader.ReadCell("author", "outer", "source").Commands(commands, "/work")
	if !confirmed || len(results) != 1 || results[0].ExitCode == nil || *results[0].ExitCode != 7 {
		t.Fatalf("initial command result = %+v, confirmed=%v", results, confirmed)
	}
	*results[0].ExitCode = 0
	results, confirmed = reader.ReadCell("author", "outer", "source").Commands(commands, "/work")
	if !confirmed || len(results) != 1 || results[0].ExitCode == nil || *results[0].ExitCode != 7 {
		t.Fatalf("returned exit mutation changed reader evidence: %s, confirmed=%v", responses.MustMarshalJSON(results), confirmed)
	}
}
