package router

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/commandsegments/segments"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotCombinedEditOutput(t *testing.T) {
	command, err := os.ReadFile("testdata/python-rewrite-command.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{string(command), shellinstrumentation.ShScript("/private/exec-track.sh", string(command))} {
		output := new(activitymodel.Retention).New()
		output.Write("build failed\n")
		output.Finish(nil, new(2))
		view := newActivityAdapter()
		view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{
			Seq: 1, Agent: "Main", Kind: "tool", Text: toolActivityShell(command),
			Native: &sessionpresentation.NativeItem{Command: command, Output: output},
		}}})
		u := &terminalUI{}
		if !u.openEntry(view.bind(u), 1) || len(u.output.pages) != 1 {
			t.Fatal("missing combined output page")
		}
		rows := make([]string, 24)
		u.paintOutput(rows, 100, len(rows))
		if u.output.Document.Text != "build failed" {
			t.Fatalf("combined output lost host text: %q", u.output.Document.Text)
		}
		assertNativeUISnapshot(t, "combined-edit-output", rows)
	}
}

func snapshotOutput(block activityui.Block, output *activitymodel.Output) activityui.Block {
	if output != nil {
		snapshot := output.View()
		block.OutputSnapshot, block.OutputContextLimited = &snapshot, false
	}
	return block
}

func dialogForOutput(output *activitymodel.Output) *terminalUI {
	view := newActivityAdapter()
	u := &terminalUI{}
	u.openBlocks(view.bind(u), []activityui.Block{testOutputBlock(view, activityui.Block{Kind: "op", Verb: "Run", Code: "go test ./..."}, output)})
	return u
}

func drawOutputDialog(u *terminalUI) string {
	rows := make([]string, 18)
	u.paintOutput(rows, 80, len(rows))
	return ansi.Strip(strings.Join(rows, "\n"))
}

func TestOutputDialogLiveFollowPauseAndEnd(t *testing.T) {
	output := new(activitymodel.Retention).New()
	for i := range 30 {
		output.Write(fmt.Sprintf("line %02d\n", i))
	}
	u := dialogForOutput(output)
	if frame := drawOutputDialog(u); !strings.Contains(frame, "line 29") {
		t.Fatalf("live dialog did not follow latest output: %q", frame)
	}
	u.outputKey("g")
	if frame := drawOutputDialog(u); !strings.Contains(frame, "line 00") || !strings.Contains(frame, "paused") {
		t.Fatal("Home did not pause live following")
	}
	output.Write("line 30\n")
	if frame := drawOutputDialog(u); strings.Contains(frame, "line 30") {
		t.Fatalf("paused dialog jumped to new output: %q", frame)
	}
	u.outputKey("G")
	if frame := drawOutputDialog(u); !strings.Contains(frame, "line 30") || strings.Contains(frame, "paused") {
		t.Fatalf("End did not resume live following: %q", frame)
	}
	output.Finish(nil, new(0))
	if frame := drawOutputDialog(u); !strings.Contains(frame, "line 30") || u.output.Document.Live {
		t.Fatalf("settled output disappeared: %q", frame)
	}
}

func TestOutputDialogWheelAndOutsideClickIsolation(t *testing.T) {
	output := new(activitymodel.Retention).New()
	for i := range 30 {
		output.Write(fmt.Sprintf("line %02d\n", i))
	}
	u := dialogForOutput(output)
	u.main = appServerTestFeatures(&appServerUI{activity: new(activitymodel.Service), view: activityui.NewView()})
	drawOutputDialog(u)
	feedOffset := u.main.view.Offset
	u.outputMouse(64, 0, 0, false)
	if strings.Contains(drawOutputDialog(u), "line 29") || u.main.view.Offset != feedOffset {
		t.Fatal("wheel did not pause dialog independently of feed")
	}
	u.outputMouse(65, 0, 0, false)
	if u.main.view.Offset != feedOffset {
		t.Fatal("wheel moved feed behind dialog")
	}
	u.outputMouse(0, 0, 0, false)
	if u.output != nil {
		t.Fatal("outside click did not close output dialog")
	}
}

func TestOutputDialogSearchAndCopyRetainedText(t *testing.T) {
	output := new(activitymodel.Retention).New()
	output.Write("alpha\nneedle one\nbeta\nneedle two\n")
	output.Finish(nil, new(0))
	u := dialogForOutput(output)
	u.main = appServerTestFeatures(&appServerUI{activity: new(activitymodel.Service), view: activityui.NewView()})
	drawOutputDialog(u)
	u.outputKey("/")
	for _, key := range []string{"n", "e", "e", "d", "l", "e", "\r"} {
		u.outputKey(key)
	}
	if frame := drawOutputDialog(u); strings.Contains(frame, "no match") || !strings.Contains(frame, "/ n N find") {
		t.Fatal("search missed retained output")
	}
	u.outputKey("n")
	u.outputKey("N")
	u.outputKey("y")
	if u.frame.Clipboard == "" || !strings.Contains(u.frame.Clipboard, "\x1b]52;c;") {
		t.Fatal("copy did not send retained output to terminal clipboard")
	}
	u.outputKey("q")
	if u.output != nil {
		t.Fatal("q did not close output dialog")
	}
}

