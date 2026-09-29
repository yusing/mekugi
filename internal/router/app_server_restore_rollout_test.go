package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
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

func rolloutAgentMessage(at int, author, recipient, kind, text string) map[string]any {
	return rolloutRecord(at, "response_item", map[string]any{"type": "agent_message", "id": "amsg-" + text, "author": author, "recipient": recipient,
		"content": []any{map[string]any{"type": "input_text", "text": "Message Type: " + kind + "\nTask name: " + recipient + "\nSender: " + author + "\nPayload:\n" + text}}})
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

// Codex's resumed history omits Code Mode cell results and delivered
// inter-agent messages; the rollout retains both. Restoration shows them where
// live Activity did, in both audiences, without the router having seen them.
func TestAppServerRestoreRolloutFailuresAndCommunications(t *testing.T) {
	workspace := t.TempDir()
	u := newAppServerSessionTestUI(t, workspace)
	u.agents = u.shell.agents
	activity := newSubagentActivity()
	u.proxy = &mekugiProxy{activity: activity}
	u.resumeThread = "main"

	rootRecords := []map[string]any{
		rolloutRecord(1, "event_msg", map[string]any{"type": "task_started", "turn_id": "r1"}),
		rolloutCompleted(1, "r1", "question"),
		rolloutCompleted(2, "r1", "cmd1"),
	}
	rootRecords = append(rootRecords, rolloutFailedCell(3, "cell-root", "SyntaxError: Unexpected token '<<'")...)
	rootRecords = append(rootRecords,
		rolloutCompleted(4, "r1", "spawn"),
		rolloutAgentMessage(20, "/root/reviewer", "/root", "MESSAGE", "I found a failure."),
		rolloutAgentMessage(40, "/root/reviewer", "/root", "FINAL_ANSWER", "Review complete."),
		rolloutCompleted(41, "r1", "answer"),
		rolloutRecord(50, "event_msg", map[string]any{"type": "task_started", "turn_id": "r2"}),
	)
	childRecords := []map[string]any{
		// A fork's inherited parent turn precedes the child's own history.
		rolloutRecord(0, "event_msg", map[string]any{"type": "task_started", "turn_id": "r1"}),
		rolloutAgentMessage(0, "/root", "/root", "MESSAGE", "Not addressed to the child."),
		rolloutRecord(5, "event_msg", map[string]any{"type": "task_started", "turn_id": "c1"}),
		rolloutRecord(5, "turn_context", map[string]any{"turn_id": "c1", "model": "gpt-6-luna", "effort": "high"}),
		rolloutAgentMessage(5, "/root", "/root/reviewer", "NEW_TASK", "Inspect the change."),
		rolloutCompleted(10, "c1", "ccmd"),
	}
	childRecords = append(childRecords, rolloutFailedCell(11, "cell-child", "ReferenceError: tools is not defined")...)
	childRecords = append(childRecords,
		rolloutAgentMessage(15, "/root", "/root/reviewer", "MESSAGE", "Keep going."),
		rolloutRecord(39, "response_item", map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Review complete."}}}),
		rolloutCompleted(39, "c1", "canswer"),
		// Delivered after the child's last output: live reads it as new.
		rolloutAgentMessage(45, "/root", "/root/reviewer", "MESSAGE", "One more thing."),
	)
	root := appServerThreadInfo{ID: "main", Cwd: workspace, Path: writeTestRollout(t, "main", rootRecords...), Turns: []appServerHistoryTurn{
		{ID: "r1", Status: "completed", StartedAt: 1001, CompletedAt: 1041, Items: []appServerItem{
			{ID: "question", Type: "userMessage", Content: []byte(`[{"type":"text","text":"Review it"}]`)},
			{ID: "cmd1", Type: "commandExecution", Command: "go vet ./...", ExitCode: new(0)},
			{ID: "spawn", Type: "subAgentActivity", AgentThreadID: "child", AgentPath: "/root/reviewer"},
			{ID: "answer", Type: "agentMessage", Text: "All reviewed."},
		}},
		{ID: "r2", Status: "failed", StartedAt: 1050, Error: &struct {
			Message string `json:"message"`
		}{"stream disconnected"}},
	}}
	if err := u.restorePaneContent(root); err != nil {
		t.Fatal(err)
	}
	if len(u.view.entries) != 0 {
		t.Fatal("Main history rendered before its children were read")
	}
	child := map[string]any{"id": "child", "cwd": workspace, "parentThreadId": "main", "path": writeTestRollout(t, "child", childRecords...),
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/reviewer"}}}}
	restoreContentReply(t, u, 0, map[string]any{"data": []any{child}, "nextCursor": nil})
	restoreContentReply(t, u, 1, map[string]any{"data": []any{}, "nextCursor": nil})
	child["turns"] = []any{map[string]any{"id": "r1", "status": "completed", "items": []any{}}, map[string]any{"id": "c1", "status": "completed", "startedAt": 1005, "completedAt": 1039, "items": []any{
		map[string]any{"id": "ccmd", "type": "commandExecution", "command": "go test ./...", "exitCode": 0},
		map[string]any{"id": "canswer", "type": "agentMessage", "text": "Review complete."},
	}}}
	restoreContentReply(t, u, 2, map[string]any{"thread": child})
	if u.restoring != nil {
		t.Fatal("restoration did not finish")
	}

	summary := func(entry activityPaneEntry) string {
		if entry.Kind == "tool" {
			return entry.Kind // Tool labels have their own tests.
		}
		switch {
		case entry.message != nil:
			return entry.Kind + "|" + entry.message.from + "->" + entry.message.to + "|" + entry.message.text
		case entry.start != nil:
			return entry.Kind + "|" + entry.start.label() + "|" + entry.assignment.text
		}
		return entry.Kind + "|" + strings.SplitN(entry.Text, "\n", 2)[0]
	}
	var main []string
	for _, entry := range u.view.entries {
		main = append(main, entry.Agent+"|"+summary(entry))
	}
	want := []string{
		"You|text|Review it",
		"Main|tool",
		"Main|error|Code Mode script failed: SyntaxError: Unexpected token '<<'",
		"Main|start|`gpt-6-luna` `high`|Inspect the change.",
		"Main|reply|/root->/root/reviewer|Keep going.",
		"/root/reviewer|reply|/root/reviewer->/root|I found a failure.",
		"/root/reviewer|final|Review complete.",
		"Main|text|All reviewed.",
		"Main|reply|/root->/root/reviewer|One more thing.",
		"Main|error|Turn failed: stream disconnected",
	}
	if !slices.Equal(main, want) {
		t.Fatalf("Main history:\n%s\nwant:\n%s", strings.Join(main, "\n"), strings.Join(want, "\n"))
	}
	for _, entry := range u.view.entries {
		if (entry.Kind == "start" || entry.Kind == "reply" || entry.Kind == "final") && entry.activitySeq == 0 {
			t.Fatalf("Main copy does not excerpt its Activity entry: %+v", entry)
		}
	}

	var pane []string
	for _, entry := range u.agents.entries {
		if entry.Agent == "/root/reviewer" {
			pane = append(pane, summary(entry))
		}
	}
	wantPane := []string{
		"start|`gpt-6-luna` `high`|Inspect the change.",
		"tool",
		"error|Code Mode script failed: ReferenceError: tools is not defined",
		"reply|/root->/root/reviewer|Keep going.",
		"reply|/root/reviewer->/root|I found a failure.",
		"final|Review complete.",
		"reply|/root->/root/reviewer|One more thing.",
	}
	if !slices.Equal(pane, wantPane) {
		t.Fatalf("Activity history:\n%s\nwant:\n%s", strings.Join(pane, "\n"), strings.Join(wantPane, "\n"))
	}
	feed := ansi.Strip(strings.Join(u.agents.renderFeed(100, 80).lines, "\n"))
	for _, text := range []string{"Inspect the change.", "Keep going.", "I found a failure.", "ReferenceError"} {
		if !strings.Contains(feed, text) {
			t.Fatalf("Activity missing %q: %s", text, feed)
		}
	}
	if strings.Contains(feed, "Not addressed to the child.") {
		t.Fatal("inherited parent message was attributed to the child")
	}

	// The child's next full-history request carries the restored tasks and
	// its undelivered message again; live Activity must not show them twice,
	// yet a new message repeating an older one's text is still new.
	activity.observe("child", "main", "/root/reviewer", true)
	activity.attachNativePane("main")
	message := func(kind, text string) map[string]any {
		return map[string]any{"type": "agent_message", "author": "/root", "recipient": "/root/reviewer", "content": []any{map[string]any{"type": "input_text",
			"text": "Message Type: " + kind + "\nTask name: /root/reviewer\nSender: /root\nPayload:\n" + text}}}
	}
	input, err := json.Marshal([]any{
		message("NEW_TASK", "Inspect the change."),
		message("MESSAGE", "Keep going."),
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Review complete."}}},
		message("NEW_TASK", "Now check the docs."),
		message("MESSAGE", "One more thing."),
		message("MESSAGE", "Keep going."),
	})
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]jsonv1.RawMessage{"model": []byte(`"gpt-6-luna"`), "input": input}
	activity.collectSubagentStart("child", &parsedResponsesRequest{fields: fields}, "/root/reviewer")
	for _, reply := range prepareSubagentInputEnvelopes(fields, "/root/reviewer").replies {
		activity.collectEvent(activityEvent{thread: "child", source: reply.source, kind: "reply", text: reply.message.text, message: &reply.message})
	}
	var live []string
	for _, entry := range activity.takeNativeActivity("main") {
		live = append(live, summary(entry))
	}
	if want := []string{"assignment|Now check the docs.", "reply|/root->/root/reviewer|Keep going."}; !slices.Equal(live, want) {
		t.Fatalf("live activity after restore = %q, want %q", live, want)
	}
}

// Main's resumed history waits for the roster, but an active turn keeps its
// interrupt target meanwhile, and router observations follow the history.
func TestAppServerResumeDefersHistoryButKeepsActiveTurn(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.thread, u.resumeThread = "", "saved"
	u.agents = newLiveActivityView()
	u.requests["1"] = "thread/resume"
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"saved","turns":[{"id":"active","status":"inProgress","items":[{"type":"userMessage","id":"q","content":[{"type":"text","text":"Still running"}]}]}]}}}`)
	if u.turn != "active" || !u.session.agent("/root").Responding || len(u.view.entries) != 0 {
		t.Fatalf("resume lost the active turn or rendered early: turn=%q entries=%+v", u.turn, u.view.entries)
	}
	// Journal delivery setup is not under test; attach only the collector.
	activity := newSubagentActivity()
	u.proxy = &mekugiProxy{activity: activity}
	activity.attachNativePane("saved")
	activity.collect("saved", "late", "error", "Code Mode script failed: late")
	appServerTestMessage(t, u, `{"id":2,"result":{"data":[],"nextCursor":null}}`)
	if len(u.view.entries) != 0 {
		t.Fatalf("observed activity preceded resumed history: %+v", u.view.entries)
	}
	appServerTestMessage(t, u, `{"id":3,"result":{"data":[],"nextCursor":null}}`)
	appServerTestMessage(t, u, `{"id":4,"result":{"data":[],"nextCursor":null}}`)
	var texts []string
	for _, entry := range u.view.entries {
		texts = append(texts, entry.Text)
	}
	if !slices.Equal(texts, []string{"Still running", "Code Mode script failed: late"}) {
		t.Fatalf("Main order = %q", texts)
	}
}
