package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotJournalChildHandoffRemaining(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	first, _ := prepareActivityTest(t, proxy, "first", "child", "root", "/root/child", nil)
	applyChildDeltaItems(t, proxy, first.directory,
		journalMutation{Op: "add", Kind: "task", Title: new("Check integration"), State: new("pending")},
		journalMutation{Op: "add", Kind: "task", Title: new("Confirm runtime behavior"), State: new("working")},
		journalMutation{Op: "add", Kind: "task", Title: new("Run live acceptance"), State: new("blocked"), Reason: new("Local fixture is unavailable")})
	childDeltaResult(t, first, false, true, "first-result")
	first.Close()
	applyChildDeltaItems(t, proxy, first.directory, journalMutation{Op: "log", Text: new("Focused offline checks passed")})
	followup, _ := prepareActivityTest(t, proxy, "followup", "child", "root", "/root/child", nil)
	defer followup.Close()
	result := childDeltaResult(t, followup, false, true, "followup-result")
	painter := activityui.Painter{Theme: livediff.DarkTheme}
	uisnapshot.Assert(t, "testdata/snapshots/journal-child-handoff-remaining.txt", strings.Join(painter.Markdown(result, 70), "\n")+"\n")
}