func TestOutputDialogTracksRetainedOutputAfterOpening(t *testing.T) {
	output := new(activitymodel.Retention).New()
	output.Write("streamed\n")
	u := dialogForOutput(output)
	drawOutputDialog(u)
	final := "host aggregate\nfinal line\n"
	output.Finish(&final, new(1))
	if frame := drawOutputDialog(u); !strings.Contains(frame, "final line") || strings.Contains(frame, "streamed") || !strings.Contains(frame, "exit 1") {
		t.Fatalf("dialog did not refresh from retained aggregate: %q", frame)
	}
}

func TestOutputDialogSearchRevealsWrappedNumberedMatch(t *testing.T) {
	full := strings.Repeat("prefix 界 ", 160) + "Needle" + strings.Repeat(" tail", 160) + "\nNeedle"
	for _, tc := range []struct {
		name  string
		block activityui.Block
	}{
		{"source", activityui.Block{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "sample.txt", Ranges: []string{"100:101"}}}}},
		{"output", activityui.Block{Kind: "op", Verb: "Run", Code: "go test ./..."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := new(activitymodel.Retention).New()
			output.Finish(&full, new(0))
			view := newActivityAdapter()
			u := &terminalUI{}
			u.openBlocks(view.bind(u), []activityui.Block{testOutputBlock(view, tc.block, output)})
			if strings.Contains(drawOutputDialog(u), "Needle") {
				t.Fatal("fixture already shows the distant match")
			}
			for _, key := range []string{"/", "needle", "\r"} {
				u.outputKey(key)
			}
			first := drawOutputDialog(u)
			if !strings.Contains(first, "Needle") {
				t.Fatalf("search did not reveal the matching wrapped row: %q", first)
			}
			u.outputKey("n")
			if next := drawOutputDialog(u); next == first || !strings.Contains(next, "Needle") {
				t.Fatal("next did not reveal the next logical match")
			}
			u.outputKey("N")
			if drawOutputDialog(u) != first {
				t.Fatal("previous did not return to the first matching wrapped row")
			}
		})
	}
}

func TestOutputDialogRestoredCommandUsesRetainedAggregate(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.Conversation = true
	u.testActivity(u.view).testAudience()
	output := "first\nsecond\nthird\n"
	u.restoreHistory([]binding.HistoryTurn{{ID: "past", Status: "completed", Items: []binding.ThreadItem{{
		ID: "cmd", Type: "commandExecution", Command: "go test ./...", AggregatedOutput: &output, ExitCode: new(1),
	}}}})
	feed := u.testActivity(u.view).RenderFeed(90, 60)
	index := -1
	for i, line := range feed.Lines {
		if strings.Contains(ansi.Strip(line), "Ran go test ./...") {
			index = i
			break
		}
	}
	terminal := &terminalUI{main: u}
	if index < 0 || !terminal.openOutput(u.view, feed.Snippets[index]) {
		t.Fatal("restored command has no output dialog target")
	}
	terminal.output.layout(76)
	if got := terminal.output.Document.Text; got != "first\nsecond\nthird" {
		t.Fatalf("restored dialog output = %q", got)
	}
}

func TestOutputDialogNativeMergeKeepsRetainedOutputIdentity(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "go test ./...", "status": "inProgress"}
	notify := func(method string, params map[string]any) {
		t.Helper()
		params["threadId"], params["turnId"] = "main", "t"
		appServerTestNotify(t, u, method, params)
	}
	notify("item/started", map[string]any{"item": item})
	if len(u.source.Entries) != 1 || len(u.testActivity(u.view).blocks(0)) == 0 {
		t.Fatal("start did not create command block")
	}
	retained := u.source.RetainedOutput(u.testActivity(u.view).blocks(0)[len(u.testActivity(u.view).blocks(0))-1].Source, u.testActivity(u.view).blocks(0)[len(u.testActivity(u.view).blocks(0))-1].Output)
	if retained == nil {
		t.Fatal("started command has no retained output")
	}
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": "streamed\n"})
	u.flushStreamOutput()
	if got := u.source.RetainedOutput(u.testActivity(u.view).blocks(0)[len(u.testActivity(u.view).blocks(0))-1].Source, u.testActivity(u.view).blocks(0)[len(u.testActivity(u.view).blocks(0))-1].Output); got != retained {
		t.Fatal("delta changed retained output identity")
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "aggregate\nfinal\n"
	notify("item/completed", map[string]any{"item": item})
	rollCommandOutput(u)
	feed := u.testActivity(u.view).RenderFeed(90, 60)
	index := -1
	for i, line := range feed.Lines {
		if strings.Contains(ansi.Strip(line), "Ran go test ./...") {
			index = i
			break
		}
	}
	terminal := &terminalUI{main: u}
	if index < 0 || !terminal.openOutput(u.view, feed.Snippets[index]) {
		t.Fatal("merged command has no output dialog target")
	}
	if terminal.output.pages[0].output != retained {
		t.Fatal("completion replaced retained output identity")
	}
	terminal.output.layout(76)
	if got := terminal.output.Document.Text; got != "aggregate\nfinal" {
		t.Fatalf("merged retained output = %q", got)
	}
}

