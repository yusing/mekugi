package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotUserShellActivity(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed", "restored"} {
		t.Run(state, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			u.clock = func() time.Time { return now }
			view := u.testActivity(u.shell.Transcript)
			view.Clock = u.clock
			view.Painter.Theme = diffrender.DarkTheme
			view.Conversation = true
			view.testAudience()
			output := "first line\nsecond line\nlast line\n"
			item := binding.ThreadItem{ID: "user-shell", Type: "commandExecution", Source: "userShell", Command: "cat notes.txt; printf done", Status: "completed", ExitCode: new(0), AggregatedOutput: &output}
			if state == "restored" {
				u.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}})
			} else {
				item.Status, item.ExitCode, item.AggregatedOutput = "inProgress", nil, nil
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
				appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "main", "turnId": "turn", "itemId": item.ID, "delta": output})
				if state != "running" {
					item.Status, item.ExitCode, item.AggregatedOutput = "completed", new(0), &output
					if state == "failed" {
						item.Status, item.ExitCode = "failed", new(2)
					}
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
				}
				for range 10 {
					flushStreamOutput(&u.attached, u.shell, &u.Client, &u.streams, u.execTrack, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.ctx, u.retained, u.segments, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues)
				}
			}
			// Later agent work must retain its ordinary classification without
			// folding the user's command into a counted batch.
			for _, command := range []string{"cat agent.txt", "printf agent"} {
				agent := binding.ThreadItem{ID: command, Type: "commandExecution", Source: "agent", Command: command, Status: "completed", ExitCode: new(0), AggregatedOutput: new("agent output\nmore output\n")}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": agent})
			}
			now = now.Add(10 * time.Second)
			settleActivity(now, view)
			blocks := view.blocks(0)
			if len(blocks) != 1 || !strings.HasPrefix(activityui.RowVerb(blocks[0]), "You ") || blocks[0].Code != item.Command || blocks[0].Collapsed || blocks[0].Collapsible() {
				t.Fatalf("user command lost source or disclosure: %+v", blocks)
			}
			if view.blocks(1)[0].Verb != "Read" || activityui.RowVerb(view.blocks(2)[0]) != "Ran" {
				t.Fatal("agent command display changed")
			}
			want := "You ran"
			if state == "running" {
				want = "You running"
			}
			for _, width := range []int{36, 72} {
				rows := view.testRenderConversation(width).Lines
				plain := ansi.Strip(strings.Join(rows, "\n"))
				if !strings.Contains(plain, want) || !strings.Contains(plain, "last line") || strings.Contains(plain, "Ran 3 commands") {
					t.Fatalf("missing user command/output at width %d:\n%s", width, plain)
				}
				if state == "failed" && !strings.Contains(plain, "exit 2") {
					t.Fatal("user command lost its failure")
				}
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/user-shell-%s-%d.txt", state, width), append(rows, "plain after activity"), width)
			}
			if _, ok := activityui.CollapseBatch(append(blocks, view.blocks(1)...)); ok {
				t.Fatal("user shell joined a collapsed batch")
			}
		})
	}
}

func TestUserShellReplayKeepsOriginAndOutput(t *testing.T) {
	path := replayTestWrite(t, t.TempDir(), "shell.jsonl", replayTestMeta("main"),
		replayTestItem("main", "turn", replayTestEpoch, replayTestEpoch+1000, map[string]any{
			"type": "CommandExecution", "id": "shell", "source": "user_shell", "status": "completed",
			"command": []string{"bash", "-lc", "cat notes.txt"}, "exit_code": 0, "aggregated_output": "first line\nlast line\n",
		}))
	source, err := replayReadTest(t, nil, t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.advance(p.Until); err != nil {
		t.Fatal(err)
	}
	got := replayPlaybackTestPaint(t, p, 80, 20)
	if !strings.Contains(got, "You ran") || !strings.Contains(got, "cat notes.txt") || !strings.Contains(got, "last line") || len(p.ui.session.Commands()) != 0 {
		t.Fatalf("replay lost user shell or revived its process:\n%s", got)
	}
}
