package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func replayJournalTestSource(t *testing.T) *sessionUIReplay {
	t.Helper()
	workspace := t.TempDir()
	meta := replayTestMeta("root")
	meta["payload"].(map[string]any)["cwd"] = workspace
	epoch := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local).UnixMilli()
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", meta,
		replayTestRecord("event_msg", epoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", epoch+100, epoch+500, map[string]any{"id": "journal-command", "type": "CommandExecution", "command": []string{"sh", "-c", "journal add 'Checked'"}, "aggregated_output": "command output", "status": "completed", "exit_code": 0}),
		replayTestItem("root", "turn", epoch+600, epoch+1000, map[string]any{"id": "journal-message", "type": "AgentMessage", "text": "Journal: Checked the implementation."}),
		replayTestItem("root", "turn", epoch+1100, epoch+1500, map[string]any{"id": "ordinary-command", "type": "CommandExecution", "command": []string{"sh", "-c", "mchanges --help"}, "aggregated_output": "Change command help", "status": "completed", "exit_code": 0}),
		replayTestRecord("event_msg", epoch+2000, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	r, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Items != 3 || r.End.Sub(r.Start) != 2*time.Second {
		t.Fatalf("replay metadata: %+v", r)
	}
	return r
}

func TestSessionUIReplayJournalPreservesSemanticMessagesAndSeek(t *testing.T) {
	r := replayJournalTestSource(t)
	p := newUIReplayPlayback(t.Context(), r, 1)
	t.Cleanup(p.close)
	for _, position := range []time.Duration{2 * time.Second, 0, 2 * time.Second} {
		if err := p.seek(position); err != nil {
			t.Fatal(err)
		}
		if position == 0 {
			if len(p.ui.view.entries) != 0 {
				t.Fatal("backward seek retained future journal content")
			}
			continue
		}
		var text strings.Builder
		for _, entry := range p.ui.view.entries {
			text.WriteString(entry.Text)
		}
		if !strings.Contains(text.String(), "Checked the implementation") || !strings.Contains(text.String(), "journal add") || !strings.Contains(text.String(), "mchanges --help") {
			t.Fatalf("lost meaningful journal or ordinary command: %s", text.String())
		}
	}
}

func TestUISnapshotSessionUIReplayJournal(t *testing.T) {
	p := newUIReplayPlayback(t.Context(), replayJournalTestSource(t), 1)
	t.Cleanup(p.close)
	p.ui.view.painter.Theme, p.ui.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
	if err := p.seek(p.until); err != nil {
		t.Fatal(err)
	}
	p.paused = true
	uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-journal.txt", replayPlaybackTestPaint(t, p, 120, 28))
}
