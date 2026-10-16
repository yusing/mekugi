package router

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
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
		view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
			Seq: 1, Agent: "Main", Kind: "tool", Text: toolActivityShell(command),
			native: &liveActivityNativeItem{command: command, output: output},
		}}})
		u := &terminalUI{}
		if !u.openEntry(view, 1) || len(u.output.pages) != 1 {
			t.Fatal("missing combined output page")
		}
		rows := make([]string, 24)
		u.paintOutput(rows, 100, len(rows))
		if u.output.laid.Text != "build failed" {
			t.Fatalf("combined output lost host text: %q", u.output.laid.Text)
		}
		assertNativeUISnapshot(t, "combined-edit-output", rows)
	}
}

func dialogForOutput(output *activitymodel.Output) *terminalUI {
	view := newActivityAdapter()
	d := &outputDialog{view: view, pages: []activityui.Block{testOutputBlock(view, activityui.Block{Kind: "op", Verb: "Run", Code: "go test ./..."}, output)}, match: -1}
	d.showPage(0)
	return &terminalUI{output: d}
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
	if frame := drawOutputDialog(u); !strings.Contains(frame, "line 29") || !u.output.follow || u.output.top == 0 {
		t.Fatalf("live dialog did not follow latest output: top=%d frame=%q", u.output.top, frame)
	}
	u.outputKey("g")
	if u.output.follow || u.output.top != 0 {
		t.Fatal("Home did not pause live following")
	}
	output.Write("line 30\n")
	if frame := drawOutputDialog(u); strings.Contains(frame, "line 30") || u.output.top != 0 {
		t.Fatalf("paused dialog jumped to new output: %q", frame)
	}
	u.outputKey("G")
	if frame := drawOutputDialog(u); !u.output.follow || !strings.Contains(frame, "line 30") {
		t.Fatalf("End did not resume live following: %q", frame)
	}
	output.Finish(nil, new(0))
	if frame := drawOutputDialog(u); !strings.Contains(frame, "line 30") || u.output.laid.Live {
		t.Fatalf("settled output disappeared: %q", frame)
	}
}

func TestOutputDialogWheelAndOutsideClickIsolation(t *testing.T) {
	output := new(activitymodel.Retention).New()
	for i := range 30 {
		output.Write(fmt.Sprintf("line %02d\n", i))
	}
	u := dialogForOutput(output)
	u.main = &appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, view: newActivityAdapter()}
	drawOutputDialog(u)
	feedOffset := u.main.view.Offset
	u.outputMouse(64, 0, 0, false)
	if u.output.top >= u.output.bottom() || u.output.follow || u.main.view.Offset != feedOffset {
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
	u.main = &appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, view: newActivityAdapter()}
	drawOutputDialog(u)
	u.outputKey("/")
	for _, key := range []string{"n", "e", "e", "d", "l", "e", "\r"} {
		u.outputKey(key)
	}
	first := u.output.match
	if first < 0 || u.output.missed {
		t.Fatal("search missed retained output")
	}
	u.outputKey("n")
	if u.output.match <= first {
		t.Fatal("next search did not advance")
	}
	u.outputKey("N")
	if u.output.match != first {
		t.Fatal("previous search did not return")
	}
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
			u.openBlocks(view, []activityui.Block{testOutputBlock(view, tc.block, output)})
			drawOutputDialog(u)
			d := u.output
			if strings.Contains(ansi.Strip(strings.Join(d.body, "\n")), "Needle") {
				t.Fatal("fixture already shows the distant match")
			}
			for _, key := range []string{"/", "needle", "\r"} {
				u.outputKey(key)
			}
			drawOutputDialog(u)
			first, top := d.match, d.top
			if first < 0 || top == 0 || !strings.Contains(ansi.Strip(strings.Join(d.body, "\n")), "Needle") {
				t.Fatalf("search did not reveal the matching wrapped row: match=%d top=%d body=%q", first, top, d.body)
			}
			u.outputKey("n")
			drawOutputDialog(u)
			if d.match != first+1 || !strings.Contains(ansi.Strip(strings.Join(d.body, "\n")), "Needle") {
				t.Fatal("next did not reveal the next logical match")
			}
			u.outputKey("N")
			drawOutputDialog(u)
			if d.match != first || d.top != top {
				t.Fatal("previous did not return to the first matching wrapped row")
			}
		})
	}
}

