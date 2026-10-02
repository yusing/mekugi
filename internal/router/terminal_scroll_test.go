package router

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestScrollablePaneKeysAgreeAtConsumers(t *testing.T) {
	tests := []struct {
		name          string
		keys          string
		offset        int
		follow        bool
		starting      int
		initialFollow bool
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
		{name: "end", keys: "\x1b[F", offset: 90, starting: 50},
		{name: "G end", keys: "G", offset: 90, starting: 50},
		{name: "end pauses following", keys: "G", offset: 90, starting: 50, initialFollow: true},
		{name: "resume", keys: "r", offset: 50, follow: true, starting: 50},
		{name: "clamp at top", keys: "k", offset: 0, starting: 0},
		{name: "clamp at bottom", keys: "j", offset: 99, starting: 99},
		{name: "clamp page at bottom", keys: " ", offset: 99, starting: 95},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := newTerminalScrollTestController(test.starting, test.initialFollow)
			defer controller.close()
			for _, key := range []byte(test.keys) {
				controller.handleKey(key)
			}
			if got := controller.view.Scroll["scroll.txt"]; got != test.offset {
				t.Fatalf("live diff offset = %d, want %d", got, test.offset)
			}
			if controller.view.Following != test.follow {
				t.Fatalf("live diff following = %v, want %v", controller.view.Following, test.follow)
			}

			activity := newLiveActivityView()
			activity.feedLines, activity.feedRows = 100, 10
			activity.offset = test.starting
			activity.following = test.initialFollow
			escape := ""
			for _, key := range []byte(test.keys) {
				escape, _ = activity.handleKey(escape, key)
			}
			// Activity stops at its last full viewport and has no r binding.
			if test.keys == "r" {
				if activity.offset != test.starting || activity.following != test.initialFollow {
					t.Fatal("Activity retained r follow binding")
				}
				return
			}
			// Reaching the bottom resumes transcript following.
			agentsOffset := min(test.offset, 90)
			if test.follow {
				agentsOffset = 90
			}
			if activity.offset != agentsOffset {
				t.Fatalf("agents offset = %d, want %d", activity.offset, agentsOffset)
			}
			if wantFollow := test.follow || agentsOffset == 90; activity.following != wantFollow {
				t.Fatalf("agents following = %v, want %v", activity.following, wantFollow)
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
			controller := newTerminalScrollTestController(test.starting, test.following)
			defer controller.close()
			for _, key := range []byte(test.seq) {
				controller.handleKey(key)
			}
			if got := controller.view.Scroll["scroll.txt"]; got != test.diffWant || controller.view.Following {
				t.Fatalf("live diff wheel offset/follow = %d/%v, want %d/false", got, controller.view.Following, test.diffWant)
			}

			activity := newLiveActivityView()
			activity.feedLines, activity.feedRows, activity.offset = 100, 10, test.starting
			activity.following = test.following
			if !activity.handleMouse(test.action, 1, 1) || activity.offset != test.activityWant || activity.following {
				t.Fatalf("agents wheel offset/follow = %d/%v, want %d/false", activity.offset, activity.following, test.activityWant)
			}
		})
	}
}

