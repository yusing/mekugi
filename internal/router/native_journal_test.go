package router

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func nativeJournalFixture() threadJournal {
	return threadJournal{Version: 2, TreeAuthored: true, Author: "/root", Items: []journalItem{
		{Path: "/1", ID: "/1", Kind: "task", Title: "Pending parser", State: "pending", Author: "/root"},
		{Path: "/2", ID: "/2", Kind: "task", Title: "Working renderer", State: "working", Author: "/root"},
		{Path: "/3", ID: "/3", Kind: "task", Title: "Blocked wiring", State: "blocked", Reason: "Needs decision", Author: "/root"},
		{Path: "/4", ID: "/4", Kind: "task", Title: "Done scanner", State: "done", Author: "/root"},
		{Path: "/4/1", ID: "/4/1", Kind: "note", Title: "Scanner passed", Author: "/root"},
	}}
}

func TestNativeJournalPaneNarrowWideAndCollapsedSubtree(t *testing.T) {
	journal := nativeJournalFixture()
	view := new(nativeJournalView)
	wide := view.render(&journal, 80, 8, true, false, livediff.DarkTheme)
	joined := ansi.Strip(strings.Join(wide, "\n"))
	for _, required := range []string{"○ /1 Pending parser", "◐ /2 Working renderer", "⚠ /3 Blocked wiring", "Needs decision", "▾ ● /4 Done scanner"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("wide pane missing %q: %q", required, joined)
		}
	}
	if !strings.Contains(joined, "Scanner passed") {
		t.Fatalf("finished subtree should expand when it fits: %q", joined)
	}
	if header := ansi.Strip(wide[0]); header != " ◐ 1 working · ⚠ 1 blocked · ○ 1 pending · ● 1 done" {
		t.Fatalf("pane header counts missing: %q", wide[:2])
	}
	for _, row := range view.render(&journal, 18, 4, true, false, livediff.DarkTheme) {
		if ansi.StringWidth(row) > 18 {
			t.Fatalf("narrow pane overflowed: width=%d row=%q", ansi.StringWidth(row), row)
		}
	}
	if len(view.rows) != 4 {
		t.Fatalf("finished child should be collapsed: %d rows", len(view.rows))
	}
	view.expanded = map[string]bool{"/4": true}
	expanded := ansi.Strip(strings.Join(view.render(&journal, 80, 8, true, false, livediff.DarkTheme), "\n"))
	if !strings.Contains(expanded, "▾ ● /4 Done scanner") || !strings.Contains(expanded, "Scanner passed") {
		t.Fatalf("space-expanded finished subtree missing: %q", expanded)
	}
}

func TestNativeJournalAgentsGroupIsNotAConstraint(t *testing.T) {
	journal := threadJournal{Items: []journalItem{
		{Path: "/1", Kind: "task", Title: "Own task", State: "working"},
		{Path: "/2", Kind: "context", Title: "No new dependencies"},
		{Path: "/@agents", Kind: "context", Title: "Agents"},
		{Path: "/@agents/@child", Kind: "task", Title: "/root/child", Agent: "/root/child", State: "working"},
	}}
	rows := ansi.Strip(strings.Join(new(nativeJournalView).render(&journal, 80, 6, false, false, livediff.DarkTheme), "\n"))
	constraint, task, group := strings.Index(rows, "◆ /2"), strings.Index(rows, "/1 Own task"), strings.Index(rows, "⎇ Agents")
	if strings.Contains(rows, "◆ /@agents") || group < 0 || constraint > task || task > group {
		t.Fatalf("Agents group rendered or ranked as a constraint: %q", rows)
	}
}

func TestNativeJournalSelectionExpansionAndCopyPath(t *testing.T) {
	journal := nativeJournalFixture()
	u, _ := newAppServerTestUI()
	u.journal = &nativeJournalSink{tree: &journal}
	shell := &terminalUI{main: u, focus: 4, journalOpen: true}
	u.shell = shell
	view := &u.journalView
	view.expanded = map[string]bool{"/4": false}
	view.render(&journal, 80, 8, false, true, livediff.DarkTheme)
	if err := shell.journalKey("G"); err != nil || view.rows[view.selected].node.Path != "/4" {
		t.Fatalf("selection did not reach finished task: %d %v", view.selected, err)
	}
	if err := shell.journalKey(" "); err != nil || !view.expanded["/4"] {
		t.Fatalf("space did not expand selected task: %v", err)
	}
	view.render(&journal, 80, 8, false, true, livediff.DarkTheme)
	if err := shell.journalKey("j"); err != nil || view.rows[view.selected].node.Path != "/4/1" {
		t.Fatalf("selection did not enter expanded subtree: %d %v", view.selected, err)
	}
	if err := shell.journalKey("c"); err != nil {
		t.Fatal(err)
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("/4/1")) + "\a"
	if shell.clipboard != want {
		t.Fatalf("copied path differs: %q", shell.clipboard)
	}
}

