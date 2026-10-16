package terminalview_test

import (
	"testing"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotAgentFaintRetainsNormalColor(t *testing.T) {
	p := activityui.Painter{}
	b := activityui.Block{Kind: "progress", Body: "Waiting", WaitTargets: []activityui.WaitTarget{{Name: "/root/agent1"}}}
	rows := append(p.Block(b, 80), "plain after wait")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/agent_faint.txt", rows, 80)
}