func TestOutputDialogRestoredCommandUsesRetainedAggregate(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.Conversation = true
	output := "first\nsecond\nthird\n"
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "cmd", Type: "commandExecution", Command: "go test ./...", AggregatedOutput: &output, ExitCode: new(1),
	}}}})
	feed := u.view.RenderFeed(90, 60)
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
	if got := terminal.output.laid.Text; got != "first\nsecond\nthird" {
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
	if len(u.view.Entries) != 1 || len(u.view.blocks(0)) == 0 {
		t.Fatal("start did not create command block")
	}
	retained := u.view.retainedOutput(u.view.blocks(0)[len(u.view.blocks(0))-1])
	if retained == nil {
		t.Fatal("started command has no retained output")
	}
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": "streamed\n"})
	u.flushStreamOutput()
	if got := u.view.retainedOutput(u.view.blocks(0)[len(u.view.blocks(0))-1]); got != retained {
		t.Fatal("delta changed retained output identity")
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "aggregate\nfinal\n"
	notify("item/completed", map[string]any{"item": item})
	rollCommandOutput(u)
	feed := u.view.RenderFeed(90, 60)
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
	if terminal.output.retainedOutput(terminal.output.pages[0]) != retained {
		t.Fatal("completion replaced retained output identity")
	}
	terminal.output.layout(76)
	if got := terminal.output.laid.Text; got != "aggregate\nfinal" {
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
	entry := activityPaneEntry{Kind: "tool", Text: "Run `echo one`; Run `echo two`", native: &liveActivityNativeItem{
		segments: []commandSegment{{text: "Run `echo one`", output: first}, {text: "Run `echo two`", output: second, exit: 2}},
	}}
	blocks := commandSegmentBlocks(entry, false)
	if len(blocks) != 2 || blocks[0].Output != 2 || blocks[1].Output != 3 {
		t.Fatalf("tracked segment output ownership = %+v", blocks)
	}
	view := newActivityAdapter()
	view.appendEntry(entry, blocks)
	u := &terminalUI{output: &outputDialog{view: view, pages: blocks, match: -1}}
	u.output.showPage(0)
	u.output.layout(76)
	if got := u.output.laid.Text; got != "segment one" {
		t.Fatalf("first segment = %q", got)
	}
	u.outputKey("\x1b[C")
	u.output.layout(76)
	if got := u.output.laid.Text; got != "segment two" || !strings.Contains(ansi.Strip(u.output.laid.Detail), "exit 2") {
		t.Fatalf("second segment = %q detail %q", got, u.output.laid.Detail)
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
			if width < outputDialogFullWidth {
				if u.output.rect.X != 0 || u.output.rect.Y != 0 || u.output.rect.W != width || u.output.rect.H != height {
					t.Fatalf("narrow dialog did not take screen width: %+v", u.output.rect)
				}
			} else if u.output.rect.W > int(float64(width)*outputDialogShare) || u.output.rect.X <= 0 {
				t.Fatalf("wide dialog exceeded frame limit: %+v", u.output.rect)
			}
		})
	}
}

func TestOutputDialogRestoredMultiReadKeepsCombinedOutput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.Conversation = true
	combined := "content of a\ncontent of b\n"
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "read", Type: "commandExecution", Command: "cat a; cat b", AggregatedOutput: &combined, ExitCode: new(0),
		CommandActions: []appServerCommandAction{{Type: "read", Path: "a", Command: "cat a"}, {Type: "read", Path: "b", Command: "cat b"}},
	}}}})
	feed := u.view.RenderFeed(90, 60)
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
		terminal.output.showPage(i)
		terminal.output.layout(76)
		if terminal.output.laid.Text == "content of a\ncontent of b" {
			found = true
		}
	}
	if !found {
		t.Fatal("combined retained output absent from every invocation page")
	}
}

