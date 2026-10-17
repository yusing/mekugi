package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func assertNativeUISnapshot(t *testing.T, name string, rows []string) {
	t.Helper()
	uisnapshot.Assert(t, "testdata/snapshots/"+name+".txt", strings.Join(rows, "\n")+"\n")
}

func TestUISnapshotNativeMainComposer(t *testing.T) {
	for _, width := range []int{36, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.Painter.Theme = diffrender.DarkTheme
			// An idle session avoids wall-clock durations and status animation.
			u.sessionPresentation().Status.Text, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			u.view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{
				{Seq: 1, Agent: "You", Kind: "text", Text: "Explain the snapshot workflow.", Observed: now},
				{Seq: 2, Agent: "Main", Kind: "final", Text: "Review the **rendered frame**, accept its baseline, then rerun the offline check.", Observed: now},
			}})
			u.Text = "Add narrow-screen coverage.\nKeep the interaction assertions."
			rows, _ := u.testMainFrame(width, 16, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("native-main-%d", width), rows)
		})
	}
}

func TestUISnapshotShellComposer(t *testing.T) {
	t.Run("marker", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.sessionPresentation().Status.Text = "Ready"
		appServerTestKeys(t, u, "!")
		u.testMainFrame(36, 6, 0)
		rows, _ := u.testMainFrame(36, 6, 0)
		uisnapshot.AssertTerminal(t, "testdata/snapshots/shell-composer-marker.txt", rows, 36)
	})
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		for _, width := range []int{18, 60} {
			t.Run(fmt.Sprintf("%d/%d", theme, width), func(t *testing.T) {
				u, _ := newAppServerTestUI()
				u.view.Painter.Theme = theme
				u.sessionPresentation().Status.Text = "Ready"
				appServerTestKeys(t, u, "\x1b[200~!printf '%s\\n' \"你好\n$HOME\" | cat\n# comment\n\x1b[201~")
				u.CursorBack = len(u.Text) - strings.Index(u.Text, "$HOME")
				rows, _ := u.testMainFrame(width, 12, 0)
				rows = append(rows, "plain after shell")
				u.deleteDraftRange(0, 1)
				normal, _ := u.testMainFrame(width, 12, 0)
				rows = append(rows, normal...)
				rows = append(rows, "plain after prompt")
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/shell-composer-%d-%d.txt", theme, width), rows, width)
			})
		}
	}
}

func TestUISnapshotNativeResumePicker(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name         string
		width        int
		all, loading bool
		query        string
	}{
		{"resume-all", 100, true, false, ""},
		{"resume-narrow", 36, false, false, ""},
		{"resume-searching", 60, false, true, "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.Painter.Theme = diffrender.DarkTheme
			u.resumePicker = &appServerResumePicker{cwd: "/workspace", all: tc.all, loading: tc.loading, query: tc.query, loadedAt: now, selected: 1,
				rows: []appServerResumeRow{
					{id: "main", title: "Improve rendered snapshot coverage", cwd: "/workspace", branch: "coverage/snapshot", updated: now.Add(-2 * time.Minute).Unix()},
					{id: "older", title: "Investigate a long parser failure message", cwd: "/other/project", branch: "fix/parser", updated: now.Add(-2 * time.Hour).Unix()},
					{id: "empty", cwd: "/workspace", branch: "main", updated: now.Add(-48 * time.Hour).Unix()},
				}}
			assertNativeUISnapshot(t, tc.name, u.resumePickerFrame(tc.width, 11))
		})
	}
}

func snapshotActivityView(now time.Time) *activityAdapter {
	v := newActivityAdapter()
	v.Painter.Theme = diffrender.DarkTheme
	v.Selected = "/root/review"
	v.Apply(sessionpresentation.ActivityEvent{Kind: "snapshot", Agents: []sessionpresentation.ActivityAgent{
		{Name: "/root", Final: true, InputTokens: 1200, OutputTokens: 240, Cost: .02, CostKnown: true},
		{Name: "/root/inventory", Role: "worker", Final: true},
		{Name: "/root/review", Role: "review", Responding: true, Started: now.Add(-2 * time.Minute), WorkTimer: activitypresentation.WorkTimer{Known: true, Since: now.Add(-2 * time.Minute)}},
		{Name: "/root/review/probe", Role: "explorer"},
	}, Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root/inventory", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root/inventory", To: "/root", Text: "Found uncovered output dialogs and native pickers."}, Observed: now},
		{Seq: 2, Agent: "/root/review", Kind: "tool", Text: "Read `native_ui_snapshot_test.go`", Observed: now},
		{Seq: 3, Agent: "/root/review/probe", Kind: "commentary", Text: "Checking narrow layouts and baseline whitespace.", Observed: now},
	}})
	return v
}