func TestNativeJournalPlanStripOnlyWithOpenTask(t *testing.T) {
	u, _ := newAppServerTestUI()
	journal := nativeJournalFixture()
	u.journal = &nativeJournalSink{tree: &journal}
	strip := ansi.Strip(u.journalPlanStrip(80))
	if !strings.HasPrefix(strip, "◐ /2 Working renderer") || !strings.HasSuffix(strip, "1/4 done · Ctrl-B 5 journal") || ansi.StringWidth(strip) != 80 {
		t.Fatalf("plan strip without events did not prefer working task: %q", strip)
	}
	mounted := append(slices.Clone(journal.Items), journalItem{Path: "/@agents/@child", Kind: "task", Title: "/root/child", State: "working"},
		journalItem{Path: "/@agents/@child/1", Kind: "task", Title: "Child pending", State: "pending"})
	u.journal.tree = &threadJournal{Items: mounted}
	if strip := ansi.Strip(u.journalPlanStrip(120)); strings.Contains(strip, "Child pending") || !strings.Contains(strip, "1/4 done") {
		t.Fatalf("plan strip counted mounted child tasks: %q", strip)
	}
	u.journal.tree = &journal
	if ansi.StringWidth(u.journalPlanStrip(17)) > 17 {
		t.Fatalf("narrow plan strip overflowed: %q", u.journalPlanStrip(17))
	}
	for i := range journal.Items {
		if journal.Items[i].Kind == "task" {
			journal.Items[i].State = "done"
		}
	}
	if got := u.journalPlanStrip(80); got != "" {
		t.Fatalf("completed plan left pinned strip: %q", got)
	}
}

func TestNativeJournalComposerTypesLeadingCapitalJ(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	journalOpen := u.shell.journalOpen
	t.Cleanup(u.shell.diff.close)
	journal := nativeJournalFixture()
	u.journal = &nativeJournalSink{tree: &journal}
	for _, key := range []string{"J", "u", "s", "t"} {
		if err := u.shell.send(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.draft != "Just" || u.shell.focus != 0 || u.shell.journalOpen != journalOpen {
		t.Fatalf("leading J was taken as a pane key: draft=%q focus=%d", u.draft, u.shell.focus)
	}
}

func TestNativeJournalShortTerminalKeepsContentAndNavigation(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	journal := nativeJournalFixture()
	u.journal = &nativeJournalSink{tree: &journal}
	u.shell.focus, u.shell.journalOpen = 4, true
	var frame bytes.Buffer
	if err := u.paint(&frame, 60, 6); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(frame.String()), "Working renderer") || u.shell.layout.journal.h == 0 {
		t.Fatalf("short terminal hid focused Journal: %q", ansi.Strip(frame.String()))
	}
	if err := u.shell.journalKey("j"); err != nil || u.journalView.selected != 1 {
		t.Fatalf("short terminal selection failed: %d %v", u.journalView.selected, err)
	}
}

func TestNativeJournalEmptyOutcomeCardsRemainDistinctUntilOutputAcknowledged(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	u, _ := newAppServerTestUI()
	u.proxy = proxy
	u.thread = transform.shellThreadID
	u.journal = proxy.journals.attachNative(workspace, u.thread)
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	journal, exists, err := readThreadJournal(proxy.replayStore, workspace, u.thread)
	if err != nil || !exists {
		t.Fatalf("native journal unavailable: %v %v", exists, err)
	}
	journal.TreeAuthored = true
	journal.Sequence = 1
	journal.Events = []journalEvent{{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Path: "/1", Kind: "task", Title: "Pending", State: "pending"}}}
	journal.Items = []journalItem{{Path: "/1", ID: "/1", Kind: "task", Title: "Pending", State: "pending"}}
	u.journal.publish(journal, true, "empty-a")
	u.journal.publish(journal, true, "empty-b")
	publications := u.journal.snapshot()
	var cards int
	for _, publication := range publications {
		if publication.card != nil {
			cards++
		}
	}
	if cards != 2 || !u.journal.pending["card:empty-a"].terminal || !u.journal.pending["card:empty-b"].terminal {
		t.Fatalf("consecutive empty Outcome cards coalesced: %+v", publications)
	}
	u.applyPendingJournal()
	before, _, err := readThreadJournal(proxy.replayStore, workspace, u.thread)
	if err != nil || before.FlushSeq != 0 {
		t.Fatalf("enqueue acknowledged card before UI output: seq=%d err=%v", before.FlushSeq, err)
	}
	var frame bytes.Buffer
	if err := u.paint(&frame, 80, 24); err != nil {
		t.Fatal(err)
	}
	if frame.Len() == 0 || len(u.journal.snapshot()) == 0 {
		t.Fatal("paint failed or publication vanished before successful output acknowledgement")
	}
}

