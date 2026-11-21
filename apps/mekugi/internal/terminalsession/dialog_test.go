package terminalsession

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	pathterminal "github.com/yusing/mekugi/libraries/pathdisplay/terminal"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

func paintDialogTest(t *testing.T, d *Dialog, page terminalui.DialogPage, width, height int) string {
	t.Helper()
	d.SetDocument(page, d.Prepare(width, height))
	rows := make([]string, height)
	d.Paint(rows, Rect{W: width, H: height}, new(terminalui.Painter))
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

func TestDialogPaintDocksFittedSheet(t *testing.T) {
	for _, width := range []int{50, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			var d Dialog
			d.SetPages([]DialogTab{{ID: "document"}})
			const height = 20
			frame := paintDialogTest(t, &d, dialogTestDocument(false, 1), width, height)
			// A one-line document keeps the minimum three body rows.
			if want := (Rect{0, height - d.Chrome() - 3, width, d.Chrome() + 3}); d.rect != want {
				t.Fatalf("sheet rect %+v, want %+v docked at the bottom", d.rect, want)
			}
			rows := strings.Split(frame, "\n")
			if !strings.HasPrefix(rows[d.rect.Y], "  ── ") || !strings.HasSuffix(strings.TrimRight(rows[d.rect.Y], " "), "[×] ──") {
				t.Fatalf("heading row %q is not a panel rule", rows[d.rect.Y])
			}
			if !strings.HasPrefix(rows[d.bodyRect().Y], "  ") || !strings.Contains(rows[d.bodyRect().Y], "line 00") {
				t.Fatalf("body row %q is not inset at the body rect", rows[d.bodyRect().Y])
			}
		})
	}
	var d Dialog
	d.SetPages([]DialogTab{{ID: "document"}})
	if paintDialogTest(t, &d, dialogTestDocument(false, 40), 80, 20); d.rect != (Rect{0, 0, 80, 20}) {
		t.Fatalf("long document did not take the whole area: %+v", d.rect)
	}
}

func TestDialogFiveTabsSub16CellPathBudget(t *testing.T) {
	var d Dialog
	var tabs []DialogTab
	for i := range 5 {
		tabs = append(tabs, DialogTab{ID: fmt.Sprint(i), Name: "Read", Path: fmt.Sprintf("/workspace/long/directory/source%d.go", i)})
	}
	d.SetPages(tabs)
	frame := paintDialogTest(t, &d, dialogTestDocument(false, 1), 124, 20)
	row := ansi.Cut(strings.Split(frame, "\n")[d.rect.Y+1], d.rect.X+2, d.rect.X+122)
	if len(d.tabs) != 5 {
		t.Fatalf("120-column tabs: got %d visible pages, want 5", len(d.tabs))
	}
	for _, tab := range d.tabs {
		prefix := fmt.Sprintf(" %d Read", tab.page+1)
		room := tab.to - tab.from - ansi.StringWidth(prefix) - 2
		if room != 15 {
			t.Fatalf("fixture must exercise a sub-16-cell path budget, got %d", room)
		}
		if cell, want := ansi.Cut(row, tab.from, tab.to), pathterminal.Truncate(tabs[tab.page].Path, room); !strings.Contains(cell, want) {
			t.Fatalf("tab dropped its path: want %q in %q", want, cell)
		}
	}
}

func TestDialogPaintLongLineAllocationBound(t *testing.T) {
	var allocations []float64
	for _, size := range []int{16 << 10, 1 << 20} {
		var d Dialog
		page := terminalui.DialogPage{Text: strings.Repeat("x", size)}
		page.Add(terminalui.DialogLine{Number: 1, Gutter: "│", Text: page.Text})
		d.SetDocument(page, d.Prepare(100, 24))
		rows := make([]string, 24)
		paint := func() {
			clear(rows)
			d.Paint(rows, Rect{W: 100, H: 24}, new(terminalui.Painter))
		}
		allocations = append(allocations, testing.AllocsPerRun(3, paint))
		d.Key("G")
		paint()
		if d.top != d.bottom() || !strings.Contains(ansi.Strip(d.body[len(d.body)-1]), "x") || d.Key("y").Text != page.Text {
			t.Fatal("bounded paint lost tail scrolling or full-source copy")
		}
	}
	t.Logf("allocations/paint: 16KiB=%.0f, 1MiB=%.0f", allocations[0], allocations[1])
	if allocations[1] > allocations[0]+16 {
		t.Fatal("paint allocations grew with offscreen wrapped rows")
	}
}

