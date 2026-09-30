package router

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestSessionUIReplayRetainsQuestionAnswerWait(t *testing.T) {
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+2000, replayTestEpoch+2000, map[string]any{
			"type": "AgentMessage", "id": "question", "delivery": "async", "content": []map[string]any{{"type": "Text", "text": "Who receives it?"}},
			"questions": []map[string]any{{"title": "Who receives it?", "options": []string{"Customers", "Internal"}}},
		}),
		replayTestItem("root", "turn", replayTestEpoch+8000, replayTestEpoch+8000, map[string]any{
			"type": "UserMessage", "id": "answer", "content": []map[string]any{{"type": "text", "text": lifecycleReply(t, "question", 0, "Who receives it?", "Customers")}},
		}),
		replayTestRecord("event_msg", replayTestEpoch+10000, map[string]any{"type": "task_complete", "turn_id": "turn"}),
	)
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newUIReplayPlayback(t.Context(), source, 4)
	t.Cleanup(p.close)
	var wire bytes.Buffer
	p.ui.client.Input = replayDiscard{Writer: &wire}
	for _, step := range []struct {
		at        time.Duration
		questions int
	}{{time.Second, 0}, {2 * time.Second, 1}, {7999 * time.Millisecond, 1}, {8 * time.Second, 0}} {
		if err := p.advance(step.at); err != nil {
			t.Fatal(err)
		}
		if p.ui.questionCount() != step.questions {
			t.Fatalf("at %s: questions=%d want=%d", step.at, p.ui.questionCount(), step.questions)
		}
		replayPlaybackTestPaint(t, p, 120, 28)
	}
	if wire.Len() != 0 {
		t.Fatalf("playback submitted question answer: %q", wire.String())
	}
}

func TestSessionUIReplayReasoningUsesRecordedItemStart(t *testing.T) {
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+1000, replayTestEpoch+3000, map[string]any{
			"type": "Reasoning", "id": "r", "summary_text": []string{"**Checking**\n\n" + strings.Repeat("Public summary chunk. ", 20)},
		}),
		replayTestRecord("event_msg", replayTestEpoch+4000, map[string]any{"type": "task_complete", "turn_id": "turn"}),
	)
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(p.close)
	for _, step := range []struct {
		at   time.Duration
		want string
	}{{time.Second, "Thinking…"}, {1500 * time.Millisecond, "Public summary"}, {3 * time.Second, "Checking for 2s"}} {
		if err := p.advance(step.at); err != nil {
			t.Fatal(err)
		}
		frame := replayPlaybackTestPaint(t, p, 120, 28)
		if !strings.Contains(frame, step.want) {
			t.Fatalf("reasoning at %s lost recorded start or synthetic streaming; want %q:\n%s", step.at, step.want, frame)
		}
	}
}

func TestSessionUIReplayRustAgentStatusShapesReachFinalWaitDisplay(t *testing.T) {
	states := map[string]any{
		"running-agent": "running", "initializing-agent": "pending_init", "interrupted-agent": "interrupted",
		"shutdown-agent": "shutdown", "missing-agent": "not_found",
		"completed-agent": map[string]any{"completed": "answer text"}, "errored-agent": map[string]any{"errored": "failure message"},
	}
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"),
		replayTestItem("root", "turn", replayTestEpoch+1000, replayTestEpoch+2000, map[string]any{
			"type": "CollabAgentToolCall", "id": "wait", "tool": "wait", "status": "completed",
			"sender_thread_id": "root", "agents_states": states,
		}),
	)
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"running-agent": "running", "initializing-agent": "pendingInit", "interrupted-agent": "interrupted",
		"shutdown-agent": "shutdown", "missing-agent": "notFound", "completed-agent": "completed", "errored-agent": "errored",
	}
	var normalized map[string]appServerAgentState
	for _, event := range source.Events {
		if event.Method == "item/completed" {
			normalized = event.Params.Item.AgentsStates
		}
	}
	if len(normalized) != len(want) {
		t.Fatalf("normalized status count = %d, want %d", len(normalized), len(want))
	}
	for id, status := range want {
		if normalized[id].Status != status {
			t.Errorf("%s normalized status = %q, want %q", id, normalized[id].Status, status)
		}
	}
	p := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.advance(p.until); err != nil {
		t.Fatal(err)
	}
	replayPlaybackTestPaint(t, p, 120, 28)
	var finalWait string
	for _, entry := range p.ui.view.entries {
		if entry.native != nil && entry.native.item == "wait" && entry.native.wait != nil {
			finalWait = entry.native.wait.ProgressText()
		}
	}
	if !strings.Contains(finalWait, "Finished waiting") {
		t.Fatalf("missing final wait presentation: %q", finalWait)
	}
	for id, status := range want {
		if status == "running" {
			status = "Still running"
		}
		if !strings.Contains(finalWait, id+": "+status) {
			t.Errorf("final wait hides observed target state %s=%s: %q", id, status, finalWait)
		}
	}
	if strings.Contains(finalWait, "answer text") || strings.Contains(finalWait, "failure message") {
		t.Fatal("status payload was incorrectly shown as a state label")
	}
}

