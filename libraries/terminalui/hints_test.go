package terminalui

import "testing"

func TestTerminalHintsRenderingAndHitRegions(t *testing.T) {
	hints := Hints{{"ctrl+c", "copy", 'c'}, {"esc", "clear", 27}}
	if got := hints.Render(); got != "\x1b[1mctrl+c\x1b[22m copy · \x1b[1mesc\x1b[22m clear" {
		t.Fatalf("render %q", got)
	}
	for _, tc := range []struct {
		x, width int
		want     byte
	}{{0, 40, 'c'}, {10, 40, 'c'}, {11, 40, 0}, {14, 40, 27}, {14, 16, 0}} {
		if got := hints.ActionAt(tc.x, tc.width); got != tc.want {
			t.Fatalf("%+v: %d", tc, got)
		}
	}
}