func seedNativeJournalPreview(p *nativePreview) {
	p.steps = nil
	proxy := p.ui.proxy
	proxy.replayStore = p.store
	if err := proxy.journals.initialize(p.t.Context(), p.store, p.workspace, "main", "/root", ""); err != nil {
		p.t.Fatal(err)
	}
	p.ui.journal = proxy.journals.attachNative(p.workspace, "main")
	p.t.Cleanup(func() { proxy.journals.detachNative(p.ui.journal) })
	if _, err := proxy.journals.apply(p.t.Context(), p.store, p.workspace, "main", "", []journalMutation{
		{Op: "add", Kind: "context", Title: new("Keep Codex as execution authority")},
		{Op: "add", Kind: "task", Title: new("Parser"), State: new("working")},
		{Op: "log", P: "/2", Text: new("Tokenizer and AST validated")},
		{Op: "set", P: "/2", State: new("done")},
		{Op: "add", Kind: "task", Title: new("Renderer"), State: new("working")},
		{Op: "set", P: "/3", Agent: "/root/renderer"},
		{Op: "log", P: "/3", Text: new("Task tree and turn card use the shared renderer")},
		{Op: "add", Kind: "task", Title: new("CLI wiring"), State: new("blocked"), Reason: new("Waiting for interface review")},
	}); err != nil {
		p.t.Fatal(err)
	}
	if err := proxy.journals.initialize(p.t.Context(), p.store, p.workspace, "preview-child", "/root/renderer", ""); err != nil {
		p.t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(p.t.Context(), p.store, p.workspace, "main", "", "/root", true); err != nil {
		p.t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(p.t.Context(), p.store, p.workspace, "preview-child", "main", "/root/renderer", true); err != nil {
		p.t.Fatal(err)
	}
	if _, err := proxy.journals.apply(p.t.Context(), p.store, p.workspace, "preview-child", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Layout engine"), State: new("working")}, {Op: "log", Text: new("Grid measurer reused; no new dependency")}}); err != nil {
		p.t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(p.t.Context(), p.store, p.workspace, "preview-child", "working", ""); err != nil {
		p.t.Fatal(err)
	}
	journal, exists, err := readThreadJournal(p.store, p.workspace, "main")
	if err != nil || !exists {
		p.t.Fatalf("preview journal: %v", err)
	}
	p.ui.journal.publish(journal, true, "journal-preview")
	p.ui.applyPendingJournal()
	p.ui.shell.focus, p.ui.shell.journalOpen = 4, true
}

func TestNativeJournalNamespacesDoNotHideUndisplayedNotes(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	workspace, unscoped := nativeJournalFixture(), nativeJournalFixture()
	unscoped.Items[1].Title = "Unscoped task"
	u.journal = &nativeJournalSink{workspace: "/workspace", tree: &workspace}
	u.unscopedJournal = &nativeJournalSink{tree: &unscoped}
	u.shell.layout.journal, u.shell.journalOpen, u.shell.focus = terminalRect{0, 0, 80, 20}, true, 4
	publication := nativeJournalPublication{event: &journalEvent{Seq: 1, Fields: journalNode{Kind: "note"}}, item: journalItem{ID: "event:1", Text: "Unscoped result", Updated: 1}}
	u.applyJournalPublication(u.unscopedJournal, publication)
	if len(u.view.entries) != 1 || u.view.entries[0].Text != "Unscoped result" || u.journalPanePresents(u.unscopedJournal) {
		t.Fatal("unscoped note was hidden or eligible for pane-only acknowledgement")
	}
	if err := u.shell.journalKey("n"); err != nil {
		t.Fatal(err)
	}
	if !u.journalPanePresents(u.unscopedJournal) || u.journalPanePresents(u.journal) || !strings.Contains(u.journalPlanStrip(100), "Unscoped task") {
		t.Fatal("namespace switch did not select the other tree and strip")
	}
	u.applyJournalPublication(u.unscopedJournal, nativeJournalPublication{event: publication.event, item: journalItem{ID: "event:2", Text: "Pane-only result", Updated: 2}})
	if len(u.view.entries) != 1 {
		t.Fatal("visible namespace note also appeared in Main")
	}
	// A key closed the pane after the last paint; its stale layout must not
	// hide the next note.
	u.shell.journalOpen, u.shell.focus = false, 0
	u.applyJournalPublication(u.unscopedJournal, nativeJournalPublication{event: publication.event, item: journalItem{ID: "event:3", Text: "After close", Updated: 3}})
	if len(u.view.entries) != 2 {
		t.Fatal("note published after the pane closed was shown nowhere")
	}
}

func TestNativeJournalTranscriptRowsAreOneLineTransitions(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Parser"), Body: new("Long task body"), State: new("working")},
		{Op: "set", P: "/1", Title: new("Parser rewrite")},
		{Op: "log", P: "/1", Text: new("Tests passed\n\nNote body")},
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := newAppServerTestUI()
	u.journal = proxy.journals.attachNative(workspace, transform.shellThreadID)
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	if err := proxy.journals.restoreNative(t.Context(), proxy.replayStore, u.journal); err != nil {
		t.Fatal(err)
	}
	u.applyPendingJournal()
	var rows []string
	for _, entry := range u.view.entries {
		if entry.Kind == "journal_event" {
			rows = append(rows, entry.Text)
		}
	}
	if len(rows) != 2 || !strings.HasSuffix(rows[0], "◐ /1 Parser") || !strings.HasSuffix(rows[1], "Tests passed") {
		t.Fatalf("transcript rows were not one-line transitions and notes: %q", rows)
	}
	if len(u.journal.snapshot()) != 3 {
		t.Fatal("the title edit must stay pending until acknowledged")
	}
}

func TestNativeJournalDurableMultilineDetails(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{
		{Op: "log", Text: new("Validation passed\n\n**Important detail**\n\nSecond paragraph")},
	})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	u.journal = proxy.journals.attachNative(workspace, transform.shellThreadID)
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	if err := newJournalStore().restoreNative(t.Context(), proxy.replayStore, u.journal); err != nil {
		t.Fatal(err)
	}
	if err := u.shell.journalKey("d"); err != nil || u.shell.output == nil {
		t.Fatalf("restored detail did not open: %v", err)
	}
	u.shell.output.layout(40)
	page := u.shell.output.laid
	var rendered strings.Builder
	for _, line := range page.Lines {
		rendered.WriteString(ansi.Strip(line.Text))
	}
	for _, want := range []string{"Validation passed", "Important detail", "Second paragraph"} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("detail lost %q: %q", want, rendered.String())
		}
	}
}

