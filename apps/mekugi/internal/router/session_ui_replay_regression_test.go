package router

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/diffrender"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/charmbracelet/x/ansi"
)

func TestSessionUIReplayRetainedCollaborationDelivery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	records := []map[string]any{replayTestMeta("root"), replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"})}
	for i, tool := range []string{"spawn_agent", "send_input", "followup_task", "send_message", "send_message"} {
		status, text := "completed", tool+" body"
		if i == 4 {
			status, text = "failed", "undelivered message"
		}
		records = append(records, replayTestItem("root", "turn", replayTestEpoch+int64(i*1000+100), replayTestEpoch+int64(i*1000+900), map[string]any{
			"type": "CollabAgentToolCall", "id": fmt.Sprint(i), "tool": tool, "status": status,
			"sender_thread_id": "root", "receiver_thread_ids": []string{"child"}, "prompt": text,
			"model": "child-model", "reasoning_effort": "high",
		}))
	}
	records = append(records, replayTestRecord("event_msg", replayTestEpoch+6000, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	path := replayTestWrite(t, dir, "rollout-root.jsonl", records...)
	replayTestWrite(t, dir, "rollout-child.jsonl", replayTestMeta("child"))
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	want := map[string]string{"0\x00child": "spawn_agent body", "1\x00child": "send_input body", "2\x00child": "followup_task body", "3\x00child": "send_message body"}
	for pass := range 2 {
		if pass > 0 {
			if err := p.seek(0); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.advance(p.Until); err != nil {
			t.Fatal(err)
		}
		got := make(map[string]string)
		for _, entry := range p.ui.childSource.Entries {
			e := entry.Source
			if e.Assignment == nil && e.Message == nil {
				continue
			}
			if _, duplicate := got[e.Native.Item]; duplicate {
				t.Fatalf("duplicate collaboration identity %q", e.Native.Item)
			}
			if e.Assignment != nil {
				got[e.Native.Item] = e.Assignment.Text
			} else if e.Message != nil {
				got[e.Native.Item] = e.Message.Text
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pass %d collaboration delivery = %#v, want %#v", pass, got, want)
		}
		child := p.ui.session.agent(p.ui.session.paths["child"])
		if child == nil || child.Settings.Model != "child-model" || child.Settings.Effort != "high" {
			t.Fatalf("spawn settings lost: %+v", child)
		}
		p.ui.testActivity(p.ui.agents).displayThread(child.Name)
		frame := replayPlaybackTestPaint(t, p, 140, 45)
		for _, text := range want {
			if !strings.Contains(frame, text) {
				t.Fatalf("pass %d rendered collaboration hides %q:\n%s", pass, text, frame)
			}
		}
		if strings.Contains(frame, "undelivered message") {
			t.Fatal("failed collaboration claimed delivery")
		}
	}
}

func TestSessionUIReplayMailboxWaitUsesRecordedRoster(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := replayTestWrite(t, dir, "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+100, replayTestEpoch+100, map[string]any{"type": "SubAgentActivity", "id": "a", "agent_thread_id": "a", "agent_path": "/root/alpha", "kind": "spawn"}),
		replayTestItem("root", "turn", replayTestEpoch+200, replayTestEpoch+200, map[string]any{"type": "SubAgentActivity", "id": "b", "agent_thread_id": "b", "agent_path": "/root/beta", "kind": "spawn"}),
		replayTestItem("root", "turn", replayTestEpoch+2000, replayTestEpoch+4000, map[string]any{"type": "CollabAgentToolCall", "id": "wait", "tool": "wait", "status": "completed", "sender_thread_id": "root"}),
		replayTestRecord("event_msg", replayTestEpoch+6000, map[string]any{"type": "task_complete", "turn_id": "turn"}),
	)
	for i, id := range []string{"a", "b"} {
		replayTestWrite(t, dir, "rollout-2023-"+id+".jsonl", replayTestMeta(id),
			replayTestRecord("event_msg", replayTestEpoch+int64(1000+i*2000), map[string]any{"type": "task_started", "turn_id": "work"}),
			replayTestRecord("event_msg", replayTestEpoch+5000, map[string]any{"type": "task_complete", "turn_id": "work"}),
		)
	}
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), root, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	for pass := range 2 {
		if pass > 0 {
			if err := p.seek(0); err != nil {
				t.Fatal(err)
			}
		}
		for _, at := range []time.Duration{2 * time.Second, 3 * time.Second, 4 * time.Second} {
			if err := p.advance(at); err != nil {
				t.Fatal(err)
			}
			want := "Waiting for agent · alpha"
			if at == 4*time.Second {
				want = "Finished waiting · alpha"
			}
			if len(p.ui.source.Entries) != 1 || p.ui.source.Entries[0].Source.Text != want {
				t.Fatalf("pass %d at %s changed recorded targets: %+v", pass, at, p.ui.source.Entries)
			}
			if rows := p.ui.testActivity(p.ui.view).RenderFeed(120, 30).Lines; len(rows) != 0 {
				t.Fatalf("wait entered ordinary transcript: %q", rows)
			}
			frame := replayPlaybackTestPaint(t, p, 140, 40)
			if !strings.Contains(frame, want) {
				t.Fatalf("pass %d at %s roster hides %q:\n%s", pass, at, want, frame)
			}
			if p.ui.session.waitStore != nil {
				t.Fatal("offline wait opened live retention")
			}
		}
	}
}

func TestSessionUIReplayRetainsQuestionAnswerWait(t *testing.T) {
	t.Parallel()
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
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 4)
	t.Cleanup(p.close)
	for _, step := range []struct {
		at        time.Duration
		questions int
	}{{time.Second, 0}, {2 * time.Second, 1}, {7999 * time.Millisecond, 1}, {8 * time.Second, 0}} {
		if err := p.advance(step.at); err != nil {
			t.Fatal(err)
		}
		if p.ui.questionCount() != step.questions || p.ui.questions.Questions != &p.ui.conversation().Questions {
			t.Fatalf("at %s: questions=%d want=%d", step.at, p.ui.questionCount(), step.questions)
		}
		replayPlaybackTestPaint(t, p, 120, 28)
	}
	if p.ui.client != nil || p.ui.requests != nil || len(p.ui.session.metadata) != 0 {
		t.Fatal("playback constructed live question transport")
	}
}

func TestSessionUIReplayReasoningUsesRecordedItemStart(t *testing.T) {
	t.Parallel()
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+1000, replayTestEpoch+3000, map[string]any{
			"type": "Reasoning", "id": "r", "summary_text": []string{"**Checking**\n\n" + strings.Repeat("Public summary chunk. ", 20)},
		}),
		replayTestRecord("event_msg", replayTestEpoch+4000, map[string]any{"type": "task_complete", "turn_id": "turn"}),
	)
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	for _, step := range []struct {
		at   time.Duration
		want string
	}{{time.Second, "Working"}, {1500 * time.Millisecond, "Public summary"}, {3 * time.Second, "Checking for 2s"}} {
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
	t.Parallel()
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
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"running-agent": "running", "initializing-agent": "pendingInit", "interrupted-agent": "interrupted",
		"shutdown-agent": "shutdown", "missing-agent": "notFound", "completed-agent": "completed", "errored-agent": "errored",
	}
	var normalized map[string]observation.AgentState
	for _, event := range source.Events {
		if event.Kind == "item/completed" {
			normalized = event.Item.AgentsStates
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
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.advance(p.Until); err != nil {
		t.Fatal(err)
	}
	replayPlaybackTestPaint(t, p, 120, 28)
	var finalWait string
	for _, entry := range p.ui.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Item == "wait" && entry.Source.Native.Wait != nil {
			finalWait = entry.Source.Text
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
	t.Parallel()
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
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if source.Thread != "fork" || source.Threads["fork"] != "/root" || source.Items != 1 || len(source.Missing) != 0 || !source.Start.Equal(time.UnixMilli(replayTestEpoch+4000)) {
		t.Fatalf("direct fork identity/bounds lost: %+v", source)
	}
	counts := make(map[string]int)
	for _, event := range source.Events {
		if event.ThreadID != "fork" || event.TurnID != "fork-turn" || event.ItemID == "ancestor-answer" {
			t.Fatalf("copied ancestor event misattributed to fork: %+v", event)
		}
		if event.Kind == "turn/started" || event.Kind == "turn/completed" || event.Kind == "item/completed" {
			counts[string(event.Kind)]++
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{"turn/started": 1, "turn/completed": 1, "item/completed": 1}) {
		t.Fatalf("direct fork lifecycle duplicated: %v", counts)
	}
}

func TestSessionUIReplayItemlessForkTurnUsesSettingsBoundary(t *testing.T) {
	t.Parallel()
	path := replayTestWrite(t, t.TempDir(), "fork.jsonl", replayTestMeta("fork"), replayTestMeta("ancestor"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "inherited"}),
		replayTestRecord("event_msg", replayTestEpoch+1000, map[string]any{"type": "task_complete", "turn_id": "inherited"}),
		replayTestRecord("event_msg", replayTestEpoch+2000, map[string]any{"type": "thread_settings_applied", "thread_id": "fork"}),
		replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_started", "turn_id": "local"}),
		replayTestRecord("event_msg", replayTestEpoch+7000, map[string]any{"type": "turn_aborted", "turn_id": "local"}),
	)
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Events) != 2 || source.End.Sub(source.Start) != 4*time.Second {
		t.Fatalf("itemless local interval lost: %+v", source)
	}
	for _, event := range source.Events {
		if event.ThreadID != "fork" || event.TurnID != "local" {
			t.Fatalf("incorrect turn owner: %+v", event)
		}
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.advance(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if p.ui.Turn != "local" {
		t.Fatal("local wait interval was omitted")
	}
	if err := p.advance(p.Until); err != nil {
		t.Fatal(err)
	}
	if p.ui.Turn != "" || p.ui.sessionPresentation().Status.Text != "Interrupted" {
		t.Fatalf("local abort lost: turn=%q status=%q", p.ui.Turn, p.ui.sessionPresentation().Status.Text)
	}
}

func TestSessionUIReplayEndPaintSettlesQueuedCommandOutput(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	var lines []string
	for j := range 200 {
		lines = append(lines, fmt.Sprintf("captured-line-%03d", j))
	}
	output := strings.Join(lines, "\n") + "\n"
	command := observation.Item{ID: "command", Type: "commandExecution", Command: "slow-command", Status: "inProgress"}
	completed := command
	completed.Status, completed.ExitCode, completed.DurationMS, completed.AggregatedOutput = "completed", new(0), new(int64(1000)), &output
	end := start.Add(time.Second)
	source := &playback.Source{Thread: "root", Cwd: "/workspace/replay", Start: start, End: end, Threads: map[string]string{"root": "/root"}, Events: []playback.Event{
		{Event: observation.Event{At: start, Kind: "item/started", ThreadID: "root", TurnID: "turn", ItemID: command.ID, Item: command}},
		{Event: observation.Event{At: start.Add(500 * time.Millisecond), Kind: "item/commandExecution/outputDelta", ThreadID: "root", TurnID: "turn", ItemID: command.ID, Delta: output}},
		{Event: observation.Event{At: end, Kind: "item/completed", ThreadID: "root", TurnID: "turn", ItemID: command.ID, Item: completed}},
	}}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	p.ui.view.Painter.Theme, p.ui.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
	if err := p.advance(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	replayPlaybackTestPaint(t, p, 120, 28)
	run := p.ui.session.commands[[3]string{"root", "turn", command.ID}]
	if run == nil || !run.output.Pending() {
		t.Fatal("regression requires painted output with a remaining backlog")
	}
	if err := p.advance(p.Until); err != nil {
		t.Fatal(err)
	}
	p.Paused = true
	first := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if len(p.ui.session.commands) != 0 {
		t.Fatal("session-end frame leaves recorded completed command running")
	}
	var tail []string
	for _, entry := range p.ui.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Item == command.ID {
			tail = entry.Source.OutputTail
			if entry.Source.Native.Phase != "item/completed" || !entry.Source.Native.CommandEnded.Equal(end) {
				t.Fatalf("final command evidence unsettled: %+v", entry.Source.Native)
			}
		}
	}
	if len(tail) == 0 || tail[len(tail)-1] != "captured-line-199" || !strings.Contains(first, "captured-line-199") {
		t.Fatalf("session-end repaint lost final retained output: tail=%q\n%s", tail, first)
	}
	second := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if first != second || !p.At.Equal(end) || p.Position != p.Until {
		t.Fatal("paused final frame continues rolling output or changes recorded clock")
	}
}