func TestOutputDialogTrackedOutputSettlesAndLossyFallsBack(t *testing.T) {
	key := [3]string{"main", "t", "cmd"}
	retention := new(activitymodel.Retention)
	hub := &execTrackHub{tracks: map[[3]string]*execTrack{key: {
		segments: []execTrackSegment{{source: "echo one", began: true, fresh: []byte("initial\n")}, {source: "echo two"}}, dirty: true,
	}}}
	view, _ := hub.view(key, false, func(string) string { return "Run `echo one`" }, retention)
	if !view.output || len(view.segments) != 1 || view.segments[0].output == nil {
		t.Fatalf("live segment did not retain output: %+v", view)
	}
	retained := view.segments[0].output
	track := hub.tracks[key]
	track.segments[0].ended = true
	track.segments[0].code = 2
	track.ended, track.closed, track.done, track.code = true, true, true, 2
	view, _ = hub.view(key, true, func(string) string { return "Run `echo one`" }, retention)
	if !view.complete || view.segments[0].output != retained || !retained.View().Done || retained.View().Exit != 2 {
		t.Fatalf("final segment did not settle retained output: %+v, %+v", view, retained.View())
	}

	other := [3]string{"main", "t", "overflow"}
	hub.tracks[other] = &execTrack{segments: []execTrackSegment{{source: "echo huge", began: true, fresh: []byte("prior\n")}, {source: "echo done"}}}
	first, _ := hub.view(other, false, func(string) string { return "Run `echo huge`" }, retention)
	previous := first.segments[0].output
	if previous == nil {
		t.Fatal("overflow setup did not allocate segment output")
	}
	hub.tracks[other].apply(segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: strings.Repeat("x", activitymodel.OutputBytes+1)})
	overflow, _ := hub.view(other, false, func(string) string { return "Run `echo huge`" }, retention)
	if overflow.output || !hub.tracks[other].lossy {
		t.Fatal("over-bound fresh output did not select combined host fallback")
	}
	if got := previous.View(); !got.Released && !got.Done {
		t.Fatalf("abandoned segment output remains live: %+v", got)
	}
}

func TestOutputDialogLossyMessageReleasesLiveSegment(t *testing.T) {
	key := [3]string{"main", "t", "lossy"}
	track := &execTrack{segments: []execTrackSegment{{source: "echo text", began: true, fresh: []byte("partial")}, {source: "echo done"}}}
	hub := &execTrackHub{tracks: map[[3]string]*execTrack{key: track}}
	var retention activitymodel.Retention
	view, _ := hub.view(key, false, execSegmentText, &retention)
	output := view.segments[0].output
	track.apply(segmenttransport.Message{Type: segmenttransport.Lossy})
	view, _ = hub.view(key, false, execSegmentText, &retention)
	if view.output || !output.View().Done || !output.View().Released {
		t.Fatalf("lossy segment retained live output: %+v", output.View())
	}
}

func TestOutputDialogTerminalKeyDecoderOwnsPrefixAndPaging(t *testing.T) {
	output := new(activitymodel.Retention).New()
	for i := range 30 {
		output.Write(fmt.Sprintf("row %02d\n", i))
	}
	u := dialogForOutput(output)
	u.main = &appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, view: newActivityAdapter()}
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
	track := &execTrack{segments: []execTrackSegment{{source: "echo text", began: true, fresh: []byte("first\n")}, {source: "sleep 1"}}}
	hub := &execTrackHub{tracks: map[[3]string]*execTrack{key: track}}
	var retention activitymodel.Retention
	view, _ := hub.view(key, false, execSegmentText, &retention)
	output := view.segments[0].output
	if output == nil || output.View().Done || strings.Join(output.View().Lines, "\n") != "first" {
		t.Fatal("live segment output missing")
	}
	track.segments[0].fresh = []byte("second\n")
	track.segments[0].ended = true
	track.segments[0].code = 3
	view, _ = hub.view(key, true, execSegmentText, &retention)
	if view.segments[0].output != output || !output.View().Done || output.View().Exit != 3 || strings.Join(output.View().Lines, "\n") != "first\nsecond" {
		t.Fatalf("settled segment: %+v", output.View())
	}
	if len(track.segments[0].fresh) != 0 {
		t.Fatal("pending output not drained")
	}
}