func TestNativeJournalCollapsedCardLeadsWithOutcomeAndAggregatesTasks(t *testing.T) {
	v := newLiveActivityView()
	parser := journalNode{Path: "/1", Kind: "task", Title: "Parser"}
	state := func(node journalNode, state string) journalNode { node.State = state; return node }
	card := &nativeJournalCard{Journal: threadJournal{Events: []journalEvent{
		{Seq: 1, Op: "add", Path: "/1", Fields: state(parser, "pending"), Transition: true},
		{Seq: 2, Op: "set", Path: "/1", Fields: state(parser, "working"), Transition: true},
		{Seq: 3, Op: "add", Path: "/1/1", Fields: journalNode{Path: "/1/1", Kind: "note", Title: "Tests passed"}},
		{Seq: 4, Op: "set", Path: "/1", Fields: state(parser, "done"), Transition: true},
		{Seq: 5, Op: "add", Path: "/2", Fields: journalNode{Path: "/2", Kind: "task", Title: "Mistake", State: "pending"}, Transition: true},
		{Seq: 6, Op: "remove", Path: "/2", Fields: journalNode{Path: "/2", Kind: "task", Title: "Mistake", State: "pending"}},
		{Seq: 7, Op: "add", Path: "/3", Fields: journalNode{Path: "/3", Kind: "answer", Body: "OUTCOME TEXT"}},
	}}}
	entry := activityPaneEntry{Seq: 1, journalCard: card, native: &liveActivityNativeItem{}}
	var out conversationLines
	v.journalCardLines(&out, entry, 90)
	text := ansi.Strip(strings.Join(out.lines, "\n"))
	outcome, turn := strings.Index(text, "OUTCOME TEXT"), strings.Index(text, "This turn")
	if outcome < 0 || turn < outcome || strings.Count(text, "/1 Parser") != 1 || !strings.Contains(text, "● /1 Parser") || !strings.Contains(text, "1 note ·") {
		t.Fatalf("collapsed card did not lead with Outcome and aggregate tasks: %q", text)
	}
	if strings.Contains(text, "Mistake") {
		t.Fatalf("collapsed card showed a node added and removed in one window: %q", text)
	}
}