func TestLiveDiffStreamWheelPauseSurvivesUpdates(t *testing.T) {
	controller := newLiveDiffTerminalController(nil, "/workspace", os.Stdout)
	defer controller.close()
	controller.scope.Workspaces = map[string]map[string]bool{"/workspace": {"thread": true}}
	preview := terminalScrollStreamPreview(30)
	if _, err := controller.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.previewPane.Render(t.Context(), "/workspace", livediff.TerminalTheme, 70, 12); err != nil {
		t.Fatal(err)
	}

	for _, key := range []byte("\x1b[<64;1;1M") {
		controller.handleKey(key)
	}
	stream := controller.previewPane.Views["scroll-stream"]
	if !stream.Paused || stream.ScrollRow != 26 {
		t.Fatalf("wheel did not pause the stream at its previous row: paused=%v row=%d", stream.Paused, stream.ScrollRow)
	}

	preview = terminalScrollStreamPreview(40)
	if _, err := controller.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview}); err != nil {
		t.Fatal(err)
	}
	lines, err := controller.previewPane.Render(t.Context(), "/workspace", livediff.TerminalTheme, 70, 12)
	if err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(lines, "\n")
	if !stream.Paused || stream.ScrollRow != 26 || !strings.Contains(visible, "stream_0027") || strings.Contains(visible, "stream_0040") {
		t.Fatalf("stream update moved its paused viewport: row=%d paused=%v lines=%q", stream.ScrollRow, stream.Paused, visible)
	}

	controller.handleKey('r')
	lines, err = controller.previewPane.Render(t.Context(), "/workspace", livediff.TerminalTheme, 70, 12)
	if err != nil {
		t.Fatal(err)
	}
	if stream.Paused || !strings.Contains(strings.Join(lines, "\n"), "stream_0040") {
		t.Fatalf("resume did not return the stream viewport to its tip: paused=%v lines=%q", stream.Paused, lines)
	}
}

func newTerminalScrollTestController(offset int, following bool) *liveDiffTerminalController {
	controller := newLiveDiffTerminalController(nil, "", os.Stdout)
	lines := make([]string, 100)
	controller.diffMode, controller.lines, controller.offset, controller.rows = true, lines, offset, 10
	controller.lastWidth, controller.lastHeight = 80, 20
	controller.rendering = livediff.Render{Lines: lines, Starts: []int{0}}
	controller.view = livediff.View{
		Files:     []livediff.File{{Path: "scroll.txt"}},
		Scroll:    map[string]int{"scroll.txt": offset},
		Following: following,
	}
	return controller
}

func terminalScrollStreamPreview(rows int) diffview.Preview {
	var input strings.Builder
	for row := 1; row <= rows; row++ {
		fmt.Fprintf(&input, "+stream_%04d\n", row)
	}
	return diffview.Preview{
		ID: "scroll-stream", Workspace: "/workspace", Thread: "thread", Input: input.String(),
	}
}

func TestScrollablePaneWheelBurstAccumulates(t *testing.T) {
	c := newTerminalScrollTestController(50, false)
	defer c.close()
	v := newLiveActivityView()
	v.following = false
	v.feedLines, v.feedRows, v.offset = 100, 10, 50
	for range 4 {
		for _, key := range []byte("\x1b[<64;1;2M") {
			c.handleKey(key)
		}
		v.handleMouse('k', 2, 1)
	}
	if c.offset != 38 || c.view.Scroll["scroll.txt"] != 38 || v.offset != 38 {
		t.Fatalf("wheel burst lost events before paint: diff=%d saved=%d agents=%d", c.offset, c.view.Scroll["scroll.txt"], v.offset)
	}
}

func TestScrollableDiffBeforePaintAndAfterFileChange(t *testing.T) {
	for _, geometry := range []struct {
		name   string
		render livediff.Render
	}{
		{name: "not painted"},
		{name: "removed file", render: livediff.Render{Lines: []string{"old", "other"}, Starts: []int{0, 1}}},
	} {
		t.Run(geometry.name, func(t *testing.T) {
			for _, keys := range []string{"\x1b[5~", "\x1b[6~", "\x1b[A", "\x1b[B", "g", "G", "\x1b[<64;1;2M"} {
				c := newTerminalScrollTestController(50, true)
				c.rendering = geometry.render
				c.lines = geometry.render.Lines
				for _, key := range []byte(keys) {
					c.handleKey(key)
				}
				if c.view.Selected != 0 || c.view.Scroll["scroll.txt"] != 50 {
					t.Fatalf("input used stale geometry: selection=%d scroll=%v", c.view.Selected, c.view.Scroll)
				}
				// Valid painted geometry restores ordinary scrolling immediately.
				c.rendering = livediff.Render{Lines: make([]string, 100), Starts: []int{0}}
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
