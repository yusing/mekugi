package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/execsegment"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func dialogForOutput(output *activityui.Output) *terminalUI {
	view := newLiveActivityView()
	d := &outputDialog{view: view, pages: []activityui.Block{{Kind: "op", Verb: "Run", Code: "go test ./...", Output: output}}, match: -1}
	d.showPage(0)
	return &terminalUI{output: d}
}

func drawOutputDialog(u *terminalUI) string {
	rows := make([]string, 18)
	u.paintOutput(rows, 80, len(rows))
	return ansi.Strip(strings.Join(rows, "\n"))
}

func TestOutputDialogLiveFollowPauseAndEnd(t *testing.T) {
	output := new(activityui.Retention).New()
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
	output := new(activityui.Retention).New()
	for i := range 30 {
		output.Write(fmt.Sprintf("line %02d\n", i))
	}
	u := dialogForOutput(output)
	u.main = &appServerUI{view: newLiveActivityView()}
	drawOutputDialog(u)
	feedOffset := u.main.view.offset
	u.outputMouse(64, 0, 0, false)
	if u.output.top >= u.output.bottom() || u.output.follow || u.main.view.offset != feedOffset {
		t.Fatal("wheel did not pause dialog independently of feed")
	}
	u.outputMouse(65, 0, 0, false)
	if u.main.view.offset != feedOffset {
		t.Fatal("wheel moved feed behind dialog")
	}
	u.outputMouse(0, 0, 0, false)
	if u.output != nil {
		t.Fatal("outside click did not close output dialog")
	}
}

func TestOutputDialogSearchAndCopyRetainedText(t *testing.T) {
	output := new(activityui.Retention).New()
	output.Write("alpha\nneedle one\nbeta\nneedle two\n")
	output.Finish(nil, new(0))
	u := dialogForOutput(output)
	u.main = &appServerUI{view: newLiveActivityView()}
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
	if u.clipboard == "" || !strings.Contains(u.clipboard, "\x1b]52;c;") {
		t.Fatal("copy did not send retained output to terminal clipboard")
	}
	u.outputKey("q")
	if u.output != nil {
		t.Fatal("q did not close output dialog")
	}
}

func TestOutputDialogTracksRetainedOutputAfterOpening(t *testing.T) {
	output := new(activityui.Retention).New()
	output.Write("streamed\n")
	u := dialogForOutput(output)
	drawOutputDialog(u)
	final := "host aggregate\nfinal line\n"
	output.Finish(&final, new(1))
	if frame := drawOutputDialog(u); !strings.Contains(frame, "final line") || strings.Contains(frame, "streamed") || !strings.Contains(frame, "exit 1") {
		t.Fatalf("dialog did not refresh from retained aggregate: %q", frame)
	}
}

