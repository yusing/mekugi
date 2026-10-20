package router

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/execsegment"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestObservedEditReceiptBeforeHostItemSurvivesSegmentRefresh(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	file := mekugi.RenderReviewFile("a.go", "a.go", "before\n", "after\n")
	// The tracker sees host starts on the transport reader. Its completion
	// can reach the preview mailbox before the UI consumes that same item.
	u.shell.preview(diffview.Preview{ID: "early", Workspace: u.session.cwd, Thread: "main", Complete: true,
		HostItems: []string{"cmd"}, Files: []mekugi.ReviewFile{file}, EditReceipt: "Edit `a.go` +1 -1 · python3"})
	command := "python3 - <<'PY'\np='a.go';s=open(p).read().replace('before','after');open(p,'w').write(s)\nPY\ngo test ./..."
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": appServerItem{ID: "cmd", Type: "commandExecution", Command: command, Status: "inProgress"}})
	for _, view := range []*liveActivityView{u.view, u.agents} {
		for _, entry := range view.entries {
			if entry.native == nil || entry.native.item != "cmd" {
				continue
			}
			native := *entry.native
			native.phase = "item/commandExecution/outputDelta"
			native.capturedEdit = nil // Host refresh carries no router receipt.
			native.segments = []commandSegment{{source: "python3", text: "Edit `a.go` · python3 (requested)"}, {source: "go test ./...", text: "Run `go test ./...`", running: true}}
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 99, Agent: entry.Agent, Kind: "tool", CallID: entry.CallID, Text: entry.Text, native: &native}}})
			if !u.shell.openActivityEdit(view, entry.Seq, "a.go") || !strings.Contains(u.shell.output.pages[0].Code, "+after") {
				t.Fatal("early observed source was lost")
			}
			u.shell.outputKey("\x1b")
			for _, block := range view.entries[0].blocks {
				if block.EditSource != "" && (block.EditOutcome != "" || strings.Contains(block.EditSource, "requested")) {
					t.Fatalf("stale edit status survived receipt: %+v", block)
				}
			}
		}
	}
}

func trackedEditReceiptData(thread string, calls ...string) *liveDiffData {
	data := newLiveDiffData()
	data.order = []string{"receipt"}
	data.attempts["receipt"] = liveDiffAttempt{receipt: &capturedActivityEdit{
		thread: thread, calls: calls,
		text: "Edit `a.go` +3 -1 · cat\n\nEdit `b.go` +1 -2 · cat",
	}}
	return data
}

func assertTrackedReceiptStats(t *testing.T, view *liveActivityView, entry int) {
	t.Helper()
	blocks := view.entries[entry].blocks
	counts := map[string][2]int{}
	var frame []string
	for _, block := range blocks {
		if path, added, removed, _, ok := activityui.EditStat(block.Label); ok {
			if _, duplicate := counts[path]; duplicate {
				t.Fatalf("duplicate receipt for %s: %+v", path, blocks)
			}
			counts[path] = [2]int{added, removed}
			if block.StatScale != 4 || block.ExitCode != 0 || block.Skipped || block.Running {
				t.Fatalf("captured counts lost bars or borrowed segment failure: %+v", block)
			}
			frame = append(frame, view.painter.Block(block, 120)...)
		}
	}
	if !reflect.DeepEqual(counts, map[string][2]int{"a.go": {3, 1}, "b.go": {1, 2}}) {
		t.Fatalf("captured stats missing from rendered blocks: %+v", blocks)
	}
	painted := strings.Join(frame, "\n")
	if !strings.Contains(ansi.Strip(painted), "+3 -1") || !strings.Contains(ansi.Strip(painted), "+1 -2") || !strings.Contains(painted, activityui.Green+"━") || !strings.Contains(painted, activityui.Red+"━") {
		t.Fatalf("counts not painted with diff colors: %q", painted)
	}
	if reparsed := parseLiveActivity(view.entries[entry].activityPaneEntry); !reflect.DeepEqual(reparsed, blocks) {
		t.Fatalf("reparse discarded corrected projection:\n%+v\n%+v", blocks, reparsed)
	}
}

