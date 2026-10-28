package launch

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
	b, err := json.Marshal(map[string]any{"schema_version": 2, "seq": f.seq, "thread_id": thread, "payload": payload})
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
