package router

import (
	"bytes"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
	wide := view.render(&journal, 80, 8)
	joined := ansi.Strip(strings.Join(wide, "\n"))
	for _, required := range []string{"○ /1 Pending parser", "◐ /2 Working renderer", "⚠ /3 Blocked wiring", "Needs decision", "▸ ● /4 Done scanner"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("wide pane missing %q: %q", required, joined)
		}
	}
	if strings.Contains(joined, "Scanner passed") {
		t.Fatalf("finished subtree expanded by default: %q", joined)
	}
	if header := ansi.Strip(wide[0]); header != "working 1 · blocked 1 · pending 1 · done 1" || !strings.HasPrefix(ansi.Strip(wide[1]), "───") {
		t.Fatalf("pane header counts or separator missing: %q", wide[:2])
	}
	for _, row := range view.render(&journal, 18, 4) {
		if ansi.StringWidth(row) > 18 {
			t.Fatalf("narrow pane overflowed: width=%d row=%q", ansi.StringWidth(row), row)
		}
	}
	if len(view.rows) != 4 {
		t.Fatalf("finished child should be collapsed: %d rows", len(view.rows))
	}
	view.expanded = map[string]bool{"/4": true}
	expanded := ansi.Strip(strings.Join(view.render(&journal, 80, 8), "\n"))
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
	rows := ansi.Strip(strings.Join(new(nativeJournalView).render(&journal, 80, 6), "\n"))
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
	view.render(&journal, 80, 8)
	if err := shell.journalKey("G"); err != nil || view.rows[view.selected].node.Path != "/4" {
		t.Fatalf("selection did not reach finished task: %d %v", view.selected, err)
	}
	if err := shell.journalKey(" "); err != nil || !view.expanded["/4"] {
		t.Fatalf("space did not expand selected task: %v", err)
	}
	view.render(&journal, 80, 8)
	if err := shell.journalKey("j"); err != nil || view.rows[view.selected].node.Path != "/4/1" {
		t.Fatalf("selection did not enter expanded subtree: %d %v", view.selected, err)
	}
	if err := shell.journalKey("\r"); err != nil {
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
	if !strings.Contains(strip, "◐ /2 Working renderer") || !strings.Contains(strip, "Ctrl-B 5: journal") {
		t.Fatalf("plan strip did not prefer working task: %q", strip)
	}
	if !strings.Contains(strip, "▸ /1 Pending parser") {
		t.Fatalf("plan strip did not show the next pending task: %q", strip)
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
	t.Cleanup(u.shell.diff.close)
	journal := nativeJournalFixture()
	u.journal = &nativeJournalSink{tree: &journal}
	for _, key := range []string{"J", "u", "s", "t"} {
		if err := u.shell.send(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.draft != "Just" || u.shell.focus != 0 || u.shell.journalOpen {
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
	page := u.view.painter.DialogPage(u.shell.output.pages[0], 40)
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

func TestNativeJournalCollapsedCardShowsRemoval(t *testing.T) {
	v := newLiveActivityView()
	card := &nativeJournalCard{Journal: threadJournal{Events: []journalEvent{{Seq: 1, Op: "remove", Path: "/1", Fields: journalNode{Path: "/1", Kind: "task", Title: "Old task", State: "working"}}}}}
	entry := activityPaneEntry{Seq: 1, journalCard: card, native: &liveActivityNativeItem{}}
	var out conversationLines
	v.journalCardLines(&out, entry, 70)
	text := ansi.Strip(strings.Join(out.lines, "\n"))
	if !strings.Contains(text, "Removed /1 Old task") || strings.Contains(text, "◐ /1") {
		t.Fatalf("collapsed card misrepresented removal: %q", text)
	}
}
