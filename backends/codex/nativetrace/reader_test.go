package nativetrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatNoticeOnce(t *testing.T) {
	for _, tc := range []struct{ name, line, want string }{
		{"unsupported", `{"schema_version":3,"seq":1,"thread_id":"thread","payload":{"type":"code_cell_started","requester":"changed-in-v3"}}`, "version 3"},
		{"discontinuous", `{"schema_version":2,"seq":2,"thread_id":"thread","payload":{"type":"code_cell_started","runtime_cell_id":"cell","model_visible_call_id":"call","source_js":"source"}}`, "version 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"trace-a", "trace-b"} {
				dir := filepath.Join(root, name)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "trace.jsonl"), []byte(tc.line+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var notices []string
			reader := NewWithNotice(root, func(message string) { notices = append(notices, message) })
			for range 2 {
				if cell := reader.ReadCell("thread", "call", "source"); cell != nil {
					t.Fatal("invalid bundle was accepted")
				}
				reader.CommandTimeout("thread", "call")
			}
			if len(notices) != 1 || !strings.Contains(notices[0], tc.want) {
				t.Fatalf("notices = %q", notices)
			}
		})
	}
}

func TestReadToolRequiresAdmittedIdentity(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "trace-native")
	if err := os.MkdirAll(filepath.Join(directory, "payloads"), 0700); err != nil {
		t.Fatal(err)
	}
	invocation := `{"tool_name":"apply_patch","tool_namespace":"functions","payload":{"type":"custom","input":"actual patch"}}`
	trace := `{"schema_version":2,"seq":1,"thread_id":"thread","codex_turn_id":"turn","payload":{"type":"code_cell_started","runtime_cell_id":"cell","model_visible_call_id":"call","source_js":"source"}}` + "\n" +
		`{"schema_version":2,"seq":2,"thread_id":"thread","codex_turn_id":"turn","payload":{"type":"tool_call_started","tool_call_id":"item","requester":{"type":"code_cell","runtime_cell_id":"cell"},"invocation_payload":{"path":"payloads/input.json"}}}` + "\n"
	for name, content := range map[string]string{"trace.jsonl": trace, "payloads/input.json": invocation} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reader := NewWithNotice(root, nil)
	identity := []string{"thread", "turn", "item", "call", "source"}
	for mismatch := -1; mismatch < len(identity); mismatch++ {
		args := append([]string(nil), identity...)
		if mismatch >= 0 {
			args[mismatch] = "other"
		}
		call, found := reader.ReadTool(args[0], args[1], args[2], args[3], args[4])
		if found != (mismatch == -1) {
			t.Fatalf("identity %v accepted=%t", args, found)
		}
		if found && (call.ID != "item" || call.Input != "actual patch" || call.Tool != "apply_patch") {
			t.Fatalf("actual invocation changed: %+v", call)
		}
	}
}