func TestNativeJournalCardDialogShowsCardRowsAndFullNotes(t *testing.T) {
	v := newLiveActivityView()
	task := journalNode{Path: "/1", Kind: "task", Title: "Tighten spacing"}
	state := func(node journalNode, state string) journalNode { node.State = state; return node }
	card := &nativeJournalCard{Journal: threadJournal{Events: []journalEvent{
		{Seq: 1, Op: "set", Path: "/1", Fields: state(task, "working"), Transition: true},
		{Seq: 2, Op: "add", Path: "/1/1", Fields: journalNode{Path: "/1/1", Kind: "note", Title: "Note", Body: "Removed the blank row.\nFocused suite passed."}},
		{Seq: 3, Op: "set", Path: "/1", Fields: state(task, "done"), Transition: true},
		{Seq: 4, Op: "add", Path: "/2", Fields: journalNode{Path: "/2", Kind: "answer", Body: "Fixed both."}},
	}, Items: []journalItem{{Path: "/3", ID: "/3", Kind: "task", Title: "Follow-up", State: "pending"}}}}
	observed := time.Date(2026, 9, 30, 23, 28, 0, 0, time.Local)
	entry := activityPaneEntry{Seq: 1, Agent: "Main", Kind: "journal_card", Observed: observed, journalCard: card, native: &liveActivityNativeItem{}}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	u := &terminalUI{}
	if !u.openEntry(v, 1) || u.output == nil {
		t.Fatal("journal card did not open")
	}
	page := v.painter.DialogPage(u.output.pages[0], 70)
	var lines []string
	for _, line := range page.Lines {
		lines = append(lines, ansi.Strip(line.Text))
	}
	body := strings.Join(lines, "\n")
	if detail := ansi.Strip(page.Detail); detail != "23:28 · 1 done · 1 open · 1 note" {
		t.Fatalf("detail row = %q", detail)
	}
	if strings.HasPrefix(body, "Journal") || !strings.HasPrefix(body, "Fixed both.") {
		t.Fatalf("dialog did not lead with the Outcome: %q", body)
	}
	if strings.Count(body, "/1 Tighten spacing") != 1 || !strings.Contains(body, "● /1 Tighten spacing") || !strings.Contains(body, "○ /3 Follow-up") {
		t.Fatalf("dialog did not aggregate tasks into card rows: %q", body)
	}
	if !strings.Contains(body, "◆ Removed the blank row.\n  Focused suite passed.") || strings.Contains(body, "click to open") {
		t.Fatalf("dialog did not write the note out: %q", body)
	}
	if !strings.HasPrefix(page.Text, "Journal") || !strings.Contains(page.Text, "Focused suite passed.") {
		t.Fatalf("copied text lost the plain card: %q", page.Text)
	}
}

func TestNativeJournalCollapsedCardShowsRemoval(t *testing.T) {
	v := newLiveActivityView()
	card := &nativeJournalCard{Journal: threadJournal{Events: []journalEvent{{Seq: 1, Op: "remove", Path: "/1", Fields: journalNode{Path: "/1", Kind: "task", Title: "Old task", State: "working"}}}}}
	entry := activityPaneEntry{Seq: 1, journalCard: card, native: &liveActivityNativeItem{}}
	var out conversationLines
	v.journalCardLines(&out, entry, 70)
	text := ansi.Strip(strings.Join(out.lines, "\n"))
	if !strings.Contains(text, "⊖ /1 Old task · removed") || strings.Contains(text, "◐ /1") {
		t.Fatalf("collapsed card misrepresented removal: %q", text)
	}
}

