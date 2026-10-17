package router

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/legacycall"

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
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "early", Workspace: u.attached.cwd, Thread: "main", Complete: true,
		HostItems: []string{"cmd"}, Files: []diffmodel.ReviewFile{file}, EditReceipt: "Edit `a.go` +1 -1 · python3"}, false)
	command := "python3 - <<'PY'\np='a.go';s=open(p).read().replace('before','after');open(p,'w').write(s)\nPY\ngo test ./..."
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: command, Status: "inProgress"}})
	for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
		for _, entry := range view.Entries {
			if entry.Source.Native == nil || entry.Source.Native.Item != "cmd" {
				continue
			}
			native := *entry.Source.Native
			native.Phase = "item/commandExecution/outputDelta"
			native.CapturedEdit = nil // Host refresh carries no router receipt.
			native.Segments = []sessionpresentation.ActivitySegment{{Source: "python3", Text: "Edit `a.go` · python3 (requested)"}, {Source: "go test ./...", Text: "Run `go test ./...`", Running: true}}
			view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 99, Agent: entry.Source.Agent, Kind: "tool", CallID: entry.Source.CallID, Text: entry.Source.Text, Native: &native}}})
			if !terminalTest(u).openActivityEdit(view.bind(terminalTest(u)), entry.Source.Seq, "a.go") || !strings.Contains(terminalTest(u).Dialog.Pages[0].Code, "+after") {
				t.Fatal("early observed source was lost")
			}
			terminalTest(u).outputKey("\x1b")
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
			view := u.testActivity(u.shell.Transcript)
			if thread == "child" {
				view = u.testActivity(u.shell.Agents)
			}
			sources := []string{"cat > a.go", "mv b.go.new b.go", "go test ./...", "mv failed.go.new failed.go", "mv skipped.go.new skipped.go"}
			var segments []sessionpresentation.ActivitySegment
			for _, source := range sources {
				segments = append(segments, sessionpresentation.ActivitySegment{Source: source, Text: execSegmentText(source)})
			}
			output := u.activity.Outputs(u.attached.session.Scope()).New()
			output.Finish(new("edit diagnostic"), new(0))
			segments[0].Output = output
			segments[2].Exit, segments[2].Tail = 2, []string{"test failed"}
			segments[3].Exit, segments[3].Tail = 1, []string{"permission denied"}
			segments[4].Skipped = true
			command := strings.Join(sources, "; ")
			view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{
				Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: toolActivityShell(command),
				Native: &sessionpresentation.NativeItem{Thread: thread, Item: "cmd", Command: command, Segments: segments},
			}}})
			data := trackedEditReceiptData(thread, "cmd")
			for range 2 {
				view.ApplyCapturedEdits(data)
				assertTrackedReceiptStats(t, view, 0)
				var outputKept, testKept, failedKept, skippedKept bool
				for _, block := range view.blocks(0) {
					switch {
					case block.Code == sources[0]:
						t.Fatalf("successful edit source leaked into Activity: %+v", block)
					case block.Verb == "Edited" && view.RetainedOutput(block.Source, block.Output) == output:
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
	var segments []sessionpresentation.ActivitySegment
	for i, source := range []string{"cat > a.go", "go vet ./...", "cat > b.go"} {
		line := strings.Fields(source)[len(strings.Fields(source))-1] + " diagnostic"
		output := u.activity.Outputs(u.attached.session.Scope()).New()
		output.Finish(new(line), new(0))
		elapsed := time.Duration(i+1) * time.Second
		segments = append(segments, sessionpresentation.ActivitySegment{Source: source, Text: execSegmentText(source), Output: output, Tail: []string{line},
			Timing: segmenttransport.Timing{Started: started, Ended: started.Add(elapsed), ElapsedNS: int64(elapsed)}})
	}
	command := "cat > a.go; go vet ./...; cat > b.go"
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{
		Seq: 1, Agent: "/root/child", Kind: "tool", CallID: "cmd", Text: toolActivityShell(command),
		Native: &sessionpresentation.NativeItem{Thread: "child", Item: "cmd", Command: command, Segments: segments},
	}}})
	u.attached.childSource.ApplyCapturedEdits(trackedEditReceiptData("child", "cmd"))
	var outputs []string
	for _, block := range u.testActivity(u.shell.Agents).blocks(0) {
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
	pages := activityui.Disclosure(nil).Blocks(sessionpresentation.CommandPresentation(u.attached.childSource.Entries[0].Source).DetailValues())
	if len(pages) != 3 || pages[0].Code != "cat > a.go" || pages[2].Code != "cat > b.go" {
		t.Fatalf("edit output dialog: %+v", pages)
	}
	feed := u.testActivity(u.shell.Agents).RenderFeed(100, 20)
	assertNativeUISnapshot(t, "captured-edit-output", feed.Lines)
	row := slices.IndexFunc(feed.Lines, func(line string) bool { return strings.Contains(ansi.Strip(line), "a.go diagnostic") })
	if row < 0 || feed.Snippets[row].Block == activityui.EditNavigationSnippet || !terminalTest(u).openOutput(u.shell.Agents, feed.Snippets[row]) {
		t.Fatalf("output row does not open its output: row %d", row)
	}
	rows := make([]string, 12)
	u.detailFiles.prepareOutput(&u.attached.session, terminalTest(u).Attachment)
	terminalTest(u).Dialog.Paint(rows, 100, len(rows))
	assertNativeUISnapshot(t, "captured-edit-output-dialog", rows)
}

