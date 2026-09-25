package router

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
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
			agentsOffset := test.offset
			if test.follow {
				agentsOffset = 90
			}
			if activity.offset != agentsOffset {
				t.Fatalf("agents offset = %d, want %d", activity.offset, agentsOffset)
			}
			if activity.following != test.follow {
				t.Fatalf("agents following = %v, want %v", activity.following, test.follow)
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
		{name: "up", seq: "\x1b[<64;1;2M", action: 'k', starting: 50, diffWant: 49, activityWant: 49},
		{name: "down", seq: "\x1b[<65;1;2M", action: 'j', starting: 50, diffWant: 51, activityWant: 51},
		{name: "up pauses following", seq: "\x1b[<64;1;2M", action: 'k', starting: 50, following: true, diffWant: 49, activityWant: 89},
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
	if _, err := controller.previewPane.render(t.Context(), "/workspace", livediff.TerminalTheme, 70, 12); err != nil {
		t.Fatal(err)
	}

	for _, key := range []byte("\x1b[<64;1;1M") {
		controller.handleKey(key)
	}
	stream := controller.previewPane.views["scroll-stream"]
	if !stream.paused || stream.scrollRow != 28 {
		t.Fatalf("wheel did not pause the stream at its previous row: paused=%v row=%d", stream.paused, stream.scrollRow)
	}

	preview = terminalScrollStreamPreview(40)
	if _, err := controller.applyEvent(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview}); err != nil {
		t.Fatal(err)
	}
	lines, err := controller.previewPane.render(t.Context(), "/workspace", livediff.TerminalTheme, 70, 12)
	if err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(lines, "\n")
	if !stream.paused || stream.scrollRow != 28 || !strings.Contains(visible, "stream_0029") || strings.Contains(visible, "stream_0040") {
		t.Fatalf("stream update moved its paused viewport: row=%d paused=%v lines=%q", stream.scrollRow, stream.paused, visible)
	}

	controller.handleKey('r')
	lines, err = controller.previewPane.render(t.Context(), "/workspace", livediff.TerminalTheme, 70, 12)
	if err != nil {
		t.Fatal(err)
	}
	if stream.paused || !strings.Contains(strings.Join(lines, "\n"), "stream_0040") {
		t.Fatalf("resume did not return the stream viewport to its tip: paused=%v lines=%q", stream.paused, lines)
	}
}

func newTerminalScrollTestController(offset int, following bool) *liveDiffTerminalController {
	controller := newLiveDiffTerminalController(nil, "", os.Stdout)
	lines := make([]string, 100)
	controller.diffMode, controller.lines, controller.offset, controller.rows = true, lines, offset, 10
	controller.lastWidth, controller.lastHeight = 80, 20
	controller.rendering = liveDiffRender{Lines: lines, Starts: []int{0}}
	controller.view = liveDiffView{
		Files:     []liveDiffFile{{Path: "scroll.txt"}},
		Scroll:    map[string]int{"scroll.txt": offset},
		Following: following,
	}
	return controller
}

func terminalScrollStreamPreview(rows int) liveDiffPreview {
	var input strings.Builder
	for row := 1; row <= rows; row++ {
		fmt.Fprintf(&input, "+stream_%04d\n", row)
	}
	return liveDiffPreview{
		ID: "scroll-stream", Workspace: "/workspace", Thread: "thread", Input: input.String(),
	}
}