func nativeJournalMouse(t *testing.T, u *appServerUI, button, x, y int) {
	t.Helper()
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<%d;%d;%dM", button, x+1, y+1)); err != nil {
		t.Fatal(err)
	}
}

func nativeJournalPaintedPane(t *testing.T, u *appServerUI) []string {
	t.Helper()
	var frame bytes.Buffer
	if err := u.paint(&frame, 120, 24); err != nil {
		t.Fatal(err)
	}
	r := u.shell.layout.journal
	return u.journalView.render(u.journalTreeSnapshot(), r.w, r.h, false, u.shell.focus == 4, u.view.painter.Theme)
}

func nativeJournalMarked(rows []string, fill string) []int {
	var marked []int
	for i, row := range rows {
		if strings.Contains(row, fill) {
			marked = append(marked, i)
		}
	}
	return marked
}

func TestNativeJournalMarksOnlyPointerOrKeyboardCursor(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	journal := nativeJournalFixture()
	u.journal = &nativeJournalSink{tree: &journal}
	u.shell.focus, u.shell.journalOpen = 4, true
	fill := u.view.painter.Theme.SelectionBackground()
	if marked := nativeJournalMarked(nativeJournalPaintedPane(t, u), fill); len(marked) != 0 {
		t.Fatalf("selection marked without pointer or keyboard: %v", marked)
	}
	if err := u.shell.journalKey("j"); err != nil {
		t.Fatal(err)
	}
	if marked := nativeJournalMarked(nativeJournalPaintedPane(t, u), fill); !slices.Equal(marked, []int{u.journalView.selected}) || u.journalView.selected != 1 {
		t.Fatalf("keyboard cursor not marked: %v selected=%d", marked, u.journalView.selected)
	}
	u.shell.focus = 0
	if marked := nativeJournalMarked(nativeJournalPaintedPane(t, u), fill); len(marked) != 0 {
		t.Fatalf("unfocused pane kept its cursor mark: %v", marked)
	}
	u.shell.focus = 4
	nativeJournalPaintedPane(t, u)
	r := u.shell.layout.journal
	nativeJournalMouse(t, u, 35, r.x+5, r.y+3)
	if marked := nativeJournalMarked(nativeJournalPaintedPane(t, u), fill); !slices.Equal(marked, []int{3}) || u.journalView.selected != 1 {
		t.Fatalf("pointer did not replace the cursor mark or moved the selection: %v selected=%d", marked, u.journalView.selected)
	}
	nativeJournalMouse(t, u, 35, r.x-3, r.y+3)
	if marked := nativeJournalMarked(nativeJournalPaintedPane(t, u), fill); len(marked) != 0 {
		t.Fatalf("mark stayed after the pointer left: %v", marked)
	}
}

func TestNativeJournalClickTogglesDisclosureOpensRowAndWheelScrolls(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	journal := nativeJournalFixture()
	u.journal = &nativeJournalSink{tree: &journal}
	u.shell.focus, u.shell.journalOpen = 4, true
	u.journalView.expanded = map[string]bool{"/4": false}
	nativeJournalPaintedPane(t, u)
	r := u.shell.layout.journal
	finished := slices.IndexFunc(u.journalView.rows, func(row journalPaneRow) bool { return row.node.Path == "/4" })
	nativeJournalMouse(t, u, 0, r.x+1, r.y+finished)
	if !u.journalView.expanded["/4"] || u.shell.output != nil {
		t.Fatalf("disclosure click did not only expand: %v", u.journalView.expanded)
	}
	rows := ansi.Strip(strings.Join(nativeJournalPaintedPane(t, u), "\n"))
	if !strings.Contains(rows, "▾ ● /4") || !strings.Contains(rows, "└ · Scanner passed") {
		t.Fatalf("expanded subtree missing: %q", rows)
	}
	nativeJournalMouse(t, u, 0, r.x+12, r.y+finished)
	if u.shell.output == nil || !u.journalView.expanded["/4"] {
		t.Fatal("row click did not open details")
	}
	u.shell.outputKey("\x1b")

	for i := range 30 {
		path := fmt.Sprintf("/%d", 10+i)
		journal.Items = append(journal.Items, journalItem{Path: path, ID: path, Kind: "task", Title: "Filler", State: "pending"})
	}
	nativeJournalPaintedPane(t, u)
	selected := u.journalView.selected
	nativeJournalMouse(t, u, 65, r.x+5, r.y+2)
	nativeJournalPaintedPane(t, u)
	if u.journalView.offset != 3 || u.journalView.selected != selected || u.shell.output != nil {
		t.Fatalf("wheel moved selection or did not scroll: offset=%d selected=%d/%d", u.journalView.offset, u.journalView.selected, selected)
	}
}

