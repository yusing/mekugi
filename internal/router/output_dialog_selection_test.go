package router

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func outputSelectionFixture(text string, live bool) (*terminalUI, *activityui.Output) {
	output := new(activityui.Retention).New()
	output.Write(text)
	if !live {
		output.Finish(nil, new(0))
	}
	u := dialogForOutput(output)
	u.main = &appServerUI{view: newLiveActivityView()}
	drawOutputDialog(u)
	return u, output
}

func outputSelectionPoint(t *testing.T, u *terminalUI, text string) (int, int) {
	t.Helper()
	for y, row := range u.output.body {
		plain := ansi.Strip(row)
		if i := strings.Index(plain, text); i >= 0 {
			return u.output.rect.x + 2 + ansi.StringWidth(plain[:i]), u.output.rect.y + 3 + y
		}
	}
	t.Fatalf("text %q absent from dialog body", text)
	return 0, 0
}

func outputSelectionDrag(u *terminalUI, x1, y1, x2, y2 int) {
	u.outputMouse(0, x1, y1, false)
	u.outputMouse(32, x2, y2, false)
	u.outputMouse(0, x2, y2, true)
}

func TestOutputDialogMouseSelectionText(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			u, _ := outputSelectionFixture("alpha\nbeta\ngamma\n", false)
			_, y1 := outputSelectionPoint(t, u, "alpha")
			_, y2 := outputSelectionPoint(t, u, "gamma")
			// Start in the numbered gutter and finish in right padding, neither is text.
			x1, x2 := u.output.rect.x+2, u.output.rect.x+u.output.rect.w-3
			if reverse {
				x1, x2, y1, y2 = x2, x1, y2, y1
			}
			outputSelectionDrag(u, x1, y1, x2, y2)
			if u.output.selection == nil {
				t.Fatal("drag did not retain selection")
			}
			if got := u.output.selection.text(); got != "alpha\nbeta\ngamma" {
				t.Fatalf("selected = %q", got)
			}
		})
	}
}

func TestOutputDialogMouseSelectionWideGraphemes(t *testing.T) {
	u, _ := outputSelectionFixture("A你好e\u0301Z\n", false)
	x, y := outputSelectionPoint(t, u, "你好")
	// Both endpoints fall inside double-width graphemes.
	outputSelectionDrag(u, x+1, y, x+3, y)
	if u.output.selection == nil || u.output.selection.text() != "你好" {
		t.Fatal("wide selection split a grapheme")
	}
	x, y = outputSelectionPoint(t, u, "e\u0301Z")
	outputSelectionDrag(u, x, y, x+1, y)
	if got := u.output.selection.text(); got != "e\u0301Z" {
		t.Fatalf("combining selection = %q", got)
	}
}

func TestOutputDialogMouseSelectionFreezesLiveSnapshot(t *testing.T) {
	u, output := outputSelectionFixture("original\n", true)
	x, y := outputSelectionPoint(t, u, "original")
	u.outputMouse(0, x, y, false)
	output.Write("new live row\n")
	if frame := drawOutputDialog(u); strings.Contains(frame, "new live row") {
		t.Fatal("live update replaced drag snapshot")
	}
	u.outputMouse(32, x+7, y, false)
	u.outputMouse(0, x+7, y, true)
	if u.output.follow || u.output.selection.text() != "original" {
		t.Fatal("drag did not pause and preserve original text")
	}
	final := "replacement aggregate\n"
	output.Finish(&final, new(0))
	if frame := drawOutputDialog(u); strings.Contains(frame, "replacement aggregate") {
		t.Fatal("completion replaced selected snapshot")
	}
	u.outputKey("\x1b")
	if u.output == nil || u.output.selection != nil {
		t.Fatal("first Escape should clear selection only")
	}
	if frame := drawOutputDialog(u); !strings.Contains(frame, "replacement aggregate") {
		t.Fatal("cleared snapshot did not refresh")
	}
	u.outputKey("\x1b")
	if u.output != nil {
		t.Fatal("second Escape did not close dialog")
	}
}

func TestOutputDialogMouseSelectionClearedByNavigation(t *testing.T) {
	for _, action := range []string{"page", "wheel", "width", "height"} {
		t.Run(action, func(t *testing.T) {
			u, _ := outputSelectionFixture("alpha\nbeta\n", false)
			u.output.pages = append(u.output.pages, activityui.Block{Kind: "op", Verb: "Run", Code: "second"})
			x, y := outputSelectionPoint(t, u, "alpha")
			outputSelectionDrag(u, x, y, x+4, y)
			switch action {
			case "page":
				u.outputKey("\x1b[C")
			case "wheel":
				u.outputMouse(64, x, y, false)
			case "width":
				u.paintOutput(make([]string, 18), 50, 18)
			case "height":
				u.paintOutput(make([]string, 8), 80, 8)
			}
			if u.output == nil || u.output.selection != nil {
				t.Fatal("navigation retained stale selection or closed dialog")
			}
			if action == "page" && u.output.page != 1 {
				t.Fatal("page navigation failed")
			}
		})
	}
}

func TestOutputDialogMouseSelectionDecodedCopy(t *testing.T) {
	for _, key := range []byte{'y', 'c', 3} {
		t.Run(fmt.Sprintf("key=%d", key), func(t *testing.T) {
			u, _ := outputSelectionFixture("alpha\nbeta\n", false)
			x, y := outputSelectionPoint(t, u, "alpha")
			for _, event := range []string{
				fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1),
				fmt.Sprintf("\x1b[<32;%d;%dM", x+5, y+1),
				fmt.Sprintf("\x1b[<0;%d;%dm", x+5, y+1),
			} {
				for i := range len(event) {
					if err := u.key(event[i]); err != nil {
						t.Fatal(err)
					}
				}
			}
			if u.output.selection == nil || u.output.selection.dragging {
				t.Fatal("decoded mouse did not complete drag")
			}
			if err := u.key(key); err != nil {
				t.Fatal(err)
			}
			want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("alpha")) + "\x07"
			if u.clipboard != want {
				t.Fatalf("clipboard = %q, want %q", u.clipboard, want)
			}
			if u.output == nil || u.output.selection != nil {
				t.Fatal("copy must clear selection without closing dialog")
			}
			if u.main.view.offset != 0 || u.selection != nil {
				t.Fatal("modal drag affected background selection or feed")
			}
		})
	}
}

func TestOutputDialogMouseSelectionCopiesLiteralFrameGlyphs(t *testing.T) {
	u, _ := outputSelectionFixture("foo─\n███\n", false)
	x, y := outputSelectionPoint(t, u, "foo─")
	endX, endY := outputSelectionPoint(t, u, "███")
	for _, event := range []string{
		fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1),
		fmt.Sprintf("\x1b[<32;%d;%dM", endX+3, endY+1),
		fmt.Sprintf("\x1b[<0;%d;%dm", endX+3, endY+1),
	} {
		for i := range len(event) {
			if err := u.key(event[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := u.key(3); err != nil {
		t.Fatal(err)
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("foo─\n███")) + "\x07"
	if u.clipboard != want {
		t.Fatalf("clipboard = %q, want %q", u.clipboard, want)
	}
}
