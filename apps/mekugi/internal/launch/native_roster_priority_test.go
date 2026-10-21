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

func TestUISnapshotNativeRosterActivePriority(t *testing.T) {
	for _, height := range []int{30, 18, 14, 11, 10} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.clock = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local) }
			u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
			u.shell.Transcript.Painter.Theme, u.shell.Agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
			u.attached.presentation.Status.Text, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
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
			want := []string{"/root", "/root/done-parent/worker", "/root/work-b", "/root/work-c", "/root/work-d", "/root/work-e"}
			switch height {
			case 18:
				want = want[:2]
			case 14:
				want = want[:3]
			case 11:
				want = want[:1]
			case 10:
				want = nil
			}
			if !slices.Equal(names, want) {
				t.Fatalf("active agents hidden by completed rows: got %v, want %v", names, want)
			}
			frame := screen.String()
			if len(want) > 0 && !strings.Contains(frame, fmt.Sprintf("+%d more", 9-len(want))) {
				t.Fatalf("inaccurate overflow count:\n%s", frame)
			}
			if terminalTest(u).Layout.Main.H < 6 {
				t.Fatalf("roster displaced usable Main: %+v", terminalTest(u).Layout)
			}
			if height == 18 && (terminalTest(u).Layout.Diff.W == 0 || terminalTest(u).Layout.Main.W == 0) {
				t.Fatalf("roster hid an open content column: %+v", terminalTest(u).Layout)
			}
			assertNativeUISnapshot(t, fmt.Sprintf("native-roster-active-%d", height), strings.Split(frame, "\n"))
		})
	}
}
