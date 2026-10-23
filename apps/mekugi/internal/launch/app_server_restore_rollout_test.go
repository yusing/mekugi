package launch

import (
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeTestRollout(t *testing.T, id string, records ...map[string]any) string {
	t.Helper()
	var out bytes.Buffer
	for _, record := range append([]map[string]any{{"type": "session_meta", "payload": map[string]any{"id": id}}}, records...) {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(line, '\n'))
	}
	path := filepath.Join(t.TempDir(), "rollout-"+id+".jsonl")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func rolloutRecord(at int, kind string, payload map[string]any) map[string]any {
	return map[string]any{"timestamp": time.Unix(1000+int64(at), 0).UTC().Format(time.RFC3339Nano), "type": kind, "payload": payload}
}

func rolloutCompleted(at int, turn, id string) map[string]any {
	return rolloutRecord(at, "event_msg", map[string]any{"type": "item_completed", "turn_id": turn, "item": map[string]any{"id": id}})
}

func rolloutFailedCell(at int, call, message string) []map[string]any {
	return []map[string]any{
		rolloutRecord(at, "response_item", map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": call, "input": "broken <<"}),
		rolloutRecord(at, "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": call, "output": []any{
			map[string]any{"type": "input_text", "text": "Script failed\nWall time 0.0 seconds\nOutput:\n"},
			map[string]any{"type": "input_text", "text": "Script error:\n" + message},
		}}),
	}
}

// Host history omits exec-cell failures; retained item anchors preserve placement.
func TestAppServerRestoreRolloutFailures(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.Startup.Thread = "main"
	records := []map[string]any{rolloutRecord(1, "event_msg", map[string]any{"type": "task_started", "turn_id": "turn"}), rolloutCompleted(2, "turn", "command")}
	records = append(records, rolloutFailedCell(3, "cell", "SyntaxError: Unexpected token '<<'")...)
	records = append(records, rolloutCompleted(4, "turn", "answer"))
	root := binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd, Path: writeTestRollout(t, "main", records...), Turns: []binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{
		{ID: "command", Type: "commandExecution", Command: "go vet ./...", ExitCode: new(0)},
		{ID: "answer", Type: "agentMessage", Text: "All reviewed."},
	}}}}
	if err := u.restoreContent(root); err != nil {
		t.Fatal(err)
	}

	if u.restoring != nil || len(u.attached.source.Entries) != 3 {
		t.Fatalf("incomplete restored history: %+v", u.attached.source.Entries)
	}
	failure := u.attached.source.Entries[1].Source
	if failure.Kind != "error" || failure.ErrorDetail != "exec script failed: SyntaxError: Unexpected token '<<'" || u.attached.source.Entries[2].Source.Text != "All reviewed." {
		t.Fatalf("lost failure detail or anchor: %+v", u.attached.source.Entries)
	}
}

func TestAppServerResumeRestoresActiveTurnBeforeSupplements(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Thread, u.Startup.Thread = "", "saved"

	appServerTestRequest(t, u, "1", "thread/resume")
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"saved","turns":[{"id":"active","status":"inProgress","items":[{"type":"userMessage","id":"q","content":[{"type":"text","text":"Still running"}]}]}]}}}`)
	if u.Turn != "active" || !u.attached.session.Agent("/root").Responding || len(u.attached.source.Entries) != 1 {
		t.Fatalf("resume lost the active turn or its history: turn=%q entries=%+v", u.Turn, u.attached.source.Entries)
	}
	// Attach only the activity collector.
	activity := sessioncontrol.NewAudience(nil)
	setTestAppServerIntegration(u, &observationFixture{activity: activity})
	activity.Attach("saved")
	activity.Collect(activitypresentation.Supplement{Thread: "saved", Source: "late", Kind: "error", Text: "exec script failed: late"})
	applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, activity)
	var texts []string
	for _, entry := range u.attached.source.Entries {
		texts = append(texts, entry.Source.Text)
	}
	if !slices.Equal(texts, []string{"Still running", "exec script failed: late"}) {
		t.Fatalf("Main order = %q", texts)
	}
}