func TestAppServerTrackedEditReceiptPreservesSegments(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			view := u.view
			if thread == "child" {
				view = u.agents
			}
			sources := []string{"cat > a.go", "mv b.go.new b.go", "go test ./...", "mv failed.go.new failed.go", "mv skipped.go.new skipped.go"}
			var segments []commandSegment
			for _, source := range sources {
				segments = append(segments, commandSegment{source: source, text: execSegmentText(source)})
			}
			output := u.session.outputs.New()
			output.Finish(new("edit diagnostic"), new(0))
			segments[0].output = output
			segments[2].exit, segments[2].tail = 2, []string{"test failed"}
			segments[3].exit, segments[3].tail = 1, []string{"permission denied"}
			segments[4].skipped = true
			command := strings.Join(sources, "; ")
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
				Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: toolActivityShell(command),
				native: &liveActivityNativeItem{thread: thread, item: "cmd", command: command, segments: segments},
			}}})
			data := trackedEditReceiptData(thread, "cmd")
			for range 2 {
				view.applyCapturedEdits(data)
				assertTrackedReceiptStats(t, view, 0)
				var outputKept, testKept, failedKept, skippedKept bool
				for _, block := range view.entries[0].blocks {
					switch {
					case block.Code == sources[0]:
						t.Fatalf("successful edit source leaked into Activity: %+v", block)
					case block.Verb == "Edited" && block.Output == output:
						outputKept = block.ExitCode == 0 && block.Segment && block.Label == "`a.go`" && block.EditSource == ""
					case block.Verb == "Run" && block.Code == sources[2]:
						testKept = block.ExitCode == 2 && strings.Join(block.Tail, "") == "test failed"
					case block.Verb == "Move" && strings.Contains(block.Label, "failed.go"):
						failedKept = block.ExitCode == 1 && block.EditOutcome == "failed" && strings.Join(block.Tail, "") == "permission denied"
					case block.Verb == "Move" && strings.Contains(block.Label, "skipped.go"):
						skippedKept = block.Skipped && block.EditOutcome == "skipped"
					case (block.Verb == "Edit" || block.Verb == "Move") && strings.Contains(block.Label, "(requested)"):
						t.Fatalf("successful intent survived receipt: %+v", block)
					}
				}
				if !outputKept || !testKept || !failedKept || !skippedKept || len(view.entries[0].blocks) != 6 {
					t.Fatalf("receipt lost segment output/state: %+v", view.entries[0].blocks)
				}
			}
		})
	}
}

// Each successful edit command's output and timing follow its own Edited row;
// its source appears only in the output dialog, which the output row opens.
func TestUISnapshotTrackedEditReceiptOutputSourceOnlyInDialog(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var segments []commandSegment
	for i, source := range []string{"cat > a.go", "go vet ./...", "cat > b.go"} {
		line := strings.Fields(source)[len(strings.Fields(source))-1] + " diagnostic"
		output := u.session.outputs.New()
		output.Finish(new(line), new(0))
		elapsed := time.Duration(i+1) * time.Second
		segments = append(segments, commandSegment{source: source, text: execSegmentText(source), output: output, tail: []string{line},
			timing: execsegment.Timing{Started: started, Ended: started.Add(elapsed), ElapsedNS: int64(elapsed)}})
	}
	command := "cat > a.go; go vet ./...; cat > b.go"
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 1, Agent: "/root/child", Kind: "tool", CallID: "cmd", Text: toolActivityShell(command),
		native: &liveActivityNativeItem{thread: "child", item: "cmd", command: command, segments: segments},
	}}})
	u.agents.applyCapturedEdits(trackedEditReceiptData("child", "cmd"))
	var outputs []string
	for _, block := range u.agents.entries[0].blocks {
		if block.Output == nil {
			continue
		}
		if block.Code == "cat > a.go" || block.Code == "cat > b.go" || block.EditSource != "" {
			t.Fatalf("edit output row leaked source or edit navigation: %+v", block)
		}
		outputs = append(outputs, block.Verb+" "+block.Label+" "+block.Duration.String())
	}
	if want := []string{"Edited `a.go` 1s", "Run  2s", "Edited `b.go` 3s"}; !slices.Equal(outputs, want) {
		t.Fatalf("output placement = %q, want %q", outputs, want)
	}
	pages := u.agents.commandOutputPages(1)
	if len(pages) != 3 || pages[0].Code != "cat > a.go" || pages[2].Code != "cat > b.go" {
		t.Fatalf("edit output dialog: %+v", pages)
	}
	feed := u.agents.renderFeed(100, 20)
	assertNativeUISnapshot(t, "captured-edit-output", feed.lines)
	row := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(ansi.Strip(line), "a.go diagnostic") })
	if row < 0 || feed.snippets[row].block == editNavigationSnippet || !u.shell.openOutput(u.agents, feed.snippets[row]) {
		t.Fatalf("output row does not open its output: row %d", row)
	}
	rows := make([]string, 12)
	u.shell.paintOutput(rows, 100, len(rows))
	assertNativeUISnapshot(t, "captured-edit-output-dialog", rows)
}

