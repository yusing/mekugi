package router

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func nativeJournalSliceFixture() threadJournal {
	return threadJournal{Version: 2, TreeAuthored: true, Author: "/root", Items: []journalItem{
		{Path: "/1", ID: "/1", Kind: "task", Title: "First slice", State: "done", Body: strings.Repeat("Root context that wraps in narrow terminals.\n", 24)},
		{Path: "/1/1", ID: "/1/1", Kind: "task", Title: "Closed branch", State: "done", Body: "Branch body"},
		{Path: "/1/1/1", ID: "/1/1/1", Kind: "note", Title: "Nested evidence", Body: "Deep evidence body"},
		{Path: "/1/2", ID: "/1/2", Kind: "note", Title: "Selected evidence", Body: "Selected evidence body\nSecond evidence line"},
		{Path: "/1/3", ID: "/1/3", Kind: "context", Title: "Tail context", Body: strings.Repeat("Tail context body\n", 24)},
		{Path: "/10", ID: "/10", Kind: "task", Title: "Other slice", State: "pending", Body: "Excluded sibling body"},
		{Path: "/10/1", ID: "/10/1", Kind: "note", Title: "Other evidence", Body: "Excluded sibling evidence"},
	}}
}

func nativeJournalSliceUI(t *testing.T, journal *threadJournal) *appServerUI {
	t.Helper()
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	u.journal = &nativeJournalSink{tree: journal}
	u.shell.focus, u.shell.journalOpen = 4, true
	return u
}

func nativeJournalSliceNode(t *testing.T, journal *threadJournal, path string) journalNode {
	t.Helper()
	for _, item := range journal.Items {
		if item.Path == path {
			return item.node()
		}
	}
	t.Fatalf("missing fixture node %s", path)
	return journalNode{}
}

func nativeJournalSliceFrame(u *terminalUI, width, height int) []string {
	rows := make([]string, height)
	u.paintOutput(rows, width, height)
	return rows
}

func TestUISnapshotNativeJournalSliceDialogIncludesCollapsedDescendantsOnly(t *testing.T) {
	journal := nativeJournalSliceFixture()
	u := nativeJournalSliceUI(t, &journal)
	u.journalView.expanded = map[string]bool{"/1": false}
	nativeJournalPaintedPane(t, u)
	if slices.ContainsFunc(u.journalView.rows, func(row journalPaneRow) bool { return row.node.Path == "/1/1/1" }) {
		t.Fatal("fixture's closed descendant unexpectedly visible in pane")
	}
	row := slices.IndexFunc(u.journalView.rows, func(row journalPaneRow) bool { return row.node.Path == "/1" })
	r := u.shell.layout.journal
	nativeJournalMouse(t, u, 0, r.x+12, r.y+row)
	d := u.shell.output
	if d == nil {
		t.Fatal("slice click did not open dialog")
	}
	nativeJournalSliceFrame(u.shell, 90, 20)
	if len(d.pages) != 1 || len(d.segments) != 5 || len(d.segmentLines) != 5 {
		t.Fatalf("slice must be one page with five segments: pages=%d segments=%d ranges=%v", len(d.pages), len(d.segments), d.segmentLines)
	}
	assertNativeJournalDialogSnapshot(t, "journal-slice-dialog", d.laid)
	if d.top != 0 || d.follow || !d.flashUntil.IsZero() {
		t.Fatalf("root click should start at top without flash: top=%d follow=%v flash=%v", d.top, d.follow, d.flashUntil)
	}
	last := -1
	for _, span := range d.segmentLines {
		if span[0] <= last || span[1] < span[0] {
			t.Fatalf("segments are not distinct ordered ranges: %v", d.segmentLines)
		}
		if heading := ansi.Strip(d.laid.Lines[span[0]].Text); !strings.HasPrefix(heading, "─ /") {
			t.Fatalf("segment has no visible path separator: %q", heading)
		}
		last = span[1]
	}
}