func TestOutputDialogCommandTabs(t *testing.T) {
	retention := new(activitymodel.Retention)
	first, second := retention.New(), retention.New()
	first.Write("package main\n\n")
	first.Finish(nil, new(0))
	second.Write("main.go:12:return 42\n")
	view := newActivityAdapter()
	entry := activityPaneEntry{Seq: 10, Agent: "Main", Kind: "tool", Text: "Read `main.go`\n\nSearch `return` in `main.go`", native: &liveActivityNativeItem{segments: []commandSegment{
		{text: "Read `main.go`", output: first},
		{text: "Search `return` in `main.go`", output: second, running: true},
	}}}
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	feed := view.RenderFeed(90, 30)
	u := &terminalUI{}
	for _, snippet := range feed.Snippets {
		if u.openOutput(view, snippet) {
			break
		}
	}
	if u.output == nil || len(u.output.origins) != 2 || u.output.origins[0].block.Source != 10 || u.output.origins[1].block.Source != 10 {
		t.Fatal("click did not open command pages")
	}
	frame := drawOutputDialog(u)
	if !strings.Contains(frame, "1 Read") || !strings.Contains(frame, "2 Search") || strings.Contains(frame, "return 42") {
		t.Fatalf("wrong command tabs or mixed output: %s", frame)
	}
	if u.output.laid.Text != "package main" || u.output.laid.Lines[0].Text == "package main" {
		t.Fatalf("read tab missing syntax or text: %+v", u.output.laid)
	}
	tab := u.output.tabs[1]
	u.outputMouse(0, u.output.rect.X+2+tab.from, u.output.rect.Y+2, false)
	drawOutputDialog(u)
	if u.output.page != 1 || u.output.laid.Text != "main.go:12:return 42" {
		t.Fatalf("tab click did not select command: %+v", u.output.laid)
	}
	third := retention.New()
	third.Write("third only\n")
	third.Finish(nil, new(0))
	view.Entries[0].Source.native.segments = append(view.Entries[0].Source.native.segments, commandSegment{text: "Run `echo third`", output: third})
	drawOutputDialog(u)
	u.outputKey("\x1b[C")
	drawOutputDialog(u)
	if len(u.output.pages) != 3 || u.output.laid.Text != "third only" {
		t.Fatalf("late command missing or mixed: %+v", u.output.laid)
	}
	// A lossy report must replace the tabs with the authoritative combined buffer.
	combined := retention.New()
	combined.Write("first\nsecond\nthird\n")
	combined.Finish(nil, new(1))
	view.Entries[0].Source.native.output = combined
	view.Entries[0].Source.outputTail = []string{"first", "second", "third"}
	frame = drawOutputDialog(u)
	if len(u.output.pages) != 1 || !strings.Contains(frame, "combined output") || u.output.laid.Text != "first\nsecond\nthird" || !strings.Contains(frame, "exit 1") {
		t.Fatalf("lossy report falsely attributed combined output: %s", frame)
	}

}

func TestOutputDialogSingleObservedCommandWithoutTail(t *testing.T) {
	output := new(activitymodel.Retention).New()
	view := newActivityAdapter()
	entry := activityPaneEntry{Seq: 10, Agent: "Main", Kind: "tool", Text: "Read `main.go`", native: &liveActivityNativeItem{command: "cat main.go", output: output, running: true, segments: []commandSegment{{source: "cat main.go", text: "Read `main.go`", running: true}}}}
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
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
		if !u.openOutput(view, snippet) || len(u.output.pages) != 1 || u.output.retainedOutput(u.output.pages[0]) != output {
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
		view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: uint64(i + 1), Agent: "Main", Kind: "tool", Text: text, native: &liveActivityNativeItem{thread: "main", item: path, segments: []commandSegment{{text: text, output: output}}}}}})
	}
	feed := view.RenderFeed(90, 30)
	u := &terminalUI{}
	for _, snippet := range feed.Snippets {
		if u.openOutput(view, snippet) {
			break
		}
	}
	if u.output == nil || len(u.output.pages) != 2 {
		t.Fatal("merged row lost an invocation")
	}
	for i, path := range []string{"first.go", "second.py"} {
		u.output.showPage(i)
		drawOutputDialog(u)
		if u.output.laid.Text != path+" only" {
			t.Fatalf("page %d: %q", i, u.output.laid.Text)
		}
	}
}

func TestOutputDialogTabsKeepNavigationState(t *testing.T) {
	retention := new(activitymodel.Retention)
	first, second := retention.New(), retention.New()
	first.Write(strings.Repeat("first row\n", 50))
	first.Finish(nil, new(0))
	second.Write("second row\n")
	second.Finish(nil, new(0))
	u := dialogForOutput(first)
	u.output.pages = append(u.output.pages, testOutputBlock(u.output.view, activityui.Block{Verb: "Run"}, second))
	drawOutputDialog(u)
	u.outputKey("\x1b[6~")
	top := u.output.top
	u.outputKey("/")
	u.outputKey("first")
	tab := u.output.tabs[1]
	u.outputMouse(0, u.output.rect.X+2+tab.from, u.output.rect.Y+2, false)
	drawOutputDialog(u)
	if u.output.top != 0 || u.output.typing || u.output.draft != "" {
		t.Fatal("new tab borrowed navigation state")
	}
	u.outputKey("\x1b[D")
	drawOutputDialog(u)
	if top == 0 || u.output.top != top || !u.output.typing || u.output.draft != "first" {
		t.Fatal("return lost tab navigation state")
	}
}

