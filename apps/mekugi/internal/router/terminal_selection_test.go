package router

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/diffrender"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
	"github.com/yusing/mekugi/modules/conversation/prompt"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func selectionTestUI(rows ...string) *terminalUI {
	main, _ := newAppServerTestUI()
	main.view.FeedLeft, main.view.FeedTop = 1, 1
	main.view.FeedRight, main.view.FeedRows = 40, len(rows)
	painted := append([]string(nil), rows...)
	painted = append(painted, "")
	return &terminalUI{main: main, width: 40, height: len(painted), frame: terminalsession.Frame{Width: 40, Rows: painted}, Shell: terminalsession.Shell{Layout: terminalsession.Layout{Main: terminalsession.Rect{0, 0, 40, len(rows)}}}}
}

func selectionTestSnapshot(u *terminalUI) *terminalsession.Selection {
	if u.output != nil {
		return u.output.Selection
	}
	if u.selection != nil {
		return u.selection.Selection
	}
	return nil
}

func selectionTestDrag(t *testing.T, u *terminalUI, x1, y1, x2, y2 int) {
	t.Helper()
	if !u.selectionMouse(0, x1, y1, false) || !u.selectionMouse(32, x2, y2, false) || !u.selectionMouse(0, x2, y2, true) {
		t.Fatal("selection did not consume drag and release")
	}
	if u.selection == nil || u.selection.Dragging() || !u.selection.Moved() {
		t.Fatal("release did not retain a completed selection")
	}
}

func TestTerminalSelectionSyntaxColors(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		for _, dialog := range []bool{false, true} {
			t.Run(fmt.Sprintf("theme=%d/dialog=%v", theme, dialog), func(t *testing.T) {
				const source = `var text = "green"`
				p := &activityui.Painter{Theme: theme}
				u := selectionTestUI(p.Inline("`" + source + "` outside"))
				u.main.view.Painter.Theme = theme
				x, y := 0, 0
				if dialog {
					u.width, u.height = 80, 18
					u.openBlocks(u.main.view, []activityui.Block{{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "sample.go"}}, Tail: []string{source + " // outside"}}})
					drawOutputDialog(u)
					x, y = outputSelectionPoint(t, u, source)
				}
				paint := func() *vt.Emulator {
					rows := slices.Clone(u.frame.Rows)
					if dialog {
						rows = make([]string, u.height)
						u.main.prepareOutput(u)
						u.output.Paint(rows, u.width, u.height)
					} else {
						u.paintSelection(rows)
					}
					screen := vt.NewEmulator(u.width, u.height)
					t.Cleanup(func() { screen.Close() })
					for y, row := range rows {
						fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row)
					}
					return screen
				}
				before := paint()
				if dialog {
					outputSelectionDrag(u, x, y, x+len(source)-1, y)
				} else {
					selectionTestDrag(t, u, x, y, x+len(source)-1, y)
				}
				after := paint()
				for at := x; at < x+len(source); at++ {
					old, got := before.CellAt(at, y).Style, after.CellAt(at, y).Style
					if got.Fg != old.Fg || got.Bg == nil || got.Bg == old.Bg || got.Attrs&uv.AttrReverse != 0 || got.Bg != after.CellAt(x, y).Style.Bg {
						t.Fatalf("selection changed syntax or lost fill at %d: before=%+v after=%+v", at, old, got)
					}
				}
				if before.CellAt(x+len(source)+1, y).Style != after.CellAt(x+len(source)+1, y).Style || selectionTestSnapshot(u).Text() != source {
					t.Fatal("selection changed adjacent styles or copied text")
				}
				if dialog {
					u.output.Selection = nil
					for _, key := range []string{"/", "green", "\r"} {
						u.outputKey(key)
					}
					searched := paint()
					at := x + strings.Index(source, "green")
					if searched.CellAt(at, y).Style.Fg != before.CellAt(at, y).Style.Fg || searched.CellAt(at, y).Style.Bg != after.CellAt(at, y).Style.Bg {
						t.Fatal("search match lost syntax colors or selection fill")
					}
				}
			})
		}
	}
}