func TestOutputDialogTrackedSegmentKeepsOwnRetention(t *testing.T) {
	retention := new(activitymodel.Retention)
	first, second := retention.New(), retention.New()
	first.Write("segment one\n")
	first.Finish(nil, new(0))
	second.Write("segment two\n")
	second.Finish(nil, new(2))
	entry := sessionpresentation.ActivityEntry{Kind: "tool", Text: "Run `echo one`; Run `echo two`", Native: &sessionpresentation.NativeItem{
		Segments: []sessionpresentation.ActivitySegment{{Text: "Run `echo one`", Output: first}, {Text: "Run `echo two`", Output: second, Exit: 2}},
	}}
	blocks := commandSegmentBlocks(sessionpresentation.CommandPresentation(entry))
	if len(blocks) != 2 || blocks[0].Output != 2 || blocks[1].Output != 3 {
		t.Fatalf("tracked segment output ownership = %+v", blocks)
	}
	view := newActivityAdapter()
	view.appendEntry(entry, blocks)
	u := &terminalUI{}
	u.openBlocks(view.bind(u), blocks)
	u.output.layout(76)
	if got := u.output.Document.Text; got != "segment one" {
		t.Fatalf("first segment = %q", got)
	}
	u.outputKey("\x1b[C")
	u.output.layout(76)
	if got := u.output.Document.Text; got != "segment two" || !strings.Contains(ansi.Strip(u.output.Document.Detail), "exit 2") {
		t.Fatalf("second segment = %q detail %q", got, u.output.Document.Detail)
	}
}

func TestUISnapshotOutputDialogVTOverlayWideAndNarrow(t *testing.T) {
	for _, width := range []int{100, 50} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			output := new(activitymodel.Retention).New()
			output.Write("modal content\n")
			output.Finish(nil, new(0))
			u := dialogForOutput(output)
			u.output.view.Painter.Theme = diffrender.TerminalTheme
			const height = 20
			rows := make([]string, height)
			for i := range rows {
				rows[i] = "background"
			}
			u.paintOutput(rows, width, height)
			// paintOutput overlays with cursor addressing, so snapshot its final VT rows.
			screen := vt.NewEmulator(width, height)
			defer screen.Close()
			for y, row := range rows {
				if _, err := fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row); err != nil {
					t.Fatal(err)
				}
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-output-dialog-%d.txt", width), strings.Split(screen.Render(), "\n"), width)

		})
	}
}

func TestOutputDialogRestoredMultiReadKeepsCombinedOutput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.Conversation = true
	u.testActivity(u.view).testAudience()
	combined := "content of a\ncontent of b\n"
	u.restoreHistory([]binding.HistoryTurn{{ID: "past", Status: "completed", Items: []binding.ThreadItem{{
		ID: "read", Type: "commandExecution", Command: "cat a; cat b", AggregatedOutput: &combined, ExitCode: new(0),
		CommandActions: []binding.CommandAction{{Type: "read", Path: "a", Command: "cat a"}, {Type: "read", Path: "b", Command: "cat b"}},
	}}}})
	feed := u.testActivity(u.view).RenderFeed(90, 60)
	terminal := &terminalUI{main: u}
	opened := false
	for _, snippet := range feed.Snippets {
		if terminal.openOutput(u.view, snippet) {
			opened = true
			break
		}
	}
	if !opened {
		t.Fatal("restored multi-read has no dialog target")
	}
	found := false
	for i := range terminal.output.pages {
		terminal.output.ShowPage(i)
		terminal.output.layout(76)
		if terminal.output.Document.Text == "content of a\ncontent of b" {
			found = true
		}
	}
	if !found {
		t.Fatal("combined retained output absent from every invocation page")
	}
}