func TestAppServerTrackedEditReceiptExactAnchor(t *testing.T) {
	view := newActivityAdapter()
	add := func(seq uint64, thread, item string) {
		source := "cat > a.go"
		view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{
			Seq: seq, Agent: thread, Kind: "tool", CallID: thread + ":" + item, Text: toolActivityShell(source),
			Native: &sessionpresentation.NativeItem{Thread: thread, Item: item, Command: source,
				Segments: []sessionpresentation.ActivitySegment{{Source: source, Text: execSegmentText(source)}},
			},
		}}})
	}
	add(1, "main", "sibling")
	add(2, "child", "anchor")
	add(3, "main", "unrelated")
	data := trackedEditReceiptData("main", "anchor", "sibling")
	view.ApplyCapturedEdits(data)
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
		view.ApplyCapturedEdits(data)
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
			output := u.activity.Outputs(u.attached.session.Scope()).New()
			output.Finish(new("test failed"), new(2))
			retainCommandSegments(u.ctx, u.retained, u.segments, &u.attached, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty, sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: thread, Turn: "turn", Item: item.ID}}, item.Observation(),
				execTrackView{complete: true, output: true, code: 2, segments: []sessionpresentation.ActivitySegment{{}, {}, {Exit: 2, Output: output}}})
			awaitCommandSegments(t, u)
			restored := newAppServerSessionTestUI(t, workspace)
			setTestAppServerIntegration(restored, testProxyWithStore(testReplayStore(mekugiReplayStore{directory: store.directory})))
			restored.review.Projection.Data = reviewReceiptSnapshot(t, trackedEditReceiptData(thread, item.ID))
			turns := []binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}}
			view := restored.testActivity(restored.shell.Transcript)
			if thread == "main" {
				restored.restoreHistory(turns)
			} else {
				restored.attached.session.Caller(thread)
				restored.restoreActivityThread(binding.ThreadInfo{ID: thread, Cwd: workspace, Turns: turns}, false)
				view = restored.testActivity(restored.shell.Agents)
			}
			if len(view.Entries) != 1 || view.Entries[0].Source.Native == nil || len(view.Entries[0].Source.Native.Segments) != 3 {
				t.Fatalf("tracked history missing: %+v", view.Entries)
			}
			for range 2 {
				applyCapturedEdits(&restored.attached, restored.review, &restored.pendingEditReceipts)
				assertTrackedReceiptStats(t, view, 0)
				last := view.blocks(0)[len(view.blocks(0))-1]
				if last.Verb != "Run" || last.ExitCode != 2 || last.Output == 0 || strings.Join(view.RetainedOutput(last.Source, last.Output).View().Lines, "\n") != "test failed" {
					t.Fatalf("restored neighbor lost retained failure/output: %+v", last)
				}
			}
		})
	}
}