func TestTerminalSelectionComposerCaretStyles(t *testing.T) {
	for _, back := range []int{0, 3} {
		main, _ := newAppServerTestUI()
		main.Text, main.CursorBack = "abcdef", back
		frame, _ := main.testMainFrame(40, 12, 0)
		u := &terminalUI{main: main, width: 40, height: len(frame), frame: terminalsession.Frame{Width: 40, Rows: frame}, Shell: terminalsession.Shell{Layout: terminalsession.Layout{Main: terminalsession.Rect{0, 0, 40, len(frame)}}}}
		x, y := main.mainView.ComposerRect.X, main.mainView.ComposerRect.Y
		selectionTestDrag(t, u, x, y, x+5, y)
		u.paintSelection(frame)
		screen := vt.NewEmulator(40, len(frame))
		defer screen.Close()
		for y, row := range frame {
			fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row)
		}
		for at := x; at < x+6; at++ {
			style := screen.CellAt(at, y).Style
			if style.Attrs&uv.AttrReverse != 0 || style.Bg != screen.CellAt(x, y).Style.Bg {
				t.Fatalf("selected composer caret breaks fill: %+v", style)
			}
		}
		if back == 0 && screen.CellAt(x+6, y).Style.Attrs&uv.AttrReverse == 0 {
			t.Fatal("selection cleared the unselected caret")
		}
	}
}

func TestTerminalUISelectionDragText(t *testing.T) {
	for _, tt := range []struct {
		name           string
		rows           []string
		x1, y1, x2, y2 int
		want           string
	}{
		{"forward", []string{"hello world"}, 0, 0, 4, 0, "hello"},
		{"reverse", []string{"hello world"}, 4, 0, 0, 0, "hello"},
		{"multiline", []string{"one two", "three four"}, 4, 0, 4, 1, "two\nthree"},
		{"reverse multiline", []string{"one two", "three four"}, 4, 1, 4, 0, "two\nthree"},
		{"wide styled", []string{"\x1b[31mA你好B\x1b[0m"}, 2, 0, 4, 0, "你好"},
		{"combining", []string{"Ae\u0301Z"}, 1, 0, 2, 0, "e\u0301Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u := selectionTestUI(tt.rows...)
			selectionTestDrag(t, u, tt.x1, tt.y1, tt.x2, tt.y2)
			if got := u.selection.Text(); got != tt.want {
				t.Fatalf("selected text = %q, want %q", got, tt.want)
			}
			rows := append([]string(nil), u.frame.Rows...)
			u.paintSelection(rows)
			for _, label := range []string{"reference", "copy", "clear"} {
				if !strings.Contains(rows[len(rows)-1], label) {
					t.Fatalf("selection actions missing %s", label)
				}
			}
		})
	}
}

func TestTerminalUISelectionSkipsFrames(t *testing.T) {
	u := selectionTestUI(
		"│ ╭─ ✓ answer ───────────╮",
		"│ │ The native preview   │",
		"│ │ renders here.        │",
		"│ │                      │",
		"│ │   indented code      │",
		"│ ╰──────────────────────╯",
	)
	selectionTestDrag(t, u, 0, 1, 39, 5)
	if got, want := u.selection.Text(), "The native preview\nrenders here.\n\n  indented code"; got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
	for y, want := range map[int][2]int{0: {0, 0}, 1: {4, 22}, 2: {4, 17}, 3: {0, 0}, 4: {4, 19}, 5: {0, 0}} {
		if left, right := u.selection.Bounds(y); left != want[0] || right != want[1] {
			t.Fatalf("row %d highlights [%d,%d), want %v", y, left, right, want)
		}
	}
	// A drag that starts on the frame begins at the text beside it.
	selectionTestDrag(t, u, 0, 0, 6, 1)
	if got := u.selection.Text(); got != "✓ answer\nThe" {
		t.Fatalf("frame-started selection = %q", got)
	}
}

