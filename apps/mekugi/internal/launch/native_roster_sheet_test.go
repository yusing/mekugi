package launch

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
)

// The open roster sheet lists subagents in roster order within its row budget,
// counts every hidden row, and leaves Main and an open Diff usable.
func TestUISnapshotNativeRosterSheet(t *testing.T) {
	for _, height := range []int{30, 18, 14, 11} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.clock = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local) }
			u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
			u.shell.Transcript.Painter.Theme, u.shell.Agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
			u.attached.presentation.Status.Text, u.settings.Model, u.settings.Effort = "Ready", "snapshot-model", "high"
			u.attached.childSource.Agents = []sessionpresentation.ActivityAgent{
				{Name: "/root"},
				{Name: "/root/done-a", Final: true},
				{Name: "/root/done-b", Final: true},
				{Name: "/root/done-parent", Final: true},
				{Name: "/root/done-parent/worker", Responding: true},
				{Name: "/root/work-b", Responding: true},
				{Name: "/root/work-c", Responding: true},
				{Name: "/root/work-d", Responding: true},
				{Name: "/root/work-e", Responding: true},
			}
			u.shell.DiffOpen = height == 18
			terminalTest(u).Focus = 3
			screen := vt.NewEmulator(100, height)
			defer screen.Close()
			if err := u.testPaint(screen, 100, height); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, hit := range u.shell.Agents.Hits {
				if !hit.Skills {
					names = append(names, hit.Agent)
				}
			}
			// Subtrees with active work lead; finished ones follow.
			all := []string{"/root/done-parent", "/root/done-parent/worker", "/root/work-b", "/root/work-c", "/root/work-d", "/root/work-e", "/root/done-a", "/root/done-b"}
			if len(names) == 0 || !slices.Equal(names, all[:len(names)]) {
				t.Fatalf("sheet rows left roster order: %v", names)
			}
			frame := screen.String()
			if hidden := len(all) - len(names); hidden > 0 != strings.Contains(frame, fmt.Sprintf("+%d more", hidden)) {
				t.Fatalf("inaccurate overflow count for %d rows:\n%s", len(names), frame)
			}
			layout := terminalTest(u).Layout
			if layout.Roster.Y-layout.Main.Y < 4 {
				t.Fatalf("roster sheet displaced usable Main: %+v", layout)
			}
			if height == 18 && (layout.Diff.W == 0 || layout.Main.W == 0) {
				t.Fatalf("roster sheet hid an open content column: %+v", layout)
			}
			assertNativeUISnapshot(t, fmt.Sprintf("native-roster-sheet-%d", height), strings.Split(frame, "\n"))
		})
	}
}
