package sessionapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotWaitProgressOnlyAwaitedAgent(t *testing.T) {
	for _, mode := range []string{"any", "all"} {
		t.Run(mode, func(t *testing.T) {
			painter := activityui.Painter{}
			painter.Theme = diffrender.DarkTheme
			var rendered []string
			for i, wake := range []string{"progress", "events"} {
				waiting := activity.Value{Kind: "progress", Body: "Waiting for", WaitTargets: []activity.WaitTarget{{Name: "/root/worker"}}}
				rendered = append(rendered, painter.Block(activityui.Block{Value: waiting}, 100)...)
				facts := appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{WaitID: fmt.Sprint(i), CallerThreadID: "root", CallerWorkID: "work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "turn"}, Mode: mode, Selected: []appserver.CoordinationTarget{{ThreadID: "worker", TaskName: "/root/worker", WorkID: "child-work"}}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: wake}}}
				entries := sessionpresentation.NativeCoordinationEntries("root", "turn", sessioncontrol.NativeMessage{}, &facts, time.Time{}, false)
				if len(entries) != 1 {
					t.Fatalf("native settlement rows: %+v", entries)
				}
				for _, value := range sessionpresentation.ActivityContent(entries[0]).Values() {
					rendered = append(rendered, painter.Block(activityui.Block{Value: value}, 100)...)
				}
			}
			rendered = append(rendered, "Following plain row")
			text := ansi.Strip(strings.Join(rendered, "\n"))
			if !strings.Contains(text, "Finished waiting for worker: yielded") || strings.Contains(text, "+4 more") || strings.Contains(text, "settled-") {
				t.Fatalf("renderer expanded native selected targets: %s", text)
			}
			uisnapshot.AssertTerminal(t, "testdata/snapshots/wait-progress-only-awaited.txt", rendered, 100)
		})
	}
}
