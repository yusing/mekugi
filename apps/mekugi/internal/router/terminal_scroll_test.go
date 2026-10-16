package router

import (
	"os"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
)

func TestScrollableDiffKeys(t *testing.T) {
	tests := []struct {
		name     string
		keys     string
		offset   int
		starting int
	}{
		{name: "line down", keys: "j", offset: 51, starting: 50},
		{name: "line up", keys: "k", offset: 49, starting: 50},
		{name: "down arrow", keys: "\x1b[B", offset: 51, starting: 50},
		{name: "up arrow", keys: "\x1b[A", offset: 49, starting: 50},
		{name: "page down", keys: "\x1b[6~", offset: 60, starting: 50},
		{name: "space page down", keys: " ", offset: 60, starting: 50},
		{name: "page up", keys: "\x1b[5~", offset: 40, starting: 50},
		{name: "b page up", keys: "b", offset: 40, starting: 50},
		{name: "home", keys: "\x1b[H", offset: 0, starting: 50},
		{name: "g home", keys: "g", offset: 0, starting: 50},
		// The pinned file heading leaves nine source rows in the ten-row pane.
		{name: "end", keys: "\x1b[F", offset: 91, starting: 50},
		{name: "G end", keys: "G", offset: 91, starting: 50},
		{name: "removed saved resume shortcut", keys: "r", offset: 50, starting: 50},
		{name: "clamp at top", keys: "k", offset: 0, starting: 0},
		{name: "clamp at bottom", keys: "j", offset: 99, starting: 99},
		{name: "clamp page at bottom", keys: " ", offset: 99, starting: 95},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := newTerminalScrollTestController(test.starting)
			defer controller.close()
			for _, key := range []byte(test.keys) {
				controller.handleKey(key)
			}
			if got := controller.view.Scroll["scroll.txt"]; got != test.offset {
				t.Fatalf("live diff offset = %d, want %d", got, test.offset)
			}
		})
	}
}

func TestScrollablePaneWheelAgreesAtConsumers(t *testing.T) {
	for _, test := range []struct {
		name         string
		seq          string
		action       byte
		starting     int
		following    bool
		diffWant     int
		activityWant int
	}{
		{name: "up", seq: "\x1b[<64;1;2M", action: 'k', starting: 50, diffWant: 47, activityWant: 47},
		{name: "down", seq: "\x1b[<65;1;2M", action: 'j', starting: 50, diffWant: 53, activityWant: 53},
		{name: "up pauses following", seq: "\x1b[<64;1;2M", action: 'k', starting: 50, following: true, diffWant: 47, activityWant: 87},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := newTerminalScrollTestController(test.starting)
			defer controller.close()
			for _, key := range []byte(test.seq) {
				controller.handleKey(key)
			}
			if got := controller.view.Scroll["scroll.txt"]; got != test.diffWant {
				t.Fatalf("live diff wheel offset = %d, want %d", got, test.diffWant)
			}

			activity := newActivityAdapter()
			activity.FeedLines, activity.FeedRows, activity.Offset = 100, 10, test.starting
			activity.Following = test.following
			if !activity.testHandleMouse(test.action, 1, 1) || activity.Offset != test.activityWant || activity.Following {
				t.Fatalf("agents wheel offset/follow = %d/%v, want %d/false", activity.Offset, activity.Following, test.activityWant)
			}
		})
	}
}

func newTerminalScrollTestController(offset int) *liveDiffTerminalController {
	controller := newLiveDiffTerminalController(nil, "", os.Stdout)
	lines := make([]string, 100)
	controller.lines, controller.offset, controller.rows = lines, offset, 10
	controller.lastWidth, controller.lastHeight = 80, 20
	controller.rendering = changeview.Render{Lines: lines, Starts: []int{0}}
	controller.view = changeview.View{
		Files:  []presentation.File{{Path: "scroll.txt"}},
		Scroll: map[string]int{"scroll.txt": offset},
	}
	return controller
}

func TestScrollablePaneWheelBurstAccumulates(t *testing.T) {
	c := newTerminalScrollTestController(50)
	defer c.close()
	v := newActivityAdapter()
	v.Following = false
	v.FeedLines, v.FeedRows, v.Offset = 100, 10, 50
	for range 4 {
		for _, key := range []byte("\x1b[<64;1;2M") {
			c.handleKey(key)
		}
		v.testHandleMouse('k', 2, 1)
	}
	if c.offset != 38 || c.view.Scroll["scroll.txt"] != 38 || v.Offset != 38 {
		t.Fatalf("wheel burst lost events before paint: diff=%d saved=%d agents=%d", c.offset, c.view.Scroll["scroll.txt"], v.Offset)
	}
}

func TestScrollableDiffBeforePaintAndAfterFileChange(t *testing.T) {
	for _, geometry := range []struct {
		name   string
		render changeview.Render
	}{
		{name: "not painted"},
		{name: "removed file", render: changeview.Render{Lines: []string{"old", "other"}, Starts: []int{0, 1}}},
	} {
		t.Run(geometry.name, func(t *testing.T) {
			for _, keys := range []string{"\x1b[5~", "\x1b[6~", "\x1b[A", "\x1b[B", "g", "G", "\x1b[<64;1;2M"} {
				c := newTerminalScrollTestController(50)
				c.rendering = geometry.render
				c.lines = geometry.render.Lines
				for _, key := range []byte(keys) {
					c.handleKey(key)
				}
				if c.view.Selected != 0 || c.view.Scroll["scroll.txt"] != 50 {
					t.Fatalf("input used stale geometry: selection=%d scroll=%v", c.view.Selected, c.view.Scroll)
				}
				// Valid painted geometry restores ordinary scrolling immediately.
				c.rendering = changeview.Render{Lines: make([]string, 100), Starts: []int{0}}
				c.lines, c.offset = c.rendering.Lines, 50
				c.handleKey('k')
				if c.view.Scroll["scroll.txt"] != 49 {
					t.Fatalf("scroll did not recover after paint: %v", c.view.Scroll)
				}
				c.close()
			}
		})
	}
}
