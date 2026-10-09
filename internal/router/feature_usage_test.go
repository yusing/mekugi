package router

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func featureDebugOutput(t *testing.T) *debugOutput {
	t.Helper()
	flags := newRouterFlags(io.Discard)
	*flags.debug = true
	d, err := openDebugOutput(flags)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close(); _ = os.RemoveAll(filepath.Dir(d.paths[0])) })
	return d
}

func readFeatureUsage(t *testing.T, d *debugOutput) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(d.log.Name())
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["event"] != "feature_usage" {
			continue
		}
		if event["schema_version"] != float64(1) {
			t.Fatalf("unexpected event: %v", event)
		}
		events = append(events, event)
	}
	return events
}

func TestFeatureUsageAllowlistAndDisabledLogging(t *testing.T) {
	var disabled featureUsageTrace
	disabled.record("journal", "tool", "mutation", "accepted", "", "")
	d := featureDebugOutput(t)
	trace := featureUsageTrace{debug: d, requestID: "request-1", threadID: "private\ntext", sessionID: strings.Repeat("s", 257)}
	for _, categories := range [][4]string{
		{"private text", "tool", "mutation", "accepted"},
		{"journal", "private text", "mutation", "accepted"},
		{"journal", "tool", "private text", "accepted"},
		{"journal", "tool", "mutation", "private text"},
		{"commentary", "code_mode", "lowering", "prepared"},
		{"commentary", "code_mode", "lowering", "unavailable"},
		{"commentary", "tool_field", "publication", "accepted"},
	} {
		trace.record(categories[0], categories[1], categories[2], categories[3], "", "")
	}
	if got := readFeatureUsage(t, d); len(got) != 0 {
		t.Fatalf("unrecognized categories retained: %v", got)
	}
	trace.record("journal", "tool", "mutation", "accepted", "call-1", "https://private.example/secret")
	got := readFeatureUsage(t, d)
	if len(got) != 1 || got[0]["request_id"] != "request-1" || got[0]["call_id"] != "call-1" {
		t.Fatalf("correlation lost: %v", got)
	}
	for _, key := range []string{"thread_id", "session_id", "message_id"} {
		if _, exists := got[0][key]; exists {
			t.Fatalf("unsafe identity retained: %s", key)
		}
	}
}