func TestNativeJournalSliceDescendantNavigationFlashesAndExpires(t *testing.T) {
	journal := nativeJournalSliceFixture()
	u := nativeJournalSliceUI(t, &journal)
	u.shell.openJournalRow(nativeJournalSliceNode(t, &journal, "/1/2"))
	d := u.shell.output
	if d == nil {
		t.Fatal("descendant click did not open slice")
	}
	frame := nativeJournalSliceFrame(u.shell, 90, 18)
	if len(d.pages) != 1 || len(d.segments) != 5 || len(d.segmentLines) != 5 {
		t.Fatalf("descendant did not open whole slice: pages=%d segments=%d ranges=%v", len(d.pages), len(d.segments), d.segmentLines)
	}
	selected := slices.IndexFunc(d.segments, func(segment activityui.Block) bool {
		return strings.Contains(ansi.Strip(segment.Body), "Selected evidence body")
	})
	if selected < 0 {
		t.Fatal("selected segment missing")
	}
	span := d.segmentLines[selected]
	if d.top != d.starts[span[0]] || d.follow || d.pendingSegment != 0 {
		t.Fatalf("descendant navigation not resolved at its start: top=%d start=%d follow=%v pending=%d", d.top, d.starts[span[0]], d.follow, d.pendingSegment)
	}
	fill := u.view.painter.Theme.SelectionBackground()
	visible := strings.Join(frame, "\n")
	if !strings.Contains(ansi.Strip(visible), "Selected evidence body") || !strings.Contains(visible, fill) || d.flashUntil.IsZero() {
		t.Fatalf("selected descendant did not visibly flash: %q", visible)
	}
	if d.expireFlash(d.flashUntil.Add(-time.Nanosecond)) {
		t.Fatal("flash expired before deadline")
	}
	if !d.expireFlash(d.flashUntil) || !d.flashUntil.IsZero() {
		t.Fatal("flash deadline did not clear flash and request repaint")
	}
	restored := strings.Join(nativeJournalSliceFrame(u.shell, 90, 18), "\n")
	if strings.Contains(restored, fill) || ansi.Strip(restored) != ansi.Strip(visible) {
		t.Fatal("expiry changed content or left selected segment highlighted")
	}
	if d.expireFlash(time.Now()) {
		t.Fatal("already expired flash requested another repaint")
	}
	d.navigate(span[0], span[1], true)
	d.navigate(span[0], span[1], false)
	if !d.flashUntil.IsZero() || d.follow {
		t.Fatal("flash=false navigation flashed or resumed follow")
	}
}

func TestUISnapshotNativeJournalSliceDialogUsesSelectedNamespace(t *testing.T) {
	workspace, unscoped := nativeJournalSliceFixture(), nativeJournalSliceFixture()
	for i := range unscoped.Items {
		unscoped.Items[i].Title = "Unscoped " + unscoped.Items[i].Title
		unscoped.Items[i].Body = "Unscoped " + unscoped.Items[i].Body
	}
	u := nativeJournalSliceUI(t, &workspace)
	u.unscopedJournal = &nativeJournalSink{tree: &unscoped}
	u.journalView.unscoped = true
	u.shell.openJournalRow(nativeJournalSliceNode(t, &unscoped, "/1/2"))
	if u.shell.output == nil {
		t.Fatal("unscoped descendant did not open")
	}
	nativeJournalSliceFrame(u.shell, 90, 18)
	assertNativeJournalDialogSnapshot(t, "journal-slice-unscoped", u.shell.output.laid)
}

func TestUISnapshotNativeJournalSliceDialogNavigationAfterNarrowResize(t *testing.T) {
	journal := nativeJournalSliceFixture()
	u := nativeJournalSliceUI(t, &journal)
	u.shell.openJournalDetail(nativeJournalSliceNode(t, &journal, "/1/2"))
	if u.shell.output == nil {
		t.Fatal("descendant did not open")
	}
	nativeJournalSliceFrame(u.shell, 100, 20)
	d := u.shell.output
	for _, width := range []int{32, 100, 24} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			nativeJournalSliceFrame(u.shell, width, 20)
			if len(d.segmentLines) != 5 {
				t.Fatalf("resize lost segments: %v", d.segmentLines)
			}
			selected := slices.IndexFunc(d.segments, func(segment activityui.Block) bool {
				return strings.Contains(ansi.Strip(segment.Body), "Selected evidence body")
			})
			if selected < 0 {
				t.Fatal("resize lost selected segment")
			}
			span := d.segmentLines[selected]
			d.navigate(span[0], span[1], false)
			nativeJournalSliceFrame(u.shell, width, 20)
			if d.top != d.starts[span[0]] || d.follow {
				t.Fatalf("resize navigation used stale wrapped rows: top=%d start=%d", d.top, d.starts[span[0]])
			}
			assertNativeJournalSnapshot(t, fmt.Sprintf("journal-slice-resized-%d", width), d.body)
		})
	}
}

