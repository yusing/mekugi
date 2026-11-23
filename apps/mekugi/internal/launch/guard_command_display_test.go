package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestUISnapshotGuardedHeredocCommit(t *testing.T) {
	source := "timeout 90s git commit -F - <<'EOF'\ndocs(ui): describe content-detected inline highlighting\nEOF"
	command := shellCommandString("/bin/bash", []string{"-lc", source})
	output := "[main e9085377] docs(ui): describe content-detected inline highlighting\n 2 files changed, 6 insertions(+), 4 deletions(-)\n"
	u := vcsCommandUI(t, "")
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.restoreHistory([]binding.HistoryTurn{{ID: "past", Status: "completed", Items: []binding.ThreadItem{{
		ID: "cmd", Type: "commandExecution", Command: command, AggregatedOutput: &output, ExitCode: new(0), DurationMS: new(int64(500)),
	}}}})
	if entry := u.attached.source.Entries[0]; entry.Source.Native.Command != command {
		t.Fatal("host command changed")
	}
	assertNativeUISnapshot(t, "guarded-heredoc-commit", strings.Split(mainFeed(u, 90), "\n"))
}

func TestUISnapshotGuardCommandDisplay(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	source := "git log -1 --format='%H%n%s'; git status --porcelain; git cat-file commit HEAD | awk '/^gpgsig / {print \"signature_present\"; exit}'"
	command := shellCommandString("/bin/bash", []string{"-lc", source})
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": command, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, source)
	outputs := []string{"9a9860a81d8a73a8d36981d3854ab11edac5cdaf\nfix(ui): align loaded skills links and dialog selection\n", "", "signature_present\n"}
	for i, ms := range []int{8, 17, 6} {
		duration := time.Duration(ms) * time.Millisecond
		report.send(
			segmenttransport.Message{Type: segmenttransport.Begin, Index: i, Timing: segmenttransport.Timing{Started: now}},
			segmenttransport.Message{Type: segmenttransport.Output, Index: i, Data: outputs[i]},
			segmenttransport.Message{Type: segmenttransport.End, Index: i, Code: new(0), Timing: segmenttransport.Timing{Started: now, Ended: now.Add(duration), ElapsedNS: int64(duration)}},
		)
	}
	report.send(segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed})
	report.conn.Close()
	hub.await(t, [3]string{"main", "t", "cmd"}, func(view execTrackView) bool { return view.complete })
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, strings.Join(outputs, "")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	awaitMain(t, u, "signature_present")
	assertNativeUISnapshot(t, "guard-command-display", u.testActivity(u.shell.Transcript).RenderFeed(90, 12).Lines)
}

func TestUISnapshotPendingGuardCommandDisplay(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	source := "sleep 1; git stash push -m 'temporary changes' -- go.work.sum; GIT_SEQUENCE_EDITOR=: git rebase -i --autosquash HEAD~2; git stash pop"
	command := shellCommandString("/bin/bash", []string{"-lc", source})
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": command, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, source)
	defer report.conn.Close()
	report.send(
		segmenttransport.Message{Type: segmenttransport.Begin, Index: 0},
		segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: "waiting\n"},
	)
	awaitMain(t, u, "waiting")
	entry := u.testActivity(u.shell.Transcript).Entries[0].Source
	want, _ := shellinstrumentation.Split(source)
	presented := sessionpresentation.CommandPresentation(entry)
	if len(presented.Segments) != len(want) {
		t.Fatalf("presented segments = %d, want %d", len(presented.Segments), len(want))
	}
	for i, segment := range presented.Segments {
		if segment.Source != want[i].Source || entry.Native.Segments[i].Source != want[i].Source {
			t.Fatalf("segment %d: retained source changed: %q", i, segment.Source)
		}
	}
	pages := activityui.Disclosure(nil).Blocks(presented.DetailValues())
	if len(pages) != len(want) {
		t.Fatalf("detail pages = %d, want %d", len(pages), len(want))
	}
	for i, page := range pages {
		if page.Code != want[i].Source {
			t.Fatalf("page %d source = %q, want %q", i, page.Code, want[i].Source)
		}
	}
	assertNativeUISnapshot(t, "pending-guard-command-display", u.testActivity(u.shell.Transcript).RenderFeed(90, 12).Lines)
}

func TestUISnapshotGuardCommandOutputDialog(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	source := "git status --porcelain; sudo journalctl -k --no-pager"
	command := shellCommandString("/bin/bash", []string{"-lc", source})
	output := "-- No entries --\n"
	u.restoreHistory([]binding.HistoryTurn{{ID: "past", Status: "completed", Items: []binding.ThreadItem{{
		ID: "cmd", Type: "commandExecution", Command: command, AggregatedOutput: &output, ExitCode: new(1),
	}}}})
	entry := u.attached.source.Entries[0]
	pages := activityui.Disclosure(nil).Blocks(sessionpresentation.CommandPresentation(entry.Source).DetailValues())
	if len(pages) != 1 || pages[0].Code != source || entry.Source.Native.Command != command || u.attached.source.RetainedOutput(pages[0].Source, pages[0].Output) != entry.Source.Native.Output {
		t.Fatalf("combined output pages = %+v; want original command display and unchanged host output", pages)
	}
	terminal := terminalTest(u)
	terminal.openBlocks(u.shell.Transcript, pages)
	assertNativeUISnapshot(t, "guard-command-output-dialog", strings.Split(drawOutputDialog(terminal), "\n"))
}

func TestUISnapshotSetupHeredocCommit(t *testing.T) {
	source := "set -e\ngit add a.go\ngit diff --cached --stat\ngit commit -F - <<'EOF'\nfix: wrapped\n\nbody\nEOF"
	command := shellCommandString("/bin/bash", []string{"-lc", source})
	output := " a.go | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n[main abc1234] fix: wrapped\n 1 file changed, 1 insertion(+), 1 deletion(-)\n"
	u := vcsCommandUI(t, "")
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.restoreHistory([]binding.HistoryTurn{{ID: "past", Status: "completed", Items: []binding.ThreadItem{{
		ID: "cmd", Type: "commandExecution", Command: command, AggregatedOutput: &output, ExitCode: new(0),
	}}}})
	folded := mainFeed(u, 90)
	u.testActivity(u.shell.Transcript).Expansion = 1
	assertNativeUISnapshot(t, "setup-heredoc-commit", strings.Split(folded+"\n\n"+mainFeed(u, 90), "\n"))
}