func TestSessionUIReplayDirectForkSkipsCopiedAncestorEvents(t *testing.T) {
	rootItem := replayTestItem("ancestor", "ancestor-turn", replayTestEpoch+1000, replayTestEpoch+2000,
		map[string]any{"type": "AgentMessage", "id": "ancestor-answer", "content": []map[string]any{{"text": "copied ancestor answer"}}})
	path := replayTestWrite(t, t.TempDir(), "rollout-2023-fork.jsonl",
		replayTestMeta("fork"), replayTestMeta("ancestor"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "ancestor-turn"}), rootItem,
		replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_complete", "turn_id": "ancestor-turn"}),
		replayTestRecord("event_msg", replayTestEpoch+4000, map[string]any{"type": "task_started", "turn_id": "fork-turn"}),
		replayTestItem("fork", "fork-turn", replayTestEpoch+4500, replayTestEpoch+5500, map[string]any{"type": "AgentMessage", "id": "fork-answer", "content": []map[string]any{{"text": "fork answer"}}}),
		replayTestRecord("event_msg", replayTestEpoch+6000, map[string]any{"type": "task_complete", "turn_id": "fork-turn"}),
	)
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if source.Thread != "fork" || source.Threads["fork"] != "/root" || source.Items != 1 || len(source.Missing) != 0 || !source.Start.Equal(time.UnixMilli(replayTestEpoch+4000)) {
		t.Fatalf("direct fork identity/bounds lost: %+v", source)
	}
	counts := make(map[string]int)
	for _, event := range source.Events {
		if event.Params.ThreadID != "fork" || event.Params.TurnID != "fork-turn" || event.Params.ItemID == "ancestor-answer" {
			t.Fatalf("copied ancestor event misattributed to fork: %+v", event)
		}
		if event.Method == "turn/started" || event.Method == "turn/completed" || event.Method == "item/completed" {
			counts[event.Method]++
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{"turn/started": 1, "turn/completed": 1, "item/completed": 1}) {
		t.Fatalf("direct fork lifecycle duplicated: %v", counts)
	}
}

func TestSessionUIReplayItemlessForkTurnUsesSettingsBoundary(t *testing.T) {
	path := replayTestWrite(t, t.TempDir(), "fork.jsonl", replayTestMeta("fork"), replayTestMeta("ancestor"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "inherited"}),
		replayTestRecord("event_msg", replayTestEpoch+1000, map[string]any{"type": "task_complete", "turn_id": "inherited"}),
		replayTestRecord("event_msg", replayTestEpoch+2000, map[string]any{"type": "thread_settings_applied", "thread_id": "fork"}),
		replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_started", "turn_id": "local"}),
		replayTestRecord("event_msg", replayTestEpoch+7000, map[string]any{"type": "turn_aborted", "turn_id": "local"}),
	)
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Events) != 2 || source.End.Sub(source.Start) != 4*time.Second {
		t.Fatalf("itemless local interval lost: %+v", source)
	}
	for _, event := range source.Events {
		if event.Params.ThreadID != "fork" || event.Params.Turn.ID != "local" {
			t.Fatalf("incorrect turn owner: %+v", event)
		}
	}
	p := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.advance(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if p.ui.turn != "local" {
		t.Fatal("local wait interval was omitted")
	}
	if err := p.advance(p.until); err != nil {
		t.Fatal(err)
	}
	if p.ui.turn != "" || p.ui.status != "Interrupted" {
		t.Fatalf("local abort lost: turn=%q status=%q", p.ui.turn, p.ui.status)
	}
}

func TestSessionUIReplayEndPaintSettlesQueuedCommandOutput(t *testing.T) {
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	var lines []string
	for j := range 200 {
		lines = append(lines, fmt.Sprintf("captured-line-%03d", j))
	}
	output := strings.Join(lines, "\n") + "\n"
	command := appServerItem{ID: "command", Type: "commandExecution", Command: "slow-command", Status: "inProgress"}
	completed := command
	completed.Status, completed.ExitCode, completed.DurationMS, completed.AggregatedOutput = "completed", new(0), new(int64(1000)), &output
	end := start.Add(time.Second)
	source := &sessionUIReplay{Thread: "root", Cwd: "/workspace/replay", Start: start, End: end, Threads: map[string]string{"root": "/root"}, Events: []uiReplayEvent{
		{At: start, Method: "item/started", Params: appServerEvent{ThreadID: "root", TurnID: "turn", ItemID: command.ID, Item: command}},
		{At: start.Add(500 * time.Millisecond), Method: "item/commandExecution/outputDelta", Params: appServerEvent{ThreadID: "root", TurnID: "turn", ItemID: command.ID, Delta: output}},
		{At: end, Method: "item/completed", Params: appServerEvent{ThreadID: "root", TurnID: "turn", ItemID: command.ID, Item: completed}},
	}}
	p := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(p.close)
	p.ui.view.painter.Theme, p.ui.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
	if err := p.advance(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	replayPlaybackTestPaint(t, p, 120, 28)
	run := p.ui.session.commands[[3]string{"root", "turn", command.ID}]
	if run == nil || run.dirty || run.output.Pending() == 0 {
		t.Fatal("regression requires painted output with a remaining backlog")
	}
	if err := p.advance(p.until); err != nil {
		t.Fatal(err)
	}
	p.paused = true
	first := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if len(p.ui.session.commands) != 0 {
		t.Fatal("session-end frame leaves recorded completed command running")
	}
	var tail []string
	for _, entry := range p.ui.view.entries {
		if entry.native != nil && entry.native.item == command.ID {
			tail = entry.outputTail
			if entry.native.phase != "item/completed" || !entry.native.commandEnded.Equal(end) {
				t.Fatalf("final command evidence unsettled: %+v", entry.native)
			}
		}
	}
	if len(tail) == 0 || tail[len(tail)-1] != "captured-line-199" || !strings.Contains(first, "captured-line-199") {
		t.Fatalf("session-end repaint lost final retained output: tail=%q\n%s", tail, first)
	}
	second := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if first != second || !p.at.Equal(end) || p.position != p.until {
		t.Fatal("paused final frame continues rolling output or changes recorded clock")
	}
}
