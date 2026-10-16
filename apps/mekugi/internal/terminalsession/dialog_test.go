package terminalsession

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/terminalui"
)

func paintDialogTest(t *testing.T, d *Dialog, page terminalui.DialogPage, width, height int) string {
	t.Helper()
	d.SetDocument(page, d.Prepare(width, height))
	rows := make([]string, height)
	d.Paint(rows, width, height, new(terminalui.Painter))
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	for y, row := range rows {
		if _, err := fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row); err != nil {
			t.Fatal(err)
		}
	}
	return ansi.Strip(screen.Render())
}

func dialogTestDocument(live bool, count int) terminalui.DialogPage {
	page := terminalui.DialogPage{Title: "Output", Live: live}
	for i := range count {
		page.Add(terminalui.DialogLine{Number: i + 1, Gutter: "┆", Text: fmt.Sprintf("line %02d", i)})
	}
	return page
}

func dialogTestKeys(d *Dialog, keys ...string) {
	for _, key := range keys {
		d.Key(key)
	}
}

func TestDialogPageStateByStableIdentity(t *testing.T) {
	var d Dialog
	a, b := DialogTab{ID: "a", Name: "A", Live: true}, DialogTab{ID: "b", Name: "B", Live: true}
	d.SetPages([]DialogTab{a, b})
	page := dialogTestDocument(true, 40)
	paintDialogTest(t, &d, page, 80, 18)
	dialogTestKeys(&d, "g", "/", "line 01", "\r", "g", "j", "/", "draft A")
	d.ShowPage(1)
	paintDialogTest(t, &d, page, 80, 18)
	if !d.follow || d.top == 0 || d.Typing || d.query != "" {
		t.Fatalf("new page inherited another page's state: %+v", d)
	}
	dialogTestKeys(&d, "/", "line 39", "\r", "G", "/", "draft B")
	bTop := d.top
	d.SetPages([]DialogTab{b, a})
	if d.Page != 0 || d.top != bTop || !d.follow || !d.Typing || d.draft != "draft B" || d.query != "line 39" || d.match != 39 {
		t.Fatal("membership reorder lost the active page or its state")
	}
	d.ShowPage(1)
	paintDialogTest(t, &d, page, 80, 18)
	if d.top != 1 || d.follow || !d.Typing || d.draft != "draft A" || d.query != "line 01" || d.match != 1 {
		t.Fatal("returning to a page lost its independent scroll/search state")
	}
	d.ShowPage(0)
	if d.top != bTop || !d.follow || d.draft != "draft B" || d.query != "line 39" {
		t.Fatal("returning to the live page lost its independent state")
	}
}

func TestDialogSearchWrappedNumberedLogicalLines(t *testing.T) {
	for _, gutter := range []string{"│", "┆"} {
		t.Run(gutter, func(t *testing.T) {
			var d Dialog
			d.SetPages([]DialogTab{{ID: "document"}})
			page := terminalui.DialogPage{Title: "Search"}
			page.Add(terminalui.DialogLine{Number: 100, Gutter: gutter, Text: strings.Repeat("prefix 界 ", 160) + "Needle" + strings.Repeat(" tail", 160)})
			page.Add(terminalui.DialogLine{Number: 101, Gutter: gutter, Text: "Needle"})
			if frame := paintDialogTest(t, &d, page, 80, 18); strings.Contains(frame, "Needle") {
				t.Fatal("fixture already shows the distant match")
			}
			dialogTestKeys(&d, "/", "nEeDlE", "\r")
			frame := paintDialogTest(t, &d, page, 80, 18)
			firstTop := d.top
			if d.match != 0 || d.missed || firstTop == 0 || !strings.Contains(frame, "Needle") {
				t.Fatalf("search did not reveal the wrapped logical line: match=%d top=%d", d.match, d.top)
			}
			d.Key("n")
			if frame := paintDialogTest(t, &d, page, 80, 18); d.match != 1 || !strings.Contains(frame, "Needle") {
				t.Fatal("next did not reveal the next logical match")
			}
			d.Key("N")
			paintDialogTest(t, &d, page, 80, 18)
			if d.match != 0 || d.top != firstTop {
				t.Fatal("previous did not return to the same wrapped row")
			}
			dialogTestKeys(&d, "/", "absent", "\r")
			if d.match != -1 || !d.missed || d.top != firstTop {
				t.Fatal("missed search changed the viewport or retained a match")
			}
		})
	}
}

