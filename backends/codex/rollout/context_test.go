package rollout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextUsageLatestCompleteRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	header := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"
	snapshot := func(tokens int) string {
		return fmt.Sprintf("{\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"total_tokens\":%d},\"model_context_window\":200000}}}\n", tokens)
	}
	// Oversized earlier content must not prevent bounded tail restoration.
	data := header + strings.Repeat("x", 9<<20) + "\n" + snapshot(90000) + snapshot(10000) + `{"type":"event_msg"`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	usage, known := ReadContextUsage("child", path)
	if !known || usage.Tokens != 10000 || usage.Window != 200000 {
		t.Fatalf("latest context: %+v, known=%t", usage, known)
	}
}

func BenchmarkContextUsageLatestRecord(b *testing.B) {
	path := filepath.Join(b.TempDir(), "rollout.jsonl")
	data := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n" + strings.Repeat("x", 9<<20) + "\n" +
		"{\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"total_tokens\":123}}}}\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if usage, known := ReadContextUsage("child", path); !known || usage.Tokens != 123 {
			b.Fatalf("latest context: %+v, known=%t", usage, known)
		}
	}
}