func TestSharedDialogCloseButton(t *testing.T) {
	for _, width := range []int{12, 40, 80, 160} {
		u := dialogForOutput(&activitymodel.Output{})
		u.output.pages[0].Body = "Dialog body"
		rows := make([]string, 20)
		u.paintOutput(rows, width, len(rows))
		d := u.output
		frame := ansi.Strip(strings.Join(rows, "\n"))
		if !strings.Contains(frame, activityui.DialogClose) {
			t.Fatalf("width %d: no close button: %s", width, frame)
		}
		u.outputMouse(0, d.rect.X+d.rect.W-6, d.rect.Y, false)
		if u.output != nil {
			t.Fatalf("width %d: close button did not dismiss", width)
		}
	}
}

func TestCommandDurationRestoresAndStopsInDialog(t *testing.T) {
	v := newActivityAdapter()
	duration := int64(70000)
	item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "echo ok", DurationMS: &duration, Status: "completed"}
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
	for i, entry := range u.view.Entries {
		if entry.Source.native != nil && entry.Source.native.item == "timed" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("missing running command")
	}
	u.view.Entries[index].Source.native.commandStarted = time.Now().Add(-2 * time.Second)
	u.view.replaceEntry(index, u.view.Entries[index].Source, parseLiveActivity(u.view.Entries[index].Source))
	u.view.Runs = nil
	feed := u.view.RenderFeed(100, 30)
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
		title := ansi.Strip(u.shell.output.laid.Title)
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
			entry := activityPaneEntry{Seq: 1, Agent: "Main", Kind: "tool", Text: "Run `echo first`\n\nRun `echo second`", Observed: time.Now().Add(-4 * time.Second), native: &liveActivityNativeItem{command: "echo first; echo second", running: true, output: second}}
			if segmented {
				entry.native.segments = []commandSegment{{text: "Run `echo first`", output: first}, {text: "Run `echo second`", output: second, running: true}}
			}
			view := newActivityAdapter()
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
			u := &terminalUI{}
			if !u.openEntry(view, 1) {
				t.Fatal("could not open batch")
			}
			want := " · 4s"
			if segmented {
				want = ""
			}
			for i := range u.output.pages {
				u.output.showPage(i)
				drawOutputDialog(u)
				if title := ansi.Strip(u.output.laid.Title); (want != "" && !strings.HasSuffix(title, want)) || (want == "" && (strings.Contains(title, " · 4s") || strings.Contains(title, " · 7s") || strings.Contains(title, "total"))) {
					t.Fatalf("live batch title %q, want %q", title, want)
				}
			}
			view.Entries[0].Source.native.running, view.Entries[0].Source.native.duration = false, 7*time.Second
			if segmented {
				view.Entries[0].Source.native.segments[1].running = false
			}
			second.Finish(nil, new(0))
			want = " · 7s"
			if segmented {
				want = ""
			}
			for i := range u.output.pages {
				u.output.showPage(i)
				drawOutputDialog(u)
				if title := ansi.Strip(u.output.laid.Title); ((want != "" && !strings.HasSuffix(title, want)) || (want == "" && (strings.Contains(title, " · 4s") || strings.Contains(title, " · 7s") || strings.Contains(title, "total")))) || strings.Contains(title, "Running") {
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
	feed := u.agents.RenderFeed(90, 30)
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
	if !d.laid.Live || !d.follow {
		t.Fatal("streaming narrative did not follow")
	}
	u.shell.outputKey("/")
	u.shell.outputKey("needle")
	u.shell.outputKey("\r")
	top, match := d.top, d.match
	if d.follow || match < 0 {
		t.Fatal("search did not pause at matching content")
	}
	delta("Fresh appended paragraph.")
	drawOutputDialog(u.shell)
	if !strings.Contains(d.laid.Text, "Fresh appended paragraph.") || d.top != top || d.match != match || d.query != "needle" || d.follow {
		t.Fatalf("delta lost text or reader state: %+v", d)
	}
	completed := body + "Fresh appended paragraph.\n\nFinal paragraph."
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{completed}}})
	rollCommandOutput(u)
	drawOutputDialog(u.shell)
	if d.laid.Live || !strings.Contains(d.laid.Text, "Final paragraph.") || d.top != top || d.query != "needle" || d.follow {
		t.Fatal("completion failed to refresh narrative without moving reader")
	}
	// The retained page remains readable after its source ages out of the feed.
	u.agents.Entries = nil
	drawOutputDialog(u.shell)
	if !strings.Contains(d.laid.Text, "Final paragraph.") {
		t.Fatal("eviction lost the last available narrative")
	}
}

