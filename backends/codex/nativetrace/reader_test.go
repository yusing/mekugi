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
