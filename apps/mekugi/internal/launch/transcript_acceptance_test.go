package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/contracts/observation"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestSessionUIReplayTranscriptAttachmentQuestionSeek(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	attached := filepath.Join(cwd, "review.txt")
	if err := os.WriteFile(attached, []byte("private attachment body"), 0600); err != nil {
		t.Fatal(err)
	}
	draft := conversationmodel.Input{Text: "Review the attachment", Files: []composer.Token{{Path: attached}}}
	draft.SnapshotFiles(cwd)
	content, err := json.Marshal(conversationbinding.Input(draft.Draft))
	if err != nil {
		t.Fatal(err)
	}
	path := replayTestWrite(t, cwd, "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+1000, replayTestEpoch+1000, map[string]any{"type": "UserMessage", "id": "input", "content": jsontext.Value(content)}),
		replayTestItem("root", "turn", replayTestEpoch+2000, replayTestEpoch+3000, map[string]any{"type": "Reasoning", "id": "reason", "summary_text": []string{"Public inspection summary"}}),
		replayTestItem("root", "turn", replayTestEpoch+4000, replayTestEpoch+4000, map[string]any{
			"type": "AgentMessage", "id": "question", "delivery": "async", "content": []map[string]string{{"type": "Text", "text": "Who receives it?"}},
			"questions": []map[string]any{{"title": "Who receives it?", "options": []string{"Customers", "Internal"}}},
		}),
		replayTestItem("root", "turn", replayTestEpoch+5000, replayTestEpoch+5000, map[string]any{
			"type": "UserMessage", "id": "reply", "content": []map[string]string{{"type": "text", "text": lifecycleReply(t, "question", 0, "Who receives it?", "Customers")}},
		}),
		replayTestItem("root", "turn", replayTestEpoch+6000, replayTestEpoch+6000, map[string]any{"type": "AgentMessage", "id": "final", "phase": "final_answer", "content": []map[string]string{{"type": "Text", "text": "Review delivered"}}}),
		replayTestRecord("event_msg", replayTestEpoch+7000, map[string]any{"type": "task_complete", "turn_id": "turn"}),
	)
	// Playback must use the submitted receipt even after the source disappears.
	if err := os.Remove(attached); err != nil {
		t.Fatal(err)
	}
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	source.Cwd = cwd
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.seek(4 * time.Second); err != nil {
		t.Fatal(err)
	}
	if p.ui.conversation.Questions.Count(p.Source.Thread) != 1 {
		t.Fatal("recorded async question did not become visible")
	}
	frame := replayPlaybackTestPaint(t, p, 120, 40)
	for _, want := range []string{"Review the attachment", "Attached review.txt", "Public inspection summary", "Who receives it?"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing %q in replay frame:\n%s", want, frame)
		}
	}
	oldUI := p.ui
	if err := p.seek(time.Second); err != nil {
		t.Fatal(err)
	}
	if p.ui == oldUI || p.ui.conversation.Questions.Count(p.Source.Thread) != 0 || !p.At.Equal(source.Start.Add(time.Second)) {
		t.Fatal("seek retained future transcript/question state or lost recorded clock")
	}
	for _, entry := range p.ui.source.Entries {
		if entry.Source.Kind == "reasoning" || entry.Source.Kind == "question" || entry.Source.Kind == "question_reply" || entry.Source.Text == "Review delivered" {
			t.Fatalf("future transcript survived seek: %+v", entry.Source)
		}
	}
	if err := p.seek(p.Until); err != nil {
		t.Fatal(err)
	}
	frame = ansi.Strip(strings.Join(p.ui.testActivity(p.ui.view).RenderFeed(120, 80).Lines, "\n"))
	if p.ui.conversation.Questions.Count(p.Source.Thread) != 0 || !strings.Contains(frame, "Customers") || !strings.Contains(frame, "Review delivered") || strings.Contains(frame, "private attachment body") {
		t.Fatalf("replayed reply/final or attachment privacy lost:\n%s", frame)
	}
	receipts, replies := 0, 0
	for _, entry := range p.ui.source.Entries {
		switch entry.Source.Kind {
		case "attachments":
			receipts++
		case "question_reply":
			replies++
		case "reasoning":
			if entry.Source.Native == nil || entry.Source.Native.Thought != time.Second {
				t.Fatalf("reasoning duration lost recorded item clock: %+v", entry.Source)
			}
		}
	}
	if receipts != 1 || replies != 1 {
		t.Fatalf("replay duplicated receipts/replies or constructed live transport: receipts=%d replies=%d", receipts, replies)
	}
}

func TestSessionUIReplayTranscriptMessageDeltaRetiresProviderThinking(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	source := &playback.Source{Thread: "root", Start: start, End: start.Add(3 * time.Second), Threads: map[string]string{"root": "/root"}, Events: []playback.Event{
		{At: start, Kind: observation.TurnStarted, ThreadID: "root", TurnID: "turn"},
		{At: start.Add(time.Second), Kind: observation.ProviderStarted, ThreadID: "root", TurnID: "turn"},
		{At: start.Add(2 * time.Second), Kind: observation.AgentDelta, ThreadID: "root", TurnID: "turn", ItemID: "answer", Item: observation.Item{ID: "answer", Type: "agentMessage"}, Delta: "Arrived without an item start"},
	}}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.advance(time.Second); err != nil {
		t.Fatal(err)
	}
	if frame := replayPlaybackTestPaint(t, p, 120, 28); !strings.Contains(frame, "Thinking") {
		t.Fatalf("provider start did not present pending thinking:\n%s", frame)
	}
	if err := p.advance(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	frame := replayPlaybackTestPaint(t, p, 120, 28)
	if !strings.Contains(frame, "Arrived without an item start") || strings.Contains(frame, "Thinking") {
		t.Fatalf("direct message delta did not replace pending thinking:\n%s", frame)
	}
}