func TestOutputDialogTrackedOutputSettlesAndLossyFallsBack(t *testing.T) {
	key := [3]string{"main", "t", "cmd"}
	hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
	report := hub.begin(t, key, "echo one; echo two")
	report.send(segmenttransport.Message{Type: segmenttransport.Begin}, segmenttransport.Message{Type: segmenttransport.Output, Data: "initial\n"})
	hub.await(t, key, func(view execTrackView) bool { return len(view.segments) == 1 && len(view.segments[0].Tail) != 0 })
	view, _ := hub.view(key, false, execSegmentText)
	retained := view.segments[0].Output
	if !view.output || retained == nil {
		t.Fatalf("live segment did not retain output: %+v", view)
	}
	report.send(segmenttransport.Message{Type: segmenttransport.End, Code: new(2)}, segmenttransport.Message{Type: segmenttransport.Done, Code: new(2)}, segmenttransport.Message{Type: segmenttransport.Closed})
	hub.await(t, key, func(view execTrackView) bool { return view.complete })
	view, _ = hub.view(key, true, execSegmentText)
	if view.segments[0].Output != retained || !retained.View().Done || retained.View().Exit != 2 {
		t.Fatalf("final segment did not settle retained output: %+v", retained.View())
	}

	other := [3]string{"main", "t", "overflow"}
	overflowReport := hub.begin(t, other, "echo huge; echo done")
	overflowReport.send(segmenttransport.Message{Type: segmenttransport.Begin}, segmenttransport.Message{Type: segmenttransport.Output, Data: "prior\n"})
	hub.await(t, other, func(view execTrackView) bool { return len(view.segments) == 1 && len(view.segments[0].Tail) != 0 })
	first, _ := hub.view(other, false, execSegmentText)
	previous := first.segments[0].Output
	if previous == nil {
		t.Fatal("overflow setup did not allocate segment output")
	}
	overflowReport.send(segmenttransport.Message{Type: segmenttransport.Output, Data: strings.Repeat("x", activitymodel.OutputBytes+1)})
	hub.await(t, other, func(view execTrackView) bool { return !view.output })
	if got := previous.View(); !got.Released || !got.Done {
		t.Fatalf("abandoned segment output remains live: %+v", got)
	}
}

func TestOutputDialogLossyMessageReleasesLiveSegment(t *testing.T) {
	key := [3]string{"main", "t", "lossy"}
	hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
	report := hub.begin(t, key, "echo text; echo done")
	report.send(segmenttransport.Message{Type: segmenttransport.Begin}, segmenttransport.Message{Type: segmenttransport.Output, Data: "partial"})
	hub.await(t, key, func(view execTrackView) bool { return len(view.segments) == 1 && len(view.segments[0].Tail) != 0 })
	view, _ := hub.view(key, false, execSegmentText)
	output := view.segments[0].Output
	report.send(segmenttransport.Message{Type: segmenttransport.Lossy})
	hub.await(t, key, func(view execTrackView) bool { return !view.output })
	if !output.View().Done || !output.View().Released {
		t.Fatalf("lossy segment retained live output: %+v", output.View())
	}
}