func TestDialogSelectionResolvesOffscreenFileLinks(t *testing.T) {
	var d Dialog
	painter := terminalui.Painter{DeferFileLinks: true, FileLink: func(string) bool { return true }}
	line := painter.Markdown("[target](sample.go)", 80, false)[0]
	var page terminalui.DialogPage
	for range 60 {
		page.Add(terminalui.DialogLine{Text: line})
	}
	d.FileLinks = map[string]bool{"sample.go": true}
	d.SetDocument(page, d.Prepare(100, 24))
	rows := make([]string, 24)
	d.Paint(rows, Rect{W: 100, H: 24}, &painter)
	x, y := d.rect.X+2, d.rect.Y+d.Chrome()-1
	d.Mouse(0, x, y, false)
	d.Mouse(32, x+2, y, false)
	d.Mouse(0, x+2, y, true)
	if d.Selection == nil || d.Selection.ScreenRows() == 0 {
		t.Fatal("selection did not retain offscreen rows")
	}
	d.Key("\x1b[F")
	if d.Selection == nil || d.Selection.Top() == 0 {
		t.Fatal("selection did not scroll beyond its original viewport")
	}
	d.Paint(rows, Rect{W: 100, H: 24}, &painter)
	for _, row := range d.body {
		if strings.Contains(row, "mekugi-file-candidate:") {
			t.Fatal("offscreen selection emitted a private file-link destination")
		}
	}
}

func TestFrameRenderKeepsTranscriptLinksWhenSelectionHiddenByDialog(t *testing.T) {
	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	main.Update(sessionpresentation.View{Main: thread, Displayed: thread, Version: sessionpresentation.Version{Attachment: 1}})
	transcript := activityui.NewView()
	transcript.Painter.DeferFileLinks = true
	transcript.Painter.FileLink = func(string) bool { return true }
	transcript.Update(&activitypresentation.Publication{Entries: []activitypresentation.Entry{{
		Facts:  activitypresentation.Facts{Seq: 1, Agent: "Main", Kind: "final", Text: "Candidate choices: types.ts"},
		Values: []activitypresentation.Value{{Content: activitymodel.Content{Kind: "text", Body: "Candidate choices: types.ts"}, Source: 1}},
	}}}, activityui.Display{Caller: "/root"})
	agents := activityui.NewView()
	dialog := &ContentDialog{View: transcript}
	dialog.Update(sessionpresentation.DetailView{Pages: []sessionpresentation.DetailItem{{
		DetailPage: activitypresentation.DetailPage{ID: "read", Verb: "Read", Body: "export type X = string;"},
	}}})
	var shell Shell
	var live previewview.PreviewPane
	frame := Frame{Width: 80}
	sel := NewSelection(Surface{Rect: Rect{0, 0, 80, 10}, Rows: make([]string, 18)}, 1, 1)
	rendered, err := frame.Render(context.Background(), FrameInput{
		Main:       &main,
		Prompts:    &prompts,
		Transcript: transcript,
		Agents:     agents,
		Shell:      &shell,
		Dialog:     dialog,
		Live:       &live,
		Selection:  sel,
		Width:      80,
		Height:     18,
		Now:        time.Unix(100, 0),
		FileLinks:  map[string]bool{"types.ts": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rendered.Rows, "\n"), "]8;;types.ts") {
		t.Fatalf("hidden selection under dialog stripped transcript hyperlink:\n%s", strings.Join(rendered.Rows, "\n"))
	}
}
