package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func assertNativeUISnapshot(t *testing.T, name string, rows []string) {
	t.Helper()
	uisnapshot.Assert(t, "testdata/snapshots/"+name+".txt", strings.Join(rows, "\n")+"\n")
}

func TestUISnapshotNativeMainComposer(t *testing.T) {
	for _, width := range []int{36, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			// An idle session avoids wall-clock durations and status animation.
			u.status, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
				{Seq: 1, Agent: "You", Kind: "text", Text: "Explain the snapshot workflow.", Observed: now},
				{Seq: 2, Agent: "Main", Kind: "final", Text: "Review the **rendered frame**, accept its baseline, then rerun the offline check.", Observed: now},
			}})
			u.draft = "Add narrow-screen coverage.\nKeep the interaction assertions."
			rows, _ := u.mainFrame(width, 16, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("native-main-%d", width), rows)
		})
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
			u.view.painter.Theme = livediff.DarkTheme
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

func snapshotActivityView(now time.Time) *liveActivityView {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	v.selected = "/root/review"
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root", Final: true, InputTokens: 1200, OutputTokens: 240, Cost: .02, CostKnown: true},
		{Name: "/root/inventory", Role: "worker", Final: true},
		{Name: "/root/review", Role: "review", Responding: true, Started: now.Add(-2 * time.Minute), WorkTimer: activeWorkTimer{Known: true, Since: now.Add(-2 * time.Minute)}},
		{Name: "/root/review/probe", Role: "explorer"},
	}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/inventory", Kind: "reply", message: &activityMessage{from: "/root/inventory", to: "/root", text: "Found uncovered output dialogs and native pickers."}, Observed: now},
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
			assertNativeUISnapshot(t, tc.name, v.nativeRoster(tc.width, 6, now, tc.focused))
		})
	}
}

func TestUISnapshotNativeRosterEditCounts(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root", Final: true, InputTokens: 2000},
		{Name: "/root/cleanup", Final: true, InputTokens: 1000},
	}})
	v.lineCounts = map[string]livediff.Counts{
		"/root":         {Added: 3, Removed: 1},
		"/root/cleanup": {Removed: 2},
	}
	v.netCounts = new(livediff.Counts{Added: 2, Removed: 1})
	assertNativeUISnapshot(t, "native-roster-edit-counts", v.nativeRoster(100, 6, now, true))
}

func TestUISnapshotNativeRosterContext(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, width := range []int{48, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			v := newLiveActivityView()
			v.painter.Theme = livediff.DarkTheme
			v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
				{Name: "/root", ContextKnown: true, ContextTokens: 171_300, ContextWindow: 285_000},
				{Name: "/root/child", ContextKnown: true, ContextTokens: 24_400, ContextWindow: 285_000},
			}})
			assertNativeUISnapshot(t, fmt.Sprintf("native-roster-context-%d", width), v.nativeRoster(width, 6, now, true))
		})
	}
}

func TestUISnapshotNativeLockedComposer(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.status = "Ready"
	u.view.painter.Theme = livediff.DarkTheme
	appServerTestKeys(t, u, "/lock\r")
	rows, _ := u.mainFrame(100, 10, 0)
	assertNativeUISnapshot(t, "native-main-locked", rows)
}

func TestUISnapshotNativeActivityLayouts(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, width := range []int{70, 105} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			v := snapshotActivityView(now)
			assertNativeUISnapshot(t, fmt.Sprintf("native-activity-%d", width), v.render(width, 16, now))
		})
	}
}

func TestUISnapshotNativeFilePicker(t *testing.T) {
	for _, width := range []int{30, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			u.session.cwd = "/workspace"
			u.picker = composerPicker{open: true, target: composerTarget{kind: '@', query: "snapshot"}, selected: 1, choices: []composerChoice{
				{name: "internal/uisnapshot/snapshot.go", path: "internal/uisnapshot/snapshot.go"},
				{name: "internal/ui/activity/snapshot_test.go", path: "internal/ui/activity/snapshot_test.go"},
				{name: "internal/ui/diffview/snapshot_test.go", path: "internal/ui/diffview/snapshot_test.go"},
			}}
			assertNativeUISnapshot(t, fmt.Sprintf("native-file-picker-%d", width), u.renderPicker(width, 7))
		})
	}
}

func TestUISnapshotNativeWaitingInputStacks(t *testing.T) {
	for _, mode := range []string{"queued", "steering"} {
		for _, width := range []int{36, 80} {
			t.Run(fmt.Sprintf("%s/%d", mode, width), func(t *testing.T) {
				u, w := newAppServerTestUI()
				u.view.painter.Theme = livediff.DarkTheme
				u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
				appServerTestTurn(t, u, "t")
				u.turnStarted = u.now()
				if mode == "queued" {
					appServerTestKeys(t, u, "Diagnose the broken pipe issue.\tAdd replay key tests.\tPreserve pending attachments.\tCover dequeue and retry.\t")
				} else {
					appServerTestKeys(t, u, "Diagnose the broken pipe issue.\r")
					first := appServerOneRequest(t, w, "turn/steer", "Diagnose the broken pipe issue.")
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, first.ID))
					appServerTestKeys(t, u, "Add replay key tests.\rPreserve pending attachments.\rCover dequeue and retry.\r")
				}
				rows, _ := u.mainFrame(width, 16, 0)
				assertNativeUISnapshot(t, fmt.Sprintf("native-waiting-%s-%d", mode, width), rows)
			})
		}
	}
}