func TestAppServerTrackedEditReceiptKeepsSiblingCombinedOutput(t *testing.T) {
	for _, tail := range [][]string{nil, {"host diagnostic"}} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		output := u.activity.Outputs(u.attached.session.Scope()).New()
		output.Finish(new("host diagnostic"), new(0))
		command := "cat > a.go; cat > b.go"
		u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{
			Seq: 1, Agent: "Main", Kind: "tool", CallID: "sibling", Text: toolActivityShell(command), OutputTail: tail,
			Native: &sessionpresentation.NativeItem{Thread: "main", Item: "sibling", Command: command, Output: output, Segments: []sessionpresentation.ActivitySegment{
				{Source: "cat > a.go", Text: execSegmentText("cat > a.go")},
				{Source: "cat > b.go", Text: execSegmentText("cat > b.go")},
			}},
		}}})
		data := trackedEditReceiptData("main", "anchor", "sibling")
		for range 2 {
			u.attached.source.ApplyCapturedEdits(data)
			blocks := u.testActivity(u.shell.Transcript).blocks(0)
			if len(blocks) != 1 || blocks[0].Verb != "Edited" || blocks[0].Code != "" || u.attached.source.RetainedOutput(blocks[0].Source, blocks[0].Output) != output || !reflect.DeepEqual(blocks[0].Tail, tail) {
				t.Fatalf("grouped sibling lost combined host output: %+v", blocks)
			}
			if page := u.shell.Transcript.Painter.DialogPage(snapshotOutput(blocks[0], u.attached.source.RetainedOutput(blocks[0].Source, blocks[0].Output)), 100); page.Text != "host diagnostic" {
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
			setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{u.attached.cwd: {"main": true}}})
			sub := testReview(broker).Subscribe()
			testReview(broker).TakePreviews(sub) // Initial scope event.
			for i := range tc.calls {
				path, call := fmt.Sprintf("a%d.go", i), fmt.Sprintf("cmd%d", i)
				file := diffmodel.RenderReviewFile(path, path, strings.Repeat(fmt.Sprintf("before source %d\n", i), tc.lines), strings.Repeat(fmt.Sprintf("after source %d\n", i), tc.lines))
				broker.Update(previewmodel.Preview{ID: call, Workspace: u.attached.cwd, Thread: "main", Complete: true,
					HostItems: []string{call}, Files: []diffmodel.ReviewFile{file}, EditReceipt: presentation.ReceiptText(u.attached.cwd, receiptEvidence(legacycall.History{ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{file}), ExecOutcome: (*legacycall.Outcome)(&changecapture.Outcome{Labels: []string{"cat"}})}))}, false)
				events := testReview(broker).TakePreviews(sub)
				if len(events) != 1 || events[0].Preview == nil {
					t.Fatal("completion was not published")
				}
				preview := *events[0].Preview
				if tc.lines == 4000 && (len(preview.Files) != 0 || !preview.Truncated) {
					t.Fatal("fixture did not cross live-card clipping")
				}
				applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), preview, false)
			}
			for i := range tc.calls {
				path, call := fmt.Sprintf("a%d.go", i), fmt.Sprintf("cmd%d", i)
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{
					ID: call, Type: "commandExecution", Command: "cat > " + path + " <<'EOF'\nafter\nEOF\ngo test ./...", Status: "inProgress"}})
				for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
					found := false
					for _, entry := range view.Entries {
						if entry.Source.Native == nil || entry.Source.Native.Item != call {
							continue
						}
						found = true
						if !terminalTest(u).openActivityEdit(view.bind(terminalTest(u)), entry.Source.Seq, path) {
							t.Fatalf("%s: observed Edited link cannot open before durable capture", call)
						}
						code := terminalTest(u).Dialog.Pages[0].Code
						if !strings.Contains(code, fmt.Sprintf("-before source %d", i)) {
							t.Fatalf("%s: wrong observed source: %q", call, code)
						}
						if tc.lines == 4000 && (!strings.Contains(code, "more diff lines") || len(code) > presentation.MaxReceiptDiffBytes) {
							t.Fatal("clipped source page lost its bound or omission marker")
						}
						terminalTest(u).outputKey("\x1b")
					}
					if !found {
						t.Fatalf("%s: host entry absent", call)
					}
				}
			}
			if len(u.pendingEditReceipts) != 0 {
				t.Fatal("matched receipts remained pending")
			}
			if u.review.Projection.Data.Len() != 0 {
				t.Fatal("observed pages became durable evidence")
			}
		})
	}
}

func TestPendingObservedEditReceiptsStayBounded(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	file := diffmodel.RenderReviewFile("a.go", "a.go", strings.Repeat("before "+strings.Repeat("x", 90)+"\n", 100), "after\n")
	receipt := presentation.ReceiptText(u.attached.cwd, receiptEvidence(legacycall.History{ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{file}), ExecOutcome: (*legacycall.Outcome)(&changecapture.Outcome{Labels: []string{"cat"}})}))
	for i := range 256 {
		call := fmt.Sprintf("cmd%d", i)
		applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: call, Workspace: u.attached.cwd, Thread: "main", Complete: true, HostItems: []string{call}, EditReceipt: receipt}, false)
	}
	pending := u.pendingEditReceipts
	if len(pending) == 0 || len(pending) >= 256 || pending[len(pending)-1].Calls[0] != "cmd255" {
		t.Fatalf("unmatched receipt budget did not retain a bounded newest window: %d", len(pending))
	}
	if u.review.Projection.Data.Len() != 0 {
		t.Fatal("unmatched receipts became durable evidence")
	}
}
