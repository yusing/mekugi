package router

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

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
			sources := []string{"cat > a.go", "cat > b.go", "go test ./...", "cat > failed.go", "cat > skipped.go"}
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
					case block.Verb == "Run" && block.Code == sources[0]:
						outputKept = block.Output == output && block.ExitCode == 0
					case block.Verb == "Run" && block.Code == sources[2]:
						testKept = block.ExitCode == 2 && strings.Join(block.Tail, "") == "test failed"
					case block.Verb == "Edit" && strings.Contains(block.Label, "failed.go"):
						failedKept = block.ExitCode == 1 && block.EditOutcome == "failed" && strings.Join(block.Tail, "") == "permission denied"
					case block.Verb == "Edit" && strings.Contains(block.Label, "skipped.go"):
						skippedKept = block.Skipped && block.EditOutcome == "skipped"
					case block.Verb == "Edit" && strings.Contains(block.Label, "(requested)"):
						t.Fatalf("successful intent survived receipt: %+v", block)
					}
				}
				if !outputKept || !testKept || !failedKept || !skippedKept {
					t.Fatalf("receipt lost segment output/state: %+v", view.entries[0].blocks)
				}
			}
		})
	}
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
			if len(blocks) != 1 || blocks[0].Verb != "Run" || blocks[0].Output != output || !reflect.DeepEqual(blocks[0].Tail, tail) {
				t.Fatalf("grouped sibling lost combined host output: %+v", blocks)
			}
			if page := u.view.painter.DialogPage(blocks[0], 100); page.Text != "host diagnostic" {
				t.Fatalf("combined output dialog lost content: %q", page.Text)
			}
		}
	}
}