func TestAppServerTrackedEditReceiptExactAnchor(t *testing.T) {
	view := newLiveActivityView()
	add := func(seq uint64, thread, item string) {
		source := "cat > a.go"
		view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
			Seq: seq, Agent: thread, Kind: "tool", CallID: thread + ":" + item, Text: toolActivityShell(source),
			native: &liveActivityNativeItem{thread: thread, item: item, command: source,
				segments: []commandSegment{{source: source, text: execSegmentText(source)}},
			},
		}}})
	}
	add(1, "main", "sibling")
	add(2, "child", "anchor")
	add(3, "main", "unrelated")
	data := trackedEditReceiptData("main", "anchor", "sibling")
	view.applyCapturedEdits(data)
	if len(view.entries[0].blocks) != 0 {
		t.Fatalf("grouped sibling duplicated aggregate: %+v", view.entries[0].blocks)
	}
	for _, i := range []int{1, 2} {
		if len(view.entries[i].blocks) != 1 || view.entries[i].blocks[0].EditOutcome != "ran" {
			t.Fatalf("receipt changed unrelated thread/item: %+v", view.entries[i].blocks)
		}
		if _, _, _, _, ok := activityui.EditStat(view.entries[i].blocks[0].Label); ok {
			t.Fatal("unrelated command acquired receipt counts")
		}
	}
	add(4, "main", "anchor")
	for range 2 {
		view.applyCapturedEdits(data)
		assertTrackedReceiptStats(t, view, 3)
		if len(view.entries[0].blocks) != 0 {
			t.Fatal("late anchor revived sibling stats")
		}
	}
}

func TestAppServerTrackedEditReceiptRestored(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, workspace)
			u.proxy = &mekugiProxy{replayStore: store}
			item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "bash -lc 'cat > a.go; cat > b.go; go test ./...'", ExitCode: new(2), AggregatedOutput: new("test failed")}
			output := u.session.outputs.New()
			output.Finish(new("test failed"), new(2))
			u.retainCommandSegments(activityPaneEntry{native: &liveActivityNativeItem{thread: thread, turn: "turn", item: item.ID}}, item,
				execTrackView{complete: true, output: true, code: 2, segments: []commandSegment{{}, {}, {exit: 2, output: output}}})
			awaitCommandSegments(t, u)
			restored := newAppServerSessionTestUI(t, workspace)
			restored.proxy = &mekugiProxy{replayStore: &mekugiReplayStore{directory: store.directory}}
			restored.shell.diff.data = trackedEditReceiptData(thread, item.ID)
			turns := []appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}}
			view := restored.view
			if thread == "main" {
				restored.restoreHistory(turns)
			} else {
				restored.session.path(thread)
				restored.restoreActivityThread(appServerThreadInfo{ID: thread, Cwd: workspace, Turns: turns})
				view = restored.agents
			}
			if len(view.entries) != 1 || view.entries[0].native == nil || len(view.entries[0].native.segments) != 3 {
				t.Fatalf("tracked history missing: %+v", view.entries)
			}
			for range 2 {
				restored.applyCapturedEdits()
				assertTrackedReceiptStats(t, view, 0)
				last := view.entries[0].blocks[len(view.entries[0].blocks)-1]
				if last.Verb != "Run" || last.ExitCode != 2 || last.Output == nil || strings.Join(last.Output.View().Lines, "\n") != "test failed" {
					t.Fatalf("restored neighbor lost retained failure/output: %+v", last)
				}
			}
		})
	}
}

func TestAppServerTrackedEditReceiptKeepsSiblingCombinedOutput(t *testing.T) {
	for _, tail := range [][]string{nil, {"host diagnostic"}} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		output := u.session.outputs.New()
		output.Finish(new("host diagnostic"), new(0))
		command := "cat > a.go; cat > b.go"
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
			Seq: 1, Agent: "Main", Kind: "tool", CallID: "sibling", Text: toolActivityShell(command), outputTail: tail,
			native: &liveActivityNativeItem{thread: "main", item: "sibling", command: command, output: output, segments: []commandSegment{
				{source: "cat > a.go", text: execSegmentText("cat > a.go")},
				{source: "cat > b.go", text: execSegmentText("cat > b.go")},
			}},
		}}})
		data := trackedEditReceiptData("main", "anchor", "sibling")
		for range 2 {
			u.view.applyCapturedEdits(data)
			blocks := u.view.entries[0].blocks
			if len(blocks) != 1 || blocks[0].Verb != "Edited" || blocks[0].Code != "" || blocks[0].Output != output || !reflect.DeepEqual(blocks[0].Tail, tail) {
				t.Fatalf("grouped sibling lost combined host output: %+v", blocks)
			}
			if page := u.view.painter.DialogPage(blocks[0], 100); page.Text != "host diagnostic" {
				t.Fatalf("combined output dialog lost content: %q", page.Text)
			}
		}
	}
}