func TestOutputDialogRestoredCommandUsesRetainedAggregate(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	output := "first\nsecond\nthird\n"
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "cmd", Type: "commandExecution", Command: "go test ./...", AggregatedOutput: &output, ExitCode: new(1),
	}}}})
	feed := u.view.renderFeed(90, 60)
	index := -1
	for i, line := range feed.lines {
		if strings.Contains(ansi.Strip(line), "Ran go test ./...") {
			index = i
			break
		}
	}
	terminal := &terminalUI{main: u}
	if index < 0 || !terminal.openOutput(u.view, feed.snippets[index]) {
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
	if len(u.view.entries) != 1 || len(u.view.entries[0].blocks) == 0 {
		t.Fatal("start did not create command block")
	}
	retained := u.view.entries[0].blocks[len(u.view.entries[0].blocks)-1].Output
	if retained == nil {
		t.Fatal("started command has no retained output")
	}
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": "streamed\n"})
	u.flushStreamOutput()
	if got := u.view.entries[0].blocks[len(u.view.entries[0].blocks)-1].Output; got != retained {
		t.Fatal("delta changed retained output identity")
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "aggregate\nfinal\n"
	notify("item/completed", map[string]any{"item": item})
	rollCommandOutput(u)
	feed := u.view.renderFeed(90, 60)
	index := -1
	for i, line := range feed.lines {
		if strings.Contains(ansi.Strip(line), "Ran go test ./...") {
			index = i
			break
		}
	}
	terminal := &terminalUI{main: u}
	if index < 0 || !terminal.openOutput(u.view, feed.snippets[index]) {
		t.Fatal("merged command has no output dialog target")
	}
	if terminal.output.pages[0].Output != retained {
		t.Fatal("completion replaced retained output identity")
	}
	terminal.output.layout(76)
	if got := terminal.output.laid.Text; got != "aggregate\nfinal" {
		t.Fatalf("merged retained output = %q", got)
	}
}

func TestOutputDialogTrackedSegmentKeepsOwnRetention(t *testing.T) {
	retention := new(activityui.Retention)
	first, second := retention.New(), retention.New()
	first.Write("segment one\n")
	first.Finish(nil, new(0))
	second.Write("segment two\n")
	second.Finish(nil, new(2))
	entry := activityPaneEntry{Kind: "tool", Text: "Run `echo one`; Run `echo two`", native: &liveActivityNativeItem{
		segments: []commandSegment{{text: "Run `echo one`", output: first}, {text: "Run `echo two`", output: second, exit: 2}},
	}}
	blocks := commandSegmentBlocks(entry)
	if len(blocks) != 2 || blocks[0].Output != first || blocks[1].Output != second {
		t.Fatalf("tracked segment output ownership = %+v", blocks)
	}
	view := newLiveActivityView()
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

func TestOutputDialogVTOverlayWideAndNarrow(t *testing.T) {
	for _, width := range []int{100, 50} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			output := new(activityui.Retention).New()
			output.Write("modal content\n")
			output.Finish(nil, new(0))
			u := dialogForOutput(output)
			const height = 20
			rows := make([]string, height)
			for i := range rows {
				rows[i] = "background"
			}
			u.paintOutput(rows, width, height)
			screen := vt.NewEmulator(width, height)
			defer screen.Close()
			for i, row := range rows {
				if _, err := screen.WriteString(fmt.Sprintf("\x1b[%d;1H%s", i+1, row)); err != nil {
					t.Fatal(err)
				}
			}
			frame := ansi.Strip(screen.String())
			border := screen.CellAt(u.output.rect.x, u.output.rect.y).Style
			if border.Bg == nil || border.Attrs&uv.AttrFaint != 0 {
				t.Fatalf("dialog inherited backdrop or lost surface: %+v", border)
			}
			if width >= outputDialogFullWidth {
				background := screen.CellAt(0, 0).Style
				if background.Bg != nil || background.Attrs&uv.AttrFaint == 0 {
					t.Fatalf("backdrop lost isolation: %+v", background)
				}
			}
			t.Logf("dialog frame:\n%s", frame)
			if !strings.Contains(frame, "modal content") || width >= outputDialogFullWidth && !strings.Contains(frame, "background") {
				t.Fatalf("VT overlay lost dialog or background: %q", frame)
			}
			if width < outputDialogFullWidth {
				if u.output.rect.x != 0 || u.output.rect.y != 0 || u.output.rect.w != width || u.output.rect.h != height {
					t.Fatalf("narrow dialog did not take screen width: %+v", u.output.rect)
				}
			} else if u.output.rect.w > int(float64(width)*outputDialogShare) || u.output.rect.x <= 0 {
				t.Fatalf("wide dialog exceeded frame limit: %+v", u.output.rect)
			}
		})
	}
}

func TestOutputDialogRestoredMultiReadKeepsCombinedOutput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	combined := "content of a\ncontent of b\n"
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "read", Type: "commandExecution", Command: "cat a; cat b", AggregatedOutput: &combined, ExitCode: new(0),
		CommandActions: []appServerCommandAction{{Type: "read", Path: "a", Command: "cat a"}, {Type: "read", Path: "b", Command: "cat b"}},
	}}}})
	feed := u.view.renderFeed(90, 60)
	terminal := &terminalUI{main: u}
	opened := false
	for _, snippet := range feed.snippets {
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
	retention := new(activityui.Retention)
	hub := &execTrackHub{tracks: map[[3]string]*execTrack{key: {
		segments: []execTrackSegment{{source: "echo one", began: true, fresh: []byte("initial\n")}}, dirty: true,
	}}}
	view, _ := hub.view(key, false, func(string) string { return "Run `echo one`" }, retention)
	if !view.output || len(view.segments) != 1 || view.segments[0].output == nil {
		t.Fatalf("live segment did not retain output: %+v", view)
	}
	retained := view.segments[0].output
	track := hub.tracks[key]
	track.segments[0].ended = true
	track.segments[0].code = 2
	track.ended, track.done, track.code = true, true, 2
	view, _ = hub.view(key, true, func(string) string { return "Run `echo one`" }, retention)
	if !view.complete || view.segments[0].output != retained || !retained.View().Done || retained.View().Exit != 2 {
		t.Fatalf("final segment did not settle retained output: %+v, %+v", view, retained.View())
	}

	other := [3]string{"main", "t", "overflow"}
	hub.tracks[other] = &execTrack{segments: []execTrackSegment{{source: "echo huge", began: true, fresh: []byte("prior\n")}}}
	first, _ := hub.view(other, false, func(string) string { return "Run `echo huge`" }, retention)
	previous := first.segments[0].output
	if previous == nil {
		t.Fatal("overflow setup did not allocate segment output")
	}
	hub.tracks[other].apply(execsegment.Message{Type: execsegment.Output, Index: 0, Data: strings.Repeat("x", activityui.OutputBytes+1)})
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
	track := &execTrack{segments: []execTrackSegment{{source: "echo text", began: true, fresh: []byte("partial")}}}
	hub := &execTrackHub{tracks: map[[3]string]*execTrack{key: track}}
	var retention activityui.Retention
	view, _ := hub.view(key, false, execSegmentText, &retention)
	output := view.segments[0].output
	track.apply(execsegment.Message{Type: execsegment.Lossy})
	view, _ = hub.view(key, false, execSegmentText, &retention)
	if view.output || !output.View().Done || !output.View().Released {
		t.Fatalf("lossy segment retained live output: %+v", output.View())
	}
}

