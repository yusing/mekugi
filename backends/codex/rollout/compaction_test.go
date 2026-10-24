package rollout

import (
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func TestCompactionReferencesKeepExactBoundaries(t *testing.T) {
	records := []string{
		`{"type":"session_meta","payload":{"id":"root"}}`,
		`{"type":"compacted","payload":{"replacement_history":[{"type":"compaction","encrypted_content":"first"}]}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"foreign","item":{"type":"ContextCompaction","id":"foreign"}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"root","item":{"type":"ContextCompaction","id":"one"}}}`,
		`{"type":"compacted","payload":{"replacement_history":[{"type":"compaction","encrypted_content":"abandoned"}]}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"root","item":{"type":"ContextCompaction","id":"missing"}}}`,
		`{"type":"compacted","payload":{"replacement_history":[{"type":"compaction","encrypted_content":"second"}]}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"root","item":{"type":"ContextCompaction","id":"two"}}}`,
	}
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	var data []byte
	for _, record := range records {
		data = append(data, record...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got := CompactionReferences(t.Context(), "root", path)
	want := map[string]string{"one": "first", "two": "second"}
	if !maps.Equal(got, want) {
		t.Fatalf("boundary references=%v, want=%v", got, want)
	}
	if got := CompactionReferences(t.Context(), "foreign", path); len(got) != 0 {
		t.Fatalf("foreign rollout admitted: %v", got)
	}
}
