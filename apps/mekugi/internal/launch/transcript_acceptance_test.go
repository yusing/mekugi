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
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
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

func TestAppServerTranscriptOlderHistoryPreservesLiveReasoning(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("latest", "Latest historical answer", 109000)}, "nextCursor": "older"})
	at := time.Unix(200, 0)
	u.clock = func() time.Time { return at }
	u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
	u.testActivity(u.shell.Agents).setDisplayFilter(false, "/root/worker")
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "live", "status": "inProgress"}})
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "live", "itemId": "shared", "delta": "Live prefix "})
	var liveSeq uint64
	for _, entry := range u.attached.childSource.Entries {
		if n := entry.Source.Native; n != nil && n.Thread == "child" && n.Turn == "live" && n.Item == "shared" {
			liveSeq = entry.Source.Seq
		}
	}
	if liveSeq == 0 {
		t.Fatal("live reasoning was not presented")
	}
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{map[string]any{
		"turnId": "t", "completedAtMs": 101000, "item": map[string]any{"id": "shared", "type": "reasoning", "summary": []string{"Older public summary"}, "content": []string{"PRIVATE"}},
	}}})
	if !u.attached.session.Agent("/root/worker").Responding {
		t.Fatal("historical page reset the active child lifecycle")
	}
	at = at.Add(2 * time.Second)
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "live", "itemId": "shared", "delta": "continues after paging"})
	reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "live", "item": map[string]any{"id": "shared", "type": "reasoning", "summary": []string{}}})
	older, latest, live := -1, -1, -1
	for i, entry := range u.attached.childSource.Entries {
		n := entry.Source.Native
		if n == nil || n.Thread != "child" {
			continue
		}
		switch {
		case n.Turn == "t" && n.Item == "shared":
			older = i
			if entry.Source.Text != "Older public summary" || entry.Source.Observed.UnixMilli() != 101000 || n.Live {
				t.Fatalf("historical identity/time was not retained: %+v", entry.Source)
			}
		case n.Item == "latest":
			latest = i
		case n.Turn == "live" && n.Item == "shared":
			live = i
			if entry.Source.Seq != liveSeq || entry.Source.Text != "Live prefix continues after paging" || !n.Live || n.Phase != "item/completed" || n.Thought != 2*time.Second || !entry.Source.Completed.Equal(at) {
				t.Fatalf("paging mutated live reasoning correlation: %+v", entry.Source)
			}
		}
	}
	if older < 0 || latest <= older || live <= latest {
		t.Fatalf("history/live placement changed: older=%d latest=%d live=%d", older, latest, live)
	}
	frame := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RenderFeed(100, 60).Lines, "\n"))
	if !strings.Contains(frame, "Older public summary") || !strings.Contains(frame, "Live prefix continues after paging") || strings.Contains(frame, "PRIVATE") {
		t.Fatalf("rendered transcript lost history/live separation:\n%s", frame)
	}
}

func TestSessionUIReplayTranscriptMessageDeltaRetiresProviderThinking(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	source := &playback.Source{Thread: "root", Start: start, End: start.Add(3 * time.Second), Threads: map[string]string{"root": "/root"}, Events: []playback.Event{
		{Event: observation.Event{At: start, Kind: observation.TurnStarted, ThreadID: "root", TurnID: "turn"}},
		{Event: observation.Event{At: start.Add(time.Second), Kind: observation.ProviderStarted, ThreadID: "root", TurnID: "turn"}},
		{Event: observation.Event{At: start.Add(2 * time.Second), Kind: observation.AgentDelta, ThreadID: "root", TurnID: "turn", ItemID: "answer", Item: observation.Item{ID: "answer", Type: "agentMessage"}, Delta: "Arrived without an item start"}},
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