func TestOutputDialogTerminalKeyDecoderOwnsPrefixAndPaging(t *testing.T) {
	output := new(activityui.Retention).New()
	for i := range 30 {
		output.Write(fmt.Sprintf("row %02d\n", i))
	}
	u := dialogForOutput(output)
	u.main = &appServerUI{view: newLiveActivityView()}
	drawOutputDialog(u)
	u.focus, u.side, u.activityOpen = 0, false, false
	for _, key := range []byte{2, '3'} {
		if err := u.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.focus != 0 || u.side || u.activityOpen || u.prefix {
		t.Fatal("modal Ctrl+B pane number changed background pane")
	}
	for _, seq := range []string{"\x02\x1b[5~", "\x02\x1b[6~"} {
		for i := range len(seq) {
			if err := u.key(seq[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if u.main.view.offset != 0 || u.output == nil {
		t.Fatal("modal paging reached background feed or closed dialog")
	}
}

func TestOutputDialogTrackedViewDrainsRetainedOutput(t *testing.T) {
	key := [3]string{"thread", "turn", "item"}
	track := &execTrack{segments: []execTrackSegment{{source: "echo text", began: true, fresh: []byte("first\n")}}}
	hub := &execTrackHub{tracks: map[[3]string]*execTrack{key: track}}
	var retention activityui.Retention
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
	retention := new(activityui.Retention)
	first, second := retention.New(), retention.New()
	first.Write("package main\n\n")
	first.Finish(nil, new(0))
	second.Write("main.go:12:return 42\n")
	view := newLiveActivityView()
	entry := activityPaneEntry{Seq: 10, Agent: "Main", Kind: "tool", Text: "Read `main.go`\n\nSearch `return` in `main.go`", native: &liveActivityNativeItem{segments: []commandSegment{
		{text: "Read `main.go`", output: first},
		{text: "Search `return` in `main.go`", output: second, running: true},
	}}}
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	feed := view.renderFeed(90, 30)
	u := &terminalUI{}
	for _, snippet := range feed.snippets {
		if u.openOutput(view, snippet) {
			break
		}
	}
	if u.output == nil || len(u.output.origins) != 1 || u.output.origins[0].Source != 10 {
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
	u.outputMouse(0, u.output.rect.x+2+tab.from, u.output.rect.y+2, false)
	drawOutputDialog(u)
	if u.output.page != 1 || u.output.laid.Text != "main.go:12:return 42" {
		t.Fatalf("tab click did not select command: %+v", u.output.laid)
	}
	third := retention.New()
	third.Write("third only\n")
	third.Finish(nil, new(0))
	view.entries[0].native.segments = append(view.entries[0].native.segments, commandSegment{text: "Run `echo third`", output: third})
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
	view.entries[0].native.output = combined
	view.entries[0].outputTail = []string{"first", "second", "third"}
	frame = drawOutputDialog(u)
	if len(u.output.pages) != 1 || !strings.Contains(frame, "combined output") || u.output.laid.Text != "first\nsecond\nthird" || !strings.Contains(frame, "exit 1") {
		t.Fatalf("lossy report falsely attributed combined output: %s", frame)
	}

}

func TestOutputDialogMergedTrackedReadsKeepAllInvocations(t *testing.T) {
	view := newLiveActivityView()
	retention := new(activityui.Retention)
	for i, path := range []string{"first.go", "second.py"} {
		output := retention.New()
		output.Write(path + " only\n")
		output.Finish(nil, new(0))
		text := "Read `" + path + "`"
		view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: uint64(i + 1), Agent: "Main", Kind: "tool", Text: text, native: &liveActivityNativeItem{thread: "main", item: path, segments: []commandSegment{{text: text, output: output}}}}}})
	}
	feed := view.renderFeed(90, 30)
	u := &terminalUI{}
	for _, snippet := range feed.snippets {
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
	retention := new(activityui.Retention)
	first, second := retention.New(), retention.New()
	first.Write(strings.Repeat("first row\n", 50))
	first.Finish(nil, new(0))
	second.Write("second row\n")
	second.Finish(nil, new(0))
	u := dialogForOutput(first)
	u.output.pages = append(u.output.pages, activityui.Block{Verb: "Run", Output: second})
	drawOutputDialog(u)
	u.outputKey("\x1b[6~")
	top := u.output.top
	u.outputKey("/")
	u.outputKey("first")
	tab := u.output.tabs[1]
	u.outputMouse(0, u.output.rect.x+2+tab.from, u.output.rect.y+2, false)
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
		u := dialogForOutput(&activityui.Output{})
		u.output.pages[0].Body = "Dialog body"
		rows := make([]string, 20)
		u.paintOutput(rows, width, len(rows))
		d := u.output
		frame := ansi.Strip(strings.Join(rows, "\n"))
		if !strings.Contains(frame, activityui.DialogClose) {
			t.Fatalf("width %d: no close button: %s", width, frame)
		}
		u.outputMouse(0, d.rect.x+d.rect.w-6, d.rect.y, false)
		if u.output != nil {
			t.Fatalf("width %d: close button did not dismiss", width)
		}
	}
}

func TestCommandDurationRestoresAndStopsInDialog(t *testing.T) {
	v := newLiveActivityView()
	duration := int64(70000)
	item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "echo ok", DurationMS: &duration, Status: "completed"}
	v.applyAppServerItem(true, "/w", "main", "main", "turn", "cmd", "item/completed", "", item)
	b := v.entries[0].blocks[0]
	if b.Duration != 70*time.Second || b.Running {
		t.Fatalf("restored timing %+v", b)
	}
	p := v.painter.DialogPage(b, 80)
	if !strings.HasSuffix(ansi.Strip(p.Title), " · 1m10s") {
		t.Fatalf("title %q", p.Title)
	}
}

func TestRunDurationLiveAndCompletedRendering(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "timed", "type": "commandExecution", "command": "sleep 2", "status": "inProgress"}})
	index := -1
	for i, entry := range u.view.entries {
		if entry.native != nil && entry.native.item == "timed" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("missing running command")
	}
	u.view.entries[index].native.commandStarted = time.Now().Add(-2 * time.Second)
	u.view.entries[index].blocks = parseLiveActivity(u.view.entries[index].activityPaneEntry)
	u.view.runs = nil
	feed := u.view.renderFeed(100, 30)
	if !strings.Contains(ansi.Strip(strings.Join(feed.lines, "\n")), "2s") {
		t.Fatal("live command omitted elapsed")
	}
	opened := false
	for _, snippet := range feed.snippets {
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
			retention := new(activityui.Retention)
			first, second := retention.New(), retention.New()
			first.Write("first\n")
			first.Finish(nil, new(0))
			second.Write("second\n")
			entry := activityPaneEntry{Seq: 1, Agent: "Main", Kind: "tool", Text: "Run `echo first`\n\nRun `echo second`", Observed: time.Now().Add(-4 * time.Second), native: &liveActivityNativeItem{command: "echo first; echo second", running: true, output: second}}
			if segmented {
				entry.native.segments = []commandSegment{{text: "Run `echo first`", output: first}, {text: "Run `echo second`", output: second, running: true}}
			}
			view := newLiveActivityView()
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
			view.entries[0].native.running, view.entries[0].native.duration = false, 7*time.Second
			if segmented {
				view.entries[0].native.segments[1].running = false
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
	feed := u.agents.renderFeed(90, 30)
	opened := false
	for _, snippet := range feed.snippets {
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
	u.agents.entries = nil
	drawOutputDialog(u.shell)
	if !strings.Contains(d.laid.Text, "Final paragraph.") {
		t.Fatal("eviction lost the last available narrative")
	}
}
