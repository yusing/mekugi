package router

import (
"github.com/yusing/mekugi/libraries/legacycall"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/terminalui"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestObservedEditReceiptBeforeHostItemSurvivesSegmentRefresh(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	file := diffmodel.RenderReviewFile("a.go", "a.go", "before\n", "after\n")
	// The tracker sees host starts on the transport reader. Its completion
	// can reach the preview mailbox before the UI consumes that same item.
	u.shell.preview(previewmodel.Preview{ID: "early", Workspace: u.session.cwd, Thread: "main", Complete: true,
		HostItems: []string{"cmd"}, Files: []diffmodel.ReviewFile{file}, EditReceipt: "Edit `a.go` +1 -1 · python3"}, false)
	command := "python3 - <<'PY'\np='a.go';s=open(p).read().replace('before','after');open(p,'w').write(s)\nPY\ngo test ./..."
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: command, Status: "inProgress"}})
	for _, view := range []*activityAdapter{u.view, u.agents} {
		for _, entry := range view.Entries {
			if entry.Source.native == nil || entry.Source.native.item != "cmd" {
				continue
			}
			native := *entry.Source.native
			native.phase = "item/commandExecution/outputDelta"
			native.capturedEdit = nil // Host refresh carries no router receipt.
			native.segments = []commandSegment{{source: "python3", text: "Edit `a.go` · python3 (requested)"}, {source: "go test ./...", text: "Run `go test ./...`", running: true}}
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 99, Agent: entry.Source.Agent, Kind: "tool", CallID: entry.Source.CallID, Text: entry.Source.Text, native: &native}}})
			if !u.shell.openActivityEdit(view, entry.Source.Seq, "a.go") || !strings.Contains(u.shell.output.pages[0].Code, "+after") {
				t.Fatal("early observed source was lost")
			}
			u.shell.outputKey("\x1b")
			for _, block := range view.blocks(0) {
				if block.EditSource != "" && (block.EditOutcome != "" || strings.Contains(block.EditSource, "requested")) {
					t.Fatalf("stale edit status survived receipt: %+v", block)
				}
			}
		}
	}
}

func trackedEditReceiptData(thread string, calls ...string) []*presentation.Receipt {
	data := []*presentation.Receipt{{
		Thread: thread, Calls: calls,
		Text: "Edit `a.go` +3 -1 · cat\n\nEdit `b.go` +1 -2 · cat",
	}}
	return data
}