func TestTerminalUISelectionActions(t *testing.T) {
	for _, dialog := range []bool{false, true} {
		for _, key := range []string{"r", "R", "c", "C", "\x03", "\x1b", "reference click", "copy click", "clear click", "separator", "question", "y"} {
			if key == "y" && !dialog {
				continue
			}
			t.Run(fmt.Sprintf("dialog=%v/%q", dialog, key), func(t *testing.T) {
				u := selectionTestUI("hello world")
				mention := "[Selected message]"
				if dialog {
					u, _ = outputSelectionFixture("hello world\n", false)
					x, y := outputSelectionPoint(t, u, "hello")
					outputSelectionDrag(u, x, y, x+4, y)
					mention = "[Selected dialog]"
				} else {
					selectionTestDrag(t, u, 0, 0, 4, 0)
				}
				u.main.Text, u.main.CursorBack, u.Focus = "before after", len("after"), 2
				if key == "question" {
					u.main.questions.Active = &prompt.Call{Questions: []prompt.Question{{}}}
				}
				selected := selectionTestSnapshot(u)
				action := key
				if strings.Contains(key, "click") || key == "separator" {
					x, y := map[string]int{"reference click": 1, "copy click": 15, "clear click": 28, "separator": 12}[key], u.height-1
					if dialog {
						rect := dialogFrameRect(t, outputDialogScreen(u))
						x += rect.X + 3
						y = rect.Y + rect.H - 1
					}
					if err := u.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1)); err != nil {
						t.Fatal(err)
					}
					action = map[string]string{"reference click": "r", "copy click": "c", "clear click": "\x1b", "separator": "separator"}[key]
				} else {
					if action == "question" {
						action = "r"
					}
					if err := u.key(action[0]); err != nil {
						t.Fatal(err)
					}
					if action == "\x1b" {
						ageTerminalEscape(u)
						if err := u.flushEscape(); err != nil {
							t.Fatal(err)
						}
					}
				}
				if action == "separator" {
					if selectionTestSnapshot(u) != selected {
						t.Fatal("separator changed selection")
					}
					return
				}
				reference := action == "r" || action == "R"
				if selectionTestSnapshot(u) != nil || dialog && (u.output == nil) != reference {
					t.Fatal("wrong selection or dialog lifetime")
				}
				if reference {
					want := "before " + mention + " after"
					if len(u.main.Selections) != 1 || u.main.Selections[0].Text != "hello" {
						t.Fatal("reference lost quote")
					}
					if u.main.Text != want || u.Focus != 0 {
						t.Fatalf("reference draft=%q focus=%d", u.main.Text, u.Focus)
					}
					u.main.undoDraft(false)
					if u.main.Text != "before after" || u.main.CursorBack != len("after") {
						t.Fatal("undo lost draft/caret")
					}
				} else {
					want := ""
					if action != "\x1b" {
						want = "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello")) + "\x07"
					}
					if u.frame.Clipboard != want || u.main.Text != "before after" || u.Focus != 2 {
						t.Fatal("copy/clear changed draft, focus or clipboard")
					}
				}
			})
		}
	}
}

func TestTerminalUISelectionWebLinkClickVersusDrag(t *testing.T) {
	const link = "https://example.com/docs?q=hello#section"
	row := "\x1b]8;;" + link + "\x1b\\docs\x1b]8;;\x1b\\"
	u := selectionTestUI(row)
	if !u.selectionMouse(0, 1, 0, false) || !u.selectionMouse(0, 1, 0, true) {
		t.Fatal("link click unhandled")
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(link)) + "\x07"
	if u.selection != nil || u.frame.Clipboard != want {
		t.Fatalf("click: selection=%v clipboard=%q", u.selection, u.frame.Clipboard)
	}
	u = selectionTestUI(row)
	selectionTestDrag(t, u, 0, 0, 3, 0)
	if u.frame.Clipboard != "" || u.selection.Text() != "docs" {
		t.Fatalf("drag copied link or lost text: clipboard=%q text=%q", u.frame.Clipboard, u.selection.Text())
	}
}

func TestTerminalUISelectionResizeClears(t *testing.T) {
	for _, width := range []bool{false, true} {
		u := selectionTestUI("hello world")
		selectionTestDrag(t, u, 0, 0, 4, 0)
		rows := append([]string(nil), u.frame.Rows...)
		if width {
			u.width++
		} else {
			rows = append(rows, "")
		}
		u.paintSelection(rows)
		if u.selection != nil {
			t.Fatal("resize retained stale selection coordinates")
		}
	}
}

