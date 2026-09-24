package livediff

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestFadeBlendsFromCanvas(t *testing.T) {
	canvas := Canvas{Background: RGB{0, 0, 0}, Foreground: RGB{200, 200, 200}}
	line := "\x1b[48;2;100;0;0m\x1b[38;2;0;200;100m+x\x1b[39m y\x1b[1m!\x1b[0m"
	if got := Fade(line, 1, canvas); got != line {
		t.Fatalf("settled row changed: %q", got)
	}
	half := Fade(line, .5, canvas)
	// The fill blends from the canvas; text blends toward the blended fill.
	for _, want := range []string{"\x1b[48;2;50;0;0m", "\x1b[38;2;25;100;50m", "\x1b[38;2;125;100;100m", "\x1b[1m"} {
		if !strings.Contains(half, want) {
			t.Fatalf("half fade lacks %q: %q", want, half)
		}
	}
	if ansi.Strip(half) != ansi.Strip(line) {
		t.Fatalf("fade changed text: %q", half)
	}
	// Default text fades too, and a hidden row matches the background.
	if got := Fade("plain", 0, canvas); got != "\x1b[38;2;0;0;0mplain" {
		t.Fatalf("hidden default text: %q", got)
	}
}

func TestBackgroundColor(t *testing.T) {
	if got, ok := BackgroundColor("\x1b]11;rgb:ffff/8080/0000\x1b\\"); !ok || got != (RGB{255, 128, 0}) {
		t.Fatalf("background: %v %v", got, ok)
	}
	if _, ok := BackgroundColor("\x1b]11;rgb:zz/00/00\a"); ok {
		t.Fatal("malformed reply accepted")
	}
}
