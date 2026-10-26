package terminalsession

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotLiveElapsedRollover(t *testing.T) {
	start := time.Unix(100, 0)
	var painter activityui.Painter
	dock := BTWDock{body: &conversationmodel.SideView{Status: "Answering", Started: start}, running: true}
	var rows []string
	for _, elapsed := range []time.Duration{0, 999 * time.Millisecond, time.Second} {
		rows = append(rows, fmt.Sprint(elapsed), PaintSessionStatus(sessionpresentation.Label{Text: "Working", Running: true, Started: start}, start.Add(elapsed), terminalui.Colors{}))
		rows = append(rows, dock.Render(&painter, start.Add(elapsed), 60, 4).Rows...)
	}
	rows = append(rows, PaintSessionStatus(sessionpresentation.Label{Text: "Completed", CompletionKnown: true, Completion: 125 * time.Millisecond}, start.Add(time.Second), terminalui.Colors{}))
	uisnapshot.Assert(t, "testdata/snapshots/live_elapsed_rollover.txt", strings.Join(rows, "\n"))
}