func TestTerminalUISelectionReplacementKeepsCleanLinks(t *testing.T) {
	u := selectionTestUI("\x1b]8;;https://example.com\x1b\\report\x1b]8;;\x1b\\ other")
	selectionTestDrag(t, u, 0, 0, 5, 0)
	u.paintSelection(u.frame.Rows)
	u.selectionMouse(0, 2, 0, false)
	u.selectionMouse(0, 2, 0, true)
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("https://example.com")) + "\x07"
	if u.frame.Clipboard != want {
		t.Fatalf("reselected link lost its target: %q", u.frame.Clipboard)
	}

	u = selectionTestUI("first second")
	selectionTestDrag(t, u, 0, 0, 4, 0)
	u.paintSelection(u.frame.Rows)
	selectionTestDrag(t, u, 6, 0, 11, 0)
	if strings.Contains(u.selection.Rows[0], "\x1b[7m") || u.selection.Text() != "second" {
		t.Fatalf("replacement inherited old highlight: %q", u.selection.Rows[0])
	}
}

func TestTerminalUISelectionKeepsPressedHitTarget(t *testing.T) {
	for _, question := range []bool{false, true} {
		u := selectionTestUI("click target")
		u.agents = activityui.NewView()
		u.Auxiliary = changeview.NewPane("", diffrender.TerminalTheme)
		for _, entry := range []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "First target"}, {Seq: 2, Agent: "Main", Kind: "text", Text: "Advanced target"}} {
			u.main.testActivity(u.main.view).appendEntry(entry, parseLiveActivity(entry))
		}
		old, next := activityui.Snippet{Run: 1}, activityui.Snippet{Run: 2}
		if question {
			u.main.view.FeedQuestions = []uint64{1}
			u.main.view.QuestionRows = map[uint64]int{1: 3, 2: 9}
		} else {
			u.main.view.FeedSnippets = []activityui.Snippet{old}
		}
		u.selectionMouse(0, 0, 0, false)
		// A streaming repaint replaces viewport hit maps before button release.
		u.main.view.FeedQuestions = []uint64{2}
		u.main.view.FeedSnippets = []activityui.Snippet{next}
		u.paintSelection(u.frame.Rows)
		u.selectionMouse(0, 0, 0, true)
		if question {
			if u.output == nil || !strings.Contains(fmt.Sprint(u.output.Pages), "First target") || strings.Contains(fmt.Sprint(u.output.Pages), "Advanced target") {
				t.Fatal("clicked an advancing question target")
			}
			u.outputKey("\x1b")
			if u.output != nil || u.main.view.Offset != 0 {
				t.Fatal("dismissing question changed transcript")
			}
		} else if u.output == nil || !strings.Contains(fmt.Sprint(u.output.Pages), "First target") || strings.Contains(fmt.Sprint(u.output.Pages), "Advanced target") {
			t.Fatal("clicked an advancing snippet target")
		}
	}
}

