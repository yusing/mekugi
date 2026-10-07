package activity_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestFaintFallbackRestoresStyles(t *testing.T) {
	// Color changes and resets inside a dim region must not strand the muted shade.
	input := "\x1b[38;5;170mA\x1b[2mB\x1b[38;5;39mC\x1b[22mD\x1b[0mE"
	expected := "\x1b[38;5;170mA\x1b[38;5;133mB\x1b[38;5;31mC\x1b[38;5;39mD\x1b[0mE"
	got, want := vt.NewEmulator(10, 1), vt.NewEmulator(10, 1)
	defer got.Close()
	defer want.Close()
	fmt.Fprint(got, activityui.FaintFallback(input))
	fmt.Fprint(want, expected)
	for x := range 5 {
		if !got.CellAt(x, 0).Style.Equal(&want.CellAt(x, 0).Style) {
			t.Fatalf("cell %d: %+v != %+v", x, got.CellAt(x, 0).Style, want.CellAt(x, 0).Style)
		}
	}
	link := "\x1b]8;;https://example.com\x1b\\"
	if !strings.Contains(activityui.FaintFallback(link+input), link) {
		t.Fatal("hyperlink changed")
	}
}

func TestUISnapshotAgentFaintRetainsNormalColor(t *testing.T) {
	p := activityui.Painter{}
	b := activityui.Block{Kind: "progress", Body: "Waiting", WaitTargets: []activityui.WaitTarget{{Name: "/root/agent1"}}}
	rows := append(p.Block(b, 80), "plain after wait")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/agent_faint.txt", rows, 80)
}
