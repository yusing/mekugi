package router

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
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
			u, _ := outputSelectionFixture("alpha─\n███\ngamma\n", false)
			_, y1 := outputSelectionPoint(t, u, "alpha")
			_, y2 := outputSelectionPoint(t, u, "gamma")
			// Start in the numbered gutter and finish in right padding, neither is text.
			x1, x2 := u.output.rect.x+2, u.output.rect.x+u.output.rect.w-3
			if reverse {
				x1, x2, y1, y2 = x2, x1, y2, y1
			}
			for _, event := range []string{fmt.Sprintf("\x1b[<0;%d;%dM", x1+1, y1+1), fmt.Sprintf("\x1b[<32;%d;%dM", x2+1, y2+1), fmt.Sprintf("\x1b[<0;%d;%dm", x2+1, y2+1)} {
				for _, key := range []byte(event) {
					if err := u.key(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			if u.selection == nil {
				t.Fatal("drag did not retain selection")
			}
			if got := u.selection.text(); got != "alpha─\n███\ngamma" {
				t.Fatalf("selected = %q", got)
			}
		})
	}
}

func TestOutputDialogSelectionPendingDrag(t *testing.T) {
	u, _ := outputSelectionFixture("alpha\nbeta\n", false)
	x, y := outputSelectionPoint(t, u, "alpha")
	u.outputMouse(0, x, y, false)
	selected := u.selection
	for _, key := range []byte{'r', 'R', 'c', 'C', 3, 'y'} {
		if err := u.key(key); err != nil {
			t.Fatal(err)
		}
		if u.output == nil || u.selection != selected || !selected.dragging || u.clipboard != "" || u.main.draft != "" {
			t.Fatalf("key %d acted before drag completion", key)
		}
	}
	u.outputMouse(0, x, y, true)
	if u.output == nil || u.selection != nil {
		t.Fatal("press-only release retained selection or closed dialog")
	}
}

func TestOutputDialogSelectionIgnoresUnhandledWheelEvents(t *testing.T) {
	u, _ := outputSelectionFixture("alpha\nbeta\n", false)
	x, y := outputSelectionPoint(t, u, "alpha")
	outputSelectionDrag(u, x, y, x+4, y)
	selected := u.selection
	for _, event := range []struct {
		button  int
		release bool
	}{{66, false}, {67, false}, {64, true}, {65, true}} {
		end := "M"
		if event.release {
			end = "m"
		}
		if err := u.mouse(fmt.Sprintf("\x1b[<%d;%d;%d%s", event.button, x+1, y+1, end)); err != nil {
			t.Fatal(err)
		}
		if u.output == nil || u.selection != selected || selected.text() != "alpha" {
			t.Fatalf("unhandled wheel event %+v changed selection", event)
		}
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
	if u.output.follow || u.selection.text() != "original" {
		t.Fatal("drag did not pause and preserve original text")
	}
	final := "replacement aggregate\n"
	output.Finish(&final, new(0))
	if frame := drawOutputDialog(u); strings.Contains(frame, "replacement aggregate") {
		t.Fatal("completion replaced selected snapshot")
	}
	u.outputKey("\x1b")
	if u.output == nil || u.selection != nil {
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
			if u.output == nil || u.selection != nil {
				t.Fatal("navigation retained stale selection or closed dialog")
			}
			if action == "page" && u.output.page != 1 {
				t.Fatal("page navigation failed")
			}
		})
	}
}

