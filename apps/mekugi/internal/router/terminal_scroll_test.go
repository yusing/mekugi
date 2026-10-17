package router

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func scrollTestUI(t *testing.T, offset int) *terminalUI {
	t.Helper()
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	terminalTest(u).Auxiliary = liveDiffChangesPane(t, 80, 10, []presentation.Chunk{liveDiffCapture("scroll", "scroll.txt", 1, "", liveDiffLinesFile("scroll", 100), presentation.Origin{})})
	terminalTest(u).Focus, terminalTest(u).Width, terminalTest(u).Height = 1, 80, 10
	terminalTest(u).Layout.Diff = terminalsession.Rect{W: 80, H: 10}
	for range offset {
		if err := terminalTest(u).key('j'); err != nil {
			t.Fatal(err)
		}
	}
	paintSavedDiff(t, savedPane(terminalTest(u)), 80, 10)
	return terminalTest(u)
}

func TestScrollablePaneWheelAgreesAtConsumers(t *testing.T) {
	for _, test := range []struct {
		name, seq              string
		action                 byte
		following              bool
		diffWant, activityWant int
	}{
		{name: "up", seq: "\x1b[<64;1;2M", action: 'k', diffWant: 47, activityWant: 47},
		{name: "down", seq: "\x1b[<65;1;2M", action: 'j', diffWant: 53, activityWant: 53},
		{name: "up pauses following", seq: "\x1b[<64;1;2M", action: 'k', following: true, diffWant: 47, activityWant: 87},
	} {
		t.Run(test.name, func(t *testing.T) {
			shell := scrollTestUI(t, 50)
			for _, key := range []byte(test.seq) {
				if err := shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
			paintSavedDiff(t, savedPane(shell), 80, 10)
			if got := savedPane(shell).Selection(79, 9).Offset; got != test.diffWant {
				t.Fatalf("saved diff wheel offset=%d want=%d", got, test.diffWant)
			}
			activity := newActivityAdapter()
			activity.FeedLines, activity.FeedRows, activity.Offset = 100, 10, 50
			activity.Following = test.following
			if !activity.testHandleMouse(test.action, 1, 1) || activity.Offset != test.activityWant || activity.Following {
				t.Fatalf("agents wheel offset/follow=%d/%v want=%d/false", activity.Offset, activity.Following, test.activityWant)
			}
		})
	}
}

func TestScrollablePaneWheelBurstAccumulates(t *testing.T) {
	shell := scrollTestUI(t, 50)
	activity := newActivityAdapter()
	activity.Following = false
	activity.FeedLines, activity.FeedRows, activity.Offset = 100, 10, 50
	for range 4 {
		for _, key := range []byte("\x1b[<64;1;2M") {
			if err := shell.key(key); err != nil {
				t.Fatal(err)
			}
		}
		activity.testHandleMouse('k', 2, 1)
	}
	paintSavedDiff(t, savedPane(shell), 80, 10)
	if got := savedPane(shell).Selection(79, 9).Offset; got != 38 || activity.Offset != 38 {
		t.Fatalf("wheel burst lost input before paint: diff=%d agents=%d", got, activity.Offset)
	}
}