func TestNativeJournalHintsNamespaceOnlyWithBothJournals(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	workspace, unscoped := nativeJournalFixture(), nativeJournalFixture()
	u.journal = &nativeJournalSink{workspace: "/workspace", tree: &workspace}
	u.shell.focus, u.shell.journalOpen = 4, true
	var frame bytes.Buffer
	if err := u.paint(&frame, 140, 24); err != nil {
		t.Fatal(err)
	}
	screen := ansi.Strip(frame.String())
	if !strings.Contains(screen, "space expand · d details") || strings.Contains(screen, "namespace") || strings.Contains(screen, "workspace") {
		t.Fatalf("single journal named a namespace or lost pane hints: %q", screen)
	}
	status := ansi.Strip(u.shell.nativeStatus())
	for _, repeated := range []string{"details", "expand", "namespace"} {
		if strings.Contains(status, repeated) {
			t.Fatalf("status bar repeats pane hint %q: %q", repeated, status)
		}
	}
	if err := u.shell.journalKey("n"); err != nil || u.journalView.unscoped {
		t.Fatalf("n switched to an absent namespace: %v", err)
	}
	u.unscopedJournal = &nativeJournalSink{tree: &unscoped}
	if hints := ansi.Strip(u.journalPaneHints()); !strings.HasSuffix(hints, "n unscoped") {
		t.Fatalf("namespace hint missing with both journals: %q", hints)
	}
	if err := u.shell.journalKey("n"); err != nil {
		t.Fatal(err)
	}
	if current, other := u.journalNamespaces(); current != "unscoped" || other != "workspace" {
		t.Fatalf("namespace switch: %q %q", current, other)
	}
}

func nativeJournalEvent(seq uint64, at time.Time, op, path, kind, title, state, body string) journalEvent {
	return journalEvent{Seq: seq, At: at.UTC().Format(time.RFC3339Nano), Op: op, Path: path, Transition: kind == "task",
		Fields: journalNode{Path: path, Kind: kind, Title: title, State: state, Body: body}}
}