func assertTrackedReceiptStats(t *testing.T, view *activityAdapter, entry int) {
	t.Helper()
	blocks := view.blocks(entry)
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
			frame = append(frame, view.Painter.Block(block, 120)...)
		}
	}
	if !reflect.DeepEqual(counts, map[string][2]int{"a.go": {3, 1}, "b.go": {1, 2}}) {
		t.Fatalf("captured stats missing from rendered blocks: %+v", blocks)
	}
	painted := strings.Join(frame, "\n")
	if !strings.Contains(ansi.Strip(painted), "+3 -1") || !strings.Contains(ansi.Strip(painted), "+1 -2") || !strings.Contains(painted, terminalui.Green+"━") || !strings.Contains(painted, terminalui.Red+"━") {
		t.Fatalf("counts not painted with diff colors: %q", painted)
	}
	if reparsed := activityui.Values(parseLiveActivity(view.Entries[entry].Source)); !reflect.DeepEqual(reparsed, view.Entries[entry].Values) {
		t.Fatalf("reparse discarded corrected projection:\n%+v\n%+v", view.Entries[entry].Values, reparsed)
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
			output := u.session.activity.Outputs(u.session.Scope()).New()
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
				for _, block := range view.blocks(0) {
					switch {
					case block.Code == sources[0]:
						t.Fatalf("successful edit source leaked into Activity: %+v", block)
					case block.Verb == "Edited" && view.retainedOutput(block) == output:
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
				if !outputKept || !testKept || !failedKept || !skippedKept || len(view.blocks(0)) != 6 {
					t.Fatalf("receipt lost segment output/state: %+v", view.blocks(0))
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
		output := u.session.activity.Outputs(u.session.Scope()).New()
		output.Finish(new(line), new(0))
		elapsed := time.Duration(i+1) * time.Second
		segments = append(segments, commandSegment{source: source, text: execSegmentText(source), output: output, tail: []string{line},
			timing: segmenttransport.Timing{Started: started, Ended: started.Add(elapsed), ElapsedNS: int64(elapsed)}})
	}
	command := "cat > a.go; go vet ./...; cat > b.go"
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 1, Agent: "/root/child", Kind: "tool", CallID: "cmd", Text: toolActivityShell(command),
		native: &liveActivityNativeItem{thread: "child", item: "cmd", command: command, segments: segments},
	}}})
	u.agents.applyCapturedEdits(trackedEditReceiptData("child", "cmd"))
	var outputs []string
	for _, block := range u.agents.blocks(0) {
		if block.Output == 0 {
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
	pages := activityui.Disclosure(nil).Blocks(commandPresentation(u.agents.Entries[0].Source).DetailValues())
	if len(pages) != 3 || pages[0].Code != "cat > a.go" || pages[2].Code != "cat > b.go" {
		t.Fatalf("edit output dialog: %+v", pages)
	}
	feed := u.agents.RenderFeed(100, 20)
	assertNativeUISnapshot(t, "captured-edit-output", feed.Lines)
	row := slices.IndexFunc(feed.Lines, func(line string) bool { return strings.Contains(ansi.Strip(line), "a.go diagnostic") })
	if row < 0 || feed.Snippets[row].Block == activityui.EditNavigationSnippet || !u.shell.openOutput(u.agents, feed.Snippets[row]) {
		t.Fatalf("output row does not open its output: row %d", row)
	}
	rows := make([]string, 12)
	u.shell.paintOutput(rows, 100, len(rows))
	assertNativeUISnapshot(t, "captured-edit-output-dialog", rows)
}

func TestAppServerTrackedEditReceiptExactAnchor(t *testing.T) {
	view := newActivityAdapter()
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
	if len(view.blocks(0)) != 0 {
		t.Fatalf("grouped sibling duplicated aggregate: %+v", view.blocks(0))
	}
	for _, i := range []int{1, 2} {
		if len(view.blocks(i)) != 1 || view.blocks(i)[0].EditOutcome != "ran" {
			t.Fatalf("receipt changed unrelated thread/item: %+v", view.blocks(i))
		}
		if _, _, _, _, ok := activityui.EditStat(view.blocks(i)[0].Label); ok {
			t.Fatal("unrelated command acquired receipt counts")
		}
	}
	add(4, "main", "anchor")
	for range 2 {
		view.applyCapturedEdits(data)
		assertTrackedReceiptStats(t, view, 3)
		if len(view.blocks(0)) != 0 {
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
			setTestAppServerIntegration(u, testProxyWithStore(store))
			item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: "bash -lc 'cat > a.go; cat > b.go; go test ./...'", ExitCode: new(2), AggregatedOutput: new("test failed")}
			output := u.session.activity.Outputs(u.session.Scope()).New()
			output.Finish(new("test failed"), new(2))
			u.retainCommandSegments(activityPaneEntry{native: &liveActivityNativeItem{thread: thread, turn: "turn", item: item.ID}}, item.Observation(),
				execTrackView{complete: true, output: true, code: 2, segments: []commandSegment{{}, {}, {exit: 2, output: output}}})
			awaitCommandSegments(t, u)
			restored := newAppServerSessionTestUI(t, workspace)
			setTestAppServerIntegration(restored, testProxyWithStore(testReplayStore(mekugiReplayStore{directory: store.directory})))
			restored.shell.saved.Data = reviewReceiptSnapshot(t, trackedEditReceiptData(thread, item.ID))
			turns := []binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}}
			view := restored.view
			if thread == "main" {
				restored.restoreHistory(turns)
			} else {
				restored.session.path(thread)
				restored.restoreActivityThread(binding.ThreadInfo{ID: thread, Cwd: workspace, Turns: turns}, false)
				view = restored.agents
			}
			if len(view.Entries) != 1 || view.Entries[0].Source.native == nil || len(view.Entries[0].Source.native.segments) != 3 {
				t.Fatalf("tracked history missing: %+v", view.Entries)
			}
			for range 2 {
				restored.applyCapturedEdits()
				assertTrackedReceiptStats(t, view, 0)
				last := view.blocks(0)[len(view.blocks(0))-1]
				if last.Verb != "Run" || last.ExitCode != 2 || last.Output == 0 || strings.Join(view.retainedOutput(last).View().Lines, "\n") != "test failed" {
					t.Fatalf("restored neighbor lost retained failure/output: %+v", last)
				}
			}
		})
	}
}