// Observed edits can cross both live-card clipping and more than one preview
// mailbox batch before the UI receives their host Activity identities.
func TestObservedEditReceiptsBeforeHostRows(t *testing.T) {
	for _, tc := range []struct {
		name         string
		calls, lines int
	}{
		{"large clipped source", 1, 4000}, {"delayed host burst", 33, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			broker := newLiveDiffBroker(t.Context())
			broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{u.session.cwd: {"main": true}}})
			sub := broker.subscribe()
			broker.takePreviews(sub) // Initial scope event.
			for i := range tc.calls {
				path, call := fmt.Sprintf("a%d.go", i), fmt.Sprintf("cmd%d", i)
				file := mekugi.RenderReviewFile(path, path, strings.Repeat(fmt.Sprintf("before source %d\n", i), tc.lines), strings.Repeat(fmt.Sprintf("after source %d\n", i), tc.lines))
				broker.publishPreview(diffview.Preview{ID: call, Workspace: u.session.cwd, Thread: "main", Complete: true,
					HostItems: []string{call}, Files: []mekugi.ReviewFile{file}, EditReceipt: editReceiptText(u.session.cwd,
						mekugiHistory{ReviewFiles: []mekugi.ReviewFile{file}, ExecOutcome: &execOutcome{Labels: []string{"cat"}}})}, false)
				events := broker.takePreviews(sub)
				if len(events) != 1 || events[0].Preview == nil {
					t.Fatal("completion was not published")
				}
				preview := *events[0].Preview
				if tc.lines == 4000 && (len(preview.Files) != 0 || !preview.Truncated) {
					t.Fatal("fixture did not cross live-card clipping")
				}
				u.shell.preview(preview)
			}
			for i := range tc.calls {
				path, call := fmt.Sprintf("a%d.go", i), fmt.Sprintf("cmd%d", i)
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": appServerItem{
					ID: call, Type: "commandExecution", Command: "cat > " + path + " <<'EOF'\nafter\nEOF\ngo test ./...", Status: "inProgress"}})
				for _, view := range []*liveActivityView{u.view, u.agents} {
					found := false
					for _, entry := range view.entries {
						if entry.native == nil || entry.native.item != call {
							continue
						}
						found = true
						if !u.shell.openActivityEdit(view, entry.Seq, path) {
							t.Fatalf("%s: observed Edited link cannot open before durable capture", call)
						}
						code := u.shell.output.pages[0].Code
						if !strings.Contains(code, fmt.Sprintf("-before source %d", i)) {
							t.Fatalf("%s: wrong observed source: %q", call, code)
						}
						if tc.lines == 4000 && (!strings.Contains(code, "more diff lines") || len(code) > maxEditReceiptDiffBytes) {
							t.Fatal("clipped source page lost its bound or omission marker")
						}
						u.shell.outputKey("\x1b")
					}
					if !found {
						t.Fatalf("%s: host entry absent", call)
					}
				}
			}
			if len(u.pendingEditReceipts) != 0 {
				t.Fatal("matched receipts remained pending")
			}
			if len(u.shell.diff.data.order) != 0 {
				t.Fatal("observed pages became durable evidence")
			}
		})
	}
}

func TestPendingObservedEditReceiptsStayBounded(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	file := mekugi.RenderReviewFile("a.go", "a.go", strings.Repeat("before "+strings.Repeat("x", 90)+"\n", 100), "after\n")
	receipt := editReceiptText(u.session.cwd, mekugiHistory{ReviewFiles: []mekugi.ReviewFile{file}, ExecOutcome: &execOutcome{Labels: []string{"cat"}}})
	for i := range 256 {
		call := fmt.Sprintf("cmd%d", i)
		u.shell.preview(diffview.Preview{ID: call, Workspace: u.session.cwd, Thread: "main", Complete: true, HostItems: []string{call}, EditReceipt: receipt})
	}
	pending := u.pendingEditReceipts
	if len(pending) == 0 || len(pending) >= 256 || pending[len(pending)-1].calls[0] != "cmd255" {
		t.Fatalf("unmatched receipt budget did not retain a bounded newest window: %d", len(pending))
	}
	if len(u.shell.diff.data.order) != 0 {
		t.Fatal("unmatched receipts became durable evidence")
	}
}