func TestNativeJournalTranscriptGroupsAdjacentChanges(t *testing.T) {
	v := newLiveActivityView()
	v.conversation = true
	now := time.Now()
	note := "Change-records confirmed FIXME recovery; owns selection/composition and namespace-wide stream IDs. Reports existing independent finalization."
	for _, event := range []journalEvent{
		nativeJournalEvent(1, now, "set", "/3", "task", "activity-dialogs", "working", ""),
		nativeJournalEvent(2, now, "add", "/7", "task", "Integrate batches and validate", "pending", ""),
		nativeJournalEvent(3, now, "add", "/3/1", "note", note, "", "Detail body"),
		nativeJournalEvent(4, now, "set", "/4", "task", "shell-boundaries", "blocked", ""),
	} {
		e := event
		v.applyTreeJournal("main", nativeJournalPublication{event: &e, item: journalItem{ID: fmt.Sprintf("event:%d", e.Seq), Text: journalRowText(e)}})
	}
	feed := v.renderFeed(60, 40)
	plain := make([]string, len(feed.lines))
	for i, line := range feed.lines {
		plain[i] = ansi.Strip(line)
		if ansi.StringWidth(line) > 60 {
			t.Fatalf("row overflowed: %q", plain[i])
		}
	}
	text := strings.Join(plain, "\n")
	if strings.Count(text, "◆ journal") != 1 || strings.Contains(text, "\n\n") {
		t.Fatalf("adjacent changes did not share one compact item:\n%s", text)
	}
	for _, want := range []string{"│ ◐ /3 activity-dialogs · started", "│ ○ /7 Integrate batches and validate · added", "│ ⚠ /4 shell-boundaries · blocked", "│ ◆ Change-records"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	noteRow := slices.IndexFunc(plain, func(row string) bool { return strings.Contains(row, "◆ Change-records") })
	if next := plain[noteRow+1]; !strings.HasPrefix(next, "│   ") {
		t.Fatalf("wrapped note did not hang under its text: %q", next)
	}
	snippet := feed.snippets[noteRow]
	block, ok := v.snippetBlock(snippet)
	if !ok || !strings.Contains(block.Body, "Detail body") {
		t.Fatalf("note row does not open its detail: %+v %v", block, ok)
	}
	if feed.snippets[noteRow-1] != (liveActivitySnippet{}) {
		t.Fatal("a task without detail must not claim a click")
	}
}

func TestNativeJournalStripPinsNewestChange(t *testing.T) {
	u, _ := newAppServerTestUI()
	now := time.Now()
	journal := threadJournal{Version: 2, TreeAuthored: true, Items: []journalItem{
		{Path: "/1", Kind: "task", Title: "Parser", State: "done"},
		{Path: "/2", Kind: "task", Title: "Renderer", State: "working", Started: &journalStamp{At: now.Add(-time.Minute).Format(time.RFC3339Nano)}},
		{Path: "/3", Kind: "task", Title: "Docs", State: "pending"},
	}, Events: []journalEvent{
		nativeJournalEvent(1, now, "set", "/1", "task", "Parser", "done", ""),
		nativeJournalEvent(2, now, "set", "/2", "task", "Renderer", "working", ""),
		nativeJournalEvent(3, now, "add", "/2/1", "note", "Progress", "", ""),
	}}
	u.journal = &nativeJournalSink{tree: &journal}
	strip := ansi.Strip(u.journalPlanStrip(80))
	if !strings.HasPrefix(strip, "◐ /2 Renderer · started · 1m") || !strings.HasSuffix(strip, "1/3 done · Ctrl-B 5 journal") {
		t.Fatalf("strip did not pin the newest task change: %q", strip)
	}
	journal.Events = append(journal.Events, nativeJournalEvent(4, now, "add", "/4", "task", "Review", "pending", ""))
	journal.Items = append(journal.Items, journalItem{Path: "/4", Kind: "task", Title: "Review", State: "pending"})
	if strip := ansi.Strip(u.journalPlanStrip(80)); !strings.HasPrefix(strip, "○ /4 Review · added") {
		t.Fatalf("a created task did not take the pin: %q", strip)
	}
	for i := range journal.Items {
		journal.Items[i].State = "done"
	}
	if strip := u.journalPlanStrip(80); strip != "" {
		t.Fatalf("finished plan stayed pinned after the turn: %q", strip)
	}
	u.turn = "t"
	if strip := ansi.Strip(u.journalPlanStrip(80)); !strings.HasPrefix(strip, "● /4 Review") {
		t.Fatalf("an active turn lost its last change: %q", strip)
	}
}

func TestNativeJournalPinReplacesOlderReplyPin(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"t"}}}`)
	send := func(id, text string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": id, "type": "agentMessage", "text": text}})
	}
	send("reply", "Older Main response")
	for i := range 20 {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": fmt.Sprint(i), "type": "commandExecution", "command": fmt.Sprint("echo activity-", i)}})
	}
	later := time.Now().Add(time.Second)
	journal := threadJournal{Version: 2, TreeAuthored: true, Items: []journalItem{{Path: "/1", Kind: "task", Title: "Renderer", State: "working"}},
		Events: []journalEvent{nativeJournalEvent(1, later, "set", "/1", "task", "Renderer", "working", "")}}
	u.journal = &nativeJournalSink{tree: &journal}
	frame, _ := u.mainFrame(80, 24, 0)
	text := ansi.Strip(strings.Join(frame, "\n"))
	if u.view.feedTop != 1 || strings.Contains(text, "Older Main response") || !strings.Contains(text, "◐ /1 Renderer · started") {
		t.Fatalf("newer journal state did not replace the pinned reply:\n%s", text)
	}
	time.Sleep(10 * time.Millisecond)
	journal.Events[0].At = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	send("newer", "Newer Main response")
	for i := range 20 {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": fmt.Sprint("more-", i), "type": "commandExecution", "command": fmt.Sprint("echo more-", i)}})
	}
	frame, _ = u.mainFrame(80, 24, 0)
	if u.view.feedTop <= 1 || !strings.Contains(ansi.Strip(frame[1]), "Newer Main response") {
		t.Fatalf("a newer reply did not win the pin back:\n%s", strings.Join(frame, "\n"))
	}
}