func TestDialogFollowPauseAndResumeOnDocumentUpdate(t *testing.T) {
	var d Dialog
	d.SetPages([]DialogTab{{ID: "live", Live: true}})
	if frame := paintDialogTest(t, &d, dialogTestDocument(true, 30), 80, 18); !d.follow || d.top == 0 || !strings.Contains(frame, "line 29") {
		t.Fatal("live document did not follow its latest row")
	}
	d.Key("g")
	if frame := paintDialogTest(t, &d, dialogTestDocument(true, 31), 80, 18); d.follow || d.top != 0 || strings.Contains(frame, "line 30") {
		t.Fatal("document update moved the paused viewport")
	}
	d.Key("G")
	if frame := paintDialogTest(t, &d, dialogTestDocument(true, 31), 80, 18); !d.follow || !strings.Contains(frame, "line 30") {
		t.Fatal("End did not resume following")
	}
	if frame := paintDialogTest(t, &d, dialogTestDocument(false, 31), 80, 18); !strings.Contains(frame, "line 30") || d.Document.Live {
		t.Fatal("settled document lost its final rows")
	}
}

func TestDialogPaintGeometryWideAndNarrow(t *testing.T) {
	for _, width := range []int{50, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			var d Dialog
			d.SetPages([]DialogTab{{ID: "document"}})
			const height = 20
			frame := paintDialogTest(t, &d, dialogTestDocument(false, 1), width, height)
			if !strings.Contains(frame, "line 00") {
				t.Fatal("document was absent from the rendered frame")
			}
			if width < 60 {
				if d.rect != (Rect{0, 0, width, height}) {
					t.Fatalf("narrow dialog did not fill the terminal: %+v", d.rect)
				}
			} else if d.rect.W != 90 || d.rect.X != 5 || d.rect.Y <= 0 || d.rect.H >= height {
				t.Fatalf("wide dialog did not center a compact surface: %+v", d.rect)
			}
			if got := strings.Index(strings.Split(frame, "\n")[d.rect.Y], "╭"); got != d.rect.X {
				t.Fatalf("rendered border starts at %d, want %d", got, d.rect.X)
			}
		})
	}
}

func TestDialogFiveTabsSub16CellPathBudget(t *testing.T) {
	var d Dialog
	var tabs []DialogTab
	for i := range 5 {
		tabs = append(tabs, DialogTab{ID: fmt.Sprint(i), Name: "Read", Path: fmt.Sprintf("/workspace/long/directory/source%d.go", i)})
	}
	d.SetPages(tabs)
	frame := paintDialogTest(t, &d, dialogTestDocument(false, 1), 138, 20)
	row := ansi.Cut(strings.Split(frame, "\n")[d.rect.Y+2], d.rect.X+2, d.rect.X+122)
	if len(d.tabs) != 5 {
		t.Fatalf("120-column tabs: got %d visible pages, want 5", len(d.tabs))
	}
	for _, tab := range d.tabs {
		prefix := fmt.Sprintf(" %d Read", tab.page+1)
		room := tab.to - tab.from - ansi.StringWidth(prefix) - 2
		if room != 15 {
			t.Fatalf("fixture must exercise a sub-16-cell path budget, got %d", room)
		}
		if cell, want := ansi.Cut(row, tab.from, tab.to), terminalui.TruncatePath(tabs[tab.page].Path, room); !strings.Contains(cell, want) {
			t.Fatalf("tab dropped its path: want %q in %q", want, cell)
		}
	}
}