func TestTerminalUISelectionRenderedClipboard(t *testing.T) {
	for _, activity := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		u.ensureShell()
		view := u.testActivity(u.view)
		if activity {
			view = u.testActivity(u.agents)
			u.childSource.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/worker", Final: true}}
			u.shell.DiffOpen = false

		}
		applyTranscriptFixture(view, true, "", "main", "main", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "hello [report](https://example.com/report)"})
		if activity {
			u.testActivity(u.agents).setDisplayFilter(true, "Main")
		}
		screen := vt.NewEmulator(120, 30)
		defer screen.Close()
		var wire bytes.Buffer
		paint := func() {
			t.Helper()
			if err := u.testPaint(io.MultiWriter(screen, &wire), 120, 30); err != nil {
				t.Fatal(err)
			}
		}
		paint()
		x, y := -1, -1
		for row := range 30 {
			for col := range 120 {
				if c := screen.CellAt(col, row); c != nil && c.Link.URL != "" {
					x, y = col, row
					break
				}
			}
			if x >= 0 {
				break
			}
		}
		if x < 0 {
			t.Fatalf("no rendered link (activity=%v): %s", activity, screen.String())
		}
		for _, end := range []string{"M", "m"} {
			if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x+1, y+1, end)); err != nil {
				t.Fatal(err)
			}
		}
		wire.Reset()
		paint()
		want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("https://example.com/report")) + "\x07"
		if !strings.Contains(wire.String(), want) {
			t.Fatalf("clipboard request missing: %q", wire.String())
		}
		wire.Reset()
		paint()
		if strings.Contains(wire.String(), "\x1b]52;") {
			t.Fatal("clipboard replayed on repaint")
		}
		for _, event := range []string{fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1), fmt.Sprintf("\x1b[<32;%d;%dM", x+6, y+1), fmt.Sprintf("\x1b[<0;%d;%dm", x+6, y+1)} {
			if err := u.shell.mouse(event); err != nil {
				t.Fatal(err)
			}
		}
		paint()
		if !strings.Contains(screen.String(), "r reference · ctrl+c copy · esc clear") {
			t.Fatalf("missing menu: %s", screen.String())
		}
		if err := u.shell.mouse("\x1b[<0;2;30M"); err != nil {
			t.Fatal(err)
		}
		mention := "[Selected message] "
		if activity {
			mention = "[Selected message] "
		}
		if u.Text != mention || len(u.Selections) != 1 || u.Selections[0].Text != "[report](https://example.com/report)" {
			t.Fatalf("reference=%q %+v", u.Text, u.Selections)
		}
	}
}

func TestTerminalUISelectionComposer(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {40, 12}, {10, 4}} {
		u := newAppServerSessionTestUI(t, "/workspace")
		u.ensureShell()
		u.insertDraft("你好abc\nsecond")
		screen := vt.NewEmulator(size[0], size[1])
		defer screen.Close()
		if err := u.testPaint(screen, size[0], size[1]); err != nil {
			t.Fatal(err)
		}
		r := u.mainView.ComposerRect
		r.X += u.shell.Layout.Main.X
		r.Y += u.shell.Layout.Main.Y
		selectionTestDrag(t, u.shell, r.X, r.Y, r.X+3, r.Y)
		if got := u.shell.selection.Text(); got != "你好" {
			t.Fatalf("size %v: selected %q", size, got)
		}
		if err := u.shell.key(3); err != nil {
			t.Fatal(err)
		}
		if u.Text != "你好abc\nsecond" || u.quitRequested {
			t.Fatal("copy changed draft or quit")
		}
		want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("你好")) + "\x07"
		if u.shell.frame.Clipboard != want {
			t.Fatalf("clipboard %q", u.shell.frame.Clipboard)
		}
	}
}

func TestTerminalUISelectionComposerReplacesTranscript(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	screen := vt.NewEmulator(40, 8)
	defer screen.Close()
	paint := func() {
		t.Helper()
		if err := u.testPaint(screen, 40, 8); err != nil {
			t.Fatal(err)
		}
	}
	paint()
	u.view.FeedQuestions = []uint64{1, 1, 1, 1}
	u.view.QuestionRows = map[uint64]int{1: 99}
	u.insertDraft("first\nsecond\nthird\nfourth\nfifth")
	paint()
	r := u.mainView.ComposerRect
	r.X += u.shell.Layout.Main.X
	r.Y += u.shell.Layout.Main.Y
	selectionTestDrag(t, u.shell, r.X, r.Y, r.X+5, r.Y+1)
	if got := u.shell.selection.Text(); got != "first\nsecond" {
		t.Fatalf("selected %q", got)
	}
	u.shell.selectionMouse(0, r.X, r.Y, false)
	u.shell.selectionMouse(0, r.X, r.Y, true)
	if u.view.FlashQuestion != 0 {
		t.Fatal("composer click activated stale transcript question")
	}
}

// Build the selected snapshot through the same drag lifecycle as real pointer input.
func selectedTerminalText(text string) *terminalsession.Selection {
	width := ansi.StringWidth(text)
	selected := terminalsession.NewSelection(terminalsession.Surface{Rect: terminalsession.Rect{W: width, H: 1}, Rows: []string{text}}, 0, 0)
	selected.Move(width-1, 0, true)
	return selected
}

func ageTerminalEscape(u *terminalUI) {
	u.input = terminalsession.Decoder{}
	u.input.Feed(27, time.Now().Add(-time.Second))
}