func TestOutputDialogTerminalKeyDecoderOwnsPrefixAndPaging(t *testing.T) {
	output := new(activitymodel.Retention).New()
	for i := range 30 {
		output.Write(fmt.Sprintf("row %02d\n", i))
	}
	u := dialogForOutput(output)
	u.main = appServerTestFeatures(&appServerUI{activity: new(activitymodel.Service), view: activityui.NewView()})
	drawOutputDialog(u)
	u.Focus = 0
	for _, key := range []byte{2, '3'} {
		if err := u.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.Focus != 0 || u.prefix {
		t.Fatal("modal Ctrl+B pane number changed background pane")
	}
	for _, seq := range []string{"\x02\x1b[5~", "\x02\x1b[6~"} {
		for i := range len(seq) {
			if err := u.key(seq[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if u.main.view.Offset != 0 || u.output == nil {
		t.Fatal("modal paging reached background feed or closed dialog")
	}
}

func TestOutputDialogTrackedViewDrainsRetainedOutput(t *testing.T) {
	key := [3]string{"thread", "turn", "item"}
	hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
	report := hub.begin(t, key, "echo text; sleep 1")
	report.send(segmenttransport.Message{Type: segmenttransport.Begin}, segmenttransport.Message{Type: segmenttransport.Output, Data: "first\n"})
	hub.await(t, key, func(view execTrackView) bool { return len(view.segments) == 1 && len(view.segments[0].Tail) != 0 })
	view, _ := hub.view(key, false, execSegmentText)
	output := view.segments[0].Output
	if output == nil || output.View().Done || strings.Join(output.View().Lines, "\n") != "first" {
		t.Fatal("live segment output missing")
	}
	report.send(segmenttransport.Message{Type: segmenttransport.Output, Data: "second\n"}, segmenttransport.Message{Type: segmenttransport.End, Code: new(3)})
	hub.await(t, key, func(view execTrackView) bool { return len(view.segments) == 1 && !view.segments[0].Running })
	view, _ = hub.view(key, true, execSegmentText)
	if view.segments[0].Output != output || !output.View().Done || output.View().Exit != 3 || strings.Join(output.View().Lines, "\n") != "first\nsecond" {
		t.Fatalf("settled segment: %+v", output.View())
	}
	hub.view(key, true, execSegmentText)
	if strings.Join(output.View().Lines, "\n") != "first\nsecond" {
		t.Fatal("repeated observation duplicated retained output")
	}
}

func TestOutputDialogCommandTabs(t *testing.T) {
	retention := new(activitymodel.Retention)
	first, second := retention.New(), retention.New()
	first.Write("package main\n\n")
	first.Finish(nil, new(0))
	second.Write("main.go:12:return 42\n")
	view := newActivityAdapter()
	entry := sessionpresentation.ActivityEntry{Seq: 10, Agent: "Main", Kind: "tool", Text: "Read `main.go`\n\nSearch `return` in `main.go`", Native: &sessionpresentation.NativeItem{Segments: []sessionpresentation.ActivitySegment{
		{Text: "Read `main.go`", Output: first},
		{Text: "Search `return` in `main.go`", Output: second, Running: true},
	}}}
	view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
	feed := view.RenderFeed(90, 30)
	u := &terminalUI{}
	for _, snippet := range feed.Snippets {
		if u.openOutput(view.bind(u), snippet) {
			break
		}
	}
	if u.output == nil || len(u.output.pages) != 2 || u.output.pages[0].Source != 10 || u.output.pages[1].Source != 10 {
		t.Fatal("click did not open command pages")
	}
	frame := drawOutputDialog(u)
	if !strings.Contains(frame, "1 Read") || !strings.Contains(frame, "2 Search") || strings.Contains(frame, "return 42") {
		t.Fatalf("wrong command tabs or mixed output: %s", frame)
	}
	if u.output.Document.Text != "package main" || u.output.Document.Lines[0].Text == "package main" {
		t.Fatalf("read tab missing syntax or text: %+v", u.output.Document)
	}
	x, y := dialogFramePoint(t, outputDialogScreen(u), "2 Search")
	u.outputMouse(0, x, y, false)
	drawOutputDialog(u)
	if u.output.Page != 1 || u.output.Document.Text != "main.go:12:return 42" {
		t.Fatalf("tab click did not select command: %+v", u.output.Document)
	}
	third := retention.New()
	third.Write("third only\n")
	third.Finish(nil, new(0))
	view.Entries[0].Source.Native.Segments = append(view.Entries[0].Source.Native.Segments, sessionpresentation.ActivitySegment{Text: "Run `echo third`", Output: third})
	drawOutputDialog(u)
	u.outputKey("\x1b[C")
	drawOutputDialog(u)
	if len(u.output.pages) != 3 || u.output.Document.Text != "third only" {
		t.Fatalf("late command missing or mixed: %+v", u.output.Document)
	}
	// A lossy report must replace the tabs with the authoritative combined buffer.
	combined := retention.New()
	combined.Write("first\nsecond\nthird\n")
	combined.Finish(nil, new(1))
	view.Entries[0].Source.Native.Output = combined
	view.Entries[0].Source.OutputTail = []string{"first", "second", "third"}
	frame = drawOutputDialog(u)
	if len(u.output.pages) != 1 || !strings.Contains(frame, "combined output") || u.output.Document.Text != "first\nsecond\nthird" || !strings.Contains(frame, "exit 1") {
		t.Fatalf("lossy report falsely attributed combined output: %s", frame)
	}

}

func TestOutputDialogSingleObservedCommandWithoutTail(t *testing.T) {
	output := new(activitymodel.Retention).New()
	view := newActivityAdapter()
	entry := sessionpresentation.ActivityEntry{Seq: 10, Agent: "Main", Kind: "tool", Text: "Read `main.go`", Native: &sessionpresentation.NativeItem{Command: "cat main.go", Output: output, Running: true, Segments: []sessionpresentation.ActivitySegment{{Source: "cat main.go", Text: "Read `main.go`", Running: true}}}}
	view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
	finishPacing(view)
	feed := view.RenderFeed(90, 30)
	for _, snippet := range feed.Snippets {
		if snippet.Run == 0 {
			continue
		}
		block, ok := view.SnippetBlock(snippet)
		if !ok || !view.ClickTarget(&block, snippet, 90) {
			t.Fatal("empty command output is not clickable")
		}
		u := &terminalUI{}
		if !u.openOutput(view.bind(u), snippet) || len(u.output.pages) != 1 || u.output.pages[0].output != output {
			t.Fatal("single command lost its retained output")
		}
		return
	}
	t.Fatal("single command lost its row")
}

func TestOutputDialogMergedTrackedReadsKeepAllInvocations(t *testing.T) {
	view := newActivityAdapter()
	retention := new(activitymodel.Retention)
	for i, path := range []string{"first.go", "second.py"} {
		output := retention.New()
		output.Write(path + " only\n")
		output.Finish(nil, new(0))
		text := "Read `" + path + "`"
		view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(i + 1), Agent: "Main", Kind: "tool", Text: text, Native: &sessionpresentation.NativeItem{Thread: "main", Item: path, Segments: []sessionpresentation.ActivitySegment{{Text: text, Output: output}}}}}})
	}
	feed := view.RenderFeed(90, 30)
	u := &terminalUI{}
	for _, snippet := range feed.Snippets {
		if u.openOutput(view.bind(u), snippet) {
			break
		}
	}
	if u.output == nil || len(u.output.pages) != 2 {
		t.Fatal("merged row lost an invocation")
	}
	for i, path := range []string{"first.go", "second.py"} {
		u.output.ShowPage(i)
		drawOutputDialog(u)
		if u.output.Document.Text != path+" only" {
			t.Fatalf("page %d: %q", i, u.output.Document.Text)
		}
	}
}

func TestSharedDialogCloseButton(t *testing.T) {
	for _, width := range []int{12, 40, 80, 160} {
		u := dialogForOutput(&activitymodel.Output{})
		u.output.pages[0].Body = "Dialog body"
		rows := make([]string, 20)
		u.paintOutput(rows, width, len(rows))
		frame := ansi.Strip(strings.Join(rows, "\n"))
		if !strings.Contains(frame, terminalui.DialogClose) {
			t.Fatalf("width %d: no close button: %s", width, frame)
		}
		x, y := dialogFramePoint(t, renderedDialogRows(rows, width), terminalui.DialogClose)
		u.outputMouse(0, x, y, false)
		if u.output != nil {
			t.Fatalf("width %d: close button did not dismiss", width)
		}
	}
}

func TestCommandDurationRestoresAndStopsInDialog(t *testing.T) {
	v := newActivityAdapter()
	duration := int64(70000)
	item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: "echo ok", DurationMS: &duration, Status: "completed"}
	applyTranscriptFixture(v, true, "/w", "main", "main", "turn", "cmd", "item/completed", "", item)
	b := v.blocks(0)[0]
	if b.Duration != 70*time.Second || b.Running {
		t.Fatalf("restored timing %+v", b)
	}
	p := v.Painter.DialogPage(b, 80)
	if !strings.HasSuffix(ansi.Strip(p.Title), " · 1m10s") {
		t.Fatalf("title %q", p.Title)
	}
}