func TestOutputDialogSkillMarkdownSearchSelectionAndCopy(t *testing.T) {
	const source = "# Skill guide\n\nUse **careful steps**."
	output := new(activityui.Retention).New()
	output.Write(source)
	output.Finish(nil, new(0))
	u := dialogForOutput(output)
	u.main = &appServerUI{view: newLiveActivityView()}
	block := activityui.ParseOperation("Skill `example`")
	block.Output = output
	u.output.pages[0] = block
	frame := drawOutputDialog(u)
	if !strings.Contains(frame, "Use careful steps.") || strings.Contains(frame, "**") || strings.Contains(frame, "# Skill guide") {
		t.Fatalf("skill Markdown not rendered in dialog: %q", frame)
	}
	u.outputKey("y")
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(source)) + "\x07"; u.clipboard != want {
		t.Fatalf("whole-page copy changed source: %q", u.clipboard)
	}
	u.outputKey("/")
	for _, key := range []string{"c", "a", "r", "e", "f", "u", "l", "\r"} {
		u.outputKey(key)
	}
	if u.output.match < 0 || u.output.missed {
		t.Fatal("search missed rendered skill text")
	}
	drawOutputDialog(u)
	x, y := outputSelectionPoint(t, u, "careful steps")
	outputSelectionDrag(u, x, y, x+len("careful steps")-1, y)
	if u.selection == nil || u.selection.text() != "careful steps" {
		t.Fatal("selection did not match rendered skill text")
	}
	u.outputKey("y")
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("careful steps")) + "\x07"; u.clipboard != want {
		t.Fatalf("selection copy changed visible text: %q", u.clipboard)
	}
}

func TestUISnapshotOutputDialogSharedSelectionActions(t *testing.T) {
	u, _ := outputSelectionFixture("alpha\nbeta\n", false)
	x, y := outputSelectionPoint(t, u, "alpha")
	outputSelectionDrag(u, x, y, x+4, y)
	rows := make([]string, 18)
	u.paintOutput(rows, 80, len(rows))
	screen := vt.NewEmulator(80, len(rows))
	defer screen.Close()
	for y, row := range rows {
		fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row)
	}
	assertNativeUISnapshot(t, "native-output-dialog-selection", strings.Split(screen.String(), "\n"))
	// The outer painter must leave the dialog-relative snapshot intact.
	u.paintSelection(rows)
	if u.selection == nil {
		t.Fatal("outer painter discarded dialog selection")
	}
}

func TestOutputDialogSelectionFooterClippedHints(t *testing.T) {
	for _, tt := range []struct {
		width, at int
		action    byte
	}{
		{17, 10, 0}, // Reference ends under the ellipsis.
		{18, 10, 'r'},
		{30, 23, 0}, // Copy is not fully visible.
		{31, 24, 0}, // Copy ends under the ellipsis.
		{32, 24, 'c'},
		{42, 35, 0},  // Clear ends under the ellipsis.
		{43, 36, 27}, // The complete footer fits without an ellipsis.
	} {
		t.Run(fmt.Sprintf("width=%d", tt.width), func(t *testing.T) {
			u, _ := outputSelectionFixture("alpha\nbeta\n", false)
			u.main, _ = newAppServerTestUI()
			rows := make([]string, 18)
			u.paintOutput(rows, tt.width, len(rows))
			x, y := outputSelectionPoint(t, u, "alpha")
			outputSelectionDrag(u, x, y, x+4, y)
			u.paintOutput(rows, tt.width, len(rows))
			selection := u.selection
			u.outputMouse(0, u.output.rect.x+3+tt.at, u.output.rect.y+u.output.rect.h-1, false)
			switch tt.action {
			case 0:
				if u.output == nil || u.selection != selection || u.clipboard != "" || u.main.draft != "" {
					t.Fatal("clipped hint executed a selection action")
				}
			case 'r':
				if u.output != nil || len(u.main.selections) != 1 || u.main.selections[0].text != "alpha" {
					t.Fatal("fully visible Reference hint did not insert the selected text")
				}
			case 'c':
				if u.output == nil || u.selection != nil || u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("alpha"))+"\x07" {
					t.Fatal("fully visible Copy hint did not copy the selected text")
				}
			case 27:
				if u.output == nil || u.selection != nil || u.clipboard != "" || u.main.draft != "" {
					t.Fatal("fully visible Clear hint did not clear only the selection")
				}
			}
		})
	}
}