func TestAppServerTrackedEditReceiptKeepsSiblingCombinedOutput(t *testing.T) {
	for _, tail := range [][]string{nil, {"host diagnostic"}} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		output := u.session.activity.Outputs(u.session.Scope()).New()
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
			blocks := u.view.blocks(0)
			if len(blocks) != 1 || blocks[0].Verb != "Edited" || blocks[0].Code != "" || u.view.retainedOutput(blocks[0]) != output || !reflect.DeepEqual(blocks[0].Tail, tail) {
				t.Fatalf("grouped sibling lost combined host output: %+v", blocks)
			}
			if page := u.view.Painter.DialogPage(snapshotOutput(blocks[0], u.view.retainedOutput(blocks[0])), 100); page.Text != "host diagnostic" {
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
			broker := newLivePreview(t.Context())
			setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{u.session.cwd: {"main": true}}})
			sub := testReview(broker).Subscribe()
			testReview(broker).TakePreviews(sub) // Initial scope event.
			for i := range tc.calls {
				path, call := fmt.Sprintf("a%d.go", i), fmt.Sprintf("cmd%d", i)
				file := diffmodel.RenderReviewFile(path, path, strings.Repeat(fmt.Sprintf("before source %d\n", i), tc.lines), strings.Repeat(fmt.Sprintf("after source %d\n", i), tc.lines))
				broker.Update(previewmodel.Preview{ID: call, Workspace: u.session.cwd, Thread: "main", Complete: true,
					HostItems: []string{call}, Files: []diffmodel.ReviewFile{file}, EditReceipt: presentation.ReceiptText(u.session.cwd, receiptEvidence(legacycall.History{ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{file}), ExecOutcome: (*legacycall.Outcome)(&changecapture.Outcome{Labels: []string{"cat"}})}))}, false)
				events := testReview(broker).TakePreviews(sub)
				if len(events) != 1 || events[0].Preview == nil {
					t.Fatal("completion was not published")
				}
				preview := *events[0].Preview
				if tc.lines == 4000 && (len(preview.Files) != 0 || !preview.Truncated) {
					t.Fatal("fixture did not cross live-card clipping")
				}
				u.shell.preview(preview, false)
			}
			for i := range tc.calls {
				path, call := fmt.Sprintf("a%d.go", i), fmt.Sprintf("cmd%d", i)
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{
					ID: call, Type: "commandExecution", Command: "cat > " + path + " <<'EOF'\nafter\nEOF\ngo test ./...", Status: "inProgress"}})
				for _, view := range []*activityAdapter{u.view, u.agents} {
					found := false
					for _, entry := range view.Entries {
						if entry.Source.native == nil || entry.Source.native.item != call {
							continue
						}
						found = true
						if !u.shell.openActivityEdit(view, entry.Source.Seq, path) {
							t.Fatalf("%s: observed Edited link cannot open before durable capture", call)
						}
						code := u.shell.output.pages[0].Code
						if !strings.Contains(code, fmt.Sprintf("-before source %d", i)) {
							t.Fatalf("%s: wrong observed source: %q", call, code)
						}
						if tc.lines == 4000 && (!strings.Contains(code, "more diff lines") || len(code) > presentation.MaxReceiptDiffBytes) {
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
			if u.shell.saved.Data.Len() != 0 {
				t.Fatal("observed pages became durable evidence")
			}
		})
	}
}

func TestPendingObservedEditReceiptsStayBounded(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	file := diffmodel.RenderReviewFile("a.go", "a.go", strings.Repeat("before "+strings.Repeat("x", 90)+"\n", 100), "after\n")
	receipt := presentation.ReceiptText(u.session.cwd, receiptEvidence(legacycall.History{ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{file}), ExecOutcome: (*legacycall.Outcome)(&changecapture.Outcome{Labels: []string{"cat"}})}))
	for i := range 256 {
		call := fmt.Sprintf("cmd%d", i)
		u.shell.preview(previewmodel.Preview{ID: call, Workspace: u.session.cwd, Thread: "main", Complete: true, HostItems: []string{call}, EditReceipt: receipt}, false)
	}
	pending := u.pendingEditReceipts
	if len(pending) == 0 || len(pending) >= 256 || pending[len(pending)-1].Calls[0] != "cmd255" {
		t.Fatalf("unmatched receipt budget did not retain a bounded newest window: %d", len(pending))
	}
	if u.shell.saved.Data.Len() != 0 {
		t.Fatal("unmatched receipts became durable evidence")
	}
}