func TestRunDurationLiveAndCompletedRendering(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "timed", "type": "commandExecution", "command": "sleep 2", "status": "inProgress"}})
	index := -1
	for i, entry := range u.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Item == "timed" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("missing running command")
	}
	u.source.Entries[index].Source.Native.CommandStarted = time.Now().Add(-2 * time.Second)
	u.testActivity(u.view).replaceEntry(index, u.source.Entries[index].Source, parseLiveActivity(u.source.Entries[index].Source))
	u.view.Runs = nil
	feed := u.testActivity(u.view).RenderFeed(100, 30)
	if !strings.Contains(ansi.Strip(strings.Join(feed.Lines, "\n")), "2s") {
		t.Fatal("live command omitted elapsed")
	}
	opened := false
	for _, snippet := range feed.Snippets {
		if u.shell.openOutput(u.view, snippet) {
			opened = true
			break
		}
	}
	if !opened {
		t.Fatal("missing dialog target")
	}
	drawOutputDialog(u.shell)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "timed", "type": "commandExecution", "command": "sleep 2", "status": "completed", "durationMs": 4000, "exitCode": 0}})
	for range 2 {
		drawOutputDialog(u.shell)
		title := ansi.Strip(u.shell.output.Document.Title)
		if strings.Contains(title, "Running") || !strings.HasSuffix(title, " · 4s") {
			t.Fatalf("completed timing %q", title)
		}
	}
}