func TestUISnapshotNativeAgentsRoster(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name    string
		width   int
		focused bool
	}{
		{"native-roster-focused", 100, true},
		{"native-roster-compact", 48, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := snapshotActivityView(now)
			assertNativeUISnapshot(t, tc.name, v.NativeRoster(tc.width, 6, now, tc.focused))
		})
	}
}

func TestUISnapshotNativeRosterEditCounts(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v := newActivityAdapter()
	v.Painter.Theme = diffrender.DarkTheme
	v.Apply(sessionpresentation.ActivityEvent{Kind: "snapshot", Agents: []sessionpresentation.ActivityAgent{
		{Name: "/root", Final: true, InputTokens: 2000},
		{Name: "/root/cleanup", Final: true, InputTokens: 1000},
	}})
	v.Model.LineCounts = map[string]diffmodel.Counts{
		"/root":         {Added: 3, Removed: 1},
		"/root/cleanup": {Removed: 2},
	}
	v.Model.NetCounts = new(diffmodel.Counts{Added: 2, Removed: 1})
	assertNativeUISnapshot(t, "native-roster-edit-counts", v.NativeRoster(100, 6, now, true))
}

func TestUISnapshotNativeRosterContext(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, width := range []int{48, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			v := newActivityAdapter()
			v.Painter.Theme = diffrender.DarkTheme
			v.Apply(sessionpresentation.ActivityEvent{Kind: "snapshot", Agents: []sessionpresentation.ActivityAgent{
				{Name: "/root", ContextKnown: true, ContextTokens: 171_300, ContextWindow: 285_000},
				{Name: "/root/child", ContextKnown: true, ContextTokens: 24_400, ContextWindow: 285_000},
			}})
			assertNativeUISnapshot(t, fmt.Sprintf("native-roster-context-%d", width), v.NativeRoster(width, 6, now, true))
		})
	}
}

func TestUISnapshotNativeLockedComposer(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.sessionPresentation().Status.Text = "Ready"
	u.view.Painter.Theme = diffrender.DarkTheme
	appServerTestKeys(t, u, "/lock\r")
	rows, _ := u.testMainFrame(100, 10, 0)
	assertNativeUISnapshot(t, "native-main-locked", rows)
}

func TestUISnapshotNativeActivityLayouts(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, width := range []int{70, 105} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			v := snapshotActivityView(now)
			assertNativeUISnapshot(t, fmt.Sprintf("native-activity-%d", width), v.Render(width, 16, now))
		})
	}
}

func TestUISnapshotNativeFilePicker(t *testing.T) {
	for _, width := range []int{30, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.Painter.Theme = diffrender.DarkTheme
			u.session.cwd = "/workspace"
			u.picker = composerPicker{State: completion.State{Open: true, Target: completion.Target{Kind: '@', Query: "snapshot"}, Selected: 1, Choices: []completion.Choice{
				{Name: "internal/uisnapshot/snapshot.go", Path: "internal/uisnapshot/snapshot.go"},
				{Name: "internal/ui/activity/snapshot_test.go", Path: "internal/ui/activity/snapshot_test.go"},
				{Name: "internal/ui/diffview/snapshot_test.go", Path: "internal/ui/diffview/snapshot_test.go"},
			}}}
			assertNativeUISnapshot(t, fmt.Sprintf("native-file-picker-%d", width), u.renderPicker(width, 7))
		})
	}
}

func TestUISnapshotNativeWaitingInputStacks(t *testing.T) {
	for _, mode := range []string{"queued", "steering"} {
		for _, width := range []int{36, 80} {
			t.Run(fmt.Sprintf("%s/%d", mode, width), func(t *testing.T) {
				u, w := newAppServerTestUI()
				u.view.Painter.Theme = diffrender.DarkTheme
				u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
				appServerTestTurn(t, u, "t")
				u.sessionPresentation().Status.Started = u.now()
				if mode == "queued" {
					appServerTestKeys(t, u, "Diagnose the broken pipe issue.\tAdd replay key tests.\tPreserve pending attachments.\tCover dequeue and retry.\t")
				} else {
					appServerTestKeys(t, u, "Diagnose the broken pipe issue.\r")
					first := appServerOneRequest(t, w, "turn/steer", "Diagnose the broken pipe issue.")
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, first.ID))
					appServerTestKeys(t, u, "Add replay key tests.\rPreserve pending attachments.\rCover dequeue and retry.\r")
				}
				rows, _ := u.testMainFrame(width, 16, 0)
				assertNativeUISnapshot(t, fmt.Sprintf("native-waiting-%s-%d", mode, width), rows)
			})
		}
	}
}