// testOutputBlock binds fixture output through the same source owner as live observations.
func testOutputBlock(view *activityAdapter, block activityui.Block, output *activitymodel.Output) activityui.Block {
	block.Source, block.Output = uint64(len(view.Entries)+1), 1
	view.appendEntry(activityPaneEntry{Seq: block.Source, native: &liveActivityNativeItem{output: output}}, []activityui.Block{block})
	return block
}

func TestOutputDialogFlatOriginsSurviveFeedAgingAndRelease(t *testing.T) {
	view := newActivityAdapter()
	output := new(activitymodel.Retention).New()
	output.Finish(new("first\nlast"), nil)
	entry := activityPaneEntry{Seq: 41, Kind: "tool", Text: "Run `echo last`", outputTail: []string{"last"}, outputOmit: 1, native: &liveActivityNativeItem{output: output}}
	view.appendEntry(entry, parseLiveActivity(entry))
	ui := &terminalUI{}
	ui.openBlocks(view, view.blocks(0))
	view.removeEntries(0, len(view.Entries))
	ui.output.layout(60)
	if ui.output.laid.Text != "first\nlast" || strings.Contains(ansi.Strip(ui.output.laid.Detail), "exit") {
		t.Fatalf("aged output = %+v", ui.output.laid)
	}
	output.Release()
	ui.output.layout(60)
	var rows []string
	for i := range ui.output.laid.Lines {
		rows = append(rows, ui.output.laid.Rows(i, 60)...)
	}
	if ui.output.laid.Text != "last" || !strings.Contains(strings.Join(rows, "\n"), "released") {
		t.Fatalf("released output = %+v", ui.output.laid)
	}

	if len(ui.output.origins) != 1 || ui.output.origins[0].output != output || ui.output.origins[0].block.OutputSnapshot != nil {
		t.Fatal("origin duplicated membership or retained a full snapshot")
	}
}

func TestOutputDialogFoldedStageKeepsSegmentPages(t *testing.T) {
	retention := new(activitymodel.Retention)
	stage, commit := retention.New(), retention.New()
	stage.Finish(new("staged"), new(0))
	commit.Finish(new("committed"), new(0))
	view := newActivityAdapter()
	entry := activityPaneEntry{Seq: 51, Agent: "Main", Kind: "tool", Text: "Stage `a.go` · git\n\nCommit `update` · git", native: &liveActivityNativeItem{segments: []commandSegment{
		{text: "Stage `a.go` · git", output: stage}, {text: "Commit `update` · git", output: commit},
	}}}
	view.appendEntry(entry, parseLiveActivity(entry))
	if len(view.blocks(0)) != 1 || len(view.blocks(0)[0].Members) != 2 {
		t.Fatal("feed lost folded stage membership")
	}
	pages := view.commandOutputPages(entry.Seq)
	if len(pages) != 2 || pages[0].Verb != "Stage" || pages[1].Verb != "Commit" || view.retainedOutput(pages[0]) != stage || view.retainedOutput(pages[1]) != commit {
		t.Fatalf("stage pages = %+v", pages)
	}
	ui := &terminalUI{}
	feed := view.RenderFeed(90, 30)
	opened := false
	for i, line := range feed.Lines {
		if strings.Contains(ansi.Strip(line), "Commit") {
			opened = ui.openOutput(view, feed.Snippets[i])
			break
		}
	}
	if !opened {
		t.Fatal("commit row cannot open")
	}
	ui.output.layout(60)
	if ui.output.laid.Text != "committed" {
		t.Fatalf("commit row selected page %d: %q", ui.output.page, ui.output.laid.Text)
	}
	view.removeEntries(0, 1)
	for i, want := range []string{"staged", "committed"} {
		ui.output.showPage(i)
		ui.output.layout(60)
		if ui.output.laid.Text != want {
			t.Fatalf("page %d = %q", i, ui.output.laid.Text)
		}
	}
}