func TestBatchDialogDoesNotAttributeInvocationTimingToSegments(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		t.Run(fmt.Sprint(segmented), func(t *testing.T) {
			retention := new(activitymodel.Retention)
			first, second := retention.New(), retention.New()
			first.Write("first\n")
			first.Finish(nil, new(0))
			second.Write("second\n")
			entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: "Main", Kind: "tool", Text: "Run `echo first`\n\nRun `echo second`", Observed: time.Now().Add(-4 * time.Second), Native: &sessionpresentation.NativeItem{Command: "echo first; echo second", Running: true, Output: second}}
			if segmented {
				entry.Native.Segments = []sessionpresentation.ActivitySegment{{Text: "Run `echo first`", Output: first}, {Text: "Run `echo second`", Output: second, Running: true}}
			}
			view := newActivityAdapter()
			view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
			u := &terminalUI{}
			if !u.openEntry(view.bind(u), 1) {
				t.Fatal("could not open batch")
			}
			want := " · 4s"
			if segmented {
				want = ""
			}
			for i := range u.output.pages {
				u.output.ShowPage(i)
				drawOutputDialog(u)
				if title := ansi.Strip(u.output.Document.Title); (want != "" && !strings.HasSuffix(title, want)) || (want == "" && (strings.Contains(title, " · 4s") || strings.Contains(title, " · 7s") || strings.Contains(title, "total"))) {
					t.Fatalf("live batch title %q, want %q", title, want)
				}
			}
			view.Entries[0].Source.Native.Running, view.Entries[0].Source.Native.Duration = false, 7*time.Second
			if segmented {
				view.Entries[0].Source.Native.Segments[1].Running = false
			}
			second.Finish(nil, new(0))
			want = " · 7s"
			if segmented {
				want = ""
			}
			for i := range u.output.pages {
				u.output.ShowPage(i)
				drawOutputDialog(u)
				if title := ansi.Strip(u.output.Document.Title); ((want != "" && !strings.HasSuffix(title, want)) || (want == "" && (strings.Contains(title, " · 4s") || strings.Contains(title, " · 7s") || strings.Contains(title, "total")))) || strings.Contains(title, "Running") {
					t.Fatalf("completed batch title %q, want %q", title, want)
				}
			}
		})
	}
}

func TestNarrativeDialogRefreshesWhileStreaming(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	body := "**Plan**\n\nneedle.\n\n" + strings.Repeat("First paragraph.\n\n", 20)
	delta := func(text string) {
		appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "r", "delta": text})
		rollCommandOutput(u)
	}
	delta(body)
	feed := u.testActivity(u.agents).RenderFeed(90, 30)
	opened := false
	for _, snippet := range feed.Snippets {
		if u.shell.openOutput(u.agents, snippet) {
			opened = true
			break
		}
	}
	if !opened {
		t.Fatal("streaming narrative has no dialog target")
	}
	drawOutputDialog(u.shell)
	d := u.shell.output
	if !d.Document.Live || strings.Contains(drawOutputDialog(u.shell), "paused") {
		t.Fatal("streaming narrative did not follow")
	}
	u.shell.outputKey("/")
	u.shell.outputKey("needle")
	u.shell.outputKey("\r")
	if frame := drawOutputDialog(u.shell); !strings.Contains(frame, "needle.") || !strings.Contains(frame, "paused") {
		t.Fatal("search did not pause at matching content")
	}
	delta("Fresh appended paragraph.")
	frame := drawOutputDialog(u.shell)
	if !strings.Contains(d.Document.Text, "Fresh appended paragraph.") || strings.Contains(frame, "Fresh appended paragraph.") || !strings.Contains(frame, "needle.") || !strings.Contains(frame, "/ n N find") {
		t.Fatalf("delta lost text or reader state: %+v", d)
	}
	completed := body + "Fresh appended paragraph.\n\nFinal paragraph."
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{completed}}})
	rollCommandOutput(u)
	frame = drawOutputDialog(u.shell)
	if d.Document.Live || !strings.Contains(d.Document.Text, "Final paragraph.") || strings.Contains(frame, "Final paragraph.") || !strings.Contains(frame, "needle.") || !strings.Contains(frame, "/ n N find") {
		t.Fatal("completion failed to refresh narrative without moving reader")
	}
	// The retained page remains readable after its source ages out of the feed.
	u.childSource.Entries = nil
	drawOutputDialog(u.shell)
	if !strings.Contains(d.Document.Text, "Final paragraph.") {
		t.Fatal("eviction lost the last available narrative")
	}
}

// testOutputBlock binds fixture output through the same source owner as live observations.
func testOutputBlock(view *activityAdapter, block activityui.Block, output *activitymodel.Output) activityui.Block {
	block.Source, block.Output = uint64(len(view.Entries)+1), 1
	view.appendEntry(sessionpresentation.ActivityEntry{Seq: block.Source, Native: &sessionpresentation.NativeItem{Output: output}}, []activityui.Block{block})
	return block
}

