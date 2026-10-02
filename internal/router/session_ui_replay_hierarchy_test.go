package router

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

// Fixtures deliberately retain latest items and lifecycle that disagree with
// the historical revisions. Only host boundaries and revisions are replayable.
func replayHierarchyFixture(t *testing.T, alter func(*threadJournal)) (*mekugiReplayStore, string, string) {
	t.Helper()
	store, workspace, _, _ := replayJournalTestEvidence(t)
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
	epoch := start.UnixMilli()
	event := func(seq uint64, second int, path, kind, title, state, agent string) journalEvent {
		at := start.Add(time.Duration(second) * time.Second).Format(time.RFC3339Nano)
		n := journalNode{Path: path, Kind: kind, Title: title, State: state, Agent: agent, Author: "/root", Created: journalStamp{Seq: seq, At: at}, Updated: journalStamp{Seq: seq, At: at}}
		if state == "working" {
			n.Started = &journalStamp{Seq: seq, At: at}
		}
		return journalEvent{Seq: seq, At: at, Author: n.Author, Op: "add", Path: path, Fields: n, Transition: true}
	}
	for _, namespace := range []string{workspace, ""} {
		label := "Scoped"
		if namespace == "" {
			label = "Unscoped"
		}
		root := threadJournal{Version: 2, Receipts: map[string]journalReceipt{}, TreeAuthored: true, Workspace: namespace, Thread: "root", Author: "/root", IdentityKnown: true,
			Events: []journalEvent{event(1, 1, "/1", "task", label+" parent", "working", "/root/bound")}}
		for _, name := range []string{"bound", "unbound"} {
			child := threadJournal{Version: 2, Receipts: map[string]journalReceipt{}, TreeAuthored: true, Workspace: namespace, Thread: name, Author: "/root/" + name, Parent: "root", IdentityKnown: true, LifecycleState: "blocked", LifecycleReason: "Latest must not leak", LifecycleAt: start.Add(time.Hour).Format(time.RFC3339Nano),
				Items:  []journalItem{{Path: "/99", Kind: "note", Title: "Latest must not leak"}},
				Events: []journalEvent{event(1, 4, "/1", "task", label+" "+name+" task", "working", ""), event(2, 9, "/1/1", "note", label+" "+name+" outcome", "", "")}}
			for i := range child.Events {
				child.Events[i].Author = child.Author
				child.Events[i].Fields.Author = child.Author
			}
			if alter != nil {
				alter(&child)
			}
			if err := writeThreadJournal(store, child); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeThreadJournal(store, root); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	meta := replayTestMeta("root")
	meta["payload"].(map[string]any)["cwd"] = workspace
	records := []map[string]any{meta, replayTestRecord("event_msg", epoch, map[string]any{"type": "task_started", "turn_id": "root-turn"})}
	for i, name := range []string{"bound", "unbound"} {
		records = append(records, replayTestItem("root", "root-turn", epoch+1000+int64(i)*100, epoch+2000+int64(i)*100, map[string]any{"id": "spawn-" + name, "type": "SubAgentActivity", "agent_thread_id": name, "agent_path": "/root/" + name, "kind": "spawn"}))
		replayTestWrite(t, dir, "rollout-test-"+name+".jsonl", replayTestMeta(name),
			replayTestRecord("event_msg", epoch+3000, map[string]any{"type": "task_started", "turn_id": name + "-turn"}),
			replayTestRecord("event_msg", epoch+10000, map[string]any{"type": "task_complete", "turn_id": name + "-turn"}))
	}
	records = append(records, replayTestRecord("event_msg", epoch+20000, map[string]any{"type": "task_complete", "turn_id": "root-turn"}))
	return store, workspace, replayTestWrite(t, dir, "rollout-test-root.jsonl", records...)
}

func replayHierarchyPlayback(t *testing.T, path string) *uiReplayPlayback {
	t.Helper()
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(p.close)
	p.ui.view.painter.Theme, p.ui.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
	return p
}

func replayHierarchyItem(t *testing.T, j *threadJournal, path string) journalItem {
	t.Helper()
	if j == nil {
		t.Fatal("missing presented journal")
	}
	i := slices.IndexFunc(j.Items, func(item journalItem) bool { return item.Path == path })
	if i < 0 {
		t.Fatalf("missing %s in %+v", path, j.Items)
	}
	return j.Items[i]
}

func TestSessionUIReplayHierarchyHistoricalMountsAndReadOnly(t *testing.T) {
	store, _, path := replayHierarchyFixture(t, nil)
	files := map[string][]byte{}
	entries, err := os.ReadDir(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := filepath.Join(store.directory, entry.Name())
		files[name], err = os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A read must not recreate a writer lock or rewrite any retained journal.
	lock := filepath.Join(store.directory, "store.lock")
	if err := os.Remove(lock); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	delete(files, lock)
	p := replayHierarchyPlayback(t, path)
	for _, second := range []int{2, 5, 11, 2, 11} {
		if err := p.seek(time.Duration(second) * time.Second); err != nil {
			t.Fatal(err)
		}
		for _, sink := range []*nativeJournalSink{p.ui.journal, p.ui.unscopedJournal} {
			j := sink.presented()
			if j == nil {
				t.Fatalf("missing journal: %v", p.source.JournalUnavailable)
			}
			opposite := "Unscoped"
			if sink == p.ui.unscopedJournal {
				opposite = "Scoped"
			}
			if slices.ContainsFunc(j.Items, func(item journalItem) bool { return strings.HasPrefix(item.Title, opposite) }) {
				t.Fatal("journal namespaces mixed")
			}
			if second == 2 {
				if slices.ContainsFunc(j.Items, func(item journalItem) bool {
					return strings.Contains(item.Path, "@bound") || strings.Contains(item.Path, "@unbound")
				}) {
					t.Fatal("backward/early seek exposed future child")
				}
				continue
			}
			for _, mount := range []string{"/1/@bound", "/@agents/@unbound"} {
				root := replayHierarchyItem(t, j, mount)
				want := "working"
				if second == 11 {
					want = "done"
				}
				if root.State != want {
					t.Fatalf("historical lifecycle = %q, want %q", root.State, want)
				}
				replayHierarchyItem(t, j, mount+"/1")
				hasOutcome := slices.ContainsFunc(j.Items, func(item journalItem) bool { return item.Path == mount+"/1/1" })
				if hasOutcome != (second == 11) {
					t.Fatalf("future/historical outcome at %ds = %v", second, hasOutcome)
				}
			}
			if slices.ContainsFunc(j.Items, func(item journalItem) bool { return strings.Contains(item.Title, "Latest must not leak") }) {
				t.Fatal("latest stored items leaked")
			}
		}
	}
	after, err := os.ReadDir(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(files) {
		t.Fatalf("offline replay added/removed files: %d versus %d", len(after), len(files))
	}
	for name, before := range files {
		data, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(before, data) {
			t.Fatalf("source modified: %s: %v", name, err)
		}
	}
}

func TestSessionUIReplayHierarchyRejectsUnprovenAncestry(t *testing.T) {
	cases := map[string]func(*threadJournal){
		"unknown":        func(j *threadJournal) { j.IdentityKnown = false },
		"conflicted":     func(j *threadJournal) { j.IdentityConflicted = true },
		"unrelated":      func(j *threadJournal) { j.Parent = "" },
		"missing-parent": func(j *threadJournal) { j.Parent = "absent" },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, path := replayHierarchyFixture(t, alter)
			p := replayHierarchyPlayback(t, path)
			if err := p.seek(p.until); err != nil {
				t.Fatal(err)
			}
			if len(p.source.JournalUnavailable) == 0 {
				t.Fatal("missing ancestry diagnostic")
			}
			for _, sink := range []*nativeJournalSink{p.ui.journal, p.ui.unscopedJournal} {
				if slices.ContainsFunc(sink.presented().Items, func(item journalItem) bool {
					return strings.Contains(item.Title, "outcome") || strings.Contains(item.Title, " task")
				}) {
					t.Fatal("unproven child mounted")
				}
			}
		})
	}
}

func TestSessionUIReplayHierarchyJournalControlsAreReadOnly(t *testing.T) {
	_, _, path := replayHierarchyFixture(t, nil)
	p := replayHierarchyPlayback(t, path)
	if err := p.seek(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	var host bytes.Buffer
	p.ui.client.Input = replayDiscard{&host}
	p.ui.shell.selectNativePane(4)
	view := &p.ui.journalView
	view.rebuild(p.ui.journalTreeSnapshot())
	view.selected = 0
	before := view.rows[0].open
	key := func(b byte) {
		t.Helper()
		quit, err := p.key(b)
		if err != nil || quit {
			t.Fatalf("key %q quit=%v err=%v", b, quit, err)
		}
	}
	key(' ')
	view.rebuild(p.ui.journalTreeSnapshot())
	if view.rows[0].open == before || p.paused {
		t.Fatal("Space did not expand/collapse without pausing")
	}
	key(' ')
	view.rebuild(p.ui.journalTreeSnapshot())
	for _, pair := range [][2]byte{{'j', 'k'}, {replayDown, replayUp}} {
		before := view.selected
		key(pair[0])
		if view.selected != before+1 {
			t.Fatal("navigation did not move down")
		}
		key(pair[1])
		if view.selected != before {
			t.Fatal("navigation did not move up")
		}
	}
	key('p')
	if !p.paused {
		t.Fatal("p did not pause")
	}
	for _, pair := range [][2]byte{{'d', 27}, {'\r', 'q'}} {
		// Mounted rows would normally open an agent, but replay always opens detail.
		view.rebuild(p.ui.journalTreeSnapshot())
		view.selected = slices.IndexFunc(view.rows, func(row journalPaneRow) bool { return row.node.Path == "/1/@bound" })
		if view.selected < 0 {
			t.Fatal("mounted row missing")
		}
		key(pair[0])
		if p.ui.shell.output == nil {
			t.Fatal("details did not open")
		}
		key('p')
		if p.paused || p.ui.shell.output == nil {
			t.Fatal("p did not resume while keeping details open")
		}
		key('p')
		if !p.paused {
			t.Fatal("p did not pause inside details")
		}
		key(pair[1])
		if p.ui.shell.output != nil {
			t.Fatal("details did not close")
		}
	}
	key('n')
	if !view.unscoped {
		t.Fatal("n did not select unscoped namespace")
	}
	view.expanded = map[string]bool{"/1": false}
	if err := p.seek(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if !p.ui.journalView.unscoped {
		t.Fatal("backward seek lost selected namespace")
	}
	if open, exists := p.ui.journalView.expanded["/1"]; !exists || open {
		t.Fatal("backward seek lost explicit collapsed disclosure")
	}
	if got := replayHierarchyItem(t, p.ui.journalTreeSnapshot(), "/1").Title; got != "Unscoped parent" {
		t.Fatalf("selected namespace after seek = %q", got)
	}
	if host.Len() != 0 {
		t.Fatalf("replay emitted %d host bytes", host.Len())
	}
}

func TestUISnapshotSessionUIReplayHierarchy(t *testing.T) {
	_, _, path := replayHierarchyFixture(t, nil)
	p := replayHierarchyPlayback(t, path)
	if err := p.seek(11 * time.Second); err != nil {
		t.Fatal(err)
	}
	p.paused = true
	p.ui.shell.selectNativePane(4)
	p.ui.journalView.expanded = map[string]bool{"/1": true, "/1/@bound": true, "/1/@bound/1": true, "/@agents": true, "/@agents/@unbound": true, "/@agents/@unbound/1": true}
	uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-hierarchy.txt", replayPlaybackTestPaint(t, p, 140, 32))
}

func TestUISnapshotSessionUIReplayHierarchyRecordedPlanClock(t *testing.T) {
	_, _, path := replayHierarchyFixture(t, nil)
	p := replayHierarchyPlayback(t, path)
	var lines []string
	for _, second := range []int{5, 8, 2} {
		if err := p.seek(time.Duration(second) * time.Second); err != nil {
			t.Fatal(err)
		}
		p.paused = true
		before := p.ui.journalPlanStrip(100)
		if _, err := p.control(0, 24*time.Hour); err != nil {
			t.Fatal(err)
		}
		if after := p.ui.journalPlanStrip(100); after != before {
			t.Fatal("paused clock advanced")
		}
		lines = append(lines, before)
	}
	uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-hierarchy-clock.txt", strings.Join(lines, "\n"))
}