func TestUISnapshotNativeJournalOrdinaryReplyContextPreservesTargets(t *testing.T) {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	question := activityPaneEntry{Seq: 7, Agent: "You", Kind: "text", Text: "What was the journal issue?", Observed: time.Date(2026, 9, 30, 8, 16, 39, 0, time.Local)}
	entry := activityPaneEntry{Seq: 8, Agent: "Main", Kind: "text", Text: "The batch was rejected without partial updates.", Observed: question.Observed, native: &liveActivityNativeItem{question: question.Seq}}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{question, entry}})
	for _, width := range []int{90, 32} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			out := v.conversationItem(1, 1, width, conversationThread{})
			assertNativeJournalSnapshot(t, fmt.Sprintf("journal-reply-context-%d", width), out.lines)
			links := 0
			for i, line := range out.lines {
				plain := ansi.Strip(line)
				if ansi.StringWidth(line) > width {
					t.Fatalf("ordinary reply overflowed width %d: %q", width, plain)
				}
				if out.questions[i] == question.Seq {
					links++
				}
				if strings.Contains(plain, "↩ re:") && out.questions[i] != question.Seq {
					t.Fatal("reply header lost original question click target")
				}
			}
			if links < 2 {
				t.Fatalf("reply header and excerpt targets lost: %v", out.questions)
			}
		})
	}
}

func TestNativeJournalSliceDescendantTerminalClickScrollAndEscape(t *testing.T) {
	const width, height = 120, 28
	journal := nativeJournalSliceFixture()
	u := nativeJournalSliceUI(t, &journal)
	u.journalView.expanded = map[string]bool{"/1": true, "/1/1": true}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: width, Rows: height}); err != nil {
		t.Fatal(err)
	}
	frames := make(chan []byte, 4)
	go func() {
		var pending synchronizedFrameBuffer
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			pending.Append(buffer[:n])
			for {
				frame, ok := pending.Next()
				if !ok {
					break
				}
				frames <- bytes.Clone(frame)
			}
			if err != nil {
				return
			}
		}
	}()
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	paint := func() string {
		t.Helper()
		if err := u.paint(slave, width, height); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			if _, err := screen.Write(frame); err != nil {
				t.Fatal(err)
			}
			return screen.String()
		case <-time.After(2 * time.Second):
			t.Fatal("PTY journal slice frame did not finish")
			return ""
		}
	}
	if frame := paint(); !strings.Contains(frame, "Selected evidence") {
		t.Fatalf("expanded pane omitted descendant before click:\n%s", frame)
	}
	selected := slices.IndexFunc(u.journalView.rows, func(row journalPaneRow) bool { return row.node.Path == "/1/2" })
	if selected < 0 {
		t.Fatal("expanded pane has no descendant click target")
	}
	r := u.shell.layout.journal
	nativeJournalMouse(t, u, 0, r.x+12, r.y+selected-u.journalView.offset)
	if u.shell.output == nil {
		t.Fatal("pane mouse click did not open slice dialog")
	}
	paneOffset, paneSelected, mainOffset, focus := u.journalView.offset, u.journalView.selected, u.view.offset, u.shell.focus
	toggled := maps.Clone(u.journalView.expanded)
	if frame := paint(); !strings.Contains(frame, "Selected evidence body") || strings.Contains(frame, "Excluded sibling body") {
		t.Fatalf("PTY click did not render selected slice segment:\n%s", frame)
	}
	d := u.shell.output
	before := d.top
	nativeJournalMouse(t, u, 65, d.rect.x+3, d.rect.y+3)
	paint()
	if d.top <= before || u.journalView.offset != paneOffset || u.journalView.selected != paneSelected || u.view.offset != mainOffset {
		t.Fatalf("dialog wheel did not scroll in isolation: dialog=%d/%d pane=%d/%d selection=%d/%d main=%d/%d", d.top, before, u.journalView.offset, paneOffset, u.journalView.selected, paneSelected, u.view.offset, mainOffset)
	}
	u.shell.outputKey("\x1b")
	frame := paint()
	if u.shell.output != nil || !u.shell.journalOpen || u.shell.focus != focus || u.journalView.offset != paneOffset || u.journalView.selected != paneSelected || u.view.offset != mainOffset || !maps.Equal(u.journalView.expanded, toggled) {
		t.Fatal("Escape changed pane navigation or disclosure state")
	}
	if !strings.Contains(frame, "Selected evidence") || strings.Contains(frame, "Selected evidence body") {
		t.Fatalf("Escape did not restore pane instead of modal:\n%s", frame)
	}
}