func TestOutputDialogFlatPagesSurviveFeedAgingAndRelease(t *testing.T) {
	view := newActivityAdapter()
	output := new(activitymodel.Retention).New()
	output.Finish(new("first\nlast"), nil)
	entry := sessionpresentation.ActivityEntry{Seq: 41, Kind: "tool", Text: "Run `echo last`", OutputTail: []string{"last"}, OutputOmit: 1, Native: &sessionpresentation.NativeItem{Output: output}}
	view.appendEntry(entry, parseLiveActivity(entry))
	ui := &terminalUI{}
	ui.openBlocks(view.bind(ui), view.blocks(0))
	view.Model.Remove(0, len(view.Entries))
	ui.output.layout(60)
	if ui.output.Document.Text != "first\nlast" || strings.Contains(ansi.Strip(ui.output.Document.Detail), "exit") {
		t.Fatalf("aged output = %+v", ui.output.Document)
	}
	output.Release()
	ui.output.layout(60)
	var rows []string
	for i := range ui.output.Document.Lines {
		rows = append(rows, ui.output.Document.Rows(i, 60)...)
	}
	if ui.output.Document.Text != "last" || !strings.Contains(strings.Join(rows, "\n"), "released") {
		t.Fatalf("released output = %+v", ui.output.Document)
	}

	if len(ui.output.pages) != 1 || ui.output.pages[0].output != output || len(ui.output.pages[0].Members) != 0 {
		t.Fatal("origin duplicated membership or retained a full snapshot")
	}
}

func TestOutputDialogHydratesOnlySelectedRetainedPage(t *testing.T) {
	view := newActivityAdapter()
	retention := new(activitymodel.Retention)
	first, second := retention.New(), retention.New()
	first.Finish(new(strings.Repeat("first\n", 1000)), new(0))
	second.Finish(new(strings.Repeat("second\n", 1000)), new(0))
	entry := sessionpresentation.ActivityEntry{Seq: 1, Kind: "tool", Native: &sessionpresentation.NativeItem{
		Command: "printf first; printf second",
		Segments: []sessionpresentation.ActivitySegment{
			{Source: "printf first", Text: "Run `printf first`", Output: first},
			{Source: "printf second", Text: "Run `printf second`", Output: second},
		},
	}}
	view.appendEntry(entry, parseLiveActivity(entry))
	ui := &terminalUI{}
	ui.openBlocks(view.bind(ui), view.blocks(0))
	frame := drawOutputDialog(ui)
	if !strings.Contains(frame, "first") || !reflect.ValueOf(second).Elem().FieldByName("snapshot").IsNil() {
		t.Fatal("opening one command hydrated its unselected sibling")
	}
	ui.outputKey("\x1b[C")
	drawOutputDialog(ui)
	if !strings.Contains(ui.output.Document.Text, "second") || reflect.ValueOf(second).Elem().FieldByName("snapshot").IsNil() {
		t.Fatal("selecting the sibling did not hydrate its retained output")
	}
}

func TestOutputDialogFoldedStageKeepsSegmentPages(t *testing.T) {
	retention := new(activitymodel.Retention)
	stage, commit := retention.New(), retention.New()
	stage.Finish(new("staged"), new(0))
	commit.Finish(new("committed"), new(0))
	view := newActivityAdapter()
	entry := sessionpresentation.ActivityEntry{Seq: 51, Agent: "Main", Kind: "tool", Text: "Stage `a.go` · git\n\nCommit `update` · git", Native: &sessionpresentation.NativeItem{Segments: []sessionpresentation.ActivitySegment{
		{Text: "Stage `a.go` · git", Output: stage}, {Text: "Commit `update` · git", Output: commit},
	}}}
	view.appendEntry(entry, parseLiveActivity(entry))
	if len(view.blocks(0)) != 1 || len(view.blocks(0)[0].Members) != 2 {
		t.Fatal("feed lost folded stage membership")
	}
	pages := activityui.Disclosure(nil).Blocks(sessionpresentation.CommandPresentation(entry).DetailValues())
	if len(pages) != 2 || pages[0].Verb != "Stage" || pages[1].Verb != "Commit" || view.RetainedOutput(pages[0].Source, pages[0].Output) != stage || view.RetainedOutput(pages[1].Source, pages[1].Output) != commit {
		t.Fatalf("stage pages = %+v", pages)
	}
	ui := &terminalUI{}
	feed := view.RenderFeed(90, 30)
	opened := false
	for i, line := range feed.Lines {
		if strings.Contains(ansi.Strip(line), "Commit") {
			opened = ui.openOutput(view.bind(ui), feed.Snippets[i])
			break
		}
	}
	if !opened {
		t.Fatal("commit row cannot open")
	}
	ui.output.layout(60)
	if ui.output.Document.Text != "committed" {
		t.Fatalf("commit row selected page %d: %q", ui.output.Page, ui.output.Document.Text)
	}
	view.Model.Remove(0, 1)
	for i, want := range []string{"staged", "committed"} {
		ui.output.ShowPage(i)
		ui.output.layout(60)
		if ui.output.Document.Text != want {
			t.Fatalf("page %d = %q", i, ui.output.Document.Text)
		}
	}
}
