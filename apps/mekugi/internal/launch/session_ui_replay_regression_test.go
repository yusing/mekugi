package launch

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
		if p.ui.conversation.Questions.Count(p.Source.Thread) != step.questions || p.ui.conversation != p.conversation.Session(p.ui.session.Scope()) {
			t.Fatalf("at %s: questions=%d want=%d", step.at, p.ui.conversation.Questions.Count(p.Source.Thread), step.questions)
		}
		replayPlaybackTestPaint(t, p, 120, 28)
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
	}{{time.Second, "Working"}, {1500 * time.Millisecond, "Public summary"}, {3 * time.Second, "Thought for 2s • Checking"}} {
		if err := p.advance(step.at); err != nil {
			t.Fatal(err)
		}
		frame := replayPlaybackTestPaint(t, p, 120, 28)
		if !strings.Contains(frame, step.want) {
			t.Fatalf("reasoning at %s lost recorded start or synthetic streaming; want %q:\n%s", step.at, step.want, frame)
		}
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
	if p.ui.Turn != "" || p.ui.presentation.Status.Text != "Interrupted" {
		t.Fatalf("local abort lost: turn=%q status=%q", p.ui.Turn, p.ui.presentation.Status.Text)
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
		{At: start, Kind: "item/started", ThreadID: "root", TurnID: "turn", ItemID: command.ID, Item: command},
		{At: start.Add(500 * time.Millisecond), Kind: "item/commandExecution/outputDelta", ThreadID: "root", TurnID: "turn", ItemID: command.ID, Delta: output},
		{At: end, Kind: "item/completed", ThreadID: "root", TurnID: "turn", ItemID: command.ID, Item: completed},
	}}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	p.ui.view.Painter.Theme, p.ui.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
	if err := p.advance(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	replayPlaybackTestPaint(t, p, 120, 28)
	run := p.ui.session.Commands()[[3]string{"root", "turn", command.ID}]
	if run == nil || !p.ui.streams.Commands[[3]string{"root", "turn", command.ID}].Output.Pending() {
		t.Fatal("regression requires painted output with a remaining backlog")
	}
	if err := p.advance(p.Until); err != nil {
		t.Fatal(err)
	}
	p.Paused = true
	first := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if len(p.ui.session.Commands()) != 0 {
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
